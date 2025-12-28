package client

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/distribution/reference"
	incusClient "github.com/lxc/incus/v6/client"
	incusApi "github.com/lxc/incus/v6/shared/api"
)

// Resource creation priorities using powers of 2 for clear separation.
// Lower priority values are created first and deleted last.
// Higher priority values are created last and deleted first (reverse dependency order).
const (
	PriorityProject  = 1 << 8  // 256 - Infrastructure (created first, deleted last)
	PriorityProfile  = 1 << 9  // 512 - Base resources
	PriorityImage    = 1 << 9  // 512 - Base resources (same tier as profiles)
	PriorityNetwork  = 1 << 9  // 512 - Base resources (same tier as profiles)
	PriorityVolume   = 1 << 10 // 1024 - Storage (depends on networks)
	PriorityInstance = 1 << 11 // 2048 - Compute (depends on everything above)
	PrioritySnapshot = 1 << 12 // 4096 - Instance-dependent (created last, deleted first)
)

// Resource provides common fields for all Incus resources.
type Resource struct {
	// Kind of the resource
	kind string

	// The project
	project *ClientProject

	// The name
	name string

	// The sanitized Incus name, empty means no sanitization required
	incusName string

	// Pre-configured logger with resource, name and incus_name fields
	logger *slog.Logger
}

func newResource(kind string, project *ClientProject, name string) Resource {
	r := Resource{kind: kind, project: project, name: name}

	r.logger = project.Logger().With("resource", r.kind, "name", r.name)
	return r
}

// Kind returns the kind.
func (r *Resource) Kind() string {
	return r.kind
}

// Name returns the name.
func (r *Resource) Name() string {
	return r.name
}

// IncusName returns the sanitized name, an empty result means no sanitization required.
func (r *Resource) IncusName() string {
	return r.incusName
}

// ProfileConfig configures profile creation from a source profile.
type ProfileConfig struct {
	SourceServer  *incusClient.ProtocolIncus
	SourceProject string
	SourceProfile string
}

// ProfileOperation tracks the current state of a profile resource.
type ProfileOperation struct {
	ETag    string
	Profile *incusApi.Profile
}

// Profile represents an Incus profile resource.
type Profile struct {
	Resource

	Config ProfileConfig

	Operation ProfileOperation
}

// Profile returns an existing or creates a new Profile resource.
func (c *ClientProject) Profile(name string, config ProfileConfig) (*Profile, error) {
	if r, ok := c.profiles.Get(name); ok {
		return r, nil
	}

	if config.SourceProject == "" && config.SourceProfile == "" {
		config.SourceProject = "default"
		config.SourceProfile = "default"
	}

	return c.profiles.Add(&Profile{Resource: newResource("profile", c, name), Config: config, Operation: ProfileOperation{}}), nil
}

// Priority returns the deletion priority for Profile resources.
func (*Profile) Priority() int {
	return PriorityProfile
}

// Get retrieves an existing profile from Incus.
func (r *Profile) Get() error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Getting", "state", r.Operation)
	err := r.get()
	if err != nil {
		return r.project.t.AddError(fmt.Errorf("failed to get %s %q: %w", r.kind, r.name, err))
	}
	return nil
}

// Create creates a new profile in Incus.
func (r *Profile) Create() error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Creating", "state", r.Operation)
	err := r.create()
	if err != nil {
		return r.project.t.AddError(fmt.Errorf("failed to create %s %q: %w", r.kind, r.name, err))
	}
	r.project.t.Add(r)
	return nil
}

// Ensure ensures the profile exists, optionally creating it.
func (r *Profile) Ensure(create bool) error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Ensuring")
	if err := r.get(); err == nil {
		return nil
	}

	if !create {
		return r.project.t.AddError(fmt.Errorf("%s %q does not exist and create is disabled", r.kind, r.name))
	}

	return r.Create()
}

// Delete deletes the profile from Incus.
func (r *Profile) Delete(timeout int, force bool) error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Deleting", "force", force)
	err := r.delete(timeout, force)
	if err != nil {
		return fmt.Errorf("failed to delete %s %q: %w", r.kind, r.name, err)
	}
	return nil
}

// get retrieves an existing profile from Incus.
// If the profile exists but has no devices, it copies devices from the source profile.
// Existing profiles with devices are not validated against the source.
func (r *Profile) get() error {
	if r.Operation.Profile != nil {
		return nil
	}

	profile, eTag, err := r.project.incus.GetProfile(r.name)
	if err != nil {
		return err
	}

	// If profile already has devices, assume it's already configured correctly.
	// No validation is performed on existing profiles.
	if len(profile.Devices) > 0 {
		r.Operation.ETag = eTag
		r.Operation.Profile = profile
		return nil
	}

	sourceServer := r.Config.SourceServer
	if sourceServer == nil {
		sourceServer = r.project.GlobalIncus()
	}

	sourceProfile, _, err := sourceServer.GetProfile(r.Config.SourceProfile)
	if err != nil {
		return fmt.Errorf("while getting source profile %q from project %q: %w", r.Config.SourceProfile, r.Config.SourceProject, err)
	}

	profile.Devices = sourceProfile.Devices
	err = r.project.incus.UpdateProfile(r.name, profile.Writable(), eTag)
	if err != nil {
		return fmt.Errorf("updating: %w", err)
	}

	r.Operation.ETag = eTag
	r.Operation.Profile = profile

	return nil
}

func (r *Profile) create() error {
	if r.Operation.Profile != nil {
		return nil
	}

	sourceServer := r.Config.SourceServer
	if sourceServer == nil {
		sourceServer = r.project.GlobalIncus()
	}

	sourceProfile, _, err := sourceServer.GetProfile(r.Config.SourceProfile)
	if err != nil {
		return fmt.Errorf("while getting source profile %q from project %q: %w", r.Config.SourceProfile, r.Config.SourceProject, err)
	}

	postArgs := incusApi.ProfilesPost{
		ProfilePut: incusApi.ProfilePut{
			Config:      sourceProfile.Config,
			Devices:     sourceProfile.Devices,
			Description: fmt.Sprintf(r.project.config.DescriptionFormat, r.name),
		},
		Name: r.name,
	}

	err = r.project.incus.CreateProfile(postArgs)
	if err != nil {
		return err
	}

	profile, eTag, err := r.project.incus.GetProfile(r.name)
	if err != nil {
		return err
	}

	r.Operation.ETag = eTag
	r.Operation.Profile = profile

	return nil
}

func (r *Profile) delete(_ int, _ bool) error {
	if r.Operation.Profile == nil {
		return ErrEmpty
	}

	err := r.project.client.incus.DeleteProfile(r.name)
	if err != nil {
		return err
	}

	r.Operation = ProfileOperation{}

	return nil
}

// ImageConfig is reserved for future use.
type ImageConfig struct{}

// ImageOperation tracks the current state of an image resource.
type ImageOperation struct {
	Remote        string
	ImageNoRemote string

	Source incusClient.ImageServer
	Cache  incusClient.InstanceServer

	ETag        string
	Fingerprint string
}

// Image represents an OCI image copied to Incus.
type Image struct {
	Resource

	Config ImageConfig

	Operation ImageOperation
}

// Image returns an existing or creates a new Image resource.
func (c *ClientProject) Image(name string, options ImageConfig) (*Image, error) {
	if r, ok := c.images.Get(name); ok {
		return r, nil
	}

	r := &Image{Resource: newResource("image", c, name), Config: options, Operation: ImageOperation{}}
	if err := r.sanitize(); err != nil {
		return nil, err
	}

	return c.images.Add(r), nil
}

// Priority returns the deletion priority for Image resources.
func (*Image) Priority() int {
	return PriorityImage
}

// Get retrieves an existing image from Incus.
func (r *Image) Get() error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Getting", "state", r.Operation)
	err := r.get()
	if err != nil {
		return r.project.t.AddError(fmt.Errorf("failed to get %s %q: %w", r.kind, r.name, err))
	}
	return nil
}

// Create creates a new image in Incus by copying from the image server.
func (r *Image) Create() error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Creating", "state", r.Operation)
	err := r.create()
	if err != nil {
		return r.project.t.AddError(fmt.Errorf("failed to create %s %q: %w", r.kind, r.name, err))
	}
	r.project.t.Add(r)
	return nil
}

// Ensure ensures the image exists, optionally creating it.
func (r *Image) Ensure(create bool) error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Ensuring")
	if err := r.get(); err == nil {
		return nil
	}

	if !create {
		return r.project.t.AddError(fmt.Errorf("%s %q does not exist and create is disabled", r.kind, r.name))
	}

	return r.Create()
}

// Delete deletes the image from Incus.
func (r *Image) Delete(timeout int, force bool) error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Deleting", "force", force)
	err := r.delete(timeout, force)
	if err != nil {
		return fmt.Errorf("failed to delete %s %q: %w", r.kind, r.name, err)
	}
	return nil
}

func (r *Image) sanitize() error {
	if r.Operation.Fingerprint != "" {
		return nil
	}

	ref, err := reference.ParseDockerRef(r.name)
	if err != nil {
		return err
	}

	r.Operation.Remote = reference.Domain(ref)
	if r.Operation.Remote == "localhost" {
		// Handle podman style "localhost" images.
		r.Operation.Remote = "local"
	}

	r.Operation.Cache = r.project.imageCache

	r.Operation.ImageNoRemote = strings.TrimLeft(ref.String(), r.Operation.Remote+"/")

	r.incusName = r.name

	return nil
}

// SetSourceServer configures the source image server for this image.
// Returns the image for method chaining.
func (r *Image) SetSourceServer(s incusClient.ImageServer) *Image {
	r.Operation.Source = s

	return r
}

func (r *Image) get() error {
	if r.Operation.Fingerprint != "" {
		return nil
	}

	alias, eTag, err := r.Operation.Cache.GetImageAlias(r.incusName)
	if err != nil {
		r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "get failed", "error", err)
		return err
	}

	r.Operation.ETag = eTag
	r.Operation.Fingerprint = alias.Target

	return nil
}

func (r *Image) create() error {
	if r.Operation.Fingerprint != "" {
		return nil
	}

	if r.Operation.Source == nil {
		return fmt.Errorf("ImageSource not configured for image %q", r.name)
	}

	imgInfo := &incusApi.Image{}
	imgInfo.Fingerprint = r.Operation.ImageNoRemote
	imgInfo.Public = true // Needed to copy from public image servers.

	copyArgs := incusClient.ImageCopyArgs{
		Aliases:    []incusApi.ImageAlias{{Name: r.incusName}},
		AutoUpdate: true,
		Public:     false,
		Mode:       "pull",
	}

	// Do the copy
	op, err := r.Operation.Cache.CopyImage(r.Operation.Source, *imgInfo, &copyArgs)
	if err != nil {
		return err
	}

	err = op.Wait()
	if err != nil {
		return err
	}

	alias, eTag, err := r.Operation.Cache.GetImageAlias(r.incusName)
	if err != nil {
		return err
	}

	r.Operation.Cache = r.project.incus
	r.Operation.ETag = eTag
	r.Operation.Fingerprint = alias.Target

	return nil
}

func (r *Image) delete(_ int, _ bool) error {
	if r.Operation.Fingerprint == "" {
		return ErrEmpty
	}

	op, err := r.Operation.Cache.DeleteImage(r.Operation.Fingerprint)
	if err != nil {
		return err
	}

	err = op.Wait()
	if err != nil {
		return err
	}

	r.Operation = ImageOperation{}

	return nil
}

// PoolVolumeConfig configures storage volume creation.
type PoolVolumeConfig struct {
	Pool string
}

// PoolVolumeOperation tracks the current state of a pool volume.
// UID and GID are set via configure() before creation for proper ownership shifting.
type PoolVolumeOperation struct {
	UID string
	GID string

	ETag   string
	Volume *incusApi.StorageVolume
}

// PoolVolume represents a custom storage volume with UID/GID shifting.
type PoolVolume struct {
	Resource

	Config PoolVolumeConfig

	Operation PoolVolumeOperation
}

// PoolVolume returns an existing or creates a new PoolVolume resource.
func (c *ClientProject) PoolVolume(name string, config PoolVolumeConfig) (*PoolVolume, error) {
	if r, ok := c.poolVolumes.Get(name); ok {
		return r, nil
	}

	if config.Pool == "" {
		config.Pool = c.config.DefaultStoragePool
	}

	r := &PoolVolume{Resource: newResource("pool-volume", c, name), Config: config, Operation: PoolVolumeOperation{}}
	if err := r.sanitize(); err != nil {
		return nil, err
	}

	return c.poolVolumes.Add(r), nil
}

// Priority returns the deletion priority for PoolVolume resources.
func (*PoolVolume) Priority() int {
	return PriorityVolume
}

// Get retrieves an existing pool volume from Incus.
func (r *PoolVolume) Get() error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Getting", "state", r.Operation)
	err := r.get()
	if err != nil {
		return r.project.t.AddError(fmt.Errorf("failed to get %s %q: %w", r.kind, r.name, err))
	}
	return nil
}

// Create creates a new pool volume in Incus.
func (r *PoolVolume) Create() error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Creating", "state", r.Operation)
	err := r.create()
	if err != nil {
		return r.project.t.AddError(fmt.Errorf("failed to create %s %q: %w", r.kind, r.name, err))
	}
	r.project.t.Add(r)
	return nil
}

// Ensure ensures the pool volume exists, optionally creating it.
func (r *PoolVolume) Ensure(create bool) error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Ensuring")
	if err := r.get(); err == nil {
		return nil
	}

	if !create {
		return r.project.t.AddError(fmt.Errorf("%s %q does not exist and create is disabled", r.kind, r.name))
	}

	return r.Create()
}

// Delete deletes the pool volume from Incus.
func (r *PoolVolume) Delete(timeout int, force bool) error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Deleting", "force", force)
	err := r.delete(timeout, force)
	if err != nil {
		return fmt.Errorf("failed to delete %s %q: %w", r.kind, r.name, err)
	}
	return nil
}

func (r *PoolVolume) sanitize() error {
	if r.incusName != "" {
		return nil
	}

	r.incusName = r.project.name + "-" + r.name
	return nil
}

// configure sets UID/GID for the volume before creation.
// Must be called before Create(). Instances call this to set ownership based on their OCI config.
func (r *PoolVolume) configure(uid, gid string) error {
	if r.Operation.Volume != nil {
		return errors.New("already created")
	}

	r.Operation.UID = uid
	r.Operation.GID = gid

	return nil
}

func (r *PoolVolume) get() error {
	if r.Operation.Volume != nil {
		return nil
	}

	volume, eTag, err := r.project.incus.GetStoragePoolVolume(r.Config.Pool, "custom", r.incusName)
	if err != nil {
		return err
	}

	if volume.Config["security.shifted"] != "true" || volume.Config["initial.uid"] != r.Operation.UID || volume.Config["initial.gid"] != r.Operation.GID {
		return errors.New("failed to validate")
	}

	r.Operation.ETag = eTag
	r.Operation.Volume = volume

	return nil
}

func (r *PoolVolume) create() error {
	if r.Operation.Volume != nil {
		return nil
	}

	volReq := incusApi.StorageVolumesPost{
		Name:        r.incusName,
		Type:        "custom",
		ContentType: "filesystem",
		StorageVolumePut: incusApi.StorageVolumePut{
			Config: map[string]string{
				"security.shifted": "true",
				"initial.uid":      r.Operation.UID,
				"initial.gid":      r.Operation.GID,
			},
		},
	}

	if err := r.project.incus.CreateStoragePoolVolume(r.Config.Pool, volReq); err != nil {
		return err
	}

	volume, eTag, err := r.project.incus.GetStoragePoolVolume(r.Config.Pool, "custom", r.incusName)
	if err != nil {
		return err
	}

	r.Operation.ETag = eTag
	r.Operation.Volume = volume

	return nil
}

func (r *PoolVolume) delete(_ int, _ bool) error {
	if r.Operation.Volume == nil {
		return ErrEmpty
	}

	err := r.project.incus.DeleteStoragePoolVolume(r.Config.Pool, "custom", r.incusName)
	if err != nil {
		return err
	}

	r.Operation = PoolVolumeOperation{}

	return nil
}

// NetworkOperation tracks the current state of a network resource.
type NetworkOperation struct {
	ETag    string
	Network *incusApi.Network
}

// Network represents a bridge network with deterministic naming.
type Network struct {
	Resource

	Operation NetworkOperation
}

// Network returns an existing or creates a new Network resource.
func (c *ClientProject) Network(name string) (*Network, error) {
	if r, ok := c.networks.Get(name); ok {
		return r, nil
	}

	r := &Network{Resource: newResource("network", c, name), Operation: NetworkOperation{}}
	if err := r.sanitize(); err != nil {
		return nil, err
	}

	return c.networks.Add(r), nil
}

// Priority returns the deletion priority for Network resources.
func (*Network) Priority() int {
	return PriorityNetwork
}

// Get retrieves an existing network from Incus.
func (r *Network) Get() error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Getting", "state", r.Operation)
	err := r.get()
	if err != nil {
		return r.project.t.AddError(fmt.Errorf("failed to get %s %q: %w", r.kind, r.name, err))
	}
	return nil
}

// Create creates a new network in Incus.
func (r *Network) Create() error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Creating", "state", r.Operation)
	err := r.create()
	if err != nil {
		return r.project.t.AddError(fmt.Errorf("failed to create %s %q: %w", r.kind, r.name, err))
	}
	r.project.t.Add(r)
	return nil
}

// Ensure ensures the network exists, optionally creating it.
func (r *Network) Ensure(create bool) error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Ensuring")
	if err := r.get(); err == nil {
		return nil
	}

	if !create {
		return r.project.t.AddError(fmt.Errorf("%s %q does not exist and create is disabled", r.kind, r.name))
	}

	return r.Create()
}

// Delete deletes the network from Incus.
func (r *Network) Delete(timeout int, force bool) error {
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Deleting", "force", force)
	err := r.delete(timeout, force)
	if err != nil {
		return fmt.Errorf("failed to delete %s %q: %w", r.kind, r.name, err)
	}
	return nil
}

func (r *Network) sanitize() error {
	if r.incusName != "" {
		return nil
	}

	r.incusName = networkNameForProject(r.project.name, r.project.config.NetworkPrefix, r.name)
	return nil
}

func (r *Network) get() error {
	if r.Operation.Network != nil {
		return nil
	}

	network, eTag, err := r.project.incus.GetNetwork(r.incusName)
	if err != nil {
		return err
	}

	r.Operation.ETag = eTag
	r.Operation.Network = network

	return nil
}

func (r *Network) create() error {
	if r.Operation.Network != nil {
		return nil
	}

	req := incusApi.NetworksPost{
		Name: r.incusName,
		Type: "bridge",
	}

	err := r.project.incus.CreateNetwork(req)
	if err != nil {
		return err
	}

	network, eTag, err := r.project.incus.GetNetwork(r.incusName)
	if err != nil {
		return err
	}

	r.Operation.ETag = eTag
	r.Operation.Network = network

	return nil
}

func (r *Network) delete(_ int, _ bool) error {
	if r.Operation.Network == nil {
		return ErrEmpty
	}

	err := r.project.incus.DeleteNetwork(r.incusName)
	if err != nil {
		return err
	}

	r.Operation = NetworkOperation{}

	return nil
}

// InstancePoolVolume attaches a storage volume to an instance.
type InstancePoolVolume struct {
	Path     string
	ReadOnly bool
	Volume   *PoolVolume
}

// InstanceBindMount attaches a host directory to an instance.
// Only supported for local connections, not remote.
type InstanceBindMount struct {
	Source   string
	Path     string
	ReadOnly bool
}

// InstancePortProxy forwards host ports to instance ports.
type InstancePortProxy struct {
	Protocol string
	HostIP   string
	Listen   uint32
	Connect  uint32
}

// InstanceConfig configures instance creation.
type InstanceConfig struct {
	Type  incusApi.InstanceType
	Image *Image

	Networks    map[string]*Network
	PortProxies []InstancePortProxy

	PoolVolumes []InstancePoolVolume
	BindMounts  []InstanceBindMount

	Config  map[string]string
	Devices map[string]map[string]string
}

// InstanceOperation tracks the current state of an instance resource.
type InstanceOperation struct {
	ETag     string
	Instance *incusApi.Instance
}

// Instance represents an Incus container or VM.
type Instance struct {
	Resource

	Options InstanceConfig

	Operation InstanceOperation
}

// Instance returns an existing or creates a new Instance resource.
func (c *ClientProject) Instance(name string, options InstanceConfig) (*Instance, error) {
	if r, ok := c.instances.Get(name); ok {
		return r, nil
	}

	if options.Type == "" {
		options.Type = incusApi.InstanceTypeContainer
	}

	if options.Networks == nil {
		options.Networks = map[string]*Network{}
	}

	if options.PortProxies == nil {
		options.PortProxies = []InstancePortProxy{}
	}

	if options.PoolVolumes == nil {
		options.PoolVolumes = []InstancePoolVolume{}
	}

	if options.BindMounts == nil {
		options.BindMounts = []InstanceBindMount{}
	}

	r := &Instance{Resource: newResource("instance", c, name), Options: options, Operation: InstanceOperation{}}
	if err := r.sanitize(); err != nil {
		return nil, err
	}

	return c.instances.Add(r), nil
}

// Priority returns the deletion priority for Instance resources.
func (*Instance) Priority() int {
	return PriorityInstance
}

// Get retrieves an existing instance from Incus.
func (r *Instance) Get() error {
	err := r.get()
	if err != nil {
		r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Get", "error", err)
		return r.project.t.AddError(fmt.Errorf("failed to get %s %q: %w", r.kind, r.name, err))
	}

	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Got")
	return nil
}

// Create creates a new instance in Incus.
func (r *Instance) Create() error {
	err := r.create()
	if err != nil {
		r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Create", "error", err)
		return r.project.t.AddError(fmt.Errorf("failed to create %s %q: %w", r.kind, r.name, err))
	}
	r.project.t.Add(r)

	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Created")
	return nil
}

// Ensure ensures the instance exists, optionally creating it.
func (r *Instance) Ensure(create bool) error {
	if err := r.get(); err == nil {
		return nil
	}

	if !create {
		return r.project.t.AddError(fmt.Errorf("%s %q does not exist and create is disabled", r.kind, r.name))
	}

	return r.Create()
}

// Delete deletes the instance from Incus.
func (r *Instance) Delete(timeout int, force bool) error {
	err := r.delete(timeout, force)
	if err != nil {
		r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Delete", "timeout", timeout, "force", force, "error", err)
		return fmt.Errorf("failed to delete %s %q: %w", r.kind, r.name, err)
	}
	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Deleted", "timeout", timeout, "force", force)
	return nil
}

func (r *Instance) sanitize() error {
	if r.incusName != "" {
		return nil
	}

	// Bind mounts require local socket access to the host filesystem
	if r.project.client.IsRemote() && len(r.Options.BindMounts) > 0 {
		return errors.New("bind mounts only locally supported")
	}

	r.incusName = sanitizeInstanceName(r.name)

	return nil
}

func (r *Instance) create() error {
	if r.Operation.Instance != nil {
		return nil
	}

	if r.Options.Image == nil {
		return errors.New("instances without an image are unsupported")
	}

	devices := r.Options.Devices
	if devices == nil {
		devices = make(map[string]map[string]string)
	}

	// Network devices
	for devName, net := range r.Options.Networks {
		if err := net.Ensure(true); err != nil {
			return err
		}

		devices[devName] = map[string]string{
			"type":    "nic",
			"network": net.incusName,
			"name":    devName,
		}
	}

	// Port proxies
	for _, port := range r.Options.PortProxies {
		hostIP := port.HostIP
		if hostIP == "" {
			hostIP = "0.0.0.0"
		}
		devName := fmt.Sprintf("proxy-%s-%s", hostIP, port.Listen)
		devices[devName] = map[string]string{
			"type":    "proxy",
			"listen":  fmt.Sprintf("%s:%s:%s", port.Protocol, hostIP, port.Listen),
			"connect": fmt.Sprintf("%s:127.0.0.1:%d", port.Protocol, port.Connect),
		}
	}

	// Image
	if err := r.Options.Image.Ensure(true); err != nil {
		return err
	}

	imageState := r.Options.Image.Operation

	imgInfo, _, err := imageState.Cache.GetImage(imageState.Fingerprint)

	// Build instance creation request
	req := incusApi.InstancesPost{
		Name: r.incusName,
		Type: r.Options.Type,
		Source: incusApi.InstanceSource{
			Type:        "image",
			Fingerprint: imageState.Fingerprint,
		},
		InstancePut: incusApi.InstancePut{
			Description: fmt.Sprintf(r.project.config.DescriptionFormat, r.name),
			Config:      r.Options.Config,
			Devices:     devices,
		},
	}

	r.logger.Log(r.project.Ctx, r.project.config.TraceLevel, "Creating from", "image_state", imageState)

	// Create instance from image
	op, err := r.project.incus.CreateInstanceFromImage(imageState.Cache, *imgInfo, req)
	if err != nil {
		return err
	}

	if err := op.Wait(); err != nil {
		return err
	}

	instance, eTag, err := r.project.incus.GetInstance(r.incusName)
	if err != nil {
		return err
	}

	var postErr error

	// Create and attach volumes after instance exists so we can read oci.uid/oci.gid
	// from the image config for proper ownership shifting
	if len(r.Options.PoolVolumes) > 0 {
		uid := instance.Config["oci.uid"]
		if uid == "" {
			uid = "0"
		}
		gid := instance.Config["oci.gid"]
		if gid == "" {
			gid = "0"
		}

		for _, iVol := range r.Options.PoolVolumes {
			vol := iVol.Volume
			if err := vol.configure(uid, gid); err != nil {
				postErr = errors.Join(postErr, err)
				continue
			}

			if err := vol.Ensure(true); err != nil {
				postErr = errors.Join(postErr, err)
				continue
			}

			device := map[string]string{
				"type":   "disk",
				"pool":   vol.Config.Pool,
				"source": vol.incusName,
				"path":   iVol.Path,
			}

			if iVol.ReadOnly {
				device["readonly"] = "true"
			}

			instance.Devices["vol-"+vol.incusName] = device
		}

		for _, vol := range r.Options.BindMounts {
			device := map[string]string{
				"type":   "disk",
				"source": vol.Source,
				"path":   vol.Path,
				"shift":  "true", // Enable uid/gid shifting for bind mounts
			}

			if vol.ReadOnly {
				device["readonly"] = "true"
			}

			// Use target path as base, sanitize for device name
			name := strings.ReplaceAll(vol.Path, string(filepath.Separator), "-")
			name = strings.TrimPrefix(name, "-")
			if name == "" {
				name = "root"
			}
			devName := fmt.Sprintf("bind-%s", name)

			instance.Devices[devName] = device
		}
	}

	if postErr != nil {
		return postErr
	}

	uop, err := r.project.incus.UpdateInstance(instance.Name, instance.Writable(), eTag)
	if err != nil {
		return err
	}

	err = uop.Wait()
	if err != nil {
		return err
	}

	r.Operation.ETag = eTag
	r.Operation.Instance = instance

	return nil
}

func (r *Instance) get() error {
	if r.Operation.Instance != nil {
		return nil
	}

	instance, eTag, err := r.project.incus.GetInstance(r.incusName)
	if err != nil {
		return err
	}

	r.Operation.ETag = eTag
	r.Operation.Instance = instance

	return nil
}

// Start starts the instance.
func (r *Instance) Start(timeout int) error {
	if r.Operation.Instance == nil {
		if err := r.Get(); err != nil {
			return err
		}
	}

	req := incusApi.InstanceStatePut{
		Action:  "start",
		Timeout: timeout,
	}

	op, err := r.project.incus.UpdateInstanceState(r.Operation.Instance.Name, req, r.Operation.ETag)
	if err != nil {
		return fmt.Errorf("failed to start %s %q: %w", r.kind, r.name, err)
	}

	if err := op.Wait(); err != nil {
		return fmt.Errorf("failed to start %s %q: %w", r.kind, r.name, err)
	}

	return nil
}

// Full retrieves the full instance state including snapshots and backups.
func (r *Instance) Full() (*incusApi.InstanceFull, string, error) {
	if r.Operation.Instance == nil {
		if err := r.Get(); err != nil {
			return nil, "", err
		}
	}

	return r.project.incus.GetInstanceFull(r.Operation.Instance.Name)
}

// Stop forcefully stops the instance.
func (r *Instance) Stop() error {
	if r.Operation.Instance == nil {
		if err := r.Get(); err != nil {
			return err
		}
	}

	if r.Operation.Instance.Status != "Running" {
		return fmt.Errorf("%s %q is not running", r.kind, r.name)
	}

	req := incusApi.InstanceStatePut{
		Action:  "stop",
		Timeout: 30,
		Force:   true,
	}
	op, err := r.project.incus.UpdateInstanceState(r.Operation.Instance.Name, req, r.Operation.ETag)
	if err != nil {
		return fmt.Errorf("failed to stop %s %q: %w", r.kind, r.name, err)
	}
	if err := op.Wait(); err != nil {
		return fmt.Errorf("failed to stop %s %q: %w", r.kind, r.name, err)
	}

	return nil
}

func (r *Instance) delete(_ int, force bool) error {
	if r.Operation.Instance == nil {
		return ErrEmpty
	}

	// Stop first if running
	if r.Operation.Instance.Status == "Running" {
		if !force {
			return errors.New("not running")
		}
		err := r.Stop()
		if err != nil {
			return err
		}
	}

	op, err := r.project.incus.DeleteInstance(r.Operation.Instance.Name)
	if err != nil {
		return err
	}

	err = op.Wait()
	if err != nil {
		return err
	}

	r.Operation = InstanceOperation{}

	return err
}
