package main

import (
	"context"
	"errors"
	"maps"

	incusClient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/cliconfig"
	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incuscompose/client"
	"gitlab.com/r3j0/incuscompose/project"
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
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		reCreate := cmd.Bool("recreate")
		timeout := cmd.Int("timeout")

		start := !cmd.Bool("no-start")

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

		conf, err := cliconfig.LoadConfig(cliconfig.DefaultConfig().ConfigDir)
		if err != nil {
			globalClient.LogError("Failed read the incus config", "error", err)
			return errLogged.Wrap(err)
		}

		services := cmd.Args().Slice()
		if len(services) == 0 {
			services = make([]string, 0, len(p.Services))
			for n := range maps.Values(p.Services) {
				services = append(services, n.Name)
			}
		}

		var rErr error
		stack := client.NewStack(c)

		imageServers := make(map[string]incusClient.ImageServer)
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
			r, err := c.Resource(client.KindImage, cSv.Image, &client.ImageConfig{})
			if err != nil {
				c.LogError("Getting image", "service", cSv.Name, "image", cSv.Image, "error", err)
				rErr = errors.Join(rErr, errLogged.Wrap(err))
				continue
			}

			image, ok := r.(*client.Image)
			if !ok {
				err = client.ErrUnknown.WithResource(r)
				c.LogError("Getting an image", err)
				rErr = errors.Join(rErr, err)
				continue
			}

			is, ok := imageServers[image.Config.Remote]
			if !ok {
				var err error
				is, err = conf.GetImageServer(image.Config.Remote)
				if err != nil {
					c.LogError("Getting an image server", "service", cSv.Name, "image", cSv.Image, "remote", image.Config.Remote, "error", err)
					rErr = errors.Join(rErr, errLogged.Wrap(err))
					continue
				}
			}

			image.Config.Source = is
			stack.Add(image)
		}
		if rErr != nil {
			return rErr
		}

		err = p.ToStack(c, stack, project.ToStackOnlyServices(cmd.Args().Slice()))
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
			if err := stack.ForAction(client.ActionStop).Run(client.ActionStop, client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
				c.LogDebug("Stopping resources", "error", err)
			}

			if err := stack.ForAction(client.ActionDelete).Run(client.ActionDelete, client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
				c.LogDebug("Deleting resources", "error", err)
			}
		}

		if err := stack.Run(client.ActionEnsure, client.OptionCreate()); err != nil {
			c.LogError("Creating resources", "error", err)
			return errLogged.Wrap(err)
		}

		if start {
			if err := stack.ForAction(client.ActionStart).Run(client.ActionStart, client.OptionTimeout(timeout)); err != nil {
				c.LogError("Starting resources", "error", err)
				return errLogged.Wrap(err)
			}
		}

		c.LogDebug("All done")
		return nil
	},
}
