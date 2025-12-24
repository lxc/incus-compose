package incuscompose

import (
	"errors"
	"fmt"
	"slices"

	"gitlab.com/r3j0/incuscompose/pkg/icclient"
)

// DownOptions holds configuration for the down command.
type DownOptions struct {
	Options

	// Remove named volumes
	Volumes bool

	// Remove networks
	RemoveNetworks bool

	// Timeout for stopping containers (seconds)
	Timeout int

	// Specific services to bring down (empty means all)
	Services []string
}

func (o *DownOptions) options() *Options {
	return &o.Options
}

var _ (OptionsType) = (*DownOptions)(nil)

// DownOption is a functional option for Down.
type DownOption func(*DownOptions)

// DownVolumes removes named volumes when bringing down the project.
func DownVolumes() DownOption {
	return func(o *DownOptions) {
		o.Volumes = true
	}
}

// DownRemoveNetworks removes networks when bringing down the project.
func DownRemoveNetworks() DownOption {
	return func(o *DownOptions) {
		o.RemoveNetworks = true
	}
}

// DownTimeout sets the timeout in seconds for stopping containers.
func DownTimeout(timeout int) DownOption {
	return func(o *DownOptions) {
		o.Timeout = timeout
	}
}

// DownServices limits the operation to specific services.
func DownServices(services []string) DownOption {
	return func(o *DownOptions) {
		o.Services = services
	}
}

// NewDownOptions creates DownOptions with the given options applied.
func NewDownOptions(opts ...DownOption) DownOptions {
	res := DownOptions{
		Options: Options{
			Verbosity: DefaultVerbosity,
		},
		Timeout:        10,
		RemoveNetworks: true,
		Services:       []string{},
	}

	for _, o := range opts {
		o(&res)
	}

	return res
}

// Down stops and removes containers for a compose project.
func Down(client *icclient.Client, project *Project, opts ...DownOption) error {
	options := NewDownOptions(opts...)

	// Switch client to the project.
	if err := client.UseProject(project.Name); err != nil {
		return err
	}

	// Get service order - reverse of start order (dependents first)
	order, err := icclient.ServiceOrder(project, false)
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

	var errs error

	// Stop and remove containers in reverse order
	for _, serviceName := range order {
		service, ok := project.Services[serviceName]
		if !ok {
			continue
		}

		// Check if container exists
		existing, eTag, err := client.InstanceFromService(service)
		if err != nil {
			// Container doesn't exist, skip
			client.Logger().Debug("Instance not found", "service", service.Name)
			continue
		}

		// Remove container
		client.Logger().Info("Removing instance for service", "service", service.Name, "instance", existing.Name)
		if err := client.RemoveInstance(existing, eTag, true); err != nil {
			errs = errors.Join(errs, err)
		}
	}

	// Remove volumes if requested
	if options.Volumes {
		for volName := range project.Volumes {
			client.Logger().Info("Removing pool volume", "name", volName)
			if err := client.RemovePoolVolume(volName, ""); err != nil {
				errs = errors.Join(errs, err)
			}
		}
	}

	// Remove networks if requested
	if options.RemoveNetworks {
		for netName := range project.Networks {
			client.Logger().Info("Removing network", "name", netName)
			if err := client.RemoveNetwork(netName); err != nil {
				errs = errors.Join(errs, err)
			}
		}
	}

	return errs
}
