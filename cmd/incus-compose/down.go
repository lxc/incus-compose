package main

import (
	"context"
	"errors"
	"time"

	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/project"
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
			&cli.DurationFlag{
				Name:  "timeout",
				Usage: "Timeout for stopping",
				Value: 10 * time.Second,
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

			stack := client.NewStack(c)
			if err := p.ToStack(c, stack, stackOpts...); err != nil {
				c.LogError("Adding the project to a stack", "error", err)
				return errLogged
			}

			var errs error

			if err := stack.Run(ctx, client.ActionEnsure); err != nil {
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

			errStop := stack.ForAction(client.ActionStop).Run(ctx, client.ActionStop, client.OptionForce(), client.OptionTimeout(cmd.Duration("timeout")))
			if errStop != nil {
				c.LogWarn("Stopping resources", "error", errStop)
				errs = errors.Join(errs, errStop)
			}

			errDel := stack.ForAction(client.ActionDelete).Run(ctx, client.ActionDelete, client.OptionForce(), client.OptionTimeout(cmd.Duration("timeout")))
			if errDel != nil {
				c.LogWarn("Deleting resources", "error", errDel)
				errs = errors.Join(errs, errDel)
			}

			if errs != nil {
				return errLogged.Wrap(errs)
			}

			finish(err == nil)
			return nil
		},
	}
}
