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
		Files: []string{},
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

// ServiceToInstance translates a compose service to an Incus instance.
// Environment vars become instance config, labels become user metadata.
// Volumes default to bind mounts for paths starting with / or ., otherwise named volumes.
// The index parameter is used for instance naming ({service}-{index}).
func ServiceToInstance(c *client.Client, p *types.Project, serviceName string, full bool, index int) ([]client.Resource, error) {
	service, ok := p.Services[serviceName]
	if !ok {
		return nil, fmt.Errorf("service %q not found", serviceName)
	}

	var errs error

	config := make(map[string]string, len(service.Environment)+len(service.Labels))

	resources := []client.Resource{}
	devices := []client.InstanceDevice{}
	postDevices := []client.InstanceDevice{}

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
	ethIdx := 0
	for name := range maps.Keys(service.Networks) {
		netConfig := &client.NetworkConfig{}
		if networkDef, ok := p.Networks[name]; ok {
			netConfig.External = bool(networkDef.External)
		}

		network, err := c.Resource(client.KindNetwork, name, netConfig)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}

		devices = append(devices, client.InstanceDevice{
			Name: fmt.Sprintf("eth%d", ethIdx),
			Config: client.InstanceDeviceConfig{
				DeviceType: client.InstanceDeviceTypeNic,
				Network:    network,
			},
		})
		ethIdx++

		resources = append(resources, network)
	}

	for _, port := range service.Ports {
		lPort, err := strconv.ParseUint(port.Published, 10, 32)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("bad publishing port %q must be a number: %w", port.Published, err))
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

		devName := fmt.Sprintf("proxy-%d", lPort)
		devConfig := client.InstanceDeviceConfig{
			DeviceType: client.InstanceDeviceTypeProxy,
			Proxy: client.InstanceDeviceProxyConfig{
				ListenType:  proto,
				ListenAddr:  listenIP,
				ListenPort:  uint32(lPort),
				ConnectType: proto,
				ConnectAddr: "127.0.0.1",
				ConnectPort: port.Target,
			},
		}

		devices = append(devices, client.InstanceDevice{Name: devName, Config: devConfig})
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

	// Instance name follows Docker Compose convention: {service}-{index}
	instanceName := fmt.Sprintf("%s-%d", service.Name, index)
	instanceConfig := &client.InstanceConfig{Full: full, Resources: slices.Clone(resources), Image: image.Name(), Config: config, Devices: devices, PostDevices: postDevices, Secrets: instanceSecrets}
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

	if len(options.OnlyServices) >= 1 {
		services := types.Services{}
		for n, svc := range p.Services {
			for _, on := range options.OnlyServices {
				if n == on {
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

		for i := 1; i <= scale; i++ {
			instanceResources, err := ServiceToInstance(c, p.Project, serviceName, options.Full, i)
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
