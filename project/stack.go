package project

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/dominikbraun/graph"

	"github.com/lxc/incus-compose/client"
)

// ServiceGraph returns services in dependency order using topological sort.
// If reverse is true, returns reverse order (useful for shutdown).
func ServiceGraph(serviceConfigs types.Services, reverse bool) ([]string, error) {
	g := graph.New(graph.StringHash, graph.Directed(), graph.PreventCycles())

	// Add vertices
	for s := range maps.Values(serviceConfigs) {
		_ = g.AddVertex(s.Name)
	}

	// Add edges for dependencies that are in scope.
	for s := range maps.Values(serviceConfigs) {
		for dep := range s.DependsOn {
			if _, ok := serviceConfigs[dep]; !ok {
				continue
			}
			// Edge from dependency to dependent (dep must start before n)
			err := g.AddEdge(dep, s.Name)
			if err != nil && err != graph.ErrEdgeAlreadyExists {
				return nil, fmt.Errorf("adding dependency edge %s -> %s: %w", dep, s.Name, err)
			}
		}
	}

	order, err := graph.TopologicalSort(g)
	if err != nil {
		return nil, fmt.Errorf("topological sort: %w", err)
	}

	if reverse {
		slices.Reverse(order)
	}

	return order, nil
}

// ToStackOptions configures how services are converted to stack operations.
type ToStackOptions struct {
	OnlyServices   []string
	Reverse        bool
	Full           bool
	NoImages       bool
	StorageVolumes bool
	InstancesOnly  bool
	ImagesOnly     bool
	Deps           bool
	Scale          map[string]int // service name -> replica count override
}

// ToStackOption is a functional option for ToStack.
type ToStackOption func(o *ToStackOptions)

// ToStackOnlyServices limits the stack to the specified services.
func ToStackOnlyServices(services []string) ToStackOption {
	return func(o *ToStackOptions) {
		o.OnlyServices = services
	}
}

// ToStackReverse reverses the service dependency graph order.
// Use for teardown so dependants are stopped before their dependencies.
// Note: cross-kind priority ordering (e.g. instances vs networks) is handled
// automatically by Stack.ForAction and does not require this option.
func ToStackReverse() ToStackOption {
	return func(o *ToStackOptions) {
		o.Reverse = true
	}
}

// ToStackFull fetches complete instance state including image alias and full instance details.
func ToStackFull() ToStackOption {
	return func(o *ToStackOptions) {
		o.Full = true
	}
}

// ToStackNoImages doesn't add images to the stack.
func ToStackNoImages() ToStackOption {
	return func(o *ToStackOptions) {
		o.NoImages = true
	}
}

// ToStackWithDeps expands the OnlyServices selection to include linked services:
// in start direction the services a selected one depends on, and in reverse
// (stop) direction the services that depend on a selected one. Without it the
// stack is limited to exactly the selected services.
func ToStackWithDeps() ToStackOption {
	return func(o *ToStackOptions) {
		o.Deps = true
	}
}

// ToStackStorageVolumes adds storage volumes to the stack.
func ToStackStorageVolumes() ToStackOption {
	return func(o *ToStackOptions) {
		o.StorageVolumes = true
	}
}

// ToStackInstancesOnly configures ToStack to only add instances to the stack.
func ToStackInstancesOnly() ToStackOption {
	return func(o *ToStackOptions) {
		o.InstancesOnly = true
	}
}

// ToStackImagesOnly configures ToStack to only add images to the the stack.
func ToStackImagesOnly() ToStackOption {
	return func(o *ToStackOptions) {
		o.ImagesOnly = true
	}
}

// ToStackScale sets replica count overrides for services.
func ToStackScale(scale map[string]int) ToStackOption {
	return func(o *ToStackOptions) {
		o.Scale = scale
	}
}

// ToStack converts the compose project services to Incus stack operations.
func (p *Project) ToStack(c *client.Client, stack *client.Stack, opts ...ToStackOption) error {
	if stack == nil {
		return client.ErrNilPointer
	}

	resources := []client.Resource{}

	options := &ToStackOptions{OnlyServices: []string{}}
	for _, o := range opts {
		o(options)
	}

	var errs error

	if len(options.OnlyServices) > 0 {
		services := types.Services{}
		for n, svc := range p.Services {
			for _, on := range options.OnlyServices {
				if strings.HasPrefix(on+"-", n+"-") {
					services[n] = svc
					if !options.Deps {
						continue
					}
					if options.Reverse {
						// stop direction: include services that depend on this one
						for otherName, otherSvc := range p.Services {
							if _, ok := otherSvc.DependsOn[n]; ok {
								services[otherName] = otherSvc
							}
						}
					} else {
						// start direction: include services this one depends on
						for depName := range svc.DependsOn {
							services[depName] = p.Services[depName]
						}
					}
				}
			}
		}

		p.Services = services
	}

	serviceOrder, err := ServiceGraph(p.Services, options.Reverse)
	if err != nil {
		return err
	}

	// Configure instances
	for _, serviceName := range serviceOrder {
		service, ok := p.Services[serviceName]
		if !ok {
			return fmt.Errorf("found %q a service that does not exists in services, this should never happen", serviceName)
		}

		// Determine the desired count: CLI --scale > deploy.replicas > 1.
		// A plain `up` reconciles to deploy.replicas in both directions, matching
		// `docker compose up`: a manual --scale applies only to that invocation,
		// and the next plain `up` restores replicas (scaling up or down).
		desired := 1
		if s, ok := options.Scale[service.Name]; ok {
			desired = s
		} else if service.Deploy != nil && service.Deploy.Replicas != nil {
			desired = int(*service.Deploy.Replicas)
		}

		// Discover existing instances above the desired count so they can be
		// reconciled away (highest index first) during Ensure.
		scale := desired
		for {
			instanceName := fmt.Sprintf("%s-%d", service.Name, scale+1)
			if ok, err := c.InstanceExists(instanceName); !ok || err != nil {
				break
			}

			scale = scale + 1
		}

		instances := []*client.Instance{}
		for i := 1; i <= scale; i++ {
			instance, instanceResources, err := serviceToInstance(c, p.Project, service.Name, options, i, scale)
			if err != nil {
				errs = errors.Join(errs, err)
				continue
			}

			resources = append(resources, instance)
			resources = append(resources, instanceResources...)

			instances = append(instances, instance)
		}

		// Reconcile down: instances beyond the desired count are marked for
		// deletion (highest index first) and torn down during Ensure.
		if len(instances) > desired {
			slices.Reverse(instances)

			for idx := range len(instances) - desired {
				instances[idx].MarkDelete()
			}
		}
	}

	if errs != nil {
		return errs
	}

	stack.Sort(options.Reverse)

	if options.InstancesOnly {
		instances, err := client.ByKind[*client.Instance](resources, client.KindInstance)
		if err != nil {
			return err
		}
		for _, i := range instances {
			stack.Add(i)
		}
	} else if options.ImagesOnly {
		images, err := client.ByKind[*client.Image](resources, client.KindImage)
		if err != nil {
			return err
		}
		for _, i := range images {
			stack.Add(i)
		}
	} else {
		stack.Add(resources...)
	}

	if !c.IsConnected() {
		return nil
	}

	return nil
}
