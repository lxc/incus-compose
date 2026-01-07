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
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		deleteProject := cmd.Bool("project")
		timeout := cmd.Int("timeout")

		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return err
		}

		// Get the per Project client early, gives early errors if the project does not exists
		c, err := globalClient.EnsureProject(p.Name, false)
		if err != nil {
			globalClient.LogError("Getting the incus project", "error", err)
			return errLogged
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

		stack := client.NewStack(c)
		err = p.ToStack(c, stack, project.ToStackOnlyServices(cmd.Args().Slice()), project.ToStackReverse())
		if err != nil {
			c.LogError("Adding the project to a stack", "error", err)
			return errLogged
		}

		// defer func() {
		// 	if c.Errors() != nil {
		// 		c.Logger().ErrorContext(c.Ctx, "Error(s) during up", "error", c.Errors())
		// 		if c.IsDebugging() {
		// 			c.Logger().WarnContext(c.Ctx, "Wont rollback in debug")
		// 		} else {
		// 			err := c.Rollback(0)
		// 			if err != nil {
		// 				c.Logger().ErrorContext(c.Ctx, "During rollback", "error", err)
		// 			}
		// 		}
		// 	}
		// }()
		//

		var errs error
		if err := stack.Run(client.ActionEnsure); err != nil {
			c.LogError("Getting resources", "error", err)
			errs = errors.Join(errs, err)
		}

		if err := stack.ForAction(client.ActionStop).Run(client.ActionStop, client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
			c.LogWarn("Stopping resources", "error", err)
			errs = errors.Join(errs, err)
		}

		if err := stack.ForAction(client.ActionDelete).Run(client.ActionDelete, client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
			c.LogWarn("Deleting resources", "error", err)
			errs = errors.Join(errs, err)
		}

		if errs != nil {
			return errLogged.Wrap(errs)
		}

		c.LogDebug("All done")
		return nil
	},
}
