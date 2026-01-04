package client

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestWorkerPoolBasic tests basic pool functionality.
func TestWorkerPoolBasic(t *testing.T) {
	tests := []struct {
		name      string
		workers   int
		taskCount int
		wantErr   bool
	}{
		{
			name:      "single worker single task",
			workers:   1,
			taskCount: 1,
			wantErr:   false,
		},
		{
			name:      "multiple workers multiple tasks",
			workers:   4,
			taskCount: 10,
			wantErr:   false,
		},
		{
			name:      "zero workers defaults to 1",
			workers:   0,
			taskCount: 3,
			wantErr:   false,
		},
		{
			name:      "empty pool returns nil",
			workers:   4,
			taskCount: 0,
			wantErr:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pool := NewWorkerPool(tc.workers)

			var completed atomic.Int32
			for i := 0; i < tc.taskCount; i++ {
				pool.Submit(func() error {
					completed.Add(1)
					return nil
				})
			}

			err := pool.Run(PoolRunArgs{FailFast: false})

			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, int32(tc.taskCount), completed.Load(), "all tasks should complete")
		})
	}
}

// TestWorkerPoolErrorAggregation tests that errors from multiple tasks are collected.
func TestWorkerPoolErrorAggregation(t *testing.T) {
	pool := NewWorkerPool(4)

	pool.Submit(func() error { return errors.New("error 1") })
	pool.Submit(func() error { return nil })
	pool.Submit(func() error { return errors.New("error 2") })
	pool.Submit(func() error { return nil })

	err := pool.Run(PoolRunArgs{FailFast: false})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error 1")
	assert.Contains(t, err.Error(), "error 2")
}

// TestWorkerPoolFailFast tests that fail-fast stops processing on first error.
func TestWorkerPoolFailFast(t *testing.T) {
	pool := NewWorkerPool(1) // Single worker to ensure order

	var executed atomic.Int32

	pool.Submit(func() error {
		executed.Add(1)
		return errors.New("first error")
	})
	pool.Submit(func() error {
		executed.Add(1)
		return nil
	})
	pool.Submit(func() error {
		executed.Add(1)
		return nil
	})

	err := pool.Run(PoolRunArgs{FailFast: true})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "first error")
	// With fail-fast and single worker, should stop after first error
	assert.Equal(t, int32(1), executed.Load(), "should stop after first error")
}

// TestWorkerPoolConcurrency verifies tasks run concurrently.
func TestWorkerPoolConcurrency(t *testing.T) {
	pool := NewWorkerPool(4)

	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32

	for i := 0; i < 8; i++ {
		pool.Submit(func() error {
			current := concurrent.Add(1)
			// Track max concurrent
			for {
				maxLoad := maxConcurrent.Load()
				if current <= maxLoad || maxConcurrent.CompareAndSwap(maxLoad, current) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			concurrent.Add(-1)
			return nil
		})
	}

	err := pool.Run(PoolRunArgs{FailFast: false})
	assert.NoError(t, err)

	// With 4 workers and 8 tasks, we should see >1 concurrent execution
	assert.Greater(t, maxConcurrent.Load(), int32(1), "should have concurrent execution")
}

// TestWorkerPoolReuse tests that pool can be reused after Run.
func TestWorkerPoolReuse(t *testing.T) {
	pool := NewWorkerPool(2)

	// First run
	var count1 atomic.Int32
	pool.Submit(func() error { count1.Add(1); return nil })
	pool.Submit(func() error { count1.Add(1); return nil })
	err := pool.Run(PoolRunArgs{})
	assert.NoError(t, err)
	assert.Equal(t, int32(2), count1.Load())

	// Second run
	var count2 atomic.Int32
	pool.Submit(func() error { count2.Add(1); return nil })
	pool.Submit(func() error { count2.Add(1); return nil })
	pool.Submit(func() error { count2.Add(1); return nil })
	err = pool.Run(PoolRunArgs{})
	assert.NoError(t, err)
	assert.Equal(t, int32(3), count2.Load())
}
