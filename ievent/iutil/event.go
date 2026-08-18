// Package iutil is the vocabulary the source and its plugins share: the event, what can be read
// onto it, and the plugin interface. It knows the Incus API types, never Incus itself.
package iutil

import (
	"maps"
	"time"
)

// Event is what travels the chain, immutable by construction: no accessor hands back anything a
// caller can write through, so it is safe to share and hold past the walk that delivered it.
type Event struct {
	action  string
	project string
	name    string
	oldName string

	state  State
	reason string

	// chainState is what the chain was when this event was minted. See ChainState.
	chainState ChainState

	// at is when the event was decoded, not when a plugin saw it.
	at time.Time

	// enriched says which reads landed, so "no networks" differs from "nobody asked".
	enriched Enrichment

	running bool

	config map[string]string

	// instanceLabels and projectLabels stay apart so a consumer can tell which one set a key;
	// Label and Labels merge them for the effective view.
	instanceLabels map[string]string
	projectLabels  map[string]string

	// nets is filled by WithNetworks, keyed by Network.Key.
	nets map[string]*Network

	// values is plugin-scoped data held as a chain of nodes, like context: one node per derive.
	values *valueNode
}

// NewEvent builds one event, in StateOk. The only thing that sets a state.
func NewEvent(at time.Time, action, project, name, oldName string) *Event {
	return &Event{
		at:      at,
		action:  action,
		project: project,
		name:    name,
		oldName: oldName,
		state:   StateOk,
	}
}

// At is when the event was decoded off the stream, which is ahead of the walk.
func (e *Event) At() time.Time { return e.at }

// Action is the Incus lifecycle action, its own string rather than a vocabulary
// of ours, so classifying it stays the caller's business.
func (e *Event) Action() string { return e.action }

// Project is the event's project, taken from the envelope rather than the
// payload, which project and profile events leave empty.
func (e *Event) Project() string { return e.project }

// Name is what the action names.
func (e *Event) Name() string { return e.name }

// OldName is the pre-rename name, empty unless this is a rename. Only a caller
// knows which actions rename anything.
func (e *Event) OldName() string { return e.oldName }

// State is what has happened to this event so far. An event that is not StateOk
// is walking the chain for the observers rather than for action.
func (e *Event) State() State { return e.state }

// Reason describes the current state: which plugin dropped the event, or what
// the source could not complete. Empty while StateOk.
func (e *Event) Reason() string { return e.reason }

// ChainState is what the whole chain was when this event was minted, never what
// has happened to the event itself - that is State.
func (e *Event) ChainState() ChainState { return e.chainState }

// WithChainState derives an event carrying the chain's state. The source stamps
// what it mints; a plugin minting one of its own carries over what it last saw.
func (e *Event) WithChainState(state ChainState) *Event {
	next := *e
	next.chainState = state

	return &next
}

// WithDropped marks the event finished with, naming who did it: a plugin's
// Name, or "source/<cause>". A no-op once the event is dropped or failed.
func (e *Event) WithDropped(by string) *Event {
	if e.state != StateOk {
		return e
	}

	next := *e
	next.state, next.reason = StateDropped, by

	return &next
}

// WithFailed marks the event as one the source could not complete. It outranks
// dropped; a second failure keeps the first.
func (e *Event) WithFailed(reason string) *Event {
	if e.state == StateFailed {
		return e
	}

	next := *e
	next.state, next.reason = StateFailed, reason

	return &next
}

// Equal reports whether two events say the same thing about the same subject. At, ChainState and
// Values are excluded: none of the three is about the subject.
func (e *Event) Equal(other *Event) bool {
	if e == nil || other == nil {
		return e == other
	}

	if e.action != other.action ||
		e.project != other.project ||
		e.name != other.name ||
		e.oldName != other.oldName ||
		e.state != other.state ||
		e.reason != other.reason ||
		e.enriched != other.enriched ||
		e.running != other.running {
		return false
	}

	return maps.Equal(e.config, other.config) &&
		maps.Equal(e.instanceLabels, other.instanceLabels) &&
		maps.Equal(e.projectLabels, other.projectLabels) &&
		maps.EqualFunc(e.nets, other.nets, (*Network).equal)
}

// Value returns what WithValue stored under key, or nil. The key must be an unexported type
// owned by the plugin that set it - nothing enforces that, which is why it is written here.
func (e *Event) Value(key any) any {
	for v := e.values; v != nil; v = v.parent {
		if v.key == key {
			return v.val
		}
	}

	return nil
}

// WithValue derives an event carrying one more value.
func (e *Event) WithValue(key, val any) *Event {
	next := *e
	next.values = &valueNode{parent: e.values, key: key, val: val}

	return &next
}

// valueNode is one link in the chain WithValue builds.
type valueNode struct {
	parent *valueNode
	key    any
	val    any
}
