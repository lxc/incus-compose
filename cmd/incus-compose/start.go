package main

import (
	"context"
	"errors"

	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/project"
)

var startCommand = &cli.Command{
	Name:      "start",
	Usage:     "Start stopped services",
	Category:  "compose",
	ArgsUsage: "[SERVICE...]",
	Flags: []cli.Flag{
		&cli.IntFlag{
			Name:  "timeout",
			Usage: "Timeout in seconds for starting",
			Value: 10,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		timeout := cmd.Int("timeout")

		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return err
		}

		c, err := globalClient.EnsureProject(p.Name)
		if err != nil {
			globalClient.LogError("Getting the incus project", "error", err)
			return errLogged
		}
		defer func() { _ = c.Done() }()

		// Register the DNS Watcher
		if err := c.RegisterDNSWatcher(); err != nil {
			globalClient.LogError("Registering the DNS watcher", "project", p.Name, "error", err)
			return errLogged.Wrap(err)
		}

		if err := c.Open(); err != nil {
			globalClient.LogError("Opening the project client", "error", err)
			return errLogged.Wrap(err)
		}

		stack := client.NewStack(c)
		err = p.ToStack(c, stack, project.ToStackOnlyServices(cmd.Args().Slice()))
		if err != nil {
			c.LogError("Adding the project to a stack", "error", err)
			return errLogged
		}

		var errs error
		if err := stack.ForAction(client.ActionEnsure).Run(client.ActionEnsure); err != nil {
			c.LogError("Getting resources", "error", err)
			errs = errors.Join(errs, err)
		}

		if err := stack.ForAction(client.ActionStart).Run(client.ActionStart, client.OptionTimeout(timeout)); err != nil {
			c.LogError("Starting resources", "error", err)
			errs = errors.Join(errs, err)
		}

		if errs != nil {
			return errLogged.Wrap(errs)
		}

		c.LogDebug("All done")
		return nil
	},
}
