package client

import (
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
	}
}

// Add appends a resource operation to the stack.
func (s *Stack) Add(resources ...Resource) *Stack {
	for _, r := range resources {
		if r != nil {
			s.resources = append(s.resources, r)
		}
	}

	return s
}

// All returns all tasks in the stack.
func (s *Stack) All() []Resource {
	return s.resources
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

func (s *Stack) runBatch(batch []Resource, kind Kind, action Action, opts ...Option) error {
	if len(batch) == 0 {
		return nil
	}

	// Execute batches in order
	var errs error

	// Images and logs run in parallel - they have no dependencies
	runParallel := (kind == KindImage || action == ActionLog) && len(batch) > 1
	if runParallel {
		pool := NewWorkerPool(s.workers)
		for _, r := range batch {
			task := r // capture for closure
			pool.Submit(func() error {
				return RunAction(task, action, opts...)
			})
		}
		if err := pool.Run(PoolRunArgs{FailFast: false}); err != nil {
			errs = errors.Join(errs, err)
		}
	} else {
		// All other resources run sequentially
		for _, r := range batch {
			if err := RunAction(r, action, opts...); err != nil {
				errs = errors.Join(errs, err)
			}
		}
	}

	return errs
}

// Run executes all tasks in priority order.
// Returns aggregated errors from all failed operations.
//
// Image tasks are executed in parallel using a worker pool.
// All other tasks are executed sequentially to respect potential dependencies.
func (s *Stack) Run(action Action, opts ...Option) error {
	s.sort()

	// Group tasks by priority into batches
	batches := s.groupByKind()

	// Execute batches in order
	var errs error

	for _, batch := range batches {
		kind := batch[0].Kind()
		errs = errors.Join(errs, s.runBatch(batch, kind, action, opts...))
	}

	return errs
}

// ForAction returns a new stack with resources filtered for the given action.
func (s *Stack) ForAction(action Action) *Stack {
	sortDescending := s.sortDescending

	// Magic: automatically determine sort order based on action
	switch action {
	case ActionStop, ActionDelete:
		sortDescending = true
	case ActionEnsure, ActionStart:
		sortDescending = false
	case ActionLog:
		// ActionLog: no sort order change, logs run in parallel anyway
	}
	// default: unknown action, no modification

	result := &Stack{
		client:         s.client,
		workers:        s.workers,
		sortDescending: sortDescending,
	}

	for _, r := range s.All() {
		if SupportsAction(r, action) {
			result.Add(r)
		}
	}

	return result
}
