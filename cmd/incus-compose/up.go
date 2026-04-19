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
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		reCreate := cmd.Bool("recreate")
		timeout := cmd.Int("timeout")
		start := !cmd.Bool("no-start")

		// Parse --scale flags (service=num)
		scaleOverrides := make(map[string]int)
		for _, s := range cmd.StringSlice("scale") {
			parts := strings.SplitN(s, "=", 2)
			if len(parts) == 2 {
				if n, err := strconv.Atoi(parts[1]); err == nil {
					scaleOverrides[parts[0]] = n
				}
			}
		}

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

		services := cmd.Args().Slice()
		if len(services) == 0 {
			services = make([]string, 0, len(p.Services))
			for _, n := range p.Services {
				services = append(services, n.Name)
			}
		}

		var rErr error
		stack := client.NewStack(c)

		images := []client.Resource{}

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

			images = append(images, r)
		}
		if rErr != nil {
			return rErr
		}

		toStackOpts := []project.ToStackOption{project.ToStackOnlyServices(cmd.Args().Slice())}
		if len(scaleOverrides) > 0 {
			toStackOpts = append(toStackOpts, project.ToStackScale(scaleOverrides))
		}
		err = p.ToStack(c, stack, toStackOpts...)
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

		// Add images after reCreate
		stack.Add(images...)

		c.LogDebug("Ensure", "resources", stack.All())

		// Ensure with create.
		if err := stack.ForAction(client.ActionEnsure).Run(client.ActionEnsure, client.OptionCreate()); err != nil {
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
	},
}
