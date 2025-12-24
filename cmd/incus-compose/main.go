package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/lmittmann/tint"
	"github.com/lxc/incus/v6/shared/cliconfig"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incuscompose"
	"gitlab.com/r3j0/incuscompose/pkg/icclient"
)

// buildOptions converts CLI flags to LoadProjectOption slice.
func buildOptions(cmd *cli.Command) []incuscompose.LoadProjectOption {
	loadOpts := []incuscompose.LoadProjectOption{}

	if name := cmd.String("project-name"); name != "" {
		loadOpts = append(loadOpts, incuscompose.LoadProjectName(name))
	}

	if files := cmd.StringSlice("file"); len(files) > 0 {
		loadOpts = append(loadOpts, incuscompose.LoadProjectFiles(files))
	}

	if dir := cmd.String("project-directory"); dir != "" {
		loadOpts = append(loadOpts, incuscompose.LoadProjectWorkingDir(dir))
	}

	if envFiles := cmd.StringSlice("env-file"); len(envFiles) > 0 {
		loadOpts = append(loadOpts, incuscompose.LoadProjectEnvFiles(envFiles))
	}

	if profiles := cmd.StringSlice("profile"); len(profiles) > 0 {
		loadOpts = append(loadOpts, incuscompose.LoadProjectProfiles(profiles))
	}

	if cmd.Bool("os-env") {
		loadOpts = append(loadOpts, incuscompose.LoadProjectOsEnv())
	}

	return loadOpts
}

var runCommand = &cli.Command{
	Name:            "run",
	Usage:           "Run a one-off command on a service",
	Category:        "compose",
	SkipFlagParsing: true,
	Arguments: []cli.Argument{
		&cli.StringArg{
			Name: "service",
		},
		&cli.StringArg{
			Name:  "command",
			Value: "",
		},
		&cli.StringArgs{
			Name: "args",
			Max:  -1,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		p, err := incuscompose.LoadProject(ctx, buildOptions(cmd)...)
		if err != nil {
			slog.Error("While loading the 'compose.yaml'", "error", err)
			return err
		}
		return incuscompose.Run(ctx, p, cmd.Args().First(), incuscompose.RunCmd(cmd.Args().Get(2), cmd.Args().Tail()))
	},
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
		&cli.StringFlag{
			Name:  "remote",
			Usage: "Incus remote to use",
			Value: "local",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		p, err := incuscompose.LoadProject(ctx, buildOptions(cmd)...)
		if err != nil {
			return err
		}

		conf, err := loadIncusConfig()
		if err != nil {
			return err
		}

		opts := []incuscompose.UpOption{}
		if cmd.Bool("recreate") {
			opts = append(opts, incuscompose.UpRecreate())
		}
		if cmd.Bool("no-start") {
			opts = append(opts, incuscompose.UpNoStart())
		}
		if cmd.Args().Len() > 0 {
			opts = append(opts, incuscompose.UpServices(cmd.Args().Slice()))
		}

		c, err := icclient.FromContext(ctx)
		if err != nil {
			return err
		}

		return incuscompose.Up(conf, c, p, opts...)
	},
}

// loadIncusConfig loads the Incus CLI configuration.
func loadIncusConfig() (*cliconfig.Config, error) {
	conf, err := cliconfig.LoadConfig("")
	if err != nil {
		return nil, fmt.Errorf("loading Incus config: %w", err)
	}
	return conf, nil
}

var downCommand = &cli.Command{
	Name:      "down",
	Usage:     "Stop and remove containers",
	Category:  "compose",
	ArgsUsage: "[SERVICE...]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "volumes",
			Aliases: []string{"v"},
			Usage:   "Remove named volumes",
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
		p, err := incuscompose.LoadProject(ctx, buildOptions(cmd)...)
		if err != nil {
			return err
		}

		opts := []incuscompose.DownOption{}
		if cmd.Bool("volumes") {
			opts = append(opts, incuscompose.DownVolumes())
		}
		if t := cmd.Int("timeout"); t > 0 {
			opts = append(opts, incuscompose.DownTimeout(int(t)))
		}
		if cmd.Args().Len() > 0 {
			opts = append(opts, incuscompose.DownServices(cmd.Args().Slice()))
		}

		c, err := icclient.FromContext(ctx)
		if err != nil {
			return err
		}

		return incuscompose.Down(c, p, opts...)
	},
}

var psCommand = &cli.Command{
	Name:      "ps",
	Usage:     "List containers",
	Category:  "compose",
	ArgsUsage: "[SERVICE...]",
	Flags: []cli.Flag{
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
		p, err := incuscompose.LoadProject(ctx, buildOptions(cmd)...)
		if err != nil {
			return err
		}

		opts := []incuscompose.PsOption{}
		if cmd.Bool("all") {
			opts = append(opts, incuscompose.PsAll())
		}
		if cmd.Args().Len() > 0 {
			opts = append(opts, incuscompose.PsServices(cmd.Args().Slice()))
		}

		c, err := icclient.FromContext(ctx)
		if err != nil {
			return err
		}

		return incuscompose.Ps(c, p, opts...)
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
		p, err := incuscompose.LoadProject(ctx, buildOptions(cmd)...)
		if err != nil {
			slog.Error("While loading the 'compose.yaml'", "error", err)
			return err
		}

		opts := []incuscompose.ConfigOption{}

		if cmd.Bool("quiet") {
			opts = append(opts, incuscompose.ConfigQuiet())
		}

		if cmd.Bool("services") {
			opts = append(opts, incuscompose.ConfigServicesOnly())
		}

		if cmd.Bool("volumes") {
			opts = append(opts, incuscompose.ConfigVolumesOnly())
		}

		if cmd.Bool("networks") {
			opts = append(opts, incuscompose.ConfigNetworksOnly())
		}

		if cmd.Bool("profiles") {
			opts = append(opts, incuscompose.ConfigProfilesOnly())
		}

		if cmd.Bool("images") {
			opts = append(opts, incuscompose.ConfigImagesOnly())
		}

		if cmd.Bool("environment") {
			opts = append(opts, incuscompose.ConfigEnvironmentOnly())
		}

		if cmd.Bool("variables") {
			opts = append(opts, incuscompose.ConfigVariablesOnly())
			// Pass load options so Config can reload the model without interpolation
			opts = append(opts, incuscompose.ConfigLoadOptions(buildOptions(cmd)...))
		}

		if format := cmd.String("format"); format != "" {
			opts = append(opts, incuscompose.ConfigFormat(format))
		}

		if output := cmd.String("output"); output != "" {
			opts = append(opts, incuscompose.ConfigOutput(output))
		}

		// Filter by specific services if provided
		if cmd.Args().Len() > 0 {
			opts = append(opts, incuscompose.ConfigServices(cmd.Args().Slice()))
		}

		return incuscompose.Config(ctx, p, opts...)
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
			runCommand,
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			initLogger(verbosity, cmd.String("ansi"))

			noClientCommands := []string{"config"}

			if slices.Contains(noClientCommands, cmd.Name) {
				return ctx, nil
			}

			// connectIncus connects to an Incus server.
			// If INCUS_COMPOSE_URL is set, it connects directly to that URL (useful for testing).
			// Otherwise, it uses the specified remote from Incus CLI config.
			//
			// Environment variables for URL-based connections:
			//   - INCUS_COMPOSE_URL: Direct URL to connect to (e.g., https://192.168.1.100:8443)
			//   - INCUS_COMPOSE_CERT: Path to TLS client certificate
			//   - INCUS_COMPOSE_KEY: Path to TLS client key
			// Check for URL override (used for testing with nested Incus)
			if url, ok := os.LookupEnv("INCUS_COMPOSE_URL"); ok {
				slog.Debug("Using connection", "url", url)

				opts := []icclient.Option{
					icclient.InsecureSkipVerify(),
				}

				// Add TLS client certificate if provided
				if cert, ok := os.LookupEnv("INCUS_COMPOSE_CERT"); ok {
					opts = append(opts, icclient.TLSClientCert(cert))
				}
				if key, ok := os.LookupEnv("INCUS_COMPOSE_KEY"); ok {
					opts = append(opts, icclient.TLSClientKey(key))
				}

				c := icclient.New(ctx, slog.Default(), url, opts...)
				if err := c.Connect(); err != nil {
					return ctx, err
				}

				return c.ToContext(ctx), nil
			}

			// TODO(r3j0): Replace the whole logic with a own config.
			// TODO(r3j0): Key sniffing is a guess, works here.
			conf, err := loadIncusConfig()
			if err != nil {
				return ctx, err
			}

			remote := cmd.String("remote")
			if remote == "" {
				remote = conf.DefaultRemote
			}

			ic, err := conf.GetInstanceServer(remote)
			if err != nil {
				return ctx, err
			}

			info, err := ic.GetConnectionInfo()
			if err != nil {
				return ctx, err
			}

			url := info.URL
			crt := info.Certificate

			crtFile := filepath.Base(crt)
			keyFile := strings.TrimSuffix(crtFile, filepath.Ext(crt)) + ".key"
			key := filepath.Join(filepath.Dir(crt), keyFile)

			opts := []icclient.Option{
				icclient.TLSClientCert(crt),
				icclient.TLSClientKey(key),
			}

			c := icclient.New(ctx, slog.Default(), url, opts...)
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
