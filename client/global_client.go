package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"

	incusClient "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/cliconfig"
)

// ClientConfig holds configuration options for the Client.
type ClientConfig struct {
	// URL is the Incus server URL to connect to.
	URL string

	// Logger to use within this client.
	Logger *slog.Logger

	// NetworkPrefix is the prefix for new networks (default: "ic-").
	NetworkPrefix string

	// DefaultStoragePool is the storage pool to use for volumes (default: "default").
	DefaultStoragePool string

	// DescriptionFormat is the format string for resource descriptions (default: "incus-compose: %s").
	DescriptionFormat string

	// ProvidedInstanceServer allows injecting an existing connection (for testing).
	ProvidedInstanceServer incusClient.InstanceServer

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
	ctx    context.Context
	Config ClientConfig

	logger    *slog.Logger
	projects  []*Client
	cliConfig *cliconfig.Config

	incus      *incusClient.ProtocolIncus
	imageCache incusClient.InstanceServer
	unix       bool
	connected  bool

	// Cache for ConnectionIP()
	connectionIP string

	// hookBefore is called hookBefore any action.
	hookBefore func(ctx context.Context, action Action, r Resource, args Options, err error) error

	// hookAfter is called hookAfter any action.
	hookAfter func(ctx context.Context, action Action, r Resource, args Options, err error) error

	progressHandler func(action Action, r Resource, args Options, p Progress)

	// outputHandler is called when a resource produces output (e.g., logs).
	outputHandler func(action Action, r Resource, data []byte)

	hookOperation       func(ctx context.Context, action Action, r Resource, args Options, op incusClient.Operation, err error) error
	hookRemoteOperation func(ctx context.Context, action Action, r Resource, args Options, op incusClient.RemoteOperation, err error) error
}

// New creates a new Client with the provided context and logger.
func New(ctx context.Context, opts ...ClientOption) *GlobalClient {
	config := ClientConfig{
		Logger:             slog.Default(),
		DefaultStoragePool: "detect",
		NetworkPrefix:      "ic-",
		DescriptionFormat:  "incus-compose: %s",
	}

	for _, o := range opts {
		o(&config)
	}

	// Load CLI config by default for image server resolution
	cliConf, _ := cliconfig.LoadConfig(cliconfig.DefaultConfig().ConfigDir)

	c := &GlobalClient{
		ctx:       ctx,
		Config:    config,
		logger:    config.Logger,
		cliConfig: cliConf,
	}

	c.hookBefore = func(_ context.Context, action Action, r Resource, args Options, err error) error {
		return err
	}

	c.hookAfter = func(_ context.Context, action Action, r Resource, args Options, err error) error {
		if err == nil {
			return nil
		}
		if cError, ok := err.(*Error); ok {
			return cError.WithResource(r)
		}
		return ErrUnknown.WithResource(r).Wrap(err)
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
				return ErrOperation.Wrap(err)
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
				return ErrOperation.Wrap(err)
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

// NewTestClient creates a new GlobalClient for testing.
func NewTestClient(ctx context.Context) (*GlobalClient, error) {
	var logger *slog.Logger

	logFormat, ok := os.LookupEnv("LOG_FORMAT")
	if !ok {
		logFormat = "text"
	}

	switch logFormat {
	case "json":
		logger = slog.New(slog.NewJSONHandler(
			os.Stderr,
			&slog.HandlerOptions{Level: slog.LevelDebug - 4}),
		)
	default:
		logger = slog.New(slog.NewTextHandler(
			os.Stderr,
			&slog.HandlerOptions{Level: slog.LevelDebug - 4}),
		)
	}

	slog.SetDefault(logger)

	// Priority: INCUS_REMOTE -> INCUS_COMPOSE_URL -> "local" remote
	var opts []ClientOption

	// 1. If INCUS_REMOTE is set, use Incus CLI config
	if remote, ok := os.LookupEnv("INCUS_REMOTE"); ok {
		slog.DebugContext(ctx, "Connecting", "remote", remote)

		conf, err := cliconfig.LoadConfig("")
		if err != nil {
			return nil, ErrConnectionFailed.Wrap(err)
		}

		server, err := conf.GetInstanceServer(remote)
		if err != nil {
			return nil, ErrConnectionFailed.Wrap(err)
		}

		opts = []ClientOption{
			ClientLogger(logger),
			ClientProvideConnection(server),
		}
	} else {
		// 3. Fall back to "local" remote
		slog.DebugContext(ctx, "Connecting", "remote", "local")

		conf, err := cliconfig.LoadConfig("")
		if err != nil {
			return nil, ErrConnectionFailed.Wrap(err)
		}

		server, err := conf.GetInstanceServer("local")
		if err != nil {
			return nil, ErrConnectionFailed.Wrap(err)
		}

		opts = []ClientOption{
			ClientLogger(logger),
			ClientProvideConnection(server),
		}
	}

	// Use own cache project for tests.
	opts = append(opts, ClientCacheProject("incus-compose-tests-cache"))

	c := New(ctx, opts...)
	if err := c.Connect(); err != nil {
		return nil, err
	}

	return c, nil
}

// NewOfflineClient creates a disconnected project client for resource planning.
// It can create in-memory resources, but cannot run Incus operations.
func NewOfflineClient(ctx context.Context, name string) *Client {
	gc := New(ctx)
	config := gc.Config
	config.DescriptionFormat = fmt.Sprintf(config.DescriptionFormat, name) + ":%s"

	return &Client{
		ctx:          ctx,
		globalClient: gc,
		config:       config,
		project:      name,
		incusProject: sanitizeProjectName(name),
		logger:       gc.logger.With("project", name),
	}
}

// Connect establishes a connection to the Incus server.
func (c *GlobalClient) Connect() error {
	if c.Config.ProvidedInstanceServer == nil {
		return errors.New("provide a ProvidedInstanceServer")
	}

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
	c.connected = true

	c.logger.Debug("Connected")

	if c.Config.DefaultStoragePool == "detect" {
		if err = c.detectStoragePool(); err != nil {
			return err
		}
	}

	return c.setupImageCache()
}

// Connection returns the project-scoped Connection client.
func (c *GlobalClient) Connection() *incusClient.ProtocolIncus {
	return c.incus
}

// detectStoragePool gets the first storage pool from the incus server.
func (c *GlobalClient) detectStoragePool() error {
	names, err := c.incus.GetStoragePoolNames()
	if err != nil {
		return fmt.Errorf("detecting storage pool: %w", err)
	}

	if len(names) == 0 {
		return fmt.Errorf("detecting storage pool: no storage pools found on server")
	}

	c.Config.DefaultStoragePool = names[0]
	return nil
}

// setupImageCache configures the image cache based on CacheProject or defaults.
func (c *GlobalClient) setupImageCache() error {
	if c.Config.CacheProject != "" {
		// Use dedicated cache project (create if needed)
		cacheClient, err := c.EnsureProject(c.Config.CacheProject, EnsureProjectWithCreate())
		if err != nil {
			return fmt.Errorf("ensuring cache project %s: %w", c.Config.CacheProject, err)
		}
		c.imageCache = cacheClient.incus
	} else {
		// Default: use "default" project as cache
		c.imageCache = c.incus.UseProject("default")
	}
	return nil
}

// LogError logs an error.
// The `any` here is ok.
func (c *GlobalClient) LogError(msg string, args ...any) {
	c.logger.ErrorContext(c.ctx, msg, args...)
}

// LogWarn logs a warning.
// The `any` here is ok.
func (c *GlobalClient) LogWarn(msg string, args ...any) {
	c.logger.WarnContext(c.ctx, msg, args...)
}

// LogDebug logs a debug message.
// The `any` here is ok.
func (c *GlobalClient) LogDebug(msg string, args ...any) {
	c.logger.Log(c.ctx, slog.LevelDebug, msg, args...)
}

// IsDebugging returns true if debug logging is enabled.
func (c *GlobalClient) IsDebugging() bool {
	return c.logger.Enabled(c.ctx, slog.LevelDebug)
}

// IsConnected returns true if the client is connected.
func (c *GlobalClient) IsConnected() bool {
	return c.connected
}

// IsRemote returns true if connected via network (not unix socket).
func (c *GlobalClient) IsRemote() bool {
	return !c.unix
}

// CliConfig returns the CLI config for image server resolution.
func (c *GlobalClient) CliConfig() *cliconfig.Config {
	return c.cliConfig
}

func (c *GlobalClient) getProject(name string) (*Client, error) {
	incusName := sanitizeProjectName(name)

	_, _, err := c.incus.GetProject(incusName)
	if err != nil {
		return nil, err
	}

	c.logger.DebugContext(c.ctx, "Got project", "name", name, "incus_name", incusName)
	return c.newProjectClient(name, incusName, false)
}

// EnsureProjectOption is a functional option for configuring project creation.
type EnsureProjectOption func(*ensureProjectOptions)

type ensureProjectOptions struct {
	create bool
	config map[string]string
}

// EnsureProjectWithCreate enables project creation if the project doesn't exist.
func EnsureProjectWithCreate() EnsureProjectOption {
	return func(opts *ensureProjectOptions) {
		opts.create = true
	}
}

// EnsureProjectWithConfig sets configuration options to apply when creating the project.
// These options are merged with default features configuration.
func EnsureProjectWithConfig(config map[string]string) EnsureProjectOption {
	return func(opts *ensureProjectOptions) {
		opts.config = config
	}
}

func (c *GlobalClient) createProject(name string, config map[string]string) (*Client, error) {
	incusName := sanitizeProjectName(name)

	// Merge user-provided config with defaults
	projectConfig := incusApi.ConfigMap{"features.profiles": "true"}
	for k, v := range config {
		projectConfig[k] = v
	}

	// Create project
	err := c.incus.CreateProject(incusApi.ProjectsPost{
		Name: incusName,
		ProjectPut: incusApi.ProjectPut{
			Description: fmt.Sprintf(c.Config.DescriptionFormat, name),
			Config:      projectConfig,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("creating project %s (incus: %s): %w", name, incusName, err)
	}

	// c.logger.DebugContext(c.Ctx, "Created project", "name", name, "incus_name", incusName)
	return c.newProjectClient(name, incusName, true)
}

// EnsureProject ensures a project exists and returns a Client for it.
// Options control whether the project is created if it doesn't exist and what config to apply.
func (c *GlobalClient) EnsureProject(name string, opts ...EnsureProjectOption) (*Client, error) {
	if !c.connected {
		return nil, ErrDisconnected
	}

	// Apply options
	options := &ensureProjectOptions{}
	for _, opt := range opts {
		opt(options)
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

	if !options.create {
		return nil, ErrNotFound.WithKindName(KindProject, name).Wrap(err)
	}

	return c.createProject(name, options.config)
}

// DeleteProject deletes a project and removes it from the cache.
func (c *GlobalClient) DeleteProject(name string, force bool) error {
	if !c.connected {
		return ErrDisconnected
	}

	incusName := sanitizeProjectName(name)

	// c.logger.DebugContext(c.Ctx, "Deleting project", "name", name, "incus_name", incusName)

	var err error
	if force {
		err = c.incus.DeleteProjectForce(incusName)
	} else {
		err = c.incus.DeleteProject(incusName)
	}
	if err != nil {
		return err
	}

	// Delete networks - they are global, not project-scoped
	for _, p := range c.projects {
		if p.project == name {
			// Delete networks first - they are global, not project-scoped
			networks, err := ByKind[*Network](p.resources.All(), KindNetwork)
			if err != nil {
				c.LogWarn("Deleting networks", "error", err)
				continue
			}

			for _, n := range networks {
				err = RunAction(c.ctx, n, ActionDelete, OptionForce())
				if err != nil {
					c.LogWarn("Deleting network", "network", n, "error", err)
				}
			}
			break
		}
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

// Progress describes the live state of a long-running resource operation.
//
// Native Incus images report a real percentage ("rootfs: 42% (3.10MB/s)"),
// so Percent is set. OCI image pulls only emit status text ("Retrieving OCI
// image from registry"); for those Percent is -1 and only Text is meaningful,
// because the registry download runs as an opaque skopeo subprocess with no
// byte or percentage feedback.
type Progress struct {
	// Percent is 0-100, or -1 when the operation reports no percentage.
	Percent int

	// Text is the raw status text from Incus, empty when none was reported.
	Text string
}

func (c *GlobalClient) reportProgress(action Action, r Resource, args Options, opAPI incusApi.Operation) {
	if c.progressHandler == nil {
		return
	}

	// Incus stores progress in Metadata with keys ending in "_progress".
	p := Progress{Percent: -1}

	if opAPI.Metadata != nil {
		for key, val := range opAPI.Metadata {
			if strings.HasSuffix(key, "_progress") {
				if s, ok := val.(string); ok {
					p.Text = s
					p.Percent = parsePercent(s)
				}
				break
			}
		}
	}

	c.progressHandler(action, r, args, p)
}

// parsePercent extracts a "NN%" value from an Incus progress string, returning
// -1 when none is present (as with OCI status text). It finds the percent sign
// anywhere in the string, e.g. "rootfs: 42% (3.10MB/s)" -> 42.
func parsePercent(s string) int {
	idx := strings.Index(s, "%")
	if idx <= 0 {
		return -1
	}

	// Walk backwards from % to the start of the number.
	start := idx - 1
	for start >= 0 && s[start] >= '0' && s[start] <= '9' {
		start--
	}
	start++ // Move past the non-digit.
	if start >= idx {
		return -1
	}

	percent := -1
	if _, err := fmt.Sscanf(s[start:], "%d%%", &percent); err != nil {
		return -1
	}
	return percent
}

// AddHookBefore adds a hook that will be executed before any action (FIFO order).
// You may use it for abort control.
func (c *GlobalClient) AddHookBefore(hook func(ctx context.Context, action Action, r Resource, args Options, err error) error) {
	prevHook := c.hookBefore
	newHook := func(ctx context.Context, action Action, r Resource, args Options, err error) error {
		// Run previous hooks FIRST (FIFO)
		if err := prevHook(ctx, action, r, args, err); err != nil {
			return err
		}
		// Then run the new hook
		return hook(ctx, action, r, args, nil)
	}

	c.hookBefore = newHook
}

// AddHookAfter adds a hook that will be executed after any action (LIFO order).
func (c *GlobalClient) AddHookAfter(hook func(ctx context.Context, action Action, r Resource, args Options, err error) error) {
	prevHook := c.hookAfter
	newHook := func(ctx context.Context, action Action, r Resource, args Options, err error) error {
		// Run new hook FIRST, then pass result to previous hooks (LIFO)
		err = hook(ctx, action, r, args, err)
		return prevHook(ctx, action, r, args, err)
	}

	c.hookAfter = newHook
}

// SetOutputHandler sets the handler for resource output (e.g., logs).
// The handler receives raw bytes - formatting is the caller's responsibility.
func (c *GlobalClient) SetOutputHandler(handler func(action Action, r Resource, data []byte)) {
	c.outputHandler = handler
}

// SetProgressHandler sets the handler for live operation progress. Pass nil to
// disable. Operations run in parallel, so the handler may be called
// concurrently and must be safe for concurrent use.
func (c *GlobalClient) SetProgressHandler(handler func(action Action, r Resource, args Options, p Progress)) {
	c.progressHandler = handler
}

// NetworkBridgeIPs returns the IPv4 and IPv6 bridge addresses of an Incus network.
// The addresses are returned without CIDR notation.
// Addresses for which the network config key is absent or set to "none" are omitted.
func (c *GlobalClient) NetworkBridgeIPs(networkName string) (ipv4 []string, ipv6 []string, err error) {
	network, _, err := c.incus.GetNetwork(networkName)
	if err != nil {
		return nil, nil, fmt.Errorf("getting network %s: %w", networkName, err)
	}

	if v := network.Config["ipv4.address"]; v != "" && v != "none" {
		ip, _, err := net.ParseCIDR(v)
		if err == nil {
			ipv4 = append(ipv4, ip.String())
		}
	} else if v := network.Config["ipv6.address"]; v != "" && v != "none" {
		ip, _, err := net.ParseCIDR(v)
		if err == nil {
			ipv6 = append(ipv6, ip.String())
		}
	}

	return ipv4, ipv6, nil
}

// ConnectionIP returns the IP of the current connection,
// this function is cached by GlobalClient.connectionIP.
func (c *GlobalClient) ConnectionIP() (string, error) {
	if !c.IsRemote() {
		return "", errors.New("Client.ConnectionIP needs a non unix connection")
	}

	if c.connectionIP != "" {
		return c.connectionIP, nil
	}

	u, err := url.Parse(c.Config.URL)
	if err != nil {
		return "", fmt.Errorf("while parsing url %q: %w", c.Config.URL, err)
	}

	if net.ParseIP(u.Hostname()) != nil {
		c.connectionIP = u.Hostname()
		return c.connectionIP, nil
	}

	ip, err := net.LookupIP(u.Hostname())
	if err != nil {
		return "", fmt.Errorf("while looking up the IP for %q: %w", c.Config.URL, err)
	}

	c.connectionIP = ip[0].String()
	return c.connectionIP, nil
}

// NetworkForIP lookups the incus network for the given ip.
func (c *GlobalClient) NetworkForIP(ip string) (string, error) {
	isV4 := net.ParseIP(ip).To4() != nil

	networks, err := c.incus.GetNetworks()
	if err != nil {
		return "", fmt.Errorf("GetNetworks: %w", err)
	}

	for _, network := range networks {
		if isV4 && network.Config["ipv4.address"] == ip {
			return network.Name, nil
		}

		if !isV4 && network.Config["ipv6.address"] == ip {
			return network.Name, nil
		}
	}

	return "", fmt.Errorf("network with ip %q not found", ip)
}
