package client

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
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
	Content io.ReadSeeker
	UID     int64
	GID     int64
	Mode    int
}

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
	return serviceName(r.name)
}

// WaitIPs polls the instance state until it reports at least one global address
// or the timeout elapses. A freshly started container may not have its DHCP
// lease yet, so this gives it time. On timeout it returns whatever was found
// (possibly empty).
func (r *Instance) WaitIPs(timeout time.Duration) (network string, ipv4 []string, ipv6 []string, err error) {
	deadline := time.Now().Add(timeout)
	for {
		network, ipv4, ipv6, err = r.client.InstanceIPs(r.IncusName())
		if err != nil {
			return "", nil, nil, err
		}
		if len(ipv4) > 0 || len(ipv6) > 0 || time.Now().After(deadline) {
			return network, ipv4, ipv6, nil
		}

		select {
		case <-r.client.globalClient.Ctx.Done():
			return network, ipv4, ipv6, r.client.globalClient.Ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// HasFull returns true if the instance has a full instance.
func (r *Instance) HasFull() bool {
	return r.IncusInstanceFull != nil
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
		return ErrInvalidFormat.WithText("extracting uid/gid").Wrap(err)
	}

	if r.Config.Image == "" {
		if alias, ok := r.IncusInstance.Config["user.image_alias"]; ok {
			r.Config.Image = alias
		} else {
			r.Config.Image = r.client.ResolveImageFingerprint(r.IncusInstance.Config["volatile.base_image"])
		}
	}

	if r.Config.Full {
		full, _, err := r.client.incus.GetInstanceFull(r.IncusInstance.Name)
		if err != nil {
			return err
		}

		r.IncusInstanceFull = full
	}

	return nil
}

func (r *Instance) create(opts ...Option) error {
	// Can't create an instance without an image
	if r.Config.Image == "" {
		return ErrImageRequired
	}

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

	// The image must have been ensured first. If its Ensure failed (e.g. the
	// pull errored), IncusAlias is nil; fail cleanly instead of dereferencing it.
	if !image.IsEnsured() {
		return ErrDependencyNotEnsured.WithResource(image)
	}

	// Get image info from project
	incusImage, _, err := r.client.incus.GetImage(image.IncusAlias.Target)
	if err != nil {
		return ErrNotFound.WithText("getting image").Wrap(err)
	}

	// Store the image name
	r.Config.Config["user.image_alias"] = image.IncusName()

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

	// Create instance from project image
	op, err := r.client.incus.CreateInstanceFromImage(r.client.incus, *incusImage, req)
	if err = r.client.hookRemoteOperation(r.client.globalClient.Ctx, ActionEnsure, r, options, op, err); err != nil {
		return err
	}

	// Get instance to extract UID/GID
	instance, eTag, err := r.client.incus.GetInstance(r.incusName)
	if err != nil {
		return ErrCreate.WithText("fetching created instance").Wrap(err)
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

	// Push files before marking as created
	if len(r.Config.Files) > 0 {
		if err := r.PushFiles(); err != nil {
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
				return ErrBadDeviceConfig.WithText("getting storage-volume resource").Wrap(err)
			}

			vol, ok := volI.(*StorageVolume)
			if !ok {
				return ErrUnsupportedAction.WithResource(volI)
			}

			// Override cached config — the resource may have been registered earlier with
			// empty UID/GID before oci.uid/oci.gid were known.
			vol.Config.Shifted = true
			vol.Config.UID = r.UID
			vol.Config.GID = r.GID

			if err := RunAction(volI, ActionPostEnsure, OptionCreate()); err != nil {
				return ErrCreate.WithText("post-ensuring volume").Wrap(err)
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
		return ErrCreate.WithText("updating with devices").Wrap(err)
	}

	if err := op.Wait(); err != nil {
		return ErrOperation.WithText("waiting for update").Wrap(err)
	}

	// Refresh instance state
	instance, eTag, err := r.client.incus.GetInstance(r.incusName)
	if err != nil {
		return ErrNotFound.WithText("refreshing instance").Wrap(err)
	}

	r.IncusInstance = instance
	r.ETag = eTag

	return nil
}

func (r *Instance) attachPostStartDevices() error {
	instance := r.IncusInstance

	// Resolve container IPs once — instance is running so this should be fast.
	network, ipv4s, ipv6s, err := r.WaitIPs(30 * time.Second)
	if err != nil {
		r.client.LogWarn("could not resolve IPs for post-start devices", "instance", r.incusName, "err", err)
	}

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
				if len(ipv6s) == 0 {
					return fmt.Errorf("no IPv6 address for NAT proxy, instance %s", r.Name())
				}
				dev.Config.Proxy.ConnectAddr = ipv6s[0]
			} else {
				if len(ipv4s) == 0 {
					return fmt.Errorf("no IPv4 address for NAT proxy, instance %s", r.Name())
				}
				dev.Config.Proxy.ConnectAddr = ipv4s[0]
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

	instance, eTag, err := r.client.incus.GetInstance(r.incusName)
	if err != nil {
		return ErrNotFound.WithText("refreshing instance after post-start devices").Wrap(err)
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
	if r.IncusInstance.Status != "Running" {
		op, err := r.client.incus.UpdateInstanceState(r.incusName, incusApi.InstanceStatePut{
			Action: "start",
		}, r.ETag)
		if err != nil {
			return ErrOperation.WithText("starting instance").Wrap(err)
		}

		if err := op.Wait(); err != nil {
			return err
		}

		// Refresh instance state
		instance, eTag, err := r.client.incus.GetInstance(r.incusName)
		if err != nil {
			return ErrNotFound.WithText("refreshing instance after start").Wrap(err)
		}

		r.IncusInstance = instance
		r.ETag = eTag

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
		if err := r.mkdirP(target[:strings.LastIndex(target, "/")]); err != nil {
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
		if idx := strings.LastIndex(target, "/"); idx > 0 {
			if err := r.mkdirP(target[:idx]); err != nil {
				return ErrCreate.WithText("creating directory for " + target).Wrap(err)
			}
		}

		err := r.client.incus.CreateInstanceFile(r.incusName, target, incusClient.InstanceFileArgs{
			Content: file.Content,
			UID:     file.UID,
			GID:     file.GID,
			Mode:    file.Mode,
			Type:    "file",
		})
		if err != nil {
			return ErrCreate.WithText("pushing file " + target).Wrap(err)
		}
	}

	return nil
}

// mkdirP creates a directory and all parent directories.
// Directories are owned by oci.UID/oci.GID to match the container user.
// TODO: This wont work for windows containers or incus-compose running on windows?
func (r *Instance) mkdirP(path string) error {
	r.client.LogDebug("Creating directories", "dir", path)

	dirs := []string{}
	p := filepath.Clean(path)
	for {
		dirs = append(dirs, p)
		parent := filepath.Dir(p)
		if parent == p {
			break // reached root (e.g., "/" or ".")
		}
		p = parent
	}

	slices.Reverse(dirs)

	for _, dir := range dirs {
		err := r.client.incus.CreateInstanceFile(r.incusName, dir, incusClient.InstanceFileArgs{
			Type: "directory",
			Mode: 0o755,
			UID:  int64(r.UID),
			GID:  int64(r.GID),
		})
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return fmt.Errorf("mkdirP failed on %s: %w", dir, err)
		}
	}

	return nil
}

// pushFile writes content to a file in the instance.
// Files are owned by nobody (65534) to match the container user.
func (r *Instance) pushFile(path string, content []byte, mode int, mkdir bool) error {
	// Clean the path to handle redundant separators and dots
	path = filepath.Clean(path)

	if mkdir {
		// Create the directory first.
		err := r.mkdirP(filepath.Dir(path))
		if err != nil {
			return fmt.Errorf("creating secrets dir '%v': %w", filepath.Dir(path), err)
		}
	}

	err := r.client.incus.CreateInstanceFile(r.incusName, path, incusClient.InstanceFileArgs{
		Content: bytes.NewReader(content),
		Mode:    mode,
		UID:     int64(r.UID),
		GID:     int64(r.GID),
	})
	if err != nil {
		return fmt.Errorf("while pushing file '%v' (%d:%d - %d), %w", path, r.UID, r.GID, mode, err)
	}

	return nil
}

// secretExists checks if a file exists in the instance with the same content.
func (r *Instance) secretExists(path string, content []byte) bool {
	reader, _, err := r.client.incus.GetInstanceFile(r.incusName, path)
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
		return nil // already stopped
	}

	op, err := r.client.incus.UpdateInstanceState(r.incusName, incusApi.InstanceStatePut{
		Action:  "stop",
		Force:   options.Force,
		Timeout: options.Timeout,
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
	r.client.resources.Remove(r)

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
func extractUIDGID(instance *incusApi.Instance) (uint32, uint32, error) {
	if incusApi.InstanceType(instance.Type) != incusApi.InstanceTypeContainer {
		return 0, 0, nil
	}

	// oci.uid/gid only exist for OCI images, not native Incus images
	uidStr, hasUID := instance.Config["oci.uid"]
	gidStr, hasGID := instance.Config["oci.gid"]
	if !hasUID || !hasGID {
		return 0, 0, nil
	}

	uid64, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		return 0, 0, err
	}

	gid64, err := strconv.ParseUint(gidStr, 10, 32)
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
