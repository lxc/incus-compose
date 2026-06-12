package main

import (
	"context"

	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/project"
)

func newBuildCommand() *cli.Command {
	return &cli.Command{
		Name:      "build",
		Usage:     "Build or rebuild service images",
		Category:  "compose",
		ArgsUsage: "[SERVICE...]",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "no-cache",
				Usage: "Do not use a cache when building the image",
			},
			&cli.StringFlag{
				Name:  "pull",
				Usage: `Pull image before running ("always"|"missing"|"never"|"policy")`,
				Value: "policy",
			},
			&cli.BoolFlag{
				Name:    "builder",
				Usage:   "Preferred builder, binary name or absolute path. Empty for auto-detect.",
				Sources: cli.EnvVars("INCUS_COMPOSE_BUILDER"),
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
				globalClient.LogError("Loading the project", "error", err)
				return errLogged.Wrap(err)
			}

			c, err := globalClient.EnsureProject(
				p.Name,
				client.EnsureProjectWithCreate(),
				client.EnsureProjectWithConfig(p.ProjectConfig()),
			)
			if err != nil {
				globalClient.LogError("Getting the incus project", "error", err)
				return errLogged.Wrap(err)
			}
			defer func() { _ = c.Done() }()

			if err := c.Open(); err != nil {
				globalClient.LogError("Opening the project client", "error", err)
				return errLogged.Wrap(err)
			}

			// This populates client.resources
			stack := client.NewStack(c)
			err = p.ToStack(c, stack, project.ToStackOnlyServices(cmd.Args().Slice()))
			if err != nil {
				c.LogError("Creating the stack", "error", err)
				return errLogged.Wrap(err)
			}

			// Collect only build-configured images from the compose project.
			noCache := cmd.Bool("no-cache")
			pull := cmd.String("pull")

			stack = client.NewStack(c)
			instances, err := client.ByKind[*client.Instance](
				c.Resources().All(),
				client.KindInstance,
			)
			if err != nil {
				c.LogError("Getting instances", "error", err)
				return errLogged.Wrap(err)
			}

			for _, i := range instances {
				r, err := c.Resource(client.KindImage, i.Config.Image, &client.ImageConfig{})
				if err != nil {
					c.LogError("Getting an image", "error", err)
					return errLogged.Wrap(err)
				}

				if noCache {
					img, ok := r.(*client.Image)
					if !ok {
						continue
					}

					img.Config.Build.NoCache = noCache
				}

				stack.Add(r)
			}

			ensureOpts := []client.Option{client.OptionCreate(), client.OptionBuild(client.BuildInfo{Mode: client.BuildForce, PreferredBuilder: cmd.String("builder")})}
			if pull == "always" {
				ensureOpts = append(ensureOpts, client.OptionPull())
			}

			err = stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure, ensureOpts...)
			if err != nil {
				c.LogError("Ensuring resources", "error", err)
				return errLogged.Wrap(err)
			}

			return nil
		},
	}
}
