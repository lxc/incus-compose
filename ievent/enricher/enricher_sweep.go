package enricher

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	incusapi "github.com/lxc/incus/v7/shared/api"
	incusutil "github.com/lxc/incus/v7/shared/util"

	"github.com/lxc/incus-compose/iclient"
	"github.com/lxc/incus-compose/ievent/iutil"
)

// sweepKind names what one message says.
type sweepKind int

const (
	// sweepAsking says a listing is going out, so Run records what it holds in
	// that scope before the answer can be older than the store it prunes.
	sweepAsking sweepKind = iota

	sweepProject
	sweepProjects

	sweepWire
	sweepWires

	// sweepWarm says every network of this round has been read.
	sweepWarm

	sweepInstances
	sweepInstance

	sweepDone
	sweepFailed
)

// sweepMsg is one thing the sweeper says.
type sweepMsg struct {
	kind sweepKind

	// about is which listing sweepAsking is about.
	about sweepKind

	project string
	name    string

	config map[string]string
	wire   *incusapi.Network
	names  []string
	err    error
}

// fleet is what the sweeper lists. An interface rather than the connection, so
// a test answers with Incus values instead of running a daemon.
type fleet interface {
	ProjectNames(ctx context.Context) ([]string, error)
	Project(ctx context.Context, name string) (*incusapi.Project, error)
	NetworkNames(ctx context.Context, project string) ([]string, error)
	InstanceNames(ctx context.Context, project string) ([]string, error)
}

// incusFleet lists through the connection.
type incusFleet struct{ conn *iclient.Connection }

func (f incusFleet) ProjectNames(ctx context.Context) ([]string, error) {
	return f.conn.GetProjectNames(ctx)
}

func (f incusFleet) Project(ctx context.Context, name string) (*incusapi.Project, error) {
	project, _, err := f.conn.GetProject(ctx, name)

	return project, err
}

func (f incusFleet) NetworkNames(ctx context.Context, project string) ([]string, error) {
	return f.conn.WithProject(project).GetNetworkNames(ctx)
}

func (f incusFleet) InstanceNames(ctx context.Context, project string) ([]string, error) {
	return f.conn.WithProject(project).GetInstanceNames(ctx, nil)
}

// roundEnd is how a round finished, which decides both what the next one costs
// and how soon it starts.
type roundEnd int

const (
	// roundDone reached the end, which is the only thing that earns the trickle.
	roundDone roundEnd = iota

	// roundStopped was abandoned where it stood, by a reconnect or the process
	// going; the next one starts at once rather than leaving the fleet unread.
	roundStopped

	// roundBroken gave up on a listing the daemon would not answer.
	roundBroken
)

// sweep goes round the fleet for the life of the process.
//
// The first round runs at no delay, because nothing is served until it lands -
// and so does one that follows a round abandoned or broken.
func (p *Plugin) sweep(ctx context.Context) {
	fast := true

	for ctx.Err() == nil {
		end := p.round(ctx, fast)

		fast = end != roundDone

		if end == roundStopped {
			continue
		}

		// Between rounds as well as inside one, or a fast fleet would be read
		// again immediately and for ever.
		if !p.pace(ctx, p.opts.ProjectDelay) {
			fast = true
		}
	}
}

// round reads the fleet once.
func (p *Plugin) round(ctx context.Context, fast bool) roundEnd {
	projectDelay, readDelay := p.opts.ProjectDelay, p.opts.ReadDelay
	if fast {
		projectDelay, readDelay = 0, 0
	}

	served, owners, end := p.roundProjects(ctx, projectDelay)
	if end != roundDone {
		return end
	}

	end = p.roundNetworks(ctx, owners, readDelay)
	if end != roundDone {
		return end
	}

	ok := p.say(ctx, sweepMsg{kind: sweepWarm})
	if !ok {
		return roundStopped
	}

	end = p.roundInstances(ctx, served, projectDelay, readDelay)
	if end != roundDone {
		return end
	}

	if !p.say(ctx, sweepMsg{kind: sweepDone}) {
		return roundStopped
	}

	return roundDone
}

// roundProjects reads every project, and answers which are served and which own
// networks. A project that could not be read still counts as served, or it
// would be pruned on the strength of a failed request.
func (p *Plugin) roundProjects(ctx context.Context, delay time.Duration) ([]string, []string, roundEnd) {
	ok := p.say(ctx, sweepMsg{kind: sweepAsking, about: sweepProjects})
	if !ok {
		return nil, nil, roundStopped
	}

	names, err := p.list(ctx, func(ctx context.Context) ([]string, error) {
		return p.fleet.ProjectNames(ctx)
	})
	if err != nil {
		p.say(ctx, sweepMsg{kind: sweepFailed, err: fmt.Errorf("listing projects: %w", err)})

		return nil, nil, roundBroken
	}

	// Every bridge lives in the default project unless a project owns its own,
	// so it is an owner whether or not it is served.
	var (
		served = make([]string, 0, len(names))
		owners = []string{defaultProject}
	)

	for _, name := range names {
		project, err := p.read1(ctx, name)
		if err != nil {
			p.say(ctx, sweepMsg{kind: sweepFailed, err: err})

			served = append(served, name)

			continue
		}

		if p.opts.Project != nil && !p.opts.Project(project) {
			continue
		}

		served = append(served, name)

		if name != defaultProject && incusutil.IsTrue(project.Config[featuresNetworks]) {
			owners = append(owners, name)
		}

		ok := p.say(ctx, sweepMsg{kind: sweepProject, project: name, config: project.Config})
		if !ok {
			return nil, nil, roundStopped
		}

		ok = p.pace(ctx, delay)
		if !ok {
			return nil, nil, roundStopped
		}
	}

	ok = p.say(ctx, sweepMsg{kind: sweepProjects, names: served})
	if !ok {
		return nil, nil, roundStopped
	}

	return served, owners, roundDone
}

// roundNetworks reads every network of every owner.
//
// Per owner rather than one listing: a project with features.networks owns its
// own, and asking one project answers with that project's alone.
func (p *Plugin) roundNetworks(ctx context.Context, owners []string, delay time.Duration) roundEnd {
	for _, owner := range owners {
		ok := p.say(ctx, sweepMsg{kind: sweepAsking, about: sweepWires, project: owner})
		if !ok {
			return roundStopped
		}

		names, err := p.list(ctx, func(ctx context.Context) ([]string, error) {
			return p.fleet.NetworkNames(ctx, owner)
		})
		if err != nil {
			// One owner failing costs its own networks rather than the fleet's.
			p.say(ctx, sweepMsg{kind: sweepFailed, err: fmt.Errorf("listing the networks of %s: %w", owner, err)})

			continue
		}

		ok = p.say(ctx, sweepMsg{kind: sweepWires, project: owner, names: names})
		if !ok {
			return roundStopped
		}

		for _, name := range names {
			ok := p.readWire(ctx, owner, name)
			if !ok {
				return roundStopped
			}

			ok = p.pace(ctx, delay)
			if !ok {
				return roundStopped
			}
		}
	}

	return roundDone
}

// readWire reads one network and hands it over.
func (p *Plugin) readWire(ctx context.Context, owner, name string) bool {
	readCtx, cancel := context.WithTimeout(ctx, p.opts.ReadTimeout)
	defer cancel()

	wire, err := p.readNet(readCtx, owner, name)
	if err != nil {
		return p.say(ctx, sweepMsg{kind: sweepFailed, err: err})
	}

	return p.say(ctx, sweepMsg{kind: sweepWire, wire: wire})
}

// roundInstances lists each project's instances and trickles the names. The
// listing goes over as one message before any of it is trickled, so absence is
// decided against what was held when the request went out.
func (p *Plugin) roundInstances(ctx context.Context, served []string, projectDelay, readDelay time.Duration) roundEnd {
	for _, project := range served {
		ok := p.say(ctx, sweepMsg{kind: sweepAsking, about: sweepInstances, project: project})
		if !ok {
			return roundStopped
		}

		names, err := p.list(ctx, func(ctx context.Context) ([]string, error) {
			return p.fleet.InstanceNames(ctx, project)
		})
		if err != nil {
			p.say(ctx, sweepMsg{kind: sweepFailed, err: fmt.Errorf("listing the instances of %s: %w", project, err)})

			continue
		}

		ok = p.say(ctx, sweepMsg{kind: sweepInstances, project: project, names: names})
		if !ok {
			return roundStopped
		}

		for _, name := range names {
			ok := p.say(ctx, sweepMsg{kind: sweepInstance, project: project, name: name})
			if !ok {
				return roundStopped
			}

			ok = p.pace(ctx, readDelay)
			if !ok {
				return roundStopped
			}
		}

		ok = p.pace(ctx, projectDelay)
		if !ok {
			return roundStopped
		}
	}

	return roundDone
}

// list bounds one listing by the read timeout.
func (p *Plugin) list(ctx context.Context, fn func(context.Context) ([]string, error)) ([]string, error) {
	readCtx, cancel := context.WithTimeout(ctx, p.opts.ReadTimeout)
	defer cancel()

	return fn(readCtx)
}

// read1 reads one project.
func (p *Plugin) read1(ctx context.Context, name string) (*incusapi.Project, error) {
	readCtx, cancel := context.WithTimeout(ctx, p.opts.ReadTimeout)
	defer cancel()

	project, err := p.fleet.Project(readCtx, name)
	if err != nil {
		return nil, fmt.Errorf("reading project %s: %w", name, err)
	}

	return project, nil
}

// say hands one message to Run, and reports whether the round may continue.
func (p *Plugin) say(ctx context.Context, msg sweepMsg) bool {
	select {
	case p.sweeps <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}

// pace waits d, and reports whether the round may continue. A reconnect ends it
// wherever it has got to, because everything behind that point is as old as the
// stream was down.
func (p *Plugin) pace(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return false
		case <-p.restart:
			return false
		default:
			return true
		}
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-p.restart:
		return false
	case <-timer.C:
		return true
	}
}

// restartSweep abandons the round and starts a fast one. Never blocks: a
// restart already pending says the same thing.
func (p *Plugin) restartSweep() {
	select {
	case p.restart <- struct{}{}:
	default:
	}
}

// absorbSweep takes one message from the sweeper.
func (p *Plugin) absorbSweep(ctx context.Context, msg sweepMsg) {
	switch msg.kind {
	case sweepAsking:
		p.asked = p.holding(msg.about, msg.project)

	case sweepProject:
		p.m.putProject(msg.project, msg.config)

		// Read, so the next event naming it is enriched rather than starting a
		// round - and one that goes away and comes back starts one again.
		delete(p.checked, msg.project)

	case sweepProjects:
		for _, project := range missing(p.asked, msg.names) {
			p.m.dropProject(project)
		}

	case sweepWire:
		p.m.putWire(*msg.wire)

	case sweepWires:
		for _, key := range missing(p.asked, keys(msg.project, msg.names)) {
			project, name, _ := strings.Cut(key, "/")
			p.m.dropWire(project, name)
		}

	case sweepWarm:
		if !p.warm {
			p.thaw(ctx)
		}

	case sweepInstances:
		for _, key := range missing(p.asked, keys(msg.project, msg.names)) {
			project, name, _ := strings.Cut(key, "/")

			p.m.dropInstance(project, name)
			delete(p.archive, archiveKey(instancePrefix, project, name))
		}

	case sweepInstance:
		p.accept(ctx, iutil.NewEvent(time.Now(),
			incusapi.EventLifecycleInstanceUpdated, msg.project, msg.name, "").
			WithChainState(p.chain))

		for ev := range p.q.release() {
			p.next(ev)
		}

	case sweepDone:
		p.raise(ctx, iutil.ActionSweepEnd, "")

	case sweepFailed:
		slog.Warn("the round could not read part of the fleet, serving what is held",
			"plugin", name, "err", msg.err)
	}
}

// holding is what the model has in one scope, taken when its listing goes out.
func (p *Plugin) holding(about sweepKind, project string) []string {
	var out []string

	switch about {
	case sweepProjects:
		for name := range p.m.projects {
			out = append(out, name)
		}

	case sweepWires:
		prefix := project + "/"

		for key := range p.m.wires {
			if strings.HasPrefix(key, prefix) {
				out = append(out, key)
			}
		}

	case sweepInstances:
		prefix := project + "/"

		for key := range p.m.instances {
			if strings.HasPrefix(key, prefix) {
				out = append(out, key)
			}
		}

	default:
		// Nothing else names a scope, so nothing else can be pruned.
	}

	return out
}

// keys renders one project's names the way the model keys them.
func keys(project string, names []string) []string {
	out := make([]string, len(names))

	for i, name := range names {
		out[i] = key(project, name)
	}

	return out
}

// missing is everything in held that listed does not have.
func missing(held, listed []string) []string {
	has := make(map[string]bool, len(listed))

	for _, name := range listed {
		has[name] = true
	}

	var out []string

	for _, name := range held {
		if !has[name] {
			out = append(out, name)
		}
	}

	return out
}
