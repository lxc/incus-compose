package main

import (
	"context"
	"errors"
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
		&cli.BoolFlag{
			Name:  "no-healthd",
			Usage: "Don't create healthd sidecar for healthchecks",
		},
		&cli.StringFlag{
			Name:    "healthd-image",
			Usage:   `Healthd OCI image to use; {version} is replaced with the incus-compose version`,
			Value:   client.DefaultHealthdImage,
			Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_IMAGE"),
		},
		&cli.StringFlag{
			Name:    "healthd-binary",
			Usage:   "Path to local ic-healthd binary (uses images:alpine/edge instead of OCI image)",
			Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_BINARY"),
		},
		&cli.StringFlag{
			Name:  "pull",
			Usage: `Pull image before running ("always"|"missing"|"never"|"policy")`,
			Value: "policy",
		},
		&cli.StringFlag{
			Name:    "healthd-network",
			Usage:   "Incus bridge for healthd to use (default: auto-detect)",
			Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_NETWORK"),
		},
		&cli.BoolFlag{
			Name:    "detach",
			Aliases: []string{"d"},
			Usage:   "Detached mode: run containers in the background",
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

		// CLI/env takes priority; fall back to compose-file extension.
		healthdNetwork := cmd.String("healthd-network")
		if healthdNetwork == "" {
			n, err := p.HealthdNetworkConfig()
			if err != nil {
				globalClient.LogError("Reading healthd-network from compose file", "error", err)
				return errLogged.Wrap(err)
			}
			healthdNetwork = n
		}

		params := upParams{
			services:       cmd.Args().Slice(),
			reCreate:       cmd.Bool("recreate"),
			start:          !cmd.Bool("no-start"),
			noHealthd:      cmd.Bool("no-healthd"),
			healthdBinary:  cmd.String("healthd-binary"),
			healthdImage:   resolveHealthdImage(cmd.String("healthd-image")),
			healthdNetwork: healthdNetwork,
			pull:           cmd.String("pull"),
			timeout:        int(cmd.Int("timeout")),
			scale:          parseScale(cmd.StringSlice("scale")),
			detach:         cmd.Bool("detach"),
		}
		if err := runUp(globalClient, c, p, params); err != nil {
			_ = c.Done()
			return err
		}
		_ = c.Done()
		if params.start && !params.detach {
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
	services       []string
	reCreate       bool
	start          bool
	noHealthd      bool
	healthdBinary  string
	healthdImage   string
	healthdNetwork string
	pull           string
	timeout        int
	scale          map[string]int
	detach         bool
}

func mkUpStack(params upParams, p *project.Project, globalClient *client.GlobalClient, c *client.Client) (*client.Stack, error) {
	var rErr error
	stack := client.NewStack(c)

	// Use CliConfig from globalClient for automatic image server resolution
	imageConfig := &client.ImageConfig{CliConfig: globalClient.CliConfig()}

	for _, cSv := range p.Services {
		if cSv.Image == "" {
			err := errors.New("Empty image, building is not yet supported")
			c.LogError("Getting image", "service", cSv.Name, "error", err)
			rErr = errors.Join(rErr, errLogged.Wrap(err))
			continue
		}

		c.LogDebug("Getting image", "image", cSv.Image, "service", cSv.Name)
		r, err := c.Resource(client.KindImage, cSv.Image, imageConfig)
		if err != nil {
			c.LogError("Getting image", "service", cSv.Name, "image", cSv.Image, "error", err)
			rErr = errors.Join(rErr, errLogged.Wrap(err))
			continue
		}

		stack.Add(r)
	}
	if rErr != nil {
		return nil, rErr
	}

	if !params.noHealthd && params.start && projectUsesHealthd(p) {
		c.LogDebug("Found healthchecks")
		healthd, img, err := prepareHealthd(globalClient, c, healthdParams{
			binary:   params.healthdBinary,
			image:    params.healthdImage,
			reCreate: params.reCreate,
			network:  params.healthdNetwork,
		})
		if err != nil {
			c.LogError("Preparing healthd", "error", err)
			return nil, errLogged.Wrap(err)
		}
		stack.Add(img)
		stack.Add(healthd)
	}

	toStackOpts := []project.ToStackOption{}
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
	reCreate := params.reCreate
	timeout := params.timeout
	start := params.start
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

	if reCreate {
		stack, err := mkUpStack(params, p, globalClient, c)
		if err != nil {
			c.LogError("Creating the stack in reCreate", "error", err)
			return errLogged.Wrap(err)
		}

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

	stack, err := mkUpStack(params, p, globalClient, c)
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
	if err := stack.ForAction(client.ActionEnsure).Run(client.ActionEnsure, ensureOpts...); err != nil {
		c.LogError("Creating resources", "error", err)
		return errLogged.Wrap(err)
	}

	if start {
		// Start
		if err := stack.ForAction(client.ActionStart).Run(client.ActionStart, client.OptionTimeout(timeout)); err != nil {
			c.LogError("Starting resources", "error", err)
			return errLogged.Wrap(err)
		}
	}

	c.LogDebug("All done")
	return nil
}
