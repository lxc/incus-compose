package main

import (
	"context"
	"errors"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

func newRestartCommand() *cli.Command {
	return &cli.Command{
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
			&cli.BoolFlag{
				Name:  "with-deps",
				Usage: "Also restart linked services",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			noColor := noColor(ctx)

			timeout := cmd.Duration("timeout")
			withDeps := cmd.Bool("with-deps")

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

			// Without --with-deps the linked services are not in scope; skip the
			// healthd interaction/dependency waits that target out-of-scope services.
			runOpts := []client.Option{
				client.OptionTimeout(timeout),
			}
			if !withDeps {
				runOpts = append(runOpts, client.OptionNoHealthd())
			}

			stopStackOpts := []project.ToStackOption{project.ToStackOnlyServices(cmd.Args().Slice()), project.ToStackReverse()}
			if withDeps {
				stopStackOpts = append(stopStackOpts, project.ToStackWithDeps())
			} else {
				stopStackOpts = append(stopStackOpts, project.ToStackInstancesOnly())
			}

			stack := client.NewStack(c, client.StackWorkers(cmd.Root().Int("workers")))
			err = p.ToStack(c, stack, stopStackOpts...)
			if err != nil {
				c.LogError("Adding the project to a stack", "error", err)
				return errLogged
			}

			var errs error
			if err := stack.ForAction(client.ActionEnsure).Run(
				ctx,
				client.ActionEnsure,
				cmd.Root().Writer,
				cmd.Root().ErrWriter,
			); err != nil {
				c.LogError("Getting resources", "error", err)
				errs = errors.Join(errs, err)
			}

			finish := startProgress(globalClient, c, noColor, cmd.Root().Writer)

			errStop := stack.ForAction(client.ActionStop).Run(ctx, client.ActionStop, cmd.Root().Writer, cmd.Root().ErrWriter, append([]client.Option{client.OptionForce()}, runOpts...)...)
			if errStop != nil {
				c.LogWarn("Stopping resources", "error", errStop)
				errs = errors.Join(errs, errStop)
			}

			// Start fresh after stop
			c.ResetResources()

			startStackOpts := []project.ToStackOption{project.ToStackOnlyServices(cmd.Args().Slice())} // No reverse here
			if withDeps {
				startStackOpts = append(startStackOpts, project.ToStackWithDeps())
			} else {
				startStackOpts = append(startStackOpts, project.ToStackInstancesOnly())
			}

			stack = client.NewStack(c, client.StackWorkers(cmd.Root().Int("workers")))
			err = p.ToStack(c, stack, startStackOpts...)
			if err != nil {
				c.LogError("Adding the project to a stack", "error", err)
				errs = errors.Join(errs, errStop)
			}

			if err := stack.ForAction(client.ActionEnsure).Run(
				ctx,
				client.ActionEnsure,
				cmd.Root().Writer,
				cmd.Root().ErrWriter,
			); err != nil {
				c.LogError("Getting resources", "error", err)
				errs = errors.Join(errs, err)
			}

			errStart := stack.ForAction(client.ActionStart).Run(ctx, client.ActionStart, cmd.Root().Writer, cmd.Root().ErrWriter, runOpts...)
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
}
