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
				Value: 1 * time.Minute,
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

			finish := func(success bool) {}
			if !cmd.Root().Bool("debug") {
				finish = startProgress(globalClient, c, noColor, cmd.Root().Writer)
			}

			resources, err := p.Resources(c)
			if err != nil {
				c.LogError("Getting project resources in reCreate", "error", err)
				return errLogged.Wrap(err)
			}

			order, err := p.ServiceOrder(false)
			if err != nil {
				c.LogError("Getting the service dependency order", "error", err)
				return errLogged.Wrap(err)
			}

			args := filterResourcesArgs{
				OnlyServices:     cmd.Args().Slice(),
				WithDependencies: !cmd.Bool("no-deps"),
				ExcludeKinds:     []client.Kind{client.KindImage, client.KindNetwork, client.KindStorageVolume},
			}
			myResources := filterResources(p, resources, args)

			stack := client.NewStack(c, client.StackWorkers(cmd.Root().Int("workers")))
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

			// Without --with-deps the linked services are not in scope, so don't
			// wait on healthd dependency conditions that can never be satisfied.
			startOpts := []client.Option{
				client.OptionTimeout(timeout),
			}

			_, err = healthdResolve(c)
			if err != nil || (!withDeps && cmd.Args().Len() > 0) {
				startOpts = append(startOpts, client.OptionNoHealthd())
			}

			filter := func(r client.Resource) bool { return r.IsEnsured() }
			errStart := stack.ForActionF(client.ActionStart, filter).Run(ctx, client.ActionStart, cmd.Root().Writer, cmd.Root().ErrWriter, startOpts...)
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
