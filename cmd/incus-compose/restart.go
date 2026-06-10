package main

import (
	"context"
	"errors"
	"time"

	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/project"
)

var restartCommand = &cli.Command{
	Name:      "restart",
	Usage:     "Restart running services",
	Category:  "compose",
	ArgsUsage: "[SERVICE...]",
	Flags: []cli.Flag{
		&cli.DurationFlag{
			Name:  "timeout",
			Usage: "Timeout for stopping and starting",
			Value: 10 * time.Second,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		timeout := cmd.Duration("timeout")

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
		err = p.ToStack(c, stack, project.ToStackOnlyServices(cmd.Args().Slice()), project.ToStackReverse())
		if err != nil {
			c.LogError("Adding the project to a stack", "error", err)
			return errLogged
		}

		var errs error
		if err := stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure); err != nil {
			c.LogError("Getting resources", "error", err)
			errs = errors.Join(errs, err)
		}

		finish := startProgress(globalClient, c, cmd.Root().Writer)

		errStop := stack.ForAction(client.ActionStop).Run(ctx, client.ActionStop, client.OptionForce(), client.OptionTimeout(timeout))
		if errStop != nil {
			c.LogWarn("Stopping resources", "error", errStop)
			errs = errors.Join(errs, errStop)
		}

		// Start fresh after stop
		c.ResetResources()

		stack = client.NewStack(c)
		err = p.ToStack(c, stack, project.ToStackOnlyServices(cmd.Args().Slice())) // No reverse here
		if err != nil {
			c.LogError("Adding the project to a stack", "error", err)
			errs = errors.Join(errs, errStop)
		}

		if err := stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure); err != nil {
			c.LogError("Getting resources", "error", err)
			errs = errors.Join(errs, err)
		}

		errStart := stack.ForAction(client.ActionStart).Run(ctx, client.ActionStart, client.OptionTimeout(timeout))
		if errStart != nil {
			c.LogError("Starting resources", "error", errStart)
			errs = errors.Join(errs, errStart)
		}

		finish(errStop == nil && errStart == nil)

		if errs != nil {
			return errLogged.Wrap(errs)
		}

		c.LogDebug("All done")
		return nil
	},
}
