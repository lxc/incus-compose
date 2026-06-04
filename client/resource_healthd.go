package client

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	incusClient "github.com/lxc/incus/v6/client"
	incusApi "github.com/lxc/incus/v6/shared/api"
)

// KindHealthd is the resource kind for healthd instances.
const KindHealthd Kind = "healthd"

// PriorityHealthd is after all regular instances.
const PriorityHealthd = PriorityInstance + 1

// DefaultHealthdImage is the default container image for healthd.
const DefaultHealthdImage = "registry.gitlab.com:r3j0/incus-compose/ic-healthd:latest"

// HealthdConfig configures the healthd sidecar instance.
type HealthdConfig struct {
	// IncusURL is the Incus API endpoint (network gateway IP).
	IncusURL string `json:"incus_url"`

	// Project is the Incus project to monitor.
	// Project string `json:"project"`

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

	ensured bool
	created bool
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

	return h, nil
}

// Healthd returns an existing Healthd resource or creates a new one.
func (c *Client) Healthd(name string, config HealthdConfig, reCreate bool) (*Healthd, error) {
	if existing := c.resources.Get(KindHealthd, name); existing != nil {
		res, ok := existing.(*Healthd)
		if !ok {
			return nil, ErrUnknownConfig.WithKindName(KindHealthd, name)
		}

		if !reCreate {
			return res, nil
		}

		if err := res.Delete(); err != nil {
			return nil, err
		}
	}

	h, err := newHealthd(c, name, &config)
	if err != nil {
		return nil, err
	}

	if err := c.registerHealthdWatcher(h); err != nil {
		return nil, err
	}

	c.resources.Add(h)
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

	c.AddHookDisconnecting(func(err error) error {
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

		if e := h.reload(); e == nil {
			c.LogDebug("HealthdWatcher reloaded healthd", "healthd", h.IncusName())
			return err
		} else {
			c.LogWarn("Reloading healthd failed, restarting", "healthd", h.IncusName(), "error", e)
			return errors.Join(err, e, h.restart())
		}
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

// Ensure creates the healthd instance if it doesn't exist.
func (r *Healthd) Ensure(opts ...Option) error {
	options := NewOptions(opts...)

	// Check if instance already exists
	_, _, err := r.client.incus.GetInstance(r.incusName)
	if err == nil {
		r.ensured = true
		r.created = false
		// TODO: Update config if changed?
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

	// Create the healthd instance
	err = r.createInstance()
	if err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}

	// Start instance first (/run is tmpfs, only exists when running)
	err = r.start()
	if err != nil {
		return fmt.Errorf("starting instance: %w", err)
	}

	if token != "" {
		// Token lives on the tmpfs at /run, single use; consumed on first registration.
		err = r.mkdirP("/run/secrets/ic-healthd")
		if err != nil {
			return fmt.Errorf("creating secrets dir: %w", err)
		}

		err = r.pushFile("/run/secrets/ic-healthd/token", []byte(token), 0o400)
		if err != nil {
			return fmt.Errorf("pushing token: %w", err)
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

// Delete removes the healthd instance.
func (r *Healthd) Delete(opts ...Option) error {
	options := NewOptions(opts...)

	// Check if instance exists
	instance, _, err := r.client.incus.GetInstance(r.incusName)
	if err != nil {
		// Already gone
		return nil
	}

	// Stop if running
	if instance.StatusCode == incusApi.Running {
		timeout := -1
		if options.Timeout > 0 {
			timeout = options.Timeout
		}

		req := incusApi.InstanceStatePut{
			Action:  "stop",
			Timeout: timeout,
			Force:   options.Force,
		}

		op, err := r.client.incus.UpdateInstanceState(r.incusName, req, "")
		if err != nil {
			return fmt.Errorf("stopping instance: %w", err)
		}

		if err := op.Wait(); err != nil {
			return fmt.Errorf("waiting for stop: %w", err)
		}
	}

	// Delete instance
	op, err := r.client.incus.DeleteInstance(r.incusName)
	if err != nil {
		return fmt.Errorf("deleting instance: %w", err)
	}

	if err := op.Wait(); err != nil {
		return fmt.Errorf("waiting for delete: %w", err)
	}

	if err := r.revokeCert(); err != nil {
		r.client.LogWarn("Failed to revoke healthd certificate", "error", err)
	}

	r.ensured = false
	r.created = false
	return nil
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
	// Look for any network resource that's been registered
	for _, res := range r.client.resources.All() {
		if res.Kind() == KindNetwork {
			if net, ok := res.(*Network); ok {
				return net
			}
		}
	}
	return nil
}

// resolveIncusURL gets the Incus URL from the network's gateway IP.
func (r *Healthd) resolveIncusURL() error {
	// If already set, skip
	if r.config.IncusURL != "" {
		return nil
	}

	// Find the network we're using
	network := r.findNetwork()
	if network == nil {
		return fmt.Errorf("no network found for healthd")
	}

	// Get network details to find IPv4 address
	netInfo, _, err := r.client.incus.GetNetwork(network.IncusName())
	if err != nil {
		return fmt.Errorf("getting network info: %w", err)
	}

	// Get IPv4 address (format: "10.15.162.1/24")
	ipv4 := netInfo.Config["ipv4.address"]
	if ipv4 == "" {
		return fmt.Errorf("network %s has no ipv4.address", network.IncusName())
	}

	// Strip the CIDR suffix
	ip := strings.Split(ipv4, "/")[0]

	// Get server port (default 8443)
	server, _, err := r.client.globalClient.incus.GetServer()
	if err != nil {
		return fmt.Errorf("getting server info: %w", err)
	}

	port := "8443"
	if addr := server.Config["core.https_address"]; addr != "" {
		// Format could be ":8443" or "[::]:8443" or "0.0.0.0:8443"
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
	}

	// Add network device - find a network from the client's resources
	if network := r.findNetwork(); network != nil {
		instanceConfig.Devices = []InstanceDevice{
			{
				Name: "eth0",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeNic,
					Network:    network,
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
					StorageVolumeConfig: &StorageVolumeConfig{Pool: "default"}, // TODO(r3j0): Why is "default" required here???
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

		instanceConfig.Files = map[string]InstanceFile{
			"/usr/local/bin/ic-healthd": {
				Content: f,
				Mode:    0o755,
			},
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

	return nil
}

// mkdirP creates a directory and all parent directories.
// Directories are owned by nobody (65534) to match the container user.
func (r *Healthd) mkdirP(path string) error {
	// Build list of directories from root to leaf
	dirs := []string{}
	for p := path; p != "/" && p != ""; p = parentDir(p) {
		dirs = append([]string{p}, dirs...)
	}

	for _, dir := range dirs {
		err := r.client.incus.CreateInstanceFile(r.incusName, dir, incusClient.InstanceFileArgs{
			Type: "directory",
			Mode: 0o755,
			UID:  65534,
			GID:  65534,
		})
		if err != nil {
			// Ignore if directory already exists
			continue
		}
	}

	return nil
}

// parentDir returns the parent directory of a path.
func parentDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return ""
}

// pushFile writes content to a file in the instance.
// Files are owned by nobody (65534) to match the container user.
func (r *Healthd) pushFile(path string, content []byte, mode int) error {
	return r.client.incus.CreateInstanceFile(r.incusName, path, incusClient.InstanceFileArgs{
		Content: bytes.NewReader(content),
		Mode:    mode,
		UID:     65534,
		GID:     65534,
	})
}

// start starts the healthd instance.
func (r *Healthd) start() error {
	// Check if already running
	instance, _, err := r.client.incus.GetInstance(r.incusName)
	if err == nil && instance.StatusCode == incusApi.Running {
		return nil
	}

	req := incusApi.InstanceStatePut{
		Action:  "start",
		Timeout: 30,
	}

	op, err := r.client.incus.UpdateInstanceState(r.incusName, req, "")
	if err != nil {
		return err
	}

	return op.Wait()
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

func (r *Healthd) reload() error {
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

func (r *Healthd) restart() error {
	state, _, err := r.client.incus.GetInstanceState(r.incusName)
	if err != nil {
		return err
	}

	if state.StatusCode != incusApi.Stopped {
		op, err := r.client.incus.UpdateInstanceState(r.incusName, incusApi.InstanceStatePut{
			Action:  "stop",
			Timeout: -1,
			Force:   true,
		}, "")
		if err != nil {
			return err
		}
		if err := op.Wait(); err != nil {
			return err
		}
	}

	op, err := r.client.incus.UpdateInstanceState(r.incusName, incusApi.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
	}, "")
	if err != nil {
		return err
	}
	return op.Wait()
}

// Stop stops the healthd instance.
func (r *Healthd) Stop(opts ...Option) error {
	options := NewOptions(opts...)

	timeout := 30
	if options.Timeout > 0 {
		timeout = options.Timeout
	}

	req := incusApi.InstanceStatePut{
		Action:  "stop",
		Timeout: timeout,
		Force:   options.Force,
	}

	op, err := r.client.incus.UpdateInstanceState(r.incusName, req, "")
	if err != nil {
		return err
	}

	return op.Wait()
}

// Start starts the healthd instance.
func (r *Healthd) Start(opts ...Option) error {
	// Check if already running
	state, _, err := r.client.incus.GetInstanceState(r.incusName)
	if err == nil && state.Status == "Running" {
		return nil
	}

	options := NewOptions(opts...)

	timeout := 30
	if options.Timeout > 0 {
		timeout = options.Timeout
	}

	req := incusApi.InstanceStatePut{
		Action:  "start",
		Timeout: timeout,
	}

	op, err := r.client.incus.UpdateInstanceState(r.incusName, req, "")
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
)
