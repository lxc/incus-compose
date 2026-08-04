package main

import (
	"context"
	"net"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/shared"
)

// refusedConn returns a client whose every call fails immediately, standing in
// for the transient API errors a contended server produces.
func refusedConn(t *testing.T) incus.InstanceServer {
	t.Helper()

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	conn, err := incus.ConnectIncusWithContext(t.Context(), "https://"+addr, &incus.ConnectionArgs{
		InsecureSkipVerify: true,
		SkipGetServer:      true,
		SkipGetEvents:      true,
	})
	require.NoError(t, err)

	return conn
}

// TestIsMarkedStoppedNeedsEvidence pins that only the config key means stopped.
//
// The key is written by `incus-compose stop`, so it is a positive statement of
// intent. Reading an API error as intent inverts restart: unless-stopped into
// never restart - evaluateBackoff drops the checker and the tracked entry, and
// a stopped instance emits no instance-started event to bring either back.
func TestIsMarkedStoppedNeedsEvidence(t *testing.T) {
	t.Parallel()

	r := &Runner{
		config:  &Config{Project: "p"},
		conn:    refusedConn(t),
		tracked: map[string]*trackedInstance{},
	}

	require.False(t, r.isMarkedStopped("web-1"),
		"an unreachable API is not evidence that the user stopped the instance")
}

// TestParseInstanceRejectsNonPositiveDurations pins that a healthcheck cannot
// carry a zero or negative interval.
//
// runPhase builds a time.Ticker from these, and NewTicker panics on a
// non-positive interval. That panic is raised on the checker's goroutine, so it
// takes down the whole daemon and every instance it watches with it.
func TestParseInstanceRejectsNonPositiveDurations(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"interval", "start_interval"} {
		for _, value := range []string{"0s", "-5s"} {
			t.Run(key+"="+value, func(t *testing.T) {
				t.Parallel()

				_, err := parseInstance(map[string]string{
					shared.HealthKeyPrefix + "test": `["CMD","true"]`,
					shared.HealthKeyPrefix + key:    value,
				}, true)

				require.Error(t, err, "%s=%s must be rejected, it panics NewTicker", key, value)
			})
		}
	}
}

// TestCheckerSurvivesNonPositiveInterval pins that the checker never panics on
// a non-positive interval, whatever produced it.
func TestCheckerSurvivesNonPositiveInterval(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	exitCh := make(chan error, 1)
	c := newChecker(refusedConn(t), "web-1", instanceConfig{
		Test:     []string{"CMD", "true"},
		Interval: 0,
		Timeout:  time.Second,
		Retries:  1,
	}, make(chan string, 1), exitCh)

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.run(ctx, false)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("checker did not return with a non-positive interval")
	}
}
