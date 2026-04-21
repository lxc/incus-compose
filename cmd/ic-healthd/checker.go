package main

import (
	"bytes"
	"context"
	"log"
	"time"

	incus "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
)

// Checker monitors a single service and restarts it when unhealthy.
type Checker struct {
	client   incus.InstanceServer
	name     string
	config   ServiceConfig
	debug    bool
	failures int

	status string
}

// NewChecker creates a new checker for the named service.
func NewChecker(client incus.InstanceServer, name string, cfg ServiceConfig, debug bool) *Checker {
	return &Checker{
		client: client,
		name:   name,
		config: cfg,
		debug:  debug,
	}
}

// Run starts the health check loop until context is cancelled.
func (c *Checker) Run(ctx context.Context) {
	ticker := time.NewTicker(c.config.Interval.Duration())
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			oldStatus := c.status

			if c.check(ctx) {
				c.failures = 0
				if c.status != "healthy" {
					c.status = "healthy"
				}
			} else {
				c.failures++
				log.Printf("%s: check failed (%d/%d)", c.name, c.failures, c.config.Retries)

				if c.status != "unhealthy" {
					c.status = "unhealthy"
				}

				if c.failures >= c.config.Retries {
					if !c.config.Restart {
						if c.debug {
							log.Printf("%s: stopping the check", c.name)
						}
						return
					}

					log.Printf("%s: restarting instance", c.name)
					if err := c.restart(ctx); err != nil {
						log.Printf("%s: restart failed: %v", c.name, err)
						c.failures = 0
					} else {
						log.Printf("%s: restarted successfully", c.name)
						c.failures = 0
						if c.status != "healthy" {
							c.status = "healthy"
						}
					}
				}
			}

			if c.status != oldStatus {
				if err := c.writeStatus(); err != nil && c.debug {
					log.Printf("%s: updating healthcheck status: %v", c.name, err)
				}
			}
		case <-ctx.Done():
			if c.debug {
				log.Printf("%s: checker stopped", c.name)
			}
			return
		}
	}
}

// check executes the healthcheck command and returns true if healthy.
func (c *Checker) check(ctx context.Context) bool {
	inst, _, err := c.client.GetInstanceState(c.name)
	if err != nil {
		if c.debug {
			log.Printf("%s: fetching instance status error: %v", c.name, err)
		}

		return false
	}

	if inst.Status != "Running" {
		if c.debug {
			log.Printf("%s: Status is not 'Running' but '%v'", c.name, inst.Status)
		}
		return false
	}

	// Build command based on test format
	var cmd []string
	switch c.config.Test[0] {
	case "CMD":
		cmd = c.config.Test[1:]
	case "CMD-SHELL":
		cmd = []string{"/bin/sh", "-c", c.config.Test[1]}
	case "NONE":
		return true
	default:
		// Assume it's a direct command
		cmd = c.config.Test
	}

	// Execute with timeout
	execCtx, cancel := context.WithTimeout(ctx, c.config.Timeout.Duration())
	defer cancel()

	exitCode, err := c.exec(execCtx, cmd)
	if err != nil {
		if c.debug {
			log.Printf("%s: exec error: %v", c.name, err)
		}
		return false
	}

	return exitCode == 0
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
		return -1, err
	}

	// Get exit code from operation metadata
	opAPI := op.Get()
	if exitCode, ok := opAPI.Metadata["return"].(float64); ok {
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

	inst.Config["user.healthcheck.status"] = c.status
	op, err := c.client.UpdateInstance(c.name, inst.Writable(), etag)
	if err != nil {
		return err
	}

	return op.Wait()
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
