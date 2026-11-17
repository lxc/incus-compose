package enricher

import (
	"context"
	"strings"

	incusapi "github.com/lxc/incus/v7/shared/api"

	"github.com/lxc/incus-compose/ievent/iutil"
)

// projectPrefix is what an action names a project with.
const projectPrefix = "project-"

// acceptProject handles one project action, and reports whether it was one.
//
// A project's configuration rides on every instance event in it, and is read
// only where nothing holds it yet. Without these, a project marked after its
// first read, or recreated under a name read before, would keep the answer that
// first read gave until the next run, hours away.
func (p *Plugin) acceptProject(ctx context.Context, ev *iutil.Event) bool {
	if !strings.HasPrefix(ev.Action(), projectPrefix) {
		return false
	}

	// The event that happened goes in first, ahead of anything it causes.
	p.q.push(ev, true)

	// A rename leaves the old name behind, which describes nothing any more.
	if ev.OldName() != "" {
		p.forgetProject(ctx, ev.OldName())
	}

	if ev.Action() == incusapi.EventLifecycleProjectDeleted {
		p.forgetProject(ctx, ev.Name())
	} else {
		// Read again even where held, which is the point. A read already out
		// may have been answered before the change, so it is dropped rather than
		// joined. What lands re-reads the instances held there, so what they
		// carry follows.
		p.abandonProjectRead(ctx, ev.Name())
		p.reads.send(ctx, &call{
			key:     resourceKey(kindProject, ev.Name(), ""),
			kind:    kindProject,
			project: ev.Name(),
		})
	}

	// A run waiting on its last project read may have just lost it.
	if p.sweepEnding && p.reads.owes() == 0 {
		p.sweepEnd(ctx)
	}

	return true
}

// forgetProject drops one project and what is filed under it. Incus deletes or
// renames only an empty project, but the instances' archive entries would make
// one recreated under the same name look like nothing new.
func (p *Plugin) forgetProject(ctx context.Context, name string) {
	p.abandonProjectRead(ctx, name)

	for s := range p.state.projectInstances(name) {
		p.archive.forget(s.project, s.instance)
		p.retries.done(s.project, s.instance)
	}

	p.state.deleteProject(name)
	p.updateMetrics()
}

// abandonProjectRead settles a project read still out as if it had landed with
// nothing: what it owed the run is paid and the instance reads queued behind it
// go, while its answer, once it comes, is no longer current and is ignored.
func (p *Plugin) abandonProjectRead(ctx context.Context, name string) {
	c := p.reads.cancel(resourceKey(kindProject, name, ""))
	if c != nil {
		p.reads.done(ctx, c)
	}
}
