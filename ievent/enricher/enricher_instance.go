package enricher

import (
	"context"
	"errors"
	"fmt"
	"time"

	incusapi "github.com/lxc/incus/v7/shared/api"
	"github.com/panjf2000/ants/v2"

	"github.com/lxc/incus-compose/iclient"
	"github.com/lxc/incus-compose/ievent/iutil"
)

// flight is one read, and every event waiting on it.
type flight struct {
	key     string
	project string
	name    string

	// network says this reads a wire rather than an instance, kept apart in the
	// key so a network and an instance called the same thing never collide.
	network bool

	items []*item

	// synthetic is the event a fan-out would emit, held here rather than pushed:
	// it goes in the line only if the read found something new.
	synthetic *iutil.Event
}

// repair is one key due another read, and when.
type repair struct {
	subject

	at time.Time
}

// join folds a second flight for the same key into this one; the first
// synthetic is kept, later ones being the same event minted twice.
func (f *flight) join(other *flight) {
	f.items = append(f.items, other.items...)

	if f.synthetic == nil {
		f.synthetic = other.synthetic
	}
}

// flightKey identifies a read, kept apart per kind for that reason.
func flightKey(network bool, project, name string) string {
	if network {
		return "n:" + key(project, name)
	}

	return "i:" + key(project, name)
}

// result is what a worker hands back, carrying the flight rather than a key so
// nothing has to find the waiting events again.
type result struct {
	flight *flight

	instance *incusapi.Instance
	state    *incusapi.InstanceState

	// wire is set when the flight read a network instead.
	wire *incusapi.Network

	err error
}

// readFunc is one instance read. A function rather than the connection itself,
// so a test can answer with Incus values it built instead of ones a daemon
// returned.
type readFunc func(ctx context.Context, project, name string) (*incusapi.Instance, *incusapi.InstanceState, error)

// incusReader reads one instance and its state through the connection.
func incusReader(conn *iclient.Connection) readFunc {
	return func(ctx context.Context, project, name string) (*incusapi.Instance, *incusapi.InstanceState, error) {
		scoped := conn.WithProject(project)

		inst, _, err := scoped.GetInstance(ctx, name, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("reading instance %s/%s: %w", project, name, err)
		}

		state, _, err := scoped.GetInstanceState(ctx, name)
		if err != nil {
			return nil, nil, fmt.Errorf("reading the state of %s/%s: %w", project, name, err)
		}

		return &inst.Instance, state, nil
	}
}

// submit offers one flight to the pool, and reports whether it was taken.
//
// The deadline is set inside the task rather than around the submit, so a read
// that waited for a worker still gets its whole budget.
func (p *Plugin) submit(ctx context.Context, f *flight) error {
	err := p.pool.Submit(func() {
		readCtx, cancel := context.WithTimeout(ctx, p.opts.ReadTimeout)
		defer cancel()

		res := result{flight: f}

		if f.network {
			res.wire, res.err = p.readNet(readCtx, f.project, f.name)
		} else {
			res.instance, res.state, res.err = p.read(readCtx, f.project, f.name)
		}

		select {
		case p.results <- res:
		case <-ctx.Done():
		}
	})
	if err != nil {
		if errors.Is(err, ants.ErrPoolOverload) {
			return err
		}

		return fmt.Errorf("submitting a read: %w", err)
	}

	return nil
}
