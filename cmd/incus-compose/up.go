package main

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-colorable"
	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/project"
)

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
		&cli.BoolFlag{
			Name:  "no-build",
			Usage: "Do not build images even if missing",
		},
		&cli.BoolFlag{
			Name:    "detach",
			Aliases: []string{"d"},
			Usage:   "Detached mode: run containers in the background",
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
		globalClient, err := clientFromContext(ctx)
		if err != nil {
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
		finish := startProgress(globalClient, c, cmd.Root().Writer)

		if !cmd.Bool("no-healthd") && healthdInUseByProject(p) {
			hparams := healthdParams{
				projectName: p.Name,
				binary:      cmd.String("healthd-binary"),
				image:       resolveHealthdImage(cmd.String("healthd-image")),
				pull:        cmd.String("pull"),
				reCreate:    cmd.Bool("recreate"),
				network:     cmd.String("healthd-network"),
				timeout:     cmd.Duration("timeout"),
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

		build := client.BuildAuto
		if cmd.Bool("build") {
			build = client.BuildForce
		} else if cmd.Bool("no-build") {
			build = client.BuildNever
		}
		params := upParams{
			reCreate:          cmd.Bool("recreate"),
			start:             !cmd.Bool("no-start"),
			services:          cmd.Args().Slice(),
			pull:              cmd.String("pull"),
			build:             build,
			timeout:           cmd.Duration("timeout"),
			dependencyTimeout: cmd.Duration("dependency-timeout"),
			scale:             parseScale(cmd.StringSlice("scale")),
		}
		if err := runUp(ctx, globalClient, c, p, params); err != nil {
			_ = c.Done()

			finish(err == nil)
			return err
		}
		_ = c.Done()
		if params.start && !cmd.Bool("detach") {
			var out io.Writer
			if f, ok := cmd.Root().Writer.(*os.File); ok {
				out = colorable.NewColorable(f)
			} else {
				out = cmd.Root().Writer
			}

			finish(err == nil)
			return runLogs(ctx, globalClient, c, p, params.services, true, out)
		}

		finish(err == nil)

		c.LogDebug("All done")
		return nil
	},
}

// upParams holds the parsed arguments for an up run.
// services is the raw service filter (empty means all services).
type upParams struct {
	services          []string
	start             bool
	reCreate          bool
	pull              string
	build             client.BuildMode
	timeout           time.Duration
	dependencyTimeout time.Duration
	scale             map[string]int
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
	timeout := params.timeout
	pull := params.pull

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
		stack := client.NewStack(c)
		toStackOpts := []project.ToStackOption{}
		toStackOpts = append(toStackOpts, project.ToStackNoImages(), project.ToStackReverse())
		if len(params.services) > 0 {
			toStackOpts = append(toStackOpts, project.ToStackOnlyServices(params.services))
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

		// Ensure without create for "recreate" (resolution only, no progress).
		if err := stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure); err != nil {
			c.LogDebug("Ensuring for reCreate", "error", err)
		} else {
			// Stop
			errStop := stack.ForAction(client.ActionStop).Run(ctx, client.ActionStop, client.OptionForce(), client.OptionTimeout(timeout))
			if errStop != nil {
				c.LogDebug("Stopping resources", "error", errStop)
			}

			// Delete
			deleteStack := stack.ForAction(client.ActionDelete)
			c.LogDebug("Recreate delete", "resources", deleteStack.All())
			errDel := deleteStack.Run(ctx, client.ActionDelete, client.OptionForce(), client.OptionTimeout(timeout))
			if errDel != nil {
				c.LogDebug("Deleting resources", "error", errDel)
			}
		}

		// Start fresh after recreate
		c.ResetResources()
	}

	stack := client.NewStack(c)
	toStackOpts := []project.ToStackOption{}
	toStackOpts = append(toStackOpts, project.ToStackStorageVolumes())
	if len(params.services) > 0 {
		toStackOpts = append(toStackOpts, project.ToStackOnlyServices(params.services))
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
	ensureOpts := []client.Option{client.OptionCreate()}
	if pull == "always" {
		ensureOpts = append(ensureOpts, client.OptionPull())
	}
	if params.build != client.BuildAuto {
		ensureOpts = append(ensureOpts, client.OptionBuild(params.build))
	}

	err = stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure, ensureOpts...)
	if err != nil {
		c.LogError("Ensuring resources", "error", err)
		return errLogged.Wrap(err)
	}

	// Start
	if params.start {
		startOpts := []client.Option{client.OptionTimeout(timeout)}
		if params.dependencyTimeout > 0 {
			startOpts = append(startOpts, client.OptionDependencyTimeout(params.dependencyTimeout))
		}
		if err := stack.ForAction(client.ActionStart).Run(ctx, client.ActionStart, startOpts...); err != nil {
			c.LogError("Starting resources", "error", err)
			return errLogged.Wrap(err)
		}
	}

	return nil
}
