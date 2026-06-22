package main

import (
	"context"
	"errors"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

func newStartCommand() *cli.Command {
	return &cli.Command{
		Name:      "start",
		Usage:     "Start stopped services",
		Category:  "compose",
		ArgsUsage: "[SERVICE...]",
		Flags: []cli.Flag{
			&cli.DurationFlag{
				Name:  "timeout",
				Usage: "Timeout for starting",
				Value: 10 * time.Second,
			},
			&cli.BoolFlag{
				Name:  "with-deps",
				Usage: "Also start linked services",
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

			stackOpts := []project.ToStackOption{project.ToStackOnlyServices(cmd.Args().Slice())}
			if withDeps {
				stackOpts = append(stackOpts, project.ToStackWithDeps())
			} else {
				stackOpts = append(stackOpts, project.ToStackInstancesOnly())
			}

			stack := client.NewStack(c)
			err = p.ToStack(c, stack, stackOpts...)
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

			// Without --with-deps the linked services are not in scope, so don't
			// wait on healthd dependency conditions that can never be satisfied.
			startOpts := []client.Option{
				client.OptionTimeout(timeout),
			}
			if !withDeps {
				startOpts = append(startOpts, client.OptionNoHealthd())
			}

			finish := startProgress(globalClient, c, noColor, cmd.Root().Writer)
			errStart := stack.ForAction(client.ActionStart).Run(ctx, client.ActionStart, cmd.Root().Writer, cmd.Root().ErrWriter, startOpts...)
			finish(errStart == nil)
			if errStart != nil {
				c.LogError("Starting resources", "error", errStart)
				errs = errors.Join(errs, errStart)
			}

			if errs != nil {
				return errLogged.Wrap(errs)
			}

			c.LogDebug("All done")
			return nil
		},
	}
}
