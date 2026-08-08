package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/avast/retry-go/v5"
	"github.com/gorilla/websocket"
	"github.com/kballard/go-shellquote"
	incusClient "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/pkg/sftp"

	"github.com/lxc/incus-compose/shared"
)

// Reader wraps bytes.Reader to add a no-op Close.
type Reader struct {
	*bytes.Reader
}

// NewReaderFromBytes returns the given ClosingBufferReader from the given bytes.
func NewReaderFromBytes(b []byte) *Reader {
	return &Reader{bytes.NewReader(b)}
}

// Close is a noop.
func (cb *Reader) Close() error {
	return nil
}

// InstanceFile represents a file to push to an instance after creation.
type InstanceFile struct {
	Target string

	// Give either "File", "Content" or "Reader"
	File    string
	Content io.ReadSeekCloser

	UID       int64 // Uses oci.uid if -1 has been given.
	GID       int64 // Uses oci.gid if -1 has been given.
	Mode      int
	NoMKDir   bool
	DirMode   int
	Overwrite bool
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

	// Files are files pushed into the instance after creation.
	// Map key is the target path in the instance.
	Files []InstanceFile

	// Extensions contains Incus instance configuration options.
	Extensions map[string]string

	// ExtraDevices contains additional raw device configurations.
	ExtraDevices map[string]map[string]string

	// NoRootDevice takes the root disk from the instance's profile instead.
	NoRootDevice bool

	// Dependencies maps dependency Incus instance names to the required health
	// status (HealthStatusHealthy, HealthStatusStarting, HealthStatusUnhealthy).
	// Instance.Start() blocks until all dependencies reach the required status.
	Dependencies map[string]string

	// Priority if set sets the instance priority to this instead PriorityInstance.
	Priority int

	// Entrypoint overrides the image entrypoint (compose `entrypoint:`). Nil
	// means unset; a non-nil value discards the image's default command.
	Entrypoint []string

	// Command overrides the image command (compose `command:`).
	Command []string

	// UID if not 0 use that value else use the user id from the image.
	UID uint64
	// GID if not 0 use that value else use the user id from the image.
	GID uint64
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

	mu *sync.Mutex

	// Updated is broadcasted whenever an fetch() or some timeout happened.
	Updated *sync.Cond

	// deleteMarked indicates that this instance will be deleted after Ensure(),
	// this is for down scaling instances.
	deleteMarked bool

	// conn is this resource's own event-isolated Incus connection, set in
	// Ensure() (which always runs before any other action) so concurrent
	// workers never share a *ProtocolIncus. See Client.Connection.
	conn *incusClient.ProtocolIncus

	// image is for internal use in create operations.
	image *Image

	// State - nil means not ensured.
	IncusInstance *incusApi.Instance

	// // UID/GID from the config or extracted from container (for volume shifting).
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

	if config.Priority == 0 {
		config.Priority = PriorityInstance
	}

	// Set defaults
	if config.Type == "" {
		config.Type = incusApi.InstanceTypeContainer
	}
	if config.Extensions == nil {
		config.Extensions = make(map[string]string)
	}

	mu := &sync.Mutex{}

	inst := &Instance{
		BaseResource: NewBaseResource(KindInstance, name, config.Priority),
		client:       c,
		incusName:    SanitizeIncusName(name, -1),
		Config:       *config,
		mu:           mu,
		Updated:      sync.NewCond(mu),
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

// ServiceName returns the compose service name which has been set by the config.
func (r *Instance) ServiceName() string {
	return r.Config.ServiceName
}

// WaitIPs polls the instance state until each attached NIC reports its
// expected global addresses (IPv4 always, IPv6 too unless the network or this
// NIC's own attachment disables it) or the timeout elapses. A freshly started
// container may not have its DHCP lease(s) yet, so this gives it time. On
// timeout it returns an error: DNSWatcher registers an AAAA-equivalent record
// for any address family it waited for, so a missing one must not pass silently.
func (r *Instance) WaitIPs(ctx context.Context, timeout time.Duration) (ips []InterfaceIPs, err error) {
	if err := r.fetch(); err != nil {
		return nil, err
	}

	// Cache if ipv6 is required per network. Incus brings up IPv6 automatically
	// on OCI containers unless the network (or this NIC's own attachment)
	// disables it. External networks opt out of the requirement entirely: we
	// never configure them ourselves, and whether they actually hand out IPv6
	// isn't something we can reliably know from our side.
	networkIpv6 := map[string]bool{}
	for _, dev := range r.Config.Devices {
		if dev.Config.DeviceType != InstanceDeviceTypeNic {
			continue
		}

		networkName := dev.Config.NetworkName
		needsIPv6 := false

		net, ok := dev.Config.Network.(*Network)
		if ok {
			networkName = net.IncusName()
			needsIPv6 = !net.Config.External

			v, extOk := net.Config.Extensions["ipv6.address"]
			if extOk && v == "none" {
				needsIPv6 = false
			}
		}

		// A device-level override (e.g. x-incus ipv6.address: none on this
		// specific attachment) takes priority over the network's own setting.
		v, extOk := dev.Config.Extensions["ipv6.address"]
		if extOk && v == "none" {
			needsIPv6 = false
		}

		if networkName != "" {
			networkIpv6[networkName] = needsIPv6
		}
	}

	deadline, cancel := context.WithTimeout(ctx, timeout)

	for {
		r.client.LogDebug("Waiting for IPs", "instance", r)

		if r.Running() {
			ips, err = r.client.InstanceIPs(r.IncusName())
			if err == nil {
				needIPv6 := false

				for _, ip := range ips {
					if len(ip.IPv6s) > 0 {
						continue
					}

					ipv6, ok := networkIpv6[ip.Network]
					if !ok {
						r.client.LogWarn("Found an unknown network", "resource", r, "network", ip.Network)
						continue
					}

					// If we don't have an IPv6 yet but we need one wait again.
					if ipv6 {
						needIPv6 = true
					}
				}

				if !needIPv6 {
					cancel()
					return ips, nil
				}
			}
		}

		select {
		case <-deadline.Done():
			cancel()
			return nil, NewError("WaitIPs").WithText(fmt.Sprintf("timeout after: %v", timeout))
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// HasFull returns true if the instance has a full instance.
func (r *Instance) HasFull() bool {
	return r.IncusInstanceFull != nil
}

func (r *Instance) fetch() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.conn == nil {
		conn, err := r.client.Connection()
		if err != nil {
			return err
		}
		r.conn = conn
	}

	// Fresh instance.
	instance, _, err := r.conn.GetInstance(r.incusName)
	if err != nil {
		return err
	}
	r.IncusInstance = instance

	r.UID = r.Config.UID
	r.GID = r.Config.GID

	if r.UID == 0 || r.GID == 0 {
		var err error
		r.UID, r.GID, err = extractUIDGID(r.IncusInstance)
		if err != nil {
			return ErrInvalidFormat.WithText("extracting uid/gid").Wrap(err)
		}
	}

	if r.Config.Full {
		full, _, err := r.conn.GetInstanceFull(r.IncusInstance.Name)
		if err != nil {
			return err
		}

		r.IncusInstanceFull = full
	}

	// Let everyone know that we have updated.
	r.Updated.Broadcast()

	return nil
}

// Ensure retrieves an existing instance or creates a new one if args.Create is true.
func (r *Instance) Ensure(ctx context.Context, opts ...Option) error {
	options := NewOptions(opts...)

	if err := r.client.hookBefore(ctx, ActionEnsure, r, options, nil); err != nil {
		return err
	}

	// Try to get existing
	// Check if exists
	err := r.fetch()
	if err == nil {
		// Keys only: a changed value still needs --recreate.
		if addErr := r.addMissingConfig(ctx); addErr != nil {
			return r.client.hookAfter(ctx, ActionEnsure, r, options, addErr)
		}

		err = r.ensured()
		err = r.client.hookAfter(ctx, ActionEnsure, r, options, err)

		if err == nil && r.deleteMarked {
			if err := r.Stop(ctx, OptionTimeout(options.Timeout), OptionForce()); err != nil {
				return err
			}

			if err := r.Delete(ctx); err != nil {
				return err
			}
		}

		return err
	}

	if !options.Create {
		err = ErrNotFound.Wrap(err)
		err = r.client.hookAfter(ctx, ActionEnsure, r, options, err)

		if r.deleteMarked {
			// Just remove the resource
			r.client.resources.Remove(r)
		}

		return err
	}

	err = r.create(ctx, opts...)
	err = r.client.hookAfter(ctx, ActionEnsure, r, options, err)

	return err
}

func (r *Instance) ensured() error {
	if r.Config.Image == "" {
		if alias, ok := r.IncusInstance.Config["user.image_alias"]; ok {
			r.Config.Image = alias
		} else {
			r.Config.Image = r.client.ResolveImageFingerprint(r.IncusInstance.Config["volatile.base_image"])
		}
	}

	return nil
}

func (r *Instance) create(ctx context.Context, opts ...Option) error {
	options := NewOptions(opts...)

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

	config := map[string]string{}

	r.UID = r.Config.UID
	r.GID = r.Config.GID

	if r.UID == 0 && r.GID == 0 {
		// Use UID/GID from image properties when available so volumes are created
		// with the correct shifted config before the instance is created.
		if image.UID > 0 || image.GID > 0 {
			r.UID = image.UID
			r.GID = image.GID
		}
	}

	entrypoint := resolveEntrypoint(r.image.Entrypoint, r.Config.Entrypoint, r.Config.Command)
	if entrypoint != "" {
		config["oci.entrypoint"] = entrypoint
	}

	// Store UID/GID.
	if !image.NativeIncus() {
		if r.UID != image.UID {
			config["oci.uid"] = strconv.FormatUint(r.UID, 10)
		}
		if r.GID != image.GID {
			config["oci.gid"] = strconv.FormatUint(r.GID, 10)
		}
	}

	// Store the image name
	config["user.image_alias"] = image.IncusName()

	// Build devices map after volumes are resolved.
	devices, err := r.buildDevices()
	if err != nil {
		return err
	}

	// Get image info from project
	incusImage, _, err := r.conn.GetImage(image.IncusAlias.Target)
	if err != nil {
		return ErrNotFound.WithText("getting image").Wrap(err)
	}

	// Copy users project / x-incus config.
	// This is after all our configs so we allow users to override it.
	maps.Copy(config, r.Config.Extensions)

	_, hasStatus := config[HealthStatusKey]

	if options.Healthd {
		// Healthd should wait until we allow it to work with it.
		config[HealthStoppedKey] = "true"
	} else if !hasStatus {
		// Without a daemon nothing will ever report on this instance.
		config[HealthStatusKey] = shared.HealthStatusUnknown
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
			Config:      config,
			Devices:     devices,
		},
	}

	// Create instance from project image.
	op, err := r.conn.CreateInstanceFromImage(r.conn, *incusImage, req)
	if err := r.client.hookRemoteOperation(ctx, ActionEnsure, r, options, op, err); err != nil {
		return err
	}

	// Get instance to extract UID/GID
	if err := r.fetch(); err != nil {
		return ErrCreate.WithText("fetching created instance").Wrap(err)
	}

	if err := r.ensured(); err != nil {
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

		// The code below would have allowed us to overwrite `eth0`,
		// but it breaks normal incus behavior (instances overwrite profile).
		// foundInProfile := false
		// for _, profile := range profiles {
		// 	foundInProfile = profile.HasDevice(name)
		// 	if foundInProfile {
		// 		break
		// 	}
		// }

		// if foundInProfile {
		// 	return nil, ErrDeviceConflict.WithText("device exists in profile " + name)
		// }

		devices[name] = config
	}

	if _, ok := devices["root"]; !ok && !r.Config.NoRootDevice {
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

// Start starts the instance.
func (r *Instance) Start(ctx context.Context, opts ...Option) error {
	const action = ActionStart
	options := NewOptions(opts...)

	if err := r.client.hookBefore(ctx, action, r, options, nil); err != nil {
		return err
	}

	if !r.IsEnsured() {
		return r.client.hookAfter(ctx, action, r, options, ErrNotEnsured)
	}

	if r.Running() {
		err := r.SetHealthCheckingStopped(ctx, false)
		if err != nil {
			return r.client.hookAfter(ctx, action, r, options, err)
		}

		return r.client.hookAfter(ctx, action, r, options, ErrRunning)
	}

	// Wait for the healthcheck to success if a test is defined.
	_, hasTest := r.IncusInstance.Config[HealthKeyPrefix+"test"]
	restart := slices.Contains(shared.RestartPolicies, r.IncusInstance.Config[HealthKeyPrefix+"restart"])
	v, ok := r.IncusInstance.Config[HealthKeyPrefix+"daemon"]
	isHealthd := ok && v == "true"

	if !isHealthd && (hasTest || restart) && options.Healthd && !options.ExternalHealthd {
		// Wait for healthd to be available for 3 seconds.
		err := retry.New(
			retry.Context(ctx),
			retry.Attempts(6),
			retry.Delay(500*time.Millisecond),
		).Do(func() error {
			running, err := r.client.HealthdRunning()
			if err != nil {
				return err
			}

			if !running {
				return errors.New("healthd is not running, cannot wait for it to check dependencies")
			}

			return nil
		})
		if err != nil {
			return r.client.hookAfter(ctx, action, r, options, err)
		}
	}

	startCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()

	err := r.SetHealthCheckingStopped(startCtx, false)
	if err != nil {
		return r.client.hookAfter(ctx, action, r, options, err)
	}

	err = r.start(startCtx, options)
	if err != nil {
		return r.client.hookAfter(ctx, action, r, options, err)
	}

	if options.Healthd {
		if (hasTest || restart) && !isHealthd {
			r.client.globalClient.emitProgress(action, r, options, Progress{
				Percent: -1,
				Text:    "Waiting for the healthcheck",
			})

			err = r.waitForHealthCheck(startCtx)
			if err != nil {
				return r.client.hookAfter(
					ctx,
					action,
					r,
					options,
					fmt.Errorf("failed to wait for the healthcheck with timeout: %v", options.Timeout),
				)
			}
		}
	}

	return r.client.hookAfter(ctx, action, r, options, nil)
}

// Running returns true if the instance is running.
func (r *Instance) Running() bool {
	if !r.IsEnsured() {
		return false
	}

	return r.IncusInstance.StatusCode == incusApi.Running
}

// retryBusy waits out the instance's operation lock, then runs write. The lock
// is taken by the driver, so a caller must do its operation wait inside write.
func retryBusy[T any](ctx context.Context, r *Instance, write func() (T, error)) (T, error) {
	var out T

	err := retry.New(
		retry.Context(ctx),
		retry.Attempts(10),
		retry.Delay(250*time.Millisecond),
		retry.LastErrorOnly(true),
		retry.RetryIf(func(err error) bool {
			return strings.Contains(err.Error(), "Instance is busy")
		}),
	).Do(func() error {
		err := r.waitBusyOperation(ctx)
		if err != nil {
			return err
		}

		out, err = write()

		return err
	})

	return out, err
}

// waitBusyOperation blocks until no queryable operation holds the instance's
// operation lock.
func (r *Instance) waitBusyOperation(ctx context.Context) error {
	instanceURL := incusApi.NewURL().Path("1.0", "instances", r.incusName).String()

	for {
		if err := ctx.Err(); err != nil {
			return ErrOperation.WithText("waiting for a pending instance operation").Wrap(err)
		}

		ops, err := r.conn.GetOperations()
		if err != nil {
			return ErrOperation.WithText("listing operations").Wrap(err)
		}

		busyOp := ""
		for _, op := range ops {
			if op.Class != incusApi.OperationClassTask || op.StatusCode.IsFinal() {
				continue
			}

			if slices.Contains(op.Resources["instances"], instanceURL) {
				busyOp = op.ID
				break
			}
		}

		if busyOp == "" {
			return nil
		}

		// Wait server-side for the operation to finish, bounded by the ctx
		// deadline. A NotFound just means it completed and was pruned between
		// the listing and here.
		timeoutSeconds := -1
		deadline, ok := ctx.Deadline()
		if ok {
			timeoutSeconds = max(int(time.Until(deadline).Seconds()), 1)
		}

		_, _, err = r.conn.GetOperationWait(busyOp, timeoutSeconds)
		if err != nil && !incusApi.StatusErrorCheck(err, http.StatusNotFound) {
			return ErrOperation.WithText("waiting for a pending instance operation").Wrap(err)
		}
	}
}

// See: https://pkg.go.dev/context#AfterFunc
func waitOnCond(ctx context.Context, cond *sync.Cond, conditionMet func() bool) error {
	cond.L.Lock()
	defer cond.L.Unlock()

	stopf := context.AfterFunc(ctx, func() {
		// We need to acquire cond.L here to be sure that the Broadcast
		// below won't occur before the call to Wait, which would result
		// in a missed signal (and deadlock).
		cond.L.Lock()
		defer cond.L.Unlock()

		// If multiple goroutines are waiting on cond simultaneously,
		// we need to make sure we wake up exactly this one.
		// That means that we need to Broadcast to all of the goroutines,
		// which will wake them all up.
		//
		// If there are N concurrent calls to waitOnCond, each of the goroutines
		// will spuriously wake up O(N) other goroutines that aren't ready yet,
		// so this will cause the overall CPU cost to be O(N²).
		cond.Broadcast()
	})
	defer stopf()

	// Since the wakeups are using Broadcast instead of Signal, this call to
	// Wait may unblock due to some other goroutine's context becoming done,
	// so to be sure that ctx is actually done we need to check it in a loop.
	for !conditionMet() {
		cond.Wait()
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	return nil
}

func (r *Instance) waitForHealthCheck(ctx context.Context) error {
	err := r.fetch()
	if err == nil && r.IncusInstance.Config[HealthStatusKey] == HealthStatusHealthy {
		r.client.LogDebug("Ready", "resource", r)

		return nil
	}

	err = waitOnCond(
		ctx,
		r.Updated,
		func() bool { return r.IncusInstance.Config[HealthStatusKey] == HealthStatusHealthy },
	)
	if err != nil {
		return err
	}

	return nil
}

// waitForDependencies blocks until all Config.Dependencies reach their required
// health status, or until the dependency timeout elapses.
func (r *Instance) waitForDependencies(ctx context.Context, action Action, options Options) error {
	if len(r.Config.Dependencies) == 0 {
		return nil
	}

	dTimeout := options.DependencyTimeout
	if dTimeout == 0 {
		dTimeout = options.Timeout
	}

	for depName, requiredStatus := range r.Config.Dependencies {
		var (
			dCtx   context.Context
			cancel context.CancelFunc
		)
		if dTimeout > 0 {
			dCtx, cancel = context.WithTimeout(ctx, dTimeout)
		} else {
			dCtx, cancel = context.WithCancel(ctx)
		}

		r.client.LogDebug("Waiting for dependency", "instance", r.incusName, "dep", depName, "status", requiredStatus)
		// Report the wait on the instance's start line so it shows a spinner
		// instead of stalling silently. This wait is not an Incus operation,
		// so it has no percentage.
		r.client.globalClient.emitProgress(action, r, options, Progress{
			Percent: -1,
			Text:    fmt.Sprintf("Waiting for dependency %s", depName),
		})

		rInst, err := r.client.Resource(KindInstance, depName, &InstanceConfig{})
		if err != nil {
			cancel()
			return fmt.Errorf("while getting instance %q: %w", depName, err)
		}
		inst, ok := rInst.(*Instance)
		if !ok {
			cancel()
			return fmt.Errorf("failed to cast the instance %q", depName)
		}

		err = inst.waitForHealthCheck(dCtx)
		if err != nil {
			cancel()
			return fmt.Errorf("failed to wait for the dependency %q with timeout %v", depName, dTimeout)
		}

		cancel()
	}

	return nil
}

func (r *Instance) start(ctx context.Context, options Options) error {
	if r.Running() {
		return nil
	}

	if options.Healthd {
		_, isHealthd := r.IncusInstance.Config[HealthKeyPrefix+"daemon"]
		if !isHealthd {
			if err := r.waitForDependencies(ctx, ActionStart, options); err != nil {
				return err
			}
		}
	}

	err := r.fetch()
	if err != nil {
		return err
	}

	sftpConn, err := retry.NewWithData[*sftp.Client](
		retry.Attempts(3),
		retry.Delay(10*time.Second),
	).Do(func() (*sftp.Client, error) {
		return r.conn.GetInstanceFileSFTP(r.incusName)
	})
	if err != nil {
		return ErrCreate.WithText("connecting to instance SFTP").Wrap(err)
	}

	// Push files while the instance is stopped: SFTP mounts the stopped rootfs,
	// most apps need theier secrets before the actual start happened.
	if err := r.PushFiles(sftpConn); err != nil {
		r.client.WarnError(sftpConn.Close, "Failed to close a sFTP connection")
		return err
	}

	r.client.WarnError(sftpConn.Close, "Failed to close a sFTP connection")

	if r.Running() {
		return ErrRunning
	}

	// Wait until no other operation holds the instance's operation lock,
	// e.g. an in-flight stop would reject the start with "Instance is busy".
	err = r.waitBusyOperation(ctx)
	if err != nil {
		return err
	}

	// The wait may have let a concurrent start finish, re-check.
	err = r.fetch()
	if err != nil {
		return err
	}

	if r.Running() {
		return ErrRunning
	}

	_, err = retryBusy(ctx, r, func() (struct{}, error) {
		op, err := r.conn.UpdateInstanceState(r.incusName, incusApi.InstanceStatePut{
			Action:  "start",
			Timeout: options.incusTimeout(),
		}, "")
		if err != nil {
			return struct{}{}, ErrOperation.WithText("creating an instance start operation").Wrap(err)
		}

		return struct{}{}, r.client.hookOperation(ctx, ActionStart, r, options, op, nil)
	})
	if err != nil {
		return ErrOperation.WithText("starting an instance").Wrap(err)
	}

	err = r.fetch()
	if err != nil {
		return ErrOperation.WithText("fetch after create").Wrap(err)
	}

	if !r.Running() {
		return ErrNotRunning.WithText("after a start")
	}

	return nil
}

// PushFiles pushes files into the instance over the instance's SFTP endpoint.
func (r *Instance) PushFiles(sftpConn *sftp.Client) error {
	if !r.IsEnsured() {
		return ErrNotEnsured
	}

	if len(r.Config.Files) == 0 {
		return nil
	}

	if sftpConn == nil {
		var err error
		sftpConn, err = r.conn.GetInstanceFileSFTP(r.incusName)
		if err != nil {
			return ErrCreate.WithText("connecting to instance SFTP").Wrap(err)
		}

		defer r.client.WarnError(sftpConn.Close, "Failed to close a sFTP connection")
	}

	for _, file := range r.Config.Files {
		err := r.pushFile(sftpConn, file)
		if err != nil {
			return ErrCreate.WithText("pushing file " + file.Target).Wrap(err)
		}
	}

	return nil
}

// pushFile writes a single InstanceFile over an established SFTP connection,
// creating parent directories and honoring the Overwrite flag.
func (r *Instance) pushFile(sftpConn *sftp.Client, file InstanceFile) error {
	if file.File != "" && file.Content != nil {
		return ErrCreate.WithText(fmt.Sprintf("cannot have both 'File' and 'Content' for instance file %q", file.Target))
	}

	if file.File != "" && file.Content == nil {
		fp, err := os.Open(file.File)
		if err != nil {
			return ErrCreate.Wrap(err)
		}
		file.Content = fp
	}

	// Resolve ownership: -1 falls back to the instance's oci.uid/oci.gid.
	uid, gid := file.UID, file.GID
	if uid == -1 {
		uid = int64(r.UID)
	}
	if gid == -1 {
		gid = int64(r.GID)
	}

	// Create parent directories, owned by the instance user.
	if !file.NoMKDir {
		dirMode := os.FileMode(file.DirMode)
		if dirMode == 0 {
			dirMode = 0o755
		}

		err := sftpRecursiveMkdir(r.client, sftpConn, path.Dir(file.Target), &dirMode, uid, gid)
		if err != nil {
			return ErrCreate.Wrap(err)
		}
	}

	// Leave an existing file untouched unless the caller opted into overwriting.
	if !file.Overwrite {
		_, err := sftpConn.Lstat(file.Target)
		if err == nil {
			// PushFiles owns the reader, so close it even when skipping.
			if file.Content != nil {
				r.client.WarnError(file.Content.Close, "Closing a push file")
			}

			r.client.LogDebug("Skipping existing instance file", "resource", r, "target", file.Target)
			return nil
		}
	}

	args := incusClient.InstanceFileArgs{
		Content:   file.Content,
		UID:       uid,
		GID:       gid,
		Mode:      file.Mode,
		Type:      "file",
		WriteMode: "overwrite",
	}

	err := SFTPCreateFile(r.client, sftpConn, file.Target, args, true)
	if err != nil {
		return ErrCreate.Wrap(err)
	}

	r.client.WarnError(file.Content.Close, "Failed to close a file")

	return nil
}

// sftpSetOwnerMode
// From: https://github.com/lxc/incus/blob/975d9869315b6db088c7c40ca5b37ee45e5ff8cf/cmd/incus/utils_sftp.go#L24
func sftpSetOwnerMode(sftpConn *sftp.Client, targetPath string, args incusClient.InstanceFileArgs) error {
	// Skip if not on UNIX.
	_, err := sftpConn.StatVFS("/")
	if err != nil {
		return nil //nolint:nilerr // StatVFS failing means the remote isn't UNIX; nothing to set
	}

	// Get the current stat information.
	st, err := sftpConn.Stat(targetPath)
	if err != nil {
		return err
	}

	fileStat, ok := st.Sys().(*sftp.FileStat)
	if !ok {
		return fmt.Errorf("invalid filestat data for %q", targetPath)
	}

	// Set owner.
	if args.UID >= 0 || args.GID >= 0 {
		if args.UID == -1 {
			args.UID = int64(fileStat.UID)
		}

		if args.GID == -1 {
			args.GID = int64(fileStat.GID)
		}

		err = sftpConn.Chown(targetPath, int(args.UID), int(args.GID))
		if err != nil {
			return err
		}
	}

	// Set mode.
	if args.Mode >= 0 {
		err = sftpConn.Chmod(targetPath, fs.FileMode(args.Mode))
		if err != nil {
			return err
		}
	}

	return nil
}

// SFTPCreateFile
// From: https://github.com/lxc/incus/blob/975d9869315b6db088c7c40ca5b37ee45e5ff8cf/cmd/incus/utils_sftp.go#L69
func SFTPCreateFile(c *Client, sftpConn *sftp.Client, targetPath string, args incusClient.InstanceFileArgs, push bool) error {
	switch args.Type {
	case "file":
		file, err := sftpConn.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
		if err != nil {
			return fmt.Errorf("failed to open target file %q: %w", targetPath, err)
		}

		defer c.WarnError(file.Close, "")

		if push {
			_, err = io.Copy(file, args.Content)
			if err != nil {
				return err
			}
		}

		err = sftpSetOwnerMode(sftpConn, targetPath, args)
		if err != nil {
			return err
		}

	case "directory":
		err := sftpConn.MkdirAll(targetPath)
		if err != nil {
			return err
		}

		err = sftpSetOwnerMode(sftpConn, targetPath, args)
		if err != nil {
			return err
		}

	case "symlink":
		// If already a symlink, re-create it.
		fInfo, err := sftpConn.Lstat(targetPath)
		if err == nil && fInfo.Mode()&os.ModeSymlink == os.ModeSymlink {
			err = sftpConn.Remove(targetPath)
			if err != nil {
				return err
			}
		}

		dest, err := io.ReadAll(args.Content)
		if err != nil {
			return err
		}

		err = sftpConn.Symlink(string(dest), targetPath)
		if err != nil {
			return err
		}
	}

	return nil
}

// sftpMkdirAll creates dir and any missing parents over SFTP, applying mode and
// ownership only to the directories it creates. Existing directories are left
// untouched, so it never re-owns pre-existing paths like /run.
// From: https://github.com/lxc/incus/blob/975d9869315b6db088c7c40ca5b37ee45e5ff8cf/cmd/incus/utils_sftp.go#L389
func sftpRecursiveMkdir(c *Client, sftpConn *sftp.Client, p string, mode *os.FileMode, uid int64, gid int64) error {
	/* special case, every instance has a /, we don't need to do anything */
	if p == "/" {
		return nil
	}

	// Remove trailing "/" e.g. /A/B/C/. Otherwise we will end up with an
	// empty array entry "" which will confuse the Mkdir() loop below.
	pclean := path.Clean(p)
	parts := strings.Split(pclean, "/")
	i := len(parts)

	for ; i >= 1; i-- {
		cur := path.Join(parts[:i]...)
		fInfo, err := sftpConn.Lstat(cur)
		if err != nil {
			continue
		}

		if !fInfo.IsDir() {
			return fmt.Errorf("%s is not a directory", cur)
		}

		i++
		break
	}

	for ; i <= len(parts); i++ {
		cur := path.Join(parts[:i]...)
		if cur == "" {
			continue
		}

		cur = "/" + cur
		cur = strings.TrimLeft(cur, "/")

		modeArg := -1
		if mode != nil {
			modeArg = int(mode.Perm())
		}

		args := incusClient.InstanceFileArgs{
			UID:  max(uid, 0),
			GID:  max(gid, 0),
			Mode: modeArg,
			Type: "directory",
		}

		c.LogDebug("Creating", "directory", cur)
		err := SFTPCreateFile(c, sftpConn, cur, args, false)
		if err != nil {
			return err
		}
	}

	return nil
}

// Stop stops the instance.
func (r *Instance) Stop(ctx context.Context, opts ...Option) error {
	options := NewOptions(opts...)

	if err := r.client.hookBefore(ctx, ActionStop, r, options, nil); err != nil {
		return err
	}

	if !r.IsEnsured() {
		return r.client.hookAfter(ctx, ActionStop, r, options, ErrNotEnsured)
	}

	if !r.Running() {
		err := r.SetHealthCheckingStopped(ctx, true)
		if err != nil {
			return r.client.hookAfter(ctx, ActionStop, r, options, err)
		}

		return r.client.hookAfter(ctx, ActionStop, r, options, ErrNotRunning)
	}

	err := r.SetHealthCheckingStopped(ctx, true)
	if err != nil {
		return r.client.hookAfter(ctx, ActionStop, r, options, err)
	}

	stopCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()

	err = r.stop(stopCtx, options)

	return r.client.hookAfter(ctx, ActionStop, r, options, err)
}

func (r *Instance) stop(ctx context.Context, options Options) error {
	if !r.Running() {
		return nil
	}

	err := r.waitBusyOperation(ctx)
	if err != nil {
		return err
	}

	// The wait may have let a concurrent stop finish, re-check.
	err = r.fetch()
	if err != nil {
		return err
	}

	if !r.Running() {
		return nil
	}

	_, err = retryBusy(ctx, r, func() (struct{}, error) {
		op, err := r.conn.UpdateInstanceState(r.incusName, incusApi.InstanceStatePut{
			Action:  "stop",
			Force:   options.Force,
			Timeout: options.incusTimeout(),
		}, "")
		if err != nil {
			return struct{}{}, ErrOperation.WithText("stopping instance").Wrap(err)
		}

		return struct{}{}, r.client.hookOperation(ctx, ActionStop, r, options, op, nil)
	})
	if err != nil {
		return err
	}

	return r.fetch()
}

// instanceConfigPatch is a minimal instance PATCH body carrying only config keys.
type instanceConfigPatch struct {
	Config map[string]string `json:"config"`
}

// SetHealthCheckingStopped writes the user.healthcheck.stopped marker, which
// tells ic-healthd a stop was deliberate. The status is ic-healthd's alone.
func (r *Instance) SetHealthCheckingStopped(ctx context.Context, stopped bool) error {
	if err := r.fetch(); err != nil {
		return err
	}

	value := "false"
	if stopped {
		value = "true"
	}

	if r.IncusInstance.Config[HealthStoppedKey] == value {
		return nil
	}

	return r.patchConfig(ctx, map[string]string{HealthStoppedKey: value})
}

// patchConfig writes the given keys and only those.
func (r *Instance) patchConfig(ctx context.Context, config map[string]string) error {
	info, err := r.conn.GetConnectionInfo()
	if err != nil {
		return err
	}

	path := incusApi.NewURL().
		Path("1.0", "instances", r.IncusName()).
		Project(info.Project).
		Target(info.Target).
		String()

	_, err = retryBusy(ctx, r, func() (struct{}, error) {
		_, _, err := r.conn.RawQuery("PATCH", path, instanceConfigPatch{Config: config}, "")

		return struct{}{}, err
	})
	if err != nil {
		return err
	}

	return r.fetch()
}

// addMissingConfig adds declared config keys the instance does not have yet.
func (r *Instance) addMissingConfig(ctx context.Context) error {
	missing := map[string]string{}

	for key, value := range r.Config.Extensions {
		_, ok := r.IncusInstance.Config[key]
		if ok {
			continue
		}

		missing[key] = value
	}

	if len(missing) == 0 {
		return nil
	}

	r.client.LogDebug("Adding missing instance config", "resource", r, "keys", slices.Sorted(maps.Keys(missing)))

	return r.patchConfig(ctx, missing)
}

// MarkDelete marks a instance to be deleted after Ensure(),
// this is for down scaling instances.
func (r *Instance) MarkDelete() {
	r.deleteMarked = true
}

// Delete removes the instance from Incus.
func (r *Instance) Delete(ctx context.Context, opts ...Option) error {
	options := NewOptions(opts...)

	if err := r.client.hookBefore(ctx, ActionDelete, r, options, nil); err != nil {
		r.IncusInstance = nil

		r.client.resources.Remove(r)
		return err
	}

	if !r.IsEnsured() {
		r.IncusInstance = nil

		r.client.resources.Remove(r)
		return r.client.hookAfter(ctx, ActionDelete, r, options, ErrNotEnsured)
	}

	// Do the delete
	_, err := retryBusy(ctx, r, func() (struct{}, error) {
		op, err := r.conn.DeleteInstance(r.incusName)

		return struct{}{}, r.client.hookOperation(ctx, ActionDelete, r, options, op, err)
	})

	if err := r.client.hookAfter(ctx, ActionDelete, r, options, err); err != nil {
		r.IncusInstance = nil

		r.client.resources.Remove(r)
		return err
	}

	r.IncusInstance = nil

	r.client.resources.Remove(r)
	return nil
}

// Log streams the instance console log to the outputHandler.
func (r *Instance) Log(ctx context.Context, opts ...Option) error {
	options := NewOptions(opts...)

	if err := r.client.hookBefore(ctx, ActionLog, r, options, nil); err != nil {
		return err
	}

	if r.conn == nil {
		conn, err := r.client.Connection()
		if err != nil {
			return r.client.hookAfter(ctx, ActionLog, r, options, err)
		}

		r.conn = conn
	}

	err := r.fetch()
	if err != nil {
		return r.client.hookAfter(ctx, ActionLog, r, options, err)
	}

	err = r.log(ctx, options)
	err = r.client.hookAfter(ctx, ActionLog, r, options, err)

	return err
}

func (r *Instance) log(ctx context.Context, options Options) error {
	outputHandler := r.client.globalClient.outputHandler
	if outputHandler == nil {
		return nil
	}

	if options.Follow {
		if err := r.logBuffer(outputHandler); err != nil {
			return err
		}
		return r.logStream(ctx, options, outputHandler)
	}

	return r.logBuffer(outputHandler)
}

// logBuffer reads the saved console log buffer via GET /console (equivalent to
// `incus console --show-log`). Used for non-follow log retrieval.
func (r *Instance) logBuffer(outputHandler func(Action, Resource, []byte)) error {
	reader, err := r.conn.GetInstanceConsoleLog(r.incusName, nil)
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

// logStream streams the console using WebSocket until context is canceled.
func (r *Instance) logStream(ctx context.Context, options Options, outputHandler func(Action, Resource, []byte)) error {
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

	op, err := r.conn.ConsoleInstance(r.incusName, req, args)
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
		return nil //nolint:nilerr // caller-initiated cancellation, not a streaming failure
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

// resolveEntrypoint builds oci.entrypoint from a compose entrypoint and command.
// Incus splits the value back with shellquote.Split, so Join is its inverse.
//
// A non-nil entrypoint replaces the image's argv outright: the compose spec
// discards the image's default command in that case, so nothing from the image
// is needed. With no entrypoint the command can only be appended to the image
// entrypoint, because Incus reports it with the image command already merged in
// and never exposes the two separately.
func resolveEntrypoint(imageEntrypoint string, entrypoint, command []string) string {
	if entrypoint != nil {
		return shellquote.Join(slices.Concat(entrypoint, command)...)
	}

	if len(command) == 0 {
		return ""
	}

	return imageEntrypoint + " " + shellquote.Join(command...)
}

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

var (
	_ Resource   = (*Instance)(nil)
	_ EnsureAble = (*Instance)(nil)
	_ StartAble  = (*Instance)(nil)
	_ StopAble   = (*Instance)(nil)
	_ DeleteAble = (*Instance)(nil)
	_ LogAble    = (*Instance)(nil)
)
