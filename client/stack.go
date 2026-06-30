package client

import (
	"context"
	"errors"
	"io"
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
func NewStack(c *Client, opts ...StackOption) *Stack {
	options := &StackOptions{
		Workers: 4,
	}

	for _, o := range opts {
		o(options)
	}

	return &Stack{
		client:         c,
		workers:        options.Workers,
		sortDescending: options.SortDescending,
		seen:           make(map[Resource]struct{}),
	}
}

// Add appends resources to the stack, skipping nil and already-added pointers.
// Since Client.Resource() deduplicates by IncusName, pointer identity is the
// right key: the same resource object must not run twice in parallel.
func (s *Stack) Add(resources ...Resource) {
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
}

// AddOrdered adds resources in the given order.
func (s *Stack) AddOrdered(order []string, resources map[string][]Resource) {
	for _, k := range order {
		res, ok := resources[k]
		if !ok {
			continue
		}

		s.Add(res...)
	}
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
func (s *Stack) groupByPriority() [][]Resource {
	if len(s.resources) == 0 {
		return nil
	}

	var batches [][]Resource
	var cBatch []Resource
	cPriority := s.resources[0].Priority()

	for _, r := range s.resources {
		if r.Priority() != cPriority {
			batches = append(batches, cBatch)
			cBatch = nil
			cPriority = r.Priority()
		}
		cBatch = append(cBatch, r)
	}

	if len(cBatch) > 0 {
		batches = append(batches, cBatch)
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
func (s *Stack) Run(ctx context.Context, action Action, stdout io.Writer, stderr io.Writer, opts ...Option) error {
	s.sort()

	if stdout != nil || stderr != nil {
		opts = append(opts, OptionOutput(stdout, stderr))
	}

	// Group tasks by priority into batches
	batches := s.groupByPriority()

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

// ForActionF returns a new stack with resources filtered for the given action,
// it allows custom filtering with the filter hook.
func (s *Stack) ForActionF(action Action, filter func(r Resource) bool) *Stack {
	result := &Stack{
		client:         s.client,
		workers:        s.workers,
		sortDescending: s.sortDescending,
		seen:           make(map[Resource]struct{}),
	}

	if filter == nil {
		filter = func(_ Resource) bool { return true }
	}

	for _, r := range s.All() {
		if SupportsAction(r, action) && filter(r) {
			result.Add(r)
		}
	}

	return result
}
