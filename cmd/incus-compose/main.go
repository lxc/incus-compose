// Package main provides the incus-compose CLI.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/lmittmann/tint"
	"github.com/lxc/incus/v7/shared/cliconfig"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/cmd/incus-compose/version"
	"github.com/lxc/incus-compose/project"
)

type noColorKey struct{}

// errLogged is an internal sentinel error, return it to silence the error but exit 1.
var errLogged = client.NewError("Logged error")

// buildLoadOptions converts CLI flags to project.LoadOption slice.
func buildLoadOptions(cmd *cli.Command) []project.LoadOption {
	loadOpts := []project.LoadOption{}

	if name := cmd.String("project-name"); name != "" {
		loadOpts = append(loadOpts, project.LoadName(name))
	}

	files := cmd.StringSlice("file")
	dir := cmd.String("project-directory")

	cfile := ""
	if len(files) == 0 {
		cfile = "compose.yaml"
		if dir != "" {
			cfile = filepath.Join(dir, cfile)
		} else {
			cfile, _ = filepath.Abs(cfile)
		}
		if _, err := os.Stat(cfile); err == nil {
			files = append(files, cfile)
		}
	} else {
		for _, f := range files {
			if filepath.Base(f) == "compose.yaml" {
				cfile = f
				break
			}
		}
	}

	if cfile != "" {
		incusCFile := filepath.Join(filepath.Dir(cfile), strings.TrimSuffix(filepath.Base(cfile), filepath.Ext(cfile))+".incus.yaml")
		if _, err := os.Stat(incusCFile); err == nil {
			files = append(files, incusCFile)
		}
	} else if dir != "" {
		incusCFile := filepath.Join(dir, "compose.incus.yaml")
		if _, err := os.Stat(incusCFile); err == nil {
			files = append(files, incusCFile)
		}
	}

	if len(files) > 0 {
		loadOpts = append(loadOpts, project.LoadFiles(files))
	}

	if dir != "" {
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

func initLogger(debug bool, noColor bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}

	logWriter.Swap(colorable.NewColorable(os.Stderr))
	logger := slog.New(tint.NewHandler(
		logWriter,
		&tint.Options{
			NoColor:    noColor,
			Level:      level,
			TimeFormat: "15:04",
		},
	))
	slog.SetDefault(logger)
}

type clientKey struct{}

func resolveHealthdImage(image string) string {
	return strings.ReplaceAll(image, "{version}", version.Version)
}

func clientFromContext(ctx context.Context) (*client.GlobalClient, error) {
	ca := ctx.Value(clientKey{})
	c, ok := ca.(*client.GlobalClient)
	if !ok {
		return nil, errors.New("failed to retrieve the client from context")
	}

	return c, nil
}

func noColor(ctx context.Context) bool {
	if ctx.Value(noColorKey{}) == nil {
		return false
	}

	ok, v := ctx.Value(noColorKey{}).(bool)
	if !ok {
		return false
	}

	return v
}

func newRootCommand() *cli.Command {
	return &cli.Command{
		Usage: "Compose for incus",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "remote",
				Usage:   "remote to connect to",
				Value:   "",
				Sources: cli.EnvVars("INCUS_REMOTE"),
			},
			&cli.StringFlag{
				Name:    "ansi",
				Usage:   `Control when to print ANSI control character ("never", "always", "auto")`,
				Value:   "auto",
				Sources: cli.EnvVars("INCUS_COMPOSE_ANSI"),
			},
			&cli.StringSliceFlag{
				Name:  "env-file",
				Usage: `Specify alternative environment files`,
			},
			&cli.StringSliceFlag{
				Name:  "profile",
				Usage: `Specify profiles to enable`,
			},
			&cli.StringFlag{
				Name:    "network-project",
				Usage:   `Project to locate the network profile`,
				Sources: cli.EnvVars("INCUS_COMPOSE_NETWORK_PROJECT"),
				Value:   "default",
			},
			&cli.StringFlag{
				Name:    "network-profile",
				Usage:   `Profile to extract devices.eth0.network from`,
				Sources: cli.EnvVars("INCUS_COMPOSE_NETWORK_PROFILE"),
				Value:   "default",
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
			&cli.StringFlag{
				Name:  "storage-pool",
				Usage: `Default storage pool to use, 'detect' will auto detect the name`,
				Value: "detect",
			},
			&cli.StringFlag{
				Name:    "image-cache",
				Usage:   `Image cache project to use`,
				Value:   "default",
				Sources: cli.EnvVars("INCUS_COMPOSE_IMAGE_CACHE"),
			},
			&cli.StringSliceFlag{
				Name:    "file",
				Aliases: []string{"f"},
				Usage:   `Compose configuration files`,
			},
			&cli.BoolFlag{
				Name:    "os-env",
				Aliases: []string{"E"},
				Usage:   `Include OS environment variables for interpolation`,
			},
			&cli.BoolFlag{
				Name:  "debug",
				Usage: `Enable debug logging`,
			},
		},
		Commands: []*cli.Command{
			newUpCommand(),
			newDownCommand(),
			newBuildCommand(),
			newStartCommand(),
			newStopCommand(),
			newRestartCommand(),
			newListCommand(),
			newPsCommand(),
			newConfigCommand(),
			newExecCommand(),
			newLogsCommand(),
			newIncusCommand(),
			newHealthdCommand(),
			newVersionCommand(),
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			noColor := false

			// NO_COLOR takes precedence (https://no-color.org/)
			if _, ok := os.LookupEnv("NO_COLOR"); ok {
				noColor = true
			} else {
				switch strings.ToLower(cmd.String("ansi")) {
				case "always":
					noColor = true
				case "auto":
					// Non-windows and no terminal.
					if runtime.GOOS == "windows" || isatty.IsTerminal(os.Stderr.Fd()) {
						noColor = false
					} else {
						noColor = true
					}
				case "never":
					noColor = true
				default:
					noColor = false
				}
			}

			initLogger(cmd.Bool("debug"), noColor)

			// Commands that don't need an Incus client connection
			noClientCommands := []string{"config", "version", "incus"}

			if slices.Contains(noClientCommands, cmd.Name) {
				return ctx, nil
			}

			// Connect to Incus server.
			// Priority: INCUS_COMPOSE_URL -> INCUS_REMOTE/--remote -> incus CLI default remote
			// Use Incus CLI config (explicit --remote flag, or configured default remote)
			conf, err := cliconfig.LoadConfig("")
			if err != nil {
				return ctx, err
			}

			remote := cmd.String("remote")
			if remote == "" {
				remote = conf.DefaultRemote
			}

			server, err := conf.GetInstanceServer(remote)
			if err != nil {
				return ctx, err
			}

			opts := []client.ClientOption{
				client.ClientLogger(slog.Default()),
				client.ClientProvideInstanceServer(server),
				client.ClientCacheProject(cmd.String("image-cache")),
				client.ClientDefaultStoragePool(cmd.String("storage-pool")),
			}

			if cmd.String("network-project") != "default" || cmd.String("network-profile") != "default" {
				opts = append(opts, client.ClientNetworkProjectProfile(
					cmd.String("network-project"),
					cmd.String("network-profile"),
				))
			}

			c := client.New(ctx, opts...)
			return context.WithValue(context.WithValue(ctx, clientKey{}, c), noColorKey{}, noColor), nil
		},
		After: func(ctx context.Context, cmd *cli.Command) error {
			return nil
		},
	}
}

func main() {
	if err := newRootCommand().Run(context.Background(), os.Args); err != nil {
		if errors.Is(err, errLogged) {
			os.Exit(1)
		}

		slog.Error("Command returned", "error", err)
		os.Exit(1)
	}
}
