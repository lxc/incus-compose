package client

import "errors"

var (
	// ErrConnectionFailed indicates the client failed to connect to the Incus server.
	ErrConnectionFailed = errors.New("connection failed")

	// ErrDisconnected is returned when attempting to use a client that hasn't called Connect().
	ErrDisconnected = errors.New("trying to use a disconnected client")

	// ErrEmpty indicates a resource operation was called on an uninitialized resource.
	ErrEmpty = errors.New("empty")
)
