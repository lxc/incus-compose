package incuscompose

import (
	"fmt"
	"log/slog"
	"slices"

	"github.com/distribution/reference"
	"github.com/lxc/incus/v6/shared/api"
	incusConfig "github.com/lxc/incus/v6/shared/cliconfig"

	"gitlab.com/r3j0/incuscompose/pkg/icclient"
)

// UpOptions holds configuration for the up command.
type UpOptions struct {
	Options

	// NoGlobalImages enables images per project
	NoGlobalImages bool

	// Recreate containers even if they exist
	Recreate bool

	// Don't start containers after creating
	NoStart bool

	// Specific services to bring up (empty means all)
	Services []string

	// The default storage pool to use for all containers.
	DefaultStoragePool string
}

func (o *UpOptions) options() *Options {
	return &o.Options
}

var _ (OptionsType) = (*UpOptions)(nil)

// UpOption is a functional option for Up.
type UpOption func(*UpOptions)

// UpNoGlobalImages enables images per project.
func UpNoGlobalImages() UpOption {
	return func(o *UpOptions) {
		o.NoGlobalImages = true
	}
}

// UpRecreate forces container recreation even if they exist.
func UpRecreate() UpOption {
	return func(o *UpOptions) {
		o.Recreate = true
	}
}

// UpNoStart creates containers without starting them.
func UpNoStart() UpOption {
	return func(o *UpOptions) {
		o.NoStart = true
	}
}

// UpServices limits the operation to specific services.
func UpServices(services []string) UpOption {
	return func(o *UpOptions) {
		o.Services = services
	}
}

// UpDefaultStoragePool set the default storage pool if a volume does not provide one.
func UpDefaultStoragePool(pool string) UpOption {
	return func(o *UpOptions) {
		o.DefaultStoragePool = pool
	}
}

// NewUpOptions creates UpOptions with the given options applied.
func NewUpOptions(opts ...UpOption) UpOptions {
	res := UpOptions{
		Options: Options{
			Verbosity: DefaultVerbosity,
		},
		Services:           []string{},
		DefaultStoragePool: "default",
	}

	for _, o := range opts {
		o(&res)
	}

	return res
}

// Up creates and starts containers for a compose project.
//
// Image Resolution:
// Images are resolved to digest references and copied to the Incus server if needed.
// The conf parameter provides access to configured remotes for image sources.
//
// Volume Handling:
// Volumes are created after containers to read oci.uid/oci.gid for proper ownership.
// Named volumes use the configured storage pool, bind mounts enable UID shifting.
func Up(conf *incusConfig.Config, client *icclient.Client, project *Project, opts ...UpOption) error {
	options := NewUpOptions(opts...)

	_, err := client.EnsureProject(project.Name, icclient.EnsureProjectCreate())
	if err != nil {
		return err
	}

	// Get service order based on dependencies
	order, err := icclient.ServiceOrder(project, true)
	if err != nil {
		return fmt.Errorf("resolving service dependencies: %w", err)
	}

	// Filter services if specified
	if len(options.Services) > 0 {
		filtered := []string{}
		for _, svc := range order {
			if slices.Contains(options.Services, svc) {
				filtered = append(filtered, svc)
			}
		}
		order = filtered
	}

	images := map[string]*api.Image{}
	for _, serviceName := range order {
		svc := project.Services[serviceName]

		ref, err := icclient.ParseDockerRef(svc.Name, svc.Image)
		if err != nil {
			// TDDO(r3j0): Cleanup?
			return err
		}

		repo := reference.Domain(ref)
		if repo == "localhost" {
			// Handle podman style "localhost" images.
			repo = "local"

			// TODO(r3j0): Update `ref`!
		}

		imageServer, err := conf.GetImageServer(repo)
		if err != nil {
			return err
		}

		incusImage, err := client.EnsureImage(ref, imageServer, options.NoGlobalImages)
		if err != nil {
			return err
		}

		images[svc.Image] = incusImage
	}

	// Create networks
	networks := make(map[string]string, len(project.Networks))
	for networkName := range project.Networks {
		incusNetwork, err := client.EnsureNetwork(networkName)
		if err != nil {
			// TODO(r3j0): Cleanup!
			return err
		}

		networks[networkName] = incusNetwork
	}

	services := make(map[string]*api.Instance, len(order))

	// Create and start services in order
	for _, serviceName := range order {
		instance, eTag, err := client.EnsureService(project.Services[serviceName], client.GlobalIncus(), options.NoGlobalImages, false)
		if err != nil {
			// TODO(r3j0): Cleanup!
			return err
		}

		services[serviceName] = instance

		// Start container
		if !options.NoStart {
			slog.Info("Starting service", "service", serviceName)
			if err = client.StartInstance(instance, eTag, 0); err != nil {
				return fmt.Errorf("starting service %q: %w", serviceName, err)
			}
		}
	}

	return nil
}
