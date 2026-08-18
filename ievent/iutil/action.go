package iutil

// Actions of ours: the slash prefixes who raised it, and an Incus action never contains one.
const (
	// ActionSweepEnd says the enricher has been all the way round the fleet, not what exists.
	ActionSweepEnd = "enricher/sweep-end"

	// ActionConnected and ActionDisconnected report the event stream itself, with no project or name.
	ActionConnected    = "source/connected"
	ActionDisconnected = "source/disconnected"

	// ActionReady and ActionNotReady report whether the raiser has something worth answering from;
	// readiness itself lives on ChainState.
	ActionReady    = "chain/ready"
	ActionNotReady = "chain/not-ready"

	// CommandDrain asks a plugin to finish: hand on everything it holds, and answer on CommandOut when empty.
	CommandDrain = "source/drain"
)
