package enricher

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	incusapi "github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/ievent/iutil"
	"github.com/lxc/incus-compose/internal/testlib"
)

// wanted is the table the source would have built.
var wanted = map[string]iutil.Want{
	incusapi.EventLifecycleInstanceUpdated: {
		Action: incusapi.EventLifecycleInstanceUpdated,
		Enrich: iutil.EnrichedInstance | iutil.EnrichedNetworks | iutil.EnrichedProject,
	},
	incusapi.EventLifecycleInstanceDeleted: {Action: incusapi.EventLifecycleInstanceDeleted},

	// Wanted for its networks alone, which is what makes it the case that a
	// name does not imply an instance.
	incusapi.EventLifecycleNetworkUpdated: {
		Action: incusapi.EventLifecycleNetworkUpdated,
		Enrich: iutil.EnrichedNetworks,
	},
	incusapi.EventLifecycleNetworkDeleted: {Action: incusapi.EventLifecycleNetworkDeleted},
	incusapi.EventLifecycleNetworkRenamed: {Action: incusapi.EventLifecycleNetworkRenamed},
	incusapi.EventLifecycleProfileUpdated: {Action: incusapi.EventLifecycleProfileUpdated},
	incusapi.EventLifecycleProfileDeleted: {Action: incusapi.EventLifecycleProfileDeleted},
}

// harness wires one plugin to a collecting successor and runs it the way main
// would, answering its reads from testlib rather than a daemon.
type harness struct {
	t   *testing.T
	p   *Plugin
	out chan *iutil.Event

	cancel context.CancelFunc
	wg     sync.WaitGroup

	// in and raised are the two doors the source gives a plugin: one to ask it
	// something, one for it to say something.
	in     chan iutil.Command
	raised chan iutil.Command

	// forward stops the goroutine that plays the source, so a drain can read its
	// own answer off raised. forwarded says it has actually gone - closing
	// forward alone races the drain answer, since select picks at random.
	forward     chan struct{}
	forwarded   chan struct{}
	stopForward sync.Once

	mu    sync.Mutex
	reads map[string]int
	err   error

	// leaseless answers every instance read with a running instance holding no
	// address, which is what one reads between its start and its DHCP lease.
	leaseless bool

	// gate holds every read until it is closed, so a test can decide when a
	// read lands and in which order.
	gate chan struct{}

	// tag goes into every instance read, so a test can make the next read say
	// something new.
	tag string

	// noNIC answers with an instance holding no network device, so a test about
	// something else is not also a test of whether its wire has been read.
	noNIC bool

	// fleet is what a round finds, and listErr what its listings fail with. Nil
	// fleet means every listing refuses, so no round ever reaches the chain.
	fleet   *testlib.Project
	listErr error
	lists   atomic.Int32
}

// errNoFleet is what the listings answer until a test sets one.
var errNoFleet = errors.New("this harness has no fleet")

// ProjectNames, Project, NetworkNames and InstanceNames are what the sweeper
// lists instead of a daemon.
func (h *harness) ProjectNames(_ context.Context) ([]string, error) {
	h.lists.Add(1)

	fleet, err := h.listing()
	if err != nil {
		return nil, err
	}

	return []string{fleet.Project.Name}, nil
}

func (h *harness) Project(_ context.Context, name string) (*incusapi.Project, error) {
	fleet, err := h.listing()
	if err != nil {
		return nil, err
	}

	if name != fleet.Project.Name {
		return nil, incusapi.StatusErrorf(http.StatusNotFound, "no such project")
	}

	return &fleet.Project, nil
}

func (h *harness) NetworkNames(_ context.Context, project string) ([]string, error) {
	fleet, err := h.listing()
	if err != nil {
		return nil, err
	}

	var out []string

	for _, net := range fleet.Networks {
		if net.Project == project {
			out = append(out, net.Name)
		}
	}

	return out, nil
}

func (h *harness) InstanceNames(_ context.Context, project string) ([]string, error) {
	fleet, err := h.listing()
	if err != nil {
		return nil, err
	}

	var out []string

	for _, inst := range fleet.Instances {
		if inst.Project == project {
			out = append(out, inst.Name)
		}
	}

	return out, nil
}

// listing is what every listing above starts with.
func (h *harness) listing() (*testlib.Project, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.listErr != nil {
		return nil, h.listErr
	}

	if h.fleet == nil {
		return nil, errNoFleet
	}

	return h.fleet, nil
}

// retag makes every instance read from here on say something new.
func (h *harness) retag(tag string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.tag = tag
}

// setFleet gives the harness a fleet and starts a round over it, the way a
// reconnect does.
func (h *harness) setFleet(fleet *testlib.Project) {
	h.mu.Lock()
	h.fleet = fleet
	h.mu.Unlock()

	h.send(iutil.ActionConnected, "", "")
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	// Not t.Context(): the testing package cancels that just before cleanups
	// run, so a harness that drains in a cleanup would find the plugin already
	// aborted and waiting for an answer it can no longer give.
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{
		t:         t,
		p:         New(Workers(8), ReadTimeout(time.Second)),
		out:       make(chan *iutil.Event, 64),
		in:        make(chan iutil.Command),
		raised:    make(chan iutil.Command, 8),
		forward:   make(chan struct{}),
		forwarded: make(chan struct{}),
		cancel:    cancel,
		reads:     map[string]int{},
	}

	h.p.read = h.answer
	h.p.readNet = h.answerNet
	h.p.fleet = h

	err := h.p.Setup(iutil.SetupArgs{
		Context:    ctx,
		Wanted:     wanted,
		Next:       func(ev *iutil.Event) { h.out <- ev },
		CommandIn:  h.in,
		CommandOut: h.raised,
	})
	require.NoError(t, err)

	// What the source does with a raised action: mint it and put it in at the
	// head, which is the enricher's own door with nothing in front of it here.
	h.wg.Go(func() {
		defer close(h.forwarded)

		for {
			select {
			case cmd := <-h.raised:
				h.p.Handle(iutil.NewEvent(time.Now(), cmd.Action, "", "", ""))
			case <-h.forward:
				return
			case <-ctx.Done():
				return
			}
		}
	})

	h.wg.Go(func() {
		err := h.p.Run(ctx)
		if err != nil {
			t.Errorf("Run: %s", err)
		}
	})

	t.Cleanup(h.stop)

	return h
}

// stop shuts the plugin down the way the source does, and is safe to call twice
// so a test can assert on what the shutdown handed on. Ask, wait for the
// answer, then cancel - canceling first is an abort, and everything held would
// go nowhere.
func (h *harness) stop() {
	h.stopForward.Do(func() {
		// The goroutine playing the source has to stop reading raised first,
		// and be gone rather than merely told to go, or it can still take the
		// answer below instead of us.
		close(h.forward)

		select {
		case <-h.forwarded:
		case <-time.After(5 * time.Second):
			h.t.Error("the goroutine playing the source never stopped")

			return
		}

		select {
		case h.in <- iutil.Command{Action: iutil.CommandDrain}:
		case <-time.After(5 * time.Second):
			h.t.Error("the enricher never took the drain")

			return
		}

		for {
			select {
			case cmd := <-h.raised:
				if cmd.Action != iutil.CommandDrain {
					continue
				}

			case <-time.After(5 * time.Second):
				h.t.Error("the enricher never answered the drain")
			}

			return
		}
	})

	h.cancel()
	h.wg.Wait()
}

// answer is what a pool worker calls instead of Incus. The instance it builds
// carries the name asked for, so a test can tell one read from another.
func (h *harness) answer(
	ctx context.Context,
	project, name string,
) (*incusapi.Instance, *incusapi.InstanceState, error) {
	h.mu.Lock()
	h.reads[project+"/"+name]++
	gate, failWith, leaseless, tag, noNIC := h.gate, h.err, h.leaseless, h.tag, h.noNIC
	h.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}

	if failWith != nil {
		return nil, nil, failWith
	}

	inst := testlib.NewInstance(project, 0, 0)
	inst.Name = name
	inst.ExpandedConfig["user.label."+testlib.LabelPrefix+"service"] = name

	if tag != "" {
		inst.ExpandedConfig["user.label."+testlib.LabelPrefix+"tag"] = tag
	}

	if noNIC {
		inst.ExpandedDevices = map[string]map[string]string{}
	}

	state := testlib.NewInstanceState(0, 0)
	if leaseless {
		state.Network["eth0"] = incusapi.InstanceStateNetwork{Type: "broadcast"}
	}

	return &inst, state, nil
}

// answerNet is what a pool worker calls instead of reading one network.
func (h *harness) answerNet(_ context.Context, project, name string) (*incusapi.Network, error) {
	h.mu.Lock()
	// Counted apart from the instance reads: the two are keyed the same way, and
	// telling them apart is the whole point of one of the tests below.
	h.reads["net:"+project+"/"+name]++
	failWith := h.err
	h.mu.Unlock()

	if failWith != nil {
		return nil, failWith
	}

	net := testlib.NewNetwork(project, 0)
	net.Name = name

	return &net, nil
}

// readsOf is how many instance reads one key has cost.
func (h *harness) readsOf(project, name string) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.reads[project+"/"+name]
}

// netReadsOf is how many network reads one key has cost.
func (h *harness) netReadsOf(project, name string) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.reads["net:"+project+"/"+name]
}

func (h *harness) send(action, project, name string) {
	h.t.Helper()

	h.p.Handle(iutil.NewEvent(time.Now(), action, project, name, ""))
}

func (h *harness) next() *iutil.Event {
	h.t.Helper()

	select {
	case ev := <-h.out:
		return ev
	case <-time.After(2 * time.Second):
		h.t.Fatal("timed out waiting for an event")

		return nil
	}
}

// TestOrderIsArrivalOrder is the contract the whole shape rests on, and the one
// worth pinning before any read exists to disturb it.
func TestOrderIsArrivalOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		arrive []string
	}{
		{name: "one instance", arrive: []string{"a"}},
		{name: "several instances", arrive: []string{"a", "b", "c", "d"}},
		// One key repeatedly is not an order case: reads after the first find
		// what it found, and only it walks. See TestAnUnchangedInstanceEventGoesNoFurther.
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := seeded(t, 0, 0)

			for _, n := range tc.arrive {
				h.send(incusapi.EventLifecycleInstanceUpdated, "p", n)
			}

			for i, n := range tc.arrive {
				assert.Equal(t, n, h.next().Name(), "position %d", i)
			}
		})
	}
}

// TestPassesEverythingThrough covers the kinds that need no read at all. They
// still take their place in the line rather than overtaking.
func TestPassesEverythingThrough(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		action string
		who    string
	}{
		{name: "an action with nothing to enrich", action: incusapi.EventLifecycleInstanceDeleted, who: "one"},
		{name: "an action nobody wanted", action: incusapi.EventLifecycleInstanceMigrated, who: "one"},
		{name: "the source's own, which carries no name", action: iutil.ActionConnected},
		{name: "the end of a round", action: iutil.ActionSweepEnd},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)

			h.send(tc.action, "p", tc.who)

			ev := h.next()
			assert.Equal(t, tc.action, ev.Action())
			assert.Equal(t, iutil.StateOk, ev.State())
		})
	}
}

// TestAlreadyFinishedEventsAreUntouched: an event a plugin in front is done
// with is walking for the observers, and enriching it would be a read nobody
// asked for.
func TestAlreadyFinishedEventsAreUntouched(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	ev := iutil.NewEvent(time.Now(), incusapi.EventLifecycleInstanceUpdated, "p", "one", "")
	h.p.Handle(ev.WithDropped("debounce"))

	out := h.next()
	assert.Equal(t, iutil.StateDropped, out.State())
	assert.Equal(t, "debounce", out.Reason(), "the first reason is the one that stands")
}

// TestShutdownHandsOnWhatItHolds: nothing this plugin accepted may be swallowed
// on the way out, because an event the chain never saw is worse than a late one.
func TestShutdownHandsOnWhatItHolds(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	for _, n := range []string{"a", "b", "c"} {
		h.send(incusapi.EventLifecycleInstanceUpdated, "p", n)
	}

	h.stop()

	require.Len(t, h.out, 3, "every event taken is handed on")

	for _, n := range []string{"a", "b", "c"} {
		assert.Equal(t, n, (<-h.out).Name(), "and still in order")
	}
}

// TestFullInboxDropsRatherThanBlocks: Handle runs on somebody else's goroutine,
// so it may not wait. A drop rather than a failure - nothing went wrong with a
// read, this plugin is behind.
func TestFullInboxDropsRatherThanBlocks(t *testing.T) {
	t.Parallel()

	// No Run, so nothing drains the inbox: Handle has to cope on its own.
	seen := []*iutil.Event{}
	p := New()
	p.next = func(ev *iutil.Event) { seen = append(seen, ev) }

	for range defaultInboxSize {
		p.Handle(iutil.NewEvent(time.Now(), incusapi.EventLifecycleInstanceUpdated, "p", "one", ""))
	}

	require.Empty(t, seen, "nothing dropped before the inbox was full")

	p.Handle(iutil.NewEvent(time.Now(), incusapi.EventLifecycleInstanceUpdated, "p", "two", ""))

	require.Len(t, seen, 1)
	assert.Equal(t, iutil.StateDropped, seen[0].State())
	assert.Equal(t, name, seen[0].Reason(), "and says who dropped it")
}

// TestEnrichesFromTheRead is the point of the plugin: what leaves carries what
// the read found, and says so.
func TestEnrichesFromTheRead(t *testing.T) {
	t.Parallel()

	h := seeded(t, 0, 0)

	h.send(incusapi.EventLifecycleInstanceUpdated, "p", "one")

	ev := h.next()

	assert.Equal(t, iutil.StateOk, ev.State())
	assert.True(t, ev.Enriched(iutil.EnrichedInstance), "the instance read landed")
	assert.True(t, ev.Running())

	service, ok := ev.Label(testlib.LabelPrefix + "service")
	assert.True(t, ok)
	assert.Equal(t, "one", service, "the labels came off the instance that was read")
}

// TestFailedReadFails is rule 7: what asked for something and did not get it
// says so, rather than arriving looking complete.
func TestFailedReadFails(t *testing.T) {
	t.Parallel()

	h := seeded(t, 0, 0)

	h.mu.Lock()
	h.err = errors.New("incusd said no")
	h.mu.Unlock()

	h.send(incusapi.EventLifecycleInstanceUpdated, "p", "one")

	ev := h.next()

	assert.Equal(t, iutil.StateFailed, ev.State())
	assert.Equal(t, "source/read", ev.Reason(), "an actor, not a bare cause")
	assert.False(t, ev.Enriched(iutil.EnrichedInstance), "and nothing pretends to have landed")
}

// TestCoalescesReadsPerKey: coalescing saves the read, not the event. Two
// events on one key cost one read and both still walk, carrying what it found.
func TestCoalescesReadsPerKey(t *testing.T) {
	t.Parallel()

	h := seeded(t, 0, 0)

	// Held, so the second event arrives while the first read is still out.
	h.mu.Lock()
	h.gate = make(chan struct{})
	gate := h.gate
	h.mu.Unlock()

	h.send(incusapi.EventLifecycleInstanceUpdated, "p", "one")

	// Wait for the read to be running before the second event arrives, so this
	// is coalescing rather than a race that happened to pass.
	require.Eventually(t, func() bool { return h.readsOf("p", "one") == 1 },
		time.Second, time.Millisecond, "the first read started")

	h.send(incusapi.EventLifecycleInstanceUpdated, "p", "one")

	// Behind both of them, so it is what arrives second only if the coalesced
	// one did not walk.
	h.send(incusapi.EventLifecycleInstanceUpdated, "p", "two")

	close(gate)

	first := h.next()

	assert.True(t, first.Enriched(iutil.EnrichedInstance))
	assert.Equal(t, "one", first.Name())
	assert.Equal(t, 1, h.readsOf("p", "one"), "it was one read")

	// Both settled from that read, and the second says what the first said.
	assert.Equal(t, "two", h.next().Name(), "the coalesced event walked with nothing new on it")
}

// TestSlowReadHoldsTheLine is the ordering cost, stated: reads run
// concurrently, delivery does not, so a slow read at the front holds up
// everything behind it.
func TestSlowReadHoldsTheLine(t *testing.T) {
	t.Parallel()

	h := seeded(t, 0, 0)

	h.mu.Lock()
	h.gate = make(chan struct{})
	gate := h.gate
	h.mu.Unlock()

	h.send(incusapi.EventLifecycleInstanceUpdated, "p", "slow")
	require.Eventually(t, func() bool { return h.readsOf("p", "slow") == 1 },
		time.Second, time.Millisecond, "the slow read started")

	for _, n := range []string{"a", "b"} {
		h.send(incusapi.EventLifecycleInstanceUpdated, "p", n)
	}

	// Their reads run and finish; nothing leaves, because the front has not.
	require.Eventually(t, func() bool { return h.readsOf("p", "b") == 1 },
		time.Second, time.Millisecond, "the reads behind it ran anyway")

	assert.Empty(t, h.out, "and none of them left")

	close(gate)

	for _, n := range []string{"slow", "a", "b"} {
		assert.Equal(t, n, h.next().Name(), "then all of it, in arrival order")
	}
}

// TestDeleteCostsNoRead is rule 3: a delete says the subject is gone, and
// reading to confirm it would be a read whose answer we already have.
func TestDeleteCostsNoRead(t *testing.T) {
	t.Parallel()

	h := seeded(t, 0, 0)

	h.send(incusapi.EventLifecycleInstanceUpdated, "p", "one")
	require.True(t, h.next().Enriched(iutil.EnrichedInstance))

	h.send(incusapi.EventLifecycleInstanceDeleted, "p", "one")

	ev := h.next()
	assert.Equal(t, incusapi.EventLifecycleInstanceDeleted, ev.Action())
	assert.Equal(t, 1, h.readsOf("p", "one"), "the delete added no read")
}

// TestOnlyInstanceActionsAreRead: a name is not enough to make something an
// instance. A network action carries one too, and reading an instance called
// net0 is the mistake this guards.
func TestOnlyInstanceActionsAreRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	h.send(incusapi.EventLifecycleNetworkUpdated, "p", "net0")

	ev := h.next()

	assert.Equal(t, incusapi.EventLifecycleNetworkUpdated, ev.Action())
	assert.Equal(t, iutil.StateOk, ev.State())
	assert.Zero(t, h.readsOf("p", "net0"), "no instance read for a network's name")

	// It is read, just as the thing it actually is.
	require.Eventually(t, func() bool { return h.netReadsOf("p", "net0") == 1 },
		2*time.Second, time.Millisecond, "read as a network instead")
}

// collect takes n events off the far end.
func (h *harness) collect(n int) []*iutil.Event {
	h.t.Helper()

	out := make([]*iutil.Event, 0, n)
	for range n {
		out = append(out, h.next())
	}

	return out
}

// TestConnectedSweeps: a stream coming up makes everything held suspect at
// once, because whatever happened while it was down was announced to nobody.
func TestConnectedSweeps(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	fleet := testlib.NewProject("p", 2, 1)
	fleet.Project.Config[featuresNetworks] = "true"

	h.setFleet(fleet)

	// Every name the round trickled, then the end of it. No opening bracket:
	// there is no duration to bracket, only an instant at the end.
	got := h.collect(4)

	assert.Equal(t, iutil.ActionConnected, got[0].Action())

	names := []string{got[1].Name(), got[2].Name()}
	assert.ElementsMatch(t, []string{testlib.InstanceName(0), testlib.InstanceName(1)}, names)

	assert.Equal(t, iutil.ActionSweepEnd, got[3].Action())

	for _, ev := range got[1:3] {
		assert.True(t, ev.Enriched(iutil.EnrichedInstance|iutil.EnrichedNetworks),
			"a round reads the networks before the instances, so what it hands over is complete")
		assert.Equal(t, incusapi.EventLifecycleInstanceUpdated, ev.Action())
	}
}

// TestSweepFillsTheWires is what makes EnrichedNetworks mean anything: the
// round reads the networks first, so an instance read after it lands under a
// known wire.
func TestSweepFillsTheWires(t *testing.T) {
	t.Parallel()

	h := seeded(t, 1, 1)

	// Now a single instance read finds the wire the round put there. Tagged, or
	// the round has already filed what this read finds and it goes nowhere.
	h.retag("moved")
	h.send(incusapi.EventLifecycleInstanceUpdated, "p", testlib.InstanceName(0))

	out := h.next()
	require.True(t, out.Enriched(iutil.EnrichedNetworks))

	found := false
	for _, net := range out.Networks() {
		found = true

		assert.Equal(t, testlib.NetworkName(0), net.Name())
		assert.NotEmpty(t, net.Prefixes(), "the wire carries the subnet the round read")
	}

	assert.True(t, found, "the instance sits on the network the round knows about")
}

// TestSweepFillsTheProjectLabels is the same thing for the other half of what a
// name is built from. A round reads each project, so an instance action carries
// its project's own settings without any read of them.
func TestSweepFillsTheProjectLabels(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	fleet := testlib.NewProject("p", 1, 1)
	fleet.Project.Config[featuresNetworks] = "true"
	fleet.Project.Config["coredns.zone"] = "example.test"

	h.setFleet(fleet)

	// connected, the one instance the round trickled, the end of the round.
	got := h.collect(3)

	swept := got[1]
	require.True(t, swept.Enriched(iutil.EnrichedProject),
		"the round read the project and handed its instance over without the labels")
	assert.Equal(t, "example.test", must(swept.Label("coredns.zone")))

	// And an event arriving after it, which takes the other door.
	h.retag("moved")
	h.send(incusapi.EventLifecycleInstanceUpdated, "p", testlib.InstanceName(0))

	out := h.next()
	require.True(t, out.Enriched(iutil.EnrichedProject))
	assert.Equal(t, "example.test", must(out.Label("coredns.zone")))
}

// TestProjectLabelsWaitForTheProjectToBeRead pins the difference between "this
// project sets none" and "this project has not been read" - an empty map would
// make them one, and a consumer acting on the first would prune the second.
func TestProjectLabelsWaitForTheProjectToBeRead(t *testing.T) {
	t.Parallel()

	h := seeded(t, 0, 0)

	// No NIC, so this stays a test about the project rather than about whether
	// the wire its NIC would name has been read yet.
	h.mu.Lock()
	h.noNIC = true
	h.mu.Unlock()

	// The round read project "p", never "other" - so an instance event naming
	// it finds no project the same way one would before any round had run.
	h.send(incusapi.EventLifecycleInstanceUpdated, "other", testlib.InstanceName(0))

	out := h.next()

	assert.False(t, out.Enriched(iutil.EnrichedProject),
		"a project nothing has read was reported as read")
	assert.Equal(t, map[string]string{"testlib.service": testlib.InstanceName(0)}, out.Labels(),
		"the instance's own label came through; an unread project added nothing")
}

// must is the value of a two-value read the test has already required.
func must(value string, _ bool) string { return value }

// TestFailedReadIsReadAgain is the first half of the retry policy: the event
// fails fast so the line keeps moving, and only the failed key is read again.
func TestFailedReadIsReadAgain(t *testing.T) {
	t.Parallel()

	h := seeded(t, 1, 1)

	h.mu.Lock()
	h.err = errors.New("incusd said no")
	h.mu.Unlock()

	before := h.lists.Load()

	h.send(incusapi.EventLifecycleInstanceUpdated, "p", testlib.InstanceName(0))

	failed := h.next()
	require.Equal(t, iutil.StateFailed, failed.State(), "the event does not wait on a retry")

	require.Eventually(t, func() bool { return h.readsOf("p", testlib.InstanceName(0)) > 1 },
		5*time.Second, 10*time.Millisecond, "the key that failed was never read again")

	assert.Equal(t, before, h.lists.Load(), "one key that failed pulled a round in")
}

// TestUnleasedInstanceIsReadAgain: a lease lands after the start event, and a
// network-updated fan-out skips an instance holding no address on that wire -
// so nothing else would ever ask again without repairSoon.
func TestUnleasedInstanceIsReadAgain(t *testing.T) {
	t.Parallel()

	h := seeded(t, 1, 1)

	h.mu.Lock()
	h.leaseless = true
	h.mu.Unlock()

	before := h.lists.Load()

	h.send(incusapi.EventLifecycleInstanceUpdated, "p", testlib.InstanceName(0))

	served := h.next()
	require.Equal(t, iutil.StateOk, served.State(), "the read landed, it just found no address")

	require.Eventually(t, func() bool { return h.readsOf("p", testlib.InstanceName(0)) > 1 },
		5*time.Second, 10*time.Millisecond, "an instance with no address was never read again")

	assert.Equal(t, before, h.lists.Load(), "one address-less instance cost a whole-fleet pass")
}

// TestFanOutOverAnUnchangedFleetEmitsNothing is what the archive is for: a wire
// that moved is one event, not one per instance sitting on it, when what those
// instances look like has not changed.
func TestFanOutOverAnUnchangedFleetEmitsNothing(t *testing.T) {
	t.Parallel()

	h := seeded(t, 2, 1)

	// The round that seeded this filed both instances as they read now, so the
	// fan-out below reads the same thing about each of them.
	h.send(incusapi.EventLifecycleNetworkUpdated, "p", testlib.NetworkName(0))

	require.Equal(t, incusapi.EventLifecycleNetworkUpdated, h.next().Action())

	require.Eventually(t, func() bool {
		return h.readsOf("p", testlib.InstanceName(0)) == 2 &&
			h.readsOf("p", testlib.InstanceName(1)) == 2
	}, 5*time.Second, 10*time.Millisecond, "the fan-out never read them")

	assert.Empty(t, h.out, "a fleet that did not move emitted an event per instance")
}

// TestAnUnchangedInstanceEventGoesNoFurther pins the other half: volatile keys
// are stripped before the comparison, so a lease renewal is an instance-updated
// whose distilled value did not move.
func TestAnUnchangedInstanceEventGoesNoFurther(t *testing.T) {
	t.Parallel()

	h := seeded(t, 1, 1)

	h.retag("moved")

	h.send(incusapi.EventLifecycleInstanceUpdated, "p", testlib.InstanceName(0))
	require.Equal(t, iutil.StateOk, h.next().State())

	h.send(incusapi.EventLifecycleInstanceUpdated, "p", testlib.InstanceName(0))

	// Behind it, so this is what arrives next only if the one in front did not.
	// Asserting on an empty channel would pass before it could have arrived.
	h.send(incusapi.EventLifecycleInstanceUpdated, "p", "other")

	assert.Equal(t, "other", h.next().Name(),
		"an event saying what the last one said walked anyway")
}

// TestADeleteBehindAReadKeepsItsOrder pins the worst case for overtaking: a
// delete needs no read, so it settles at once while the update in front of it
// is still out. The delete also drops what the archive held, so the update is
// news again rather than compared against a value the delete has retired.
func TestADeleteBehindAReadKeepsItsOrder(t *testing.T) {
	t.Parallel()

	h := seeded(t, 1, 1)

	// The first read is what the second one below compares equal to.
	h.retag("moved")
	h.send(incusapi.EventLifecycleInstanceUpdated, "p", testlib.InstanceName(0))
	require.Equal(t, iutil.StateOk, h.next().State())

	h.mu.Lock()
	h.gate = make(chan struct{})
	gate := h.gate
	h.mu.Unlock()

	// Held on its read, and the delete behind it needs none.
	h.send(incusapi.EventLifecycleInstanceUpdated, "p", testlib.InstanceName(0))
	h.send(incusapi.EventLifecycleInstanceDeleted, "p", testlib.InstanceName(0))

	assert.Empty(t, h.out, "the delete went out while the event in front of it was still reading")

	close(gate)

	assert.Equal(t, incusapi.EventLifecycleInstanceUpdated, h.next().Action())
	assert.Equal(t, incusapi.EventLifecycleInstanceDeleted, h.next().Action())
}

// TestALiveEventAndAFanOutOnOneKeyAreOneEvent: both settle from the same read,
// so the real one walks and the invented one has nothing left to say.
func TestALiveEventAndAFanOutOnOneKeyAreOneEvent(t *testing.T) {
	t.Parallel()

	h := seeded(t, 1, 1)

	h.mu.Lock()
	h.gate = make(chan struct{})
	h.tag = "moved"
	gate := h.gate
	h.mu.Unlock()

	// A profile fans out without a read of its own, so the instance read it
	// issues is still in flight for the live event below to join.
	h.send(incusapi.EventLifecycleProfileUpdated, "p", "default")
	require.Equal(t, incusapi.EventLifecycleProfileUpdated, h.next().Action())

	h.send(incusapi.EventLifecycleInstanceUpdated, "p", testlib.InstanceName(0))

	close(gate)

	got := h.next()
	assert.Equal(t, incusapi.EventLifecycleInstanceUpdated, got.Action())
	assert.Equal(t, iutil.StateOk, got.State(), "the event somebody sent was the one dropped")

	require.Eventually(t, func() bool { return h.readsOf("p", testlib.InstanceName(0)) == 2 },
		5*time.Second, 10*time.Millisecond, "the two did not share one read")

	assert.Empty(t, h.out, "the fan-out emitted an event of its own beside the real one")
}

// TestFailedNetworkReadIsNotAnInstanceRead pins what a re-read cannot repair:
// the fan-out mints instance-updated, so a wire handed to it would have the
// daemon asked for an instance called net0. A failed wire read waits for the
// round instead.
func TestFailedNetworkReadIsNotAnInstanceRead(t *testing.T) {
	t.Parallel()

	h := seeded(t, 1, 1)

	h.mu.Lock()
	h.err = errors.New("incusd said no")
	h.mu.Unlock()

	// It walks settled and ahead of the read, so what the read did is not on it.
	h.send(incusapi.EventLifecycleNetworkUpdated, "p", testlib.NetworkName(0))

	require.Equal(t, incusapi.EventLifecycleNetworkUpdated, h.next().Action())

	assert.Never(t, func() bool { return h.readsOf("p", testlib.NetworkName(0)) > 0 },
		time.Second, 20*time.Millisecond, "the wire's own name was read as an instance")
}

// TestARoundMeetsAnInstanceMidBoot pins the one absorb point: a round reaches
// the same conclusion about a read as the event path does, so a running
// instance holding no address gets repaired either way it was found.
func TestARoundMeetsAnInstanceMidBoot(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	fleet := testlib.NewProject("p", 1, 1)
	fleet.Project.Config[featuresNetworks] = "true"

	// A running instance holding no address, which is what a state read that
	// failed leaves behind and what a lease that has not landed looks like.
	h.mu.Lock()
	h.leaseless = true
	h.mu.Unlock()

	h.setFleet(fleet)

	// connected, the instance the round trickled, the end of the round.
	h.collect(3)

	before := h.readsOf("p", testlib.InstanceName(0))

	require.Eventually(t, func() bool { return h.readsOf("p", testlib.InstanceName(0)) > before },
		5*time.Second, 10*time.Millisecond, "a round met an instance mid-boot and never read it again")
}

// TestRepairSlowsDownButNeverStops is the other half, and the one the e2e
// caught: a key that spends its fast attempts drops to the slow rate rather
// than stopping and waiting for the round, which is minutes away by design.
func TestRepairSlowsDownButNeverStops(t *testing.T) {
	t.Parallel()

	h := seeded(t, 1, 1)

	before := h.readsOf("p", testlib.InstanceName(0))

	h.mu.Lock()
	h.err = errors.New("incusd said no")
	h.mu.Unlock()

	h.send(incusapi.EventLifecycleInstanceUpdated, "p", testlib.InstanceName(0))

	require.Equal(t, iutil.StateFailed, h.next().State())

	// The read the event asked for, and no more re-reads than it was given.
	want := before + 1 + repairTries

	require.Eventually(t, func() bool {
		return h.readsOf("p", testlib.InstanceName(0)) == want
	}, 5*time.Second, 10*time.Millisecond, "the key spent a different number of attempts")

	assert.Never(t, func() bool { return h.readsOf("p", testlib.InstanceName(0)) > want },
		2*repairDelay, 20*time.Millisecond, "the fast attempts did not slow down")

	require.Eventually(t, func() bool { return h.readsOf("p", testlib.InstanceName(0)) > want },
		3*slowRepairDelay, 50*time.Millisecond, "a key that spent its attempts was never read again")
}

// TestAReconnectRestartsTheRound: everything held is as old as the stream was
// down, so the round in flight is abandoned wherever it had got to and a fast
// one starts.
func TestAReconnectRestartsTheRound(t *testing.T) {
	t.Parallel()

	h := seeded(t, 1, 1)

	before := h.lists.Load()

	h.send(iutil.ActionConnected, "", "")

	require.Equal(t, iutil.ActionConnected, h.next().Action())

	require.Eventually(t, func() bool { return h.lists.Load() > before },
		5*time.Second, 10*time.Millisecond, "a reconnect started no round")

	// The instance is unchanged, so it emits nothing; the end of the round is
	// what says a whole one ran.
	assert.Equal(t, iutil.ActionSweepEnd, h.next().Action())
}

// TestGoneIsNotAFailure: an instance read after it went is the ordinary race
// between an event and the delete that overtook it, not something to repair.
func TestGoneIsNotAFailure(t *testing.T) {
	t.Parallel()

	h := seeded(t, 0, 0)

	h.mu.Lock()
	h.err = incusapi.StatusErrorf(http.StatusNotFound, "instance not found")
	h.mu.Unlock()

	h.send(incusapi.EventLifecycleInstanceUpdated, "p", "one")

	ev := h.next()

	assert.Equal(t, iutil.StateOk, ev.State(), "nothing failed; it is simply not there")
	assert.False(t, ev.Enriched(iutil.EnrichedInstance), "and nothing pretends to have landed")

	// Nothing is owed: re-reading a gone instance repairs nothing.
	assert.Never(t, func() bool { return h.readsOf("p", "one") > 1 },
		2*repairDelay, 20*time.Millisecond, "a gone instance was read again")
}

// TestSweepReadsProjectLabels: the project's own settings come off the project,
// which is what `incus project set` writes.
func TestSweepReadsProjectLabels(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	fleet := testlib.NewProject("p", 1, 1)
	testlib.Label(fleet.Project.Config, "zone", "internal")

	fleet.Project.Config[featuresNetworks] = "true"

	h.setFleet(fleet)
	h.collect(3)

	h.stop()

	zone, ok := h.p.m.projects["p"][testlib.LabelPrefix+"zone"]
	require.True(t, ok, "the round patched the project in")
	assert.Equal(t, "internal", zone)
}

// TestARoundTakesTheProjectNotItsProfile: the default profile is the wrong
// place to look even though a project always has one - its keys are already in
// every instance's expanded configuration, so reading them as the project's
// leaves `incus project set user.label.coredns.zone=...` with nowhere to land.
func TestARoundTakesTheProjectNotItsProfile(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	fleet := testlib.NewProject("shop", 0, 0)
	fleet.Project.Config[testlib.LabelPrefix+"zone"] = "my.zone.com"
	fleet.Project.Config[featuresNetworks] = "true"

	h.setFleet(fleet)

	// connected, then the end of the round: no instances to trickle.
	h.collect(2)

	h.stop()

	got := h.p.m.projects["shop"]

	assert.Equal(t, "my.zone.com", got[testlib.LabelPrefix+"zone"])

	// Handed over whole, ours and everybody else's. Picking out what a key means
	// is the consumer's, which is what lets one read answer coredns and operator.
	assert.Equal(t, "true", got[featuresNetworks])
}

// seeded runs one round so the model holds a fleet, and returns the harness.
// The project owns its networks, or a round would only read the default
// project's and the fleet's own wires would be invisible.
func seeded(t *testing.T, instances, networks int) *harness {
	t.Helper()

	h := newHarness(t)

	fleet := testlib.NewProject("p", instances, max(networks, 1))
	fleet.Project.Config[featuresNetworks] = "true"

	h.setFleet(fleet)

	// connected, then one event per instance, then the end of the round.
	h.collect(2 + instances)

	return h
}

// TestNetworkUpdatePatchesAndFansOut: a wire is iutil, so a subnet moving
// changes every record on it. The event says so and the re-reads follow.
func TestNetworkUpdatePatchesAndFansOut(t *testing.T) {
	t.Parallel()

	h := seeded(t, 2, 1)

	before := h.readsOf("p", testlib.InstanceName(0))

	// Tagged, so what the re-reads find is worth an event of its own.
	h.retag("moved")
	h.send(incusapi.EventLifecycleNetworkUpdated, "p", testlib.NetworkName(0))

	// The change first, then what it caused.
	assert.Equal(t, incusapi.EventLifecycleNetworkUpdated, h.next().Action())

	fanned := []string{h.next().Name(), h.next().Name()}
	assert.ElementsMatch(t,
		[]string{testlib.InstanceName(0), testlib.InstanceName(1)}, fanned,
		"everything on the wire is re-read")

	assert.Greater(t, h.readsOf("p", testlib.InstanceName(0)), before,
		"and re-read means read, not guessed")
}

// TestNetworkDeleteForgetsTheWire: the wire goes, so nothing is left resolving
// against a key that describes nothing. Nothing is on it, because Incus
// refuses to delete a managed network anything is attached to.
func TestNetworkDeleteForgetsTheWire(t *testing.T) {
	t.Parallel()

	h := seeded(t, 0, 1)

	h.send(incusapi.EventLifecycleNetworkDeleted, "p", testlib.NetworkName(0))

	assert.Equal(t, incusapi.EventLifecycleNetworkDeleted, h.next().Action())

	// The model belongs to Run's goroutine, so it is only safe to look at once
	// Run has returned.
	h.stop()

	assert.NotContains(t, h.p.m.wires, key("p", testlib.NetworkName(0)), "the wire is gone")
}

// TestNetworkRenameDropsTheOldKey: a rename leaves the old key behind whatever
// else happens - the wire is still there, but not under the name its addresses
// were filed under.
func TestNetworkRenameDropsTheOldKey(t *testing.T) {
	t.Parallel()

	h := seeded(t, 1, 1)

	ev := iutil.NewEvent(time.Now(), incusapi.EventLifecycleNetworkRenamed,
		"p", "renamed", testlib.NetworkName(0))
	h.p.Handle(ev)

	assert.Equal(t, incusapi.EventLifecycleNetworkRenamed, h.next().Action())

	require.Eventually(t, func() bool { return h.netReadsOf("p", "renamed") > 0 },
		2*time.Second, time.Millisecond, "the new name is read")

	h.stop()

	assert.NotContains(t, h.p.m.wires, key("p", testlib.NetworkName(0)),
		"and the old key does not survive it")
}

// TestProfileUpdateFansOut: a profile re-expands the configuration of every
// instance using it, and the event names none of them.
func TestProfileUpdateFansOut(t *testing.T) {
	t.Parallel()

	h := seeded(t, 3, 1)

	before := h.lists.Load()

	// Tagged, so what the re-reads find is worth an event of its own.
	h.retag("moved")
	h.send(incusapi.EventLifecycleProfileUpdated, "p", "default")

	assert.Equal(t, incusapi.EventLifecycleProfileUpdated, h.next().Action())

	fanned := []string{h.next().Name(), h.next().Name(), h.next().Name()}
	assert.ElementsMatch(t, []string{
		testlib.InstanceName(0), testlib.InstanceName(1), testlib.InstanceName(2),
	}, fanned, "every instance in the project, and the project was not read to find them")

	assert.Equal(t, before, h.lists.Load(), "no round was needed for that")
}

// TestProfileDeleteCostsNoRead: Incus refuses to delete a profile anything is
// expanded from, so the event names one no instance used. Fanning out over the
// project would be a read per instance to learn nothing.
func TestProfileDeleteCostsNoRead(t *testing.T) {
	t.Parallel()

	h := seeded(t, 2, 1)

	before := h.readsOf("p", testlib.InstanceName(0))

	h.retag("moved")
	h.send(incusapi.EventLifecycleProfileDeleted, "p", "gone")

	assert.Equal(t, incusapi.EventLifecycleProfileDeleted, h.next().Action())

	assert.Never(t, func() bool { return h.readsOf("p", testlib.InstanceName(0)) > before },
		time.Second, 20*time.Millisecond, "a profile nothing used re-read the project")
}

// TestProfileUpdateInAnEmptyProjectCostsNothing: nothing to re-expand, nothing
// to read.
func TestProfileUpdateInAnEmptyProjectCostsNothing(t *testing.T) {
	t.Parallel()

	h := seeded(t, 1, 1)

	h.send(incusapi.EventLifecycleProfileUpdated, "elsewhere", "default")

	assert.Equal(t, incusapi.EventLifecycleProfileUpdated, h.next().Action())
	assert.Zero(t, h.readsOf("elsewhere", testlib.InstanceName(0)))
}

// TestARoundPrunesWhatItAskedAbout: absence is decided against what was held
// when the listing went out, so a name created while the request was in flight
// survives - and has to, since nothing else would ever put it back.
func TestARoundPrunesWhatItAskedAbout(t *testing.T) {
	t.Parallel()

	p := New()
	p.m = newModel()
	p.archive = map[string]*iutil.Event{}

	p.m.putWire(testlib.NewNetwork("p", 0))

	hold := func(name string) {
		inst := testlib.NewInstance("p", 0, 0)
		inst.Name = name

		require.NotNil(t, p.m.putInstance(&inst, testlib.NewInstanceState(0, 0)))
	}

	hold("keep")
	hold("gone")

	ctx := t.Context()

	p.absorbSweep(ctx, sweepMsg{kind: sweepAsking, about: sweepInstances, project: "p"})

	// Created while the listing was in flight, so the answer below cannot know
	// about it.
	hold("newcomer")

	p.absorbSweep(ctx, sweepMsg{kind: sweepInstances, project: "p", names: []string{"keep"}})

	assert.NotNil(t, p.m.instance("p", "keep"), "a listed name was pruned")
	assert.Nil(t, p.m.instance("p", "gone"), "a name the listing no longer has stayed in the model")
	assert.NotNil(t, p.m.instance("p", "newcomer"),
		"a name created while the listing was in flight was pruned")
}

// TestARoundPrunesAProjectItNoLongerServes: a project leaving the served set
// takes its instances with it, or a fan-out keeps re-reading names in a project
// this binary has been told to ignore.
func TestARoundPrunesAProjectItNoLongerServes(t *testing.T) {
	t.Parallel()

	p := New()
	p.m = newModel()
	p.archive = map[string]*iutil.Event{}

	p.m.putProject("shop", map[string]string{})
	p.m.putProject("blog", map[string]string{})
	p.m.putWire(testlib.NewNetwork("blog", 0))

	inst := testlib.NewInstance("blog", 0, 0)
	require.NotNil(t, p.m.putInstance(&inst, testlib.NewInstanceState(0, 0)))

	ctx := t.Context()

	p.absorbSweep(ctx, sweepMsg{kind: sweepAsking, about: sweepProjects})
	p.absorbSweep(ctx, sweepMsg{kind: sweepProjects, names: []string{"shop"}})

	assert.Contains(t, p.m.projects, "shop")
	assert.NotContains(t, p.m.projects, "blog")
	assert.Nil(t, p.m.instance("blog", testlib.InstanceName(0)),
		"the project went and its instances stayed behind")
}

// TestARoundLeavesTheModelAloneWhenTheListingFailed: an empty answer and a
// daemon that would not answer arrive the same way, and one of them means every
// name in the project.
func TestARoundLeavesTheModelAloneWhenTheListingFailed(t *testing.T) {
	t.Parallel()

	h := seeded(t, 1, 1)

	h.mu.Lock()
	h.listErr = errors.New("incusd said no")
	h.mu.Unlock()

	h.send(iutil.ActionConnected, "", "")
	require.Equal(t, iutil.ActionConnected, h.next().Action())

	assert.Never(t, func() bool { return len(h.out) > 0 },
		time.Second, 20*time.Millisecond, "a round that read nothing announced something")

	h.stop()

	assert.NotNil(t, h.p.m.instance("p", testlib.InstanceName(0)),
		"a listing that failed pruned the fleet it could not read")
}

// TestRoundsAreNotBackToBack: every round after the first is paced, or a fleet
// small enough to read in no time would be read again immediately, for ever.
func TestRoundsAreNotBackToBack(t *testing.T) {
	t.Parallel()

	h := seeded(t, 1, 1)

	before := h.lists.Load()

	assert.Never(t, func() bool { return h.lists.Load() > before },
		time.Second, 20*time.Millisecond, "a second round started with no delay at all")
}

// TestWantsWhatItActsOnItself: it asks for no enrichment of its own, since it
// is the plugin that performs everybody else's.
func TestWantsWhatItActsOnItself(t *testing.T) {
	t.Parallel()

	got := New().Wants()

	require.Len(t, got, 2)

	for _, want := range got {
		assert.Zero(t, want.Enrich, "the enricher asked to be enriched")
	}

	assert.Equal(t, incusapi.EventLifecycleProfileUpdated, got[0].Action)
	assert.True(t, got[0].Debounce, "every update in a burst costs a read per instance")

	assert.Equal(t, incusapi.EventLifecycleProfileDeleted, got[1].Action)
	assert.False(t, got[1].Debounce, "a delete collapsed into a burst is a delete lost")
}
