package dns

import (
	"context"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"

	incusapi "github.com/lxc/incus/v7/shared/api"
)

// Reconciling what is served against Incus. held is this plugin's store, and a
// store is reconciled by whoever owns it.

// namesReq asks for the instance names of a set of projects.
type namesReq struct{ projects []string }

// namesRes is one project's instance names, or why they could not be read.
type namesRes struct {
	project string
	names   []string
	err     error
}

// reconcile answers listings until ctx is canceled. Its own goroutine, because
// the fold loop may not block on Incus.
func (p *Plugin) reconcile(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return

		case req := <-p.asking:
			for _, project := range req.projects {
				select {
				case p.listed <- p.look(ctx, project):
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// look answers what one project still holds. A gone project and one whose
// marker now excludes it both answer with no names, so prune drops every record in it.
func (p *Plugin) look(ctx context.Context, project string) namesRes {
	if p.projectOf != nil {
		read, err := p.projectOf(ctx, project)

		switch {
		case incusapi.StatusErrorCheck(err, http.StatusNotFound):
			return namesRes{project: project}

		case err != nil:
			return namesRes{project: project, err: err}

		case p.cfg.Project != nil && !p.cfg.Project(read):
			return namesRes{project: project}
		}
	}

	names, err := p.namesOf(ctx, project)

	return namesRes{project: project, names: names, err: err}
}

// reconcileSoon asks about every project held, recording what was asked so a
// name folded in mid-flight survives. Skipped mid-round.
func (p *Plugin) reconcileSoon() {
	if p.namesOf == nil {
		return
	}

	asked := map[string][]string{}

	for key := range p.held {
		project, _, found := strings.Cut(key, "/")
		if !found {
			continue
		}

		asked[project] = append(asked[project], key)
	}

	if len(asked) == 0 {
		return
	}

	select {
	case p.asking <- namesReq{projects: slices.Collect(maps.Keys(asked))}:
		p.asked = asked
	default:
	}
}

// prune drops what is held and the listing does not have.
func (p *Plugin) prune(res namesRes) {
	asked := p.asked[res.project]
	delete(p.asked, res.project)

	if res.err != nil {
		// Nothing is dropped on a failed listing: an empty answer and an
		// unreachable daemon arrive the same way.
		slog.Warn("reconciling what is served", "project", res.project, "err", res.err)

		return
	}

	listed := make(map[string]bool, len(res.names))

	for _, name := range res.names {
		listed[heldKey(res.project, name)] = true
	}

	gone := 0

	for _, key := range asked {
		_, held := p.held[key]
		if listed[key] || !held {
			continue
		}

		delete(p.held, key)

		gone++
	}

	if gone == 0 {
		return
	}

	slog.Info("dropped what is no longer served", "project", res.project, "records", gone)

	p.publish()
}
