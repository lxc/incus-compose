// Package client provides a high-level wrapper around the Incus client library.
//
// This package abstracts the complexities of interacting with Incus servers and provides
// a compose-spec friendly interface for managing instances, networks, volumes, and projects.
//
// Key features:
//   - Project-aware client with automatic name sanitization
//   - OCI image management with automatic copying and caching
//   - Network management with deterministic naming
//   - Storage volume management with UID/GID shifting
//   - Service-to-instance translation from compose-spec
package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	incusClient "github.com/lxc/incus/v6/client"
	incusApi "github.com/lxc/incus/v6/shared/api"
)

// clientKey is a context key for storing the Client in context.Context.
type clientKey struct{}

// Config holds configuration options for the Client.
type Config struct {
	// URL is the Incus server URL to connect to
	URL string

	// InsecureSkipVerify accepts any certificate (insecure, for testing only)
	InsecureSkipVerify bool

	// TLSClientCert is the path to TLS client certificate for authentication
	TLSClientCert string

	// TLSClientKey is the path to TLS client key for authentication
	TLSClientKey string

	// NetworkPrefix is the prefix for new networks (default: "ic-")
	NetworkPrefix string

	// DefaultStoragePool is the storage pool to use for volumes (default: "default")
	DefaultStoragePool string

	// DescriptionFormat is the format string for resource descriptions (default: "incus-compose: %s")
	DescriptionFormat string

	TraceLevel slog.Level

	ProvidedInstanceServer incusClient.InstanceServer
	ProvidedImageCache     incusClient.InstanceServer
}

// Option is a functional option for configuring the Client.
type Option func(*Config)

// URL sets the URL to connect to.
func URL(u string) Option {
	return func(c *Config) {
		c.URL = u
	}
}

// InsecureSkipVerify enables skipping TLS certificate verification (insecure, for testing only).
func InsecureSkipVerify() Option {
	return func(c *Config) {
		c.InsecureSkipVerify = true
	}
}

// TLSClientCert sets the path to the TLS client certificate.
func TLSClientCert(f string) Option {
	return func(c *Config) {
		c.TLSClientCert = f
	}
}

// TLSClientKey sets the path to the TLS client key.
func TLSClientKey(f string) Option {
	return func(c *Config) {
		c.TLSClientKey = f
	}
}

// DefaultStoragePool sets the default storage pool name for volumes.
func DefaultStoragePool(n string) Option {
	return func(c *Config) {
		c.DefaultStoragePool = n
	}
}

// NetworkPrefix sets the prefix for network names.
func NetworkPrefix(n string) Option {
	return func(c *Config) {
		c.NetworkPrefix = n
	}
}

// DescriptionFormat sets the format string for resource descriptions.
func DescriptionFormat(n string) Option {
	return func(c *Config) {
		c.DescriptionFormat = n
	}
}

// TraceLevel sets the log level used for tracing.
func TraceLevel(n slog.Level) Option {
	return func(c *Config) {
		c.TraceLevel = n
	}
}

// ProvideConnection overrides internal connection management with the given connection.
func ProvideConnection(instances incusClient.InstanceServer, cache incusClient.InstanceServer) Option {
	return func(c *Config) {
		c.ProvidedInstanceServer = instances
		c.ProvidedImageCache = cache
	}
}

// Client provides a high-level interface to Incus operations.
// It wraps the Incus client library and provides project-aware operations,
// automatic name sanitization, and compose-spec service management.
type Client struct {
	// Ctx is the context for operations
	Ctx context.Context

	// Config holds the client configuration
	Config Config

	// logger is the structured logger
	logger *slog.Logger

	t        *transaction
	projects []*ClientProject

	// global incus client
	incus *incusClient.ProtocolIncus

	// image cache
	// Used to copy images to and create instances from.
	imageCache incusClient.InstanceServer

	// Unix indicates whatever the client is using a unix socket to connect.
	unix bool

	// connected tracks whether Connect() has been called successfully
	connected bool
}

// New creates a new Client instance with the provided context and logger.
// The client must call Connect() before performing any operations.
func New(ctx context.Context, logger *slog.Logger, opts ...Option) *Client {
	// Setup defaults
	config := Config{
		DefaultStoragePool: "default",
		NetworkPrefix:      "ic-",
		DescriptionFormat:  "incus-compose: %s",
		TraceLevel:         slog.LevelDebug - 4,
	}

	// Apply Options
	for _, o := range opts {
		o(&config)
	}

	c := &Client{
		Ctx:    ctx,
		Config: config,
		logger: logger,
		t:      newTransaction(),
	}

	return c
}

// Connect establishes a connection to the Incus server.
// This must be called before any other operations.
func (c *Client) Connect() error {
	args := &incusClient.ConnectionArgs{
		InsecureSkipVerify: c.Config.InsecureSkipVerify,
		AuthType:           "tls",
	}

	var incus incusClient.InstanceServer
	var err error
	var info *incusClient.ConnectionInfo

	if c.Config.ProvidedInstanceServer != nil && c.Config.ProvidedImageCache != nil {
		info, err = c.Config.ProvidedInstanceServer.GetConnectionInfo()
		if err != nil {
			return fmt.Errorf("%w: %w", ErrConnectionFailed, err)
		}
		c.Config.URL = info.URL

		c.unix = false
		if info.SocketPath != "" {
			c.unix = true
		}

		pIncus, ok := c.Config.ProvidedInstanceServer.(*incusClient.ProtocolIncus)
		if !ok {
			return fmt.Errorf("%w: cant cast the provided `ExistingConnection` to ProtocolIncus", ErrConnectionFailed)
		}
		c.incus = pIncus

		c.imageCache = c.Config.ProvidedImageCache

	} else if c.Config.URL != "" && c.Config.TLSClientCert != "" && c.Config.TLSClientKey != "" {
		// Read TLS client certificate and key files if provided
		certPath, err := filepath.Abs(c.Config.TLSClientCert)
		if err != nil {
			return fmt.Errorf("%w: failed to find the TLS client cert '%s': %w", ErrConnectionFailed, c.Config.TLSClientCert, err)
		}
		certData, err := os.ReadFile(certPath)
		if err != nil {
			return fmt.Errorf("%w: failed to read the TLS client cert '%s': %w", ErrConnectionFailed, certPath, err)
		}

		keyPath, err := filepath.Abs(c.Config.TLSClientKey)
		if err != nil {
			return fmt.Errorf("%w: failed to find the TLS client key for '%s': %w", ErrConnectionFailed, c.Config.TLSClientCert, err)
		}
		keyData, err := os.ReadFile(keyPath)
		if err != nil {
			return fmt.Errorf("%w: failed to read the TLS client key for cert '%s': %w", ErrConnectionFailed, certPath, err)
		}

		args.TLSClientCert = string(certData)
		args.TLSClientKey = string(keyData)

		incus, err = incusClient.ConnectIncusWithContext(c.Ctx, c.Config.URL, args)
		if err != nil {
			return fmt.Errorf("%w: failed to connect to Incus URL: %w", ErrConnectionFailed, c.Config.URL, err)
		}

		c.unix = false

		pIncus, ok := incus.(*incusClient.ProtocolIncus)
		if !ok {
			return fmt.Errorf("%w: cant cast the InstanceServer from `incusClient.ConnectIncusWithContext` to ProtocolIncus", ErrConnectionFailed)
		}

		c.incus = pIncus
		c.imageCache = incus
	} else {
		return errors.New("Either provide an URL() or ProvidedConnection() as option")
	}

	c.logger = c.logger.With("url", c.Config.URL)

	c.connected = true
	return nil
}

// IsDebugging returns true if debug logging is enabled.
func (c *Client) IsDebugging() bool {
	return c.logger.Enabled(c.Ctx, slog.LevelDebug)
}

// Logger returns the structured logger instance.
func (c *Client) Logger() *slog.Logger {
	return c.logger
}

// IsRemote returns if the client is running on a remote connection.
func (c *Client) IsRemote() bool {
	return !c.unix
}

// FromContext returns the client from the context.
func FromContext(ctx context.Context) (*Client, error) {
	c, ok := ctx.Value(clientKey{}).(*Client)
	if !ok {
		return nil, errors.New("failed to get the client from the context")
	}

	return c, nil
}

// ToContext injects the client into a context.
func (c *Client) ToContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, clientKey{}, c)
}

// EnsureProject ensures a project exists and returns a ClientProject.
// Creates the project if create is true and it doesn't exist.
// Automatically sanitizes the project name for Incus compatibility.
func (c *Client) EnsureProject(project string, create bool) (*ClientProject, error) {
	if !c.connected {
		return nil, ErrDisconnected
	}

	// Already known?
	for _, p := range c.projects {
		if p.Name() == project {
			return p, nil
		}
	}

	incusProject := sanitizeProjectName(project)

	// Check if project exists
	projectNames, err := c.incus.GetProjectNames()
	if err != nil {
		c.logger.Debug("Failed to fetch project names", slog.Any("error", err))
		return nil, fmt.Errorf("fetching project names: %w", err)
	}
	if slices.Contains(projectNames, incusProject) {
		p, err := newClientProject(c, project, incusProject)
		if err != nil {
			return nil, err
		}

		c.projects = append(c.projects, p)
		return p, nil
	}

	if !create {
		return nil, fmt.Errorf("project %q sanitized to %q does not exist", project, incusProject)
	}

	// Create project
	projectArgs := incusApi.ProjectsPost{
		Name: incusProject,
		ProjectPut: incusApi.ProjectPut{
			Description: fmt.Sprintf("incus-compose: %s", project),
			Config:      incusApi.ConfigMap{"features.profiles": "true"},
		},
	}

	err = c.incus.CreateProject(projectArgs)
	if err != nil {
		return nil, fmt.Errorf("creating project %q sanitized to %q: %w", project, incusProject, err)
	}

	p, err := newClientProject(c, project, incusProject)
	if err != nil {
		return nil, err
	}

	c.t.Add(p)
	c.projects = append(c.projects, p)

	return p, nil
}

// Errors returns all errors from all projects.
func (c *Client) Errors() error {
	var jErr error

	for _, p := range c.projects {
		jErr = errors.Join(jErr, p.Errors())
	}

	return jErr
}

// CreateErrors returns only creation-related errors from all projects.
func (c *Client) CreateErrors() error {
	var jErr error

	for _, p := range c.projects {
		jErr = errors.Join(jErr, p.CreateErrors())
	}

	return jErr
}

// Rollback removes all created resources in reverse priority order.
// Higher priority resources are deleted first.
func (c *Client) Rollback(timeout int) error {
	var jErr error

	// Rollback created projects
	deleted, err := c.t.Rollback(timeout)
	jErr = errors.Join(jErr, err)

	// Remove deleted projects
	for _, n := range deleted {
		idx := slices.IndexFunc(c.projects, func(p *ClientProject) bool { return p.Name() == n })
		if idx != -1 {
			c.projects[idx] = nil
		}
	}

	// Rollback created resource per projects
	for _, p := range c.projects {
		if p != nil {
			jErr = errors.Join(jErr, p.Rollback(timeout))
		}
	}

	return jErr
}

// StoreAble defines resources that can be stored in a ResourceStore.
type StoreAble interface {
	Name() string
}

// ResourceStore provides a generic store for named resources.
type ResourceStore[T StoreAble] struct {
	resources []T
}

// Add appends a resource to the store and returns it.
func (s *ResourceStore[T]) Add(r T) T {
	s.resources = append(s.resources, r)
	return r
}

// Get retrieves a resource by name. Returns the resource and true if found.
func (s *ResourceStore[T]) Get(name string) (T, bool) {
	idx := slices.IndexFunc(s.resources, func(r T) bool { return r.Name() == name })
	if idx == -1 {
		var zero T
		return zero, false
	}

	return s.resources[idx], true
}

// ClientProject wraps a project-scoped Incus client with resource management.
// It tracks all resources created within the project for rollback support.
type ClientProject struct {
	client *Client

	config Config

	name      string
	incusName string

	// Transaction
	t *transaction

	// Ctx is the context from the client.
	Ctx context.Context

	// logger is the structured logger
	// It uses the the clients logger with additional fields
	logger *slog.Logger

	// incus is the project-scoped Incus client
	incus *incusClient.ProtocolIncus

	// image cache server from Client
	imageCache incusClient.InstanceServer

	profiles    ResourceStore[*Profile]
	images      ResourceStore[*Image]
	poolVolumes ResourceStore[*PoolVolume]
	networks    ResourceStore[*Network]
	instances   ResourceStore[*Instance]
}

func newClientProject(client *Client, name string, incusName string) (*ClientProject, error) {
	incus := client.incus.UseProject(incusName)
	pIncus, ok := incus.(*incusClient.ProtocolIncus)
	if !ok {
		return nil, errors.New("cant cast the project scoped InstanceServer to ProtocolInucs")
	}

	p := &ClientProject{
		client:     client,
		config:     client.Config,
		name:       name,
		incusName:  incusName,
		t:          newTransaction(),
		Ctx:        client.Ctx,
		logger:     client.logger.With("project", name),
		incus:      pIncus,
		imageCache: client.imageCache,

		profiles:    ResourceStore[*Profile]{},
		images:      ResourceStore[*Image]{},
		poolVolumes: ResourceStore[*PoolVolume]{},
		networks:    ResourceStore[*Network]{},
		instances:   ResourceStore[*Instance]{},
	}

	if client.IsDebugging() {
		p.logger = p.logger.With("incus_project", incusName)
	}

	return p, nil
}

// Name returns the unsanitized project name.
func (c *ClientProject) Name() string {
	return c.name
}

// IncusName returns the sanitized Incus project name.
func (c *ClientProject) IncusName() string {
	return c.incusName
}

// Kind returns the resource kind.
func (c *ClientProject) Kind() string {
	return "project"
}

// Logger returns the project-scoped logger.
func (c *ClientProject) Logger() *slog.Logger {
	return c.logger
}

// Incus returns the project-scoped Incus client.
// This client operates within the current project set by UseProject().
func (c *ClientProject) Incus() *incusClient.ProtocolIncus {
	return c.incus
}

// GlobalIncus returns the non-project-scoped Incus client.
// Use this when operations need to access global resources or other projects.
func (c *ClientProject) GlobalIncus() *incusClient.ProtocolIncus {
	return c.client.incus
}

// IsDebugging returns true if debug logging is enabled.
func (c *ClientProject) IsDebugging() bool {
	return c.client.IsDebugging()
}

// Delete removes the project from Incus.
func (c *ClientProject) Delete(_ int, _ bool) error {
	err := c.client.incus.DeleteProject(c.name)
	if err != nil {
		return fmt.Errorf("failed to delete project %q: %w", c.name, err)
	}

	return nil
}

// Priority returns the deletion priority for Project resources.
func (*ClientProject) Priority() int {
	return PriorityProject
}

// Errors returns all errors from this project's transaction.
func (c *ClientProject) Errors() error {
	return c.t.Errors()
}

// CreateErrors returns only creation-related errors from this project.
func (c *ClientProject) CreateErrors() error {
	return c.t.CreateErrors()
}

// Rollback removes all created resources in this project.
func (c *ClientProject) Rollback(timeout int) error {
	_, err := c.t.Rollback(timeout)
	return err
}

var _ (tDeleteAble) = (*ClientProject)(nil)
