package main

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// stressIterations returns the number of down/up cycles to run, overridable via
// INCUS_COMPOSE_STRESS_ITER for cranking the loop up locally.
func stressIterations(t *testing.T) int {
	t.Helper()

	v, ok := os.LookupEnv("INCUS_COMPOSE_STRESS_ITER")
	if !ok || v == "" {
		return 0
	}

	n, err := strconv.Atoi(v)
	require.NoErrorf(t, err, "INCUS_COMPOSE_STRESS_ITER=%q is not an integer", v)

	return n
}

// TestStressHealthdDownUp reliably reproduces the healthd down/up races by looping
// the cycle with no sleeps. It exercises both the "Instance is busy running a stop
// operation" race (recreate path) and the "forkfile.sock: connection reset by peer"
// token-push race. The tight scheduling, not any single run, is what surfaces them.
func TestStressHealthdDownUp(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipSlow(t)

	iter := stressIterations(t)
	if iter < 0 {
		t.Skip("Set INCUS_COMPOSE_STRESS_ITER to a value higher 0")
	}

	ctx := context.Background()
	pn := t.Name()
	compose := "../../test/fixtures/healthd-debug/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	// Bring the project (and the healthd sidecar) up once.
	_, stderr, err := runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoErrorf(t, err, "initial up failed: %s", stderr.String())

	t.Run("down-then-up", func(t *testing.T) {
		for i := range iter {
			_, stderr, err := runCommand(t, ctx, pn, "-f", compose, "healthd", "down")
			require.NoErrorf(t, err, "iteration %d: healthd down failed: %s", i, stderr.String())

			_, stderr, err = runCommand(t, ctx, pn, "-f", compose, "healthd", "up")
			require.NoErrorf(t, err, "iteration %d: healthd up failed: %s", i, stderr.String())
		}
	})

	t.Run("recreate", func(t *testing.T) {
		for i := range iter {
			_, stderr, err := runCommand(t, ctx, pn, "-f", compose, "healthd", "up", "--recreate")
			require.NoErrorf(t, err, "iteration %d: healthd up --recreate failed: %s", i, stderr.String())
		}
	})
}
