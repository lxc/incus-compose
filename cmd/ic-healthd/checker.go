package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"gitlab.com/r3j0/incus-compose/client"
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

// Run starts the health check loop until context is cancelled.
func (c *Checker) Run(ctx context.Context, inStart bool, startInstance bool) {
	var ticker *time.Ticker

	slog.Debug("Starting checker",
		"instance", c.name,
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

	origCtx := ctx
	var cancel context.CancelFunc

	if inStart {
		ticker = time.NewTicker(c.config.StartInterval)
		ctx, cancel = context.WithTimeout(ctx, c.config.StartPeriod)
	} else {
		ticker = time.NewTicker(c.config.Interval)
		ctx, cancel = context.WithCancel(ctx)
	}

	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			oldStatus := c.status

			if c.check(ctx) == nil {
				c.failures = 0
				c.restartDelay = c.config.RestartDelay
				if c.status != client.HealthStatusHealthy {
					c.status = client.HealthStatusHealthy
				}
			} else {
				c.failures++
				slog.Warn("check failed",
					"instance", c.name,
					"failures", c.failures,
					"retries", c.config.Retries,
					"inStart", inStart,
					"startInstance", startInstance,
				)

				if c.status != client.HealthStatusUnhealthy {
					c.status = client.HealthStatusUnhealthy
				}

				if c.failures >= c.config.Retries {
					if !c.config.Restart {
						slog.Debug("stopping the check", "instance", c.name)
						cancel()
						return
					}

					c.failures = 0

					if c.config.UnlessStopped && c.isStopped() {
						slog.Debug("unless-stopped: intentionally stopped, skipping restart", "instance", c.name)
					} else {
						slog.Info("restarting instance", "instance", c.name, "delay", c.restartDelay)
						c.restartDelay = min(c.restartDelay*2, maxRestartDelay)

						slog.Debug("backoff restart, sleeping", "delay", c.restartDelay)
						time.Sleep(c.restartDelay)

						cancel()
						c.Run(origCtx, true, true)

						// Do not start the checker again.
						inStart = false
					}
				}
			}

			if c.status != oldStatus {
				if err := c.writeStatus(); err != nil {
					slog.Debug("updating healthcheck status", "instance", c.name, "error", err)
				}
			}
		case <-ctx.Done():
			cancel()

			if inStart {
				slog.Debug("checker restart (start -> real)", "instance", c.name)

				// Run real checker after the start checker.
				c.Run(origCtx, false, false)
			}

			return
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

	if inst.Status != "Running" {
		slog.Debug("status is not Running", "instance", c.name, "status", inst.Status)
		return errors.New("Not running")
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

	exitCode, err := c.exec(execCtx, cmd)
	if err != nil {
		slog.Debug("exec error", "instance", c.name, "error", err)
		return err
	}

	if exitCode != 0 {
		return errors.New("cmd failed")
	}

	return nil
}

// exec runs a command inside the instance and returns the exit code.
func (c *Checker) exec(ctx context.Context, cmd []string) (int, error) {
	req := api.InstanceExecPost{
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
		return -1, err
	}

	// Wait for I/O to complete
	select {
	case <-args.DataDone:
	case <-ctx.Done():
		return -1, ctx.Err()
	}

	// Wait for operation to complete
	err = op.Wait()
	if err != nil {
		slog.Debug("Output", "name", c.name, "stdout", stdout.String(), "stderr", stderr.String())
		return -1, err
	}

	// Get exit code from operation metadata
	opAPI := op.Get()
	if exitCode, ok := opAPI.Metadata["return"].(float64); ok {
		if exitCode != 0 {
			slog.Debug("Output", "name", c.name, "stdout", stdout.String(), "stderr", stderr.String())
		}
		return int(exitCode), nil
	}

	return -1, nil
}

// writeStatus persists c.status into the instance's user.healthcheck.status config key.
func (c *Checker) writeStatus() error {
	inst, etag, err := c.client.GetInstance(c.name)
	if err != nil {
		return err
	}

	inst.Config[client.HealthConfigKey] = c.status
	op, err := c.client.UpdateInstance(c.name, inst.Writable(), etag)
	if err != nil {
		return err
	}

	return op.Wait()
}

// isStopped reports whether the instance has user.stopped=true, meaning it was
// intentionally stopped. Returns true on API error (instance gone counts as stopped).
func (c *Checker) isStopped() bool {
	inst, _, err := c.client.GetInstance(c.name)
	if err != nil {
		return true
	}
	return inst.Config["user.stopped"] == "true"
}

// restart brings the instance back to Running. If it's already stopped we
// only start; otherwise we stop (force) and start. We avoid the "restart"
// action because it errors on a stopped instance.
func (c *Checker) restart(_ context.Context) error {
	state, _, err := c.client.GetInstanceState(c.name)
	if err != nil {
		return err
	}

	if state.StatusCode != api.Stopped {
		stopReq := api.InstanceStatePut{
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

	startReq := api.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
	}

	op, err := c.client.UpdateInstanceState(c.name, startReq, "")
	if err != nil {
		return err
	}

	return op.Wait()
}
