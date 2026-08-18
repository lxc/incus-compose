package enricher

import (
	"context"
	"fmt"
	"strings"

	incusapi "github.com/lxc/incus/v7/shared/api"

	"github.com/lxc/incus-compose/iclient"
	"github.com/lxc/incus-compose/ievent/iutil"
)

// networkPrefix is what an action names a network with.
const networkPrefix = "network-"

// netReadFunc is one network read. Its own type beside readFunc so a test can
// answer either without a daemon.
type netReadFunc func(ctx context.Context, project, name string) (*incusapi.Network, error)

// incusNetReader reads one network through the connection.
func incusNetReader(conn *iclient.Connection) netReadFunc {
	return func(ctx context.Context, project, name string) (*incusapi.Network, error) {
		net, _, err := conn.WithProject(project).GetNetwork(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("reading network %s/%s: %w", project, name, err)
		}

		return net, nil
	}
}

// acceptNetwork handles one network action, and reports whether it was one.
// The event itself carries no enrichment, since its subject is a wire; what it
// causes is the patch, and the re-read of everything on that wire.
func (p *Plugin) acceptNetwork(ctx context.Context, ev *iutil.Event) bool {
	if !strings.HasPrefix(ev.Action(), networkPrefix) {
		return false
	}

	// The event that happened goes in first, ahead of anything it causes.
	p.q.push(ev, true)

	// A rename leaves the old key behind; everything that was on it is re-read,
	// because the key its addresses were filed under has gone.
	if ev.OldName() != "" {
		p.forget(ctx, ev.Project(), ev.OldName())
	}

	if ev.Action() == incusapi.EventLifecycleNetworkDeleted {
		p.forget(ctx, ev.Project(), ev.Name())

		return true
	}

	// Created, updated or renamed: the wire is read and patched first, so the
	// re-reads resolve against the new subnet rather than the old one.
	p.issue(ctx, &flight{
		key:     flightKey(true, ev.Project(), ev.Name()),
		project: ev.Project(),
		name:    ev.Name(),
		network: true,
	})

	return true
}

// forget drops one wire and re-reads what was on it. Both halves matter:
// dropping alone leaves addresses filed under a key nothing describes any
// more, and re-reading alone leaves the wire there to resolve against.
func (p *Plugin) forget(ctx context.Context, project, name string) {
	on := p.m.instancesOn(key(project, name))

	p.m.dropWire(project, name)
	p.fanOut(ctx, on)
}
