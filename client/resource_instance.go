package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/gosimple/slug"
	incusClient "github.com/lxc/incus/v6/client"
	incusApi "github.com/lxc/incus/v6/shared/api"
)

// maxInstanceNameLen is the maximum length for Incus instance names.
// Incus allows up to 63 characters (DNS hostname limit).
const maxInstanceNameLen = 63

// InstanceConfig configures instance creation.
type InstanceConfig struct {
	// Type is the instance type (container or VM).
	Type incusApi.InstanceType

	// Full fetches the full instance.
	Full bool

	// Image is the OCI image to create the instance from.
	Image string

	// Ensured Resources that this instance depends on.
	Resources []Resource

	// Devices are devices attached before instance creation (networks, proxies).
	Devices []InstanceDevice

	// PostDevices are devices attached after instance creation (volumes needing UID/GID).
	PostDevices []InstanceDevice

	// Config contains Incus instance configuration options.
	Config map[string]string

	// ExtraDevices contains additional raw device configurations.
	ExtraDevices map[string]map[string]string
}

// GetConfig returns the configuration.
func (c *InstanceConfig) GetConfig() any {
	return c
}

// Instance represents an Incus container or virtual machine.
type Instance struct {
	*BaseResource

	client    *Client
	incusName string
	created   bool
	Config    InstanceConfig

	// State - nil means not ensured.
	IncusInstance *incusApi.Instance
	ETag          string

	// UID/GID extracted from container (for volume shifting).
	UID uint32
	GID uint32

	IncusInstanceFull *incusApi.InstanceFull
	IncusImageAlias   *incusApi.ImageAliasesEntry
}

func newInstance(c *Client, name string, configGetter Config) (*Instance, error) {
	if configGetter == nil {
		return nil, ErrUnknownConfig.WithKindName(KindInstance, name)
	}

	var config *InstanceConfig
	cConfig, ok := configGetter.GetConfig().(*InstanceConfig)
	if !ok {
		return nil, ErrUnknownConfig.WithKindName(KindInstance, name)
	}
	config = cConfig

	// Set defaults
	if config.Type == "" {
		config.Type = incusApi.InstanceTypeContainer
	}
	if config.Config == nil {
		config.Config = make(map[string]string)
	}

	inst := &Instance{
		BaseResource: NewBaseResource(KindInstance, name, PriorityInstance),
		client:       c,
		incusName:    sanitizeInstanceName(name),
		Config:       *config,
	}

	return inst, nil
}

// Instance returns an existing Instance resource or creates a new one.
func (c *Client) Instance(name string, config InstanceConfig) (*Instance, error) {
	// Check if already in store
	if existing := c.resources.Get(KindInstance, name); existing != nil {
		res, ok := existing.(*Instance)
		if !ok {
			return nil, ErrUnknownConfig.WithKindName(KindInstance, name)
		}

		return res, nil
	}

	inst, err := newInstance(c, name, &config)
	if err != nil {
		return nil, err
	}

	c.resources.Add(inst)
	return inst, nil
}

// String is for debugging.
func (r *Instance) String() string {
	return fmt.Sprintf("%v(%v)", r.kind, r.incusName)
}

// IncusName returns the sanitized instance name used in Incus.
func (r *Instance) IncusName() string {
	return r.incusName
}

// IsEnsured returns true if the instance has been fetched/created.
func (r *Instance) IsEnsured() bool {
	return r.IncusInstance != nil
}

// Created returns true if the instance was created during the last Ensure call.
func (r *Instance) Created() bool {
	return r.created
}

// HasFull returns true if the instance has a full instance.
func (r *Instance) HasFull() bool {
	return r.IncusImageAlias != nil && r.IncusInstanceFull != nil
}

// Ensure retrieves an existing instance or creates a new one if args.Create is true.
func (r *Instance) Ensure(opts ...Option) error {
	if r.IsEnsured() {
		return nil
	}

	options := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionEnsure, r, options, nil); err != nil {
			return err
		}
	}

	// Try to get existing
	// Check if exists
	instance, eTag, err := r.client.incus.GetInstance(r.incusName)
	if err == nil {
		err = r.ensured(instance, eTag)

		if r.client.hookAfter != nil {
			err = r.client.hookAfter(ActionEnsure, r, options, err)
		}

		return err
	}

	if !options.Create {
		return ErrNotFound.WithResource(r).Wrap(err)
	}

	err = r.create(opts...)

	if r.client.hookAfter != nil {
		err = r.client.hookAfter(ActionEnsure, r, options, err)
	}

	return err
}

func (r *Instance) ensured(instance *incusApi.Instance, eTag string) error {
	var err error
	r.IncusInstance = instance
	r.ETag = eTag
	r.UID, r.GID, err = extractUIDGID(instance)
	if err != nil {
		return fmt.Errorf("failed to extract uid/gid: %w", err)
	}

	if r.Config.Full {
		imageResource, err := r.client.Resource(KindImage, r.Config.Image, &ImageConfig{})
		if err != nil {
			return err
		}

		image, ok := imageResource.(*Image)
		if !ok {
			return ErrUnknown.WithResource(imageResource)
		}

		r.IncusImageAlias = image.IncusAlias

		full, _, err := r.client.incus.GetInstanceFull(r.IncusInstance.Name)
		if err != nil {
			return err
		}

		r.IncusInstanceFull = full
	}

	return nil
}

func (r *Instance) create(opts ...Option) error {
	// Validate bind mounts (reject if remote)
	if err := r.validateBindMounts(); err != nil {
		return err
	}

	if r.Config.Resources != nil {
		for _, rDep := range r.Config.Resources {
			if !rDep.IsEnsured() {
				return ErrDependencyNotEnsured.WithResource(rDep)
			}
		}
	}

	// Build pre-devices map
	devices, err := r.buildDevices()
	if err != nil {
		return err
	}

	imageResource, err := r.client.Resource(KindImage, r.Config.Image, &ImageConfig{})
	if err != nil {
		return err
	}

	image, ok := imageResource.(*Image)
	if !ok {
		return ErrUnknown.WithResource(imageResource)
	}

	// Copy image from cache to project (if not already there)
	if err := image.CopyTo(r.client.incus); err != nil {
		return fmt.Errorf("copying image to project: %w", err)
	}

	// Get image info from project (not cache)
	incusImage, _, err := r.client.incus.GetImage(image.IncusAlias.Target)
	if err != nil {
		return fmt.Errorf("getting image: %w", err)
	}

	// Create instance request
	req := incusApi.InstancesPost{
		Name: r.incusName,
		Type: r.Config.Type,
		Source: incusApi.InstanceSource{
			Type:        "image",
			Fingerprint: incusImage.Fingerprint,
		},
		InstancePut: incusApi.InstancePut{
			Description: fmt.Sprintf(r.client.Config().DescriptionFormat, r.Name()),
			Config:      r.Config.Config,
			Devices:     devices,
		},
	}

	options := NewOptions(opts...)

	// Create instance from project-local image
	op, err := r.client.incus.CreateInstanceFromImage(r.client.incus, *incusImage, req)
	if err = r.client.hookRemoteOperation(r.client.globalClient.Ctx, ActionEnsure, r, options, op, err); err != nil {
		return err
	}

	// Get instance to extract UID/GID
	instance, eTag, err := r.client.incus.GetInstance(r.incusName)
	if err != nil {
		return fmt.Errorf("fetching created instance: %w", err)
	}

	if err = r.ensured(instance, eTag); err != nil {
		return err
	}

	// Process post-devices (volumes needing UID/GID)
	if len(r.Config.PostDevices) > 0 {
		if err := r.attachPostDevices(); err != nil {
			return err
		}
	}

	r.created = true
	return nil
}

func (r *Instance) validateBindMounts() error {
	for _, dev := range r.Config.PostDevices {
		if dev.Config.DeviceType == InstanceDeviceTypeDisk {
			// Bind mount = no StorageVolumeConfig
			if dev.Config.Disk.StorageVolumeConfig == nil && r.client.IsRemote() {
				return ErrBindMountRemote
			}
		}
	}
	return nil
}

func (r *Instance) buildDevices() (map[string]map[string]string, error) {
	var devices map[string]map[string]string

	if r.Config.ExtraDevices != nil {
		devices = maps.Clone(r.Config.ExtraDevices)
	} else {
		devices = make(map[string]map[string]string)
	}

	profiles, err := ByKind[*Profile](r.Config.Resources, KindProfile)
	if err != nil {
		return nil, err
	}

	// Add Devices
	for _, dev := range r.Config.Devices {
		name, config, err := dev.ToIncusDevice()
		if err != nil {
			return nil, err
		}

		foundInProfile := false
		for _, profile := range profiles {
			foundInProfile = profile.HasDevice(name)
			if foundInProfile {
				break
			}
		}

		if foundInProfile {
			return nil, fmt.Errorf("device exists in profile %v", name)
		}

		devices[name] = config
	}

	if _, ok := devices["root"]; !ok {
		foundInProfile := false
		for _, profile := range profiles {
			foundInProfile = profile.HasDevice("root")
			if foundInProfile {
				break
			}
		}

		if !foundInProfile {
			devices["root"] = map[string]string{
				"type": "disk",
				"path": "/",
				"pool": "default",
			}
		}
	}

	return devices, nil
}

func (r *Instance) attachPostDevices() error {
	instance := r.IncusInstance

	for _, dev := range r.Config.PostDevices {
		// Ensure volume if needed
		if dev.Config.DeviceType == InstanceDeviceTypeDisk && dev.Config.Disk.StorageVolumeConfig != nil {
			volConfig := *dev.Config.Disk.StorageVolumeConfig
			volConfig.Shifted = true
			volConfig.UID = r.UID
			volConfig.GID = r.GID

			volI, err := r.client.Resource(KindStorageVolume, dev.Config.Disk.Source, &volConfig)
			if err != nil {
				return fmt.Errorf("creating volume: %w", err)
			}

			vol, ok := volI.(*StorageVolume)
			if !ok {
				return ErrUnsupportedAction.WithResource(volI)
			}

			if err := RunAction(volI, ActionEnsure); err != nil {
				return fmt.Errorf("ensuring volume: %w", err)
			}

			// Update disk config with volume details
			dev.Config.Disk.Source = vol.IncusName()
			dev.Config.Disk.StorageVolumeConfig.Pool = vol.Config.Pool
		}

		devName, devConfig, err := dev.ToIncusDevice()
		if err != nil {
			return err
		}

		instance.Devices[devName] = devConfig
	}

	// Update instance with post-devices
	op, err := r.client.incus.UpdateInstance(instance.Name, instance.Writable(), r.ETag)
	if err != nil {
		return fmt.Errorf("updating with devices: %w", err)
	}

	if err := op.Wait(); err != nil {
		return fmt.Errorf("waiting for update: %w", err)
	}

	// Refresh instance state
	instance, eTag, err := r.client.incus.GetInstance(r.incusName)
	if err != nil {
		return fmt.Errorf("refreshing: %w", err)
	}

	r.IncusInstance = instance
	r.ETag = eTag

	return nil
}

// Start starts the instance.
func (r *Instance) Start(opts ...Option) error {
	if !r.IsEnsured() {
		return ErrNotEnsured
	}

	options := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionStart, r, options, nil); err != nil {
			return err
		}
	}

	err := r.start()

	if r.client.hookAfter != nil {
		err = r.client.hookAfter(ActionStart, r, options, err)
	}

	return err
}

func (r *Instance) start() error {
	if r.IncusInstance.Status == "Running" {
		return nil // already running
	}

	op, err := r.client.incus.UpdateInstanceState(r.incusName, incusApi.InstanceStatePut{
		Action: "start",
	}, r.ETag)
	if err != nil {
		return fmt.Errorf("starting instance: %w", err)
	}

	if err := op.Wait(); err != nil {
		return err
	}

	// Refresh instance state
	instance, eTag, err := r.client.incus.GetInstance(r.incusName)
	if err != nil {
		return fmt.Errorf("refreshing instance after start: %w", err)
	}

	r.IncusInstance = instance
	r.ETag = eTag

	return nil
}

// Stop stops the instance.
func (r *Instance) Stop(opts ...Option) error {
	if !r.IsEnsured() {
		return ErrNotEnsured
	}

	options := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionStop, r, options, nil); err != nil {
			return err
		}
	}

	if r.IncusInstance.Status == "Stopped" {
		return nil // already stopped
	}

	op, err := r.client.incus.UpdateInstanceState(r.incusName, incusApi.InstanceStatePut{
		Action: "stop",
		Force:  options.Force,
	}, r.ETag)

	err = r.client.hookOperation(r.client.globalClient.Ctx, ActionStop, r, options, op, err)

	if r.client.hookAfter != nil {
		return r.client.hookAfter(ActionStop, r, options, err)
	}

	return err
}

// Delete removes the instance from Incus.
func (r *Instance) Delete(opts ...Option) error {
	if !r.IsEnsured() {
		return nil
	}

	options := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionDelete, r, options, nil); err != nil {
			return err
		}
	}

	op, err := r.client.incus.DeleteInstance(r.incusName)

	// Do the delete
	err = r.client.hookOperation(r.client.globalClient.Ctx, ActionDelete, r, options, op, err)

	if r.client.hookAfter != nil {
		if err := r.client.hookAfter(ActionDelete, r, options, err); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// Clear state
	r.IncusInstance = nil
	r.ETag = ""
	r.UID = 0
	r.GID = 0

	return nil
}

// Log streams the instance console log to the outputHandler.
func (r *Instance) Log(opts ...Option) error {
	if !r.IsEnsured() {
		return ErrNotEnsured
	}

	options := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionLog, r, options, nil); err != nil {
			return err
		}
	}

	err := r.log(options)

	if r.client.hookAfter != nil {
		err = r.client.hookAfter(ActionLog, r, options, err)
	}

	return err
}

func (r *Instance) log(options Options) error {
	outputHandler := r.client.globalClient.outputHandler
	if outputHandler == nil {
		return nil
	}

	return r.logStream(options, outputHandler)
}

// logStream streams the console using WebSocket.
// If options.Follow is true, it streams until context is cancelled.
// Otherwise, it streams the current log buffer and exits after a brief timeout.
func (r *Instance) logStream(options Options, outputHandler func(Action, Resource, []byte)) error {
	ctx := r.client.globalClient.Ctx

	// For non-follow mode, use a short timeout to exit after initial data
	if !options.Follow {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
	}

	// Channel to signal disconnect
	consoleDisconnect := make(chan bool)

	// Terminal that writes to outputHandler
	terminal := &logTerminal{
		resource:      r,
		outputHandler: outputHandler,
	}

	// Connect to console WebSocket
	req := incusApi.InstanceConsolePost{
		Type:  "console",
		Force: true, // Take over existing console connections
	}

	// Control handler - required by Incus API, but we don't need window resize
	controlHandler := func(conn *websocket.Conn) {
		<-ctx.Done()
		closeMsg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
		_ = conn.WriteMessage(websocket.CloseMessage, closeMsg)
	}

	args := &incusClient.InstanceConsoleArgs{
		Terminal:          terminal,
		Control:           controlHandler,
		ConsoleDisconnect: consoleDisconnect,
	}

	op, err := r.client.incus.ConsoleInstance(r.incusName, req, args)
	if err != nil {
		return fmt.Errorf("connecting to console: %w", err)
	}

	// Handle context cancellation
	go func() {
		<-ctx.Done()
		close(consoleDisconnect)
	}()

	// Wait for operation to complete using hookOperation
	err = r.client.hookOperation(ctx, ActionLog, r, options, op, err)

	// Context cancellation (including timeout) is not an error
	if ctx.Err() != nil {
		return nil
	}

	if err != nil {
		return fmt.Errorf("console streaming: %w", err)
	}

	return nil
}

// logTerminal implements io.ReadWriteCloser for console streaming.
type logTerminal struct {
	resource      *Instance
	outputHandler func(Action, Resource, []byte)
}

func (t *logTerminal) Write(p []byte) (int, error) {
	t.outputHandler(ActionLog, t.resource, p)
	return len(p), nil
}

func (t *logTerminal) Read(_ []byte) (int, error) {
	select {} // Block forever - we never send input
}

// Close implements io.Closer.
func (t *logTerminal) Close() error {
	return nil
}

var (
	_ Resource   = (*Instance)(nil)
	_ EnsureAble = (*Instance)(nil)
	_ StartAble  = (*Instance)(nil)
	_ StopAble   = (*Instance)(nil)
	_ DeleteAble = (*Instance)(nil)
	_ LogAble    = (*Instance)(nil)
)

// extractUIDGID extracts UID and GID from a container instance.
func extractUIDGID(instance *incusApi.Instance) (uint32, uint32, error) {
	if incusApi.InstanceType(instance.Type) != incusApi.InstanceTypeContainer {
		return 0, 0, nil
	}

	uid64, err := strconv.ParseUint(instance.Config["oci.uid"], 10, 32)
	if err != nil {
		return 0, 0, err
	}

	gid64, err := strconv.ParseUint(instance.Config["oci.gid"], 10, 32)
	if err != nil {
		return 0, 0, err
	}

	return uint32(uid64), uint32(gid64), nil
}

// sanitizeInstanceName converts a string to a valid Incus instance name.
// Converts to lowercase, replaces special chars and underscores with hyphens.
// Names exceeding 63 chars are replaced with a 32-char hex hash for DNS compatibility.
func sanitizeInstanceName(name string) string {
	// slug.Make converts to lowercase, replaces special chars with hyphens
	// but keeps underscores, so we replace them explicitly
	safe := slug.Make(name)
	safe = strings.ReplaceAll(safe, "_", "-")

	if len(safe) > maxInstanceNameLen {
		// Fall back to hash for very long names
		sha256sum := sha256.Sum256([]byte(name))
		safe = hex.EncodeToString(sha256sum[:16]) // 32 hex chars
	}

	return safe
}
