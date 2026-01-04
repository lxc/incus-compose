package client

import (
	"errors"
	"fmt"
)

// Error is a sentinel-based error type that supports context enrichment.
type Error struct {
	sentinel error
	text     string
	wrapped  error
}

// NewError creates a new sentinel error.
func NewError(text string) *Error {
	return &Error{sentinel: errors.New(text), text: text}
}

// WithKindName adds resource kind and name context to the error.
func (e *Error) WithKindName(kind Kind, name string) *Error {
	return &Error{
		sentinel: e.sentinel,
		text:     fmt.Sprintf("%v: %v(%v)", e.text, kind, name),
		wrapped:  e.wrapped,
	}
}

// WithText adds text context to the error.
func (e *Error) WithText(text string) *Error {
	return &Error{
		sentinel: e.sentinel,
		text:     fmt.Sprintf("%v %v", e.text, text),
		wrapped:  e.wrapped,
	}
}

// WithAction adds action context to the error.
func (e *Error) WithAction(action Action) *Error {
	return &Error{
		sentinel: e.sentinel,
		text:     fmt.Sprintf("%v %v", e.text, action),
		wrapped:  e.wrapped,
	}
}

// WithResource adds resource context to the error.
func (e *Error) WithResource(resource Resource) *Error {
	return &Error{
		sentinel: e.sentinel,
		text:     fmt.Sprintf("%v: %v(%v)", e.text, resource.Kind(), resource.Name()),
		wrapped:  e.wrapped,
	}
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.wrapped != nil {
		return e.text + ": " + e.wrapped.Error()
	}
	return e.text
}

// Unwrap returns the wrapped error for errors.Unwrap() support.
func (e *Error) Unwrap() error {
	return e.wrapped
}

// Wrap wraps another error, preserving the sentinel identity.
func (e *Error) Wrap(wrapped error) *Error {
	return &Error{sentinel: e.sentinel, text: e.text, wrapped: wrapped}
}

// Is implements errors.Is() support by comparing sentinel pointers.
func (e *Error) Is(target error) bool {
	if other, ok := target.(*Error); ok {
		return other.sentinel == e.sentinel
	}
	return false
}

var (
	// ErrUnsupportedAction indicates the resource does not support the action.
	ErrUnsupportedAction = NewError("resource does not support action")

	// ErrUnknown indicates an unknown error occurred.
	ErrUnknown = NewError("unknown")

	// ErrUnknownConfig indicates an unknown config for a resource.
	ErrUnknownConfig = NewError("unknown config for resource")

	// ErrNilPointer indicates something is a nil pointer.
	ErrNilPointer = NewError("found a nil pointer")

	// ErrBadDeviceConfig indicates a bad device config.
	ErrBadDeviceConfig = NewError("bad config for device")

	// ErrDependencyNotEnsured indicates a dependency is not ensured.
	ErrDependencyNotEnsured = NewError("dependency not ensured")

	// ErrDisconnected indicates an operation was attempted on a disconnected client.
	ErrDisconnected = NewError("client is not connected")

	// ErrConnectionFailed indicates a connection attempt failed.
	ErrConnectionFailed = NewError("connection failed")

	// ErrAborted indicates an operation was aborted (e.g., by BeforeAny hook).
	ErrAborted = NewError("operation aborted")

	// ErrNotFound indicates a resource was not found.
	ErrNotFound = NewError("resource not found")

	// ErrNotEnsured indicates an operation requires the resource to be ensured first.
	ErrNotEnsured = NewError("resource not ensured")

	// ErrImageRequired indicates an instance requires an image.
	ErrImageRequired = NewError("instances without an image are not yet supported")

	// ErrBindMountRemote indicates bind mounts are not supported over network connections.
	ErrBindMountRemote = NewError("bind mounts not supported over network connection")
)
