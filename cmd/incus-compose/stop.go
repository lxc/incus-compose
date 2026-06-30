package main

import (
	"context"
	"errors"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
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

			finish := func(success bool) {}
			if !cmd.Root().Bool("debug") {
				finish = startProgress(globalClient, c, noColor, cmd.Root().Writer)
			}

			resources, err := p.Resources(c)
			if err != nil {
				c.LogError("Getting project resources in reCreate", "error", err)
				return errLogged.Wrap(err)
			}

			order, err := p.ServiceOrder(true)
			if err != nil {
				c.LogError("Getting the service dependency order", "error", err)
				return errLogged.Wrap(err)
			}

			args := filterResourcesArgs{
				OnlyServices:     cmd.Args().Slice(),
				WithDependencies: !cmd.Bool("no-deps"),
				Reverse:          true,
				ExcludeKinds:     []client.Kind{client.KindImage, client.KindNetwork, client.KindStorageVolume},
			}
			myResources := filterResources(p, resources, args)

			stack := client.NewStack(c, client.StackSortDescending(), client.StackWorkers(cmd.Root().Int("workers")))
			stack.AddOrdered(order, myResources)

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

			_, err = healthdResolve(c)
			if err != nil || (!withDeps && cmd.Args().Len() > 0) {
				stopOpts = append(stopOpts, client.OptionNoHealthd())
			}

			filter := func(r client.Resource) bool { return r.IsEnsured() }
			errStop := stack.ForActionF(client.ActionStop, filter).Run(ctx, client.ActionStop, cmd.Root().Writer, cmd.Root().ErrWriter, stopOpts...)
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
