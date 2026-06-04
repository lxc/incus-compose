// Package main provides the incus-compose CLI.
package main

import (
	"context"
	"errors"
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

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/cmd/incus-compose/version"
	"gitlab.com/r3j0/incus-compose/project"
)

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
		incusCFile := filepath.Join(filepath.Dir(cfile), "compose.incus.yaml")
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

// noColor is set by the --ansi flag Action based on flag value and terminal detection.
var noColor bool

func initLogger(debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
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
				Action: func(ctx context.Context, cmd *cli.Command, v string) error {
					// NO_COLOR takes precedence (https://no-color.org/)
					if _, ok := os.LookupEnv("NO_COLOR"); ok {
						noColor = true
						return nil
					}
					switch v {
					case "always":
						noColor = false
					case "auto":
						// Non-windows and no terminal.
						noColor = runtime.GOOS != "windows" && !isatty.IsTerminal(os.Stderr.Fd())
					case "never":
						noColor = true
					default:
						return fmt.Errorf("flag 'ansi' value %q invalid", v)
					}
					return nil
				},
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
				Name:    "healthd-image",
				Usage:   `Healthd OCI image to use; {version} is replaced with the incus-compose version`,
				Value:   client.DefaultHealthdImage,
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_IMAGE"),
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
			upCommand,
			downCommand,
			startCommand,
			stopCommand,
			restartCommand,
			listCommand,
			psCommand,
			configCommand,
			execCommand,
			logsCommand,
			versionCommand,
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			initLogger(cmd.Bool("debug"))

			// Commands that don't need an Incus client connection
			noClientCommands := []string{"config", "version"}

			if slices.Contains(noClientCommands, cmd.Name) {
				return ctx, nil
			}

			// Connect to Incus server.
			// Priority: INCUS_COMPOSE_URL -> INCUS_REMOTE/--remote -> incus CLI default remote
			cacheProject := "default"
			if v, ok := os.LookupEnv("INCUS_COMPOSE_IMAGE_CACHE"); ok {
				cacheProject = v
			}

			// 1. If INCUS_COMPOSE_URL is set, use direct URL connection
			if url, ok := os.LookupEnv("INCUS_COMPOSE_URL"); ok {
				slog.Debug("Using connection", "url", url)

				opts := []client.ClientOption{
					client.ClientURL(url),
					client.ClientLogger(slog.Default()),
					client.ClientInsecureSkipVerify(),
					client.ClientDefaultStoragePool(cmd.String("storage-pool")),
				}

				// Add TLS client certificate if provided
				if cert, ok := os.LookupEnv("INCUS_COMPOSE_CERT"); ok {
					opts = append(opts, client.ClientTLSClientCert(cert))
				}
				if key, ok := os.LookupEnv("INCUS_COMPOSE_KEY"); ok {
					opts = append(opts, client.ClientTLSClientKey(key))
				}

				opts = append(opts, client.ClientCacheProject(cacheProject))

				c := client.New(ctx, opts...)
				if err := c.Connect(); err != nil {
					return ctx, err
				}

				if cmd.Bool("debug") {
					client.AddDebuggerHook(c)
				}

				return context.WithValue(ctx, clientKey{}, c), nil
			}

			// 2. Use Incus CLI config (explicit --remote flag, or configured default remote)
			conf, err := cliconfig.LoadConfig("")
			if err != nil {
				return ctx, err
			}

			remote := cmd.String("remote")
			if remote == "" {
				remote = conf.DefaultRemote
			}

			slog.Debug("Using connection", "remote", remote)

			server, err := conf.GetInstanceServer(remote)
			if err != nil {
				return ctx, err
			}

			opts := []client.ClientOption{
				client.ClientLogger(slog.Default()),
				client.ClientProvideInstanceServer(server),
				client.ClientCacheProject(cacheProject),
				client.ClientDefaultStoragePool(cmd.String("storage-pool")),
			}

			c := client.New(ctx, opts...)
			if err := c.Connect(); err != nil {
				return ctx, err
			}

			if cmd.Bool("debug") {
				client.AddDebuggerHook(c)
			}

			return context.WithValue(ctx, clientKey{}, c), nil
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
