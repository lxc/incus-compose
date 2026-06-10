package client

import (
	"context"
	"errors"
	"sort"
)

// StackRunArgs holds arguments for Stack.Run().
type StackRunArgs struct {
	Options

	// Workers is the number of parallel workers per batch (default: 4).
	Workers int
}

// StackOptions configures stack execution.
type StackOptions struct {
	Workers        int
	SortDescending bool
}

// StackOption configures stack options.
type StackOption func(*StackOptions)

// StackWorkers sets the number of parallel workers.
func StackWorkers(w int) StackOption {
	return func(o *StackOptions) { o.Workers = w }
}

// StackSortDescending sorts resources in descending priority order.
func StackSortDescending() StackOption {
	return func(o *StackOptions) { o.SortDescending = true }
}

// Stack manages a collection of resource operations.
// Resources are executed in priority order with proper dependency handling.
type Stack struct {
	client *Client

	workers        int
	sortDescending bool

	resources []Resource
	seen      map[Resource]struct{}
}

// NewStack creates a new Stack for the given project.
func NewStack(p *Client, opts ...StackOption) *Stack {
	options := &StackOptions{
		Workers: 4,
	}

	for _, o := range opts {
		o(options)
	}

	return &Stack{
		client:         p,
		workers:        options.Workers,
		sortDescending: options.SortDescending,
		seen:           make(map[Resource]struct{}),
	}
}

// Add appends resources to the stack, skipping nil and already-added pointers.
// Since Client.Resource() deduplicates by IncusName, pointer identity is the
// right key: the same resource object must not run twice in parallel.
func (s *Stack) Add(resources ...Resource) *Stack {
	for _, r := range resources {
		if r == nil {
			continue
		}
		if _, ok := s.seen[r]; ok {
			continue
		}
		s.seen[r] = struct{}{}
		s.resources = append(s.resources, r)
	}

	return s
}

// All returns all tasks in the stack.
func (s *Stack) All() []Resource {
	return s.resources
}

// Sort sets the sort order.
func (s *Stack) Sort(desc bool) {
	s.sortDescending = desc
}

func (s *Stack) sort() {
	if s.sortDescending {
		sort.SliceStable(s.resources, func(i, j int) bool {
			return s.resources[i].Priority() > s.resources[j].Priority()
		})
	} else {
		sort.SliceStable(s.resources, func(i, j int) bool {
			return s.resources[i].Priority() < s.resources[j].Priority()
		})
	}
}

// groupByPriority groups sorted tasks into batches by kind.
func (s *Stack) groupByKind() [][]Resource {
	if len(s.resources) == 0 {
		return nil
	}

	var batches [][]Resource
	var currentBatch []Resource
	currentKind := s.resources[0].Kind()

	for _, r := range s.resources {
		if r.Kind() != currentKind {
			batches = append(batches, currentBatch)
			currentBatch = nil
			currentKind = r.Kind()
		}
		currentBatch = append(currentBatch, r)
	}

	if len(currentBatch) > 0 {
		batches = append(batches, currentBatch)
	}

	return batches
}

func (s *Stack) runBatch(ctx context.Context, batch []Resource, kind Kind, action Action, opts ...Option) error {
	if len(batch) == 0 {
		return nil
	}

	// Execute batches in order
	var errs error

	pool := NewWorkerPool(s.workers)
	for _, r := range batch {
		task := r // capture for closure
		pool.Submit(func() error {
			return RunAction(ctx, task, action, opts...)
		})
	}
	if err := pool.Run(PoolRunArgs{FailFast: false}); err != nil {
		errs = errors.Join(errs, err)
	}

	return errs
}

// Run executes all tasks in priority order.
// Returns aggregated errors from all failed operations.
//
// Image tasks are executed in parallel using a worker pool.
// All other tasks are executed sequentially to respect potential dependencies.
func (s *Stack) Run(ctx context.Context, action Action, opts ...Option) error {
	s.sort()

	// Group tasks by priority into batches
	batches := s.groupByKind()

	// Execute batches in order
	var errs error

	for _, batch := range batches {
		kind := batch[0].Kind()
		errs = errors.Join(errs, s.runBatch(ctx, batch, kind, action, opts...))
	}

	return errs
}

// ForAction returns a new stack with resources filtered for the given action.
func (s *Stack) ForAction(action Action) *Stack {
	result := &Stack{
		client:         s.client,
		workers:        s.workers,
		sortDescending: s.sortDescending,
		seen:           make(map[Resource]struct{}),
	}

	for _, r := range s.All() {
		if SupportsAction(r, action) {
			result.Add(r)
		}
	}

	return result
}
