package main

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"

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
		&cli.IntFlag{
			Name:  "timeout",
			Usage: "Timeout in seconds for stopping/starting",
			Value: 10,
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

		if !cmd.Bool("no-healthd") && healthdInUseByProject(p) {
			hparams := healthdParams{
				projectName: p.Name,
				binary:      cmd.String("healthd-binary"),
				image:       resolveHealthdImage(cmd.String("healthd-image")),
				pull:        cmd.String("pull"),
				reCreate:    cmd.Bool("recreate"),
				network:     cmd.String("healthd-network"),
				timeout:     cmd.Int("timeout"),
			}

			inst, resources, err := healthdGetResources(c, hparams)
			if err != nil {
				globalClient.LogError("Creating healthd resources", "error", err)
				return errLogged.Wrap(err)
			}

			if err := healthdUp(c, inst, resources, hparams); err != nil {
				globalClient.LogError("Starting healthd", "error", err)
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
			reCreate: cmd.Bool("recreate"),
			start:    !cmd.Bool("no-start"),
			services: cmd.Args().Slice(),
			pull:     cmd.String("pull"),
			build:    build,
			timeout:  int(cmd.Int("timeout")),
			scale:    parseScale(cmd.StringSlice("scale")),
		}
		if err := runUp(globalClient, c, p, params); err != nil {
			_ = c.Done()
			return err
		}
		_ = c.Done()
		if !cmd.Bool("detach") {
			var out io.Writer
			if f, ok := cmd.Root().Writer.(*os.File); ok {
				out = colorable.NewColorable(f)
			} else {
				out = cmd.Root().Writer
			}
			return runLogs(globalClient, c, p, params.services, true, out)
		}
		return nil
	},
}

// upParams holds the parsed arguments for an up run.
// services is the raw service filter (empty means all services).
type upParams struct {
	services  []string
	start     bool
	reCreate  bool
	noVolumes bool
	pull      string
	build     client.BuildMode
	timeout   int
	scale     map[string]int
}

func upMakeStack(params upParams, p *project.Project, c *client.Client) (*client.Stack, error) {
	stack := client.NewStack(c)

	toStackOpts := []project.ToStackOption{}
	if !params.noVolumes {
		toStackOpts = append(toStackOpts, project.ToStackStorageVolumes())
	}
	if len(params.services) > 0 {
		toStackOpts = append(toStackOpts, project.ToStackOnlyServices(params.services))
	}
	if len(params.scale) > 0 {
		toStackOpts = append(toStackOpts, project.ToStackScale(params.scale))
	}
	err := p.ToStack(c, stack, toStackOpts...)
	if err != nil {
		c.LogError("Adding the project to a stack", "error", err)
		return nil, errLogged.Wrap(err)
	}

	return stack, nil
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
func runUp(globalClient *client.GlobalClient, c *client.Client, p *project.Project, params upParams) error {
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
		params.noVolumes = true
		stack, err := upMakeStack(params, p, c)
		if err != nil {
			c.LogError("Creating the stack in reCreate", "error", err)
			return errLogged.Wrap(err)
		}
		params.noVolumes = false

		c.LogDebug("Ensure", "resources", stack.All())

		// Ensure without create for "recreate"
		if err := stack.ForAction(client.ActionEnsure).Run(client.ActionEnsure); err != nil {
			c.LogDebug("Ensuring for reCreate", "error", err)
		} else {
			// Stop
			if err := stack.ForAction(client.ActionStop).Run(client.ActionStop, client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
				c.LogDebug("Stopping resources", "error", err)
			}

			// Delete
			deleteStack := stack.ForAction(client.ActionDelete)
			c.LogDebug("Recreate delete", "resources", deleteStack.All())
			if err := deleteStack.Run(client.ActionDelete, client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
				c.LogDebug("Deleting resources", "error", err)
			}
		}
	}

	stack, err := upMakeStack(params, p, c)
	if err != nil {
		return err
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
	if err := stack.ForAction(client.ActionEnsure).Run(client.ActionEnsure, ensureOpts...); err != nil {
		c.LogError("Creating resources", "error", err)
		return errLogged.Wrap(err)
	}

	// Start
	if params.start {
		if err := stack.ForAction(client.ActionStart).Run(client.ActionStart, client.OptionTimeout(timeout)); err != nil {
			c.LogError("Starting resources", "error", err)
			return errLogged.Wrap(err)
		}
	}

	c.LogDebug("All done")
	return nil
}
