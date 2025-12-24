// Package icclient provides error definitions for the Incus client wrapper.
package icclient

import "errors"

var (
	// ErrDisconnected is returned when attempting to use a client that hasn't called Connect().
	ErrDisconnected = errors.New("trying to use a disconnected client")

	// ErrInstanceNotRunning is returned when attempting to stop an instance that isn't running.
	ErrInstanceNotRunning = errors.New("the instance is not running")

	// ErrInstanceRunning is returned when attempting to remove a running instance without force.
	ErrInstanceRunning = errors.New("the instance is running")
)
