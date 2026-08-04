package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/avast/retry-go/v5"
	incus "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"

	"github.com/lxc/incus-compose/shared"
)

// instanceConfigPatch is a minimal instance PATCH body carrying only config keys.
type instanceConfigPatch struct {
	Config map[string]string `json:"config"`
}

// checker probes one instance and reports its status; the runner decides restarts.
type checker struct {
	conn   incus.InstanceServer
	name   string
	params instanceConfig

	failures int    // local to this run; never carried across a respawn
	status   string // last status written, to skip redundant writes

	statusCh chan<- string // every user.healthcheck.status write, mirrored here
	exitCh   chan<- error  // exactly one send, then run has returned for good
}

// newChecker builds a checker for name; the runner restarts the instance itself.
func newChecker(
	conn incus.InstanceServer,
	name string,
	cfg instanceConfig,
	statusCh chan<- string, exitCh chan<- error,
) *checker {
	return &checker{
		conn:     conn,
		name:     name,
		params:   cfg,
		statusCh: statusCh,
		exitCh:   exitCh,
	}
}

// phaseResult tells run what to do after a checking phase ends.
type phaseResult int

const (
	phaseStop    phaseResult = iota // retries exhausted: stop and report
	phaseNormal                     // continue with the normal-interval checker
	phaseStopped                    // the instance is not running: exit, events own the restart
)

// run alternates the start-period and normal phases until ctx ends or retries run out.
func (c *checker) run(ctx context.Context, inStart bool) {
	for {
		if c.params.StartPeriod < 1 {
			// Disable inStart if the period is smaller 1
			inStart = false
		}

		result := c.runPhase(ctx, inStart)
		if ctx.Err() != nil {
			slog.Debug("Checker exiting, canceled", "instance", c.name)
			c.exitCh <- nil
			return
		}

		switch result {
		case phaseStop:
			slog.Debug("Checker exiting, retries exhausted", "instance", c.name, "failures", c.failures)
			c.exitCh <- ErrRetriesExhausted.WithFailures(uint64(c.failures))
			return
		case phaseStopped:
			c.exitCh <- ErrInstanceStopped
			return
		case phaseNormal:
			inStart = false
		}
	}
}

// runPhase runs one checking phase and returns how run should proceed. The caller
// must check ctx.Err() first: phaseCtx.Done() also unblocks on a parent cancel.
func (c *checker) runPhase(ctx context.Context, inStart bool) phaseResult {
	interval := c.params.Interval
	retries := c.params.Retries
	phaseCtx, cancel := context.WithCancel(ctx)
	if inStart {
		interval = c.params.StartInterval
		phaseCtx, cancel = context.WithTimeout(ctx, c.params.StartPeriod)
	}
	defer cancel()

	// NewTicker panics on a non-positive interval, which would kill the daemon
	// and every instance it watches, not just this checker.
	if interval <= 0 {
		slog.Warn("Non-positive check interval, falling back to the default",
			"instance", c.name, "interval", interval)
		interval = defaultInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			var status string
			var result phaseResult
			done := false

			// Logged either side of the check so a silent checker tells whether
			// the tick stopped arriving or the check itself never returned.
			slog.Debug("Check starting", "instance", c.name, "inStart", inStart)

			started := time.Now()
			err := c.check(phaseCtx)
			slog.Debug("Check done", "instance", c.name, "took", time.Since(started), "error", err)

			if errors.Is(err, ErrNotRunning) {
				// Events own bringing the instance and its checker back.
				slog.Info("Checker exiting, instance is not running", "instance", c.name)
				return phaseStopped
			} else if err == nil {
				c.failures = 0
				status = shared.HealthStatusHealthy

				if inStart {
					// First success during the start period: switch to the normal checker.
					result, done = phaseNormal, true
				}
			} else {
				c.failures++
				slog.Debug("check failed",
					"instance", c.name,
					"failures", c.failures,
					"retries", retries,
					"inStart", inStart,
					"error", err,
				)
				status = shared.HealthStatusUnhealthy

				if c.failures >= retries {
					result, done = phaseStop, true
				}
			}

			if err := c.writeStatus(phaseCtx, status); err != nil {
				slog.Debug("updating healthcheck status", "instance", c.name, "error", err)
			}

			if done {
				return result
			}
		case <-phaseCtx.Done():
			if inStart {
				slog.Debug("checker phase (start -> normal)", "instance", c.name)
				// Start period elapsed: switch to the normal checker.
				return phaseNormal
			}

			return phaseStop
		}
	}
}

// withContext runs call on its own goroutine and gives up when ctx is done.
func withContext[T any](ctx context.Context, call func() (T, error)) (T, error) {
	type result struct {
		value T
		err   error
	}

	ch := make(chan result, 1)
	go func() {
		value, err := call()
		ch <- result{value: value, err: err}
	}()

	select {
	case r := <-ch:
		return r.value, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// check runs the healthcheck command once and returns nil when healthy.
func (c *checker) check(ctx context.Context) error {
	stateCtx, cancel := context.WithTimeout(ctx, c.params.Timeout)
	defer cancel()

	inst, err := withContext(stateCtx, func() (*incusApi.InstanceState, error) {
		state, _, err := c.conn.GetInstanceState(c.name)
		return state, err
	})
	if err != nil {
		slog.Debug("fetching instance status error", "instance", c.name, "error", err)
		return err
	}

	if inst.StatusCode != incusApi.Running {
		slog.Debug("instance is not running", "instance", c.name, "status", inst.Status)
		return ErrNotRunning
	}

	// Build command based on test format
	if len(c.params.Test) == 0 {
		return nil
	}

	var cmd []string
	switch c.params.Test[0] {
	case "CMD":
		cmd = c.params.Test[1:]
	case "CMD-SHELL":
		cmd = []string{"/bin/sh", "-c", c.params.Test[1]}
	case "NONE":
		return nil
	default:
		// Assume it's a direct command
		cmd = c.params.Test
	}

	// Execute with timeout
	execCtx, cancel := context.WithTimeout(ctx, c.params.Timeout)
	defer cancel()

	exitCode, stdout, stderr, err := c.exec(execCtx, cmd)
	if err != nil {
		slog.Debug("exec error", "instance", c.name, "error", err, "stdout", stdout, "stderr", stderr)
		return err
	}

	if exitCode != 0 {
		return fmt.Errorf("cmd failed, exit code: %d", exitCode)
	}

	return nil
}

// exec runs a command inside the instance and returns the exit code.
func (c *checker) exec(ctx context.Context, cmd []string) (int, string, string, error) {
	req := incusApi.InstanceExecPost{
		Command:     cmd,
		WaitForWS:   true,
		Interactive: false,
	}

	var stdout, stderr bytes.Buffer
	args := incus.InstanceExecArgs{
		Stdin:    nil,
		Stdout:   &stdout,
		Stderr:   &stderr,
		DataDone: make(chan bool),
	}

	// A timed-out call leaves any operation it creates for the server to reap.
	op, err := withContext(ctx, func() (incus.Operation, error) {
		return c.conn.ExecInstance(c.name, req, &args)
	})
	if err != nil {
		return -1, "", "", err
	}

	// Wait for I/O to complete
	select {
	case <-args.DataDone:
	case <-ctx.Done():
		if err := op.Cancel(); err != nil {
			slog.Debug("canceling exec operation", "instance", c.name, "error", err)
		}
		return -1, "", "", ctx.Err()
	}

	// Wait for operation to complete
	err = op.Wait()
	if err != nil {
		return -1, stdout.String(), stderr.String(), err
	}

	// Get exit code from operation metadata
	opAPI := op.Get()
	if exitCode, ok := opAPI.Metadata["return"].(float64); ok {
		return int(exitCode), stdout.String(), stderr.String(), nil
	}

	return -1, "", "", nil
}

// writeStatus persists status to user.healthcheck.status, mirroring it on statusCh.
func (c *checker) writeStatus(ctx context.Context, status string) error {
	if c.status == status {
		// We already wrote that.
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()

	inst, err := withContext(ctx, func() (*incusApi.Instance, error) {
		i, _, err := c.conn.GetInstance(c.name)
		return i, err
	})
	if err != nil {
		return err
	}

	if inst.Config[shared.HealthStatusKey] == status {
		return nil
	}

	select {
	case c.statusCh <- status:
	default:
		// Don't block on a slow runner; selfWrites is an efficiency measure only.
	}

	slog.Info("Status update", "instance", c.name, "old", inst.Config[shared.HealthStatusKey], "current", status)

	info, err := c.conn.GetConnectionInfo()
	if err != nil {
		return err
	}

	path := incusApi.NewURL().
		Path("1.0", "instances", c.name).
		Project(info.Project).
		Target(info.Target).
		String()

	// The instance operation lock is briefly held by a concurrent start/stop.
	err = retry.New(
		retry.Context(ctx),
		retry.Attempts(6),
		retry.Delay(250*time.Millisecond),
		retry.RetryIf(func(err error) bool {
			return strings.Contains(err.Error(), "Instance is busy")
		}),
		retry.LastErrorOnly(true),
	).Do(func() error {
		_, err := withContext(ctx, func() (struct{}, error) {
			_, _, patchErr := c.conn.RawQuery("PATCH", path, instanceConfigPatch{
				Config: map[string]string{shared.HealthStatusKey: status},
			}, "")

			return struct{}{}, patchErr
		})
		return err
	})
	if err != nil {
		return err
	}

	c.status = status
	return nil
}
