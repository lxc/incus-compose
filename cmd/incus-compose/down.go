package main

import (
	"context"
	"errors"
	"fmt"

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
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		deleteProject := cmd.Bool("project")

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
		c, err := globalClient.EnsureProject(p.Name)
		if err != nil {
			globalClient.LogError("Getting the incus project", "project", p.Name, "error", err)
			return errLogged.Wrap(err)
		}
		defer func() { _ = c.Done() }()

		if deleteProject {
			networks, err := projectNetworks(c, p)
			if err != nil {
				c.LogWarn("Getting project networks", "project", p.Name, "error", err)
			}

			c.LogDebug("Deleting the project")
			err = globalClient.DeleteProject(c.Project(), true)
			if err != nil {
				globalClient.LogError("Deleting the project", "project", p.Name, "error", err)
				return errLogged
			}

			if networks != nil {
				if err := deleteProjectNetworks(c, networks); err != nil {
					globalClient.LogError("Deleting project networks", "project", p.Name, "error", err)
					return errLogged.Wrap(err)
				}
			}

			return nil
		}

		// Register the DNS Watcher
		if err := c.RegisterDNSWatcher(); err != nil {
			globalClient.LogError("Registering the DNS watcher", "project", p.Name, "error", err)
			return errLogged.Wrap(err)
		}

		if err := c.Open(); err != nil {
			globalClient.LogError("Opening the project client", "project", p.Name, "error", err)
			return errLogged.Wrap(err)
		}

		return runDown(globalClient, c, p, downParams{
			services: cmd.Args().Slice(),
			timeout:  int(cmd.Int("timeout")),
		})
	},
}

func projectNetworks(c *client.Client, p *project.Project) ([]*client.Network, error) {
	stack := client.NewStack(c)
	if err := p.ToStack(c, stack, project.ToStackReverse()); err != nil {
		c.LogError("Adding the project to a stack", "error", err)
		return nil, err
	}

	networkStack := client.NewStack(c)
	networks := []*client.Network{}
	for _, r := range stack.All() {
		network, ok := r.(*client.Network)
		if ok && !network.Config.External {
			networkStack.Add(network)
			networks = append(networks, network)
		}
	}

	if err := networkStack.Run(client.ActionEnsure); err != nil {
		return nil, err
	}

	return networks, nil
}

func deleteProjectNetworks(c *client.Client, networks []*client.Network) error {
	var errs error
	for _, network := range networks {
		if network.Config.External {
			continue
		}

		if err := c.GlobalConnection().DeleteNetwork(network.IncusName()); err != nil {
			errs = errors.Join(errs, fmt.Errorf("deleting network %q: %w", network.Name(), err))
		}
	}
	return errs
}

// downParams holds the parsed arguments for a down run.
// services is the raw service filter (empty means all services).
type downParams struct {
	services []string
	images   bool
	timeout  int
}

// runDown stops and removes the instances of a loaded project, along with their
// per-project image copies. Volumes and the image cache are left untouched.
func runDown(globalClient *client.GlobalClient, c *client.Client, p *project.Project, params downParams) error {
	stackOpts := []project.ToStackOption{project.ToStackOnlyServices(params.services)}

	if !params.images {
		stackOpts = append(stackOpts, project.ToStackNoImages())
	}

	stack := client.NewStack(c)
	if err := p.ToStack(c, stack, stackOpts...); err != nil {
		c.LogError("Adding the project to a stack", "error", err)
		return errLogged
	}

	if projectUsesHealthd(p) {
		if name, err := c.FindHealthdName(); err != nil {
			c.LogError("Finding healthd", "error", err)
			return errLogged.Wrap(err)
		} else if name != "" {
			healthd, err := c.Resource(client.KindHealthd, name, &client.HealthdConfig{})
			if err != nil {
				c.LogError("Getting healthd resource", "error", err)
				return errLogged.Wrap(err)
			}
			stack.Add(healthd)
			c.LogDebug("Added healthd sidecar to stack")
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
