package main

import (
	"context"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

func newDownCommand() *cli.Command {
	return &cli.Command{
		Name:      "down",
		Usage:     "Stop and remove containers",
		Category:  "compose",
		ArgsUsage: "[SERVICE...]",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name: "project",
				// The alias volumes is for docker-compose compatibility
				Aliases: []string{"volumes"},
				Usage:   "Remove the project",
			},
			&cli.StringFlag{
				Name:  "rmi",
				Usage: `Remove images used by services. "local" for known images.`,
			},
			&cli.BoolFlag{
				Name:  "images",
				Usage: `Remove known images from the project.`,
			},
			&cli.DurationFlag{
				Name:  "timeout",
				Usage: "Timeout for stopping",
				Value: 10 * time.Second,
			},
			&cli.BoolFlag{
				Name:  "no-deps",
				Usage: "Don't stop linked services",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			globalClient, err := clientFromContext(ctx)
			if err != nil {
				return err
			}
			if err := globalClient.Connect(); err != nil {
				return err
			}

			p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
			if err != nil {
				globalClient.LogError("Configuring the project", "error", err)
				return errLogged.Wrap(err)
			}

			// Get the per Project client early, gives early errors if the project does not exists
			c, err := globalClient.EnsureProject(p.Name)
			if err != nil {
				globalClient.LogWarn("Getting the incus project", "project", p.Name, "error", err)
				return nil
			}
			defer func() { _ = c.Done() }()

			// Register the DNS Watcher
			if err := c.RegisterDNSWatcher(); err != nil {
				globalClient.LogError("Registering the DNS watcher", "project", p.Name, "error", err)
				return errLogged.Wrap(err)
			}

			if err := c.Open(); err != nil {
				globalClient.LogError("Opening the project client", "project", p.Name, "error", err)
				return errLogged.Wrap(err)
			}

			finish := startProgress(globalClient, c, cmd.Root().Writer)

			stackOpts := []project.ToStackOption{project.ToStackOnlyServices(cmd.Args().Slice()), project.ToStackReverse()}
			if !cmd.Bool("no-deps") {
				if cmd.Args().Len() > 0 {
					stackOpts = append(stackOpts, project.ToStackInstancesOnly())
				} else {
					stackOpts = append(stackOpts, project.ToStackWithDeps())
				}
			} else {
				stackOpts = append(stackOpts, project.ToStackInstancesOnly())
			}

			if !cmd.Bool("images") && cmd.String("rmi") != "local" && cmd.String("rmi") != "all" {
				stackOpts = append(stackOpts, project.ToStackNoImages())
			}

			stack := client.NewStack(c)
			if err := p.ToStack(c, stack, stackOpts...); err != nil {
				c.LogError("Adding the project to a stack", "error", err)
				return errLogged
			}

			if err := stack.Run(ctx, client.ActionEnsure, cmd.Root().Writer, cmd.Root().ErrWriter); err != nil {
				c.LogWarn("Getting resources", "error", err)
			}

			if cmd.Bool("project") {
				c.LogDebug("Deleting the project")
				err := globalClient.DeleteProject(c.Project(), true)
				if err != nil {
					c.LogError("Deleting the project", "error", err)
					return errLogged.Wrap(err)
				}

				return nil
			}

			runOpts := []client.Option{
				client.OptionForce(),
				client.OptionTimeout(cmd.Duration("timeout")),
			}

			errStop := stack.ForAction(client.ActionStop).Run(ctx, client.ActionStop, cmd.Root().Writer, cmd.Root().ErrWriter, runOpts...)
			if errStop != nil {
				c.LogWarn("Stopping resources", "error", errStop)
			}

			errDel := stack.ForAction(client.ActionDelete).Run(ctx, client.ActionDelete, cmd.Root().Writer, cmd.Root().ErrWriter, runOpts...)
			if errDel != nil {
				c.LogWarn("Deleting resources", "error", errDel)
			}

			finish(err == nil)
			return nil
		},
	}
}
