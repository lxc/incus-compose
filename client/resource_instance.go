package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path"
	"slices"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	incusClient "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"
)

// InstanceSecret represents a secret to be pushed into the instance.
type InstanceSecret struct {
	Source  string // secret name
	Target  string // path in container (default: /run/secrets/{source})
	Content []byte // file content
	UID     int64
	GID     int64
	Mode    int // default: 0400
}

// InstanceFile represents a file to push to an instance after creation.
type InstanceFile struct {
	// Give either "File" or "Content"
	File    string
	Content io.ReadSeekCloser

	UID     int64
	GID     int64
	Mode    int
	NoMKDir bool
	DirMode int
}

// InstanceConfig configures instance creation.
type InstanceConfig struct {
	// ServiceName represents the compose service name.
	ServiceName string

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

	// PostStartDevices are devices attached after the instance is started.
	// Use for devices that require a running instance, e.g. NAT proxy (needs container IP).
	PostStartDevices []InstanceDevice

	// Secrets are files pushed into the instance after start.
	Secrets []InstanceSecret

	// Files are files pushed into the instance after creation.
	// Map key is the target path in the instance.
	Files map[string]InstanceFile

	// Config contains Incus instance configuration options.
	Config map[string]string

	// ExtraDevices contains additional raw device configurations.
	ExtraDevices map[string]map[string]string

	// Dependencies maps dependency Incus instance names to the required health
	// status (HealthStatusHealthy, HealthStatusStarting, HealthStatusUnhealthy).
	// Instance.Start() blocks until all dependencies reach the required status.
	Dependencies map[string]string
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

	// image is for internal use in create operations.
	image *Image

	// State - nil means not ensured.
	IncusInstance *incusApi.Instance
	ETag          string

	// UID/GID extracted from container (for volume shifting).
	UID uint64
	GID uint64

	IncusInstanceFull *incusApi.InstanceFull
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
		incusName:    SanitizeIncusName(name, -1),
		Config:       *config,
	}

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

// ServiceName returns the compose service name by stripping the trailing
// "-{index}" from the instance name ("database-1" -> "database").
func (r *Instance) ServiceName() string {
	return r.Config.ServiceName
}

// WaitIPs polls the instance state until it reports at least one global address
// or the timeout elapses. A freshly started container may not have its DHCP
// lease yet, so this gives it time. On timeout it returns whatever was found
// (possibly empty).
func (r *Instance) WaitIPs(timeout time.Duration) (ips []InterfaceIPs, err error) {
	deadline, cancel := context.WithTimeout(r.client.globalClient.Ctx, timeout)

	for {
		ips, err = r.client.InstanceIPs(r.IncusName())
		if err == nil {
			cancel()
			return ips, nil
		}

		r.client.LogDebug("Waiting for IPs", "instance", r, "error", err)

		select {
		case <-deadline.Done():
			cancel()
			return nil, deadline.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// HasFull returns true if the instance has a full instance.
func (r *Instance) HasFull() bool {
	return r.IncusInstanceFull != nil
}

func (r *Instance) fetch() error {
	// Fresh instance.
	instance, eTag, err := r.client.incus.GetInstance(r.incusName)
	if err != nil {
		return err
	}
	r.IncusInstance = instance
	r.ETag = eTag

	if r.Config.Full {
		full, _, err := r.client.incus.GetInstanceFull(r.IncusInstance.Name)
		if err != nil {
			return err
		}

		r.IncusInstanceFull = full
	}

	return nil
}

// Ensure retrieves an existing instance or creates a new one if args.Create is true.
func (r *Instance) Ensure(opts ...Option) error {
	options := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionEnsure, r, options, nil); err != nil {
			return err
		}
	}

	// Try to get existing
	// Check if exists
	err := r.fetch()
	if err == nil {
		err = r.ensured()

		if r.client.hookAfter != nil {
			err = r.client.hookAfter(ActionEnsure, r, options, err)
		}

		return err
	}

	if !options.Create {
		err = ErrNotFound.WithResource(r).Wrap(err)

		if r.client.hookAfter != nil {
			err = r.client.hookAfter(ActionEnsure, r, options, err)
		}

		return err
	}

	err = r.create(opts...)

	if r.client.hookAfter != nil {
		err = r.client.hookAfter(ActionEnsure, r, options, err)
	}

	return err
}

func (r *Instance) ensured() error {
	var err error
	r.UID, r.GID, err = extractUIDGID(r.IncusInstance)
	if err != nil {
		return ErrInvalidFormat.WithText("extracting uid/gid").Wrap(err)
	}

	if r.Config.Image == "" {
		if alias, ok := r.IncusInstance.Config["user.image_alias"]; ok {
			r.Config.Image = alias
		} else {
			r.Config.Image = r.client.ResolveImageFingerprint(r.IncusInstance.Config["volatile.base_image"])
		}
	}

	return nil
}

func (r *Instance) create(opts ...Option) error {
	// Can't create an instance without an image
	if r.Config.Image == "" {
		return ErrImageRequired
	}

	if r.Config.Resources != nil {
		for _, rDep := range r.Config.Resources {
			if !rDep.IsEnsured() {
				return ErrDependencyNotEnsured.WithResource(rDep)
			}
		}
	}

	imageResource, err := r.client.Resource(KindImage, r.Config.Image, &ImageConfig{})
	if err != nil {
		return err
	}

	image, ok := imageResource.(*Image)
	if !ok {
		return ErrUnknown.WithResource(imageResource)
	}

	// The image must have been ensured first. If its Ensure failed (e.g. the
	// pull errored), IncusAlias is nil; fail cleanly instead of dereferencing it.
	if !image.IsEnsured() {
		r.client.LogDebug("Dependency", "image", image)
		return ErrDependencyNotEnsured.WithResource(image)
	}

	r.image = image

	// Use UID/GID from image properties when available so volumes are created
	// with the correct shifted config before the instance is created.
	if image.UID > 0 || image.GID > 0 {
		r.UID = image.UID
		r.GID = image.GID
	}

	// Resolve storage volumes before building the device map so that each
	// disk device's Source is the volume name, not the raw host path.
	if err := r.ensureVolumes(); err != nil {
		return err
	}

	// Build pre-devices map after volumes are resolved.
	devices, err := r.buildDevices()
	if err != nil {
		return err
	}

	// Get image info from project
	incusImage, _, err := r.client.incus.GetImage(image.IncusAlias.Target)
	if err != nil {
		return ErrNotFound.WithText("getting image").Wrap(err)
	}

	postConfig := make(map[string]string, len(r.Config.Config))
	maps.Copy(postConfig, r.Config.Config)

	// Store the image name
	postConfig["user.image_alias"] = image.IncusName()

	// Healthd should wait until we allow it to work with it.
	postConfig["user.stopped"] = "true"

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
			Config:      postConfig,
			Devices:     devices,
		},
	}

	options := NewOptions(opts...)

	// Create instance from project image
	op, err := r.client.incus.CreateInstanceFromImage(r.client.incus, *incusImage, req)
	if err = r.client.hookRemoteOperation(r.client.globalClient.Ctx, ActionEnsure, r, options, op, err); err != nil {
		return err
	}

	// Get instance to extract UID/GID
	if err := r.fetch(); err != nil {
		return ErrCreate.WithText("fetching created instance").Wrap(err)
	}

	if err = r.ensured(); err != nil {
		return err
	}

	r.created = true
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
			return nil, ErrDeviceConflict.WithText("device exists in profile " + name)
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
				"pool": r.client.Config().DefaultStoragePool,
			}
		}
	}

	return devices, nil
}

// ensureVolumes creates storage volumes referenced in Devices before the instance
// is created, and updates each device's Source and Pool with the resolved values.
func (r *Instance) ensureVolumes() error {
	for i := range r.Config.Devices {
		dev := &r.Config.Devices[i]
		if dev.Config.DeviceType != InstanceDeviceTypeDisk {
			continue
		}

		svc := dev.Config.Disk.StorageVolumeConfig
		if svc == nil {
			continue
		}

		volConfig := *svc
		volConfig.Shifted = true
		volConfig.ImageResource = r.image

		volI, err := r.client.Resource(KindStorageVolume, dev.Config.Disk.Source, &volConfig)
		if err != nil {
			return ErrBadDeviceConfig.WithText("getting storage-volume resource").Wrap(err)
		}

		vol, ok := volI.(*StorageVolume)
		if !ok {
			return ErrUnsupportedAction.WithResource(volI)
		}

		dev.Config.Disk.Source = vol.IncusName()
		dev.Config.Disk.StorageVolumeConfig.Pool = vol.Config.Pool
	}

	return nil
}

func (r *Instance) attachPostStartDevices() error {
	// Fetch fresh state and ETag before modifying devices — start may have
	// invalidated the cached ETag via UpdateInstanceState.
	if err := r.fetch(); err != nil {
		return ErrNotFound.WithText("refreshing instance before post-start devices").Wrap(err)
	}

	instance := r.IncusInstance

	// Resolve container IPs once — instance is running so this should be fast.
	ips, err := r.WaitIPs(30 * time.Second)
	if err != nil {
		r.client.LogWarn("could not resolve IPs for post-start devices", "instance", r.incusName, "err", err)
	}

	network := ips[0].Network
	iPv4s := ips[0].IPv4s
	iPv6s := ips[0].IPv6s

	var bridgeV4Addrs, bridgeV6Addrs []string
	bridgeV4Addrs, bridgeV6Addrs, err = r.client.NetworkBridgeIPs(network)
	if err != nil {
		return fmt.Errorf("nat-proxy: could not get bridge IPs for network %s: %w", network, err)
	}

	if len(bridgeV4Addrs) == 0 && len(bridgeV6Addrs) == 0 {
		return fmt.Errorf("nat-proxy: didn't get an IP for network %s", network)
	}

	for _, dev := range r.Config.PostStartDevices {
		if dev.Config.DeviceType == InstanceDeviceTypeProxy && dev.Config.Proxy.Nat {
			if dev.Config.Proxy.ListenAddr == "" {
				dev.Config.Proxy.ListenAddr = bridgeV4Addrs[0]
			}

			if ip := net.ParseIP(dev.Config.Proxy.ListenAddr).To4(); ip == nil {
				if len(iPv6s) < 1 {
					return fmt.Errorf("no IPv6 address for NAT proxy, instance %s", r.Name())
				}
				dev.Config.Proxy.ConnectAddr = iPv6s[0]
			} else {
				if len(iPv4s) < 1 {
					return fmt.Errorf("no IPv4 address for NAT proxy, instance %s", r.Name())
				}
				dev.Config.Proxy.ConnectAddr = iPv4s[0]
			}
		}

		devName, devConfig, err := dev.ToIncusDevice()
		if err != nil {
			return err
		}

		instance.Devices[devName] = devConfig
	}

	op, err := r.client.incus.UpdateInstance(instance.Name, instance.Writable(), r.ETag)
	if err != nil {
		return ErrCreate.WithText("updating with post-start devices").Wrap(err)
	}

	if err := op.Wait(); err != nil {
		return ErrOperation.WithText("waiting for post-start device update").Wrap(err)
	}

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

	if err := r.waitForDependencies(options); err != nil {
		if r.client.hookAfter != nil {
			return r.client.hookAfter(ActionStart, r, options, err)
		}
		return err
	}

	err := r.start()

	if r.client.hookAfter != nil {
		err = r.client.hookAfter(ActionStart, r, options, err)
	}

	return err
}

// Running returns true if the instance is running.
func (r *Instance) Running() bool {
	if !r.IsEnsured() {
		return false
	}

	return r.IncusInstance.Status == "Running"
}

// waitForDependencies blocks until all Config.Dependencies reach their required
// health status, or until the dependency timeout elapses.
func (r *Instance) waitForDependencies(options Options) error {
	if len(r.Config.Dependencies) == 0 {
		return nil
	}

	timeout := options.DependencyTimeout
	if timeout == 0 {
		timeout = options.Timeout
	}

	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}

	for depName, requiredStatus := range r.Config.Dependencies {
		r.client.LogDebug("Waiting for dependency", "instance", r.incusName, "dep", depName, "status", requiredStatus)
		for {
			inst, _, err := r.client.incus.GetInstance(depName)
			if err == nil && inst.Config[HealthConfigKey] == requiredStatus {
				r.client.LogDebug("Dependency ready", "dep", depName)
				break
			}

			if !deadline.IsZero() && time.Now().After(deadline) {
				return fmt.Errorf("dependency %q did not reach status %q within %s", depName, requiredStatus, timeout)
			}

			time.Sleep(2 * time.Second)
		}
	}
	return nil
}

func (r *Instance) start() error {
	if r.Running() {
		return nil
	}

	op, err := r.client.incus.UpdateInstanceState(r.incusName, incusApi.InstanceStatePut{
		Action: "start",
	}, r.ETag)
	if err != nil {
		return ErrOperation.WithText("starting instance").Wrap(err)
	}

	if err := op.Wait(); err != nil {
		return err
	}

	_ = r.setStopped(false)

	if r.created {
		if err := r.PushFiles(); err != nil {
			return err
		}

		// Push secrets after instance is running
		if len(r.Config.Secrets) > 0 {
			if err := r.PushSecrets(); err != nil {
				return err
			}
		}
	}

	if r.created && len(r.Config.PostStartDevices) > 0 {
		if err := r.attachPostStartDevices(); err != nil {
			return err
		}
	}

	return nil
}

// PushSecrets pushes secrets into the running instance.
// Secrets are only pushed if they don't already exist with the same content.
func (r *Instance) PushSecrets() error {
	if !r.IsEnsured() {
		return ErrNotEnsured
	}

	for _, secret := range r.Config.Secrets {
		target := secret.Target
		if target == "" {
			target = "/run/secrets/" + secret.Source
		}

		mode := secret.Mode
		if mode == 0 {
			mode = 0o400
		}

		// Check if secret already exists with same content
		if r.secretExists(target, secret.Content) {
			continue
		}

		// Create parent directories recursively
		if err := r.mkdirP(path.Dir(target), 0o700); err != nil {
			return ErrCreate.WithText("creating secret directory").Wrap(err)
		}

		err := r.client.incus.CreateInstanceFile(r.incusName, target, incusClient.InstanceFileArgs{
			Content: bytes.NewReader(secret.Content),
			UID:     secret.UID,
			GID:     secret.GID,
			Mode:    mode,
			Type:    "file",
		})
		if err != nil {
			return ErrCreate.WithText("pushing secret " + secret.Source).Wrap(err)
		}
	}

	return nil
}

// PushFiles pushes files into the instance.
func (r *Instance) PushFiles() error {
	if !r.IsEnsured() {
		return ErrNotEnsured
	}

	for target, file := range r.Config.Files {
		// Create parent directories recursively
		if !file.NoMKDir {
			if err := r.mkdirP(path.Dir(target), file.DirMode); err != nil {
				return ErrCreate.WithText("creating directory for " + target).Wrap(err)
			}
		}

		uid, gid := file.UID, file.GID

		// Use the instances oci.UID/oci.GID (which comes the from image)
		if uid == -1 {
			uid = int64(r.UID)
		}
		if gid == -1 {
			gid = int64(r.GID)
		}

		if file.File != "" && file.Content != nil {
			return ErrCreate.WithText(fmt.Sprintf("cannot have both 'File' and 'Content' for a instance file %q", target))
		}

		if file.File != "" {
			f, err := os.Open(file.File)
			if err != nil {
				return ErrCreate.WithText(fmt.Sprintf("opening instance file %q", file.File)).Wrap(err)
			}

			file.Content = f
		}

		err := r.client.incus.CreateInstanceFile(r.incusName, target, incusClient.InstanceFileArgs{
			Content: file.Content,
			UID:     uid,
			GID:     gid,
			Mode:    file.Mode,
			Type:    "file",
		})
		if err != nil {
			return ErrCreate.WithText("pushing file " + target).Wrap(err)
		}
	}

	return nil
}

// mkdirP creates a directory and all parent directories inside the container.
// Directories are owned by oci.UID/oci.GID to match the container user.
// Uses slash-separated paths so it works regardless of host OS.
func (r *Instance) mkdirP(dirPath string, mode int) error {
	r.client.LogDebug("Creating directories", "dir", dirPath)

	dirs := []string{}
	p := path.Clean(dirPath)
	for {
		parent := path.Dir(p)
		if parent == p {
			break // reached root (e.g., "/" or ".")
		}

		dirs = append(dirs, p)
		p = parent
	}

	slices.Reverse(dirs)

	for _, dir := range dirs {
		err := r.client.incus.CreateInstanceFile(r.incusName, dir, incusClient.InstanceFileArgs{
			Type: "directory",
			Mode: mode,
			UID:  int64(r.UID),
			GID:  int64(r.GID),
		})
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return fmt.Errorf("mkdirP failed on %q for %q: %w", dir, dirPath, err)
		}
	}

	return nil
}

// secretExists checks if a file exists in the instance with the same content.
func (r *Instance) secretExists(sPath string, content []byte) bool {
	reader, _, err := r.client.incus.GetInstanceFile(r.incusName, sPath)
	if err != nil {
		return false // doesn't exist
	}
	defer reader.Close()

	existing, err := io.ReadAll(reader)
	if err != nil {
		return false
	}

	return bytes.Equal(existing, content)
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
		_ = r.setStopped(true)

		if r.client.hookAfter != nil {
			return r.client.hookAfter(ActionStop, r, options, nil)
		}

		return nil
	}

	op, err := r.client.incus.UpdateInstanceState(r.incusName, incusApi.InstanceStatePut{
		Action:  "stop",
		Force:   options.Force,
		Timeout: int(options.Timeout.Seconds()),
	}, r.ETag)

	err = r.client.hookOperation(r.client.globalClient.Ctx, ActionStop, r, options, op, err)
	if err != nil {
		if r.client.hookAfter != nil {
			return r.client.hookAfter(ActionStop, r, options, err)
		}

		return err
	}

	err = r.setStopped(true)

	if r.client.hookAfter != nil {
		return r.client.hookAfter(ActionStop, r, options, err)
	}

	return err
}

// setStopped writes or clears the user.stopped config key on the instance.
// It is a best-effort call; callers ignore the error with _.
func (r *Instance) setStopped(stopped bool) error {
	// Always fetch before writing to get a fresh ETag — the caller may have
	// just run UpdateInstanceState which invalidates the cached ETag.
	if err := r.fetch(); err != nil {
		return err
	}

	current, exists := r.IncusInstance.Config["user.stopped"]
	if stopped {
		if exists && current == "true" {
			return nil
		}
		r.IncusInstance.Config["user.stopped"] = "true"
	} else {
		if !exists {
			return nil
		}
		delete(r.IncusInstance.Config, "user.stopped")
	}

	op, err := r.client.incus.UpdateInstance(r.incusName, r.IncusInstance.Writable(), r.ETag)
	if err != nil {
		return err
	}

	return op.Wait()
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

	if options.Follow {
		if err := r.logBuffer(outputHandler); err != nil {
			return err
		}
		return r.logStream(options, outputHandler)
	}

	return r.logBuffer(outputHandler)
}

// logBuffer reads the saved console log buffer via GET /console (equivalent to
// `incus console --show-log`). Used for non-follow log retrieval.
func (r *Instance) logBuffer(outputHandler func(Action, Resource, []byte)) error {
	reader, err := r.client.incus.GetInstanceConsoleLog(r.incusName, nil)
	if err != nil {
		return ErrOperation.WithText("getting console log").Wrap(err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return ErrOperation.WithText("reading console log").Wrap(err)
	}

	outputHandler(ActionLog, r, data)
	return nil
}

// logStream streams the console using WebSocket until context is cancelled.
func (r *Instance) logStream(options Options, outputHandler func(Action, Resource, []byte)) error {
	ctx := r.client.globalClient.Ctx

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

	// Control handler - required by Incus API, but we don't need window resize.
	// We just wait for context cancellation; the library handles websocket cleanup.
	controlHandler := func(_ *websocket.Conn) {
		<-ctx.Done()
	}

	args := &incusClient.InstanceConsoleArgs{
		Terminal:          terminal,
		Control:           controlHandler,
		ConsoleDisconnect: consoleDisconnect,
	}

	op, err := r.client.incus.ConsoleInstance(r.incusName, req, args)
	if err != nil {
		return ErrOperation.WithText("connecting to console").Wrap(err)
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
		return ErrOperation.WithText("console streaming").Wrap(err)
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
func extractUIDGID(instance *incusApi.Instance) (uint64, uint64, error) {
	if incusApi.InstanceType(instance.Type) != incusApi.InstanceTypeContainer {
		return 0, 0, nil
	}

	// oci.uid/gid only exist for OCI images, not native Incus images
	uidStr, hasUID := instance.Config["oci.uid"]
	gidStr, hasGID := instance.Config["oci.gid"]
	if !hasUID || !hasGID {
		return 0, 0, nil
	}

	uid, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		return 0, 0, err
	}

	gid, err := strconv.ParseUint(gidStr, 10, 32)
	if err != nil {
		return 0, 0, err
	}

	return uid, gid, nil
}
