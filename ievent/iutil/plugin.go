package iutil

// Next hands the event on. The last plugin in the chain is given one that does
// nothing.
type Next func(ev *Event)

// Plugin is one link in the chain: it holds its successor and continues the walk itself, so the
// chain runs as a call stack. A plugin appearing twice needs two constructions, not one value listed twice.
type Plugin interface {
	// Name identifies the plugin in logs, in metrics, and in the chain.
	Name() string

	// Wants declares which actions this plugin cares about and how much of each must be read
	// before it sees one; read from every plugin before anything is wired.
	Wants() []Want

	// Setup wires the plugin, once, before anything runs. An error here stops
	// the process.
	Setup(args SetupArgs) error

	// Handle runs in its parent's goroutine and must not block: enqueue and return, then call Next
	// later from the plugin's own goroutine. Check the state first; not StateOk means observers-only:
	//
	//	func (p *Plugin) Handle(ev *iutil.Event) {
	//		if ev.State() != iutil.StateOk {
	//			p.next(ev)
	//
	//			return
	//		}
	//		...
	//	}
	Handle(ev *Event)
}
