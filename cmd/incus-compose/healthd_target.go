package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
	"github.com/lxc/incus-compose/shared"
)

// errNoHealthd is what a healthd sub-command reports when there is no daemon to
// act on. Run outside a project there is nothing else it could mean.
var errNoHealthd = errors.New(
	"no ic-healthd is running; bring one up with `incus-compose healthd up`")

// healthdTarget is the daemon a healthd sub-command acts on.
type healthdTarget struct {
	// project is nil when there was no compose file, in which case the command
	// acts on the shared daemon alone.
	project *project.Project

	// client owns the daemon: the compose project's own for project scope, the
	// default project's for the shared one.
	client   *client.Client
	instance *client.Instance
	scope    string
}

// healthdProject loads the compose project a healthd command was run in. No
// compose file is not an error: the command then acts on the shared daemon.
func healthdProject(ctx context.Context, cmd *cli.Command) (*project.Project, error) {
	p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
	if errors.Is(err, project.ErrNoComposeFile) {
		return nil, nil
	}

	return p, err
}

// resolveHealthdTarget finds the daemon to act on and opens the client owning
// it. The returned func closes that client again.
//
// With a compose file it follows that project's scope. Without one it goes
// straight to the shared daemon in the default project, and errors when there
// is none rather than guessing at a project.
func resolveHealthdTarget(ctx context.Context, cmd *cli.Command, gc *client.GlobalClient) (*healthdTarget, func(), error) {
	p, err := healthdProject(ctx, cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("configuring the project: %w", err)
	}

	target := &healthdTarget{project: p, scope: shared.HealthScopeGlobal}

	if p != nil {
		c, err := gc.EnsureProject(p.Name)
		if err != nil {
			return nil, nil, fmt.Errorf("getting the incus project: %w", err)
		}

		target.client, target.scope, err = healthdClient(p, c)
		if errors.Is(err, client.ErrNotFound) {
			return nil, nil, errNoHealthd
		}
		if err != nil {
			return nil, nil, fmt.Errorf("resolving the healthd scope: %w", err)
		}
	} else {
		target.client, err = gc.EnsureProject(globalProject)
		if errors.Is(err, client.ErrNotFound) {
			return nil, nil, errNoHealthd
		}
		if err != nil {
			return nil, nil, fmt.Errorf("getting the %s project: %w", globalProject, err)
		}
	}

	if err := target.client.Open(); err != nil {
		return nil, nil, fmt.Errorf("opening the project client: %w", err)
	}

	done := func() { target.client.WarnError(target.client.Done, "Failure during Client.Done()") }

	name, err := target.client.FindHealthd()
	if err != nil {
		done()

		return nil, nil, errNoHealthd
	}

	res, err := target.client.Resource(client.KindInstance, name, &client.InstanceConfig{})
	if err != nil {
		done()

		return nil, nil, err
	}

	inst, ok := res.(*client.Instance)
	if !ok {
		done()

		return nil, nil, errors.New("unexpected resource type for healthd")
	}

	target.instance = inst

	return target, done, nil
}
