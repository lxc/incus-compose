package main

import (
	"context"
	"errors"

	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/project"
)

var downCommand = &cli.Command{
	Name:      "down",
	Usage:     "Stop and remove containers",
	Category:  "compose",
	ArgsUsage: "[SERVICE...]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "project",
			Aliases: []string{"volumes"},
			Usage:   "Remove the project",
		},
		&cli.IntFlag{
			Name:  "timeout",
			Usage: "Timeout in seconds for stopping",
			Value: 10,
		},
		&cli.StringFlag{
			Name:  "remote",
			Usage: "Incus remote to use",
			Value: "local",
		},
		&cli.BoolFlag{
			Name:  "no-healthd",
			Usage: "Don't stop/remove healthd sidecar",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		deleteProject := cmd.Bool("project")
		noHealthd := cmd.Bool("no-healthd")

		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return errLogged.Wrap(err)
		}

		// Get the per Project client early, gives early errors if the project does not exists
		c, err := globalClient.EnsureProject(p.Name, false)
		if err != nil {
			globalClient.LogError("Getting the incus project", "error", err)
			return errLogged.Wrap(err)
		}
		defer func() { _ = c.Close() }()

		if err := c.RegisterScaleWatcher(); err != nil {
			globalClient.LogError("Registering the scale watcher", "error", err)
			return errLogged.Wrap(err)
		}

		if err := c.Open(); err != nil {
			globalClient.LogError("Opening the project client", "error", err)
			return errLogged.Wrap(err)
		}

		if deleteProject {
			// Down with a project is easy, isn't it?
			err = globalClient.DeleteProject(c.Project(), true)
			if err != nil {
				globalClient.LogError("Deleting the project", "error", err)
				return errLogged
			}
			return nil
		}

		return runDown(globalClient, c, p, downParams{
			services:  cmd.Args().Slice(),
			timeout:   int(cmd.Int("timeout")),
			noHealthd: noHealthd,
		})
	},
}

// downParams holds the parsed arguments for a down run.
// services is the raw service filter (empty means all services).
type downParams struct {
	services  []string
	timeout   int
	noHealthd bool
}

// runDown stops and removes the instances of a loaded project, along with their
// per-project image copies. Volumes and the image cache are left untouched.
func runDown(globalClient *client.GlobalClient, c *client.Client, p *project.Project, params downParams) error {
	stack := client.NewStack(c)
	if err := p.ToStack(c, stack, project.ToStackOnlyServices(params.services), project.ToStackReverse()); err != nil {
		c.LogError("Adding the project to a stack", "error", err)
		return errLogged
	}

	services := params.services
	if len(services) == 0 {
		services = make([]string, 0, len(p.Services))
		for _, n := range p.Services {
			services = append(services, n.Name)
		}
	}

	// Remove the per-project image copies so the next up re-copies fresh from
	// the (possibly auto-updated) cache. Cache images live in a separate project
	// and are not affected. See issue #29.
	imageConfig := &client.ImageConfig{CliConfig: globalClient.CliConfig()}
	for _, sName := range services {
		cSv, ok := p.Services[sName]
		if !ok || cSv.Image == "" {
			continue
		}

		image, err := c.Resource(client.KindImage, cSv.Image, imageConfig)
		if err != nil {
			c.LogWarn("Getting image", "service", cSv.Name, "image", cSv.Image, "error", err)
			continue
		}
		stack.Add(image)
	}

	if !params.noHealthd {
		for _, sName := range services {
			cSv, ok := p.Services[sName]
			if ok && cSv.HealthCheck != nil {
				healthd, err := c.Healthd("ic-healthd", client.HealthdConfig{})
				if err != nil {
					c.LogError("Getting healthd resource", "error", err)
					return errLogged.Wrap(err)
				}
				stack.Add(healthd)
				c.LogDebug("Added healthd sidecar to stack")
				break
			}
		}
	}

	var errs error
	if err := stack.Run(client.ActionEnsure); err != nil {
		c.LogError("Getting resources", "error", err)
		errs = errors.Join(errs, err)
	}

	if err := stack.ForAction(client.ActionStop).Run(client.ActionStop, client.OptionForce(), client.OptionTimeout(params.timeout)); err != nil {
		c.LogWarn("Stopping resources", "error", err)
		errs = errors.Join(errs, err)
	}

	if err := stack.ForAction(client.ActionDelete).Run(client.ActionDelete, client.OptionForce(), client.OptionTimeout(params.timeout)); err != nil {
		c.LogWarn("Deleting resources", "error", err)
		errs = errors.Join(errs, err)
	}

	if errs != nil {
		return errLogged.Wrap(errs)
	}

	c.LogDebug("All done")
	return nil
}
