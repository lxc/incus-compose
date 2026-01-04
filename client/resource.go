package client

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
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

// Options holds arguments for resource actions.
type Options struct {
	// Create resources if they don't exist (for ActionEnsure).
	Create bool

	// Force deletion/stop even if resource is in use.
	Force bool

	// Timeout in seconds for actions (0 = default).
	Timeout int
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

// OptionTimeout in seconds for actions (0 = default).
func OptionTimeout(t int) Option {
	return func(o *Options) {
		o.Timeout = t
	}
}

// NewOptions makes a ActionArgs struct from ActionO* options.
func NewOptions(opts ...Option) Options {
	args := Options{}

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

// FilterDuplicates filters duplicates out of Resources.
func FilterDuplicates(resources []Resource) []Resource {
	known := make(map[string]Resource, len(resources))

	for _, r := range resources {
		key := fmt.Sprintf("%v:%v", r.Kind(), r.Name())
		if _, ok := known[key]; !ok {
			known[key] = r
		} else {
			slog.Debug(key)
		}
	}

	return slices.Collect(maps.Values(known))
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
	default:
		return false
	}
}

// RunAction runs a action on a resource.
func RunAction(r Resource, action Action, opts ...Option) error {
	switch action {
	case ActionEnsure:
		if e, ok := r.(EnsureAble); ok {
			return e.Ensure(opts...)
		}
		return ErrUnsupportedAction.WithAction(ActionEnsure).WithResource(r)

	case ActionDelete:
		if e, ok := r.(DeleteAble); ok {
			return e.Delete(opts...)
		}
		return ErrUnsupportedAction.WithAction(ActionDelete).WithResource(r)
	case ActionStart:
		if e, ok := r.(StartAble); ok {
			return e.Start(opts...)
		}
		return ErrUnsupportedAction.WithAction(ActionStart).WithResource(r)
	case ActionStop:
		if e, ok := r.(StopAble); ok {
			return e.Stop(opts...)
		}
		return ErrUnsupportedAction.WithAction(ActionStop).WithResource(r)
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

// Get retrieves a resource by kind and name. Returns nil if not found.
func (s *ResourceStore) Get(kind Kind, name string) Resource {
	idx := slices.IndexFunc(s.resources, func(r Resource) bool {
		return r.Kind() == kind && r.Name() == name
	})
	if idx == -1 {
		return nil
	}
	return s.resources[idx]
}
