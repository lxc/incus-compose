package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-colorable"
	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/project"
)

// healthdParams holds the image/binary options for healthd setup.
type healthdParams struct {
	projectName string
	binary      string
	image       string // already resolved via resolveHealthdImage
	reCreate    bool
	network     string // Incus bridge name; empty = auto-detect
}

// projectUsesHealthd reports whether any of the named services declares a healthcheck.
// If services is empty, all project services are checked.
func projectUsesHealthd(p *project.Project) bool {
	for _, svc := range p.Services {
		// https://github.com/compose-spec/compose-spec/blob/main/05-services.md#restart
		if svc.Restart != "no" {
			return true
		}

		if svc.HealthCheck != nil {
			return true
		}
	}
	return false
}

// prepareHealthd resolves the sidecar image, builds HealthdConfig, and returns the
// registered *client.Healthd plus its *client.Image. The caller adds them to a stack
// or ensures them directly.
func prepareHealthd(globalClient *client.GlobalClient, c *client.Client, params healthdParams) ([]client.Resource, error) {
	imageName := params.image
	if params.binary != "" {
		imageName = "images:alpine/edge"
	}

	c.LogDebug("Using healthd image", "image", imageName)

	imageConfig := &client.ImageConfig{CliConfig: globalClient.CliConfig()}
	imgRes, err := c.Resource(client.KindImage, imageName, imageConfig)
	if err != nil {
		return nil, fmt.Errorf("getting the healthd image '%v': %w", imageName, err)
	}

	volRes, err := c.Resource(
		client.KindStorageVolume,
		"ic-healthd",
		&client.StorageVolumeConfig{Shifted: true, ImageResource: imgRes},
	)
	if err != nil {
		return nil, client.ErrUnknown.WithKindName(client.KindStorageVolume, "ic-healthd").Wrap(err)
	}
	volume, ok := volRes.(*client.StorageVolume)
	if !ok {
		return nil, client.ErrUnknown.WithResource(volRes)
	}

	img, ok := imgRes.(*client.Image)
	if !ok {
		return nil, client.ErrUnknown.WithResource(imgRes)
	}

	config := &client.HealthdConfig{
		StorageVolume: volume,
		ImageResource: img,
		Network:       params.network,
	}
	if params.binary != "" {
		config.Binary = params.binary
	}

	healthd, err := c.Resource(client.KindHealthd, fmt.Sprintf("%s-ic-healthd", params.projectName), config)
	if err != nil {
		return nil, fmt.Errorf("getting the healthd resource: %w", err)
	}

	return []client.Resource{img, volume, healthd}, nil
}

// resolveHealthd returns the existing Healthd resource or errors if the sidecar
// is not running. Used by management sub-commands that require ic-healthd to exist.
func resolveHealthd(c *client.Client) (*client.Healthd, error) {
	name, err := c.FindHealthdName()
	if err != nil {
		return nil, fmt.Errorf("finding healthd: %w", err)
	}
	if name == "" {
		return nil, errors.New("healthd is not running")
	}

	res, err := c.Resource(client.KindHealthd, name, &client.HealthdConfig{})
	if err != nil {
		return nil, err
	}
	h, ok := res.(*client.Healthd)
	if !ok {
		return nil, errors.New("unexpected resource type for healthd")
	}
	return h, nil
}

var healthdCommand = &cli.Command{
	Name:     "healthd",
	Usage:    "Manage the ic-healthd sidecar",
	Category: "extensions",
	Commands: []*cli.Command{
		healthdLogsCommand,
		healthdReloadCommand,
		healthdRestartCommand,
		healthdUpCommand,
		healthdDownCommand,
	},
}

var healthdLogsCommand = &cli.Command{
	Name:  "logs",
	Usage: "View output from the healthd sidecar",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "follow",
			Aliases: []string{"f"},
			Usage:   "Follow log output",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return errLogged.Wrap(err)
		}

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

		h, err := resolveHealthd(c)
		if err != nil {
			c.LogError(err.Error())
			return errLogged.Wrap(err)
		}

		var out io.Writer
		if f, ok := cmd.Root().Writer.(*os.File); ok {
			out = colorable.NewColorable(f)
		} else {
			out = cmd.Root().Writer
		}
		formatter := newLogFormatter(out, noColor)
		formatter.registerService(h.IncusName())
		globalClient.SetOutputHandler(formatter.write)

		var opts []client.Option
		if cmd.Bool("follow") {
			opts = append(opts, client.OptionFollow())
		}

		if err := h.Log(opts...); err != nil {
			c.LogError("Getting healthd logs", "error", err)
			return errLogged.Wrap(err)
		}

		formatter.flush()
		return nil
	},
}

var healthdReloadCommand = &cli.Command{
	Name:  "reload",
	Usage: "Send SIGHUP to the ic-healthd process",
	Action: func(ctx context.Context, cmd *cli.Command) error {
		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return errLogged.Wrap(err)
		}

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

		h, err := resolveHealthd(c)
		if err != nil {
			c.LogError(err.Error())
			return errLogged.Wrap(err)
		}

		if err := h.Reload(); err != nil {
			c.LogError("Reloading healthd", "error", err)
			return errLogged.Wrap(err)
		}

		return nil
	},
}

var healthdRestartCommand = &cli.Command{
	Name:  "restart",
	Usage: "Restart the ic-healthd sidecar",
	Flags: []cli.Flag{
		&cli.IntFlag{
			Name:  "timeout",
			Usage: "Timeout in seconds for stopping",
			Value: 10,
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return errLogged.Wrap(err)
		}

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

		h, err := resolveHealthd(c)
		if err != nil {
			c.LogError(err.Error())
			return errLogged.Wrap(err)
		}

		timeout := int(cmd.Int("timeout"))
		if err := h.Stop(client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
			c.LogWarn("Stopping healthd", "error", err)
		}
		if err := h.Start(); err != nil {
			c.LogError("Starting healthd", "error", err)
			return errLogged.Wrap(err)
		}

		return nil
	},
}

func mkHealthdStack(cmd *cli.Command, p *project.Project, globalClient *client.GlobalClient, c *client.Client) (*client.Stack, error) {
	// Register project resources so resolveIncusURL can find a network.
	if err := p.ToStack(c, client.NewStack(c)); err != nil {
		return nil, err
	}

	params := healthdParams{
		projectName: p.Name,
		binary:      cmd.String("binary"),
		image:       resolveHealthdImage(cmd.String("image")),
		reCreate:    cmd.Bool("recreate"),
		network:     cmd.String("network"),
	}

	healthdRes, err := prepareHealthd(globalClient, c, params)
	if err != nil {
		return nil, err
	}

	stack := client.NewStack(c)
	stack.Add(healthdRes...)

	return stack, nil
}

var healthdUpCommand = &cli.Command{
	Name:  "up",
	Usage: "Create or recreate the ic-healthd sidecar",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "recreate",
			Usage: "Recreate the sidecar even if it already exists",
		},
		&cli.StringFlag{
			Name:    "image",
			Usage:   `Healthd OCI image to use; {version} is replaced with the incus-compose version`,
			Value:   client.DefaultHealthdImage,
			Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_IMAGE"),
		},
		&cli.StringFlag{
			Name:    "binary",
			Usage:   "Path to local ic-healthd binary (uses images:alpine/edge instead of OCI image)",
			Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_BINARY"),
		},
		&cli.StringFlag{
			Name:    "network",
			Usage:   "Incus bridge for healthd to use (default: auto-detect)",
			Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_NETWORK"),
		},
		&cli.StringFlag{
			Name:  "pull",
			Usage: `Pull image before running ("always"|"missing"|"never"|"policy")`,
			Value: "policy",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return errLogged.Wrap(err)
		}

		if !projectUsesHealthd(p) {
			return fmt.Errorf("no service in this project declares a healthcheck")
		}

		c, err := globalClient.EnsureProject(
			p.Name,
			client.EnsureProjectWithCreate(),
			client.EnsureProjectWithConfig(p.ProjectConfig()),
		)
		if err != nil {
			globalClient.LogError("Getting the incus project", "error", err)
			return errLogged.Wrap(err)
		}
		if err := c.Open(); err != nil {
			globalClient.LogError("Opening the project client", "error", err)
			return errLogged.Wrap(err)
		}
		defer func() { _ = c.Done() }()

		if cmd.Bool("recreate") {
			stack, err := mkHealthdStack(cmd, p, globalClient, c)
			if err != nil {
				globalClient.LogError("Creating the stack", "error", err)
				return errLogged.Wrap(err)
			}

			c.LogDebug("Ensure", "resources", stack.All())

			if err := stack.ForAction(client.ActionEnsure).Run(client.ActionEnsure); err != nil {
				c.LogWarn("Ensuring healthd in recreate", "error", err)
			}
			if err := stack.ForAction(client.ActionDelete).Run(client.ActionDelete, client.OptionForce()); err != nil {
				c.LogDebug("Deleting healthd", "error", err)
			}
		}

		// Create a new stack after Delete as stack entries are now invalid.
		stack, err := mkHealthdStack(cmd, p, globalClient, c)
		if err != nil {
			globalClient.LogError("Creating the stack", "error", err)
			return errLogged.Wrap(err)
		}

		c.LogDebug("Ensure", "resources", stack.All())

		// Ensure with create. --pull=always refreshes cached images from registry.
		ensureOpts := []client.Option{client.OptionCreate()}
		if cmd.String("pull") == "always" {
			ensureOpts = append(ensureOpts, client.OptionPull())
		}

		if err := stack.ForAction(client.ActionEnsure).Run(client.ActionEnsure, ensureOpts...); err != nil {
			c.LogError("Creating healthd", "error", err)
			return errLogged.Wrap(err)
		}

		if err := stack.ForAction(client.ActionStart).Run(client.ActionStart); err != nil {
			c.LogError("Starting healthd", "error", err)
			return errLogged.Wrap(err)
		}

		return nil
	},
}

var healthdDownCommand = &cli.Command{
	Name:  "down",
	Usage: "Stop and remove the ic-healthd sidecar",
	Flags: []cli.Flag{
		&cli.IntFlag{
			Name:  "timeout",
			Usage: "Timeout in seconds for stopping",
			Value: 10,
		},
		&cli.StringFlag{
			Name:    "image",
			Usage:   `Healthd OCI image to use; {version} is replaced with the incus-compose version`,
			Value:   client.DefaultHealthdImage,
			Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_IMAGE"),
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return errLogged.Wrap(err)
		}

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

		stack, err := mkHealthdStack(cmd, p, globalClient, c)
		if err != nil {
			globalClient.LogError("Creating the stack", "error", err)
			return errLogged.Wrap(err)
		}

		c.LogDebug("Ensure", "resources", stack.All())

		if err := stack.ForAction(client.ActionEnsure).Run(client.ActionEnsure); err != nil {
			c.LogWarn("Ensuring healthd in recreate", "error", err)
		}

		timeout := int(cmd.Int("timeout"))
		if err := stack.ForAction(client.ActionDelete).Run(client.ActionDelete, client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
			c.LogDebug("Deleting healthd", "error", err)
		}

		return nil
	},
}
