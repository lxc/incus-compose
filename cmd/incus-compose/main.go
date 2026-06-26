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

	"github.com/creativeprojects/go-selfupdate"
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

	if name := cmd.Root().String("project-name"); name != "" {
		loadOpts = append(loadOpts, project.LoadName(name))
	}

	files := cmd.Root().StringSlice("file")
	dir := cmd.Root().String("project-directory")

	composeFileNames := []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

	cfile := ""
	if len(files) == 0 {
		for _, name := range composeFileNames {
			var candidate string
			var err error
			if dir != "" {
				candidate, err = filepath.Abs(filepath.Join(dir, name))
			} else {
				candidate, err = filepath.Abs(name)
			}
			if err == nil {
				if _, err := os.Stat(candidate); err == nil {
					cfile = candidate
					files = append(files, candidate)
					break
				}
			}
		}
	} else {
		for _, f := range files {
			if slices.Contains(composeFileNames, filepath.Base(f)) {
				cfile = f
				break
			}
		}
	}

	if cfile != "" {
		incusCFile := filepath.Join(filepath.Dir(cfile), strings.TrimSuffix(filepath.Base(cfile), filepath.Ext(cfile))+".incus"+filepath.Ext(cfile))
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

// selfUpdateWritable reports whether the running executable can be replaced in
// place. Self-update writes a new binary into the executable's directory and
// renames it over the target, so directory writability is what matters -- the
// running file itself cannot be opened O_WRONLY (ETXTBSY on Linux, locked on
// Windows). The check is delegated to a platform-specific dirWritable.
func selfUpdateWritable() bool {
	exe, err := selfupdate.ExecutablePath() // resolves symlinks to the real file
	if err != nil {
		return false
	}
	return dirWritable(filepath.Dir(exe))
}

func newRootCommand() *cli.Command {
	commands := []*cli.Command{
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
	}

	// "self-update" is only available if the executable is writeable and the version is not "latest".
	if version.Current() != "latest" && selfUpdateWritable() {
		commands = append(commands, newSelfUpdateCommand())
	}

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
				Name:    "env-file",
				Usage:   `Specify alternative environment files`,
				Sources: cli.EnvVars("INCUS_COMPOSE_ENV_FILE"),
			},
			&cli.StringSliceFlag{
				Name:    "profile",
				Usage:   `Specify profiles to enable`,
				Sources: cli.EnvVars("INCUS_COMPOSE_PROFILES"),
			},
			&cli.StringFlag{
				Name:    "project-directory",
				Aliases: []string{"pd"},
				Usage:   `Specify an alternate working directory (default: the path of the, first specified, Compose file)`,
				Sources: cli.EnvVars("INCUS_COMPOSE_PROJECT_DIRECTORY"),
			},
			&cli.StringFlag{
				Name:    "project-name",
				Aliases: []string{"p"},
				Usage:   `Project name`,
				Sources: cli.EnvVars("INCUS_COMPOSE_PROJECT_NAME"),
			},
			&cli.StringFlag{
				Name:    "storage-pool",
				Usage:   `Default storage pool to use, 'detect' will auto detect the name`,
				Value:   "detect",
				Sources: cli.EnvVars("INCUS_COMPOSE_STORAGE_POOL"),
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
				Sources: cli.EnvVars("INCUS_COMPOSE_FILE"),
			},
			&cli.BoolFlag{
				Name:    "os-env",
				Aliases: []string{"E"},
				Usage:   `Include OS environment variables for interpolation`,
			},
			&cli.BoolFlag{
				Name:    "debug",
				Usage:   `Enable debug logging`,
				Sources: cli.EnvVars("INCUS_COMPOSE_DEBUG"),
			},
			&cli.IntFlag{
				Name:    "workers",
				Usage:   `Number of concurrent workers`,
				Sources: cli.EnvVars("INCUS_COMPOSE_WORKERS"),
				Value:   10,
			},
		},
		Commands: commands,
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
