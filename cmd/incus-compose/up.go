package main

import (
	"context"
	"errors"
	"strconv"
	"strings"

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
			Name:  "healthd-binary",
			Usage: "Path to local ic-healthd binary (uses images:alpine/edge instead of OCI image)",
		},
		&cli.BoolFlag{
			Name:  "pull",
			Usage: "Refresh cached images from their source registry before creating",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return errLogged.Wrap(err)
		}

		// Get the per Project client early, gives early errors if the project does not exists
		c, err := globalClient.EnsureProject(p.Name, true)
		if err != nil {
			globalClient.LogError("Getting the incus project", "error", err)
			return errLogged.Wrap(err)
		}
		if err := c.RegisterScaleWatcher(); err != nil {
			globalClient.LogError("Registering the scale watcher", "error", err)
			return errLogged.Wrap(err)
		}

		if err := c.Open(); err != nil {
			globalClient.LogError("Opening the project client", "error", err)
			return errLogged.Wrap(err)
		}
		defer func() { _ = c.Close() }()

		return runUp(globalClient, c, p, upParams{
			services:      cmd.Args().Slice(),
			reCreate:      cmd.Bool("recreate"),
			start:         !cmd.Bool("no-start"),
			noHealthd:     cmd.Bool("no-healthd"),
			healthdBinary: cmd.String("healthd-binary"),
			pull:          cmd.Bool("pull"),
			timeout:       int(cmd.Int("timeout")),
			scale:         parseScale(cmd.StringSlice("scale")),
		})
	},
}

// upParams holds the parsed arguments for an up run.
// services is the raw service filter (empty means all services).
type upParams struct {
	services      []string
	reCreate      bool
	start         bool
	noHealthd     bool
	healthdBinary string
	pull          bool
	timeout       int
	scale         map[string]int
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
	noHealthd := params.noHealthd
	healthdBinary := params.healthdBinary
	scaleOverrides := params.scale

	services := params.services
	if len(services) == 0 {
		services = make([]string, 0, len(p.Services))
		for _, n := range p.Services {
			services = append(services, n.Name)
		}
	}

	var rErr error
	stack := client.NewStack(c)

	// Prepare healthd early (before image loop) so we can add its image

	var healthdConfig *client.HealthdConfig
	if !noHealthd && start {
		for _, sName := range services {
			cSv, ok := p.Services[sName]
			if ok && cSv.HealthCheck != nil {
				healthdConfig = &client.HealthdConfig{}
				c.LogDebug("Found healthchecks")
				if healthdBinary != "" {
					healthdConfig.Binary = healthdBinary
					c.LogDebug("Using local healthd binary", "path", healthdBinary)
				}
				break
			}
		}
	}

	// Use CliConfig from globalClient for automatic image server resolution
	imageConfig := &client.ImageConfig{CliConfig: globalClient.CliConfig()}

	for _, sName := range services {
		cSv, ok := p.Services[sName]
		if !ok {
			err := errors.New("Unknown service")
			c.LogError("Getting image", "service", sName, "error", err)
			rErr = errors.Join(rErr, errLogged.Wrap(err))
			continue
		}

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
		return rErr
	}

	// Handle healthd image (same pattern as service images)
	var healthd *client.Healthd
	if healthdConfig != nil {
		var imageName string
		if healthdBinary != "" {
			// Use system container for local binary
			imageName = "images:alpine/edge"
		} else {
			// Use OCI image
			imageName = client.DefaultHealthdImage
		}

		// Setup the request
		r, err := c.Resource(client.KindImage, imageName, imageConfig)
		if err != nil {
			c.LogError("Getting healthd image", "image", imageName, "error", err)
			return errLogged.Wrap(err)
		}

		// Cast the request to image
		healthdImage, ok := r.(*client.Image)
		if !ok {
			err = client.ErrUnknown.WithResource(r)
			c.LogError("Getting healthd image", err)
			return errLogged.Wrap(err)
		}

		// Add the request to images
		stack.Add(healthdImage)

		// Set image on config
		healthdConfig.ImageResource = healthdImage

		healthdName := "ic-healthd"
		healthd, err = c.Healthd(healthdName, *healthdConfig)
		if err != nil {
			c.LogError("Creating healthd resource", "error", err)
			return errLogged.Wrap(err)
		}
		c.LogDebug("Prepared healthd sidecar image", "name", healthdName)
	}

	toStackOpts := []project.ToStackOption{project.ToStackOnlyServices(params.services)}
	if len(scaleOverrides) > 0 {
		toStackOpts = append(toStackOpts, project.ToStackScale(scaleOverrides))
	}
	err := p.ToStack(c, stack, toStackOpts...)
	if err != nil {
		c.LogError("Adding the project to a stack", "error", err)
		return errLogged.Wrap(err)
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

	if reCreate {
		// Ensure without create for "recreate"
		if err := stack.ForAction(client.ActionEnsure).Run(client.ActionEnsure); err != nil {
			c.LogDebug("Stopping resources", "error", err)
		}

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

	// Add healthd after recreate (like images, so it doesn't get deleted during recreate)
	if healthd != nil {
		stack.Add(healthd)
	}

	c.LogDebug("Ensure", "resources", stack.All())

	// Ensure with create. --pull refreshes cached images first.
	ensureOpts := []client.Option{client.OptionCreate()}
	if params.pull {
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
