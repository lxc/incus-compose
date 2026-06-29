package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
)

func cleanLines(t *testing.T, in string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(in), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// TestSlowUpNoDeps verifies `up <service> --no-deps` starts only the named service
// and does not wait on its (unstarted) service_healthy dependencies.
func TestSlowUpNoDeps(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	ctx := context.Background()
	pn := t.Name()

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach", "--no-deps", "nginx")
	require.NoError(t, err)

	_, _, err = runCommand(t, ctx, pn, "-f", compose, "ps", "--quiet")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)
	require.NoError(t, ensureInstance(t, ctx, c, "nginx-1"))
	require.Error(t, ensureInstance(t, ctx, c, "backend1-1"))
	require.Error(t, ensureInstance(t, ctx, c, "backend2-1"))
}

// TestSlowUpDeps verifies `up <service>` (default) follows depends_on and starts the
// linked services too.
func TestSlowUpDeps(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	ctx := context.Background()
	pn := t.Name()

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach", "nginx")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)
	require.NoError(t, ensureInstance(t, ctx, c, "nginx-1"))
	require.NoError(t, ensureInstance(t, ctx, c, "backend1-1"))
	require.NoError(t, ensureInstance(t, ctx, c, "backend2-1"))
}

// TestSlowDownNoDeps verifies `down <service> --no-deps` removes only the named
// service and leaves its dependants running.
func TestSlowDownNoDeps(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	ctx := context.Background()
	pn := t.Name()

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	_, _, err = runCommand(t, ctx, pn, "-f", compose, "down", "--no-deps", "backend1")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)
	require.NoError(t, ensureInstance(t, ctx, c, "nginx-1"))
	require.NoError(t, ensureInstance(t, ctx, c, "backend2-1"))
	require.Error(t, ensureInstance(t, ctx, c, "backend1-1"))
}

// TestSlowDownDeps verifies `down <service>` (default) follows depends_on in reverse
// and also removes the services that depend on the named one.
func TestSlowDownDeps(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	ctx := context.Background()
	pn := t.Name()

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	_, _, err = runCommand(t, ctx, pn, "-f", compose, "down", "backend1")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)
	require.NoError(t, ensureInstance(t, ctx, c, "backend2-1"))
	require.NoError(t, ensureInstance(t, ctx, c, "nginx-1"))
	require.Error(t, ensureInstance(t, ctx, c, "backend1-1"))
}

// TestSlowPsDeps verifies that `ps <service> --with-deps` includes the linked
// services as real services, whereas the default scopes to the named service
// (other running instances show up only as <orphan>).
func TestSlowPsDeps(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	ctx := context.Background()
	pn := t.Name()

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	stdoutNoDeps, _, err := runCommand(t, ctx, pn, "-f", compose, "ps", "--services", "nginx")
	require.NoError(t, err)

	noDeps := cleanLines(t, stdoutNoDeps.String())
	require.Contains(t, noDeps, "nginx")
	require.NotContains(t, noDeps, "backend1")
	require.NotContains(t, noDeps, "backend2")

	stdoutDeps, _, err := runCommand(t, ctx, pn, "-f", compose, "ps", "--services", "--with-deps", "nginx")
	require.NoError(t, err)

	withDeps := cleanLines(t, stdoutDeps.String())
	require.Contains(t, withDeps, "nginx")
	require.Contains(t, withDeps, "backend1")
	require.Contains(t, withDeps, "backend2")
}

// TestSlowStartStopRestartLogsWithDeps exercises start/stop/restart/logs with and
// without --with-deps. The default keeps each command scoped to the named
// service (and, crucially, start does not block on out-of-scope healthd
// dependency conditions); --with-deps follows depends_on like up/down.
func TestSlowStartStopRestartLogsWithDeps(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	ctx := context.Background()
	pn := t.Name()

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	// Bring everything up and healthy.
	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	// restart --with-deps is a smoke check while everything is running.
	_, _, err = runCommand(t, ctx, pn, "-f", compose, "restart", "--with-deps", "nginx")
	require.NoError(t, err)

	// logs accepts --with-deps and the default form while running.
	_, _, err = runCommand(t, ctx, pn, "-f", compose, "logs", "--with-deps", "nginx")
	require.NoError(t, err)
	_, _, err = runCommand(t, ctx, pn, "-f", compose, "logs", "nginx")
	require.NoError(t, err)

	// Stop all services (no cascade requested) -> nothing running.
	_, _, err = runCommand(t, ctx, pn, "-f", compose, "stop", "nginx", "backend1", "backend2")
	require.NoError(t, err)
	stdout, _, err := runCommand(t, ctx, pn, "-f", compose, "ps", "--quiet")
	require.NoError(t, err)
	require.Empty(t, cleanLines(t, stdout.String()))

	// start nginx without --with-deps must start only nginx and must not block
	// waiting for the (still stopped) backends to become healthy.
	_, _, err = runCommand(t, ctx, pn, "-f", compose, "start", "nginx")
	require.NoError(t, err)
	stdout, _, err = runCommand(t, ctx, pn, "-f", compose, "ps", "--quiet")
	require.NoError(t, err)
	names := cleanLines(t, stdout.String())
	require.Contains(t, names, "nginx-1")
	require.NotContains(t, names, "backend1-1")
	require.NotContains(t, names, "backend2-1")

	// start nginx --with-deps brings its dependencies up too.
	_, _, err = runCommand(t, ctx, pn, "-f", compose, "start", "--with-deps", "nginx")
	require.NoError(t, err)
	stdout, _, err = runCommand(t, ctx, pn, "-f", compose, "ps", "--quiet")
	require.NoError(t, err)
	names = cleanLines(t, stdout.String())
	require.Contains(t, names, "nginx-1")
	require.Contains(t, names, "backend1-1")
	require.Contains(t, names, "backend2-1")

	// stop backend1 --with-deps also stops its dependant (nginx); backend2 stays.
	_, _, err = runCommand(t, ctx, pn, "-f", compose, "stop", "--with-deps", "backend1")
	require.NoError(t, err)
	stdout, _, err = runCommand(t, ctx, pn, "-f", compose, "ps", "--quiet")
	require.NoError(t, err)
	names = cleanLines(t, stdout.String())
	require.Contains(t, names, "backend2-1")
	require.NotContains(t, names, "nginx-1")
	require.NotContains(t, names, "backend1-1")
}

func TestSlowUpDownGrafana(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/grafana/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up grafana",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list grafana",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSlowDownProjectDeletesNetworks(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	networks := plannedNetworkNames(t, ctx, pn, compose)
	require.NotEmpty(t, networks)

	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
		}
	})

	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)

	for _, name := range networks {
		conn, err := c.Connection()
		require.NoError(t, err)
		_, _, err = conn.GetNetwork(name)
		require.NoError(t, err, "for network %q", name)
	}

	_, _, err = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	require.NoError(t, err)
	cleaned = true

	for _, name := range networks {
		conn, err := c.Connection()
		require.NoError(t, err)
		_, _, err = conn.GetNetwork(name)
		require.Error(t, err, "for network %q", name)
	}
}

func TestSlowUpRecreate(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up simple-nginx",
			args:    []string{"-f", compose, "up", "--detach", "--recreate"},
			wantErr: false,
		},
		{
			name:    "list simple-nginx",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSlowUpUpRecreate(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up simple-nginx",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list1 simple-nginx",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
		{
			name:    "up simple-nginx",
			args:    []string{"-f", compose, "up", "--detach", "--recreate"},
			wantErr: false,
		},
		{
			name:    "list2 simple-nginx",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSlowUpRecreateDown(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up simple-nginx",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list simple-nginx",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
		{
			name:    "recreate simple-nginx",
			args:    []string{"-f", compose, "up", "--detach", "--recreate"},
			wantErr: false,
		},
		{
			name:    "list recreated simple-nginx",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSlowLifecycleSimpleNginx(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "up",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "ps table",
			args: []string{"-f", compose, "ps", "--all"},
		},
		{
			name: "ps json",
			args: []string{"-f", compose, "ps", "--all", "--format", "json"},
		},
		{
			name: "ps quiet",
			args: []string{"-f", compose, "ps", "--all", "--quiet"},
		},
		{
			name: "ps services",
			args: []string{"-f", compose, "ps", "--all", "--services"},
		},
		{
			name: "stop service",
			args: []string{"-f", compose, "stop", "web"},
		},
		{
			name: "ps stopped",
			args: []string{"-f", compose, "ps", "--all"},
		},
		{
			name: "start service",
			args: []string{"-f", compose, "start", "web"},
		},
		{
			name: "exec dry run",
			args: []string{"-f", compose, "exec", "--dry-run", "web", "echo", "hello"},
		},
		// {
		// 	name: "restart service",
		// 	args: []string{"-f", compose, "restart", "web"},
		// },
		{
			name: "logs service",
			args: []string{"-f", compose, "logs", "web"},
		},
		{
			name: "down resources",
			args: []string{"-f", compose, "down"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			require.NoError(t, err)
		})
	}
}

func TestSlowUpDownScale(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/nginx-scale/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up nginx-scale",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "scale nginx-scale",
			args:    []string{"-f", compose, "up", "--detach", "--scale=web=3"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSlowUpDownDownscale(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/nginx-scale/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up nginx-scale",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "downscale nginx-scale",
			args:    []string{"-f", compose, "up", "--detach", "--scale=web=6"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSlowUpDownWithScale(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/nginx-scale/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up nginx-scale",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list nginx-scale",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSlowListSnapshots(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "list_yaml",
			args: []string{"-f", compose, "list", "--format", "yaml"},
		},
		{
			name: "list_json",
			args: []string{"-f", compose, "list", "--format", "json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := runCommand(t, ctx, pn, tt.args...)
			require.NoError(t, err)
			snapshotter.SnapshotT(t, normalizeListOutput(t, stdout))
		})
	}
}

func TestSlowExternalNetwork(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/test-external-network/compose.yaml"

	gc, err := client.NewTestClient(ctx)
	require.NoError(t, err)

	conn, err := gc.Connection()
	require.NoError(t, err)

	_, _, err = conn.GetNetwork("incusbr0")
	if err != nil {
		t.Skipf("No incusbr0: %v", err)
	}

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up test-external-network",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list test-external-network",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSlowUpDownWithIncusOptions(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-incus-options/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-incus-options",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list with-incus-options",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSlowUpDownWithProjectOptions(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-project-options/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-project-options",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list with-project-options",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSlowUpDownWithSecrets(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-secrets/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-secrets",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list with-secrets",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSlowDownImages(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	_, _, err = runCommand(t, ctx, pn, "-f", compose, "down")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)
	r, err := c.Resource(client.KindImage, "docker.io/nginx:alpine", &client.ImageConfig{})
	require.NoError(t, err)
	require.NoError(t, client.RunAction(ctx, r, client.ActionEnsure), "image should survive plain down")

	_, _, err = runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	_, _, err = runCommand(t, ctx, pn, "-f", compose, "down", "--images")
	require.NoError(t, err)

	r, err = c.Resource(client.KindImage, "docker.io/nginx:alpine", &client.ImageConfig{})
	require.NoError(t, err)
	require.Error(t, client.RunAction(ctx, r, client.ActionEnsure), "image should be removed by down --images")
}

func TestSlowUpDownWithVolume(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-volume/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-volume",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list with-volume",
			args:    []string{"-f", compose, "list"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
