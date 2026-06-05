// Package project loads Docker Compose files and configures client resources.
//
// This package is not a passive loader. It actively drives resource creation
// by calling into the client package. The typical flow is:
//
//  1. CLI creates a client.Client
//  2. project.Load() parses the compose file
//  3. project.ToStack() configures resources on the client and builds a Stack
//  4. CLI runs the Stack to execute operations on Incus
package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/errdefs"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/compose-spec/compose-go/v2/utils"
	"github.com/dominikbraun/graph"

	"gitlab.com/r3j0/incus-compose/client"
)

// LoadOptions holds configuration for Load and LoadModel.
type LoadOptions struct {
	// Project name
	Name string

	// Compose configuration file paths
	Files []string

	// Working directory (if empty, uses current directory or path of first file)
	WorkingDir string

	// Alternative environment files
	EnvFiles []string

	// Profiles to enable
	Profiles []string

	// OsEnv includes OS environment variables in project env (default: false for portability)
	OsEnv bool
}

// LoadOption is a functional option for LoadProject.
type LoadOption func(*LoadOptions)

// LoadName sets the project name.
func LoadName(name string) LoadOption {
	return func(o *LoadOptions) {
		o.Name = name
	}
}

// LoadFiles sets the compose configuration file paths.
func LoadFiles(files []string) LoadOption {
	return func(o *LoadOptions) {
		o.Files = files
	}
}

// LoadWorkingDir sets the working directory.
func LoadWorkingDir(dir string) LoadOption {
	return func(o *LoadOptions) {
		o.WorkingDir = dir
	}
}

// LoadEnvFiles sets alternative environment files.
func LoadEnvFiles(files []string) LoadOption {
	return func(o *LoadOptions) {
		o.EnvFiles = files
	}
}

// LoadProfiles sets the profiles to enable.
func LoadProfiles(profiles []string) LoadOption {
	return func(o *LoadOptions) {
		o.Profiles = profiles
	}
}

// LoadOsEnv includes OS environment variables in the project environment.
// Without this, only .env files and compose file env vars are used (more portable).
func LoadOsEnv() LoadOption {
	return func(o *LoadOptions) {
		o.OsEnv = true
	}
}

// NewLoadOptions creates LoadOptions with the given options applied.
func NewLoadOptions(opts ...LoadOption) LoadOptions {
	res := LoadOptions{
		Files:    []string{},
		Profiles: []string{},
	}

	for _, o := range opts {
		o(&res)
	}

	return res
}

// LoadModel loads the raw compose model without interpolation.
// Useful for extracting variable definitions before resolution.
func LoadModel(ctx context.Context, opts ...LoadOption) (map[string]any, error) {
	options := NewLoadOptions(opts...)

	cliOptions, err := buildProjectOptions(options, cli.WithInterpolation(false))
	if err != nil {
		return nil, err
	}

	model, err := cliOptions.LoadModel(ctx)
	if errors.Is(err, errdefs.ErrNotFound) {
		return nil, fmt.Errorf("no compose.yaml found, either change to a directory with a `compose.yaml` or use `--file`")
	}

	return model, err
}

// serviceToInstance translates a compose service to an Incus instance.
// Environment vars become instance config, labels become user metadata.
// Volumes default to bind mounts for paths starting with / or ., otherwise named volumes.
func serviceToInstance(c *client.Client, p *types.Project, serviceName string, full bool, index int, networkProfile client.Resource) ([]client.Resource, error) {
	service, ok := p.Services[serviceName]
	if !ok {
		return nil, fmt.Errorf("service %q not found", serviceName)
	}

	var errs error

	config := make(map[string]string, len(service.Environment)+len(service.Labels))

	resources := []client.Resource{}
	devices := []client.InstanceDevice{}
	postDevices := []client.InstanceDevice{}
	postStartDevices := []client.InstanceDevice{}

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

	// Command override
	if len(service.Command) > 0 {
		config["oci.entrypoint"] = formatCommand(service.Command)
	}

	// Restart policy
	applyRestartPolicy(config, service.Restart)

	// image, this will fail if the image hasn't been configured before.
	image, err := c.Resource(client.KindImage, service.Image, &client.ImageConfig{})
	if err != nil {
		errs = errors.Join(errs, err)
	}

	if full && image != nil {
		// Full needs images.
		resources = append(resources, image)
	}

	// Networks
	if networkProfile != nil {
		resources = append(resources, networkProfile)
		for name := range maps.Keys(service.Networks) {
			if svcNet := service.Networks[name]; svcNet != nil && (svcNet.Ipv4Address != "" || svcNet.Ipv6Address != "") {
				errs = errors.Join(errs, fmt.Errorf("service %q network %q uses static addresses, which are not supported with x-incus-compose.network-profile", service.Name, name))
			}
		}
	} else {
		ethIdx := 0
		for name := range maps.Keys(service.Networks) {
			netConfig := &client.NetworkConfig{}
			if networkDef, ok := p.Networks[name]; ok {
				netConfig.External = bool(networkDef.External)
				netConfig.Extensions = networkExtensions(networkDef)
				netConfig.OverrideName = networkXIncusComposeNetwork(networkDef)
			}

			network, err := c.Resource(client.KindNetwork, name, netConfig)
			if err != nil {
				errs = errors.Join(errs, err)
				continue
			}

			nicConfig := client.InstanceDeviceConfig{
				DeviceType: client.InstanceDeviceTypeNic,
				Network:    network,
			}

			if svcNet := service.Networks[name]; svcNet != nil {
				nicConfig.Ipv4Address = svcNet.Ipv4Address
				nicConfig.Ipv6Address = svcNet.Ipv6Address
			}

			devices = append(devices, client.InstanceDevice{
				Name:   fmt.Sprintf("eth%d", ethIdx),
				Config: nicConfig,
			})
			ethIdx++

			resources = append(resources, network)
		}
	}

	// natProxyEntries maps listen-port → {listen IPs, connect port}.
	type natProxyEntry struct {
		listen  []string
		connect uint32
	}
	natProxyEntries := map[uint32]natProxyEntry{}

	// Extract nat-proxy configuration from x-incus-compose extension
	if xIncusCompose := serviceXIncusComposeExtensions(service); xIncusCompose != nil {
		if rawList, ok := xIncusCompose["nat-proxy"].([]any); ok {
			for _, item := range rawList {
				entry, ok := item.(map[string]any)
				if !ok {
					continue
				}
				var lPort uint64
				switch v := entry["port"].(type) {
				case int:
					lPort = uint64(v)
				case float64:
					lPort = uint64(v)
				case string:
					var portErr error
					lPort, portErr = strconv.ParseUint(v, 10, 32)
					if portErr != nil {
						errs = errors.Join(errs, fmt.Errorf("nat-proxy port %q is not a number: %w", v, portErr))
						continue
					}
				}
				var connectPort uint32
				switch v := entry["connect"].(type) {
				case int:
					connectPort = uint32(v)
				case float64:
					connectPort = uint32(v)
				}
				var listenIPs []string
				if rawListen, ok := entry["listen"].([]any); ok {
					for _, ip := range rawListen {
						if s, ok := ip.(string); ok {
							listenIPs = append(listenIPs, s)
						}
					}
				}
				natProxyEntries[uint32(lPort)] = natProxyEntry{listen: listenIPs, connect: connectPort}
			}
		}
	}

	// Resolve empty listen lists from the project's bridge network addresses.
	// Collect NIC-referenced network names once, reuse for all unspecified entries.
	var bridgeAddrs []string
	for lPort, entry := range natProxyEntries {
		if len(entry.listen) > 0 {
			continue
		}
		if bridgeAddrs == nil {
			for _, dev := range devices {
				if dev.Config.DeviceType != client.InstanceDeviceTypeNic || dev.Config.Network == nil {
					continue
				}
				v4, v6, err := c.NetworkBridgeIPs(dev.Config.Network.IncusName())
				if err != nil {
					c.LogWarn("nat-proxy: could not get bridge IPs", "network", dev.Config.Network.IncusName(), "err", err)
					continue
				}
				bridgeAddrs = append(bridgeAddrs, v4...)
				bridgeAddrs = append(bridgeAddrs, v6...)
			}
			if len(bridgeAddrs) == 0 {
				bridgeAddrs = []string{"0.0.0.0"}
			}
		}
		entry.listen = bridgeAddrs
		natProxyEntries[lPort] = entry
	}

	for _, port := range service.Ports {
		lPort, err := strconv.ParseUint(port.Published, 10, 32)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("bad publishing port %q must be a number: %w", port.Published, err))
			continue
		}

		// A nat-proxy entry for this listen port takes over — skip the userspace proxy.
		if _, covered := natProxyEntries[uint32(lPort)]; covered {
			continue
		}

		proto := port.Protocol
		if proto == "" {
			proto = "tcp"
		}

		listenIP := port.HostIP
		if listenIP == "" {
			listenIP = "0.0.0.0"
		}

		devices = append(devices, client.InstanceDevice{
			Name: fmt.Sprintf("proxy-%d", lPort),
			Config: client.InstanceDeviceConfig{
				DeviceType: client.InstanceDeviceTypeProxy,
				Proxy: client.InstanceDeviceProxyConfig{
					ListenType:  proto,
					ListenAddr:  listenIP,
					ListenPort:  uint32(lPort),
					ConnectType: proto,
					ConnectAddr: "127.0.0.1",
					ConnectPort: port.Target,
				},
			},
		})
	}

	// Create NAT proxy devices — one per listen IP per nat-proxy entry.
	// connect.addr is left empty and resolved in attachPostStartDevices once the instance is running.
	hasNic := func() bool {
		for _, dev := range devices {
			if dev.Config.DeviceType == client.InstanceDeviceTypeNic {
				return true
			}
		}
		return false
	}()

	for lPort, entry := range natProxyEntries {
		if !hasNic {
			c.LogWarn("nat-proxy requested but no managed NIC found, skipping", "service", service.Name, "port", lPort)
			continue
		}
		for idx, listenIP := range entry.listen {
			postStartDevices = append(postStartDevices, client.InstanceDevice{
				Name: fmt.Sprintf("proxy-%d-%d", lPort, idx),
				Config: client.InstanceDeviceConfig{
					DeviceType: client.InstanceDeviceTypeProxy,
					Proxy: client.InstanceDeviceProxyConfig{
						ListenType:  "tcp",
						ListenAddr:  listenIP,
						ListenPort:  lPort,
						ConnectType: "tcp",
						ConnectAddr: "", // resolved in attachPostStartDevices
						ConnectPort: entry.connect,
						Nat:         true,
					},
				},
			})
		}
	}

	for _, cVol := range service.Volumes {
		if cVol.Type == "" {
			// Infer type from source path (short syntax compatibility)
			// Absolute or relative paths are bind mounts, named sources are volumes
			if cVol.Source != "" && (strings.HasPrefix(cVol.Source, "/") || strings.HasPrefix(cVol.Source, ".")) {
				cVol.Type = "bind"
			} else if cVol.Source != "" {
				cVol.Type = "volume"
			}
		}

		switch cVol.Type {
		case "volume":
			volConfig := &client.StorageVolumeConfig{}

			_, err := c.Resource(client.KindStorageVolume, cVol.Source, volConfig)
			if err != nil {
				errs = errors.Join(errs, err)
				continue
			}

			devName := fmt.Sprintf("vol-%s", cVol.Source)
			devConfig := client.InstanceDeviceConfig{
				DeviceType: client.InstanceDeviceTypeDisk,
				Disk: client.InstanceDeviceDiskConfig{
					StorageVolumeConfig: volConfig,
					Source:              cVol.Source,
					Path:                cVol.Target,
					Shift:               true,
				},
			}

			if cVol.ReadOnly {
				devConfig.Disk.ReadOnly = true
			}

			postDevices = append(postDevices, client.InstanceDevice{Name: devName, Config: devConfig})
		case "bind":
			devName := fmt.Sprintf("bind-%s", strings.ReplaceAll(cVol.Source, "/", "-"))
			devConfig := client.InstanceDeviceConfig{
				DeviceType: client.InstanceDeviceTypeDisk,
				Disk: client.InstanceDeviceDiskConfig{
					Source: cVol.Source,
					Path:   cVol.Target,
					Shift:  true,
				},
			}

			if cVol.ReadOnly {
				devConfig.Disk.ReadOnly = true
			}

			postDevices = append(postDevices, client.InstanceDevice{Name: devName, Config: devConfig})
		case "tmpfs":
			devName := fmt.Sprintf("tmpfs-%s", strings.ReplaceAll(cVol.Target, "/", "-"))
			devConfig := client.InstanceDeviceConfig{
				DeviceType: client.InstanceDeviceTypeTmpfs,
				Tmpfs: client.InstanceDeviceTmpfsConfig{
					Path: cVol.Target,
					Size: formatTmpfsSize(cVol.Tmpfs),
				},
			}
			devices = append(devices, client.InstanceDevice{Name: devName, Config: devConfig})
		default:
			err := fmt.Errorf("Unknown volume type %q for service %q", cVol.Type, service.Name)
			errs = errors.Join(errs, err)
			continue
		}
	}

	// Copy "restart"
	if service.Restart != "" {
		config["user.restart"] = service.Restart
	}

	// Resource limits
	if service.Deploy != nil && service.Deploy.Resources.Limits != nil {
		applyResourceLimits(config, service.Deploy.Resources.Limits)
	}

	// Healtcheck
	if service.HealthCheck != nil {
		config["user.healthcheck.status"] = "starting"

		testB, err := json.Marshal(service.HealthCheck.Test)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("converting service %q healthcheck test: %w", service.Name, err))
			return nil, errs
		}
		config["user.healthcheck.test"] = string(testB)

		if service.HealthCheck.Interval != nil {
			config["user.healthcheck.interval"] = service.HealthCheck.Interval.String()
		}

		if service.HealthCheck.Retries != nil {
			config["user.healthcheck.retries"] = strconv.FormatUint(*service.HealthCheck.Retries, 10)
		}

		if service.HealthCheck.Timeout != nil {
			config["user.healthcheck.timeout"] = service.HealthCheck.Timeout.String()
		}
	}

	// Secrets
	var instanceSecrets []client.InstanceSecret
	for _, svcSecret := range service.Secrets {
		secretDef, ok := p.Secrets[svcSecret.Source]
		if !ok {
			errs = errors.Join(errs, fmt.Errorf("secret %q not defined", svcSecret.Source))
			continue
		}

		var content []byte
		var err error
		switch {
		case secretDef.File != "":
			content, err = os.ReadFile(secretDef.File)
			if err != nil {
				errs = errors.Join(errs, fmt.Errorf("reading secret %q: %w", svcSecret.Source, err))
				continue
			}
		case secretDef.Environment != "":
			content = []byte(os.Getenv(secretDef.Environment))
		default:
			errs = errors.Join(errs, fmt.Errorf("secret %q has no source (file or environment)", svcSecret.Source))
			continue
		}

		instanceSecrets = append(instanceSecrets, client.InstanceSecret{
			Source:  svcSecret.Source,
			Target:  svcSecret.Target,
			Content: content,
			UID:     parseSecretUID(svcSecret.UID),
			GID:     parseSecretGID(svcSecret.GID),
			Mode:    parseSecretMode(svcSecret.Mode),
		})
	}

	if errs != nil {
		return nil, errs
	}

	// Apply x-incus extensions (raw Incus options)
	if xIncusOpts := serviceXIncusExtensions(service); len(xIncusOpts) > 0 {
		for k, v := range xIncusOpts {
			config[k] = v
		}
	}

	// x-incus-compose extensions are extracted but not yet handled in this implementation.
	// They will be used for compose-specific transformations in future updates.
	_ = serviceXIncusComposeExtensions(service)

	// Instance name follows Docker Compose convention: {service}-{index}
	instanceName := fmt.Sprintf("%s-%d", service.Name, index)
	instanceConfig := &client.InstanceConfig{Full: full, Resources: slices.Clone(resources), Image: image.Name(), Config: config, Devices: devices, PostDevices: postDevices, PostStartDevices: postStartDevices, Secrets: instanceSecrets}
	instance, err := c.Resource(client.KindInstance, instanceName, instanceConfig)
	if err != nil {
		return nil, err
	}
	resources = append(resources, instance)

	return resources, nil
}

// applyResourceLimits maps Docker Compose deploy.resources.limits to Incus config keys.
//
// CPU mapping:
//   - Integer cpus (e.g. 2.0): limits.cpu = "2" (pin to N CPUs)
//   - Fractional cpus (e.g. 0.5): limits.cpu.allowance = "50%"
//
// Memory mapping: limits.memory = human-readable size (GiB, MiB, KiB, or B).
func applyResourceLimits(config map[string]string, limits *types.Resource) {
	if limits == nil {
		return
	}
	if limits.NanoCPUs != 0 {
		cpus := limits.NanoCPUs.Value()
		if cpus == float32(int(cpus)) {
			config["limits.cpu"] = strconv.Itoa(int(cpus))
		} else {
			config["limits.cpu.allowance"] = fmt.Sprintf("%.0f%%", float64(cpus)*100)
		}
	}
	if limits.MemoryBytes != 0 {
		config["limits.memory"] = formatMemoryLimit(int64(limits.MemoryBytes))
	}
}

// formatMemoryLimit converts bytes to a human-readable Incus memory limit string.
func formatMemoryLimit(bytes int64) string {
	const (
		gib = 1 << 30
		mib = 1 << 20
		kib = 1 << 10
	)
	switch {
	case bytes%gib == 0:
		return fmt.Sprintf("%dGiB", bytes/gib)
	case bytes%mib == 0:
		return fmt.Sprintf("%dMiB", bytes/mib)
	case bytes%kib == 0:
		return fmt.Sprintf("%dKiB", bytes/kib)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

// applyRestartPolicy maps Docker Compose restart policies to Incus boot config.
//
// Mapping:
//   - "no" (default): boot.autostart=false
//   - "always": boot.autostart=true
//   - "on-failure": boot.autostart=true, boot.autorestart=true
//   - "unless-stopped": boot.autostart unset (uses last-state behavior)
func applyRestartPolicy(config map[string]string, policy string) {
	switch policy {
	case "always":
		config["boot.autostart"] = "true"
	case "on-failure":
		config["boot.autostart"] = "true"
		config["boot.autorestart"] = "true"
	case "unless-stopped":
		// Leave unset - Incus defaults to "last-state" behavior
	case "no", "":
		config["boot.autostart"] = "false"
	}
}

// formatTmpfsSize converts compose tmpfs size to a string.
func formatTmpfsSize(opts *types.ServiceVolumeTmpfs) string {
	if opts == nil || opts.Size == 0 {
		return ""
	}
	return strconv.FormatInt(int64(opts.Size), 10)
}

// parseSecretUID parses a UID string to int64.
func parseSecretUID(uid string) int64 {
	if uid == "" {
		return 0
	}
	v, _ := strconv.ParseInt(uid, 10, 64)
	return v
}

// parseSecretGID parses a GID string to int64.
func parseSecretGID(gid string) int64 {
	if gid == "" {
		return 0
	}
	v, _ := strconv.ParseInt(gid, 10, 64)
	return v
}

// parseSecretMode parses a file mode to int.
func parseSecretMode(mode *types.FileMode) int {
	if mode == nil {
		return 0
	}
	return int(*mode)
}

// networkXIncusComposeNetwork extracts the x-incus-compose.network string override
// from a compose network definition. Returns "" if not set.
func networkXIncusComposeNetwork(networkDef types.NetworkConfig) string {
	var raw map[string]any
	ok, err := networkDef.Extensions.Get("x-incus-compose", &raw)
	if !ok || err != nil {
		return ""
	}
	n, ok := raw["network"].(string)
	if !ok {
		return ""
	}
	return n
}

// networkExtensions extracts the x-incus extension map from a compose network
// definition and returns it as a flat map[string]string for use as Incus network
// config. Keys and values are taken verbatim from the x-incus YAML block.
func networkExtensions(networkDef types.NetworkConfig) map[string]string {
	var raw map[string]any
	ok, err := networkDef.Extensions.Get("x-incus", &raw)
	if !ok || err != nil || len(raw) == 0 {
		return nil
	}

	result := make(map[string]string, len(raw))
	for k, v := range raw {
		result[k] = fmt.Sprint(v)
	}

	return result
}

func projectXIncusComposeExtensions(p *Project) map[string]any {
	if p == nil || p.Project == nil || p.Extensions == nil {
		return nil
	}

	var raw map[string]any
	ok, err := p.Extensions.Get("x-incus-compose", &raw)
	if !ok || err != nil || len(raw) == 0 {
		return nil
	}

	return raw
}

// serviceXIncusExtensions extracts the x-incus extension map from a compose service
// definition and returns it as a flat map[string]string for use as Incus instance
// config. Keys and values are taken verbatim from the x-incus YAML block.
func serviceXIncusExtensions(service types.ServiceConfig) map[string]string {
	var raw map[string]any
	ok, err := service.Extensions.Get("x-incus", &raw)
	if !ok || err != nil || len(raw) == 0 {
		return nil
	}

	result := make(map[string]string, len(raw))
	for k, v := range raw {
		result[k] = fmt.Sprint(v)
	}

	return result
}

// serviceXIncusComposeExtensions extracts the x-incus-compose extension map from
// a compose service definition. This is for compose-specific features and
// transformations handled by incus-compose (not raw Incus options).
func serviceXIncusComposeExtensions(service types.ServiceConfig) map[string]any {
	var raw map[string]any
	ok, err := service.Extensions.Get("x-incus-compose", &raw)
	if !ok || err != nil || len(raw) == 0 {
		return nil
	}

	return raw
}

// formatCommand formats a command slice for oci.entrypoint.
func formatCommand(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	if len(cmd) == 1 {
		return cmd[0]
	}
	// Quote arguments after the first
	quoted := make([]string, len(cmd))
	quoted[0] = cmd[0]
	for i := 1; i < len(cmd); i++ {
		quoted[i] = `"` + cmd[i] + `"`
	}
	return strings.Join(quoted, " ")
}

// ServiceGraph returns services in dependency order using topological sort.
// If reverse is true, returns reverse order (useful for shutdown).
func ServiceGraph(serviceConfigs types.Services, reverse bool) ([]string, error) {
	g := graph.New(graph.StringHash, graph.Directed(), graph.PreventCycles())

	// Add vertices
	for s := range maps.Values(serviceConfigs) {
		_ = g.AddVertex(s.Name)
	}

	// Add edges for dependencies
	for s := range maps.Values(serviceConfigs) {
		for dep := range s.DependsOn {
			// Edge from dependency to dependent (dep must start before n)
			err := g.AddEdge(dep, s.Name)
			if err != nil && err != graph.ErrEdgeAlreadyExists {
				return nil, fmt.Errorf("adding dependency edge %s -> %s: %w", dep, s.Name, err)
			}
		}
	}

	order, err := graph.TopologicalSort(g)
	if err != nil {
		return nil, fmt.Errorf("topological sort: %w", err)
	}

	if reverse {
		slices.Reverse(order)
	}

	return order, nil
}

// Project wraps a Docker Compose project with Incus client integration.
type Project struct {
	*types.Project
}

// New creates a new Project.
func New() *Project {
	return &Project{}
}

// Load loads a compose project with full interpolation and validation.
func (p *Project) Load(ctx context.Context, opts ...LoadOption) (*Project, error) {
	options := NewLoadOptions(opts...)

	cliOptions, err := buildProjectOptions(options)
	if err != nil {
		return p, err
	}

	cp, err := cliOptions.LoadProject(ctx)
	if errors.Is(err, errdefs.ErrNotFound) {
		return p, fmt.Errorf("no compose.yaml found, either change to a directory with a `compose.yaml` or use `--file`")
	}

	if err != nil {
		return p, err
	}

	p.Project = cp
	return p, nil
}

// ToStackOptions configures how services are converted to stack operations.
type ToStackOptions struct {
	OnlyServices []string
	Reverse      bool
	Full         bool
	Scale        map[string]int // service name -> replica count override
}

// ToStackOption is a functional option for ToStack.
type ToStackOption func(o *ToStackOptions)

// ToStackOnlyServices limits the stack to the specified services.
func ToStackOnlyServices(services []string) ToStackOption {
	return func(o *ToStackOptions) {
		o.OnlyServices = services
	}
}

// ToStackReverse reverses the service dependency graph order.
// Use for teardown so dependants are stopped before their dependencies.
// Note: cross-kind priority ordering (e.g. instances vs networks) is handled
// automatically by Stack.ForAction and does not require this option.
func ToStackReverse() ToStackOption {
	return func(o *ToStackOptions) {
		o.Reverse = true
	}
}

// ToStackFull fetches complete instance state including image alias and full instance details.
func ToStackFull() ToStackOption {
	return func(o *ToStackOptions) {
		o.Full = true
	}
}

// ToStackScale sets replica count overrides for services.
func ToStackScale(scale map[string]int) ToStackOption {
	return func(o *ToStackOptions) {
		o.Scale = scale
	}
}

// NetworkProfileConfig reads the top-level x-incus-compose.network-profile extension.
func (p *Project) NetworkProfileConfig() (*client.ProfileConfig, error) {
	extensions := projectXIncusComposeExtensions(p)
	if extensions == nil {
		return nil, nil
	}

	raw, ok := extensions["network-profile"]
	if !ok {
		return nil, nil
	}

	value, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("x-incus-compose.network-profile must be a string in {project}:{profile} format")
	}

	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("x-incus-compose.network-profile %q must be in {project}:{profile} format", value)
	}
	if parts[0] == "" {
		return nil, fmt.Errorf("x-incus-compose.network-profile %q has empty project", value)
	}
	if parts[1] == "" {
		return nil, fmt.Errorf("x-incus-compose.network-profile %q has empty profile", value)
	}

	return &client.ProfileConfig{SourceProject: parts[0], SourceProfile: parts[1], NetworkOnly: true}, nil
}

// ProjectConfig reads `x-incus` extensions from the project and returns that.
func (p *Project) ProjectConfig() map[string]string {
	if p == nil || p.Project == nil || p.Extensions == nil {
		return nil
	}

	var raw map[string]any
	ok, err := p.Extensions.Get("x-incus", &raw)
	if !ok || err != nil || len(raw) == 0 {
		return nil
	}

	result := make(map[string]string, len(raw))
	for k, v := range raw {
		result[k] = fmt.Sprint(v)
	}

	return result
}

// ToStack converts the compose project services to Incus stack operations.
func (p *Project) ToStack(c *client.Client, stack *client.Stack, opts ...ToStackOption) error {
	if stack == nil {
		return client.ErrNilPointer
	}

	resources := []client.Resource{}

	options := &ToStackOptions{OnlyServices: []string{}}
	for _, o := range opts {
		o(options)
	}

	var errs error

	profileConfig, err := p.NetworkProfileConfig()
	if err != nil {
		return err
	}

	var networkProfile client.Resource
	if profileConfig != nil {
		networkProfile, err = c.Resource(client.KindProfile, "default", profileConfig)
		if err != nil {
			return err
		}
		resources = append(resources, networkProfile)
	}

	if len(options.OnlyServices) >= 1 {
		services := types.Services{}
		for n, svc := range p.Services {
			for _, on := range options.OnlyServices {
				if strings.HasPrefix(on+"-", n+"-") {
					services[n] = svc
					for depName := range svc.DependsOn {
						services[depName] = p.Services[depName]
					}
				}
			}
		}

		p.Services = services
	}

	serviceOrder, err := ServiceGraph(p.Services, options.Reverse)
	if err != nil {
		return err
	}

	// Configure instances
	for _, serviceName := range serviceOrder {
		service, ok := p.Services[serviceName]
		if !ok {
			return fmt.Errorf("found %q a service that does not exists in services, this should never happen", serviceName)
		}

		// Determine scale: CLI override > deploy.replicas > 1
		scale := 1
		if s, ok := options.Scale[serviceName]; ok {
			scale = s
		} else if service.Deploy != nil && service.Deploy.Replicas != nil {
			scale = int(*service.Deploy.Replicas)
		}

		for {
			instanceName := fmt.Sprintf("%s-%d", service.Name, scale+1)
			if ok, err := c.InstanceExists(instanceName); !ok || err != nil {
				break
			}

			scale = scale + 1
		}

		for i := 1; i <= scale; i++ {
			instanceResources, err := serviceToInstance(c, p.Project, serviceName, options.Full, i, networkProfile)
			if err != nil {
				errs = errors.Join(errs, err)
				continue
			}

			resources = append(resources, instanceResources...)
		}
	}

	if errs != nil {
		return errs
	}

	resources = client.FilterDuplicates(resources)
	stack.Add(resources...)

	if !c.IsConnected() {
		return nil
	}

	return nil
}

// buildProjectOptions creates cli.ProjectOptions from LoadOptions.
func buildProjectOptions(options LoadOptions, extraOpts ...cli.ProjectOptionsFn) (*cli.ProjectOptions, error) {
	projectOptions := []cli.ProjectOptionsFn{}

	if options.WorkingDir != "" {
		projectOptions = append(projectOptions, cli.WithWorkingDirectory(options.WorkingDir))
	}

	// Include OS env if requested (full docker-compose compatibility)
	if options.OsEnv {
		projectOptions = append(projectOptions, cli.WithOsEnv)
	}

	// Load .env files with OS env available for interpolation but not added to project
	projectOptions = append(projectOptions,
		cli.WithEnvFiles(options.EnvFiles...),
		withDotEnvAndOsEnv, // Custom handler: uses OS env for interpolation only
		cli.WithConfigFileEnv,
		cli.WithDefaultConfigPath,
		// Apply env files again after working dir is determined
		cli.WithEnvFiles(options.EnvFiles...),
		withDotEnvAndOsEnv,
	)

	if options.Name != "" {
		projectOptions = append(projectOptions, cli.WithName(options.Name))
	}

	if len(options.Profiles) > 0 {
		projectOptions = append(projectOptions, cli.WithProfiles(options.Profiles))
	}

	// Add any extra options (e.g., WithoutEnvironmentResolution)
	projectOptions = append(projectOptions, extraOpts...)

	return cli.NewProjectOptions(
		options.Files,
		projectOptions...,
	)
}

// getOsEnv returns OS environment variables as a map.
func getOsEnv() map[string]string {
	return utils.GetAsEqualsMap(os.Environ())
}

// withDotEnvAndOsEnv loads .env files using OS env for interpolation only.
// OS env variables are NOT added to the project environment unless LoadOsEnv is used.
// This provides portability while allowing .env files to reference system variables.
func withDotEnvAndOsEnv(o *cli.ProjectOptions) error {
	// Get OS env for interpolation
	osEnv := getOsEnv()

	// Merge current project env with OS env for lookups
	lookupEnv := make(map[string]string, len(osEnv)+len(o.Environment))
	maps.Copy(lookupEnv, osEnv)
	maps.Copy(lookupEnv, o.Environment)

	// Parse .env files using combined env for interpolation
	envMap, err := dotenv.GetEnvFromFile(lookupEnv, o.EnvFiles)
	if err != nil {
		return err
	}

	// Only merge the .env results into project environment
	o.Environment.Merge(envMap)
	return nil
}
