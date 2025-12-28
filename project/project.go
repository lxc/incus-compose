// Package project provides docker-compose project loading and service-to-instance translation.
package project

import (
	"context"
	"errors"
	"fmt"
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

	"gitlab.com/r3j0/incuscompose/client"
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

// Load loads a compose project with full interpolation and validation.
func Load(ctx context.Context, opts ...LoadOption) (*types.Project, error) {
	options := NewLoadOptions(opts...)

	cliOptions, err := buildProjectOptions(options)
	if err != nil {
		return nil, err
	}

	cp, err := cliOptions.LoadProject(ctx)
	if errors.Is(err, errdefs.ErrNotFound) {
		return nil, fmt.Errorf("No compose.yaml found, either change to a directory with a `compose.yaml` or use `--file`")
	}

	return cp, err
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
		return nil, fmt.Errorf("No compose.yaml found, either change to a directory with a `compose.yaml` or use `--file`")
	}

	return model, err
}

// ServiceToInstance translates a compose service to an Incus instance.
// Environment vars become instance config, labels become user metadata.
// Volumes default to bind mounts for paths starting with / or ., otherwise named volumes.
func ServiceToInstance(c *client.ClientProject, service types.ServiceConfig, image *client.Image) (*client.Instance, error) {
	config := make(map[string]string, len(service.Environment)+len(service.Labels))

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

	// Network devices
	networks := make(map[string]*client.Network, len(service.Networks))
	ethIdx := 0
	for netName := range service.Networks {
		net, err := c.Network(netName)
		if err != nil {
			return nil, err
		}
		devName := fmt.Sprintf("eth%d", ethIdx)
		networks[devName] = net

		ethIdx++
	}

	ports := make([]client.InstancePortProxy, len(service.Ports))
	for _, port := range service.Ports {
		lPort, err := strconv.ParseUint(port.Published, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("bad publishing port %q must be a number: %w", port.Published, err)
		}

		ports = append(ports, client.InstancePortProxy{
			Protocol: port.Protocol,
			HostIP:   port.HostIP,
			Listen:   uint32(lPort),
			Connect:  port.Target,
		})
	}

	pVolumes := []client.InstancePoolVolume{}
	bindMounts := []client.InstanceBindMount{}

	for _, vol := range service.Volumes {
		if vol.Type == "" {
			// Infer type from source path (short syntax compatibility)
			// Absolute or relative paths are bind mounts, named sources are volumes
			if vol.Source != "" && (strings.HasPrefix(vol.Source, "/") || strings.HasPrefix(vol.Source, ".")) {
				vol.Type = "bind"
			} else if vol.Source != "" {
				vol.Type = "volume"
			}
		}

		switch vol.Type {
		case "volume":
			volume, err := c.PoolVolume(vol.Source, client.PoolVolumeConfig{})
			if err != nil {
				return nil, err
			}

			pVol := client.InstancePoolVolume{}
			pVol.Path = vol.Target
			pVol.Volume = volume

			if vol.ReadOnly {
				pVol.ReadOnly = true
			}

			pVolumes = append(pVolumes, pVol)
		case "bind":
			bMount := client.InstanceBindMount{}
			bMount.Source = vol.Source
			bMount.Path = vol.Target

			if vol.ReadOnly {
				bMount.ReadOnly = true
			}

			bindMounts = append(bindMounts, bMount)
		case "tmpfs":
			// tmpfs not yet implemented - Incus has native tmpfs device support
			c.Logger().WarnContext(c.Ctx, "tmpfs volumes not yet supported", "target", vol.Target)
		default:
			return nil, fmt.Errorf("Unknown volume type %q for service %q", vol.Type, service.Name)
		}
	}

	return c.Instance(service.Name, client.InstanceConfig{
		Image:       image,
		Networks:    networks,
		PortProxies: ports,
		PoolVolumes: pVolumes,
		BindMounts:  bindMounts,
		Config:      config,
	})
}

// ServiceGraph returns services in dependency order using topological sort.
// If reverse is true, returns reverse order (useful for shutdown).
func ServiceGraph(serviceConfigs types.Services, reverse bool) ([]string, error) {
	g := graph.New(graph.StringHash, graph.Directed(), graph.PreventCycles())

	// Add vertices
	for n := range serviceConfigs {
		_ = g.AddVertex(n)
	}

	// Add edges for dependencies
	for n, s := range serviceConfigs {
		for dep := range s.DependsOn {
			// Edge from dependency to dependent (dep must start before n)
			err := g.AddEdge(dep, n)
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

// Images maps image references to client Image resources.
type Images = map[string]*client.Image

// ToInstances converts compose services to Incus instances.
// Creates networks and translates service configs to instance configs.
func ToInstances(clientProject *client.ClientProject, images Images, project *types.Project, services []string) (map[string]*client.Instance, error) {
	_, err := clientProject.Profile("default", client.ProfileConfig{})
	if err != nil {
		return nil, err
	}

	// Configure Networks
	iNetworks := make(map[string]*client.Network, len(project.Networks))
	for networkName := range project.Networks {
		net, err := clientProject.Network(networkName)
		if err != nil {
			return nil, err
		}

		iNetworks[networkName] = net
	}

	instances := make(map[string]*client.Instance, len(services))

	// Configure instances
	for _, serviceName := range services {
		service := project.Services[serviceName]

		image, ok := images[service.Image]
		if ok {
			instance, err := ServiceToInstance(clientProject, service, image)
			if err != nil {
				return nil, err
			}

			instances[serviceName] = instance
		} else {
			instance, err := clientProject.Instance(serviceName, client.InstanceConfig{})
			if err != nil {
				return nil, err
			}

			instances[serviceName] = instance
		}
	}

	return instances, nil
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
	lookupEnv := make(map[string]string)
	for k, v := range osEnv {
		lookupEnv[k] = v
	}
	for k, v := range o.Environment {
		lookupEnv[k] = v // Project env overrides OS env
	}

	// Parse .env files using combined env for interpolation
	envMap, err := dotenv.GetEnvFromFile(lookupEnv, o.EnvFiles)
	if err != nil {
		return err
	}

	// Only merge the .env results (not OS env) into project environment
	o.Environment.Merge(envMap)
	return nil
}
