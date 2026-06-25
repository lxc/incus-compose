package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bradleyjkemp/cupaloy/v2"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

var snapshotter = cupaloy.New(cupaloy.SnapshotSubdirectory(filepath.Join("..", "..", "test", "snapshots", "e2e")))

func skipLocal(t *testing.T) {
	if os.Getenv("INCUS_COMPOSE_TEST_LOCAL") != "" {
		t.Skip("Skipping: env INCUS_COMPOSE_TEST_LOCAL is set, run `just test` for this test")
	}
}

func skipSlow(t *testing.T) {
	if os.Getenv("INCUS_COMPOSE_TEST_SLOW") == "" {
		t.Skip("Skipping: env INCUS_COMPOSE_TEST_SLOW is not set, run `just test-slow` for this test")
	}
}

func skipNotSameHost(t *testing.T, gc *client.GlobalClient) {
	if gc.SameHost() != nil {
		t.Skip("not on the same host")
	}
}

func runCommand(t *testing.T, ctx context.Context, projectName string, args ...string) (*bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()

	projectName = strings.ToLower(strings.ReplaceAll(projectName, "/", "-"))

	mArgs := []string{"incus-compose", "--debug", "--project-name", projectName}
	mArgs = append(mArgs, args...)
	slog.DebugContext(ctx, "Running", "args", mArgs)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := newRootCommand()
	cmd.Writer = stdout
	cmd.ErrWriter = stderr
	err := cmd.Run(ctx, mArgs)

	return stdout, stderr, err
}

// normalizeListOutput removes dynamic content (IP addresses, network hashes) for snapshot comparison.
func normalizeListOutput(t *testing.T, output *bytes.Buffer) string {
	t.Helper()

	ipRegex := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`)
	outStr := ipRegex.ReplaceAllString(output.String(), "")

	return outStr
}

func plannedNetworkNames(t *testing.T, ctx context.Context, projectName, compose string) []string {
	t.Helper()

	projectName = strings.ToLower(strings.ReplaceAll(projectName, "/", "-"))

	proj, err := project.New().Load(ctx, project.LoadFiles([]string{compose}))
	require.NoError(t, err)

	c := client.NewOfflineClient(ctx, projectName)
	stack := client.NewStack(c)
	require.NoError(t, proj.ToStack(c, stack))

	names := []string{}
	for _, r := range stack.All() {
		if r.Kind() == client.KindNetwork {
			names = append(names, r.IncusName())
		}
	}
	return names
}

func projectClient(t *testing.T, ctx context.Context, projectName string, opts ...client.EnsureProjectOption) *client.Client {
	t.Helper()

	gc, err := client.NewTestClient(ctx)
	require.NoError(t, err)

	err = gc.Connect()
	require.NoError(t, err)

	c, err := gc.EnsureProject(projectName, opts...)
	require.NoError(t, err)

	return c
}

func ensureInstance(t *testing.T, ctx context.Context, c *client.Client, name string, opts ...client.Option) error {
	t.Helper()

	r, err := c.Resource(client.KindInstance, name, &client.InstanceConfig{})
	if err != nil {
		return err
	}

	return client.RunAction(ctx, r, client.ActionEnsure, opts...)
}

func TestConfigCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		fixture string
		wantErr bool
	}{
		{
			name:    "simple-nginx yaml",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "config"},
			fixture: "../../test/fixtures/simple-nginx",
		},
		{
			name:    "simple-nginx json",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "config", "--format", "json"},
			fixture: "../../test/fixtures/simple-nginx",
		},
		{
			name:    "two-services yaml",
			args:    []string{"-f", "../../test/fixtures/two-services/compose.yaml", "config"},
			fixture: "../../test/fixtures/two-services",
		},
		{
			name:    "wordpress",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config"},
			fixture: "../../test/fixtures/wordpress",
		},
		{
			name:    "with-secrets",
			args:    []string{"-f", "../../test/fixtures/with-secrets/compose.yaml", "config"},
			fixture: "../../test/fixtures/with-secrets",
		},
		{
			name:    "with-restart",
			args:    []string{"-f", "../../test/fixtures/with-restart/compose.yaml", "config"},
			fixture: "../../test/fixtures/with-restart",
		},
		{
			name:    "with-incus-options",
			args:    []string{"-f", "../../test/fixtures/with-incus-options/compose.yaml", "config"},
			fixture: "../../test/fixtures/with-incus-options",
		},
		{
			name:    "with-project-options",
			args:    []string{"-f", "../../test/fixtures/with-project-options/compose.yaml", "config"},
			fixture: "../../test/fixtures/with-project-options",
		},
		{
			name:    "with-build",
			args:    []string{"-f", "../../test/fixtures/with-build/compose.yaml", "config"},
			fixture: "../../test/fixtures/with-build",
		},
		{
			name:    "project-directory simple-nginx",
			args:    []string{"--project-directory", "../../test/fixtures/simple-nginx", "config"},
			fixture: "../../test/fixtures/simple-nginx",
		},
		{
			name:    "project-directory docker-compose with incus overlay",
			args:    []string{"--project-directory", "../../test/fixtures/with-docker-compose", "config"},
			fixture: "../../test/fixtures/with-docker-compose",
		},
		{
			name:    "file docker-compose with incus overlay",
			args:    []string{"-f", "../../test/fixtures/with-docker-compose/docker-compose.yaml", "config"},
			fixture: "../../test/fixtures/with-docker-compose",
		},
		{
			name:    "nonexistent file",
			args:    []string{"-f", "nonexistent.yaml", "config"},
			wantErr: true,
		},
		{
			name:    "invalid yaml",
			args:    []string{"-f", "../../test/fixtures/invalid/compose.yaml", "config"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, err := runCommand(t, context.Background(), "test-local-config", tt.args...)

			if tt.wantErr {
				require.Error(t, err, "Stdout: %s --- Stderr: %s", stdout.String(), stderr.String())
			} else {
				require.NoError(t, err)
			}

			if tt.fixture != "" {
				absFixturePath, _ := filepath.Abs(tt.fixture)
				output := strings.ReplaceAll(stdout.String(), absFixturePath, "$FIXTURE_PATH")
				snapshotter.SnapshotT(t, output)
			}
		})
	}
}

func TestConfigFilterByService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		fixture string
	}{
		{
			name:    "wordpress filter db service",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "db"},
			fixture: "../../test/fixtures/wordpress",
		},
		{
			name:    "wordpress filter wordpress service includes db dependency",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "wordpress"},
			fixture: "../../test/fixtures/wordpress",
		},
		{
			name:    "config --services list",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "--services"},
			fixture: "../../test/fixtures/wordpress",
		},
		{
			name:    "config --volumes list",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "--volumes"},
			fixture: "../../test/fixtures/wordpress",
		},
		{
			name:    "config --quiet validation",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "--quiet"},
			fixture: "../../test/fixtures/wordpress",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stdout, _, err := runCommand(t, context.Background(), "test-local-config-filter", tt.args...)
			require.NoError(t, err)

			if tt.fixture != "" {
				absFixturePath, _ := filepath.Abs(tt.fixture)
				output := strings.ReplaceAll(stdout.String(), absFixturePath, "$FIXTURE_PATH")
				snapshotter.SnapshotT(t, output)
			}
		})
	}
}

func TestNormalLifecycle(t *testing.T) {
	skipLocal(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/two-services/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	tests := []struct {
		name     string
		args     []string
		wantErr  bool
		snapshot bool
	}{
		{
			name:    "up",
			args:    []string{"-f", compose, "up", "--detach"},
			wantErr: false,
		},
		{
			name:     "list",
			args:     []string{"-f", compose, "list"},
			wantErr:  false,
			snapshot: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if tt.snapshot {
				snapshotter.SnapshotT(t, normalizeListOutput(t, stdout))
			}
		})
	}
}

// dnsServiceIPs aggregates the dnsmasq address records for a service across the
// project's managed networks.
func dnsServiceIPs(t *testing.T, c *client.Client, networks []string, service string) []string {
	t.Helper()

	conn, err := c.Connection()
	require.NoError(t, err)

	var ips []string
	for _, name := range networks {
		net, _, err := conn.GetNetwork(name)
		require.NoError(t, err, "for network %q", name)
		ips = append(ips, client.DNSmasqParse(net.Config["raw.dnsmasq"])[service]...)
	}
	return ips
}

// TestUpDownscaleRemovesInstancesAndDNS deploys the replicas=3 baseline, then
// downscales to a single instance with --scale and verifies both the surplus
// instances and their DNS records are removed while the survivor keeps resolving.
func TestUpDownscaleRemovesInstancesAndDNS(t *testing.T) {
	skipLocal(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/nginx-downscale/compose.yaml"

	networks := plannedNetworkNames(t, ctx, pn, compose)
	require.NotEmpty(t, networks)

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)
	for _, name := range []string{"web-1", "web-2", "web-3"} {
		ok, err := c.InstanceExists(name)
		require.NoError(t, err)
		require.True(t, ok, "instance %q should exist after up", name)
	}

	before := dnsServiceIPs(t, c, networks, "web")
	require.NotEmpty(t, before, "web should have DNS records for 3 replicas")

	_, _, err = runCommand(t, ctx, pn, "-f", compose, "up", "--detach", "--scale=web=1")
	require.NoError(t, err)

	survivor, err := c.InstanceExists("web-1")
	require.NoError(t, err)
	require.True(t, survivor, "web-1 should remain after downscale")

	for _, name := range []string{"web-2", "web-3"} {
		ok, err := c.InstanceExists(name)
		require.NoError(t, err)
		require.False(t, ok, "instance %q should be removed after downscale", name)
	}

	after := dnsServiceIPs(t, c, networks, "web")
	require.NotEmpty(t, after, "web-1 should still resolve after downscale")
	require.Less(t, len(after), len(before), "DNS must shed records for removed instances")
}

// TestUpReconcilesToReplicas verifies docker-parity scaling: a plain `up`
// reconciles a service to deploy.replicas in both directions. A manual --scale
// applies only to that invocation; the next plain `up` restores replicas.
func TestUpReconcilesToReplicas(t *testing.T) {
	skipLocal(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/nginx-downscale/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	// Baseline: deploy.replicas=3.
	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)
	allNames := []string{"web-1", "web-2", "web-3", "web-4", "web-5"}
	assertCount := func(want int) {
		t.Helper()
		for i, name := range allNames {
			ok, err := c.InstanceExists(name)
			require.NoError(t, err)
			require.Equal(t, i < want, ok, "instance %q existence (want %d running)", name, want)
		}
	}
	assertCount(3)

	// Manual downscale to 1.
	_, _, err = runCommand(t, ctx, pn, "-f", compose, "up", "--detach", "--scale=web=1")
	require.NoError(t, err)
	assertCount(1)

	// Plain up restores replicas=3 (scales back up).
	_, _, err = runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)
	assertCount(3)

	// Manual upscale to 5.
	_, _, err = runCommand(t, ctx, pn, "-f", compose, "up", "--detach", "--scale=web=5")
	require.NoError(t, err)
	assertCount(5)

	// Plain up reconciles back down to replicas=3.
	_, _, err = runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)
	assertCount(3)
}
