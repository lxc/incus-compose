package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
)

func TestLifecycleHealthd(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/healthd-debug/compose.yaml"

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
			name: "list",
			args: []string{"-f", compose, "list"},
		},
		{
			name: "healthd logs",
			args: []string{"-f", compose, "healthd", "logs"},
		},
		{
			name: "healthd reload",
			args: []string{"-f", compose, "healthd", "reload"},
		},
		{
			name: "healthd restart",
			args: []string{"-f", compose, "healthd", "restart"},
		},
		{
			name: "healthd down",
			args: []string{"-f", compose, "healthd", "down"},
		},
		{
			name: "healthd up --recreate",
			args: []string{"-f", compose, "healthd", "up", "--recreate"},
		},
		{
			name: "down",
			args: []string{"-f", compose, "down", "--project"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCommand(t, ctx, pn, tt.args...)
			require.NoError(t, err)
		})
	}
}

func TestNoHealthdSkipsHealthdInstance(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/with-restart/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach", "--no-healthd")
	require.NoError(t, err)

	gc, err := client.NewTestClient(ctx)
	require.NoError(t, err)

	c, err := gc.EnsureProject(pn)
	require.NoError(t, err)

	h, err := healthdResolve(c)
	require.Nil(t, h)
	require.Error(t, err)
}

func TestNoHealthdWhenNotNeeded(t *testing.T) {
	skipLocal(t)
	skipSlow(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	gc, err := client.NewTestClient(ctx)
	require.NoError(t, err)

	c, err := gc.EnsureProject(pn)
	require.NoError(t, err)

	h, err := healthdResolve(c)
	require.Nil(t, h)
	require.Error(t, err)
}
