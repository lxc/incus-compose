package main

import (
	"fmt"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
	"github.com/lxc/incus-compose/shared"
)

// resolveHealthdScope returns the scope for the project, first match wins, so a
// project keeps the scope it carries until that key itself is changed.
func resolveHealthdScope(projectConfig map[string]string, cliScope, composeScope string) (string, error) {
	sources := []struct {
		where string
		value string
	}{
		{"the Incus project's " + shared.HealthScopeKey, projectConfig[shared.HealthScopeKey]},
		{"--healthd-scope", cliScope},
		{"x-incus-compose.healthd.scope", composeScope},
	}

	for _, source := range sources {
		switch source.value {
		case "":
		case shared.HealthScopeProject, shared.HealthScopeGlobal:
			return source.value, nil
		default:
			return "", fmt.Errorf("%s is %q, must be %q or %q",
				source.where, source.value, shared.HealthScopeProject, shared.HealthScopeGlobal)
		}
	}

	return shared.HealthScopeGlobal, nil
}

// healthdClient returns the scope p carries and the client of the project its
// daemon lives in. Only the stored scope counts: the flag and the compose file
// say what the next `up` will do, not what watches p now.
func healthdClient(p *project.Project, c *client.Client) (*client.Client, string, error) {
	projectConfig, err := c.Global().ProjectConfig(p.Name)
	if err != nil {
		return nil, "", err
	}

	scope := projectConfig[shared.HealthScopeKey]
	if scope != shared.HealthScopeGlobal {
		return c, scope, nil
	}

	targetProject := systemProject
	daemonProject := projectConfig[shared.HealthProjectKey]
	if daemonProject != "" {
		targetProject = daemonProject
	}

	hc, err := c.Global().EnsureProject(targetProject)
	if err != nil {
		return nil, "", fmt.Errorf("getting the %s project: %w", targetProject, err)
	}

	return hc, scope, nil
}
