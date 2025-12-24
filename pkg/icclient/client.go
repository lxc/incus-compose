// Package icclient provides a high-level wrapper around the Incus client library.
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
//
// TODO(r3j0): Check the ETag handling, make sure its consistent.
package icclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	cTypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/distribution/reference"
	incusClient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
	incusApi "github.com/lxc/incus/v6/shared/api"
	"github.com/opencontainers/go-digest"
)

// clientKey is a context key for storing the Client in context.Context.
type clientKey struct{}

// ServiceConfig is an alias for the compose-spec `ServiceConfig` type.
type ServiceConfig = cTypes.ServiceConfig

// Project is an alias for the compose-spec `Project` type.
type Project = cTypes.Project

// Verbosity defines the logging verbosity level.
type Verbosity = int

// Verbosity levels for logging output.
const (
	VerbosityInfo  Verbosity = 0
	VerbosityDebug Verbosity = 1
	VerbosityTrace Verbosity = 2
)

// DefaultVerbosity is the default verbosity level for operations.
var DefaultVerbosity = VerbosityInfo

// Config holds configuration options for the Client.
type Config struct {
	// Verbosity controls the logging level (VerbosityInfo, VerbosityDebug, VerbosityTrace)
	Verbosity Verbosity

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
}

// Option is a functional option for configuring the Client.
type Option func(*Config)

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

// Client provides a high-level interface to Incus operations.
// It wraps the Incus client library and provides project-aware operations,
// automatic name sanitization, and compose-spec service management.
type Client struct {
	// Config holds the client configuration
	Config Config

	// Ctx is the context for operations
	Ctx context.Context

	// logger is the structured logger
	logger *slog.Logger

	// project is the current compose project name
	project string

	// incusProject is the sanitized Incus project name
	incusProject string

	// incus is the project-scoped Incus client
	incus *incusClient.ProtocolIncus

	// globalIncus is the global (no project) Incus client
	globalIncus *incusClient.ProtocolIncus

	// socketPath is the Unix socket path (empty for remote connections)
	socketPath string

	// connected tracks whether Connect() has been called successfully
	connected bool
}

// New creates a new Client instance with the provided context, logger, and URL.
// The client must call Connect() before performing any operations.
//
// Example:
//
//	client := icclient.New(ctx, logger, "https://localhost:8443",
//		icclient.TLSClientCert("/path/to/cert.crt"),
//		icclient.TLSClientKey("/path/to/cert.key"),
//	)
//	if err := client.Connect(); err != nil {
//		return err
//	}
func New(ctx context.Context, logger *slog.Logger, url string, opts ...Option) *Client {
	// Setup defaults
	config := Config{
		Verbosity:          DefaultVerbosity,
		DefaultStoragePool: "default",
		NetworkPrefix:      "ic-",
		DescriptionFormat:  "incus-compose: %s",
	}

	// Apply Options
	for _, o := range opts {
		o(&config)
	}

	config.URL = url

	return &Client{
		Config: config,
		Ctx:    ctx,
		logger: logger.With("url", url),
	}
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

// Logger returns the structured logger instance.
func (c *Client) Logger() *slog.Logger {
	return c.logger
}

// Incus returns the project-scoped Incus client.
// This client operates within the current project set by UseProject().
func (c *Client) Incus() *incusClient.ProtocolIncus {
	return c.incus
}

// GlobalIncus returns the global Incus client (no project scope).
// Use this for operations that need to work across projects.
func (c *Client) GlobalIncus() *incusClient.ProtocolIncus {
	return c.globalIncus
}

// IsDebug returns true if debug logging is enabled.
func (c *Client) IsDebug() bool {
	return c.Config.Verbosity >= VerbosityDebug
}

// IsTrace returns true if trace logging is enabled.
func (c *Client) IsTrace() bool {
	return c.Config.Verbosity >= VerbosityTrace
}

// Connect establishes a connection to the Incus server.
// This must be called before any other operations.
// Returns ErrDisconnected if called on an already connected client.
func (c *Client) Connect() error {
	args := &incusClient.ConnectionArgs{
		InsecureSkipVerify: c.Config.InsecureSkipVerify,
		AuthType:           "tls",
	}

	// Read TLS client certificate and key files if provided
	if c.Config.TLSClientCert != "" && c.Config.TLSClientKey != "" {
		certPath, err := filepath.Abs(c.Config.TLSClientCert)
		if err != nil {
			return fmt.Errorf("failed to find the TLS client cert '%s': %w", c.Config.TLSClientCert, err)
		}
		keyPath, err := filepath.Abs(c.Config.TLSClientKey)
		if err != nil {
			return fmt.Errorf("failed to find the TLS client key for '%s': %w", c.Config.TLSClientKey, err)
		}

		certData, err := os.ReadFile(certPath)
		if err != nil {
			return fmt.Errorf("failed to read TLS client certificate '%s': %w", certPath, err)
		}

		keyData, err := os.ReadFile(keyPath)
		if err != nil {
			return fmt.Errorf("failed to read the TLS client key '%s': %w", keyPath, err)
		}

		args.TLSClientCert = string(certData)
		args.TLSClientKey = string(keyData)
	}

	c.logger.Debug("Connecting")

	incus, err := incusClient.ConnectIncusWithContext(c.Ctx, c.Config.URL, args)
	if err != nil {
		return fmt.Errorf("failed to connect to Incus URL '%s': %w", c.Config.URL, err)
	}

	info, err := incus.GetConnectionInfo()
	if err != nil {
		return err
	}

	if info.Protocol != "incus" {
		return fmt.Errorf("expected an incus server, got a %q", info.Protocol)
	}

	gIncus, ok := incus.(*incusClient.ProtocolIncus)
	if !ok {
		return fmt.Errorf("cant cast an InstanceServer to ProtocolIncus: %q", c.Config.URL)
	}

	c.globalIncus = gIncus
	c.incus = gIncus
	c.socketPath = info.SocketPath

	c.connected = true
	return nil
}

// UseProject switches the client to operate within the specified project.
// The project name is automatically sanitized to meet Incus naming requirements.
// Returns an error if the project doesn't exist or the connection fails.
func (c *Client) UseProject(project string) error {
	incusProject := sanitizeProjectName(project)

	incus := c.globalIncus.UseProject(incusProject)
	info, err := incus.GetConnectionInfo()
	if err != nil {
		return err
	}

	if info.Protocol != "incus" {
		return fmt.Errorf("expected an incus server, got a %q", info.Protocol)
	}

	pIncus, ok := incus.(*incusClient.ProtocolIncus)
	if !ok {
		return fmt.Errorf("cant cast an InstanceServer to ProtocolInucs: %q", c.Config.URL)
	}

	c.incus = pIncus
	c.project = project
	c.incusProject = incusProject
	c.logger = c.logger.With("project", project)

	return nil
}

// HasProject returns true if the client is currently scoped to a project.
func (c *Client) HasProject() bool {
	return c.project != ""
}

// IsRemote returns true if the client is connected to a remote Incus server.
// A connection is considered remote if it uses HTTPS (not a Unix socket).
// This includes SSH-tunneled connections, as they also use HTTPS endpoints.
func (c *Client) IsRemote() bool {
	return c.socketPath == ""
}

// EnsureProjectOptions holds configuration for project creation/validation.
type EnsureProjectOptions struct {
	// Whether to create the project if it doesn't exist
	// If false, will error if project doesn't exist
	Create bool

	// Profile to create / update.
	Profile string

	// Source project to copy profile from (defaults to "default")
	SourceProject string

	// Profile to copy from (defaults to "default")
	SourceProfile string
}

// EnsureProjectOption is a functional option for EnsureProject.
type EnsureProjectOption func(*EnsureProjectOptions)

// EnsureProjectCreate enables project creation if it doesn't exist.
func EnsureProjectCreate() EnsureProjectOption {
	return func(o *EnsureProjectOptions) {
		o.Create = true
	}
}

// EnsureProjectProfile sets the profile name to create/update.
func EnsureProjectProfile(n string) EnsureProjectOption {
	return func(o *EnsureProjectOptions) {
		o.Profile = n
	}
}

// EnsureProjecSourceProject sets the source project to copy the profile from.
func EnsureProjecSourceProject(n string) EnsureProjectOption {
	return func(o *EnsureProjectOptions) {
		o.SourceProject = n
	}
}

// EnsureProjectSourceProfile sets the source profile name to copy from.
func EnsureProjectSourceProfile(n string) EnsureProjectOption {
	return func(o *EnsureProjectOptions) {
		o.SourceProfile = n
	}
}

// EnsureProject ensures an Incus project exists and is properly configured.
// It optionally creates the project if it doesn't exist (with EnsureProjectCreate option).
// Returns the sanitized project name and any error encountered.
func (c *Client) EnsureProject(project string, opts ...EnsureProjectOption) (string, error) {
	if !c.connected {
		return "", ErrDisconnected
	}

	options := EnsureProjectOptions{
		Profile:       "default",
		SourceProject: "default",
		SourceProfile: "default",
	}

	for _, o := range opts {
		o(&options)
	}

	incusProject := sanitizeProjectName(project)

	// Check if project exists
	projectNames, err := c.globalIncus.GetProjectNames()
	if err != nil {
		c.logger.Debug("Failed to fetch project names", slog.Any("error", err))
		return "", fmt.Errorf("fetching project names: %w", err)
	}
	if slices.Contains(projectNames, incusProject) {
		if err = c.UseProject(project); err != nil {
			return "", err
		}
		return "", c.EnsureProfile(options.SourceProject, options.SourceProfile, incusProject, options.Profile, nil)
	}

	if !options.Create {
		return "", fmt.Errorf("project %q sanitized to %q does not exist", project, incusProject)
	}

	// Create project
	projectArgs := incusApi.ProjectsPost{
		Name: incusProject,
		ProjectPut: incusApi.ProjectPut{
			Description: fmt.Sprintf("incus-compose: %s", project),
			Config:      incusApi.ConfigMap{"features.profiles": "true"},
		},
	}

	err = c.globalIncus.CreateProject(projectArgs)
	if err != nil {
		return "", fmt.Errorf("creating project %q sanitized to %q: %w", project, incusProject, err)
	}

	if err = c.UseProject(project); err != nil {
		return "", err
	}

	// Check and populate default profile if needed
	err = c.EnsureProfile(options.SourceProject, options.SourceProfile, incusProject, options.Profile, nil)
	if err != nil {
		return "", err
	}

	return incusProject, nil
}

// EnsureProfile ensures a profile exists in the target project with proper configuration.
// If the profile doesn't exist, it creates it and copies device configuration from the source profile.
// If sourceServer is nil, uses the global Incus client.
func (c *Client) EnsureProfile(sourceProject, sourceProfile, targetProject, targetProfile string, sourceServer *incusClient.ProtocolIncus) error {
	if !c.connected {
		return ErrDisconnected
	}

	if sourceServer == nil {
		sourceServer = c.globalIncus
	}

	// Get the target default profile (in the project)
	apiTargetProfile, etag, err := c.incus.GetProfile(targetProfile)
	if err != nil {
		err = c.incus.CreateProfile(incusApi.ProfilesPost{Name: targetProfile, ProfilePut: incusApi.ProfilePut{Description: fmt.Sprintf(c.Config.DescriptionFormat, c.project)}})
		if err != nil {
			return fmt.Errorf("configuring default profile for project %q: %w", targetProject, err)
		}
		apiTargetProfile, etag, err = c.incus.GetProfile(targetProfile)
		if err != nil {
			return fmt.Errorf("configuring default profile for project %q: %w", targetProject, err)
		}
	}

	if len(apiTargetProfile.Devices) > 0 {
		// Profile already configured, nothing to do
		return nil
	}

	apiSourceProfile, _, err := sourceServer.GetProfile(sourceProfile)
	if err != nil {
		return fmt.Errorf("getting source profile %q from project %q: %w", sourceProfile, sourceProject, err)
	}

	apiTargetProfile.Devices = apiSourceProfile.Devices
	err = c.incus.UpdateProfile(targetProfile, apiTargetProfile.Writable(), etag)
	if err != nil {
		return fmt.Errorf("updating default profile: %w", err)
	}

	return nil
}

// EnsureNetwork ensures a network exists in the current project.
// If the network doesn't exist, it creates a new bridge network.
// Returns the sanitized network name used in Incus.
func (c *Client) EnsureNetwork(name string) (string, error) {
	if !c.connected {
		return "", ErrDisconnected
	}

	incusNetwork := networkNameForProject(c.incusProject, c.Config.NetworkPrefix, name)

	logger := c.logger.With("name", name)
	if c.IsDebug() {
		logger = logger.With("incus_name", incusNetwork)
	}

	// Check if exists
	_, _, err := c.incus.GetNetwork(incusNetwork)
	if err == nil {
		logger.Debug("Network exists")
		return incusNetwork, nil
	}

	logger.Info("Creating network")

	req := incusApi.NetworksPost{
		Name: incusNetwork,
		Type: "bridge",
	}

	return incusNetwork, c.incus.CreateNetwork(req)
}

// RemoveNetwork removes a network from the current project.
// Returns nil if the network doesn't exist.
func (c *Client) RemoveNetwork(name string) error {
	if !c.connected {
		return ErrDisconnected
	}

	incusNetwork := networkNameForProject(c.incusProject, c.Config.NetworkPrefix, name)

	logger := c.logger.With("name", name)
	if c.IsDebug() {
		logger = logger.With("incus_name", incusNetwork)
	}

	// Check if network exists
	_, _, err := c.incus.GetNetwork(incusNetwork)
	if err != nil {
		// TODO(r3j0): Use errors.Is!
		if strings.Contains(err.Error(), "not found") {
			return nil
		}
		return err
	}

	return c.incus.DeleteNetwork(incusNetwork)
}

// EnsureImage ensures an OCI/container image exists in the Incus image store.
// If the image doesn't exist locally, it copies it from the provided image server.
// The noProject parameter controls whether to store the image globally or in the current project.
func (c *Client) EnsureImage(ref reference.Named, imageServer incusClient.ImageServer, noProject bool) (*incusApi.Image, error) {
	if !c.connected {
		return nil, ErrDisconnected
	}

	remote := reference.Domain(ref)

	imageNoRemote := strings.TrimLeft(ref.String(), remote+"/")

	logger := c.logger.With(slog.String("remote", remote), slog.String("image", imageNoRemote))

	var err error
	var alias *incusApi.ImageAliasesEntry
	destImageServer := c.incus
	if noProject {
		destImageServer = c.globalIncus
	}

	alias, _, err = destImageServer.GetImageAlias(ref.String())
	if err == nil {
		digestRef, err := reference.WithDigest(reference.TrimNamed(ref), digest.Digest("sha256:"+alias.Target))
		logger.Debug("Found image on the server", slog.String("digest", digestRef.String()))
		if err != nil {
			return nil, fmt.Errorf("creating a digest, %w", err)
		}

		logger.Debug("Getting image info", "fingerprint", alias.Target)
		imageInfo, _, err := destImageServer.GetImage(alias.Target)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve image: %w", err)
		}

		return imageInfo, nil
	}

	logger.Info("Copying image")

	// Copy the image
	// That part is from: https://github.com/lxc/incus/blob/7e6d27123ebaa4bf9bff4d9fc30c11d9206feb83/cmd/incus/image.go#L236
	imgInfo := &api.Image{}
	imgInfo.Fingerprint = imageNoRemote
	imgInfo.Public = true // Needed to copy from public image servers.

	copyArgs := incusClient.ImageCopyArgs{
		Aliases:    []api.ImageAlias{{Name: ref.String()}},
		AutoUpdate: true,
		Public:     false,
		Mode:       "pull",
	}

	// Do the copy
	op, err := destImageServer.CopyImage(imageServer, *imgInfo, &copyArgs)
	if err != nil {
		logger.Debug("Image copy operation failed", slog.Any("error", err))
		return nil, fmt.Errorf("copying image %q, %w", ref, err)
	}

	// TODO(r3j0): Add progress and make it cancelable.
	err = op.Wait()
	if err != nil {
		logger.Debug("Image copy wait failed", slog.Any("error", err))
		return nil, fmt.Errorf("copying image %q, %w", ref, err)
	}

	alias, _, err = destImageServer.GetImageAlias(ref.String())
	if err != nil {
		logger.Debug("Failed to get image alias after copy", slog.Any("error", err))
		return nil, fmt.Errorf("getting image %q alias after copy, %w", ref, err)
	}

	digestRef, err := reference.WithDigest(reference.TrimNamed(ref), digest.Digest("sha256:"+alias.Target))
	if err != nil {
		logger.Debug("Failed to create digest reference", slog.Any("error", err))
		return nil, fmt.Errorf("creating a digest, %w", err)
	}

	imageInfo, _, err := destImageServer.GetImage(digestRef.Digest().Encoded())
	if err != nil {
		return nil, fmt.Errorf("failed to resolve image: %w", err)
	}

	return imageInfo, nil
}

// EnsurePoolVolume ensures a storage volume exists with the proper UID/GID configuration.
// The volume is created with security.shifted=true for proper UID/GID mapping.
// Returns the volume, ETag, and any error encountered.
func (c *Client) EnsurePoolVolume(name, uid, gid string, storagePool string) (*incusApi.StorageVolume, string, error) {
	if !c.connected {
		return nil, "", ErrDisconnected
	}

	if storagePool == "" {
		storagePool = c.Config.DefaultStoragePool
	}

	incusName := name
	if c.HasProject() {
		incusName = fmt.Sprintf("%s-%s", name, c.project)
	}

	logger := c.logger.With("name", name)
	if c.IsDebug() {
		logger = logger.With("incus_name", incusName, "uid", uid, "gid", gid)
	}

	// Check if volume exists
	vol, etag, err := c.incus.GetStoragePoolVolume(storagePool, "custom", incusName)
	if err == nil {
		if vol.Config["security.shifted"] != "true" || vol.Config["initial.uid"] != uid || vol.Config["initial.gid"] != gid {
			return nil, "", fmt.Errorf("failed to validate existing volume config: %q", name)
		}

		slog.Debug("Volume exists")

		return vol, etag, err
	}

	// Create volume with proper uid/gid
	logger.Info("Creating volume")

	volReq := api.StorageVolumesPost{
		Name:        incusName,
		Type:        "custom",
		ContentType: "filesystem",
		StorageVolumePut: api.StorageVolumePut{
			Config: map[string]string{
				"security.shifted": "true",
				"initial.uid":      uid,
				"initial.gid":      gid,
			},
		},
	}

	if err := c.incus.CreateStoragePoolVolume(storagePool, volReq); err != nil {
		return nil, "", fmt.Errorf("creating volume %s: %w", incusName, err)
	}

	return c.incus.GetStoragePoolVolume(storagePool, "custom", incusName)
}

// RemovePoolVolume removes a storage pool volume.
// It searches all storage pools to find and delete the volume.
// Returns nil if the volume doesn't exist.
func (c *Client) RemovePoolVolume(name string, storagePool string) error {
	if !c.connected {
		return ErrDisconnected
	}

	if storagePool == "" {
		storagePool = c.Config.DefaultStoragePool
	}

	incusName := name
	if c.HasProject() {
		incusName = fmt.Sprintf("%s-%s", name, c.project)
	}

	// Get storage pools and try to find the volume
	pools, err := c.incus.GetStoragePoolNames()
	if err != nil {
		return err
	}

	for _, pool := range pools {
		_, _, err := c.incus.GetStoragePoolVolume(pool, "custom", incusName)
		if err == nil {
			c.logger.Debug("Removing storage pool volume: %q", "name", name)
			return c.incus.DeleteStoragePoolVolume(pool, "custom", incusName)
		}
	}

	return nil
}

// AttachPoolVolume attaches a storage volume to an instance as a disk device.
// The device is named "vol-{volumeName}" and mounted at the target path.
// If already attached, this is a no-op.
func (c *Client) AttachPoolVolume(volume *incusApi.StorageVolume, instance *incusApi.Instance, eTag string, storagePool string, target string, readyOnly bool) error {
	if !c.connected {
		return ErrDisconnected
	}

	logger := c.logger.With("instance", instance.Name, "volume", volume.Name)

	// Check if already attached
	devName := fmt.Sprintf("vol-%s", volume.Name)
	if _, exists := instance.Devices[devName]; exists {
		logger.Debug("Volume already attached")
		return nil
	}

	logger = logger.With("device_name", devName)

	logger.Debug("Attaching volume")

	device := map[string]string{
		"type":   "disk",
		"pool":   storagePool,
		"source": volume.Name,
		"path":   target,
	}

	if readyOnly {
		device["readonly"] = "true"
	}

	instance.Devices[devName] = device

	op, err := c.incus.UpdateInstance(instance.Name, instance.Writable(), eTag)
	if err != nil {
		return fmt.Errorf("attaching volume %q to instance %q: %w", volume.Name, instance.Name, err)
	}

	err = op.Wait()
	if err != nil {
		return fmt.Errorf("attaching pool volume %q to instance %q: %w", volume.Name, instance.Name, err)
	}

	return err
}

// AttachBindMount attaches a host path as a bind-mounted disk device to an instance.
// The source path is made absolute and UID/GID shifting is enabled.
// The device is named "bind-{sanitized-target-path}".
//
// Note: Bind mounts only work with local Incus connections (Unix socket).
// For remote connections (including SSH tunnels), use named volumes instead.
func (c *Client) AttachBindMount(instance *incusApi.Instance, eTag string, source string, target string, readOnly bool) error {
	if !c.connected {
		return ErrDisconnected
	}

	if c.IsRemote() {
		return fmt.Errorf(
			"bind mounts are not supported with remote Incus connections\n"+
				"The source path %q exists on your local machine, not on the Incus host.\n"+
				"Use named volumes instead. See: https://linuxcontainers.org/incus/docs/main/howto/storage_volumes/",
			source,
		)
	}

	// Use target path as base, sanitize for device name
	name := strings.ReplaceAll(target, "/", "-")
	name = strings.TrimPrefix(name, "-")
	if name == "" {
		name = "root"
	}
	devName := fmt.Sprintf("bind-%s", name)

	logger := c.logger.With("instance", instance.Name, "source", source, "device_name", devName)

	if _, exists := instance.Devices[devName]; exists {
		logger.Debug("Bind mount already attached")
		return nil
	}

	// Make source path absolute
	sourcePath := source
	if !filepath.IsAbs(sourcePath) {
		var err error
		sourcePath, err = filepath.Abs(sourcePath)
		if err != nil {
			return fmt.Errorf("resolving bind mount path: %w", err)
		}
	}

	logger.Debug("Attaching bind mount", "source", sourcePath, "target", target)

	device := map[string]string{
		"type":   "disk",
		"source": sourcePath,
		"path":   target,
		"shift":  "true", // Enable uid/gid shifting for bind mounts
	}

	if readOnly {
		device["readonly"] = "true"
	}

	instance.Devices[devName] = device

	op, err := c.incus.UpdateInstance(instance.Name, instance.Writable(), eTag)
	if err != nil {
		return fmt.Errorf("attaching bind mount: %w", err)
	}

	err = op.Wait()
	if err != nil {
		return fmt.Errorf("attaching bind mount %q to instance %q: %w", source, instance.Name, err)
	}

	return err
}

// IncusServiceName translates a compose-spec service to a valid Incus instance name.
// Uses service.ContainerName if set, otherwise uses service.Name.
// The name is sanitized to meet Incus naming requirements.
func (c *Client) IncusServiceName(service ServiceConfig) string {
	incusName := sanitizeInstanceName(service.Name)
	if service.ContainerName != "" {
		incusName = sanitizeInstanceName(service.ContainerName)
	}

	return incusName
}

// Instance retrieves an Incus instance by name.
// Returns the instance, ETag, and any error encountered.
func (c *Client) Instance(name string) (*incusApi.Instance, string, error) {
	if !c.connected {
		return nil, "", ErrDisconnected
	}

	return c.incus.GetInstance(name)
}

// InstanceFromService retrieves the Incus instance corresponding to a compose-spec service.
// The service name is translated to an Incus instance name before lookup.
// Returns the instance, ETag, and any error encountered.
func (c *Client) InstanceFromService(service ServiceConfig) (*incusApi.Instance, string, error) {
	if !c.connected {
		return nil, "", ErrDisconnected
	}

	incusName := c.IncusServiceName(service)

	inst, etag, err := c.incus.GetInstance(incusName)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get service %q: %w", service.Name, err)
	}

	return inst, etag, err
}

// StopInstance stops a running instance with a 30-second timeout and force flag.
// Returns an error if the instance is not running.
func (c *Client) StopInstance(instance *incusApi.Instance, eTag string) error {
	if !c.connected {
		return ErrDisconnected
	}

	if instance.Status != "Running" {
		return fmt.Errorf("failed to stop %q, its status is %q: %w", instance.Name, instance.Status, ErrInstanceNotRunning)
	}

	req := api.InstanceStatePut{
		Action:  "stop",
		Timeout: 30,
		Force:   true,
	}
	op, err := c.incus.UpdateInstanceState(instance.Name, req, eTag)
	if err != nil {
		return err
	}
	if err := op.Wait(); err != nil {
		return err
	}

	return nil
}

// RemoveInstance deletes an instance from Incus.
// If force is true and the instance is running, it will be stopped first.
// Otherwise, returns an error if the instance is running.
func (c *Client) RemoveInstance(instance *incusApi.Instance, eTag string, force bool) error {
	if !c.connected {
		return ErrDisconnected
	}

	// Stop first if running
	if instance.Status == "Running" {
		if !force {
			return fmt.Errorf("failed to remove %q: %w", instance.Name, ErrInstanceRunning)
		}
		err := c.StopInstance(instance, eTag)
		if err != nil {
			return err
		}
	}

	op, err := c.incus.DeleteInstance(instance.Name)
	if err != nil {
		return err
	}

	return op.Wait()
}

// StartInstance starts an instance with the specified timeout.
// A timeout of 0 means no timeout (-1 in Incus API).
func (c *Client) StartInstance(instance *incusApi.Instance, eTag string, timeout int) error {
	if !c.connected {
		return ErrDisconnected
	}

	if timeout == 0 {
		timeout = -1
	}

	req := api.InstanceStatePut{
		Action:  "start",
		Timeout: timeout,
	}

	op, err := c.incus.UpdateInstanceState(instance.Name, req, eTag)
	if err != nil {
		return err
	}

	if err := op.Wait(); err != nil {
		return fmt.Errorf("failed to start instance %q: %w", instance.Name, err)
	}

	return nil
}

// EnsureService ensures an Incus instance exists for a compose-spec service.
// This translates the service definition into Incus instance configuration including:
//   - Environment variables and labels
//   - Network devices
//   - Port proxies
//   - Volumes (bind mounts and named volumes)
//
// If reCreate is true, any existing instance is deleted and recreated.
// Returns the instance, ETag, and any error encountered.
func (c *Client) EnsureService(service ServiceConfig, imageServer incusClient.InstanceServer, noImageProject bool, reCreate bool) (*incusApi.Instance, string, error) {
	if !c.connected {
		return nil, "", ErrDisconnected
	}

	incusName := c.IncusServiceName(service)

	logger := c.logger.With("name", service.Name)
	if c.IsDebug() {
		logger = logger.With("incus_name", incusName)
	}

	instance, etag, err := c.InstanceFromService(service)
	if err == nil {
		if reCreate {
			err = c.RemoveInstance(instance, etag, true)
			if err != nil {
				return nil, "", fmt.Errorf("failed to remove service %q: %w", service.Name, err)
			}
		} else {
			// TODO(r3j0): Validate the service?
			logger.Debug("Service exists")
			return instance, etag, nil
		}
	}

	// Build config
	config := map[string]string{}

	// Environment variables
	for key, val := range service.Environment {
		if val != nil {
			config["environment."+key] = *val
		}
	}

	// Labels as user config
	for key, val := range service.Labels {
		config["user."+key] = val
	}

	// Build devices
	devices := map[string]map[string]string{}

	// Network devices
	ethIdx := 0
	for netName := range service.Networks {
		incusNetwork, err := c.EnsureNetwork(netName)
		if err != nil {
			return nil, "", err
		}

		devName := fmt.Sprintf("eth%d", ethIdx)
		devices[devName] = map[string]string{
			"type":    "nic",
			"network": incusNetwork,
			"name":    devName,
		}
		ethIdx++
	}

	// Port proxies
	for _, port := range service.Ports {
		hostIP := port.HostIP
		if hostIP == "" {
			hostIP = "0.0.0.0"
		}
		devName := fmt.Sprintf("proxy-%s-%s", hostIP, port.Published)
		devices[devName] = map[string]string{
			"type":    "proxy",
			"listen":  fmt.Sprintf("%s:%s:%s", port.Protocol, hostIP, port.Published),
			"connect": fmt.Sprintf("%s:127.0.0.1:%d", port.Protocol, port.Target),
		}
	}

	ref, err := ParseDockerRef(service.Name, service.Image)
	if err != nil {
		return nil, "", err
	}

	imgInfo, err := c.EnsureImage(ref, imageServer, noImageProject)
	if err != nil {
		return nil, "", err
	}

	// Build instance creation request
	req := api.InstancesPost{
		Name: incusName,
		Type: api.InstanceTypeContainer,
		Source: api.InstanceSource{
			Type:        "image",
			Fingerprint: service.Image,
		},
		InstancePut: api.InstancePut{
			Config:  config,
			Devices: devices,
		},
	}

	// Create instance from image
	op, err := c.incus.CreateInstanceFromImage(imageServer, *imgInfo, req)
	if err != nil {
		return nil, "", err
	}

	err = op.Wait()
	if err != nil {
		return nil, "", fmt.Errorf("failed to create the instance for service %q: %w", service.Name, err)
	}

	instance, etag, err = c.Instance(incusName)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get the service instance for service %q after create: %w", service.Name, err)
	}

	var postErr error // Post error when something post create happens.

	// Create and attach volumes (after container exists so we can read oci.uid/oci.gid)
	if len(service.Volumes) > 0 {
		uid := instance.Config["oci.uid"]
		if uid == "" {
			uid = "0"
		}
		gid := instance.Config["oci.gid"]
		if gid == "" {
			gid = "0"
		}

	VOLUMES_LOOP:
		for _, vol := range service.Volumes {
			if vol.Type == "" {
				// Default to bind mount for backward compatibility (short syntax)
				if vol.Source != "" && (strings.HasPrefix(vol.Source, "/") || strings.HasPrefix(vol.Source, ".")) {
					vol.Type = "bind"
				} else if vol.Source != "" {
					vol.Type = "volume"
				}
			}

			switch vol.Type {
			case "volume":
				// Named volume - create Incus storage volume
				incusVolume, etag, err := c.EnsurePoolVolume(vol.Source, uid, gid, "")
				if err != nil {
					postErr = err
					break VOLUMES_LOOP
				}

				err = c.AttachPoolVolume(incusVolume, instance, etag, "", vol.Target, vol.ReadOnly)
				if err != nil {
					postErr = err
					break VOLUMES_LOOP
				}
			case "bind":
				// Bind mount - add disk device
				if err := c.AttachBindMount(instance, etag, vol.Source, vol.Target, vol.ReadOnly); err != nil {
					postErr = err
					break VOLUMES_LOOP
				}
			case "tmpfs":
				// tmpfs - skip for now, Incus handles this differently
				logger.Warn("tmpfs volumes not yet supported", "target", vol.Target)
			default:
				postErr = fmt.Errorf("Unknown volume type %q for service %q", vol.Type, service.Name)
			}
		}
	}

	if postErr != nil {
		if c.IsDebug() {
			logger.Debug("Error while post creating the service, keeping the instance: %w", "error", postErr)
		} else {
			if removeErr := c.RemoveInstance(instance, etag, true); removeErr != nil {
				return nil, "", fmt.Errorf("failed to remove the instance %q after %w happened: %w", instance.Name, postErr, removeErr)
			}
		}

		return nil, "", postErr
	}

	return instance, etag, nil
}
