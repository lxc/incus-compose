package incuscompose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	cCli "github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/errdefs"
	"github.com/compose-spec/compose-go/v2/template"
	cTypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/compose-spec/compose-go/v2/utils"
	"github.com/kr/pretty"
	"go.yaml.in/yaml/v4"
)

// ErrNoServer is returned when we have no incus server.
var ErrNoServer = fmt.Errorf("detected a Project without a incus Server")

// ErrNotLinux is returned when attempting to access the "local" remote on non-Linux systems.
var ErrNotLinux = fmt.Errorf("can't connect to a local server on a non-Linux system")

// Verbosity defines the logging verbosity level.
type Verbosity = int

// Verbosity levels for logging output.
const (
	VerbosityInfo  Verbosity = 0
	VerbosityDebug Verbosity = 1
	VerbosityTrace Verbosity = 2
)

// Project is for now an alias to compose-go.Project.
type Project = cTypes.Project

// LoadProjectOptions holds configuration for LoadProject.
type LoadProjectOptions struct {
	Options

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

	// OsEnv includes OS environment variables for interpolation (default: false)
	OsEnv bool
}

func (o *LoadProjectOptions) options() *Options {
	return &o.Options
}

// LoadProjectOption is a functional option for LoadProject.
type LoadProjectOption func(*LoadProjectOptions)

// LoadProjectName sets the project name.
func LoadProjectName(name string) LoadProjectOption {
	return func(o *LoadProjectOptions) {
		o.Name = name
	}
}

// LoadProjectFiles sets the compose configuration file paths.
func LoadProjectFiles(files []string) LoadProjectOption {
	return func(o *LoadProjectOptions) {
		o.Files = files
	}
}

// LoadProjectWorkingDir sets the working directory.
func LoadProjectWorkingDir(dir string) LoadProjectOption {
	return func(o *LoadProjectOptions) {
		o.WorkingDir = dir
	}
}

// LoadProjectEnvFiles sets alternative environment files.
func LoadProjectEnvFiles(files []string) LoadProjectOption {
	return func(o *LoadProjectOptions) {
		o.EnvFiles = files
	}
}

// LoadProjectProfiles sets the profiles to enable.
func LoadProjectProfiles(profiles []string) LoadProjectOption {
	return func(o *LoadProjectOptions) {
		o.Profiles = profiles
	}
}

// LoadProjectOsEnv enables OS environment variables for interpolation.
func LoadProjectOsEnv() LoadProjectOption {
	return func(o *LoadProjectOptions) {
		o.OsEnv = true
	}
}

// NewLoadProjectOptions creates LoadProjectOptions with the given options applied.
func NewLoadProjectOptions(opts ...LoadProjectOption) LoadProjectOptions {
	res := LoadProjectOptions{
		Options: Options{
			Verbosity: DefaultVerbosity,
		},
		Files: []string{},
	}

	for _, o := range opts {
		o(&res)
	}

	return res
}

// LoadProject loads a project using compose-spec/compose-go.
func LoadProject(ctx context.Context, opts ...LoadProjectOption) (*Project, error) {
	options := NewLoadProjectOptions(opts...)

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

// LoadModel loads the compose file and returns the raw model (map[string]any) without interpolation.
// This is useful for extracting variables before they are resolved.
func LoadModel(ctx context.Context, opts ...LoadProjectOption) (map[string]any, error) {
	options := NewLoadProjectOptions(opts...)

	cliOptions, err := buildProjectOptions(options, cCli.WithInterpolation(false))
	if err != nil {
		return nil, err
	}

	model, err := cliOptions.LoadModel(ctx)
	if errors.Is(err, errdefs.ErrNotFound) {
		return nil, fmt.Errorf("No compose.yaml found, either change to a directory with a `compose.yaml` or use `--file`")
	}

	return model, err
}

// buildProjectOptions creates the cli.ProjectOptions from LoadProjectOptions.
func buildProjectOptions(options LoadProjectOptions, extraOpts ...cCli.ProjectOptionsFn) (*cCli.ProjectOptions, error) {
	projectOptions := []cCli.ProjectOptionsFn{}

	if options.WorkingDir != "" {
		projectOptions = append(projectOptions, cCli.WithWorkingDirectory(options.WorkingDir))
	}

	// Optionally include OS environment variables directly (full docker-compose compatibility)
	if options.OsEnv {
		projectOptions = append(projectOptions, cCli.WithOsEnv)
	}

	// Apply env files and .env (with OS env available for interpolation)
	projectOptions = append(projectOptions,
		cCli.WithEnvFiles(options.EnvFiles...),
		withDotEnvAndOsEnv,
		cCli.WithConfigFileEnv,
		cCli.WithDefaultConfigPath,
		// Apply env files and .env again after working dir is determined
		cCli.WithEnvFiles(options.EnvFiles...),
		withDotEnvAndOsEnv,
	)

	if options.Name != "" {
		projectOptions = append(projectOptions, cCli.WithName(options.Name))
	}

	if len(options.Profiles) > 0 {
		projectOptions = append(projectOptions, cCli.WithProfiles(options.Profiles))
	}

	// Add any extra options (e.g., WithoutEnvironmentResolution)
	projectOptions = append(projectOptions, extraOpts...)

	return cCli.NewProjectOptions(
		options.Files,
		projectOptions...,
	)
}

// getOsEnv returns a map of OS environment variables.
func getOsEnv() map[string]string {
	return utils.GetAsEqualsMap(os.Environ())
}

// withDotEnvAndOsEnv imports environment variables from .env file,
// using OS environment for interpolation but not adding OS env to the project.
func withDotEnvAndOsEnv(o *cCli.ProjectOptions) error {
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

// DefaultVerbosity is the default verbosity level for operations.
var DefaultVerbosity = VerbosityInfo

// Options holds common configuration options.
type Options struct {
	Verbosity Verbosity
}

// IsDebug returns if debug is enabled.
func (o *Options) IsDebug() bool {
	return o.Verbosity >= VerbosityDebug
}

// IsTrace returns if trace is enabled.
func (o *Options) IsTrace() bool {
	return o.Verbosity >= VerbosityTrace
}

func (o *Options) options() *Options {
	return o
}

// OptionsType is an interface for types that embed Options.
type OptionsType interface {
	options() *Options
}

// Makes sure it implements `OptionsType`.
var _ (OptionsType) = (*Options)(nil)

// Option is a generic functional option type.
type Option func(OptionsType)

// DefaultOptions provides default values for Options.
var DefaultOptions = Options{
	Verbosity: DefaultVerbosity,
}

// WithVerbosity sets the verbosity level.
func WithVerbosity(v Verbosity) Option {
	return func(o OptionsType) {
		opts := o.options()
		opts.Verbosity = v
	}
}

// RunOptions holds configuration for the run command.
type RunOptions struct {
	Options

	Cmd     string
	CmdArgs []string

	// Build images before starting containers.
	Build bool
}

// RunCmd sets the command and arguments to run.
func RunCmd(cmd string, cmdArgs []string) Option {
	return func(o OptionsType) {
		opts, ok := o.(*RunOptions)
		if !ok {
			return
		}
		opts.Cmd = cmd
		opts.CmdArgs = cmdArgs
	}
}

// RunBuild enables building images before starting containers.
func RunBuild() Option {
	return func(o OptionsType) {
		opts, ok := o.(*RunOptions)
		if !ok {
			return
		}
		opts.Build = true
	}
}

// NewRunOptions creates RunOptions with the given options applied.
func NewRunOptions(opts ...Option) RunOptions {
	res := RunOptions{
		Options: Options{
			Verbosity: DefaultVerbosity,
		},

		CmdArgs: []string{},
	}

	for _, o := range opts {
		o(&res)
	}

	return res
}

// Run executes a one-off command on a service.
func Run(ctx context.Context, project *Project, service string, opts ...Option) error {
	options := NewRunOptions(opts...)

	_, _ = pretty.Print(options.Verbosity)

	return nil
}

// ConfigOptions holds configuration for the config command.
type ConfigOptions struct {
	Options

	// Output format (yaml or json)
	Format string

	// Output file path (empty means stdout)
	Output string

	// Filter flags
	ServicesOnly    bool
	VolumesOnly     bool
	NetworksOnly    bool
	ProfilesOnly    bool
	ImagesOnly      bool
	EnvironmentOnly bool
	VariablesOnly   bool
	Quiet           bool

	// Filter by specific services
	Services []string

	// LoadOptions for reloading model (needed for --variables)
	LoadOptions []LoadProjectOption
}

func (o *ConfigOptions) options() *Options {
	return &o.Options
}

// Makes sure it implements `OptionsType`.
var _ (OptionsType) = (*ConfigOptions)(nil)

// ConfigOption is a functional option for Config.
type ConfigOption func(*ConfigOptions)

// ConfigFormat sets the output format (yaml or json).
func ConfigFormat(format string) ConfigOption {
	return func(o *ConfigOptions) {
		o.Format = format
	}
}

// ConfigOutput sets the output file path.
func ConfigOutput(output string) ConfigOption {
	return func(o *ConfigOptions) {
		o.Output = output
	}
}

// ConfigServicesOnly outputs only service names.
func ConfigServicesOnly() ConfigOption {
	return func(o *ConfigOptions) {
		o.ServicesOnly = true
	}
}

// ConfigVolumesOnly outputs only volume names.
func ConfigVolumesOnly() ConfigOption {
	return func(o *ConfigOptions) {
		o.VolumesOnly = true
	}
}

// ConfigNetworksOnly outputs only network names.
func ConfigNetworksOnly() ConfigOption {
	return func(o *ConfigOptions) {
		o.NetworksOnly = true
	}
}

// ConfigProfilesOnly outputs only profile names.
func ConfigProfilesOnly() ConfigOption {
	return func(o *ConfigOptions) {
		o.ProfilesOnly = true
	}
}

// ConfigImagesOnly outputs only image names.
func ConfigImagesOnly() ConfigOption {
	return func(o *ConfigOptions) {
		o.ImagesOnly = true
	}
}

// ConfigEnvironmentOnly outputs only environment variables used for interpolation.
func ConfigEnvironmentOnly() ConfigOption {
	return func(o *ConfigOptions) {
		o.EnvironmentOnly = true
	}
}

// ConfigVariablesOnly outputs only model variables and default values.
func ConfigVariablesOnly() ConfigOption {
	return func(o *ConfigOptions) {
		o.VariablesOnly = true
	}
}

// ConfigLoadOptions sets the load options for reloading the model.
func ConfigLoadOptions(opts ...LoadProjectOption) ConfigOption {
	return func(o *ConfigOptions) {
		o.LoadOptions = opts
	}
}

// ConfigQuiet enables quiet mode (validate only, no output).
func ConfigQuiet() ConfigOption {
	return func(o *ConfigOptions) {
		o.Quiet = true
	}
}

// ConfigServices filters output to specific services.
func ConfigServices(services []string) ConfigOption {
	return func(o *ConfigOptions) {
		o.Services = services
	}
}

// NewConfigOptions creates ConfigOptions with the given options applied.
func NewConfigOptions(opts ...ConfigOption) ConfigOptions {
	res := ConfigOptions{
		Options: Options{
			Verbosity: DefaultVerbosity,
		},
		Format:   "yaml",
		Services: []string{},
	}

	for _, o := range opts {
		o(&res)
	}

	return res
}

// Config validates and outputs the compose configuration.
func Config(ctx context.Context, project *Project, opts ...ConfigOption) error {
	options := NewConfigOptions(opts...)

	// If quiet, just validate and return
	if options.Quiet {
		return nil
	}

	// Determine output writer
	var writer io.Writer = os.Stdout
	if options.Output != "" {
		f, err := os.Create(options.Output)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}
		defer f.Close()
		writer = f
	}

	// Handle filter-only options
	if options.ServicesOnly {
		names := make([]string, 0, len(project.Services))
		for name := range project.Services {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			_, _ = fmt.Fprintln(writer, name)
		}
		return nil
	}

	if options.VolumesOnly {
		names := make([]string, 0, len(project.Volumes))
		for name := range project.Volumes {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			_, _ = fmt.Fprintln(writer, name)
		}
		return nil
	}

	if options.NetworksOnly {
		names := make([]string, 0, len(project.Networks))
		for name := range project.Networks {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			_, _ = fmt.Fprintln(writer, name)
		}
		return nil
	}

	if options.ProfilesOnly {
		profiles := make([]string, len(project.Profiles))
		copy(profiles, project.Profiles)
		sort.Strings(profiles)
		for _, profile := range profiles {
			_, _ = fmt.Fprintln(writer, profile)
		}
		return nil
	}

	if options.ImagesOnly {
		// Print images in service order (not sorted, matching docker-compose behavior)
		for _, svc := range project.Services {
			if svc.Image != "" {
				_, _ = fmt.Fprintln(writer, svc.Image)
			}
		}
		return nil
	}

	if options.EnvironmentOnly {
		// Get environment variable names and sort them
		names := make([]string, 0, len(project.Environment))
		for name := range project.Environment {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			_, _ = fmt.Fprintf(writer, "%s=%s\n", name, project.Environment[name])
		}
		return nil
	}

	if options.VariablesOnly {
		// Load the raw model without interpolation to extract variables
		model, err := LoadModel(ctx, options.LoadOptions...)
		if err != nil {
			return fmt.Errorf("failed to load model for variables: %w", err)
		}

		variables := template.ExtractVariables(model, template.DefaultPattern)

		// Print header
		_, _ = fmt.Fprintf(writer, "%-23s %-19s %-19s %s\n", "NAME", "REQUIRED", "DEFAULT VALUE", "ALTERNATE VALUE")

		// Collect and sort variable names
		names := make([]string, 0, len(variables))
		for name := range variables {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			v := variables[name]
			required := "false"
			if v.Required {
				required = "true"
			}
			_, _ = fmt.Fprintf(writer, "%-23s %-19s %-19s %s\n", name, required, v.DefaultValue, v.PresenceValue)
		}
		return nil
	}

	// Filter project by specific services if requested
	filteredProject := project
	if len(options.Services) > 0 {
		filteredProject = &Project{
			Name:         project.Name,
			WorkingDir:   project.WorkingDir,
			Services:     make(cTypes.Services),
			Networks:     project.Networks,
			Volumes:      project.Volumes,
			Secrets:      project.Secrets,
			Configs:      project.Configs,
			ComposeFiles: project.ComposeFiles,
			Environment:  project.Environment,
		}

		for _, serviceName := range options.Services {
			if svc, ok := project.Services[serviceName]; ok {
				filteredProject.Services[serviceName] = svc
			}
		}
	}

	// Output full config in requested format
	switch options.Format {
	case "json":
		// Use a buffer to capture JSON output and remove trailing newline
		var buf bytes.Buffer
		encoder := json.NewEncoder(&buf)
		encoder.SetIndent("", "  ")
		err := encoder.Encode(filteredProject)
		if err != nil {
			return err
		}

		// Remove trailing newline to match docker-compose behavior
		jsonBytes := buf.Bytes()
		if len(jsonBytes) > 0 && jsonBytes[len(jsonBytes)-1] == '\n' {
			jsonBytes = jsonBytes[:len(jsonBytes)-1]
		}

		_, err = writer.Write(jsonBytes)
		return err
	case "yaml":
		// Use a buffer to capture YAML output and remove trailing newline
		var buf bytes.Buffer
		encoder := yaml.NewEncoder(&buf)
		encoder.SetIndent(2)
		err := encoder.Encode(filteredProject)
		if err := encoder.Close(); err != nil {
			return err
		}
		if err != nil {
			return err
		}

		// Remove trailing newline to match docker-compose behavior
		yamlBytes := buf.Bytes()
		if len(yamlBytes) > 0 && yamlBytes[len(yamlBytes)-1] == '\n' {
			yamlBytes = yamlBytes[:len(yamlBytes)-1]
		}

		_, err = writer.Write(yamlBytes)
		return err
	default:
		return fmt.Errorf("unsupported format: %s", options.Format)
	}
}
