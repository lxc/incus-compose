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
			noColor := noColor(ctx)

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

			finish := func(success bool) {}
			if !cmd.Root().Bool("debug") {
				finish = startProgress(globalClient, c, noColor, cmd.Root().Writer)
			}

			resources, err := p.Resources(c, project.ResourcesReverse())
			if err != nil {
				c.LogError("Getting project resources in reCreate", "error", err)
				return errLogged.Wrap(err)
			}

			args := filterResourcesArgs{
				OnlyServices:     cmd.Args().Slice(),
				WithDependencies: !cmd.Bool("no-deps"),
				ExcludeKinds:     []client.Kind{client.KindNetwork},
			}

			if !cmd.Bool("images") && cmd.String("rmi") != "local" && cmd.String("rmi") != "all" {
				args.ExcludeKinds = append(args.ExcludeKinds, client.KindImage)
			}

			myResources := filterResources(p, resources, args)

			stack := client.NewStack(c, client.StackSortDescending(), client.StackWorkers(cmd.Root().Int("workers")))
			stack.Add(flattenResources(myResources)...)

			if healthdInUseByProject(globalClient, p) {
				h, err := healthdResolve(c)
				if err == nil {
					stack.Add(h)
				}
			}

			if err := stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure, cmd.Root().Writer, cmd.Root().ErrWriter); err != nil {
				c.LogWarn("Getting resources", "error", err)
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

			if cmd.Bool("project") {
				c.LogDebug("Deleting the project")
				err := globalClient.DeleteProject(c.Project(), true)
				if err != nil {
					c.LogError("Deleting the project", "error", err)
					return errLogged.Wrap(err)
				}
			}

			finish(err == nil)
			return nil
		},
	}
}
