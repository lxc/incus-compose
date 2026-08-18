package iutil

// State is what has happened to an event so far. The set is closed and the
// source owns it; anything a plugin wants to carry of its own goes in WithValue.
type State string

const (
	// StateOk is an event nothing has finished with.
	StateOk State = "ok"

	// StateDropped is an event a plugin has finished with, still walking the
	// chain so that the observers behind it can see it.
	StateDropped State = "dropped"

	// StateFailed is an event the source could not complete.
	StateFailed State = "failed"
)

// ChainState is what the whole chain knows, as State is what happened to one event. Gate on
// ChainWarm, not against ChainCold: the zero value is neither, so an unstamped event acts like nothing has been read.
type ChainState string

const (
	// ChainCold is a fleet nothing has read whole yet.
	ChainCold ChainState = "cold"

	// ChainWarm is a fleet read whole. The last consumer sets it, so it also
	// says the whole chain has caught up with the round that finished.
	ChainWarm ChainState = "warm"
)
