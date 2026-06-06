package client

import (
	"errors"
	"fmt"
	"os"
	"strings"

	incusApi "github.com/lxc/incus/v6/shared/api"
)

// KindHealthd is the resource kind for healthd instances.
const KindHealthd Kind = "healthd"

// PriorityHealthd is after all regular instances.
const PriorityHealthd = PriorityInstance + 1

// DefaultHealthdImage is the default container image for healthd.
const DefaultHealthdImage = "registry.gitlab.com/r3j0/incus-compose/ic-healthd:{version}"

// HealthdConfig configures the healthd sidecar instance.
type HealthdConfig struct {
	// IncusURL is the Incus API endpoint (network gateway IP).
	IncusURL string `json:"incus_url"`

	// Network is the Incus bridge name healthd should attach to and use to reach
	// the Incus API. When empty, resolveNetwork() auto-detects the bridge.
	Network string `json:"network,omitempty"`

	// Image overrides the default healthd container image name.
	Image string `json:"-"`

	// ImageResource is the ensured Image resource for the healthd container.
	// This is set by cmd/incus-compose after resolving the image server.
	ImageResource *Image `json:"-"`

	// Binary is the path to a local ic-healthd binary to push into the container.
	// If set, this binary is pushed to /usr/local/bin/ic-healthd before start.
	Binary string `json:"-"`
}

// GetConfig returns the configuration.
func (c *HealthdConfig) GetConfig() any {
	return c
}

// Healthd manages the healthd sidecar instance.
type Healthd struct {
	*BaseResource

	client    *Client
	incusName string
	config    *HealthdConfig
	image     string

	ensured  bool
	created  bool
	instance *Instance // backing Instance; set on creation or lazily by getInstance()
}

type healthdWatcherState struct {
	existed bool
	changed bool
}

func newHealthd(c *Client, name string, configGetter Config) (*Healthd, error) {
	if configGetter == nil {
		return nil, ErrUnknownConfig.WithKindName(KindHealthd, name)
	}

	config, ok := configGetter.GetConfig().(*HealthdConfig)
	if !ok {
		return nil, ErrUnknownConfig.WithKindName(KindHealthd, name)
	}

	image := config.Image
	if image == "" {
		if config.Binary != "" {
			// Use system container when pushing local binary
			image = "images:alpine/edge"
		} else {
			image = DefaultHealthdImage
		}
	}

	h := &Healthd{
		BaseResource: NewBaseResource(KindHealthd, name, PriorityHealthd),
		client:       c,
		incusName:    sanitizeInstanceName(name),
		config:       config,
		image:        image,
	}

	if err := c.registerHealthdWatcher(h); err != nil {
		return nil, err
	}

	return h, nil
}

func (c *Client) registerHealthdWatcher(h *Healthd) error {
	st := &healthdWatcherState{}
	c.snapshotHealthdWatcher(h, st)

	c.AddHookConnected(func(err error) error {
		c.snapshotHealthdWatcher(h, st)
		return err
	})

	c.AddHookAfter(func(action Action, r Resource, _ Options, err error) error {
		if healthdInstanceChanged(h, action, r, err) {
			st.changed = true
			c.LogDebug("HealthdWatcher instance changed", "action", action, "instance", r.IncusName())
		}
		return err
	})

	c.AddHookDone(func(err error) error {
		c.LogDebug("HealthdWatcher disconnecting", "healthd", h.IncusName(), "existed", st.existed, "changed", st.changed)
		if !st.existed || !st.changed {
			return err
		}

		state, _, e := c.incus.GetInstanceState(h.IncusName())
		if e != nil {
			c.LogDebug("HealthdWatcher healthd missing, skipping reload", "healthd", h.IncusName(), "error", e)
			return err
		}
		if state.StatusCode != incusApi.Running {
			c.LogDebug("HealthdWatcher healthd not running, skipping reload", "healthd", h.IncusName(), "status", state.Status)
			return err
		}

		if e := h.Reload(); e == nil {
			c.LogDebug("HealthdWatcher reloaded healthd", "healthd", h.IncusName())
			return err
		}

		c.LogWarn("Reloading healthd failed, restarting", "healthd", h.IncusName(), "error", e)
		return errors.Join(err, e, h.Stop(OptionForce()), h.Start())
	})

	return nil
}

func (c *Client) snapshotHealthdWatcher(h *Healthd, st *healthdWatcherState) {
	if c.incus == nil {
		st.existed = false
		return
	}

	_, _, err := c.incus.GetInstance(h.IncusName())
	st.existed = err == nil
	c.LogDebug("HealthdWatcher snapshot", "healthd", h.IncusName(), "existed", st.existed)
}

func healthdInstanceChanged(h *Healthd, action Action, r Resource, err error) bool {
	if err != nil || r.Kind() != KindInstance {
		return false
	}

	inst, ok := r.(*Instance)
	if !ok || inst.IncusName() == h.IncusName() {
		return false
	}

	switch action {
	case ActionEnsure:
		return inst.Created()
	case ActionStart, ActionStop, ActionDelete:
		return true
	default:
		return false
	}
}

// String is for debugging.
func (r *Healthd) String() string {
	return fmt.Sprintf("%v(%v)", r.kind, r.incusName)
}

// IncusName returns the sanitized instance name used in Incus.
func (r *Healthd) IncusName() string {
	return r.incusName
}

// IsEnsured returns true if the resource has been ensured.
func (r *Healthd) IsEnsured() bool {
	return r.ensured
}

// Created returns true if the resource was created during the last Ensure call.
func (r *Healthd) Created() bool {
	return r.created
}

// getInstance resolves and ensures the backing ic-healthd Instance.
// Returns ErrNotFound if the container does not exist in Incus.
func (r *Healthd) getInstance() (*Instance, error) {
	if r.instance != nil {
		return r.instance, nil
	}
	res, err := r.client.Resource(KindInstance, r.name, &InstanceConfig{Image: r.image})
	if err != nil {
		return nil, err
	}
	inst, ok := res.(*Instance)
	if !ok {
		return nil, ErrUnknownConfig.WithKindName(KindInstance, r.name)
	}
	// no OptionCreate: loads existing state, returns ErrNotFound if absent
	if err := inst.Ensure(); err != nil {
		return nil, err
	}
	r.instance = inst
	return inst, nil
}

// Ensure creates the healthd instance if it doesn't exist.
func (r *Healthd) Ensure(opts ...Option) error {
	options := NewOptions(opts...)

	// Check if instance already exists
	_, _, err := r.client.incus.GetInstance(r.incusName)
	if err == nil {
		r.ensured = true
		r.created = false
		if inst, e := r.getInstance(); e == nil {
			r.instance = inst
		}
		return nil
	}

	if !options.Create {
		return ErrNotFound.WithKindName(KindHealthd, r.name)
	}

	// Create restricted token for this project
	// Token creation requires server to be listening on network - skip for MVP if unavailable
	token, err := r.createToken()
	if err != nil {
		// Log but continue - ic-healthd will need to handle the missing token
		r.client.LogWarn("Failed to get a token", "error", err)
		token = ""
	}

	// Create the healthd instance (stopped)
	err = r.createInstance()
	if err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}

	// Start instance first (/run is tmpfs, only exists when running)
	if err := r.instance.Start(); err != nil {
		return fmt.Errorf("starting instance: %w", err)
	}

	if err := r.instance.mkdirP("/var/lib/ic-healthd"); err != nil {
		return fmt.Errorf("creating data-dir '/var/lib/ic-healthd': %w", err)
	}

	if token != "" {
		err = r.instance.pushFile("/run/secrets/ic-healthd/token", []byte(token), 0o400, true)
		if err != nil {
			return fmt.Errorf("pushing token to '/run/secrets/ic-healthd/token': %w", err)
		}
	}

	// For native images with binary, exec the healthd process
	// (oci.entrypoint only works for OCI images)
	if r.config.Binary != "" {
		if err := r.execHealthd(); err != nil {
			return fmt.Errorf("exec healthd: %w", err)
		}
	}

	r.ensured = true
	r.created = true
	return nil
}

// Start starts the healthd instance.
func (r *Healthd) Start(opts ...Option) error {
	inst, err := r.getInstance()
	if err != nil {
		return err
	}
	return inst.Start(opts...)
}

// Stop stops the healthd instance.
func (r *Healthd) Stop(opts ...Option) error {
	inst, err := r.getInstance()
	if err != nil {
		return err
	}
	return inst.Stop(opts...)
}

// Delete removes the healthd instance.
func (r *Healthd) Delete(opts ...Option) error {
	inst, err := r.getInstance()
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil // already gone
		}
		return err
	}
	if err := inst.Stop(OptionForce()); err != nil {
		r.client.LogDebug("Stopping healthd before delete", "error", err)
	}
	if err := inst.Delete(opts...); err != nil {
		return err
	}
	r.ensured = false
	r.created = false
	r.client.resources.Remove(r)

	return r.revokeCert()
}

// Log streams the healthd instance console log to the output handler.
func (r *Healthd) Log(opts ...Option) error {
	inst, err := r.getInstance()
	if err != nil {
		return err
	}
	return inst.Log(opts...)
}

// Reload sends SIGHUP to the ic-healthd process.
func (r *Healthd) Reload() error {
	req := incusApi.InstanceExecPost{
		Command:     []string{"sh", "-c", "pids=\"$(pidof ic-healthd)\" && for pid in $pids; do kill -HUP \"$pid\"; done"},
		WaitForWS:   false,
		Interactive: false,
	}

	op, err := r.client.incus.ExecInstance(r.incusName, req, nil)
	if err != nil {
		return err
	}

	return op.Wait()
}

// revokeCert removes the healthd's trust-store certificate, if any.
// Looks up by the name set in createToken; tolerates missing entries.
func (r *Healthd) revokeCert() error {
	certs, err := r.client.globalClient.incus.GetCertificates()
	if err != nil {
		return fmt.Errorf("listing certificates: %w", err)
	}

	want := r.certName()
	for _, cert := range certs {
		if cert.Name != want {
			continue
		}
		if err := r.client.globalClient.incus.DeleteCertificate(cert.Fingerprint); err != nil {
			return fmt.Errorf("deleting certificate %s: %w", cert.Fingerprint, err)
		}
	}
	return nil
}

// findNetwork looks for a network resource in the client's resource store.
// Returns the first network resource if found, nil otherwise.
func (r *Healthd) findNetwork() *Network {
	for _, res := range r.client.resources.All() {
		if res.Kind() == KindNetwork {
			if net, ok := res.(*Network); ok {
				return net
			}
		}
	}
	return nil
}

// resolveNetwork resolves and caches the Incus bridge name healthd should attach to.
// When r.config.Network is already set it is verified to exist and an error is returned
// if not found. Otherwise the bridge is auto-detected in order:
//  1. incusbr0
//  2. eth0 device in the Incus default profile
//  3. first compose-managed network in the resource store
func (r *Healthd) resolveNetwork() error {
	if r.config.Network != "" {
		if _, _, err := r.client.incus.GetNetwork(r.config.Network); err != nil {
			return fmt.Errorf("healthd network %q not found: %w", r.config.Network, err)
		}
		return nil
	}

	// 1. incusbr0
	if _, _, err := r.client.incus.GetNetwork("incusbr0"); err == nil {
		r.config.Network = "incusbr0"
		return nil
	}

	// 2. eth0 of the default profile
	if profile, _, err := r.client.globalClient.incus.GetProfile("default"); err == nil {
		if eth0, ok := profile.Devices["eth0"]; ok {
			if name := eth0["network"]; name != "" {
				if _, _, err := r.client.incus.GetNetwork(name); err == nil {
					r.config.Network = name
					return nil
				}
			}
		}
	}

	// 3. first compose-managed network
	if network := r.findNetwork(); network != nil {
		r.config.Network = network.IncusName()
		return nil
	}

	return fmt.Errorf("no network found for healthd: set --healthd-network or x-incus-compose.healthd-network")
}

// resolveIncusURL gets the Incus API URL via the resolved bridge's gateway IP.
func (r *Healthd) resolveIncusURL() error {
	if r.config.IncusURL != "" {
		return nil
	}

	if err := r.resolveNetwork(); err != nil {
		return err
	}

	netInfo, _, err := r.client.incus.GetNetwork(r.config.Network)
	if err != nil {
		return fmt.Errorf("getting network info for %s: %w", r.config.Network, err)
	}

	ipv4 := netInfo.Config["ipv4.address"]
	if ipv4 == "" {
		return fmt.Errorf("network %s has no ipv4.address", r.config.Network)
	}

	ip := strings.Split(ipv4, "/")[0]

	server, _, err := r.client.globalClient.incus.GetServer()
	if err != nil {
		return fmt.Errorf("getting server info: %w", err)
	}

	port := "8443"
	if addr := server.Config["core.https_address"]; addr != "" {
		if idx := strings.LastIndex(addr, ":"); idx != -1 {
			port = addr[idx+1:]
		}
	}

	r.config.IncusURL = fmt.Sprintf("https://%s:%s", ip, port)
	return nil
}

// certName returns the name used for this healthd's certificate in the Incus trust store.
func (r *Healthd) certName() string {
	return "ic-healthd-" + r.client.IncusProject()
}

// createToken creates a restricted token for the healthd to use.
func (r *Healthd) createToken() (string, error) {
	req := incusApi.CertificatesPost{
		CertificatePut: incusApi.CertificatePut{
			Name:       r.certName(),
			Type:       "client",
			Restricted: true,
			Projects:   []string{r.client.IncusProject()},
		},
		Token: true,
	}

	op, err := r.client.incus.CreateCertificateToken(req)
	if err != nil {
		return "", err
	}

	opAPI := op.Get()
	addToken, err := opAPI.ToCertificateAddToken()
	if err != nil {
		return "", fmt.Errorf("converting operation to certificate add token: %w", err)
	}

	return addToken.String(), nil
}

// createInstance creates the healthd container using an Instance resource.
// Sets r.instance on success; the caller is responsible for starting it.
func (r *Healthd) createInstance() error {
	// Get Incus URL from network's gateway IP
	if err := r.resolveIncusURL(); err != nil {
		return fmt.Errorf("resolving incus URL: %w", err)
	}

	flags := []string{fmt.Sprintf(" --incus=%s --project=%s", r.config.IncusURL, r.client.IncusProject())}

	// Passthrough debugging.
	if r.client.globalClient.IsDebugging() {
		flags = append(flags, " --debug")
	}

	// Create instance config
	instanceConfig := &InstanceConfig{
		Image: r.image,
		Type:  incusApi.InstanceTypeContainer,
		Config: map[string]string{
			"user.internal":           "true",
			"user.healthcheck.daemon": "true",
			"oci.entrypoint":          "/usr/local/bin/ic-healthd run" + strings.Join(flags, " "),
		},
		Files: map[string]InstanceFile{},
	}

	// Add network device — r.config.Network is already set by resolveIncusURL above.
	if r.config.Network != "" {
		instanceConfig.Devices = []InstanceDevice{
			{
				Name: "eth0",
				Config: InstanceDeviceConfig{
					DeviceType:  InstanceDeviceTypeNic,
					NetworkName: r.config.Network,
				},
			},
		}
	}

	// Persistent data volume for config.json and the registered cert/key.
	// Survives container restarts so re-registration is not required after restart.
	instanceConfig.PostDevices = []InstanceDevice{
		{
			Name: "data",
			Config: InstanceDeviceConfig{
				DeviceType: InstanceDeviceTypeDisk,
				Disk: InstanceDeviceDiskConfig{
					StorageVolumeConfig: &StorageVolumeConfig{Pool: r.client.config.DefaultStoragePool},
					Source:              r.incusName,
					Path:                "/var/lib/ic-healthd",
					Shift:               true,
				},
			},
		},
	}

	// Add image resource as dependency if using OCI image
	if r.config.ImageResource != nil {
		instanceConfig.Resources = []Resource{r.config.ImageResource}
	}

	// Add binary file if specified
	if r.config.Binary != "" {
		f, err := os.Open(r.config.Binary)
		if err != nil {
			return fmt.Errorf("opening binary %s: %w", r.config.Binary, err)
		}
		// File will be closed after instance.Ensure() pushes files

		instanceConfig.Files["/usr/local/bin/ic-healthd"] = InstanceFile{
			Content: f,
			Mode:    0o755,
		}
	}

	// Get or create instance resource
	instanceRes, err := r.client.Resource(KindInstance, r.name, instanceConfig)
	if err != nil {
		return fmt.Errorf("creating instance resource: %w", err)
	}

	instance, ok := instanceRes.(*Instance)
	if !ok {
		return fmt.Errorf("invalid instance resource type")
	}

	// Set the correct image
	instance.Config.Image = r.image

	// Ensure instance (creates if needed)
	if err := instance.Ensure(OptionCreate()); err != nil {
		return fmt.Errorf("ensuring instance: %w", err)
	}

	// Close binary file if opened
	if r.config.Binary != "" {
		if f, ok := instanceConfig.Files["/usr/local/bin/ic-healthd"].Content.(*os.File); ok {
			f.Close()
		}
	}

	r.instance = instance
	return nil
}

// execHealthd runs the healthd binary in the background (for native images).
func (r *Healthd) execHealthd() error {
	// Get Incus URL from network's gateway IP
	if err := r.resolveIncusURL(); err != nil {
		return fmt.Errorf("resolving incus URL: %w", err)
	}

	flags := []string{fmt.Sprintf(" --incus=%s --project=%s", r.config.IncusURL, r.client.IncusProject())}

	// Passthrough debugging.
	if r.client.globalClient.IsDebugging() {
		flags = append(flags, " --debug")
	}

	// Wait for network to be ready, then run healthd in background.
	// The network device might not be fully configured when the container starts.
	cmd := []string{
		"sh", "-c",
		`nohup /usr/local/bin/ic-healthd run` + strings.Join(flags, " ") + `> /var/log/ic-healthd.log 2>&1 &`,
	}

	execReq := incusApi.InstanceExecPost{
		Command:     cmd,
		WaitForWS:   false,
		Interactive: false,
	}

	op, err := r.client.incus.ExecInstance(r.incusName, execReq, nil)
	if err != nil {
		return err
	}

	return op.Wait()
}

// compile-time interface checks.
var (
	_ Resource   = (*Healthd)(nil)
	_ EnsureAble = (*Healthd)(nil)
	_ DeleteAble = (*Healthd)(nil)
	_ StartAble  = (*Healthd)(nil)
	_ StopAble   = (*Healthd)(nil)
	_ LogAble    = (*Healthd)(nil)
)
