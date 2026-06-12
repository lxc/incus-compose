package client

import (
	"context"
	"slices"
	"time"
)

// Resource creation priorities using powers of 2 for clear separation.
// Lower priority values are created first and deleted last.
const (
	PriorityProject  = 1 << 8  // Infrastructure (created first, deleted last)
	PriorityProfile  = 1 << 9  // Base config
	PriorityImage    = 1 << 10 // Images (own batch for parallel downloads)
	PriorityNetwork  = 1 << 11 // Networks
	PriorityVolume   = 1 << 12 // Storage
	PriorityInstance = 1 << 13 // Instance depends on everything above
)

// InterfaceIPs represents interface ips.
type InterfaceIPs struct {
	Network string
	IPv4s   []string
	IPv6s   []string
}

// Options holds arguments for resource actions.
type Options struct {
	// Create resources if they don't exist (for ActionEnsure).
	Create bool

	// Force deletion/stop even if resource is in use.
	Force bool

	// Timeout for actions (0 = default).
	Timeout time.Duration

	// DependencyTimeout is the max time to wait for dependency health checks.
	// Falls back to Timeout when zero.
	DependencyTimeout time.Duration

	// Follow enables continuous streaming (for ActionLog).
	Follow bool

	// Pull forces cached images to refresh from their source registry (for ActionEnsure).
	Pull bool

	// Build controls rebuild behaviour for build-configured images (for ActionEnsure).
	Build BuildInfo

	// Healthd indicates that we use healthd features.
	Healthd bool
}

// incusTimeout converts Timeout to the seconds value expected by the Incus
// state API. Zero maps to -1 (daemon default) because Incus treats 0 as an
// immediate kill on stop instead of a graceful shutdown.
func (o Options) incusTimeout() int {
	if o.Timeout == 0 {
		return -1
	}

	return int(o.Timeout.Seconds())
}

// Option configures action arguments.
type Option func(o *Options)

// OptionCreate creates resources if they don't exist (for ActionEnsure).
func OptionCreate() Option {
	return func(o *Options) {
		o.Create = true
	}
}

// OptionForce forces deletion/stop even if resource is in use.
func OptionForce() Option {
	return func(o *Options) {
		o.Force = true
	}
}

// OptionTimeout sets the timeout for actions.
func OptionTimeout(t time.Duration) Option {
	return func(o *Options) {
		o.Timeout = t
	}
}

// OptionDependencyTimeout sets the max time to wait for dependency health checks.
// Falls back to OptionTimeout when zero.
func OptionDependencyTimeout(t time.Duration) Option {
	return func(o *Options) {
		o.DependencyTimeout = t
	}
}

// OptionFollow enables continuous streaming (for ActionLog).
func OptionFollow() Option {
	return func(o *Options) {
		o.Follow = true
	}
}

// OptionPull forces cached images to refresh from their source registry (for ActionEnsure).
func OptionPull() Option {
	return func(o *Options) {
		o.Pull = true
	}
}

// OptionBuild sets the build info for build-configured images (for ActionEnsure).
func OptionBuild(info BuildInfo) Option {
	return func(o *Options) {
		o.Build = info
	}
}

// OptionNoHealthd indicates that we dont use healthd features.
func OptionNoHealthd() Option {
	return func(o *Options) {
		o.Healthd = false
	}
}

// NewOptions makes a ActionArgs struct from ActionO* options.
func NewOptions(opts ...Option) Options {
	args := Options{Healthd: true}

	for _, o := range opts {
		o(&args)
	}

	return args
}

// ByKind filters resources by kind and returns them as the specified type.
func ByKind[T Resource](resources []Resource, kind Kind) ([]T, error) {
	result := []T{}
	for _, r := range resources {
		if r.Kind() == kind {
			i, ok := r.(T)
			if !ok {
				return result, ErrUnknown.WithResource(r)
			}

			result = append(result, i)
		}
	}

	return result, nil
}

// SupportsAction returns if the Resource supports the action.
func SupportsAction(r Resource, action Action) bool {
	switch action {
	case ActionEnsure:
		_, ok := r.(EnsureAble)
		return ok
	case ActionDelete:
		_, ok := r.(DeleteAble)
		return ok
	case ActionStart:
		_, ok := r.(StartAble)
		return ok
	case ActionStop:
		_, ok := r.(StopAble)
		return ok
	case ActionLog:
		_, ok := r.(LogAble)
		return ok
	default:
		return false
	}
}

// RunAction runs a action on a resource.
func RunAction(ctx context.Context, r Resource, action Action, opts ...Option) error {
	switch action {
	case ActionEnsure:
		if e, ok := r.(EnsureAble); ok {
			return e.Ensure(ctx, opts...)
		}
		return ErrUnsupportedAction.WithAction(ActionEnsure).WithResource(r)
	case ActionDelete:
		if e, ok := r.(DeleteAble); ok {
			return e.Delete(ctx, opts...)
		}
		return ErrUnsupportedAction.WithAction(ActionDelete).WithResource(r)
	case ActionStart:
		if e, ok := r.(StartAble); ok {
			return e.Start(ctx, opts...)
		}
		return ErrUnsupportedAction.WithAction(ActionStart).WithResource(r)
	case ActionStop:
		if e, ok := r.(StopAble); ok {
			return e.Stop(ctx, opts...)
		}
		return ErrUnsupportedAction.WithAction(ActionStop).WithResource(r)
	case ActionLog:
		if e, ok := r.(LogAble); ok {
			return e.Log(ctx, opts...)
		}
		return ErrUnsupportedAction.WithAction(ActionLog).WithResource(r)
	default:
		return ErrUnsupportedAction.WithAction(action).WithResource(r)
	}
}

// BaseResource provides common fields for all Incus resources.
type BaseResource struct {
	kind     Kind
	name     string
	priority int
}

// NewBaseResource creates a new BaseResource.
func NewBaseResource(kind Kind, name string, priority int) *BaseResource {
	return &BaseResource{
		kind:     kind,
		name:     name,
		priority: priority,
	}
}

// Kind returns the resource kind.
func (r *BaseResource) Kind() Kind {
	return r.kind
}

// Name returns the resource name.
func (r *BaseResource) Name() string {
	return r.name
}

// Priority returns the resource priority.
func (r *BaseResource) Priority() int {
	return r.priority
}

// ResourceStore provides storage for any BasicResource type.
type ResourceStore struct {
	resources []Resource
}

// All returns all resources.
func (s *ResourceStore) All() []Resource {
	return s.resources
}

// Add appends a resource to the store.
func (s *ResourceStore) Add(r Resource) {
	s.resources = append(s.resources, r)
}

// Remove removes a resource from the store by kind and name.
func (s *ResourceStore) Remove(r Resource) {
	s.resources = slices.DeleteFunc(s.resources, func(res Resource) bool {
		if res == nil {
			return true
		}

		return res.Kind() == r.Kind() && res.IncusName() == r.IncusName()
	})
}

// Get retrieves a resource by kind and its Incus-normalized name. Returns nil if not found.
func (s *ResourceStore) Get(kind Kind, incusName string) Resource {
	idx := slices.IndexFunc(s.resources, func(r Resource) bool {
		return r.Kind() == kind && r.IncusName() == incusName
	})
	if idx == -1 {
		return nil
	}
	return s.resources[idx]
}
