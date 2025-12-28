// Package main provides the incus-compose CLI.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"sort"
	"text/tabwriter"

	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/lmittmann/tint"
	"github.com/lxc/incus/v6/shared/cliconfig"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"
	"go.yaml.in/yaml/v4"

	"gitlab.com/r3j0/incuscompose/client"
	"gitlab.com/r3j0/incuscompose/project"
)

// buildLoadOptions converts CLI flags to project.LoadOption slice.
func buildLoadOptions(cmd *cli.Command) []project.LoadOption {
	loadOpts := []project.LoadOption{}

	if name := cmd.String("project-name"); name != "" {
		loadOpts = append(loadOpts, project.LoadName(name))
	}

	if files := cmd.StringSlice("file"); len(files) > 0 {
		loadOpts = append(loadOpts, project.LoadFiles(files))
	}

	if dir := cmd.String("project-directory"); dir != "" {
		loadOpts = append(loadOpts, project.LoadWorkingDir(dir))
	}

	if envFiles := cmd.StringSlice("env-file"); len(envFiles) > 0 {
		loadOpts = append(loadOpts, project.LoadEnvFiles(envFiles))
	}

	if profiles := cmd.StringSlice("profile"); len(profiles) > 0 {
		loadOpts = append(loadOpts, project.LoadProfiles(profiles))
	}

	if cmd.Bool("os-env") {
		loadOpts = append(loadOpts, project.LoadOsEnv())
	}

	return loadOpts
}

// loadIncusConfig loads the Incus CLI configuration.
func loadIncusConfig() (*cliconfig.Config, error) {
	conf, err := cliconfig.LoadConfig("")
	if err != nil {
		return nil, fmt.Errorf("loading Incus config: %w", err)
	}
	return conf, nil
}

var upCommand = &cli.Command{
	Name:      "up",
	Usage:     "Create and start containers",
	Category:  "compose",
	ArgsUsage: "[SERVICE...]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "recreate",
			Usage: "Recreate containers even if they exist",
		},
		&cli.BoolFlag{
			Name:  "no-start",
			Usage: "Don't start containers after creating",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		reCreate := cmd.Bool("recreate")
		start := !cmd.Bool("no-start")

		p, err := project.Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			return err
		}

		services := types.Services{}
		if len(cmd.Args().Slice()) > 0 {
			for _, n := range cmd.Args().Slice() {
				s := p.Services[n]
				services[n] = s
				for depName := range s.DependsOn {
					services[depName] = p.Services[depName]
				}
			}
		} else {
			services = p.AllServices()
		}

		order, err := project.ServiceGraph(services, false)
		if err != nil {
			return err
		}

		c, err := client.FromContext(ctx)
		if err != nil {
			return err
		}

		clientProject, err := c.EnsureProject(p.Name, true)
		if err != nil {
			return err
		}

		defer func() {
			if c.Errors() != nil {
				c.Logger().ErrorContext(c.Ctx, "Error(s) during up", "error", c.Errors())
				if c.IsDebugging() {
					c.Logger().WarnContext(c.Ctx, "Wont rollback in debug")
				} else {
					err := c.Rollback(0)
					if err != nil {
						c.Logger().ErrorContext(c.Ctx, "During rollback", "error", err)
					}
				}
			}
		}()

		images := map[string]*client.Image{}
		for _, service := range services {
			image, err := clientProject.Image(service.Image, client.ImageConfig{})
			if err != nil {
				return err
			}

			// Get image server for the remote
			conf, err := loadIncusConfig()
			if err != nil {
				return err
			}

			imageServer, err := conf.GetImageServer(image.Operation.Remote)
			if err != nil {
				return err
			}

			image.SetSourceServer(imageServer)

			if err := image.Ensure(true); err != nil {
				return err
			}

			images[service.Image] = image
		}

		instances, err := project.ToInstances(clientProject, images, p, order)
		if err != nil {
			return err
		}

		// Create profiles first.
		for _, profile := range clientProject.Profiles.All() {
			if err := profile.Ensure(true); err != nil {
				return err
			}
		}

		// Create and start in order
		for _, name := range order {
			instance := instances[name]

			if reCreate {
				if err := instance.Get(); err == nil {
					instance.Delete(0, true)
				}
			}

			err := instance.Ensure(true)
			if err != nil {
				clientProject.Logger().ErrorContext(clientProject.Ctx, "Service failed to create", "error", err)
				break
			}

			if start {
				if err := instance.Start(30); err != nil {
					clientProject.Logger().WarnContext(clientProject.Ctx, "Service failed to start", "error", err)
				}
			}
		}

		return nil
	},
}

var downCommand = &cli.Command{
	Name:      "down",
	Usage:     "Stop and remove containers",
	Category:  "compose",
	ArgsUsage: "[SERVICE...]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "volumes",
			Usage: "Remove volumes",
		},
		&cli.BoolFlag{
			Name:  "networks",
			Usage: "Remove networks",
		},
		&cli.IntFlag{
			Name:  "timeout",
			Usage: "Timeout in seconds for stopping",
			Value: 10,
		},
		&cli.StringFlag{
			Name:  "remote",
			Usage: "Incus remote to use",
			Value: "local",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		volumes := cmd.Bool("volumes")
		networks := cmd.Bool("networks")
		timeout := cmd.Int("timeout")

		p, err := project.Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			return err
		}

		services := types.Services{}
		if len(cmd.Args().Slice()) > 0 {
			for _, n := range cmd.Args().Slice() {
				s := p.Services[n]
				services[n] = s
				for depName := range s.DependsOn {
					services[depName] = p.Services[depName]
				}
			}
		} else {
			services = p.AllServices()
		}

		order, err := project.ServiceGraph(services, true)
		if err != nil {
			return err
		}

		c, err := client.FromContext(ctx)
		if err != nil {
			return err
		}

		clientProject, err := c.EnsureProject(p.Name, true)
		if err != nil {
			return err
		}

		defer func() {
			if c.Errors() != nil {
				c.Logger().ErrorContext(c.Ctx, "Error(s) during up", "error", c.Errors())
				if c.IsDebugging() {
					c.Logger().WarnContext(c.Ctx, "Wont rollback in debug")
				} else {
					err := c.Rollback(0)
					if err != nil {
						c.Logger().ErrorContext(c.Ctx, "During rollback", "error", err)
					}
				}
			}
		}()

		instances, err := project.ToInstances(clientProject, project.Images{}, p, order)
		if err != nil {
			return err
		}

		var errs error

		// Stop and remove containers in reverse order
		for _, serviceName := range order {
			instance, ok := instances[serviceName]
			if !ok {
				continue
			}

			// Check if container exists
			err := instance.Get()
			if err != nil {
				// Container doesn't exist, skip
				clientProject.Logger().DebugContext(ctx, "Instance not found", "service", serviceName)
				continue
			}

			// Remove container
			clientProject.Logger().InfoContext(ctx, "Removing instance for service", "service", serviceName, "instance", instance.Name())
			if err := instance.Delete(timeout, true); err != nil {
				errs = errors.Join(errs, err)
			}
		}

		// Remove volumes if requested
		if volumes {
			for volName := range p.Volumes {
				clientProject.Logger().InfoContext(ctx, "Removing pool volume", "name", volName)
				r, err := clientProject.PoolVolume(volName, client.PoolVolumeConfig{})
				if err != nil {
					continue
				}

				if err := r.Get(); err != nil {
					continue
				}

				if err := r.Delete(timeout, true); err != nil {
					errs = errors.Join(errs, err)
				}
			}
		}

		// Remove networks if requested
		if networks {
			for netName := range p.Networks {
				clientProject.Logger().InfoContext(ctx, "Removing pool volume", "name", netName)
				r, err := clientProject.Network(netName)
				if err != nil {
					continue
				}

				if err := r.Get(); err != nil {
					continue
				}

				if err := r.Delete(timeout, true); err != nil {
					errs = errors.Join(errs, err)
				}
			}
		}

		return errs
	},
}

// ContainerStatus holds the status of a single instance for ps output.
type ContainerStatus struct {
	Service   string   `json:"service" yaml:"service"`
	Container string   `json:"container" yaml:"container"`
	Image     string   `json:"image" yaml:"image"`
	Status    string   `json:"status" yaml:"status"`
	Addresses []string `json:"addresses" yaml:"addresses"`
}

// ContainerStatuses collects and formats instance statuses.
type ContainerStatuses struct {
	Writer io.Writer

	Statuses []ContainerStatus
}

// NewContainerStatuses creates a new ContainerStatuses collector.
func NewContainerStatuses(w io.Writer) *ContainerStatuses {
	return &ContainerStatuses{Writer: w, Statuses: []ContainerStatus{}}
}

// Add appends a status to the collection.
func (c *ContainerStatuses) Add(status ContainerStatus) {
	c.Statuses = append(c.Statuses, status)
}

// Yaml outputs statuses as YAML.
func (c *ContainerStatuses) Yaml() error {
	return yaml.NewEncoder(c.Writer).Encode(c.Statuses)
}

// Json outputs statuses as JSON.
func (c *ContainerStatuses) Json() error {
	return json.NewEncoder(c.Writer).Encode(c.Statuses)
}

// Table outputs statuses as a formatted table.
func (c *ContainerStatuses) Table() error {
	tw := tabwriter.NewWriter(c.Writer, 0, 0, 2, ' ', 0)
	_, err := fmt.Fprintln(tw, "SERVICE\tCONTAINER\tIMAGE\tSTATUS\tADDRESSES")
	if err != nil {
		return err
	}

	var errs error
	for _, s := range c.Statuses {
		addrs := ""
		if len(s.Addresses) > 0 {
			addrs = s.Addresses[0]
			for _, a := range s.Addresses[1:] {
				addrs += ", " + a
			}
		}
		_, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			s.Service,
			s.Container,
			s.Image,
			s.Status,
			addrs,
		)

		errs = errors.Join(errs, err)
	}

	return errors.Join(errs, tw.Flush())
}

var psCommand = &cli.Command{
	Name:      "ps",
	Usage:     "List containers",
	Category:  "compose",
	ArgsUsage: "[SERVICE...]",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "format",
			Usage: "Format the output. Values: [table | yaml | json]",
			Value: "table",
			Action: func(ctx context.Context, cmd *cli.Command, v string) error {
				if !slices.Contains([]string{"table", "yaml", "json"}, v) {
					return fmt.Errorf("invalid format: %s (must be table, yaml or json)", v)
				}
				return nil
			},
		},
		&cli.BoolFlag{
			Name:    "all",
			Aliases: []string{"a"},
			Usage:   "Show all containers (including stopped)",
		},
		&cli.StringFlag{
			Name:  "remote",
			Usage: "Incus remote to use",
			Value: "local",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		p, err := project.Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			return err
		}

		services := types.Services{}
		if len(cmd.Args().Slice()) > 0 {
			for _, n := range cmd.Args().Slice() {
				s := p.Services[n]
				services[n] = s
				for depName := range s.DependsOn {
					services[depName] = p.Services[depName]
				}
			}
		} else {
			services = p.AllServices()
		}

		order, err := project.ServiceGraph(services, false)
		if err != nil {
			return err
		}

		c, err := client.FromContext(ctx)
		if err != nil {
			return err
		}

		clientProject, err := c.EnsureProject(p.Name, true)
		if err != nil {
			return err
		}

		clientProject.Logger().Debug("Services", "services", order)

		instances, err := project.ToInstances(clientProject, project.Images{}, p, order)
		if err != nil {
			return err
		}

		var rErrs error

		fd := os.Stdout
		if cmd.String("output") != "" {
			fd, err = os.OpenFile(cmd.String("output"), os.O_APPEND|os.O_CREATE, 0o644)
			if err != nil {
				return err
			}

			defer fd.Close()
		}

		statuses := NewContainerStatuses(fd)

		for _, serviceName := range order {
			service := services[serviceName]
			instance := instances[serviceName]

			status := ContainerStatus{
				Service:   service.Name,
				Container: instance.IncusName(),
				Image:     service.Image,
				Status:    "Not created",
				Addresses: []string{},
			}

			// Get container info
			inst, _, err := instance.Full()
			if err == nil {
				status.Status = inst.State.Status

				// Get IP addresses
				for _, network := range inst.State.Network {
					for _, addr := range network.Addresses {
						if addr.Family == "inet" && addr.Scope == "global" {
							status.Addresses = append(status.Addresses, addr.Address)
						}
					}
				}
			}

			// Skip stopped containers unless --all
			if !cmd.Bool("all") && status.Status != "Running" {
				clientProject.Logger().DebugContext(clientProject.Ctx, "Skipping", "status", status)
				continue
			}

			statuses.Add(status)
		}

		switch cmd.String("format") {
		case "table":
			rErrs = errors.Join(rErrs, statuses.Table())
		case "yaml":
			rErrs = errors.Join(rErrs, statuses.Yaml())
		case "json":
			rErrs = errors.Join(rErrs, statuses.Json())
		default:
			rErrs = errors.Join(rErrs, fmt.Errorf("Unknown format: %q", cmd.String("format")))
		}

		return rErrs
	},
}

var configCommand = &cli.Command{
	Name:      "config",
	Usage:     "Parse, resolve and render compose file in canonical format",
	Category:  "compose",
	ArgsUsage: "[SERVICE...]",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "format",
			Usage: "Format the output. Values: [yaml | json]",
			Value: "yaml",
			Action: func(ctx context.Context, cmd *cli.Command, v string) error {
				if v != "yaml" && v != "json" {
					return fmt.Errorf("invalid format: %s (must be yaml or json)", v)
				}
				return nil
			},
		},
		&cli.BoolFlag{
			Name:  "services",
			Usage: "Print the service names, one per line",
		},
		&cli.BoolFlag{
			Name:  "volumes",
			Usage: "Print the volume names, one per line",
		},
		&cli.BoolFlag{
			Name:  "networks",
			Usage: "Print the network names, one per line",
		},
		&cli.BoolFlag{
			Name:  "profiles",
			Usage: "Print the profile names, one per line",
		},
		&cli.BoolFlag{
			Name:    "quiet",
			Aliases: []string{"q"},
			Usage:   "Only validate the configuration, don't print anything",
		},
		&cli.BoolFlag{
			Name:  "images",
			Usage: "Print the image names, one per line",
		},
		&cli.BoolFlag{
			Name:  "environment",
			Usage: "Print environment used for interpolation",
		},
		&cli.BoolFlag{
			Name:  "variables",
			Usage: "Print model variables and default values",
		},
		&cli.StringFlag{
			Name:    "output",
			Aliases: []string{"o"},
			Usage:   "Save to file (default to stdout)",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		loadOpts := buildLoadOptions(cmd)
		p, err := project.Load(ctx, loadOpts...)
		if err != nil {
			slog.Error("While loading the 'compose.yaml'", "error", err)
			return err
		}

		services := types.Services{}
		if len(cmd.Args().Slice()) > 0 {
			for _, n := range cmd.Args().Slice() {
				s := p.Services[n]
				services[n] = s
				for depName := range s.DependsOn {
					services[depName] = p.Services[depName]
				}
			}
		} else {
			services = p.AllServices()
		}

		// If quiet, just validate and return
		if cmd.Bool("quiet") {
			return nil
		}

		// Determine output writer
		writer := os.Stdout
		if cmd.String("output") != "" {
			writer, err = os.OpenFile(cmd.String("output"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}

			defer writer.Close()
		}

		// Handle filter-only options
		if cmd.Bool("services") {
			names := make([]string, 0, len(services))
			for name := range p.Services {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				_, _ = fmt.Fprintln(writer, name)
			}
			return nil
		}

		if cmd.Bool("volumes") {
			names := make([]string, 0, len(p.Volumes))
			for name := range p.Volumes {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				_, _ = fmt.Fprintln(writer, name)
			}
			return nil
		}

		if cmd.Bool("networks") {
			names := make([]string, 0, len(p.Networks))
			for name := range p.Networks {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				_, _ = fmt.Fprintln(writer, name)
			}
			return nil
		}

		if cmd.Bool("profiles") {
			profiles := make([]string, len(p.Profiles))
			copy(profiles, p.Profiles)
			sort.Strings(profiles)
			for _, profile := range profiles {
				_, _ = fmt.Fprintln(writer, profile)
			}
			return nil
		}

		if cmd.Bool("images") {
			// Print images in service order (not sorted, matching docker-compose behavior)
			for _, svc := range services {
				if svc.Image != "" {
					_, _ = fmt.Fprintln(writer, svc.Image)
				}
			}
			return nil
		}

		if cmd.Bool("environment") {
			// Get environment variable names and sort them
			names := make([]string, 0, len(p.Environment))
			for name := range p.Environment {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				_, _ = fmt.Fprintf(writer, "%s=%s\n", name, p.Environment[name])
			}
			return nil
		}

		if cmd.Bool("variables") {
			// Load the raw model without interpolation to extract variables
			model, err := project.LoadModel(ctx, loadOpts...)
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
		filteredProject := p
		if len(services) > 0 {
			filteredProject = &types.Project{
				Name:         p.Name,
				WorkingDir:   p.WorkingDir,
				Services:     services,
				Networks:     p.Networks,
				Volumes:      p.Volumes,
				Secrets:      p.Secrets,
				Configs:      p.Configs,
				ComposeFiles: p.ComposeFiles,
				Environment:  p.Environment,
			}
		}

		// Output full config in requested format
		switch cmd.String("format") {
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
			return fmt.Errorf("unsupported format: %s", cmd.String("format"))
		}
	},
}

// initLogger configures the default slog logger with color and verbosity settings.
func initLogger(verbosity int, ansi string) {
	var level slog.Level
	switch {
	case verbosity >= 2:
		// Trace
		level = slog.LevelDebug - 4
	case verbosity >= 1:
		level = slog.LevelDebug
	default:
		level = slog.LevelInfo
	}

	noColor := false
	switch ansi {
	case "never":
		noColor = true
	case "always":
		// No need, just for clarity.
		noColor = false
	case "auto":
		// Support for https://no-color.org/
		if _, ok := os.LookupEnv("NO_COLOR"); ok {
			noColor = true
		} else if runtime.GOOS != "windows" && !isatty.IsTerminal(os.Stderr.Fd()) {
			// None windows and no terminal.
			noColor = true
		}
	}

	logger := slog.New(tint.NewHandler(
		colorable.NewColorable(os.Stderr),
		&tint.Options{
			NoColor:    noColor,
			Level:      level,
			TimeFormat: "15:04",
		},
	))
	slog.SetDefault(logger)
}

func main() {
	// Store for cli.BoolFlag.Config.Count
	var verbosity int

	cmd := &cli.Command{
		Usage: "Compose for incus",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "remote",
				Usage: "remote to connect to",
				Value: "",
			},
			&cli.StringFlag{
				Name:  "ansi",
				Usage: `Control when to print ANSI control character ("never", "always", "auto")`,
				Value: "auto",
				Action: func(ctx context.Context, cmd *cli.Command, v string) error {
					if !slices.Contains([]string{"never", "always", "auto"}, v) {
						return fmt.Errorf("Flag 'ansi' value %v invalid", v)
					}
					return nil
				},
			},
			&cli.StringSliceFlag{
				Name:  "env-file",
				Usage: "Specify alternative environment files",
			},
			&cli.StringSliceFlag{
				Name:  "profile",
				Usage: "Specify profiles to enable",
			},
			&cli.StringFlag{
				Name:        "project-directory",
				Usage:       `Specify an alternate working directory`,
				DefaultText: `current directory or parent of first compose file`,
			},
			&cli.StringFlag{
				Name:    "project-name",
				Aliases: []string{"p"},
				Usage:   `Project name`,
			},
			&cli.StringSliceFlag{
				Name:    "file",
				Aliases: []string{"f"},
				Usage:   `Compose configuration files`,
			},
			&cli.BoolFlag{
				Name:    "os-env",
				Aliases: []string{"E"},
				Usage:   "Include OS environment variables for interpolation",
			},
			&cli.BoolFlag{
				Name:    "verbosity",
				Aliases: []string{"v"},
				Config: cli.BoolConfig{
					Count: &verbosity,
				},
			},
		},
		Commands: []*cli.Command{
			upCommand,
			downCommand,
			psCommand,
			configCommand,
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			initLogger(verbosity, cmd.String("ansi"))

			// Commands that don't need an Incus client connection
			noClientCommands := []string{"config"}

			if slices.Contains(noClientCommands, cmd.Name) {
				return ctx, nil
			}

			// Connect to Incus server.
			// Two connection modes:
			// 1. Direct URL (via env vars, used for testing with nested Incus)
			// 2. Incus CLI config remote (normal usage)
			//
			// Environment variables for URL-based connections:
			//   - INCUS_COMPOSE_URL: Direct URL to connect to (e.g., https://192.168.1.100:8443)
			//   - INCUS_COMPOSE_CERT: Path to TLS client certificate
			//   - INCUS_COMPOSE_KEY: Path to TLS client key
			// Check for URL override (used for testing with nested Incus)
			if url, ok := os.LookupEnv("INCUS_COMPOSE_URL"); ok {
				slog.Debug("Using connection", "url", url)

				opts := []client.Option{
					client.URL(url),
					client.InsecureSkipVerify(),
				}

				// Add TLS client certificate if provided
				if cert, ok := os.LookupEnv("INCUS_COMPOSE_CERT"); ok {
					opts = append(opts, client.TLSClientCert(cert))
				}
				if key, ok := os.LookupEnv("INCUS_COMPOSE_KEY"); ok {
					opts = append(opts, client.TLSClientKey(key))
				}

				c := client.New(ctx, slog.Default(), opts...)
				if err := c.Connect(); err != nil {
					return ctx, err
				}

				return c.ToContext(ctx), nil
			}

			// Use Incus CLI configuration to resolve remotes
			// TODO(r3j0): Replace with custom config to avoid Incus CLI dependency
			conf, err := loadIncusConfig()
			if err != nil {
				return ctx, err
			}

			remote := cmd.String("remote")
			if remote == "" {
				remote = conf.DefaultRemote
			}

			instanceServer, err := conf.GetInstanceServer(remote)
			if err != nil {
				return ctx, err
			}

			imageCache, err := conf.GetInstanceServer(remote)
			if err != nil {
				return ctx, err
			}

			opts := []client.Option{
				client.ProvideConnection(instanceServer, imageCache),
			}

			slog.Debug("Using connection", "remote", remote)
			c := client.New(ctx, slog.Default(), opts...)
			if err := c.Connect(); err != nil {
				return ctx, err
			}
			return c.ToContext(ctx), nil
		},
		After: func(ctx context.Context, cmd *cli.Command) error {
			return nil
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		slog.Error("Command returned", "error", err)
		os.Exit(1)
	}
}
