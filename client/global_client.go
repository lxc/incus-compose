package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	incusClient "github.com/lxc/incus/v6/client"
	incusApi "github.com/lxc/incus/v6/shared/api"
)

// ClientConfig holds configuration options for the Client.
type ClientConfig struct {
	// URL is the Incus server URL to connect to.
	URL string

	// Logger to use within this client.
	Logger *slog.Logger

	// InsecureSkipVerify accepts any certificate (insecure, for testing only).
	InsecureSkipVerify bool

	// TLSClientCert is the path to TLS client certificate for authentication.
	TLSClientCert string

	// TLSClientKey is the path to TLS client key for authentication.
	TLSClientKey string

	// NetworkPrefix is the prefix for new networks (default: "ic-").
	NetworkPrefix string

	// DefaultStoragePool is the storage pool to use for volumes (default: "default").
	DefaultStoragePool string

	// DescriptionFormat is the format string for resource descriptions (default: "incus-compose: %s").
	DescriptionFormat string

	// ProvidedInstanceServer allows injecting an existing connection (for testing).
	ProvidedInstanceServer incusClient.InstanceServer

	// ProvidedImageCache allows injecting an existing image cache (for testing).
	ProvidedImageCache incusClient.InstanceServer

	// CacheProject is the project name to use as image cache.
	// If set, the project will be created if it doesn't exist.
	CacheProject string
}

// ClientOption is a functional option for configuring the Client.
type ClientOption func(*ClientConfig)

// ClientURL sets the Incus server ClientURL.
func ClientURL(u string) ClientOption {
	return func(c *ClientConfig) { c.URL = u }
}

// ClientLogger sets the client to use within the created client.
func ClientLogger(l *slog.Logger) ClientOption {
	return func(c *ClientConfig) { c.Logger = l }
}

// ClientInsecureSkipVerify disables TLS certificate verification.
func ClientInsecureSkipVerify() ClientOption {
	return func(c *ClientConfig) { c.InsecureSkipVerify = true }
}

// ClientTLSClientCert sets the path to the TLS client certificate.
func ClientTLSClientCert(f string) ClientOption {
	return func(c *ClientConfig) { c.TLSClientCert = f }
}

// ClientTLSClientKey sets the path to the TLS client key.
func ClientTLSClientKey(f string) ClientOption {
	return func(c *ClientConfig) { c.TLSClientKey = f }
}

// ClientDefaultStoragePool sets the default storage pool name.
func ClientDefaultStoragePool(n string) ClientOption {
	return func(c *ClientConfig) { c.DefaultStoragePool = n }
}

// ClientNetworkPrefix sets the prefix for network names.
func ClientNetworkPrefix(n string) ClientOption {
	return func(c *ClientConfig) { c.NetworkPrefix = n }
}

// ClientDescriptionFormat sets the format string for resource descriptions.
func ClientDescriptionFormat(n string) ClientOption {
	return func(c *ClientConfig) { c.DescriptionFormat = n }
}

// ClientProvideConnection injects existing connections (for testing).
func ClientProvideConnection(instances incusClient.InstanceServer) ClientOption {
	return func(c *ClientConfig) {
		c.ProvidedInstanceServer = instances
	}
}

// ClientProvideInstanceServer injects an existing instance server connection.
func ClientProvideInstanceServer(server incusClient.InstanceServer) ClientOption {
	return func(c *ClientConfig) {
		c.ProvidedInstanceServer = server
	}
}

// ClientCacheProject sets the project name to use as image cache.
func ClientCacheProject(n string) ClientOption {
	return func(c *ClientConfig) { c.CacheProject = n }
}

// GlobalClient provides a high-level interface to Incus operations.
type GlobalClient struct {
	Ctx    context.Context
	Config ClientConfig

	logger   *slog.Logger
	projects []*Client

	incus      *incusClient.ProtocolIncus
	imageCache incusClient.InstanceServer
	unix       bool
	connected  bool

	// hookBefore is called hookBefore any action.
	hookBefore func(action Action, r Resource, args Options, err error) error

	// hookAfter is called hookAfter any action.
	hookAfter func(action Action, r Resource, args Options, err error) error

	progressHandler func(action Action, r Resource, args Options, progress int)

	// outputHandler is called when a resource produces output (e.g., logs).
	outputHandler func(action Action, r Resource, data []byte)

	hookOperation       func(ctx context.Context, action Action, r Resource, args Options, op incusClient.Operation, err error) error
	hookRemoteOperation func(ctx context.Context, action Action, r Resource, args Options, op incusClient.RemoteOperation, err error) error
}

// New creates a new Client with the provided context and logger.
func New(ctx context.Context, opts ...ClientOption) *GlobalClient {
	config := ClientConfig{
		Logger:             slog.Default(),
		DefaultStoragePool: "default",
		NetworkPrefix:      "ic-",
		DescriptionFormat:  "incus-compose: %s",
	}

	for _, o := range opts {
		o(&config)
	}

	c := &GlobalClient{
		Ctx:    ctx,
		Config: config,
		logger: config.Logger,
	}

	c.hookBefore = func(action Action, r Resource, args Options, err error) error {
		return err
	}

	c.hookAfter = func(action Action, r Resource, args Options, err error) error {
		if cError, ok := err.(*Error); ok {
			return cError.WithResource(r)
		}

		return err
	}

	c.hookOperation = func(ctx context.Context, action Action, r Resource, args Options, op incusClient.Operation, err error) error {
		if err != nil {
			return err
		}

		if op == nil {
			return ErrNilPointer
		}

		if c.progressHandler != nil {
			_, err := op.AddHandler(func(opAPI incusApi.Operation) {
				c.reportProgress(action, r, args, opAPI)
			})
			if err != nil {
				return fmt.Errorf("adding a progress handler to %w", err)
			}
		}

		err = errors.Join(err, op.WaitContext(ctx))
		if err != nil {
			return ErrOperation.Wrap(err)
		}

		return nil
	}

	c.hookRemoteOperation = func(_ context.Context, action Action, r Resource, args Options, op incusClient.RemoteOperation, err error) error {
		// Note: ctx is accepted for API consistency but RemoteOperation doesn't support WaitContext
		if err != nil {
			return err
		}

		if op == nil {
			return ErrNilPointer
		}

		if c.progressHandler != nil {
			_, err := op.AddHandler(func(opAPI incusApi.Operation) {
				c.reportProgress(action, r, args, opAPI)
			})
			if err != nil {
				return fmt.Errorf("adding a progress handler to %w", err)
			}
		}

		err = errors.Join(err, op.Wait())
		if err != nil {
			return ErrOperation.Wrap(err)
		}

		return nil
	}

	return c
}

// Connect establishes a connection to the Incus server.
func (c *GlobalClient) Connect() error {
	if c.Config.ProvidedInstanceServer != nil {
		return c.connectProvided()
	}

	if c.Config.URL != "" && c.Config.TLSClientCert != "" && c.Config.TLSClientKey != "" {
		return c.connectTLS()
	}

	return errors.New("provide either URL with TLS certs or ProvidedInstanceServer")
}

func (c *GlobalClient) connectProvided() error {
	info, err := c.Config.ProvidedInstanceServer.GetConnectionInfo()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}

	c.Config.URL = info.URL
	c.unix = info.SocketPath != ""

	// Force "default" project for global client - project-scoped clients are created via EnsureProject.
	pIncus, ok := c.Config.ProvidedInstanceServer.UseProject("default").(*incusClient.ProtocolIncus)
	if !ok {
		return fmt.Errorf("%w: cannot cast to ProtocolIncus", ErrConnectionFailed)
	}

	c.incus = pIncus
	c.logger = c.logger.With("url", c.Config.URL)
	c.connected = true
	return c.setupImageCache()
}

func (c *GlobalClient) connectTLS() error {
	certPath, err := filepath.Abs(c.Config.TLSClientCert)
	if err != nil {
		return ErrConnectionFailed.WithText("while reading cert").Wrap(err)
	}

	certData, err := os.ReadFile(certPath)
	if err != nil {
		return ErrConnectionFailed.WithText("while reading cert").Wrap(err)
	}

	keyPath, err := filepath.Abs(c.Config.TLSClientKey)
	if err != nil {
		// Not wrapping "err" so we hide the key path.
		return ErrConnectionFailed.WithText(fmt.Sprintf("while reading key for cert %v", certPath))
	}

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		// Not wrapping "err" so we hide the key path.
		return ErrConnectionFailed.WithText(fmt.Sprintf("while reading key for cert %v", certPath))
	}

	args := &incusClient.ConnectionArgs{
		InsecureSkipVerify: c.Config.InsecureSkipVerify,
		AuthType:           "tls",
		TLSClientCert:      string(certData),
		TLSClientKey:       string(keyData),
	}

	incus, err := incusClient.ConnectIncusWithContext(c.Ctx, c.Config.URL, args)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}

	pIncus, ok := incus.(*incusClient.ProtocolIncus)
	if !ok {
		return fmt.Errorf("%w: cannot cast to ProtocolIncus", ErrConnectionFailed)
	}

	c.incus = pIncus
	c.unix = false
	c.logger = c.logger.With("url", c.Config.URL)
	c.connected = true
	return c.setupImageCache()
}

// setupImageCache configures the image cache based on CacheProject or defaults.
func (c *GlobalClient) setupImageCache() error {
	if c.Config.CacheProject != "" {
		// Use dedicated cache project (create if needed)
		cacheClient, err := c.EnsureProject(c.Config.CacheProject, true)
		if err != nil {
			return fmt.Errorf("ensuring cache project %s: %w", c.Config.CacheProject, err)
		}
		c.imageCache = cacheClient.incus
	} else if c.Config.ProvidedImageCache != nil {
		// Use provided cache (for testing)
		c.imageCache = c.Config.ProvidedImageCache.UseProject("default")
	} else {
		// Default: use "default" project as cache
		c.imageCache = c.incus.UseProject("default")
	}
	return nil
}

// LogError logs an error.
// The `any` here is ok.
func (c *GlobalClient) LogError(msg string, args ...any) {
	c.logger.ErrorContext(c.Ctx, msg, args...)
}

// IsDebugging returns true if debug logging is enabled.
func (c *GlobalClient) IsDebugging() bool {
	return c.logger.Enabled(c.Ctx, slog.LevelDebug)
}

// IsConnected returns true if the client is connected.
func (c *GlobalClient) IsConnected() bool {
	return c.connected
}

// IsRemote returns true if connected via network (not unix socket).
func (c *GlobalClient) IsRemote() bool {
	return !c.unix
}

func (c *GlobalClient) getProject(name string) (*Client, error) {
	incusName := sanitizeProjectName(name)

	_, _, err := c.incus.GetProject(incusName)
	if err != nil {
		return nil, err
	}

	c.logger.DebugContext(c.Ctx, "Got project", "name", name, "incus_name", incusName)
	return c.newClientProject(name, incusName, false)
}

func (c *GlobalClient) createProject(name string) (*Client, error) {
	incusName := sanitizeProjectName(name)

	// Create project
	err := c.incus.CreateProject(incusApi.ProjectsPost{
		Name: incusName,
		ProjectPut: incusApi.ProjectPut{
			Description: fmt.Sprintf(c.Config.DescriptionFormat, name),
			Config:      incusApi.ConfigMap{"features.profiles": "true"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("creating project %s (incus: %s): %w", name, incusName, err)
	}

	// c.logger.DebugContext(c.Ctx, "Created project", "name", name, "incus_name", incusName)
	return c.newClientProject(name, incusName, true)
}

// EnsureProject ensures a project exists and returns a ClientProject.
// Creates the project if create is true and it does not exist.
func (c *GlobalClient) EnsureProject(name string, create bool) (*Client, error) {
	if !c.connected {
		return nil, ErrDisconnected
	}

	// Check if already known
	for _, p := range c.projects {
		if p != nil && p.project == name {
			return p, nil
		}
	}

	p, err := c.getProject(name)
	if err == nil {
		return p, nil
	}

	if !create {
		return nil, ErrNotFound.WithKindName(KindProject, name).Wrap(err)
	}

	return c.createProject(name)
}

// DeleteProject deletes a project and removes it from the cache.
func (c *GlobalClient) DeleteProject(name string, force bool) error {
	if !c.connected {
		return ErrDisconnected
	}

	incusName := sanitizeProjectName(name)

	// c.logger.DebugContext(c.Ctx, "Deleting project", "name", name, "incus_name", incusName)

	// Delete networks first - they are global, not project-scoped
	for _, p := range c.projects {
		if p.project == name {
			// Delete networks first - they are global, not project-scoped
			networks, err := ByKind[*Network](p.resources.All(), KindNetwork)
			if err != nil {
				continue
			}

			for _, n := range networks {
				_ = RunAction(n, ActionDelete, OptionForce())
			}
			break
		}
	}

	var err error
	if force {
		err = c.incus.DeleteProjectForce(incusName)
	} else {
		err = c.incus.DeleteProject(incusName)
	}
	if err != nil {
		return fmt.Errorf("deleting project %s (incus: %s): %w", name, incusName, err)
	}

	// Remove from cache
	for i, p := range c.projects {
		if p.project == name {
			c.projects = append(c.projects[:i], c.projects[i+1:]...)
			break
		}
	}

	return nil
}

func (c *GlobalClient) reportProgress(action Action, r Resource, args Options, opAPI incusApi.Operation) {
	if c.progressHandler == nil {
		return
	}

	// Extract progress from operation metadata
	// Incus stores progress in Metadata with keys ending in "_progress"
	percent := -1

	if opAPI.Metadata != nil {
		for key, val := range opAPI.Metadata {
			if strings.HasSuffix(key, "_progress") {
				if s, ok := val.(string); ok {
					// Native Incus images: "rootfs: 42% (3.10MB/s)" or "metadata: 100% (876B/s)"
					// OCI images: "Retrieving OCI image from registry"
					// Find percentage pattern anywhere in the string
					if idx := strings.Index(s, "%"); idx > 0 {
						// Walk backwards from % to find the start of the number
						start := idx - 1
						for start >= 0 && s[start] >= '0' && s[start] <= '9' {
							start--
						}
						start++ // Move past the non-digit
						if start < idx {
							if _, err := fmt.Sscanf(s[start:], "%d%%", &percent); err != nil {
								percent = -1
							}
						}
					}
				}
				break
			}
		}
	}

	c.progressHandler(action, r, args, percent)
}

// AddHookBefore adds a hook that will be executed before any action (FIFO order).
// You may use it for abort control.
func (c *GlobalClient) AddHookBefore(hook func(action Action, r Resource, args Options, err error) error) {
	prevHook := c.hookBefore
	newHook := func(action Action, r Resource, args Options, err error) error {
		// Run previous hooks FIRST (FIFO)
		if err := prevHook(action, r, args, err); err != nil {
			return err
		}
		// Then run the new hook
		return hook(action, r, args, nil)
	}

	c.hookBefore = newHook
}

// AddHookAfter adds a hook that will be executed after any action (LIFO order).
func (c *GlobalClient) AddHookAfter(hook func(action Action, r Resource, args Options, err error) error) {
	prevHook := c.hookAfter
	newHook := func(action Action, r Resource, args Options, err error) error {
		// Run new hook FIRST, then pass result to previous hooks (LIFO)
		err = hook(action, r, args, err)
		return prevHook(action, r, args, err)
	}

	c.hookAfter = newHook
}

// SetOutputHandler sets the handler for resource output (e.g., logs).
// The handler receives raw bytes - formatting is the caller's responsibility.
func (c *GlobalClient) SetOutputHandler(handler func(action Action, r Resource, data []byte)) {
	c.outputHandler = handler
}
