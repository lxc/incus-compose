package main

import (
	"context"
	"errors"
	"time"

	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/project"
)

func newStopCommand() *cli.Command {
	return &cli.Command{
		Name:      "stop",
		Usage:     "Stop running services",
		Category:  "compose",
		ArgsUsage: "[SERVICE...]",
		Flags: []cli.Flag{
			&cli.DurationFlag{
				Name:  "timeout",
				Usage: "Timeout for stopping",
				Value: 10 * time.Second,
			},
			&cli.BoolFlag{
				Name:  "with-deps",
				Usage: "Also stop linked services",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
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

			stackOpts := []project.ToStackOption{project.ToStackOnlyServices(cmd.Args().Slice()), project.ToStackReverse()}
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

			// Without --with-deps the linked services are not in scope; skip the
			// healthd interaction that targets out-of-scope dependencies.
			stopOpts := []client.Option{
				client.OptionForce(),
				client.OptionTimeout(timeout),
			}
			if !withDeps {
				stopOpts = append(stopOpts, client.OptionNoHealthd())
			}

			finish := startProgress(globalClient, c, cmd.Root().Writer)
			errStop := stack.ForAction(client.ActionStop).Run(ctx, client.ActionStop, cmd.Root().Writer, cmd.Root().ErrWriter, stopOpts...)
			finish(errStop == nil)
			if errStop != nil {
				c.LogWarn("Stopping resources", "error", errStop)
				errs = errors.Join(errs, errStop)
			}

			if errs != nil {
				return errLogged.Wrap(errs)
			}

			c.LogDebug("All done")
			return nil
		},
	}
}
