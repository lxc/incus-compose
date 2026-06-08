package main

import (
	"context"
	"fmt"
	"slices"

	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/project"
)

var buildCommand = &cli.Command{
	Name:      "build",
	Usage:     "Build or rebuild service images",
	Category:  "compose",
	ArgsUsage: "[SERVICE...]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "no-cache",
			Usage: "Do not use cache when building the image",
		},
		&cli.BoolFlag{
			Name:  "pull",
			Usage: "Always attempt to pull a newer version of the base image",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		globalClient, err := clientFromContext(ctx)
		if err != nil {
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

		filterServices := cmd.Args().Slice()

		// Collect only build-configured images from the compose project.
		noCache := cmd.Bool("no-cache")
		pull := cmd.Bool("pull")

		builtAny := false
		for name, svc := range p.Services {
			if svc.Build == nil {
				continue
			}
			if len(filterServices) > 0 && !slices.Contains(filterServices, name) {
				continue
			}

			imageName := svc.Image
			if imageName == "" {
				imageName = "localhost/" + p.Name + "-" + name
			}

			buildCfg := &client.BuildConfig{
				Context:    svc.Build.Context,
				Dockerfile: svc.Build.Dockerfile,
				NoCache:    noCache || svc.Build.NoCache,
				Pull:       pull || svc.Build.Pull,
			}
			if len(svc.Build.Args) > 0 {
				buildCfg.Args = make(map[string]string, len(svc.Build.Args))
				for k, v := range svc.Build.Args {
					if v != nil {
						buildCfg.Args[k] = *v
					}
				}
			}

			img, err := c.Resource(client.KindImage, imageName, &client.ImageConfig{Build: buildCfg})
			if err != nil {
				c.LogError("Configuring image resource", "service", name, "error", err)
				return errLogged.Wrap(err)
			}

			ensurable, ok := img.(client.EnsureAble)
			if !ok {
				c.LogError("Image resource does not support Ensure", "service", name)
				return errLogged.Wrap(fmt.Errorf("image resource for %q is not ensurable", name))
			}
			if err := ensurable.Ensure(client.OptionCreate(), client.OptionBuild(client.BuildForce)); err != nil {
				c.LogError("Building image", "service", name, "error", err)
				return errLogged.Wrap(err)
			}

			builtAny = true
			_, _ = fmt.Fprintf(cmd.Root().Writer, "Built image for service %q: %s\n", name, imageName)
		}

		if !builtAny {
			if len(filterServices) > 0 {
				_, _ = fmt.Fprintf(cmd.Root().Writer, "No build-configured services matched the filter.\n")
			} else {
				_, _ = fmt.Fprintf(cmd.Root().Writer, "No services have a build: configuration.\n")
			}
		}

		return nil
	},
}
