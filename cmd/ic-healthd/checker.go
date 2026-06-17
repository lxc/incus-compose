package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	incus "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"

	"github.com/lxc/incus-compose/client"
)

// Checker monitors a single instance and restarts it when unhealthy.
type Checker struct {
	client       incus.InstanceServer
	name         string
	config       InstanceConfig
	failures     int
	status       string
	restartDelay time.Duration
}

// NewChecker creates a new checker for the named instance.
func NewChecker(myclient incus.InstanceServer, name string, cfg InstanceConfig) *Checker {
	return &Checker{
		client:       myclient,
		name:         name,
		config:       cfg,
		restartDelay: cfg.RestartDelay,
	}
}

// phaseResult tells Run what to do after a checking phase ends.
type phaseResult int

const (
	phaseStop    phaseResult = iota // stop checking entirely
	phaseNormal                     // continue with the normal-interval checker
	phaseRestart                    // restart the instance and re-enter the start period
)

// Run drives the health check loop until ctx is cancelled. It alternates
// between the start-period checker (start interval, bounded by the start
// period) and the normal checker, restarting the instance with exponential
// backoff when it stays unhealthy. inStart selects the start-period checker;
// startInstance restarts the instance before checking.
func (c *Checker) Run(ctx context.Context, inStart bool, startInstance bool) {
	ticker := time.NewTicker(checkInstanceRunningDelay)

	for {
		slog.Debug("Loop starting checker",
			"instance", c.name,
			"status", c.status,
			"inStart", inStart,
			"startInstance", startInstance,
			"config", c.config,
		)

		if c.config.StartPeriod < 1 {
			// Disable inStart if the period is smaller 1
			inStart = false
		}

		if startInstance {
			if err := c.restart(ctx); err != nil {
				slog.Error("restart failed", "instance", c.name, "error", err)
			}
		}

		inst, _, err := c.client.GetInstance(c.name)
		if err != nil ||
			(inst != nil && inst.StatusCode != incusApi.Running) ||
			(inst != nil && inst.Config[client.HealthStoppedKey] == "true") {
			slog.Debug("Loop not ready", "instance", c.name, "error", err)

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				continue
			}
		}

		result := c.runPhase(ctx, inStart)
		slog.Debug("Loop stopped checker",
			"instance", c.name,
			"status", c.status,
			"inStart", inStart,
			"result", result,
		)

		switch result {
		case phaseStop:
			slog.Debug("Loop stop",
				"instance", c.name,
				"status", c.status,
			)
			return
		case phaseNormal:
			inStart, startInstance = false, false
		case phaseRestart:
			inStart, startInstance = true, true
		}
	}
}

// runPhase runs a single checking phase until a transition is required. When
// inStart is true it uses the start interval and is bounded by the start
// period; otherwise it uses the normal interval and runs until ctx is
// cancelled. The returned phaseResult tells Run how to proceed.
func (c *Checker) runPhase(ctx context.Context, inStart bool) phaseResult {
	interval := c.config.Interval
	phaseCtx, cancel := context.WithCancel(ctx)
	if inStart {
		interval = c.config.StartInterval
		phaseCtx, cancel = context.WithTimeout(ctx, c.config.StartPeriod)
	}
	defer cancel()

	if inStart && c.check(phaseCtx) == nil {
		// First success during the start period: switch to the normal checker.
		c.failures = 0
		c.restartDelay = c.config.RestartDelay

		if err := c.writeStatus(client.HealthStatusHealthy); err != nil {
			slog.Debug("updating healthcheck status", "instance", c.name, "error", err)
		}

		return phaseNormal
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			var status string
			var result phaseResult
			done := false

			err := c.check(phaseCtx)
			if err == nil {
				c.failures = 0
				c.restartDelay = c.config.RestartDelay
				status = client.HealthStatusHealthy

				if inStart {
					// First success during the start period: switch to the normal checker.
					result, done = phaseNormal, true
				}
			} else {
				c.failures++
				slog.Warn("check failed",
					"instance", c.name,
					"failures", c.failures,
					"retries", c.config.Retries,
					"inStart", inStart,
					"error", err,
				)
				status = client.HealthStatusUnhealthy

				if c.failures >= c.config.Retries {
					switch {
					case !c.config.Restart:
						slog.Debug("stopping the check", "instance", c.name)
						result, done = phaseStop, true
					case c.config.UnlessStopped && c.isStopped():
						c.failures = 0
						slog.Debug("unless-stopped: intentionally stopped, skipping restart", "instance", c.name)
					default:
						c.failures = 0
						delay := c.restartDelay
						c.restartDelay = min(c.restartDelay*2, maxRestartDelay)

						slog.Info("restarting instance", "instance", c.name, "delay", delay)
						select {
						case <-time.After(delay):
						case <-ctx.Done():
							return phaseStop
						}

						result, done = phaseRestart, true
					}
				}
			}

			if err := c.writeStatus(status); err != nil {
				slog.Debug("updating healthcheck status", "instance", c.name, "error", err)
			}

			if done {
				return result
			}
		case <-phaseCtx.Done():
			if inStart {
				slog.Debug("checker restart (start -> real)", "instance", c.name)
				// Start period elapsed: switch to the normal checker.
				return phaseNormal
			}

			return phaseStop
		}
	}
}

// check executes the healthcheck command and returns true if healthy.
func (c *Checker) check(ctx context.Context) error {
	inst, _, err := c.client.GetInstanceState(c.name)
	if err != nil {
		slog.Debug("fetching instance status error", "instance", c.name, "error", err)
		return err
	}

	if inst.StatusCode != incusApi.Running {
		return errors.New("not running")
	}

	// Build command based on test format
	if len(c.config.Test) == 0 {
		return nil
	}

	var cmd []string
	switch c.config.Test[0] {
	case "CMD":
		cmd = c.config.Test[1:]
	case "CMD-SHELL":
		cmd = []string{"/bin/sh", "-c", c.config.Test[1]}
	case "NONE":
		return nil
	default:
		// Assume it's a direct command
		cmd = c.config.Test
	}

	// Execute with timeout
	execCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
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
func (c *Checker) exec(ctx context.Context, cmd []string) (int, string, string, error) {
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

	op, err := c.client.ExecInstance(c.name, req, &args)
	if err != nil {
		return -1, "", "", err
	}

	// Wait for I/O to complete
	select {
	case <-args.DataDone:
	case <-ctx.Done():
		if err := op.Cancel(); err != nil {
			slog.Debug("cancelling exec operation", "instance", c.name, "error", err)
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

// writeStatus persists c.status into the instance's user.healthcheck.status config key.
func (c *Checker) writeStatus(status string) error {
	if c.status == status {
		// We already wrote that.
		return nil
	}

	slog.Debug("Writing status", "instance", c.name, "status", status)

	inst, etag, err := c.client.GetInstance(c.name)
	if err != nil {
		return err
	}

	if inst.Config[client.HealthStoppedKey] == "true" {
		status = client.HealthStatusStopped

		if c.status == status {
			// We already wrote that.
			return nil
		}
	}

	inst.Config[client.HealthConfigKey] = status
	op, err := c.client.UpdateInstance(c.name, inst.Writable(), etag)
	if err != nil {
		return err
	}

	if err := op.Wait(); err != nil {
		return err
	}

	c.status = status
	return nil
}

// isStopped reports whether the instance has user.healthcheck.stopped=true, meaning it was
// intentionally stopped. Returns true on API error (instance gone counts as stopped).
func (c *Checker) isStopped() bool {
	inst, _, err := c.client.GetInstance(c.name)
	if err != nil {
		return true
	}
	return inst.Config[client.HealthStoppedKey] == "true"
}

// restart brings the instance back to Running. If it's already stopped we
// only start; otherwise we stop (force) and start. We avoid the "restart"
// action because it errors on a stopped instance.
func (c *Checker) restart(_ context.Context) error {
	state, _, err := c.client.GetInstanceState(c.name)
	if err != nil {
		return err
	}

	if state.StatusCode != incusApi.Stopped {
		stopReq := incusApi.InstanceStatePut{
			Action:  "stop",
			Timeout: -1,
			Force:   true,
		}

		op, err := c.client.UpdateInstanceState(c.name, stopReq, "")
		if err != nil {
			return err
		}

		if err := op.Wait(); err != nil {
			return err
		}
	}

	startReq := incusApi.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
	}

	op, err := c.client.UpdateInstanceState(c.name, startReq, "")
	if err != nil {
		return err
	}

	return op.Wait()
}
