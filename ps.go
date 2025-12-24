package incuscompose

import (
	"fmt"
	"io"
	"os"
	"slices"
	"text/tabwriter"

	"gitlab.com/r3j0/incuscompose/pkg/icclient"
)

// PsOptions holds configuration for the ps command.
type PsOptions struct {
	Options

	// Output writer (defaults to stdout)
	Output io.Writer

	// Show all containers (including stopped)
	All bool

	// Specific services to show (empty means all)
	Services []string

	// Output format: table, json
	Format string
}

func (o *PsOptions) options() *Options {
	return &o.Options
}

var _ (OptionsType) = (*PsOptions)(nil)

// PsOption is a functional option for Ps.
type PsOption func(*PsOptions)

// PsOutput sets the output writer for ps results.
func PsOutput(w io.Writer) PsOption {
	return func(o *PsOptions) {
		o.Output = w
	}
}

// PsAll includes stopped containers in the output.
func PsAll() PsOption {
	return func(o *PsOptions) {
		o.All = true
	}
}

// PsServices limits output to specific services.
func PsServices(services []string) PsOption {
	return func(o *PsOptions) {
		o.Services = services
	}
}

// PsFormat sets the output format (table or json).
func PsFormat(format string) PsOption {
	return func(o *PsOptions) {
		o.Format = format
	}
}

// NewPsOptions creates PsOptions with the given options applied.
func NewPsOptions(opts ...PsOption) PsOptions {
	res := PsOptions{
		Options: Options{
			Verbosity: DefaultVerbosity,
		},
		Output:   os.Stdout,
		Format:   "table",
		Services: []string{},
	}

	for _, o := range opts {
		o(&res)
	}

	return res
}

// ContainerStatus holds the status of a single container.
type ContainerStatus struct {
	Service   string
	Container string
	Image     string
	Status    string
	Addresses []string
}

// Ps lists the status of containers for a compose project.
func Ps(client *icclient.Client, project *Project, opts ...PsOption) error {
	options := NewPsOptions(opts...)

	// Switch client to the project.
	if err := client.UseProject(project.Name); err != nil {
		return err
	}

	var statuses []ContainerStatus

	for serviceName, service := range project.Services {
		// Filter by service if specified
		if len(options.Services) > 0 {
			found := slices.Contains(options.Services, serviceName)
			if !found {
				continue
			}
		}

		containerName := containerNameForService(&service)

		status := ContainerStatus{
			Service:   serviceName,
			Container: containerName,
			Image:     service.Image,
			Status:    "Not created",
			Addresses: []string{},
		}

		// Get container info
		inst, _, err := client.Incus().GetInstanceFull(containerName)
		if err == nil {
			status.Status = inst.State.Status

			// Get IP addresses
			for _, network := range inst.State.Network {
				for _, addr := range network.Addresses {
					if addr.Family == "inet" && addr.Scope == "global" {
						status.Addresses = append(status.Addresses, addr.Address)
					}
				}
			}
		}

		// Skip stopped containers unless --all
		if !options.All && status.Status != "Running" {
			continue
		}

		statuses = append(statuses, status)
	}

	// Print if output is set
	if options.Output != nil && options.Format == "table" {
		printStatusTable(options.Output, statuses)
	}

	return nil
}

func printStatusTable(w io.Writer, statuses []ContainerStatus) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SERVICE\tCONTAINER\tIMAGE\tSTATUS\tADDRESSES")

	for _, s := range statuses {
		addrs := ""
		if len(s.Addresses) > 0 {
			addrs = s.Addresses[0]
			for _, a := range s.Addresses[1:] {
				addrs += ", " + a
			}
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			s.Service,
			s.Container,
			s.Image,
			s.Status,
			addrs,
		)
	}

	_ = tw.Flush()
}
