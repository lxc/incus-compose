package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gosimple/slug"
	"github.com/lmittmann/tint"
	incusClient "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/cliconfig"
	"github.com/mattn/go-colorable"
)

// Client wraps a project-scoped Incus client with resource management.
type Client struct {
	globalClient *GlobalClient
	config       ClientConfig

	project      string
	incusProject string
	created      bool

	incus      *incusClient.ProtocolIncus
	imageCache incusClient.InstanceServer
	logger     *slog.Logger

	// Resource storage
	resources ResourceStore

	// Cache for ConnectionIP()
	connectionIP string

	// hookBefore is called before any action
	hookBefore func(action Action, r Resource, args Options, err error) error

	// hookAfter is called after any action
	hookAfter func(action Action, r Resource, args Options, err error) error

	hookOperation       func(ctx context.Context, action Action, r Resource, args Options, op incusClient.Operation, err error) error
	hookRemoteOperation func(ctx context.Context, action Action, r Resource, args Options, op incusClient.RemoteOperation, err error) error

	// hookConnected is called once when the client is ready, before any action.
	hookConnected func(err error) error

	// hookDone is called once when the client's work is complete, for cleanup.
	hookDone func(err error) error
}

func (c *GlobalClient) newClientProject(name, incusName string, created bool) (*Client, error) {
	config := c.projectConfig(name)

	incus := c.incus.UseProject(incusName)
	pIncus, ok := incus.(*incusClient.ProtocolIncus)
	if !ok {
		return nil, ErrConnectionFailed.WithText("cannot cast project-scoped client to ProtocolIncus")
	}

	cp := &Client{
		globalClient: c,
		config:       config,
		project:      name,
		incusProject: incusName,
		created:      created,
		incus:        pIncus,
		imageCache:   c.imageCache,
		logger:       c.logger.With("project", name),

		hookBefore: c.hookBefore,
		hookAfter:  c.hookAfter,

		hookOperation:       c.hookOperation,
		hookRemoteOperation: c.hookRemoteOperation,

		hookConnected: func(err error) error { return err },
		hookDone:      func(err error) error { return err },
	}

	if c.IsDebugging() {
		cp.logger = cp.logger.With("incus_project", incusName)
	}

	c.projects = append(c.projects, cp)

	return cp, nil
}

// Project returns the user-facing project name.
func (c *Client) Project() string {
	return c.project
}

// IncusProject returns the sanitized Incus project name.
func (c *Client) IncusProject() string {
	return c.incusProject
}

// IsRemote returns true if connected via network (not unix socket).
func (c *Client) IsRemote() bool {
	return c.globalClient.IsRemote()
}

// NetworkBridgeIPs returns the IPv4 and IPv6 bridge addresses of an Incus network.
// The addresses are returned without CIDR notation.
// Addresses for which the network config key is absent or set to "none" are omitted.
func (c *Client) NetworkBridgeIPs(networkName string) (ipv4 []string, ipv6 []string, err error) {
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

// IsDebugging returns if debugging is enabled.
func (c *Client) IsDebugging() bool {
	return c.globalClient.IsDebugging()
}

// LogDebug logs an debug message.
// The `any` here is ok.
func (c *Client) LogDebug(msg string, args ...any) {
	c.logger.DebugContext(c.globalClient.Ctx, msg, args...)
}

// LogInfo logs an info message.
// The `any` here is ok.
func (c *Client) LogInfo(msg string, args ...any) {
	c.logger.InfoContext(c.globalClient.Ctx, msg, args...)
}

// LogWarn logs an warning message.
// The `any` here is ok.
func (c *Client) LogWarn(msg string, args ...any) {
	c.logger.WarnContext(c.globalClient.Ctx, msg, args...)
}

// LogError logs an error.
// The `any` here is ok.
func (c *Client) LogError(msg string, args ...any) {
	c.logger.ErrorContext(c.globalClient.Ctx, msg, args...)
}

// Connection returns the project-scoped Connection client.
func (c *Client) Connection() *incusClient.ProtocolIncus {
	return c.incus
}

// GlobalConnection returns the global (non-project-scoped) Incus client.
func (c *Client) GlobalConnection() *incusClient.ProtocolIncus {
	return c.globalClient.incus
}

// Config returns the client config.
func (c *Client) Config() ClientConfig {
	return c.config
}

func (c *GlobalClient) projectConfig(name string) ClientConfig {
	config := c.Config
	config.DescriptionFormat = fmt.Sprintf(config.DescriptionFormat, name) + ":%s"
	return config
}

// IsConnected reports whether the project client can run Incus operations.
func (c *Client) IsConnected() bool {
	return c != nil && c.incus != nil
}

// projectRoot returns the absolute path to the project root directory.
func projectRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Dir(filepath.Dir(file))
}

// resolvePath resolves a path relative to the project root.
func resolvePath(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(projectRoot(), path)
}

// NewTestClient creates a new GlobalClient for testing.
// Returns error if INCUS_COMPOSE_URL is not set.
func NewTestClient(ctx context.Context) (*GlobalClient, error) {
	if _, ok := os.LookupEnv("INCUS_COMPOSE_TEST_LOCAL"); ok {
		return nil, ErrTestLocal
	}

	var logger *slog.Logger

	logFormat, ok := os.LookupEnv("LOG_FORMAT")
	if !ok {
		_, inCI := os.LookupEnv("CI")
		if inCI {
			logFormat = "text"
		} else {
			logFormat = "colortext"
		}
	}

	switch logFormat {
	case "json":
		logger = slog.New(slog.NewJSONHandler(
			os.Stderr,
			&slog.HandlerOptions{Level: slog.LevelDebug - 4}),
		)
	case "colortext":
		logger = slog.New(tint.NewHandler(
			colorable.NewColorable(os.Stderr),
			&tint.Options{
				Level:      slog.LevelDebug - 4,
				TimeFormat: "15:04",
			},
		))
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
	} else if u, ok := os.LookupEnv("INCUS_COMPOSE_URL"); ok {
		// 2. If INCUS_COMPOSE_URL is set, use direct URL connection
		slog.DebugContext(ctx, "Connecting", "url", u)

		cert := resolvePath(os.Getenv("INCUS_COMPOSE_CERT"))
		key := resolvePath(os.Getenv("INCUS_COMPOSE_KEY"))

		opts = []ClientOption{
			ClientURL(u),
			ClientLogger(logger),
			ClientInsecureSkipVerify(),
		}

		if cert != "" {
			opts = append(opts, ClientTLSClientCert(cert))
		}
		if key != "" {
			opts = append(opts, ClientTLSClientKey(key))
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

	AddDebuggerHook(c)

	return c, nil
}

// NewOfflineClient creates a disconnected project client for resource planning.
// It can create in-memory resources, but cannot run Incus operations.
func NewOfflineClient(ctx context.Context, name string) *Client {
	gc := New(ctx)
	config := gc.projectConfig(name)

	return &Client{
		globalClient: gc,
		config:       config,
		project:      name,
		incusProject: sanitizeProjectName(name),
		logger:       gc.logger.With("project", name),
	}
}

// Resource returns an existing resource or creates a new one.
// Deduplication uses IncusName so differently-formatted inputs that resolve
// to the same Incus resource (e.g. "nginx:alpine" vs "docker.io/library/nginx:alpine") return the same object.
func (c *Client) Resource(kind Kind, name string, config Config) (Resource, error) {
	// Fast path: check by raw name before constructing the resource.
	if res := c.resources.Get(kind, name); res != nil {
		return res, nil
	}

	var (
		res Resource
		err error
	)

	switch kind {
	case KindProfile:
		res, err = newProfile(c, name, config)
	case KindNetwork:
		res, err = newNetwork(c, name, config)
	case KindStorageVolume:
		res, err = newStorageVolume(c, name, config)
	case KindImage:
		res, err = newImage(c, name, config)
	case KindInstance:
		res, err = newInstance(c, name, config)
	default:
		return nil, ErrUnknownResource.WithText(string(kind))
	}

	if err != nil {
		return nil, err
	}

	// Deduplicate by IncusName().
	if existing := c.resources.Get(kind, res.IncusName()); existing != nil {
		return existing, nil
	}

	c.resources.Add(res)
	return res, nil
}

// AddHookBefore adds a hook that will be executed before any action.
// You may use it for abort control.
func (c *Client) AddHookBefore(hook func(action Action, r Resource, args Options, err error) error) {
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
func (c *Client) AddHookAfter(hook func(action Action, r Resource, args Options, err error) error) {
	prevHook := c.hookAfter
	newHook := func(action Action, r Resource, args Options, err error) error {
		// Run new hook FIRST, then pass result to previous hooks (LIFO)
		err = hook(action, r, args, err)
		return prevHook(action, r, args, err)
	}

	c.hookAfter = newHook
}

// AddHookConnected adds a hook that will be executed when the client connects (FIFO order).
func (c *Client) AddHookConnected(hook func(err error) error) {
	prevHook := c.hookConnected
	newHook := func(err error) error {
		// Run previous hooks FIRST (FIFO)
		if err := prevHook(err); err != nil {
			return err
		}
		// Then run the new hook
		return hook(nil)
	}

	c.hookConnected = newHook
}

// AddHookDone adds a hook that will be executed when the client's work is complete (LIFO order).
func (c *Client) AddHookDone(hook func(err error) error) {
	prevHook := c.hookDone
	newHook := func(err error) error {
		// Run new hook FIRST, then pass result to previous hooks (LIFO)
		err = hook(err)
		return prevHook(err)
	}

	c.hookDone = newHook
}

// Open fires the connected hooks. Call once after registering all hooks,
// before running any stack actions.
func (c *Client) Open() error {
	return c.hookConnected(nil)
}

// Done fires the done hooks. Call when the client's work is complete.
func (c *Client) Done() error {
	return c.hookDone(nil)
}

// FindHealthdName returns the name of the healthd instance in the project,
// identified by user.healthcheck.daemon=true. Returns ("", nil) if not found.
func (c *Client) FindHealthdName() (string, error) {
	if c.incus == nil {
		return "", nil
	}

	instances, err := c.incus.GetInstances("")
	if err != nil {
		return "", fmt.Errorf("listing instances: %w", err)
	}

	for _, inst := range instances {
		if inst.Config["user.healthcheck.daemon"] == "true" {
			return inst.Name, nil
		}
	}

	return "", nil
}

// InstanceExists reports whether an instance with the given name exists in Incus.
func (c *Client) InstanceExists(name string) (bool, error) {
	if c.incus == nil {
		return false, nil
	}

	_, _, err := c.incus.GetInstance(SanitizeIncusName(name, -1))
	return err == nil, nil
}

// InstanceIPs fetches the global IPv4 and IPv6 addresses of a named
// instance directly from Incus, without going through an Instance resource.
func (c *Client) InstanceIPs(incusName string) (ips []InterfaceIPs, err error) {
	state, _, err := c.incus.GetInstanceState(incusName)
	if err != nil {
		return nil, err
	}

	if state.Status != "Running" {
		return nil, errors.New("instance not running")
	}

	result := []InterfaceIPs{}

	for sDevice, sNetwork := range state.Network {
		if sNetwork.Type == "loopback" || sNetwork.Addresses == nil {
			continue
		}

		res, err := c.Resource(KindInstance, incusName, &InstanceConfig{})
		if err != nil {
			return nil, err
		}

		inst, ok := res.(*Instance)
		if !ok || !inst.IsEnsured() {
			continue
		}

		devices, ok := inst.IncusInstance.Devices[sDevice]
		if !ok {
			return result, nil
		}

		network := devices["network"]

		iPv4s := []string{}
		iPv6s := []string{}
		for _, addr := range sNetwork.Addresses {
			if addr.Scope == "global" && addr.Family == "inet" && addr.Address != "" {
				iPv4s = append(iPv4s, addr.Address)
			}
			if addr.Scope == "global" && addr.Family == "inet6" && addr.Address != "" {
				iPv6s = append(iPv6s, addr.Address)
			}
		}

		if len(iPv4s) < 1 && len(iPv6s) < 1 {
			// No ip found
			continue
		}

		result = append(result, InterfaceIPs{Network: network, IPv4s: iPv4s, IPv6s: iPv6s})
	}

	if len(result) < 1 {
		return nil, ErrNotFound.WithText("no Networks/IPs found")
	}

	return result, nil
}

// ResolveImageFingerprint returns the first alias name for the given fingerprint,
// or the fingerprint itself if no alias is found or the lookup fails.
func (c *Client) ResolveImageFingerprint(fingerprint string) string {
	if fingerprint == "" {
		return ""
	}
	img, _, err := c.globalClient.incus.GetImage(fingerprint)
	if err == nil && img != nil && len(img.Aliases) > 0 {
		return img.Aliases[0].Name
	}

	c.LogWarn("failed to resolve image", "fingerprint", fingerprint)
	return fingerprint
}

// ConnectionIP returns the IP of the current connection,
// this function is cached by Client.connectionIP.
func (c *Client) ConnectionIP() (string, error) {
	if !c.IsRemote() {
		return "", errors.New("Client.ConnectionIP needs a non unix connection")
	}

	if c.connectionIP != "" {
		return c.connectionIP, nil
	}

	u, err := url.Parse(c.config.URL)
	if err != nil {
		return "", fmt.Errorf("while parsing url %q: %w", c.config.URL, err)
	}

	if net.ParseIP(u.Hostname()) != nil {
		c.connectionIP = u.Hostname()
		return c.connectionIP, nil
	}

	ip, err := net.LookupIP(u.Hostname())
	if err != nil {
		return "", fmt.Errorf("while looking up the IP for %q: %w", c.config.URL, err)
	}

	c.connectionIP = ip[0].String()
	return c.connectionIP, nil
}

// NetworkForIP lookups the incus network for the given ip.
func (c *Client) NetworkForIP(ip string) (string, error) {
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

// AddDebuggerHook adds a debugging hook for client resources.
func AddDebuggerHook(c *GlobalClient) {
	c.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if err != nil {
			c.LogDebug("Result with error", "name", r.Name(), "kind", r.Kind(), "action", action, "created", r.Created(), "error", err)
			return err
		}

		c.LogDebug("Done", "name", r.Name(), "kind", r.Kind(), "action", action, "created", r.Created())
		return nil
	})
}

// sanitizeProjectName converts a string to a valid Incus project name.
// Replaces underscores with hyphens and removes special characters via slug.
func sanitizeProjectName(name string) string {
	safe := slug.Make(name)
	safe = strings.ReplaceAll(safe, "_", "-")
	return safe
}

// SanitizeIncusName converts a string to a valid Incus instance name.
// Converts to lowercase, replaces special chars and underscores with hyphens.
// Names exceeding 63 chars are replaced with a 32-char hex hash for DNS compatibility.
func SanitizeIncusName(name string, maxLength int) string {
	if maxLength == -1 {
		maxLength = MaxIncusNameLen
	}

	// slug.Make converts to lowercase, replaces special chars with hyphens
	// but keeps underscores, so we replace them explicitly
	safe := slug.Make(name)
	safe = strings.ReplaceAll(safe, "_", "-")

	if len(safe) > maxLength {
		// Fall back to hash for very long names
		sha256sum := sha256.Sum256([]byte(name))
		safe = hex.EncodeToString(sha256sum[:16]) // 32 hex chars
	}

	return safe
}
