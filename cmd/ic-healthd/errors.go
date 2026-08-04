package main

import (
	"errors"
	"fmt"
)

// Error is a sentinel-based error type, same pattern as client/errors.go.
type Error struct {
	sentinel error
	failures uint64
	wrapped  error
}

// newError creates a new sentinel error.
func newError(text string) *Error {
	return &Error{sentinel: errors.New(text)}
}

// WithFailures returns a new error with the given failure count attached.
func (e *Error) WithFailures(failures uint64) *Error {
	return &Error{
		sentinel: e.sentinel,
		failures: failures,
		wrapped:  e.wrapped,
	}
}

// Wrap wraps another error, preserving the sentinel identity.
func (e *Error) Wrap(wrapped error) *Error {
	return &Error{sentinel: e.sentinel, failures: e.failures, wrapped: wrapped}
}

// Error implements the error interface.
func (e *Error) Error() string {
	text := e.sentinel.Error()
	if e.failures > 0 {
		text = fmt.Sprintf("%s (failures: %d)", text, e.failures)
	}
	if e.wrapped != nil {
		return text + ": " + e.wrapped.Error()
	}
	return text
}

// Unwrap returns the wrapped error for errors.Unwrap() support.
func (e *Error) Unwrap() error {
	return e.wrapped
}

// Is implements errors.Is() support by comparing sentinel pointers.
func (e *Error) Is(target error) bool {
	if other, ok := target.(*Error); ok {
		return other.sentinel == e.sentinel
	}
	return false
}

// As implements errors.As() support by copying to target if it's *Error.
func (e *Error) As(target any) bool {
	if t, ok := target.(**Error); ok {
		*t = e
		return true
	}
	return false
}

// ErrRetriesExhausted means a checker gave up; the runner then evaluates restart policy.
var ErrRetriesExhausted = newError("healthcheck retries exhausted")

// ErrNotRunning is an internal sentinel error.
var ErrNotRunning = newError("not running")

// ErrInstanceStopped means the checker exited because its instance stopped;
// the lifecycle events own restarting it.
var ErrInstanceStopped = newError("instance stopped")
