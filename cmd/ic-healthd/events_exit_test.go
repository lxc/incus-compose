package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHandleCheckerExitLeavesNoZombie pins the invariant handleStarted relies
// on: an entry in tracked has a live checker.
//
// handleStarted returns early when the name is already tracked, so an entry
// left behind by a checker that exited is never given a new one - the instance
// stays tracked, reports nothing, and no lifecycle event can revive it.
func TestHandleCheckerExitLeavesNoZombie(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         error
		restart     string
		wantTracked bool
	}{
		{
			// The instance stopped; a later instance-started must be able to
			// spawn a fresh checker, which needs the entry gone.
			name:        "instance stopped",
			err:         ErrInstanceStopped,
			restart:     "always",
			wantTracked: false,
		},
		{
			// No restart policy: nothing will respawn, so the entry must go.
			name:        "retries exhausted without restart",
			err:         ErrRetriesExhausted.WithFailures(3),
			restart:     "no",
			wantTracked: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &Runner{
				config:  &Config{Project: "p"},
				tracked: map[string]*trackedInstance{},
			}
			r.tracked["web-1"] = &trackedInstance{
				cancel:       func() {},
				serverParams: instanceConfig{Restart: tt.restart},
			}

			r.handleCheckerExit("web-1", tt.err)

			r.mu.Lock()
			_, tracked := r.tracked["web-1"]
			r.mu.Unlock()

			require.Equal(t, tt.wantTracked, tracked,
				"a tracked entry with no checker is never revived: handleStarted returns early on it")
		})
	}
}
