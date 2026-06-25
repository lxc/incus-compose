package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/mattn/go-colorable"
	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

const (
	defaultHealthdImage       = "ghcr.io/lxc/incus-compose/ic-healthd:{version}"
	defaultHealthdCPULimit    = "1"
	defaultHealthdMemoryLimit = "50MB"
)

// healthdParams holds the image/binary options for healthd setup.
type healthdParams struct {
	projectName string
	binary      string
	image       string // already resolved via resolveHealthdImage
	pull        string
	reCreate    bool
	network     string // Incus bridge name; empty = auto-detect
	timeout     time.Duration
	stdout      io.Writer
	stderr      io.Writer
	workers     int
}

// closingBufferReader wraps bytes.Reader to add a no-op Close.
type closingBufferReader struct {
	*bytes.Reader
}

// Close is a noop.
func (cb *closingBufferReader) Close() error {
	return nil
}

// healthdCertName returns the name used for this project's healthd certificate in the Incus trust store.
func healthdCertName(c *client.Client) string {
	return "ic-healthd-" + c.IncusProject()
}

// healthdCreateToken creates a restricted token for the healthd to use.
func healthdCreateToken(c *client.Client) (string, error) {
	req := incusApi.CertificatesPost{
		CertificatePut: incusApi.CertificatePut{
			Name:       healthdCertName(c),
			Type:       "client",
			Restricted: true,
			Projects:   []string{c.IncusProject()},
		},
		Token: true,
	}

	conn, err := c.Connection()
	if err != nil {
		return "", err
	}

	op, err := conn.CreateCertificateToken(req)
	if err != nil {
		return "", err
	}

	opAPI := op.Get()
	addToken, err := opAPI.ToCertificateAddToken()
	if err != nil {
		return "", fmt.Errorf("converting operation to certificate add token: %w", err)
	}

	return addToken.String(), nil
}

// healthdRevokeCert removes the healthd's trust-store certificate, if any.
func healthdRevokeCert(c *client.Client) error {
	gConn, err := c.GlobalConnection()
	if err != nil {
		return fmt.Errorf("while getting a global connection: %w", err)
	}

	certs, err := gConn.GetCertificates()
	if err != nil {
		return fmt.Errorf("listing certificates: %w", err)
	}

	want := healthdCertName(c)
	for _, cert := range certs {
		if cert.Name != want {
			continue
		}
		if err := gConn.DeleteCertificate(cert.Fingerprint); err != nil {
			return fmt.Errorf("deleting certificate %s: %w", cert.Fingerprint, err)
		}
	}
	return nil
}

// healthdInUseByProject reports whether any service in the project requires ic-healthd:
// a declared healthcheck, a non-default restart policy, or a service_healthy depends_on.
func healthdInUseByProject(p *project.Project) bool {
	for _, svc := range p.Services {
		// https://github.com/compose-spec/compose-spec/blob/main/05-services.md#restart
		if svc.Restart != "" && svc.Restart != "no" {
			return true
		}

		if svc.HealthCheck != nil {
			return true
		}

		for _, dep := range svc.DependsOn {
			if dep.Condition == types.ServiceConditionHealthy {
				return true
			}
		}
	}
	return false
}

// healthdGetResources creates the image and volume resources for healthd and returns a
// configured (but not yet ensured) instance resource. The returned []client.Resource
// slice contains the image and volume; callers build a stack from it as needed.
func healthdGetResources(c *client.Client, params healthdParams) (*client.Instance, []client.Resource, error) {
	imageName := params.image
	if params.binary != "" {
		imageName = "images:alpine/edge"
	}

	imgRes, err := c.Resource(client.KindImage, imageName, &client.ImageConfig{})
	if err != nil {
		return nil, nil, fmt.Errorf("getting the healthd image '%v': %w", imageName, err)
	}

	volRes, err := c.Resource(
		client.KindStorageVolume,
		"ic-healthd",
		&client.StorageVolumeConfig{Shifted: true, ImageResource: imgRes},
	)
	if err != nil {
		return nil, nil, client.ErrUnknown.WithKindName(client.KindStorageVolume, "ic-healthd").Wrap(err)
	}

	volume, ok := volRes.(*client.StorageVolume)
	if !ok {
		return nil, nil, client.ErrUnknown.WithResource(volRes)
	}

	img, ok := imgRes.(*client.Image)
	if !ok {
		return nil, nil, client.ErrUnknown.WithResource(imgRes)
	}

	instanceConfig := &client.InstanceConfig{
		Image: imgRes.IncusName(),
		Type:  incusApi.InstanceTypeContainer,
		Config: map[string]string{
			"limits.cpu":              defaultHealthdCPULimit,
			"limits.memory":           defaultHealthdMemoryLimit,
			"user.internal":           "true",
			"user.healthcheck.daemon": "true",
		},
		Resources: []client.Resource{img},
	}

	instanceConfig.Devices = append(instanceConfig.Devices, client.InstanceDevice{
		Name: "data",
		Config: client.InstanceDeviceConfig{
			DeviceType: client.InstanceDeviceTypeDisk,
			Disk: client.InstanceDeviceDiskConfig{
				StorageVolumeConfig: &volume.Config,
				Source:              "ic-healthd",
				Path:                "/var/lib/ic-healthd",
				Shift:               true,
			},
		},
	})

	instRes, err := c.Resource(client.KindInstance, fmt.Sprintf("%s-ic-healthd", params.projectName), instanceConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("getting the healthd instance resource: %w", err)
	}

	inst, ok := instRes.(*client.Instance)
	if !ok {
		return nil, nil, client.ErrUnknown.WithResource(instRes)
	}

	return inst, []client.Resource{img, volume}, nil
}

// healthdUp generates a restricted Incus token, writes it (and optionally a local binary)
// into the instance via InstanceConfig.Files, ensures (creates) the instance, and starts it.
func healthdUp(ctx context.Context, c *client.Client, inst *client.Instance, resources []client.Resource, params healthdParams) error {
	if params.network == "" {
		network, err := c.Global().DefaultNetwork()
		if err != nil {
			return err
		}

		params.network = network
	}

	token, err := healthdCreateToken(c)
	if err != nil {
		c.LogWarn("Failed to get a token", "error", err)
		token = ""
	}

	if inst.Config.Files == nil {
		inst.Config.Files = make(map[string]client.InstanceFile)
	}

	inst.Config.Files["/etc/ic-healthd/token"] = client.InstanceFile{
		Content: &closingBufferReader{bytes.NewReader([]byte(token))},
		UID:     -1,
		GID:     -1,
		Mode:    0o400,
		DirMode: 0o700,
	}

	incusAPIURL := c.Config().URL
	if params.network != "" {
		inst.Config.Devices = append(inst.Config.Devices, client.InstanceDevice{
			Name: "eth0",
			Config: client.InstanceDeviceConfig{
				DeviceType:  client.InstanceDeviceTypeNic,
				NetworkName: params.network,
			},
		})

		conn, err := c.Connection()
		if err != nil {
			return err
		}

		network, _, err := conn.GetNetwork(params.network)
		if err != nil {
			return fmt.Errorf("failed to fetch the network: %w", err)
		}

		ipSplit := strings.Split(network.Config["ipv4.address"], "/")
		ip := net.ParseIP(ipSplit[0])
		if ip == nil {
			return fmt.Errorf("result is nil while parsing ip %q", ipSplit[0])
		}

		incusAPIURL = fmt.Sprintf("https://%s:8443", ip)
	}

	flags := []string{fmt.Sprintf(" --incus=%s --project=%s", incusAPIURL, c.IncusProject())}
	if c.IsDebugging() {
		flags = append(flags, " --debug")
	}

	if params.binary != "" {
		f, err := filepath.Abs(params.binary)
		if err != nil {
			return err
		}

		inst.Config.Files["/usr/local/bin/ic-healthd"] = client.InstanceFile{
			File:    f,
			UID:     -1,
			GID:     -1,
			Mode:    0o700,
			DirMode: 0o700,
		}
	} else {
		// c.LogDebug("Setting entrypoint")
		inst.Config.Config["oci.entrypoint"] = "/usr/local/bin/ic-healthd run" + strings.Join(flags, " ")
	}

	stack := client.NewStack(c, client.StackWorkers(params.workers))
	stack.Add(resources...)
	stack.Add(inst)

	c.LogDebug("Ensure", "resources", stack.All())

	ensureOpts := []client.Option{client.OptionCreate()}
	if params.pull == "always" {
		ensureOpts = append(ensureOpts, client.OptionPull())
	}

	if err := stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure, params.stdout, params.stderr, ensureOpts...); err != nil {
		c.LogError("Creating healthd resources", "error", err)
		return err
	}

	if err := stack.ForAction(client.ActionStart).Run(ctx, client.ActionStart, params.stdout, params.stderr); err != nil {
		c.LogError("Starting healthd resources", "error", err)
		return err
	}

	if params.binary != "" {
		cmd := []string{
			"sh", "-c",
			`nohup /usr/local/bin/ic-healthd run` + strings.Join(flags, " ") + `> /var/log/ic-healthd.log 2>&1 &`,
		}
		execReq := incusApi.InstanceExecPost{
			Command:     cmd,
			WaitForWS:   false,
			Interactive: false,
		}
		conn, err := c.Connection()
		if err != nil {
			return err
		}

		op, err := conn.ExecInstance(inst.IncusName(), execReq, nil)
		if err != nil {
			return err
		}
		if err := op.Wait(); err != nil {
			return err
		}
	}

	return healthdRegisterReloader(c, inst)
}

// healthdDown stops the instance, deletes it, and revokes its Incus trust certificate.
func healthdDown(ctx context.Context, c *client.Client, inst *client.Instance, resources []client.Resource, timeout time.Duration, stdout, stderr io.Writer) {
	stack := client.NewStack(c, client.StackSortDescending())

	for _, r := range resources {
		if r.Kind() != client.KindImage {
			stack.Add(r)
		}
	}
	stack.Add(inst)

	c.LogDebug("Ensure", "resources", stack.All())

	if err := stack.ForAction(client.ActionEnsure).Run(ctx, client.ActionEnsure, stdout, stderr); err != nil {
		c.LogWarn("Ensuring healthd", "error", err)
	}

	if err := stack.ForAction(client.ActionStop).Run(ctx, client.ActionStop, stdout, stderr, client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
		c.LogWarn("Stopping healthd resources", "error", err)
	}

	if err := stack.ForAction(client.ActionDelete).Run(ctx, client.ActionDelete, stdout, stderr, client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
		c.LogWarn("Deleting healthd resources", "error", err)
	}

	if err := healthdRevokeCert(c); err != nil {
		c.LogWarn("Cannot revoke the healthd cert", "error", err)
	}
}

// healthdResolve returns the existing healthd Instance or errors if the sidecar
// is not running. Used by management sub-commands that require ic-healthd to exist.
func healthdResolve(c *client.Client) (*client.Instance, error) {
	name, err := c.FindHealthd()
	if err != nil {
		return nil, fmt.Errorf("finding healthd: %w", err)
	}

	res, err := c.Resource(client.KindInstance, name, &client.InstanceConfig{})
	if err != nil {
		return nil, err
	}
	inst, ok := res.(*client.Instance)
	if !ok {
		return nil, errors.New("unexpected resource type for healthd")
	}
	return inst, nil
}

func healthdRegisterReloader(c *client.Client, h *client.Instance) error {
	mu := &sync.Mutex{}
	reloading := false

	c.AddHookAfter(func(ctx context.Context, action client.Action, r client.Resource, _ client.Options, err error) error {
		if err != nil || !r.IsEnsured() || r.Kind() != client.KindInstance {
			return err
		}

		inst, ok := r.(*client.Instance)
		if !ok || inst.IncusName() == h.IncusName() {
			return err
		}

		changed := false
		switch action {
		case client.ActionEnsure:
			changed = inst.Created()
		case client.ActionStart, client.ActionStop, client.ActionDelete:
			changed = true
		default:
			changed = false
		}

		if !changed || reloading {
			return err
		}

		mu.Lock()

		reloading = true

		conn, e := c.Connection()
		if e != nil {
			c.LogDebug("HealthdReloader connection failed, skipping reload", "healthd", h.IncusName(), "error", e)
			reloading = false
			mu.Unlock()
			return err
		}

		state, _, e := conn.GetInstanceState(h.IncusName())
		if e != nil {
			c.LogDebug("HealthdReloader healthd missing, skipping reload", "healthd", h.IncusName(), "error", e)
			reloading = false
			mu.Unlock()
			return err
		}
		if state.StatusCode != incusApi.Running {
			c.LogDebug("HealthdReloader healthd not running, skipping reload", "healthd", h.IncusName(), "status", state.Status)
			reloading = false
			mu.Unlock()
			return err
		}

		if e := healthdReload(c, h); e == nil {
			c.LogDebug("HealthdReloader reloaded healthd", "healthd", h.IncusName())
			reloading = false
			mu.Unlock()
			return err
		}

		c.LogWarn("Reloading healthd failed, restarting", "healthd", h.IncusName(), "error", e)
		err = errors.Join(err, e, h.Stop(ctx, client.OptionForce()), h.Start(ctx))
		reloading = false
		mu.Unlock()
		return err
	})

	return nil
}

func healthdReload(c *client.Client, h *client.Instance) error {
	req := incusApi.InstanceExecPost{
		Command:     []string{"sh", "-c", "pids=\"$(pidof ic-healthd)\" && for pid in $pids; do kill -HUP \"$pid\"; done"},
		WaitForWS:   true,
		Interactive: false,
	}

	conn, err := c.Connection()
	if err != nil {
		return err
	}

	op, err := conn.ExecInstance(h.IncusName(), req, nil)
	if err != nil {
		return err
	}

	return op.Wait()
}

func newHealthdCommand() *cli.Command {
	return &cli.Command{
		Name:     "healthd",
		Usage:    "Manage the ic-healthd sidecar",
		Category: "extensions",
		Commands: []*cli.Command{
			newHealthdLogsCommand(),
			newHealthdReloadCommand(),
			newHealthdRestartCommand(),
			newHealthdUpCommand(),
			newHealthdDownCommand(),
		},
	}
}

func newHealthdLogsCommand() *cli.Command {
	return &cli.Command{
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

			h, err := healthdResolve(c)
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

			if err := h.Ensure(ctx); err != nil {
				c.LogError("Ensuring healthd", "error", err)
				return errLogged.Wrap(err)
			}

			if err := h.Log(ctx, opts...); err != nil {
				c.LogError("Getting healthd logs", "error", err)
				return errLogged.Wrap(err)
			}

			formatter.flush()
			return nil
		},
	}
}

func newHealthdReloadCommand() *cli.Command {
	return &cli.Command{
		Name:  "reload",
		Usage: "Send SIGHUP to the ic-healthd process",
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

			// Render live progress for the ensure phase, where image downloads happen.
			finish := startProgress(globalClient, c, noColor, cmd.Root().Writer)

			h, err := healthdResolve(c)
			if err != nil {
				c.LogError(err.Error())
				finish(false)
				return errLogged.Wrap(err)
			}

			if err := h.Ensure(ctx); err != nil {
				c.LogError("Ensuring healthd", "error", err)
				finish(false)
				return errLogged.Wrap(err)
			}

			if err := healthdReload(c, h); err != nil {
				c.LogError("Reloading healthd", "error", err)
				finish(false)
				return errLogged.Wrap(err)
			}

			finish(true)
			return nil
		},
	}
}

func newHealthdRestartCommand() *cli.Command {
	return &cli.Command{
		Name:  "restart",
		Usage: "Restart the ic-healthd sidecar",
		Flags: []cli.Flag{
			&cli.DurationFlag{
				Name:  "timeout",
				Usage: "Timeout for stopping",
				Value: 10 * time.Second,
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

			// Render live progress for the ensure phase, where image downloads happen.
			finish := startProgress(globalClient, c, noColor, cmd.Root().Writer)

			h, err := healthdResolve(c)
			if err != nil {
				c.LogError(err.Error())
				finish(false)
				return errLogged.Wrap(err)
			}

			if err := h.Ensure(ctx); err != nil {
				c.LogError("Ensuring healthd", "error", err)
				finish(false)
				return errLogged.Wrap(err)
			}

			timeout := cmd.Duration("timeout")
			if err := h.Stop(ctx, client.OptionForce(), client.OptionTimeout(timeout)); err != nil {
				c.LogWarn("Stopping healthd", "error", err)
			}

			if err := h.Start(ctx); err != nil {
				c.LogError("Starting healthd", "error", err)
				finish(false)
				return errLogged.Wrap(err)
			}

			if err := healthdRegisterReloader(c, h); err != nil {
				c.LogError("Registering healthd reloader", "error", err)
				finish(false)
				return errLogged.Wrap(err)
			}

			finish(true)
			return nil
		},
	}
}

func newHealthdUpCommand() *cli.Command {
	return &cli.Command{
		Name:  "up",
		Usage: "Create or recreate the ic-healthd sidecar",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "image",
				Usage:   `Healthd OCI image to use; {version} is replaced with the incus-compose version`,
				Value:   defaultHealthdImage,
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
			&cli.BoolFlag{
				Name:  "recreate",
				Usage: "Recreate the sidecar even if it already exists",
			},
			&cli.StringFlag{
				Name:  "pull",
				Usage: `Pull image before running ("always"|"missing"|"never"|"policy")`,
				Value: "policy",
			},
			&cli.DurationFlag{
				Name:  "timeout",
				Usage: "Timeout for stopping",
				Value: 10 * time.Second,
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
				globalClient.LogError("Configuring the project", "error", err)
				return errLogged.Wrap(err)
			}

			if !healthdInUseByProject(p) {
				return fmt.Errorf("no service in this project declares a healthcheck")
			}

			params := healthdParams{
				projectName: p.Name,
				binary:      cmd.String("binary"),
				image:       resolveHealthdImage(cmd.String("image")),
				pull:        cmd.String("pull"),
				reCreate:    cmd.Bool("recreate"),
				network:     cmd.String("network"),
				timeout:     cmd.Duration("timeout"),
				stdout:      cmd.Root().Writer,
				stderr:      cmd.Root().ErrWriter,
				workers:     cmd.Root().Int("workers"),
			}

			c, err := globalClient.EnsureProject(
				p.Name,
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

			// Render live progress for the ensure phase, where image downloads happen.
			finish := startProgress(globalClient, c, noColor, cmd.Root().Writer)

			if params.reCreate {
				if existing, resources, err := healthdGetResources(c, params); err == nil {
					healthdDown(ctx, c, existing, resources, params.timeout, params.stdout, params.stderr)
				}

				c.ResetResources()
			}

			inst, resources, err := healthdGetResources(c, params)
			if err != nil {
				globalClient.LogError("Creating healthd resources", "error", err)
				finish(false)
				return errLogged.Wrap(err)
			}

			if err := healthdUp(ctx, c, inst, resources, params); err != nil {
				globalClient.LogError("Starting healthd", "error", err)
				finish(false)
				return errLogged.Wrap(err)
			}

			finish(true)
			return nil
		},
	}
}

func newHealthdDownCommand() *cli.Command {
	return &cli.Command{
		Name:  "down",
		Usage: "Stop and remove the ic-healthd sidecar",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "image",
				Usage:   `Healthd OCI image to use; {version} is replaced with the incus-compose version`,
				Value:   defaultHealthdImage,
				Sources: cli.EnvVars("INCUS_COMPOSE_HEALTHD_IMAGE"),
			},
			&cli.DurationFlag{
				Name:  "timeout",
				Usage: "Timeout for stopping",
				Value: 10 * time.Second,
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
				globalClient.LogError("Configuring the project", "error", err)
				return errLogged.Wrap(err)
			}

			params := healthdParams{
				projectName: p.Name,
				binary:      "",
				image:       resolveHealthdImage(cmd.String("image")),
				reCreate:    false,
				network:     "auto",
				timeout:     cmd.Duration("timeout"),
				stdout:      cmd.Root().Writer,
				stderr:      cmd.Root().ErrWriter,
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

			// Render live progress for the ensure phase, where image downloads happen.
			finish := startProgress(globalClient, c, noColor, cmd.Root().Writer)

			inst, resources, err := healthdGetResources(c, params)
			if err != nil {
				globalClient.LogError("Getting healthd resources", "error", err)
				finish(false)
				return errLogged.Wrap(err)
			}

			healthdDown(ctx, c, inst, resources, params.timeout, params.stdout, params.stderr)
			finish(true)
			return err
		},
	}
}
