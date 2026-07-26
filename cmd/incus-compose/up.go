package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
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
				Usage: "Recreate containers by deleting them first",
			},
			&cli.BoolFlag{
				Name:    "no-start",
				Usage:   "Don't start containers after creating",
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_NO_START"),
			},
			&cli.DurationFlag{
				Name:    "timeout",
				Usage:   "Timeout for stopping/starting a service",
				Value:   2 * time.Minute,
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_TIMEOUT"),
			},
			&cli.DurationFlag{
				Name:    "dependency-timeout",
				Usage:   "Max time to wait for service_healthy depends_on (0 = no limit)",
				Value:   5 * time.Minute,
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_DEPENDENCY_TIMEOUT"),
			},
			&cli.StringSliceFlag{
				Name:    "scale",
				Usage:   "Scale SERVICE to NUM instances (service=num)",
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_SCALE"),
			},
			&cli.StringFlag{
				Name:    "pull",
				Usage:   `Pull image before running ("always"|"missing"|"never"|"policy")`,
				Value:   "policy",
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_PULL"),
			},
			&cli.BoolFlag{
				Name:    "build",
				Usage:   "Build images before starting containers",
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_BUILD"),
			},
			&cli.StringFlag{
				Name:    "builder",
				Usage:   "Preferred builder, binary name or absolute path. Empty for auto-detect.",
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_BUILDER"),
			},
			&cli.BoolFlag{
				Name:    "no-build",
				Usage:   "Do not build images even if missing",
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_NO_BUILD"),
			},
			&cli.BoolFlag{
				Name:    "no-deps",
				Usage:   "Don't start linked services",
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_NO_DEPS"),
			},
			&cli.BoolFlag{
				Name:    "detach",
				Aliases: []string{"d"},
				Usage:   "Detached mode: run containers in the background (a WIP)",
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_DETACH"),
			},
			&cli.BoolFlag{
				Name:    "no-healthd",
				Usage:   "Don't create healthd sidecar for healthchecks",
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_NO_HEALTHD"),
			},
			&cli.BoolFlag{
				Name:    "external-healthd",
				Usage:   "Use healthd but do not try to create or lookup it",
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_EXTERNAL_HEALTHD"),
			},
			&cli.StringFlag{
				Name:    "healthd-image",
				Usage:   `Healthd OCI image to use; {version} is replaced with the incus-compose version`,
				Value:   DefaultHealthdImage,
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_HEALTHD_IMAGE"),
			},
			&cli.StringFlag{
				Name:    "healthd-binary",
				Usage:   "Path to local ic-healthd binary (uses images:alpine/edge instead of OCI image)",
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_HEALTHD_BINARY"),
			},
			&cli.StringFlag{
				Name:    "healthd-incus",
				Usage:   `Connection URL of the incus to connect to from inside the sidecar. Empty = detect the ip from the bridge we are connected too`,
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_HEALTHD_INCUS"),
			},
			&cli.StringFlag{
				Name:    "healthd-network",
				Usage:   "Incus bridge for healthd to use (default: auto-detect)",
				Sources: cli.EnvVars("INCUS_COMPOSE_UP_HEALTHD_NETWORK"),
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

			if cmd.Args().Len() > 0 {
				for _, s := range cmd.Args().Slice() {
					_, ok := p.Services[s]
					if !ok {
						err := client.ErrNotFound.WithKindName(client.KindInstance, s)
						globalClient.LogError("Service not found", "service", s)
						return errLogged.Wrap(err)
					}
				}
			}

			if cmd.Bool("external-healthd") {
				p.ClientConfig.Healthd.External = true
			}

			c, err := globalClient.EnsureProject(
				p.Name,
				client.EnsureProjectWithCreate(),
				client.EnsureProjectWithConfig(p.ClientConfig.XIncus),
			)
			if err != nil {
				globalClient.LogError("Getting the incus project", "error", err)
				return errLogged.Wrap(err)
			}
			defer c.WarnError(c.Done, "Failure during Client.Done()")

			err = c.Open()
			if err != nil {
				globalClient.LogError("Opening the project client", "error", err)
				return errLogged.Wrap(err)
			}

			stdout := cmd.Root().Writer
			stderr := cmd.Root().ErrWriter

			// We start all resources, just ignore that warning but let progress know them (so add before - LIFO - progress runs before).
			c.IgnoreError(client.ActionStart, client.ErrRunning)
			c.IgnoreError(client.ActionStop, client.ErrNotRunning)

			// The recreate client has own errors it ignores.
			rc := c.Clone()
			rc.IgnoreError(client.ActionStop, client.ErrNotEnsured)
			rc.IgnoreError(client.ActionDelete, client.ErrNotEnsured)
			rc.IgnoreError(client.ActionDelete, client.ErrNotFound)

			// Register the DNS Watcher after the progress renderer so progress waits for the dns changes.
			err = c.RegisterDNSWatcher()
			if err != nil {
				globalClient.LogError("Registering the DNS watcher", "project", p.Name, "error", err)
				return errLogged.Wrap(err)
			}

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
			if !p.ClientConfig.Healthd.External && !usesHealthd {
				runOptions = append(runOptions, client.OptionNoHealthd())
			}

			if p.ClientConfig.Healthd.External {
				runOptions = append(runOptions, client.OptionExternalHealthd())
			}

			if cmd.Bool("recreate") {
				var rprogress *progressRenderer
				if !cmd.Bool("debug") {
					rprogress = newProgressRenderer(stdout, noColor, isatty.IsTerminal(os.Stdout.Fd()))
					rprogress.Start(rc)
				}

				scale := parseScale(cmd.StringSlice("scale"))
				resources, err := p.Resources(rc, project.ResourcesScale(scale))
				if err != nil {
					rc.LogError("Getting project resources in reCreate", "error", err)
					if rprogress != nil {
						rprogress.Stop(rc)
					}
					return errLogged.Wrap(err)
				}

				order, err := p.ServiceOrder(true)
				if err != nil {
					rc.LogError("Getting the service dependency order", "error", err)
					if rprogress != nil {
						rprogress.Stop(rc)
					}
					return errLogged.Wrap(err)
				}

				// The client needs to know about all instances for DNSWatcher as well as networks for healthd, even those we filter out later.
				ensureStack := client.NewStack(rc, client.StackSortDescending(), client.StackWorkers(cmd.Root().Int("workers")))
				args := filterResourcesArgs{
					ExcludeKinds: []client.Kind{client.KindImage, client.KindStorageVolume},
				}
				myResources := filterResources(p, resources, args)
				ensureStack.AddOrdered(order, myResources)

				args = filterResourcesArgs{
					OnlyServices:     cmd.Args().Slice(),
					WithDependencies: !cmd.Bool("no-deps"),
					ExcludeKinds:     []client.Kind{client.KindImage, client.KindNetwork, client.KindStorageVolume},
				}
				myResources = filterResources(p, resources, args)

				stack := client.NewStack(rc, client.StackSortDescending(), client.StackWorkers(cmd.Root().Int("workers")))
				stack.AddOrdered(order, myResources)

				rc.LogDebug("Ensure", "resources", stack.All())

				recreateOptions := append(append([]client.Option{}, runOptions...), client.OptionForce())

				// Ensure without create for "recreate" (resolution only, no progress).
				if err := ensureStack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure, stdout, stderr); err != nil {
					rc.LogDebug("Ensuring for reCreate", "error", err)
				} else {
					// Stop
					errStop := stack.ForAction(client.ActionStop).Run(ctx, client.ActionStop, stdout, stderr, recreateOptions...)
					if errStop != nil {
						rc.LogDebug("Stopping resources", "error", errStop)
					}

					// Delete
					deleteStack := stack.ForAction(client.ActionDelete)
					rc.LogDebug("Recreate delete", "resources", deleteStack.All())
					errDel := deleteStack.Run(ctx, client.ActionDelete, stdout, stderr, recreateOptions...)
					if errDel != nil {
						rc.LogDebug("Deleting resources", "error", errDel)
					}
				}

				if rprogress != nil {
					rprogress.Stop(rc)
				}
			}

			if !cmd.Root().Bool("debug") {
				progress := newProgressRenderer(stdout, noColor, isatty.IsTerminal(os.Stdout.Fd()))
				progress.Start(c)
				defer progress.Stop(c)

				stdout = progress.bypass()
				stderr = stdout
			}

			scale := parseScale(cmd.StringSlice("scale"))
			resources, err := p.Resources(c, project.ResourcesScale(scale))
			if err != nil {
				c.LogError("Getting project resources in reCreate", "error", err)
				return errLogged.Wrap(err)
			}

			order, err := p.ServiceOrder(false)
			if err != nil {
				c.LogError("Getting the service dependency order", "error", err)
				return errLogged.Wrap(err)
			}

			args := filterResourcesArgs{
				OnlyServices:     cmd.Args().Slice(),
				WithDependencies: !cmd.Bool("no-deps"),
			}
			myResources := filterResources(p, resources, args)

			stack := client.NewStack(c, client.StackWorkers(cmd.Root().Int("workers")), client.StackFailFast())
			stack.AddOrdered(order, myResources)

			if usesHealthd && !cmd.Bool("external-healthd") {
				healthdIncus := p.ClientConfig.Healthd.Incus
				healthdNetwork := p.ClientConfig.Healthd.Network
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
					incus:       incus,
					network:     healthdNetwork,
					timeout:     cmd.Duration("timeout"),
					workers:     cmd.Root().Int("workers"),
				}

				hInst, hResources, err := healthdGetResources(c, hparams)
				if err != nil {
					globalClient.LogError("Creating healthd resources", "error", err)
					return errLogged.Wrap(err)
				}

				stack.Add(hResources...)
				stack.Add(hInst)
			}

			c.LogDebug("Ensure", "resources", stack.All())

			// Ensure with create. --pull=always refreshes cached images from registry.
			// policy and missing only use the local cache (pull if not present).
			startOptions := append(append([]client.Option{}, runOptions...), client.OptionCreate())
			if cmd.String("pull") == "always" {
				startOptions = append(startOptions, client.OptionPull())
			}
			if buildInfo.Mode != client.BuildAuto || buildInfo.PreferredBuilder != "" {
				startOptions = append(startOptions, client.OptionBuild(buildInfo))
			}
			if cmd.Duration("dependency-timeout") > 0 {
				startOptions = append(startOptions, client.OptionDependencyTimeout(cmd.Duration("dependency-timeout")))
			}

			err = stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure, stdout, stderr, startOptions...)
			if err != nil {
				c.LogError("Ensuring resources", "error", err)
				return errLogged.Wrap(err)
			}

			// Start
			if !cmd.Bool("no-start") {
				startFilter := func(r client.Resource) bool { return r.IsEnsured() }

				err := stack.ForActionF(client.ActionStart, startFilter).Run(ctx, client.ActionStart, stdout, stderr, startOptions...)
				if err != nil {
					c.LogError("Starting resources", "error", err)
					return errLogged.Wrap(err)
				}
			}

			_ = c.Done()

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
