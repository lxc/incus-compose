package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	incusClient "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

// execCommand implements `incus-compose exec` similar to `docker compose exec` (MVP).
// Supports: -d/--detach, --dry-run, -e/--env, -T/--no-tty, -u/--user (flag accepted, not applied),
// -w/--workdir (implemented by shell wrapper). `--privileged` is accepted but not acted upon in MVP.
//
// Notes:
//   - We implement a shell-wrapper strategy for environment and workdir so that the feature works
//     even if the target container doesn't provide helpers. This is an MVP approach and not as
//     robust as native Exec options on some runtimes.
func newExecCommand() *cli.Command {
	return &cli.Command{
		Name:      "exec",
		Usage:     "Execute a command in a running instance",
		Category:  "compose",
		ArgsUsage: "SERVICE COMMAND [ARGS...]",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "detach",
				Aliases: []string{"d"},
				Usage:   "Detached mode: Run command in the background",
			},
			&cli.BoolFlag{
				Name:  "dry-run",
				Usage: "Execute command in dry run mode",
			},
			&cli.StringSliceFlag{
				Name:    "env",
				Aliases: []string{"e"},
				Usage:   "Set environment variables (KEY=VALUE). May be specified multiple times.",
			},
			&cli.IntFlag{
				Name:  "index",
				Usage: "Index of the container if service has multiple replicas (not implemented in MVP)",
				Value: 0,
			},
			&cli.BoolFlag{
				Name:    "no-tty",
				Usage:   "Disable pseudo-TTY allocation. By default a TTY is allocated when available.",
				Aliases: []string{"T"},
			},
			&cli.BoolFlag{
				Name:  "privileged",
				Usage: "Give extended privileges to the process (accepted but not implemented in MVP)",
			},
			&cli.StringFlag{
				Name:    "user",
				Aliases: []string{"u"},
				Usage:   "Run the command as this user (accepted but not implemented in MVP)",
			},
			&cli.StringFlag{
				Name:    "workdir",
				Aliases: []string{"w"},
				Usage:   "Path to workdir directory for this command (implemented via shell wrapper)",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			// Validate args
			args := cmd.Args().Slice()
			if len(args) < 2 {
				return fmt.Errorf("usage: %s SERVICE COMMAND [ARGS...]", cmd.Name)
			}
			service := args[0]
			origCmd := args[1:]

			// Get global client from context
			globalClient, err := clientFromContext(ctx)
			if err != nil {
				return err
			}
			if err := globalClient.Connect(); err != nil {
				return err
			}

			// Load project
			p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
			if err != nil {
				globalClient.LogError("Configuring the project", "error", err)
				return errLogged.Wrap(err)
			}

			// Get the per Project client - don't create if it doesn't exist
			c, err := globalClient.EnsureProject(p.Name)
			if err != nil {
				globalClient.LogError("Getting the incus project", "error", err)
				return errLogged.Wrap(err)
			}
			if err := c.Open(); err != nil {
				globalClient.LogError("Opening the project client", "error", err)
				return errLogged.Wrap(err)
			}
			defer func() { _ = c.Done() }()

			allResources, err := p.Resources(c, project.ResourcesFull())
			if err != nil {
				c.LogError("Getting project resources in reCreate", "error", err)
				return errLogged.Wrap(err)
			}

			resources, ok := allResources[service]
			if !ok {
				c.LogError("No service", "service", service)
				return errLogged.Wrap(client.ErrNotFound.WithText("service not found"))
			}

			var inst *client.Instance
			for _, r := range resources {
				if r.Kind() == client.KindInstance {
					i, ok := r.(*client.Instance)
					if !ok {
						continue
					}

					if i.ServiceName() == service {
						inst = i
						break
					}
				}
			}

			if inst == nil {
				c.LogError("No instance for service", "service", service)
				return errLogged.Wrap(client.ErrNotFound.WithText("service instance not found"))
			}

			err = client.RunAction(ctx, inst, client.ActionEnsure)
			if err != nil {
				c.LogError("Failed to ensure instance", "name", inst.Name())
				return errLogged.Wrap(client.NewError("instance failed to ensure").WithResource(inst))
			}

			// Make sure we have full instance details
			if !inst.HasFull() {
				c.LogError("Instance missing full details", "name", inst.Name())
				return errLogged.Wrap(client.NewError("instance missing full details").WithResource(inst))
			}

			// Check instance running state
			instFull := inst.IncusInstanceFull
			if instFull == nil || instFull.State.Status != "Running" {
				c.LogError("Instance is not running", "name", inst.Name(), "status", func() string {
					if instFull == nil {
						return "unknown"
					}
					return instFull.State.Status
				}())
				return errLogged.Wrap(client.NewError("instance is not running").WithResource(inst))
			}

			// Build the effective command. For MVP we use a shell wrapper to support env and workdir.
			envs := cmd.StringSlice("env")
			workdir := cmd.String("workdir")
			noTty := cmd.Bool("no-tty") || (os.Getenv("NO_COLOR") != "" && !isatty.IsTerminal(os.Stderr.Fd()))
			detach := cmd.Bool("detach")
			dryRun := cmd.Bool("dry-run")

			// Start with original command joined; prefer preserving args by using sh -c with joined and escaped parts.
			// Simple escaping: join with space. This is an MVP and may not handle arbitrary complex args perfectly.
			joined := strings.Join(origCmd, " ")

			// Build env prefix
			envPrefix := ""
			if len(envs) > 0 {
				// envs are expected as KEY=VALUE
				escaped := make([]string, 0, len(envs))
				for _, e := range envs {
					// rudimentary escaping of single quotes
					escaped = append(escaped, strings.ReplaceAll(e, `'`, `'\''`))
				}
				envPrefix = "env " + strings.Join(escaped, " ")
			}

			// Prepend workdir if provided
			workPrefix := ""
			if workdir != "" {
				// rudimentary escaping
				workDirEsc := strings.ReplaceAll(workdir, `'`, `'\''`)
				workPrefix = fmt.Sprintf("cd '%s' && ", workDirEsc)
			}

			var execCommand []string
			if envPrefix != "" || workPrefix != "" {
				// Use sh -lc wrapper
				cmdStr := strings.TrimSpace(envPrefix + " " + workPrefix + joined)
				execCommand = []string{"sh", "-lc", cmdStr}
			} else {
				// No wrapper needed, run directly
				execCommand = origCmd
			}

			// Dry-run: print and exit
			if dryRun {
				out := cmd.Writer
				if out == nil {
					out = os.Stdout
				}
				_, _ = fmt.Fprintf(out, "DRY-RUN: would exec on %s (%s): %v\n", inst.Name(), inst.IncusName(), execCommand)
				return nil
			}

			// Determine TTY allocation: default allocate when stdin is a terminal and not explicitly disabled.
			interactive := !noTty && isatty.IsTerminal(os.Stdin.Fd())

			// Build Incus exec request.
			req := incusApi.InstanceExecPost{
				Command:     execCommand,
				WaitForWS:   true,
				Interactive: interactive,
			}

			// Prepare arguments for ExecInstance
			argsExec := incusClient.InstanceExecArgs{
				Stdin:    nil,
				Stdout:   nil,
				Stderr:   nil,
				DataDone: make(chan bool),
			}

			// If detached, do not attach to stdin/stdout/stderr and tell server we're not interactive.
			if detach {
				req.WaitForWS = false
				req.Interactive = false
				argsExec.Stdin = nil
				argsExec.Stdout = nil
				argsExec.Stderr = nil
			} else {
				// Attach to the local stdio, but honour cmd.Writer/ErrWriter so
				// non-interactive callers (tests, piped output) can capture output.
				stdout := io.Writer(os.Stdout)
				if cmd.Writer != nil && !interactive {
					stdout = cmd.Root().Writer
				}
				stderr := io.Writer(os.Stderr)
				if cmd.ErrWriter != nil && !interactive {
					stderr = cmd.Root().ErrWriter
				}
				argsExec.Stdin = os.Stdin
				argsExec.Stdout = stdout
				argsExec.Stderr = stderr
			}

			// If interactive TTY requested, put the local terminal into raw mode.
			var oldState *term.State
			restoreTTY := func() {
				if oldState != nil {
					_ = term.Restore(int(os.Stdin.Fd()), oldState)
				}
			}

			if req.Interactive && isatty.IsTerminal(os.Stdin.Fd()) {
				oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
				if err != nil {
					c.LogError("failed to set terminal raw mode", "error", err)
					return errLogged.Wrap(err)
				}
				// Ensure we restore terminal on exit
				defer restoreTTY()

				// Also ensure we restore on SIGINT/SIGTERM to avoid leaving terminal in raw mode.
				signals := make(chan os.Signal, 1)
				signal.Notify(signals, syscall.SIGINT)
				signal.Notify(signals, syscall.SIGTERM)
				go func() {
					select {
					case <-signals:
						restoreTTY()
					case <-ctx.Done():
						restoreTTY()
					}
				}()
			}

			// Perform the exec via the incus client.
			incusName := inst.IncusName()
			conn, err := c.Connection()
			if err != nil {
				return err
			}
			op, err := conn.ExecInstance(incusName, req, &argsExec)
			if err != nil {
				c.LogError("ExecInstance failed", "error", err)
				return errLogged.Wrap(err)
			}

			// Detached mode: we don't wait for websocket data; wait for the operation to be accepted.
			if detach {
				// Wait for the operation to reach a terminal state that indicates the server accepted the request.
				if err := op.Wait(); err != nil {
					c.LogError("detached exec failed", "error", err)
					return errLogged.Wrap(err)
				}
				// Print operation metadata / info for user visibility.
				out := cmd.Writer
				if out == nil {
					out = os.Stdout
				}
				_, _ = fmt.Fprintf(out, "Detached exec started on %s\n", inst.Name())
				return nil
			}

			// For attached (non-detach) mode, wait for I/O to complete and the operation to finish.
			// Wait for I/O completion signalled by DataDone channel or context cancellation.
			select {
			case <-argsExec.DataDone:
			case <-ctx.Done():
				// Ensure we wait on operation to clean up on server side.
				_ = op.Wait()
				return nil
			}

			// Wait for operation to complete and inspect exit code.
			if err := op.Wait(); err != nil {
				c.LogError("exec operation failed", "error", err)
				return errLogged.Wrap(err)
			}

			// Try extract exit code if present in metadata.
			opAPI := op.Get()
			if opAPI.Metadata != nil {
				if rc, ok := opAPI.Metadata["return"].(float64); ok {
					exitCode := int(rc)
					if exitCode != 0 {
						// Propagate non-zero exit as an error.
						return fmt.Errorf("command exited with code %d", exitCode)
					}
				}
			}

			return nil
		},
	}
}
