package dns

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"testing"

	incusapi "github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/ievent/iutil"
)

// reconciler is a plugin with a fake listing, and its reconciler running. Every
// project it is asked about exists and is served unless a test says otherwise.
func reconciler(t *testing.T, names map[string][]string, err error, opts ...Option) *Plugin {
	t.Helper()

	p := New(append([]Option{Suffix("incus")}, opts...)...)
	p.next = func(_ *iutil.Event) {}
	p.namesOf = func(_ context.Context, project string) ([]string, error) {
		if err != nil {
			return nil, err
		}

		return names[project], nil
	}
	p.projectOf = func(_ context.Context, project string) (*incusapi.Project, error) {
		if !slices.Contains(slices.Collect(maps.Keys(names)), project) {
			return nil, incusapi.StatusErrorf(http.StatusNotFound, "project not found")
		}

		return &incusapi.Project{
			Name:       project,
			ProjectPut: incusapi.ProjectPut{Config: map[string]string{"user.coredns": "true"}},
		}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go p.reconcile(ctx)

	return p
}

// reconcile asks and applies one answer, which is what a round end does.
func reconcile(t *testing.T, p *Plugin) {
	t.Helper()

	p.reconcileSoon()

	select {
	case res := <-p.listed:
		p.prune(res)
	case <-t.Context().Done():
		t.Fatal("the reconciler never answered")
	}
}

// TestReconcileDropsWhatIncusNoLongerHas pins the one path to a name the cold
// store restored and Incus has since lost, since no event ever announces it.
func TestReconcileDropsWhatIncusNoLongerHas(t *testing.T) {
	t.Parallel()

	p := reconciler(t, map[string][]string{"shop": {"db"}}, nil)

	p.fold(enriched(incusapi.EventLifecycleInstanceStarted, "shop", "web", "10.0.0.2"))
	p.fold(enriched(incusapi.EventLifecycleInstanceStarted, "shop", "db", "10.0.0.3"))

	require.Len(t, p.held, 2)

	reconcile(t, p)

	assert.NotContains(t, p.held, heldKey("shop", "web"), "Incus no longer has it")
	assert.Contains(t, p.held, heldKey("shop", "db"))
}

// TestReconcileKeepsWhatArrivedWhileItAsked pins that absence is decided
// against what was held when the request went out, not against held as it stands now.
func TestReconcileKeepsWhatArrivedWhileItAsked(t *testing.T) {
	t.Parallel()

	p := reconciler(t, map[string][]string{"shop": {"db"}}, nil)

	p.fold(enriched(incusapi.EventLifecycleInstanceStarted, "shop", "db", "10.0.0.3"))

	// Asked about shop, and only db was held at that moment.
	p.reconcileSoon()

	p.fold(enriched(incusapi.EventLifecycleInstanceStarted, "shop", "web", "10.0.0.2"))

	p.prune(<-p.listed)

	assert.Contains(t, p.held, heldKey("shop", "web"),
		"a record folded in while the listing was in flight was dropped")
	assert.Contains(t, p.held, heldKey("shop", "db"))
}

// TestReconcileDropsNothingWhenTheListingFailed pins that a failed listing
// drops nothing: it looks the same as an empty answer meaning "every record".
func TestReconcileDropsNothingWhenTheListingFailed(t *testing.T) {
	t.Parallel()

	// The project is there and served; it is the listing behind it that refuses.
	p := reconciler(t, map[string][]string{"shop": nil}, errors.New("incusd said no"))

	p.fold(enriched(incusapi.EventLifecycleInstanceStarted, "shop", "web", "10.0.0.2"))

	reconcile(t, p)

	assert.Contains(t, p.held, heldKey("shop", "web"))
}

// TestReconcileSkipsWhileTheLastRoundIsStillOut: one listing per project per
// round is what this costs, and asking twice says nothing the first did not.
func TestReconcileSkipsWhileTheLastRoundIsStillOut(t *testing.T) {
	t.Parallel()

	p := New()
	p.next = func(_ *iutil.Event) {}
	p.namesOf = func(_ context.Context, _ string) ([]string, error) { return nil, nil }

	p.fold(enriched(incusapi.EventLifecycleInstanceStarted, "shop", "web", "10.0.0.2"))

	// Nothing is reading asking, so the one slot stays taken.
	p.reconcileSoon()
	require.Len(t, p.asking, 1)

	p.reconcileSoon()
	assert.Len(t, p.asking, 1, "a second round queued a listing behind the first")
}

// TestReconcileAsksAboutNothingWhenItHoldsNothing: a fresh process holds no
// record, so there is no project to ask about and nothing to prune.
func TestReconcileAsksAboutNothingWhenItHoldsNothing(t *testing.T) {
	t.Parallel()

	p := New()
	p.namesOf = func(_ context.Context, _ string) ([]string, error) { return nil, nil }

	p.reconcileSoon()

	assert.Empty(t, p.asking)
}

// TestReconcileDropsAProjectItNoLongerServes pins that a marker gone is a
// delete for us; the predicate comes from main since Incus cannot say so itself.
func TestReconcileDropsAProjectItNoLongerServes(t *testing.T) {
	t.Parallel()

	p := reconciler(t, map[string][]string{"shop": {"web"}}, nil,
		Project(func(project *incusapi.Project) bool {
			return project.Config["user.coredns"] == "yes"
		}))

	p.fold(enriched(incusapi.EventLifecycleInstanceStarted, "shop", "web", "10.0.0.2"))

	reconcile(t, p)

	assert.Empty(t, p.held, "the project still answers with its marker gone")
}

// TestReconcileDropsAProjectThatIsGone pins that reading the project, not the
// listing, tells a deleted project from an empty one: Incus answers both with [].
func TestReconcileDropsAProjectThatIsGone(t *testing.T) {
	t.Parallel()

	asked := 0

	p := reconciler(t, map[string][]string{"blog": {"www"}}, nil)
	p.namesOf = func(_ context.Context, _ string) ([]string, error) {
		asked++

		return nil, nil
	}

	p.fold(enriched(incusapi.EventLifecycleInstanceStarted, "shop", "web", "10.0.0.2"))

	reconcile(t, p)

	assert.Empty(t, p.held, "a project Incus no longer has kept its records")
	assert.Zero(t, asked, "a project that is gone was listed anyway")
}

// TestReconcileKeepsAProjectItStillServes is the other side of both: present and
// marked is read for its names like any other.
func TestReconcileKeepsAProjectItStillServes(t *testing.T) {
	t.Parallel()

	p := reconciler(t, map[string][]string{"shop": {"web"}}, nil,
		Project(func(project *incusapi.Project) bool {
			return project.Config["user.coredns"] == "true"
		}))

	p.fold(enriched(incusapi.EventLifecycleInstanceStarted, "shop", "web", "10.0.0.2"))

	reconcile(t, p)

	assert.Contains(t, p.held, heldKey("shop", "web"))
}

// TestReconcileDropsNothingWhenTheProjectReadFailed: a daemon that would not
// answer must not read as a marker that has gone.
func TestReconcileDropsNothingWhenTheProjectReadFailed(t *testing.T) {
	t.Parallel()

	p := reconciler(t, map[string][]string{"shop": {"web"}}, nil)
	p.projectOf = func(_ context.Context, _ string) (*incusapi.Project, error) {
		return nil, errors.New("incusd said no")
	}

	p.fold(enriched(incusapi.EventLifecycleInstanceStarted, "shop", "web", "10.0.0.2"))

	reconcile(t, p)

	assert.Contains(t, p.held, heldKey("shop", "web"))
}
