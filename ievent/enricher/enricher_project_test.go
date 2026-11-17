package enricher

import (
	"fmt"
	"maps"
	"testing"
	"time"

	incusapi "github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/ievent/iutil"
	"github.com/lxc/incus-compose/internal/testlib"
)

// marker is a key a consumer filters projects by, the way ic-healthd does with
// user.healthcheck.project.
const marker = "user.marker"

// setProjectConfig changes what the fixture's project answers with from here on.
// A copy rather than in place: a read hands the state this very map, and editing
// it would change what was read before as well.
func (h *fixture) setProjectConfig(key, value string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	config := maps.Clone(h.fleet.Project.Config)
	config[key] = value
	h.fleet.Project.Config = config
}

// carries reports whether ev came with the project setting key to value.
func carries(ev *iutil.Event, key, value string) bool {
	if !ev.Enriched(iutil.EnrichedProject) {
		return false
	}

	got, ok := ev.Project().ConfigValue(key)

	return ok && got == value
}

// TestProjectUpdateRereadsTheProject: a project's configuration is read once and
// rides on every instance event in it, so a project marked after that read
// stayed unmarked until the next run, hours away. The update reads it again,
// and every instance held there goes on again carrying what it now says.
func TestProjectUpdateRereadsTheProject(t *testing.T) {
	t.Parallel()

	h := seeded(t, 2, 1)

	h.setProjectConfig(marker, "yes")
	h.send(incusapi.EventLifecycleProjectUpdated, "p", "p")

	assert.Equal(t, incusapi.EventLifecycleProjectUpdated, h.next().Action())

	fanned := h.collect(2)
	assert.ElementsMatch(t,
		[]string{testlib.InstanceName(0), testlib.InstanceName(1)},
		[]string{fanned[0].Name(), fanned[1].Name()},
		"every instance held in the project went on again")

	for _, ev := range fanned {
		assert.True(t, carries(ev, marker, "yes"), "%s still carries the project as first read", ev.Name())
	}
}

// TestProjectDeleteForgetsIt: a project deleted and created again under the same
// name is a new project, and must be read as one rather than answered from what
// the old one said.
func TestProjectDeleteForgetsIt(t *testing.T) {
	t.Parallel()

	h := seeded(t, 0, 1)

	h.mu.Lock()
	h.noNIC = true
	h.mu.Unlock()

	h.send(incusapi.EventLifecycleProjectDeleted, "p", "p")
	assert.Equal(t, incusapi.EventLifecycleProjectDeleted, h.next().Action())

	// Created again, marked this time. Nothing announces the creation to the
	// enricher: the first instance event in it is what has it read.
	h.setProjectConfig(marker, "yes")

	tries := 0
	require.Eventually(t, func() bool {
		tries++
		h.retag(fmt.Sprint(tries))
		h.send(incusapi.EventLifecycleInstanceUpdated, "p", testlib.InstanceName(0))

		return carries(h.next(), marker, "yes")
	}, 2*time.Second, 10*time.Millisecond, "the recreated project was answered from the deleted one")
}

// TestProjectRenameForgetsTheOldName: Incus renames only an empty project, and
// what was filed under the old name describes nothing any more.
func TestProjectRenameForgetsTheOldName(t *testing.T) {
	t.Parallel()

	h := seeded(t, 0, 1)

	h.p.Handle(iutil.NewEvent(time.Now(), incusapi.EventLifecycleProjectRenamed, "q", "q", "p"))
	assert.Equal(t, incusapi.EventLifecycleProjectRenamed, h.next().Action())

	h.stop()

	assert.Nil(t, h.p.state.projectConfig("p"), "the old name survived the rename")
}

// TestProjectUpdateDropsAReadAlreadyOut: a read issued before the update may be
// answered before it too. Joined, its answer would be taken for the new one.
func TestProjectUpdateDropsAReadAlreadyOut(t *testing.T) {
	t.Parallel()

	p := bare(t)
	p.args.Wanted = wanted
	ctx := t.Context()

	key := resourceKey(kindProject, "p", "")
	before := &call{key: key, kind: kindProject, project: "p"}
	p.reads.send(ctx, before)

	p.accept(ctx, iutil.NewEvent(time.Now(), incusapi.EventLifecycleProjectUpdated, "p", "p", ""))
	require.Equal(t, 1, p.reads.owes(), "the abandoned read is still owed, or its replacement is not")

	p.settleRead(ctx, result{call: before, project: &incusapi.Project{ProjectPut: incusapi.ProjectPut{Config: map[string]string{marker: "old"}}}})
	assert.Nil(t, p.state.projectConfig("p"), "the answer read before the update was taken")

	after := p.reads.calls[key]
	require.NotNil(t, after, "the update read nothing again")

	p.settleRead(ctx, result{call: after, project: &incusapi.Project{ProjectPut: incusapi.ProjectPut{Config: map[string]string{marker: "new"}}}})
	assert.Equal(t, "new", p.state.projectConfig("p")[marker])
	assert.Zero(t, p.reads.owes())
}

// TestProjectDeleteEndsARunWaitingOnIt: a run that reached its end with a
// project read still out waits for it. Abandoned by a delete, that read would
// never land, and the chain would never be told the fleet is warm.
func TestProjectDeleteEndsARunWaitingOnIt(t *testing.T) {
	t.Parallel()

	p := bare(t)
	p.args.Wanted = wanted
	out := make(chan iutil.Command, 1)
	p.args.CommandOut = out
	ctx := t.Context()

	p.reads.send(ctx, &call{key: resourceKey(kindProject, "p", ""), kind: kindProject, project: "p"})
	p.sweepEnding = true

	p.accept(ctx, iutil.NewEvent(time.Now(), incusapi.EventLifecycleProjectDeleted, "p", "p", ""))

	assert.Zero(t, p.reads.owes())

	select {
	case cmd := <-out:
		assert.Equal(t, iutil.ActionSweepEnd, cmd.Action)
	default:
		t.Fatal("the run never ended")
	}
}
