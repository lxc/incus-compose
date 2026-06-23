package main

import (
	"context"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

func newUpCommand() *cli.Command {
	return &cli.Command{
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
			&cli.DurationFlag{
				Name:  "timeout",
				Usage: "Timeout for stopping/starting",
				Value: 10 * time.Second,
			},
			&cli.DurationFlag{
				Name:  "dependency-timeout",
				Usage: "Max time to wait for service_healthy depends_on (0 = no limit)",
				Value: 5 * time.Minute,
			},
			&cli.StringSliceFlag{
				Name:  "scale",
				Usage: "Scale SERVICE to NUM instances (service=num)",
			},
			&cli.StringFlag{
				Name:  "pull",
				Usage: `Pull image before running ("always"|"missing"|"never"|"policy")`,
				Value: "policy",
			},
			&cli.BoolFlag{
				Name:  "build",
				Usage: "Build images before starting containers",
			},
			&cli.StringFlag{
				Name:    "builder",
				Usage:   "Preferred builder, binary name or absolute path. Empty for auto-detect.",
				Sources: cli.EnvVars("INCUS_COMPOSE_BUILDER"),
			},
			&cli.BoolFlag{
				Name:  "no-build",
				Usage: "Do not build images even if missing",
			},
			&cli.BoolFlag{
				Name:  "no-deps",
				Usage: "Don't start linked services",
			},
			&cli.BoolFlag{
				Name:    "detach",
				Aliases: []string{"d"},
				Usage:   "Detached mode: run containers in the background (a WIP)",
			},
			&cli.BoolFlag{
				Name:  "no-healthd",
				Usage: "Don't create healthd sidecar for healthchecks",
			},
			&cli.StringFlag{
				Name:    "healthd-image",
				Usage:   `Healthd OCI image to use; {version} is replaced with the incus-compose version`,
				Value:   defaultHealthdImage,
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_IMAGE"),
			},
			&cli.StringFlag{
				Name:    "healthd-binary",
				Usage:   "Path to local ic-healthd binary (uses images:alpine/edge instead of OCI image)",
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_BINARY"),
			},
			&cli.StringFlag{
				Name:    "healthd-network",
				Usage:   "Incus bridge for healthd to use (default: auto-detect)",
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_NETWORK"),
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			noColor := noColor(ctx)

			globalClient, err := clientFromContext(ctx)
			if err != nil {
				return err
			}
			if err := globalClient.Connect(); err != nil {
				return err
			}

			p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
			if err != nil {
				globalClient.LogError("Loading the project", "error", err)
				return errLogged.Wrap(err)
			}

			c, err := globalClient.EnsureProject(
				p.Name,
				client.EnsureProjectWithCreate(),
				client.EnsureProjectWithConfig(p.ProjectConfig()),
			)
			if err != nil {
				globalClient.LogError("Getting the incus project", "error", err)
				return errLogged.Wrap(err)
			}

			// Register the DNS Watcher
			if err := c.RegisterDNSWatcher(); err != nil {
				globalClient.LogError("Registering the DNS watcher", "project", p.Name, "error", err)
				return errLogged.Wrap(err)
			}

			if err := c.Open(); err != nil {
				globalClient.LogError("Opening the project client", "error", err)
				return errLogged.Wrap(err)
			}

			// Render live progress for the ensure phase, where image downloads happen.
			finish := startProgress(globalClient, c, noColor, cmd.Root().Writer)

			usesHealthd := !cmd.Bool("no-healthd")
			if !healthdInUseByProject(p) {
				usesHealthd = false
			}

			if usesHealthd {
				hparams := healthdParams{
					projectName: p.Name,
					binary:      cmd.String("healthd-binary"),
					image:       resolveHealthdImage(cmd.String("healthd-image")),
					pull:        cmd.String("pull"),
					reCreate:    cmd.Bool("recreate"),
					network:     cmd.String("healthd-network"),
					timeout:     cmd.Duration("timeout"),
					stdout:      cmd.Root().Writer,
					stderr:      cmd.Root().ErrWriter,
					workers:     cmd.Root().Int("workers"),
				}

				inst, resources, err := healthdGetResources(c, hparams)
				if err != nil {
					globalClient.LogError("Creating healthd resources", "error", err)

					finish(err == nil)
					return errLogged.Wrap(err)
				}

				if err := healthdUp(ctx, c, inst, resources, hparams); err != nil {
					globalClient.LogError("Starting healthd", "error", err)

					finish(err == nil)
					return errLogged.Wrap(err)
				}
			}

			buildMode := client.BuildAuto
			if cmd.Bool("build") {
				buildMode = client.BuildForce
			} else if cmd.Bool("no-build") {
				buildMode = client.BuildNever
			}
			buildInfo := client.BuildInfo{
				Mode:             buildMode,
				PreferredBuilder: cmd.String("builder"),
			}

			params := upParams{
				reCreate:          cmd.Bool("recreate"),
				start:             !cmd.Bool("no-start"),
				healthd:           usesHealthd,
				deps:              !cmd.Bool("no-deps"),
				services:          cmd.Args().Slice(),
				pull:              cmd.String("pull"),
				build:             buildInfo,
				timeout:           cmd.Duration("timeout"),
				dependencyTimeout: cmd.Duration("dependency-timeout"),
				scale:             parseScale(cmd.StringSlice("scale")),
				stdout:            cmd.Root().Writer,
				stderr:            cmd.Root().ErrWriter,
				workers:           cmd.Root().Int("workers"),
			}
			if err := runUp(ctx, globalClient, c, p, params); err != nil {
				_ = c.Done()

				finish(err == nil)
				return err
			}
			_ = c.Done()

			finish(err == nil)

			c.LogDebug("All done")
			return nil
		},
	}
}

// upParams holds the parsed arguments for an up run.
// services is the raw service filter (empty means all services).
type upParams struct {
	services          []string
	start             bool
	healthd           bool
	deps              bool
	reCreate          bool
	pull              string
	build             client.BuildInfo
	timeout           time.Duration
	dependencyTimeout time.Duration
	scale             map[string]int
	stdout            io.Writer
	stderr            io.Writer
	workers           int
}

// parseScale parses --scale flags of the form "service=num".
func parseScale(values []string) map[string]int {
	scaleOverrides := make(map[string]int)
	for _, s := range values {
		parts := strings.SplitN(s, "=", 2)
		if len(parts) == 2 {
			if n, err := strconv.Atoi(parts[1]); err == nil {
				scaleOverrides[parts[0]] = n
			}
		}
	}
	return scaleOverrides
}

// runUp creates (and optionally starts) the services of a loaded project.
func runUp(ctx context.Context, globalClient *client.GlobalClient, c *client.Client, p *project.Project, params upParams) error {
	runOptions := []client.Option{client.OptionTimeout(params.timeout)}
	// With --no-deps the linked services are out of scope, so don't wait on
	// healthd dependency conditions (depends_on: service_healthy) that can never
	// be satisfied because those dependencies were never started.
	if !params.healthd || !params.deps {
		runOptions = append(runOptions, client.OptionNoHealthd())
	}

	// defer func() {
	// 	if c.Errors() != nil {
	// 		c.Logger().ErrorContext(c.Ctx, "Error(s) during up", "error", c.Errors())
	// 		if c.IsDebugging() {
	// 			c.Logger().WarnContext(c.Ctx, "Wont rollback in debug")
	// 		} else {
	// 			err := c.Rollback(0)
	// 			if err != nil {
	// 				c.Logger().ErrorContext(c.Ctx, "During rollback", "error", err)
	// 			}
	// 		}
	// 	}
	// }()

	if params.reCreate {
		stack := client.NewStack(c, client.StackWorkers(params.workers))
		toStackOpts := []project.ToStackOption{}
		toStackOpts = append(toStackOpts, project.ToStackNoImages(), project.ToStackReverse(), project.ToStackOnlyServices(params.services))
		if params.deps {
			toStackOpts = append(toStackOpts, project.ToStackWithDeps())
		}
		if len(params.scale) > 0 {
			toStackOpts = append(toStackOpts, project.ToStackScale(params.scale))
		}
		err := p.ToStack(c, stack, toStackOpts...)
		if err != nil {
			c.LogError("Creating the stack in reCreate", "error", err)
			return errLogged.Wrap(err)
		}

		c.LogDebug("Ensure", "resources", stack.All())

		recreateOptions := append(runOptions, client.OptionForce())

		// Ensure without create for "recreate" (resolution only, no progress).
		if err := stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure, params.stdout, params.stderr); err != nil {
			c.LogDebug("Ensuring for reCreate", "error", err)
		} else {
			// Stop
			errStop := stack.ForAction(client.ActionStop).Run(ctx, client.ActionStop, params.stdout, params.stderr, recreateOptions...)
			if errStop != nil {
				c.LogDebug("Stopping resources", "error", errStop)
			}

			// Delete
			deleteStack := stack.ForAction(client.ActionDelete)
			c.LogDebug("Recreate delete", "resources", deleteStack.All())
			errDel := deleteStack.Run(ctx, client.ActionDelete, params.stdout, params.stderr, recreateOptions...)
			if errDel != nil {
				c.LogDebug("Deleting resources", "error", errDel)
			}
		}

		// Start fresh after recreate
		c.ResetResources()
	}

	stack := client.NewStack(c, client.StackWorkers(params.workers))
	toStackOpts := []project.ToStackOption{}
	toStackOpts = append(toStackOpts, project.ToStackStorageVolumes(), project.ToStackOnlyServices(params.services))
	if params.deps {
		toStackOpts = append(toStackOpts, project.ToStackWithDeps())
	}
	if len(params.scale) > 0 {
		toStackOpts = append(toStackOpts, project.ToStackScale(params.scale))
	}
	err := p.ToStack(c, stack, toStackOpts...)
	if err != nil {
		c.LogError("Adding the project to a stack", "error", err)
		return errLogged.Wrap(err)
	}

	c.LogDebug("Ensure", "resources", stack.All())

	// Ensure with create. --pull=always refreshes cached images from registry.
	// policy and missing only use the local cache (pull if not present).
	startOptions := append(runOptions, client.OptionCreate())
	if params.pull == "always" {
		startOptions = append(startOptions, client.OptionPull())
	}
	if params.build.Mode != client.BuildAuto || params.build.PreferredBuilder != "" {
		startOptions = append(startOptions, client.OptionBuild(params.build))
	}
	if params.dependencyTimeout > 0 {
		startOptions = append(startOptions, client.OptionDependencyTimeout(params.dependencyTimeout))
	}

	err = stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure, params.stdout, params.stderr, startOptions...)
	if err != nil {
		c.LogError("Ensuring resources", "error", err)
		return errLogged.Wrap(err)
	}

	// Start
	if params.start {
		if err := stack.ForAction(client.ActionStart).Run(ctx, client.ActionStart, params.stdout, params.stderr, startOptions...); err != nil {
			c.LogError("Starting resources", "error", err)
			return errLogged.Wrap(err)
		}
	}

	return nil
}
