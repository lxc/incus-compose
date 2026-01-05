// Package main provides the incus-compose CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"slices"

	"github.com/lmittmann/tint"
	"github.com/lxc/incus/v6/shared/cliconfig"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/client"
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

// initLogger configures the default slog logger with color and debug settings.
func initLogger(debug bool, ansi string) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
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

type clientKey struct{}

func clientFromContext(ctx context.Context) (*client.GlobalClient, error) {
	ca := ctx.Value(clientKey{})
	c, ok := ca.(*client.GlobalClient)
	if !ok {
		return nil, errors.New("failed to retrieve the client from context")
	}

	return c, nil
}

func main() {
	cmd := &cli.Command{
		Usage: "Compose for incus",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "remote",
				Usage:   "remote to connect to",
				Value:   "",
				Sources: cli.EnvVars("INCUS_REMOTE"),
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
			listCommand,
			configCommand,
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			initLogger(cmd.Bool("debug"), cmd.String("ansi"))

			// Commands that don't need an Incus client connection
			noClientCommands := []string{"config"}

			if slices.Contains(noClientCommands, cmd.Name) {
				return ctx, nil
			}

			// Connect to Incus server.
			// Priority: INCUS_REMOTE/--remote -> INCUS_COMPOSE_URL -> "local" remote
			remote := cmd.String("remote")

			// 1. If remote is explicitly set, use Incus CLI config
			if remote != "" {
				slog.Debug("Using connection", "remote", remote)

				conf, err := cliconfig.LoadConfig("")
				if err != nil {
					return ctx, err
				}

				server, err := conf.GetInstanceServer(remote)
				if err != nil {
					return ctx, err
				}

				opts := []client.ClientOption{
					client.ClientLogger(slog.Default()),
					client.ClientProvideInstanceServer(server),
					client.ClientCacheProject("incus-compose-images"),
				}

				c := client.New(ctx, opts...)
				if err := c.Connect(); err != nil {
					return ctx, err
				}

				return context.WithValue(ctx, clientKey{}, c), nil
			}

			// 2. If INCUS_COMPOSE_URL is set, use direct URL connection
			if url, ok := os.LookupEnv("INCUS_COMPOSE_URL"); ok {
				slog.Debug("Using connection", "url", url)

				opts := []client.ClientOption{
					client.ClientURL(url),
					client.ClientLogger(slog.Default()),
					client.ClientInsecureSkipVerify(),
				}

				// Add TLS client certificate if provided
				if cert, ok := os.LookupEnv("INCUS_COMPOSE_CERT"); ok {
					opts = append(opts, client.ClientTLSClientCert(cert))
				}
				if key, ok := os.LookupEnv("INCUS_COMPOSE_KEY"); ok {
					opts = append(opts, client.ClientTLSClientKey(key))
				}

				opts = append(opts, client.ClientCacheProject("incus-compose-images"))

				c := client.New(ctx, opts...)
				if err := c.Connect(); err != nil {
					return ctx, err
				}

				return context.WithValue(ctx, clientKey{}, c), nil
			}

			// 3. Fall back to "local" remote
			slog.Debug("Using connection", "remote", "local")

			conf, err := cliconfig.LoadConfig("")
			if err != nil {
				return ctx, err
			}

			server, err := conf.GetInstanceServer("local")
			if err != nil {
				return ctx, err
			}

			opts := []client.ClientOption{
				client.ClientLogger(slog.Default()),
				client.ClientProvideInstanceServer(server),
				client.ClientCacheProject("incus-compose-images"),
			}

			c := client.New(ctx, opts...)
			if err := c.Connect(); err != nil {
				return ctx, err
			}

			return context.WithValue(ctx, clientKey{}, c), nil
		},
		After: func(ctx context.Context, cmd *cli.Command) error {
			return nil
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		if errors.Is(err, errLogged) {
			os.Exit(1)
		}

		slog.Error("Command returned", "error", err)
		os.Exit(1)
	}
}
