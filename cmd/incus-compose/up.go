package main

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

func newUpCommand() *cli.Command {
	return &cli.Command{
		Name:      "up",
		Usage:     "Create and start containers",
		Category:  "compose",
		ArgsUsage: "[SERVICE...]",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "recreate",
				Usage: "Recreate containers even if they exist",
			},
			&cli.BoolFlag{
				Name:  "no-start",
				Usage: "Don't start containers after creating",
			},
			&cli.DurationFlag{
				Name:  "timeout",
				Usage: "Timeout for stopping/starting",
				Value: 1 * time.Minute,
			},
			&cli.DurationFlag{
				Name:  "dependency-timeout",
				Usage: "Max time to wait for service_healthy depends_on (0 = no limit)",
				Value: 5 * time.Minute,
			},
			&cli.StringSliceFlag{
				Name:  "scale",
				Usage: "Scale SERVICE to NUM instances (service=num)",
			},
			&cli.StringFlag{
				Name:  "pull",
				Usage: `Pull image before running ("always"|"missing"|"never"|"policy")`,
				Value: "policy",
			},
			&cli.BoolFlag{
				Name:  "build",
				Usage: "Build images before starting containers",
			},
			&cli.StringFlag{
				Name:    "builder",
				Usage:   "Preferred builder, binary name or absolute path. Empty for auto-detect.",
				Sources: cli.EnvVars("INCUS_COMPOSE_BUILDER"),
			},
			&cli.BoolFlag{
				Name:  "no-build",
				Usage: "Do not build images even if missing",
			},
			&cli.BoolFlag{
				Name:  "no-deps",
				Usage: "Don't start linked services",
			},
			&cli.BoolFlag{
				Name:    "detach",
				Aliases: []string{"d"},
				Usage:   "Detached mode: run containers in the background (a WIP)",
			},
			&cli.BoolFlag{
				Name:  "no-healthd",
				Usage: "Don't create healthd sidecar for healthchecks",
			},
			&cli.StringFlag{
				Name:    "healthd-image",
				Usage:   `Healthd OCI image to use; {version} is replaced with the incus-compose version`,
				Value:   defaultHealthdImage,
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_IMAGE"),
			},
			&cli.StringFlag{
				Name:    "healthd-binary",
				Usage:   "Path to local ic-healthd binary (uses images:alpine/edge instead of OCI image)",
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_BINARY"),
			},
			&cli.StringFlag{
				Name:    "healthd-incus",
				Usage:   `Connection URL of the incus to connect to from inside the sidecar. Empty = detect the ip from the bridge we are connected too`,
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_INCUS"),
			},
			&cli.StringFlag{
				Name:    "healthd-network",
				Usage:   "Incus bridge for healthd to use (default: auto-detect)",
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_NETWORK"),
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			noColor := noColor(ctx)

			globalClient, err := clientFromContext(ctx)
			if err != nil {
				return err
			}
			if err := globalClient.Connect(); err != nil {
				return err
			}

			p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
			if err != nil {
				globalClient.LogError("Loading the project", "error", err)
				return errLogged.Wrap(err)
			}

			c, err := globalClient.EnsureProject(
				p.Name,
				client.EnsureProjectWithCreate(),
				client.EnsureProjectWithConfig(p.ProjectConfig()),
			)
			defer func() {
				_ = c.Done()
			}()

			if err != nil {
				globalClient.LogError("Getting the incus project", "error", err)
				return errLogged.Wrap(err)
			}

			// Register the DNS Watcher
			if err := c.RegisterDNSWatcher(); err != nil {
				globalClient.LogError("Registering the DNS watcher", "project", p.Name, "error", err)
				return errLogged.Wrap(err)
			}

			if err := c.Open(); err != nil {
				globalClient.LogError("Opening the project client", "error", err)
				return errLogged.Wrap(err)
			}

			// Render live progress for the ensure phase, where image downloads happen.
			finish := startProgress(globalClient, c, noColor, cmd.Root().Writer)

			usesHealthd := !cmd.Bool("no-healthd")
			if usesHealthd && !healthdInUseByProject(globalClient, p) {
				usesHealthd = false
			}

			buildMode := client.BuildAuto
			if cmd.Bool("build") {
				buildMode = client.BuildForce
			} else if cmd.Bool("no-build") {
				buildMode = client.BuildNever
			}
			buildInfo := client.BuildInfo{
				Mode:             buildMode,
				PreferredBuilder: cmd.String("builder"),
			}

			runOptions := []client.Option{client.OptionTimeout(cmd.Duration("timeout"))}
			// With --no-deps the linked services are out of scope, so don't wait on
			// healthd dependency conditions (depends_on: service_healthy) that maybe can't
			// be satisfied because those dependencies were never started.
			if !usesHealthd || cmd.Bool("no-deps") {
				runOptions = append(runOptions, client.OptionNoHealthd())
			}

			if cmd.Bool("recreate") {
				stack := client.NewStack(c, client.StackWorkers(cmd.Root().Int("workers")))
				toStackOpts := []project.ToStackOption{}
				toStackOpts = append(toStackOpts, project.ToStackNoImages(), project.ToStackReverse(), project.ToStackOnlyServices(cmd.Args().Slice()))
				if !cmd.Bool("no-deps") {
					toStackOpts = append(toStackOpts, project.ToStackWithDeps())
				}

				scale := parseScale(cmd.StringSlice("scale"))
				if len(scale) > 0 {
					toStackOpts = append(toStackOpts, project.ToStackScale(scale))
				}
				err := p.ToStack(c, stack, toStackOpts...)
				if err != nil {
					c.LogError("Creating the stack in reCreate", "error", err)
					return errLogged.Wrap(err)
				}

				c.LogDebug("Ensure", "resources", stack.All())

				recreateOptions := append(runOptions, client.OptionForce())

				// Do not recreate networks.
				recreateFilter := func(r client.Resource) bool {
					if r.Kind() == client.KindNetwork {
						return false
					}
					return true
				}

				// Ensure without create for "recreate" (resolution only, no progress).
				if err := stack.ForActionF(client.ActionEnsure, recreateFilter).Run(ctx, client.ActionEnsure, cmd.Root().Writer, cmd.Root().ErrWriter); err != nil {
					c.LogDebug("Ensuring for reCreate", "error", err)
				} else {
					// Stop
					errStop := stack.ForActionF(client.ActionStop, recreateFilter).Run(ctx, client.ActionStop, cmd.Root().Writer, cmd.Root().ErrWriter, recreateOptions...)
					if errStop != nil {
						c.LogDebug("Stopping resources", "error", errStop)
					}

					// Delete
					deleteStack := stack.ForActionF(client.ActionDelete, recreateFilter)
					c.LogDebug("Recreate delete", "resources", deleteStack.All())
					errDel := deleteStack.Run(ctx, client.ActionDelete, cmd.Root().Writer, cmd.Root().ErrWriter, recreateOptions...)
					if errDel != nil {
						c.LogDebug("Deleting resources", "error", errDel)
					}
				}

				// Start fresh after recreate
				c.ResetResources()
			}

			stack := client.NewStack(c, client.StackWorkers(cmd.Root().Int("workers")))
			toStackOpts := []project.ToStackOption{}
			toStackOpts = append(toStackOpts, project.ToStackStorageVolumes(), project.ToStackOnlyServices(cmd.Args().Slice()))
			if !cmd.Bool("no-deps") {
				toStackOpts = append(toStackOpts, project.ToStackWithDeps())
			}
			scale := parseScale(cmd.StringSlice("scale"))
			if len(scale) > 0 {
				toStackOpts = append(toStackOpts, project.ToStackScale(scale))
			}
			err = p.ToStack(c, stack, toStackOpts...)
			if err != nil {
				c.LogError("Adding the project to a stack", "error", err)
				return errLogged.Wrap(err)
			}

			if usesHealthd {
				healthdIncus, healthdNetwork := p.HealthdConfig()
				if cmd.String("healthd-incus") != "" {
					healthdIncus = cmd.String("healthd-incus")
				}
				if cmd.String("healthd-network") != "" {
					healthdNetwork = cmd.String("healthd-network")
				}

				var (
					incus *url.URL
					err   error
				)
				if healthdIncus != "" {
					incus, err = url.Parse(healthdIncus)
					if err != nil {
						globalClient.LogError("Parsing the URL given with `--healthd-incus` failed", "error", err)
						return errLogged.Wrap(errors.New("parsing error"))
					}
				}

				hparams := healthdParams{
					projectName: p.Name,
					binary:      cmd.String("healthd-binary"),
					image:       resolveHealthdImage(cmd.String("healthd-image")),
					pull:        cmd.String("pull"),
					reCreate:    cmd.Bool("recreate"),
					incus:       incus,
					network:     healthdNetwork,
					timeout:     cmd.Duration("timeout"),
					stdout:      cmd.Root().Writer,
					stderr:      cmd.Root().ErrWriter,
					workers:     cmd.Root().Int("workers"),
				}

				inst, resources, err := healthdGetResources(c, hparams)
				if err != nil {
					globalClient.LogError("Creating healthd resources", "error", err)

					finish(err == nil)
					return errLogged.Wrap(err)
				}

				stack.Add(resources...)
				stack.Add(inst)
			}

			c.LogDebug("Ensure", "resources", stack.All())

			// Ensure with create. --pull=always refreshes cached images from registry.
			// policy and missing only use the local cache (pull if not present).
			startOptions := append(runOptions, client.OptionCreate())
			if cmd.String("pull") == "always" {
				startOptions = append(startOptions, client.OptionPull())
			}
			if buildInfo.Mode != client.BuildAuto || buildInfo.PreferredBuilder != "" {
				startOptions = append(startOptions, client.OptionBuild(buildInfo))
			}
			if cmd.Duration("dependency-timeout") > 0 {
				startOptions = append(startOptions, client.OptionDependencyTimeout(cmd.Duration("dependency-timeout")))
			}

			err = stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure, cmd.Root().Writer, cmd.Root().ErrWriter, startOptions...)
			if err != nil {
				c.LogError("Ensuring resources", "error", err)
				return errLogged.Wrap(err)
			}

			// Start
			if !cmd.Bool("no-start") {
				startFilter := func(r client.Resource) bool { return r.IsEnsured() }

				if err := stack.ForActionF(client.ActionStart, startFilter).Run(ctx, client.ActionStart, cmd.Root().Writer, cmd.Root().ErrWriter, startOptions...); err != nil {
					c.LogError("Starting resources", "error", err)
					return errLogged.Wrap(err)
				}
			}

			_ = c.Done()

			finish(err == nil)

			return nil
		},
	}
}

// parseScale parses --scale flags of the form "service=num".
func parseScale(values []string) map[string]int {
	scaleOverrides := make(map[string]int)
	for _, s := range values {
		parts := strings.SplitN(s, "=", 2)
		if len(parts) == 2 {
			if n, err := strconv.Atoi(parts[1]); err == nil {
				scaleOverrides[parts[0]] = n
			}
		}
	}
	return scaleOverrides
}
