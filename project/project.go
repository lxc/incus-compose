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
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/errdefs"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/compose-spec/compose-go/v2/utils"
	"github.com/dominikbraun/graph"

	"github.com/lxc/incus-compose/client"
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

func buildPlatform(service types.ServiceConfig) (string, error) {
	if service.Build == nil {
		return "", nil
	}
	if len(service.Build.Platforms) > 1 {
		return "", fmt.Errorf("build.platforms with multiple platforms is not supported")
	}
	if len(service.Build.Platforms) == 1 {
		return service.Build.Platforms[0], nil
	}
	return service.Platform, nil
}

// serviceToInstance translates a compose service to an Incus instance.
// Environment vars become instance config, labels become user metadata.
// Volumes default to bind mounts for paths starting with / or ., otherwise named volumes.
func serviceToInstance(c *client.Client, p *types.Project, serviceName string, options *ToStackOptions, index, scale int) ([]client.Resource, error) {
	service, ok := p.Services[serviceName]
	if !ok {
		return nil, fmt.Errorf("service %q not found", serviceName)
	}

	var errs error

	config := make(map[string]string, len(service.Environment)+len(service.Labels))

	resources := []client.Resource{}
	devices := []client.InstanceDevice{}
	postStartDevices := []client.InstanceDevice{}
	files := map[string]client.InstanceFile{}

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

	var (
		image client.Resource
		err   error
	)
	if !options.NoImages {
		if service.Build != nil {
			imageName := service.Image
			if imageName == "" {
				imageName = "localhost/" + service.Name
			}
			platform, platformErr := buildPlatform(service)
			if platformErr != nil {
				errs = errors.Join(errs, platformErr)
			}
			buildCfg := &client.BuildConfig{
				Context:          service.Build.Context,
				Dockerfile:       service.Build.Dockerfile,
				DockerfileInline: service.Build.DockerfileInline,
				Target:           service.Build.Target,
				Platform:         platform,
				NoCache:          service.Build.NoCache,
				Pull:             service.Build.Pull,
				Args:             service.Build.Args.ToMapping(),
			}
			if len(service.Build.Args) > 0 {
				buildCfg.Args = make(map[string]string, len(service.Build.Args))
				for k, v := range service.Build.Args {
					if v != nil {
						buildCfg.Args[k] = *v
					}
				}
			}
			image, err = c.Resource(client.KindImage, imageName, &client.ImageConfig{Build: buildCfg})
		} else {
			image, err = c.Resource(client.KindImage, service.Image, &client.ImageConfig{})
		}
		if err != nil {
			errs = errors.Join(errs, err)
		}

		img, ok := image.(*client.Image)
		if !ok {
			return nil, errors.New("not an image")
		}

		img.Config.Services = append(img.Config.Services, service.Name)

		resources = append(resources, image)
	}

	// Networks

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
				v4, v6, err := c.Global().NetworkBridgeIPs(dev.Config.Network.IncusName())
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
			volDef := p.Volumes[cVol.Source]
			volConfig := &client.StorageVolumeConfig{
				Shifted:       true,
				ImageResource: image,
				Pool:          volumeXIncusComposePool(volDef),
				ExtraConfig:   volumeXIncusExtensions(volDef),
			}

			v, err := c.Resource(client.KindStorageVolume, cVol.Source, volConfig)
			if err != nil {
				errs = errors.Join(errs, err)
				continue
			}

			if options.StorageVolumes {
				resources = append(resources, v)
			}

			devName := "vol-" + client.SanitizeIncusName(cVol.Source, client.MaxIncusNameLen-4)
			devConfig := client.InstanceDeviceConfig{
				DeviceType: client.InstanceDeviceTypeDisk,
				Disk: client.InstanceDeviceDiskConfig{
					StorageVolumeConfig: volConfig,
					Source:              v.IncusName(),
					Path:                cVol.Target,
					Shift:               true,
				},
			}

			if cVol.ReadOnly {
				devConfig.Disk.ReadOnly = true
			}

			devices = append(devices, client.InstanceDevice{Name: devName, Config: devConfig})
		case "bind":
			info, err := os.Stat(cVol.Source)
			if err != nil {
				return nil, client.ErrUnknown.WithKindName(client.KindInstance, service.Name).Wrap(err)
			}

			absSource, err := filepath.Abs(cVol.Source)
			if err != nil {
				return nil, client.ErrUnknown.WithKindName(client.KindInstance, service.Name).Wrap(err)
			}

			devName := "bind-" + client.SanitizeIncusName(cVol.Source, client.MaxIncusNameLen-5)

			if !info.IsDir() {
				files[cVol.Target] = client.InstanceFile{
					File:    absSource,
					UID:     -1,
					GID:     -1,
					Mode:    0o644,
					DirMode: 0o755,
				}
			} else {
				volConfig := &client.StorageVolumeConfig{
					Shifted:       true,
					ImageResource: image,
					HostPath:      absSource,
				}

				v, err := c.Resource(client.KindStorageVolume, "bind-"+cVol.Source, volConfig)
				if err != nil {
					errs = errors.Join(errs, err)
					continue
				}

				if options.StorageVolumes {
					resources = append(resources, v)
				}

				devConfig := client.InstanceDeviceConfig{
					DeviceType: client.InstanceDeviceTypeDisk,
					Disk: client.InstanceDeviceDiskConfig{
						StorageVolumeConfig: volConfig,
						Source:              v.IncusName(),
						Path:                cVol.Target,
						Shift:               true,
					},
				}

				if cVol.ReadOnly {
					devConfig.Disk.ReadOnly = true
				}

				devices = append(devices, client.InstanceDevice{Name: devName, Config: devConfig})
			}
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

	// shm_size mounts a tmpfs at /dev/shm with the specified size.
	if service.ShmSize > 0 {
		devices = append(devices, client.InstanceDevice{
			Name: "shm",
			Config: client.InstanceDeviceConfig{
				DeviceType: client.InstanceDeviceTypeTmpfs,
				Tmpfs: client.InstanceDeviceTmpfsConfig{
					Path: "/dev/shm",
					Size: strconv.FormatInt(int64(service.ShmSize), 10),
				},
			},
		})
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
		config[client.HealthConfigKey] = client.HealthStatusStarting

		testB, err := json.Marshal(service.HealthCheck.Test)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("converting service %q healthcheck test: %w", service.Name, err))
			return nil, errs
		}
		config["user.healthcheck.test"] = string(testB)

		if service.HealthCheck.StartPeriod != nil {
			config["user.healthcheck.start_period"] = service.HealthCheck.StartPeriod.String()
		}

		if service.HealthCheck.StartInterval != nil {
			config["user.healthcheck.start_interval"] = service.HealthCheck.StartInterval.String()
		}

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

	// Build dependency health-wait map for depends_on with condition: service_healthy.
	var deps map[string]string
	for depName, dep := range service.DependsOn {
		if dep.Condition != types.ServiceConditionHealthy {
			continue
		}
		depSvc := p.Services[depName]
		depScale := 1
		if s, ok := options.Scale[depName]; ok {
			depScale = s
		} else if depSvc.Deploy != nil && depSvc.Deploy.Replicas != nil {
			depScale = int(*depSvc.Deploy.Replicas)
		}
		if deps == nil {
			deps = make(map[string]string)
		}
		if depSvc.ContainerName != "" {
			deps[client.SanitizeIncusName(depSvc.ContainerName, client.MaxIncusNameLen)] = client.HealthStatusHealthy
		} else {
			for i := 1; i <= depScale; i++ {
				deps[client.SanitizeIncusName(fmt.Sprintf("%s-%d", depName, i), client.MaxIncusNameLen)] = client.HealthStatusHealthy
			}
		}
	}

	// Instance name: container_name takes precedence, otherwise {service}-{index}.
	instanceName := fmt.Sprintf("%s-%d", service.Name, index)
	if service.ContainerName != "" {
		if scale > 1 {
			instanceName = fmt.Sprintf("%s-%d", service.ContainerName, index)
		} else {
			instanceName = service.ContainerName
		}
	}

	instanceConfig := &client.InstanceConfig{
		ServiceName:      service.Name,
		Full:             options.Full,
		Resources:        slices.Clone(resources),
		Config:           config,
		Devices:          devices,
		PostStartDevices: postStartDevices,
		Secrets:          instanceSecrets,
		Files:            files,
		Dependencies:     deps,
	}
	if image != nil {
		instanceConfig.Image = image.IncusName()
	}
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

// volumeXIncusExtensions extracts the x-incus extension map from a compose volume
// definition and returns it as a flat map[string]string for use as Incus volume
// config. Keys and values are taken verbatim from the x-incus YAML block.
func volumeXIncusExtensions(volDef types.VolumeConfig) map[string]string {
	var raw map[string]any
	ok, err := volDef.Extensions.Get("x-incus", &raw)
	if !ok || err != nil || len(raw) == 0 {
		return nil
	}

	result := make(map[string]string, len(raw))
	for k, v := range raw {
		result[k] = fmt.Sprint(v)
	}

	return result
}

// volumeXIncusComposePool extracts the pool name from x-incus-compose.pool on a
// compose volume definition.
func volumeXIncusComposePool(volDef types.VolumeConfig) string {
	var raw map[string]any
	ok, err := volDef.Extensions.Get("x-incus-compose", &raw)
	if !ok || err != nil {
		return ""
	}
	pool, ok := raw["pool"].(string)
	if !ok {
		return ""
	}
	return pool
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

	// Add edges for dependencies that are in scope.
	for s := range maps.Values(serviceConfigs) {
		for dep := range s.DependsOn {
			if _, ok := serviceConfigs[dep]; !ok {
				continue
			}
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
	OnlyServices   []string
	Reverse        bool
	Full           bool
	NoImages       bool
	StorageVolumes bool
	InstancesOnly  bool
	ImagesOnly     bool
	Deps           bool
	Scale          map[string]int // service name -> replica count override
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

// ToStackNoImages doesn't add images to the stack.
func ToStackNoImages() ToStackOption {
	return func(o *ToStackOptions) {
		o.NoImages = true
	}
}

// ToStackWithDeps expands the OnlyServices selection to include linked services:
// in start direction the services a selected one depends on, and in reverse
// (stop) direction the services that depend on a selected one. Without it the
// stack is limited to exactly the selected services.
func ToStackWithDeps() ToStackOption {
	return func(o *ToStackOptions) {
		o.Deps = true
	}
}

// ToStackStorageVolumes adds storage volumes to the stack.
func ToStackStorageVolumes() ToStackOption {
	return func(o *ToStackOptions) {
		o.StorageVolumes = true
	}
}

// ToStackInstancesOnly configures ToStack to only add instances to the stack.
func ToStackInstancesOnly() ToStackOption {
	return func(o *ToStackOptions) {
		o.InstancesOnly = true
	}
}

// ToStackImagesOnly configures ToStack to only add images to the the stack.
func ToStackImagesOnly() ToStackOption {
	return func(o *ToStackOptions) {
		o.ImagesOnly = true
	}
}

// ToStackScale sets replica count overrides for services.
func ToStackScale(scale map[string]int) ToStackOption {
	return func(o *ToStackOptions) {
		o.Scale = scale
	}
}

// HealthdNetworkConfig reads the top-level x-incus-compose.healthd-network extension.
// Returns an empty string when the key is absent.
func (p *Project) HealthdNetworkConfig() (string, error) {
	extensions := projectXIncusComposeExtensions(p)
	if extensions == nil {
		return "", nil
	}
	raw, ok := extensions["healthd-network"]
	if !ok {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("x-incus-compose.healthd-network must be a string")
	}
	return value, nil
}

// NetworkConfig reads the top-level x-incus-compose.network extension.
// Returns empty strings when the key is absent.
func (p *Project) NetworkConfig() (project, profile string, err error) {
	extensions := projectXIncusComposeExtensions(p)
	if extensions == nil {
		return "", "", nil
	}
	raw, ok := extensions["network"]
	if !ok {
		return "", "", nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("x-incus-compose.network must be a map")
	}
	proj, ok := m["project"].(string)
	if !ok {
		return "", "", fmt.Errorf("x-incus-compose.network.project must be a string")
	}
	prof, ok := m["profile"].(string)
	if !ok {
		return "", "", fmt.Errorf("x-incus-compose.network.profile must be a string")
	}
	return proj, prof, nil
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

	if len(options.OnlyServices) > 0 {
		services := types.Services{}
		for n, svc := range p.Services {
			for _, on := range options.OnlyServices {
				if strings.HasPrefix(on+"-", n+"-") {
					services[n] = svc
					if !options.Deps {
						continue
					}
					if options.Reverse {
						// stop direction: include services that depend on this one
						for otherName, otherSvc := range p.Services {
							if _, ok := otherSvc.DependsOn[n]; ok {
								services[otherName] = otherSvc
							}
						}
					} else {
						// start direction: include services this one depends on
						for depName := range svc.DependsOn {
							services[depName] = p.Services[depName]
						}
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
			instanceResources, err := serviceToInstance(c, p.Project, serviceName, options, i, scale)
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

	stack.Sort(options.Reverse)

	if options.InstancesOnly {
		instances, err := client.ByKind[*client.Instance](resources, client.KindInstance)
		if err != nil {
			return err
		}
		for _, i := range instances {
			stack.Add(i)
		}
	} else if options.ImagesOnly {
		images, err := client.ByKind[*client.Image](resources, client.KindImage)
		if err != nil {
			return err
		}
		for _, i := range images {
			stack.Add(i)
		}
	} else {
		stack.Add(resources...)
	}

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
