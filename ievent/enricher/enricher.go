// Package enricher reads what an event's subject looks like now, and fills the
// rest of the event in from what it already holds. See
// docs/root/architecture/ievent/enricher.md for the design.
package enricher

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	incusapi "github.com/lxc/incus/v7/shared/api"
	"github.com/panjf2000/ants/v2"

	"github.com/lxc/incus-compose/iclient"
	"github.com/lxc/incus-compose/ievent/iutil"
)

// name is what this plugin is called, in the chain and in failure reasons.
const name = "enricher"

// Defaults, used for whatever main left unset.
const (
	defaultWorkers = 16

	defaultReadTimeout = 10 * time.Second

	// defaultProjectDelay is the gap between one project of a round and the
	// next, so a fleet is not read project after project at full rate.
	defaultProjectDelay = 30 * time.Second

	// defaultReadDelay is the gap between the reads inside one project.
	defaultReadDelay = 5 * time.Second
)

// repairDelay/repairTries/slowRepairDelay: how soon and how often a failed key
// is read again, and the slower rate it drops to once repairTries is spent.
const (
	repairDelay     = time.Second
	repairTries     = 3
	slowRepairDelay = 5 * time.Second
)

// defaultInboxSize absorbs a burst before Handle has to drop; matched to the
// Incus client's own event channel size.
const defaultInboxSize = 1024

// instancePrefix is how an event that names an instance is told from one that
// names a network with the same bare name.
const instancePrefix = "instance-"

// profilePrefix is what an action names a profile with.
const profilePrefix = "profile-"

// retryDelay is how long a submit the nonblocking pool refused waits before
// being offered again; nothing is failed for the pool being busy.
const retryDelay = 20 * time.Millisecond

// Plugin fills events in from what it holds, reading only the subject of each.
//
// Everything below the inbox belongs to the goroutine Run owns; a pool worker
// touches nothing here, which is what keeps this package free of a mutex.
type Plugin struct {
	opts options

	// wanted is the source's finished table of what each action needs read for;
	// an action absent from it needs nothing done to it.
	wanted map[string]iutil.Want

	next  iutil.Next
	inbox chan *iutil.Event

	// read, readNet and fleet are fields rather than free functions, so a test
	// can supply Incus values without a daemon.
	conn    *iclient.Connection
	read    readFunc
	readNet netReadFunc
	fleet   fleet

	// out puts a command at the head of the chain, reaching plugins in front as
	// well as behind.
	out chan<- iutil.Command

	// in is the source asking this plugin to finish, on a channel of its own so
	// it arrives whatever the inbox looks like.
	in <-chan iutil.Command

	results chan result
	sweeps  chan sweepMsg

	// restart tells the sweeper to abandon its round and start a fast one; one
	// slot, since a restart already pending says the same thing.
	restart chan struct{}

	// Everything below belongs to the goroutine Run owns, and is set up there.
	pool *ants.Pool
	q    *queue
	m    *model

	// flights is the read in flight for each key; a second event on a key joins
	// it rather than issuing another.
	flights map[string]*flight

	// warm says the first whole-fleet pass has landed, so every network an
	// instance might sit on is known. Never cleared once set.
	warm bool

	// cold is every instance key an event arrived for before warm, in arrival
	// order; issued for real once the first pass lands.
	cold []string

	// waiting is what the pool refused, oldest first, and retry is when to
	// offer them again.
	waiting []*flight
	retry   *time.Timer

	// repairs is every key due another read, oldest first; reread fires when the
	// head falls due.
	repairs []repair
	reread  *time.Timer

	// tries is what each key has spent, so one that never settles ends up with
	// the pass rather than being read for ever.
	tries map[string]int

	// archive is the last event about each subject, keyed by kind, project and
	// name.
	archive map[string]*iutil.Event

	// asked is what the model held in one scope when that scope's listing went
	// out; absence is decided against this, not against the model as it stands.
	asked []string

	// checked is every project a round has already been started for, so an event
	// naming one nothing has read costs one restart rather than one per event.
	checked map[string]bool

	// chain is what the last event said, and what this plugin stamps on the ones
	// it mints: those never pass the source, so nothing else would.
	chain iutil.ChainState
}

// options is what main decides about this plugin, kept separate from a shared
// iutil options type since nothing here has a debounce window.
type options struct {
	Workers      int
	ReadTimeout  time.Duration
	InboxSize    int
	ProjectDelay time.Duration
	ReadDelay    time.Duration
	Project      func(p *incusapi.Project) bool
}

// Option sets one of the options; New fills in defaults for whatever is left
// zero.
type Option func(*options)

// Workers caps the Incus reads this plugin may have in flight. One endpoint
// fronts a whole cluster, so this bounds load on somebody else's machine.
func Workers(n int) Option { return func(o *options) { o.Workers = n } }

// ReadTimeout bounds one read of the daemon.
func ReadTimeout(d time.Duration) Option { return func(o *options) { o.ReadTimeout = d } }

// InboxSize sets how many events this plugin buffers before it has to drop.
func InboxSize(n int) Option { return func(o *options) { o.InboxSize = n } }

// ProjectDelay sets the gap between one project of a round and the next.
func ProjectDelay(d time.Duration) Option { return func(o *options) { o.ProjectDelay = d } }

// ReadDelay sets the gap between the reads inside one project.
func ReadDelay(d time.Duration) Option { return func(o *options) { o.ReadDelay = d } }

// Project sets which projects the binary serves. Nil serves every one the
// certificate can see, which is the standalone default:
//
//	enricher.Project(func(p *incusapi.Project) bool {
//		return p.Config["user.coredns"] == "true"
//	})
func Project(fn func(p *incusapi.Project) bool) Option {
	return func(o *options) { o.Project = fn }
}

// New builds an enricher.
//
// ReadTimeout starts when a worker picks a read up, not when it is offered to
// the pool, so time spent waiting for a worker is never charged to the daemon.
func New(opts ...Option) *Plugin {
	o := options{
		Workers:      defaultWorkers,
		ReadTimeout:  defaultReadTimeout,
		InboxSize:    defaultInboxSize,
		ProjectDelay: defaultProjectDelay,
		ReadDelay:    defaultReadDelay,
	}

	for _, opt := range opts {
		opt(&o)
	}

	slog.Info("Starting", "plugin", name, "config", o)

	return &Plugin{
		opts:  o,
		inbox: make(chan *iutil.Event, o.InboxSize),
		// Buffered to the worker count, so a worker never blocks handing back.
		results: make(chan result, o.Workers),
		// Unbuffered, so the sweeper's pace is felt rather than run ahead of.
		sweeps:  make(chan sweepMsg),
		restart: make(chan struct{}, 1),
	}
}

// Name identifies the plugin, and names it in the reason of what it fails.
func (p *Plugin) Name() string { return name }

// Wants the actions it acts on itself, and no read for any of them: it must
// read SetupArgs.Wanted instead, or it would double-count everyone else's.
func (p *Plugin) Wants() []iutil.Want {
	return []iutil.Want{
		// Collapsible: a profile carries no history an update could lose.
		{Action: incusapi.EventLifecycleProfileUpdated, Debounce: true},

		// Not collapsed: a delete lost in a burst is a delete lost.
		{Action: incusapi.EventLifecycleProfileDeleted},
	}
}

// Setup keeps the successor, the table and the connection, and starts nothing.
func (p *Plugin) Setup(args iutil.SetupArgs) error {
	p.next = args.Next
	p.wanted = args.Wanted

	p.in, p.out = args.CommandIn, args.CommandOut

	if p.read == nil {
		p.read = incusReader(args.Conn)
	}

	if p.readNet == nil {
		p.readNet = incusNetReader(args.Conn)
	}

	p.conn = args.Conn

	return nil
}

// Handle puts the event on the inbox and returns.
//
// It runs on the previous plugin's goroutine and must not block, so a full
// inbox drops rather than waits; dropped, not failed, since nothing went wrong.
func (p *Plugin) Handle(ev *iutil.Event) {
	select {
	case p.inbox <- ev:
	default:
		p.next(ev.WithDropped(name))
	}
}

// Run enriches until ctx is canceled. It blocks, so main owns the goroutine:
//
//	wg.Go(func() error { return enr.Run(ctx) })
//
// Reads in flight are abandoned rather than waited for; the events themselves
// still go.
func (p *Plugin) Run(ctx context.Context) error {
	pool, err := ants.NewPool(p.opts.Workers, ants.WithNonblocking(true))
	if err != nil {
		return fmt.Errorf("creating the read pool: %w", err)
	}

	defer pool.Release()

	p.pool = pool

	if p.fleet == nil {
		p.fleet = incusFleet{conn: p.conn}
	}

	p.q = &queue{}
	p.m = newModel()
	p.flights = map[string]*flight{}
	p.warm = false
	p.cold = nil
	p.tries = map[string]int{}
	p.archive = map[string]*iutil.Event{}
	p.checked = map[string]bool{}
	p.retry = time.NewTimer(retryDelay)
	p.reread = time.NewTimer(repairDelay)

	p.retry.Stop()
	p.reread.Stop()

	defer p.retry.Stop()
	defer p.reread.Stop()

	// Its own context, so the round stops when Run returns, not only when the
	// process does.
	sweepCtx, stopSweeping := context.WithCancel(ctx)

	var sweeping sync.WaitGroup

	defer sweeping.Wait()
	defer stopSweeping()

	sweeping.Go(func() { p.sweep(sweepCtx) })

	for {
		select {
		case <-ctx.Done():
			// An abort, not a shutdown: reads in flight are abandoned.
			return nil

		case cmd := <-p.in:
			// Everything already on the inbox; nothing feeds it further once the
			// source has stopped.
		drained:
			for {
				select {
				case ev := <-p.inbox:
					p.accept(ctx, ev)
				default:
					break drained
				}
			}

			// Then the whole line, settled or not, in arrival order. Reads
			// still in flight are abandoned rather than waited for.
			for ev := range p.q.drain() {
				p.next(ev)
			}

			// Answered last, so the plugin behind is asked only once everything
			// this one held has been pushed into it.
			select {
			case p.out <- cmd:
			case <-ctx.Done():
			}

			return nil

		case ev := <-p.inbox:
			p.accept(ctx, ev)

			for ev := range p.q.release() {
				p.next(ev)
			}

		case msg := <-p.sweeps:
			p.absorbSweep(ctx, msg)

		case res := <-p.results:
			// The patch happens here, not in the worker: the model belongs to
			// this goroutine.
			f := res.flight
			delete(p.flights, f.key)

			var (
				e      *instance
				landed = true
			)

			switch {
			case !f.network:
				e, landed = p.absorb(f.project, f.name, res.instance, res.state, res.err)

			case res.err == nil:
				// The wire first, so the re-reads resolve against it, not what
				// it replaced.
				p.m.putWire(*res.wire)
				p.fanOut(ctx, p.m.instancesOn(f.key[2:]))
			}

			// Before the synthetic below, which is then compared against what
			// they filed.
			for _, it := range f.items {
				if !landed {
					// Not filed: the next read compares against the last answer
					// there was, not one that did not land.
					p.q.settle(it, it.ev.WithFailed("source/read"))

					continue
				}

				ev := it.ev
				if e != nil {
					ev = ev.WithInstance(e.running, e.config, e.nets)
				}

				if !p.news(ev) {
					p.q.trash(it)

					continue
				}

				p.q.settle(it, ev)
			}

			// Through withProject like every other instance event emitted here,
			// or it is not the same event a live one on this key is.
			if f.synthetic != nil && e != nil {
				syn := p.withProject(f.synthetic.WithInstance(e.running, e.config, e.nets))
				if p.news(syn) {
					p.q.push(syn, true)
				}
			}

			for ev := range p.q.release() {
				p.next(ev)
			}

		case <-p.reread.C:
			// Through the fan-out: a re-read has to mint an event of its own,
			// or what it finds reaches nobody.
			now := time.Now()

			for len(p.repairs) > 0 && !p.repairs[0].at.After(now) {
				p.fanOut(ctx, []subject{p.repairs[0].subject})
				p.repairs = p.repairs[1:]
			}

			if len(p.repairs) > 0 {
				p.reread.Reset(time.Until(p.repairs[0].at))
			}

		case <-p.retry.C:
			// Offered again in refusal order; the first refusal stops the
			// round, since the pool is still full.
			for len(p.waiting) > 0 {
				err := p.submit(ctx, p.waiting[0])
				if err != nil {
					break
				}

				p.waiting = p.waiting[1:]
			}

			if len(p.waiting) > 0 {
				p.retry.Reset(retryDelay)
			}
		}
	}
}

// raise tells the chain something, at the head rather than through next, so
// debounce in front of this plugin sees it too.
func (p *Plugin) raise(ctx context.Context, action string, chain iutil.ChainState) {
	select {
	case p.out <- iutil.Command{Action: action, ChainState: chain}:
	case <-ctx.Done():
	}
}

// withProject attaches the project's own labels out of the model, not a read of
// its own, where somebody asked for them.
//
// A project the pass has not reached is left unenriched rather than enriched
// with nothing, so a consumer can tell "not set" from "not read yet".
func (p *Plugin) withProject(ev *iutil.Event) *iutil.Event {
	if p.wanted[ev.Action()].Enrich&iutil.EnrichedProject == 0 {
		return ev
	}

	config, known := p.m.projects[ev.Project()]
	if !known {
		p.projectUnknown(ev.Project())

		return ev
	}

	return ev.WithProject(config)
}

// projectUnknown starts the round again, once, for a project nothing has read.
//
// The archive lets the re-emitted instances through as news, because the
// labels attached are part of what makes an event news.
func (p *Plugin) projectUnknown(project string) {
	if project == "" || p.checked[project] {
		return
	}

	p.checked[project] = true

	p.restartSweep()
}

// accept puts one event in the line, issuing whatever read it needs first.
func (p *Plugin) accept(ctx context.Context, ev *iutil.Event) {
	if ev.ChainState() != "" {
		p.chain = ev.ChainState()
	}

	// A stream coming back makes everything held here suspect at once: whatever
	// happened while it was down was announced to nobody.
	if ev.Action() == iutil.ActionConnected {
		p.q.push(ev, true)
		p.restartSweep()

		return
	}

	want := p.wanted[ev.Action()]

	// Before every push below, since the labels are the model's and owe nothing
	// to the read this event may or may not go on to need.
	ev = p.withProject(ev)

	// Already finished, or no name to read: neither is worth a read, and both
	// keep their place rather than overtaking what is still waiting.
	if ev.State() != iutil.StateOk || ev.Name() == "" {
		p.q.push(ev, true)

		return
	}

	// A wire moving changes every record on it, so that path patches and fans
	// out rather than enriching the event in front of it.
	if p.acceptNetwork(ctx, ev) {
		return
	}

	// A profile re-expands every instance using it, so this fans out over the
	// model. Except a delete, which Incus refuses while anything still uses it.
	if strings.HasPrefix(ev.Action(), profilePrefix) {
		p.q.push(ev, true)

		if ev.Action() != incusapi.EventLifecycleProfileDeleted {
			p.fanOut(ctx, p.m.instancesIn(ev.Project()))
		}

		return
	}

	// A delete is complete as it stands; reading to confirm it would answer a
	// question we already have the answer to.
	if ev.Action() == incusapi.EventLifecycleInstanceDeleted {
		p.m.dropInstance(ev.Project(), ev.Name())
		delete(p.archive, archiveKey(instancePrefix, ev.Project(), ev.Name()))
		p.q.push(ev, true)

		return
	}

	// A rename takes the old name out. The new one is read like any other
	// change, which is what the event that follows this line does.
	if ev.Action() == incusapi.EventLifecycleInstanceRenamed && ev.OldName() != "" {
		p.m.dropInstance(ev.Project(), ev.OldName())
		delete(p.archive, archiveKey(instancePrefix, ev.Project(), ev.OldName()))
	}

	// Both have to be true: wanted for instance enrichment, and the action
	// actually names an instance (a network action can be wanted too).
	instance := strings.HasPrefix(ev.Action(), instancePrefix) &&
		want.Enrich&iutil.EnrichedInstance != 0

	if !instance {
		p.q.push(ev, true)

		return
	}

	it := p.q.push(ev, false)

	p.issueOrHold(ctx, &flight{
		key:     flightKey(false, ev.Project(), ev.Name()),
		project: ev.Project(),
		name:    ev.Name(),
		items:   []*item{it},
	})
}

// issueOrHold sends one instance read, unless the first whole-fleet pass has
// not landed yet, in which case it joins flights but waits for thaw to send it.
func (p *Plugin) issueOrHold(ctx context.Context, f *flight) {
	if p.warm {
		p.issue(ctx, f)

		return
	}

	out, holding := p.flights[f.key]
	if holding {
		out.join(f)

		return
	}

	p.flights[f.key] = f
	p.cold = append(p.cold, f.key)
}

// thaw sends every read cold held back, in arrival order. Called once, from
// the first pass to land, once wires answers for every network an instance
// could sit on.
func (p *Plugin) thaw(ctx context.Context) {
	keys := p.cold
	p.cold = nil
	p.warm = true

	for _, key := range keys {
		f := p.flights[key]
		delete(p.flights, key)

		p.issue(ctx, f)
	}
}

// issue sends one read, or joins the one already out for that key: coalescing
// saves the read, not the event.
func (p *Plugin) issue(ctx context.Context, f *flight) {
	out, running := p.flights[f.key]
	if running {
		out.join(f)

		return
	}

	p.flights[f.key] = f

	err := p.submit(ctx, f)
	if err != nil {
		// Refused, not failed: it keeps its place and is offered again shortly.
		p.waiting = append(p.waiting, f)
		p.retry.Reset(retryDelay)
	}
}

// fanOut re-reads a set of instances nothing named directly, each as a
// synthetic instance-updated held on its flight, going on only if the read
// found something new.
func (p *Plugin) fanOut(ctx context.Context, subjects []subject) {
	for _, s := range subjects {
		p.issue(ctx, &flight{
			key:     flightKey(false, s.project, s.name),
			project: s.project,
			name:    s.name,
			synthetic: iutil.NewEvent(time.Now(),
				incusapi.EventLifecycleInstanceUpdated, s.project, s.name, "").
				WithChainState(p.chain),
		})
	}
}

// archiveKey is what the last event about one subject is filed under: the kind
// its action names, and the subject's project and name.
func archiveKey(action, project, name string) string {
	kind, _, found := strings.Cut(action, "-")
	if !found {
		return ""
	}

	return kind + " " + key(project, name)
}

// news reports whether ev says anything the last event about its subject did
// not, and files it either way. An action naming no kind is always news.
func (p *Plugin) news(ev *iutil.Event) bool {
	k := archiveKey(ev.Action(), ev.Project(), ev.Name())
	if k == "" {
		return true
	}

	was := p.archive[k]
	p.archive[k] = ev

	return !ev.Equal(was)
}

// absorb patches the model from one instance read and decides what that key is
// owed - the only place either happens.
//
// The second return says whether the read landed; false fails the events
// waiting on it, the same as a read that errored.
func (p *Plugin) absorb(
	project, name string,
	inst *incusapi.Instance,
	state *incusapi.InstanceState,
	err error,
) (*instance, bool) {
	key := flightKey(false, project, name)

	switch {
	case err == nil:

	case incusapi.StatusErrorCheck(err, http.StatusNotFound):
		// Read after it went; the delete is on its way with the same news.
		p.m.dropInstance(project, name)
		delete(p.tries, key)
		delete(p.archive, archiveKey(instancePrefix, project, name))

		return nil, true

	default:
		p.repairSoon(project, name)

		return nil, false
	}

	e := p.m.putInstance(inst, state)

	// A NIC on a network nothing has read yet: nothing was stored, and nothing
	// would correct it since the wire's own fan-out skips it.
	if e == nil {
		p.repairSoon(project, name)

		return nil, false
	}

	// No lease yet, or a state read that could not be made.
	if e.running && !e.addressed() {
		p.repairSoon(project, name)

		return e, true
	}

	delete(p.tries, key)

	return e, true
}

// repairSoon schedules another read of one key, never inline (the events
// queued behind it would wait it out), slowing to slowRepairDelay rather than
// stopping once it spends its fast attempts.
func (p *Plugin) repairSoon(project, name string) {
	key := flightKey(false, project, name)

	delay := repairDelay
	if p.tries[key] >= repairTries {
		delay = slowRepairDelay
	}

	p.tries[key]++

	at := time.Now().Add(delay)

	// Ordered by when, not appended: the timer is armed for the head, and a
	// fast repair behind a slow one would otherwise wait it out.
	i := len(p.repairs)
	for i > 0 && p.repairs[i-1].at.After(at) {
		i--
	}

	p.repairs = slices.Insert(p.repairs, i, repair{
		subject: subject{project: project, name: name},
		at:      at,
	})

	if i == 0 {
		p.reread.Reset(delay)
	}
}
