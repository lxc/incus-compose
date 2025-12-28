package client

import (
	"errors"
	"sort"
)

// transaction tracks created resources and accumulated errors for rollback support.
type transaction struct {
	jErr       error
	createJErr error

	resources []BasicResource
}

func newTransaction() *transaction {
	return &transaction{
		resources: []BasicResource{},
	}
}

// Add registers a resource with this transaction for potential rollback.
func (t *transaction) Add(p BasicResource) {
	t.resources = append(t.resources, p)
}

// AddError accumulates a general error.
func (t *transaction) AddError(err error) error {
	t.jErr = errors.Join(t.jErr, err)
	return err
}

// AddCreateError accumulates a creation error that indicates rollback is needed.
func (t *transaction) AddCreateError(err error) error {
	t.createJErr = errors.Join(t.createJErr, err)
	return err
}

// Errors returns all accumulated errors (both general and creation errors).
func (t *transaction) Errors() error {
	return errors.Join(t.jErr, t.createJErr)
}

// CreateErrors returns only creation-related errors.
func (t *transaction) CreateErrors() error {
	return t.createJErr
}

// Rollback deletes all created resources in reverse priority order.
// Higher priority resources are deleted first to respect dependencies.
// Returns the names of successfully deleted resources and any errors encountered.
func (t *transaction) Rollback(timeout int) ([]string, error) {
	var jErr error
	deleted := []string{}

	if timeout == 0 {
		timeout = 5
	}

	// Sort by priority descending (highest priority deleted first)
	sort.SliceStable(t.resources, func(i, j int) bool {
		return t.resources[i].Priority() > t.resources[j].Priority()
	})

	// Delete in priority order
	for _, r := range t.resources {
		err := r.Delete(timeout, true)
		if err == nil {
			deleted = append(deleted, r.Name())
		} else {
			jErr = errors.Join(jErr, err)
		}
	}

	return deleted, jErr
}
