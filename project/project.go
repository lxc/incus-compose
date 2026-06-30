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
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"

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

// Project wraps a Docker Compose project with Incus client integration.
type Project struct {
	*types.Project

	// xic caches the decoded top-level x-incus-compose extension. It is filled
	// lazily on first access. The config phase is single-threaded, so no
	// synchronization is needed.
	xic *xICProject
}

// xICProject is the typed view of the top-level x-incus-compose extension.
type xICProject struct {
	Healthd struct {
		Incus   string `mapstructure:"incus"`
		Network string `mapstructure:"network"`
	} `mapstructure:"healthd"`
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

	if p.Extensions != nil {
		var ext xICProject
		ok, err := p.Extensions.Get("x-incus-compose", &ext)
		if err != nil {
			return nil, err
		}
		if ok {
			p.xic = &ext
		}
	}

	return p, nil
}

// InstanceNames returns the Incus instance names for all services.
func (p *Project) InstanceNames() []string {
	var names []string
	for _, svcName := range p.ServiceNames() {
		service, err := p.GetService(svcName)
		if err != nil {
			continue
		}

		replicas := 1
		if service.Deploy != nil && service.Deploy.Replicas != nil {
			replicas = int(*service.Deploy.Replicas)
		}

		for i := 1; i <= replicas; i++ {
			names = append(names, instanceName(service, i, replicas))
		}
	}

	return names
}

// HealthdConfig reads the top-level x-incus-compose.healthd extension.
// Returns empty strings when the key is absent.
func (p *Project) HealthdConfig() (incusURL, network string) {
	if p.xic == nil {
		return "", ""
	}

	return p.xic.Healthd.Incus, p.xic.Healthd.Network
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

// ResourcesOptions configures how services are converted to stack operations.
type ResourcesOptions struct {
	Reverse bool
	Full    bool
	Scale   map[string]int // service name -> replica count override
}

// ResourcesOption is a functional option for ToStack.
type ResourcesOption func(o *ResourcesOptions)

// ResourcesReverse reverses the service dependency graph order.
// Use for teardown so dependants are stopped before their dependencies.
// Note: cross-kind priority ordering (e.g. instances vs networks) is handled
// automatically by Stack.ForAction and does not require this option.
func ResourcesReverse() ResourcesOption {
	return func(o *ResourcesOptions) {
		o.Reverse = true
	}
}

// ResourcesFull fetches complete instance state including image alias and full instance details.
func ResourcesFull() ResourcesOption {
	return func(o *ResourcesOptions) {
		o.Full = true
	}
}

// ResourcesScale sets replica count overrides for services.
func ResourcesScale(scale map[string]int) ResourcesOption {
	return func(o *ResourcesOptions) {
		o.Scale = scale
	}
}

// Resources converts the compose project services to client resources.
func (p *Project) Resources(c *client.Client, opts ...ResourcesOption) (map[string][]client.Resource, error) {
	options := &ResourcesOptions{}
	for _, o := range opts {
		o(options)
	}

	result := map[string][]client.Resource{}

	var errs error

	serviceOrder, err := ServiceGraph(p.Services, options.Reverse)
	if err != nil {
		return nil, err
	}

	// Configure instances
	for _, serviceName := range serviceOrder {
		service, ok := p.Services[serviceName]
		if !ok {
			continue
		}

		// Determine the desired count: CLI --scale > deploy.replicas > 1.
		// A plain `up` reconciles to deploy.replicas in both directions, matching
		// `docker compose up`: a manual --scale applies only to that invocation,
		// and the next plain `up` restores replicas (scaling up or down).
		desired := 1
		if s, ok := options.Scale[service.Name]; ok {
			desired = s
		} else if service.Deploy != nil && service.Deploy.Replicas != nil {
			desired = int(*service.Deploy.Replicas)
		}

		// Discover existing instances above the desired count so they can be
		// reconciled away (highest index first) during Ensure.
		scale := desired
		for {
			instanceName := fmt.Sprintf("%s-%d", service.Name, scale+1)
			if ok, err := c.InstanceExists(instanceName); !ok || err != nil {
				break
			}

			scale = scale + 1
		}

		instances := []*client.Instance{}
		for i := 1; i <= scale; i++ {
			instance, instanceResources, err := serviceToInstance(c, p.Project, service.Name, options, i, scale)
			if err != nil {
				errs = errors.Join(errs, err)
				continue
			}

			result[service.Name] = append(result[service.Name], instance)
			result[service.Name] = append(result[service.Name], instanceResources...)

			instances = append(instances, instance)
		}

		// Reconcile down: instances beyond the desired count are marked for
		// deletion (highest index first) and torn down during Ensure.
		if len(instances) > desired {
			slices.Reverse(instances)

			for idx := range len(instances) - desired {
				instances[idx].MarkDelete()
			}
		}
	}

	if errs != nil {
		return nil, errs
	}

	return result, nil
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
