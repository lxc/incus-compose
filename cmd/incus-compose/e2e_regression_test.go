package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
)

// TestNoDanglingNetworksAfterDown is a regression test for the project default
// network not being removed after `down --project`.
func TestNoDanglingNetworksAfterDown(t *testing.T) {
	t.Parallel()
	skipLocal(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	_, _, err = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	require.NoError(t, err)

	gc, err := client.NewTestClient(ctx)
	require.NoError(t, err)

	conn, err := gc.Connection()
	require.NoError(t, err)

	networkName := client.SanitizeNetworkName(pn, "ic-", "default")
	networkNames, err := conn.GetNetworkNames()
	require.NoError(t, err)

	require.NotContains(t, networkNames, networkName, "network %q was not removed by down --project", networkName)
}

// TestExecSelectsCorrectInstance is a regression test for the exec command
// dispatching to the wrong instance when multiple services share a stack.
// It runs `hostname` in each service of a multi-service project and asserts
// the output matches the expected Incus instance name.
func TestExecSelectsCorrectInstance(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	tests := []struct {
		service  string
		wantHost string
	}{
		{"nginx", "nginx-1"},
		{"backend1", "backend1-1"},
		{"backend2", "backend2-1"},
	}

	for _, tt := range tests {
		t.Run(tt.service, func(t *testing.T) {
			stdout, _, err := runCommand(t, ctx, pn, "-f", compose, "exec", "--no-tty", tt.service, "hostname")
			require.NoError(t, err)
			if strings.TrimSpace(stdout.String()) != tt.wantHost {
				t.Errorf("got hostname %q, want %q", strings.TrimSpace(stdout.String()), tt.wantHost)
			}
		})
	}
}
