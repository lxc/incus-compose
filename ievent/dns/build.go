package dns

import (
	"hash/fnv"
	"maps"
	"net/netip"
	"slices"
	"sort"

	incusutil "github.com/lxc/incus/v7/shared/util"
	"github.com/miekg/dns"

	"github.com/lxc/incus-compose/ievent/dns/ecs_view"
	"github.com/lxc/incus-compose/ievent/iutil"
)

// instance is one instance as this plugin holds it: everything a record needs
// out of the event the enricher handed over, and nothing else.
type instance struct {
	zone string
	meta map[string]string

	// project is the project's own labels as they were last actually read, kept
	// apart from meta because most actions arrive without them. See patchInstance.
	project map[string]string

	// transfer says the project opted its zone into zone transfer, fed from
	// project alone so setting it on an instance cannot expose its siblings.
	transfer bool

	// ns is the project's own NS names for this zone, absolute, fed from project
	// alone for the same reason transfer is: a name server is the zone's, not
	// one instance's to claim.
	ns []string

	// nets is every network this instance sits on, keyed by Network.Key. The
	// value carries the wire and this instance's addresses on it together.
	nets map[string]*iutil.Network
}

// patchInstance turns one enriched event into what is held, or nil if no
// networks were read. prev's project labels carry over when this read omitted them.
func patchInstance(ev *iutil.Event, prev *instance, suffix string) *instance {
	if !ev.Enriched(iutil.EnrichedNetworks) {
		return nil
	}

	nets := maps.Collect(ev.Networks())

	meta := instanceLabels(ev)

	var project map[string]string

	switch {
	case ev.Enriched(iutil.EnrichedProject):
		project = projectLabels(ev)
	case prev != nil:
		project = prev.project
	}

	// The project's settings default the instance's, overridden field by field.
	// Transfer and NS stay the project's alone; instanceLabels has already
	// dropped both.
	for key, value := range project {
		if key == metaTransfer || key == metaNS {
			continue
		}

		_, own := meta[key]
		if own {
			continue
		}

		if meta == nil {
			meta = map[string]string{}
		}

		meta[key] = value
	}

	zone := zoneFor(ev.Project(), meta, suffix)

	return &instance{
		zone:     zone,
		meta:     meta,
		project:  project,
		transfer: transferable(project),
		ns:       relativeNames(project[metaNS], zone),
		nets:     nets,
	}
}

// transferable reports whether a project's labels opt its zone into transfer.
// Two callers read it; the key and the spelling of true must agree between them.
func transferable(project map[string]string) bool {
	return incusutil.IsTrue(project[metaTransfer])
}

// zoneFor returns the zone a project serves. Its own labels may override the
// name; otherwise it is <project>.<suffix>.
func zoneFor(project string, meta map[string]string, suffix string) string {
	override := meta[metaZone]
	if override != "" {
		return dns.CanonicalName(override)
	}

	return dns.CanonicalName(project + "." + suffix)
}

// build derives every record from everything held, fleet-wide rather than per
// project. Pure and non-aliasing, so a published snapshot is safe mid-fold.
func build(held map[string]*instance, prev *ecs_view.Snapshot, ttl uint32) *ecs_view.Snapshot {
	snap := &ecs_view.Snapshot{
		ByZone: map[string]*ecs_view.Zone{},
		ByAddr: map[netip.Addr]ecs_view.ViewID{},
		Views:  map[ecs_view.ViewID]map[string]ecs_view.RRSets{},
		Nets:   subnets(held),
		TTL:    ttl,
	}

	// Every zone's names, and the instances answering to each. Two projects
	// resolving to one zone name really are one zone.
	hosts := map[string]map[string][]*instance{}

	// The reverse of the same thing: zone, then name, then what answers there.
	revs := map[string]map[string][]ptrEntry{}

	// A zone may be handed over whole only if every instance in it agrees; one
	// holdout on a shared zone name closes transfer for both projects.
	transfers := map[string]bool{}

	// The zone's own NS names, unioned rather than agreed on: two projects
	// sharing a zone name are one zone, so their name servers are too.
	nsSets := map[string][]string{}

	for key, inst := range held {
		names := hosts[inst.zone]
		if names == nil {
			names = map[string][]*instance{}
			hosts[inst.zone] = names
			transfers[inst.zone] = true
		}

		transfers[inst.zone] = transfers[inst.zone] && inst.transfer
		nsSets[inst.zone] = append(nsSets[inst.zone], inst.ns...)

		host := dns.CanonicalName(nameOf(key)) + inst.zone

		names[host] = append(names[host], inst)

		// A scaled service's replicas land under one record.
		service := inst.meta[metaService]
		if service != "" {
			svc := dns.CanonicalName(service) + inst.zone
			names[svc] = append(names[svc], inst)
		}

		// An instance queries from the networks it sits on, so every address of
		// its own resolves to one view.
		id, _ := viewOf(inst)

		for netKey, net := range inst.nets {
			indexAddrs(snap, net.IPv4(), id)
			indexAddrs(snap, net.IPv6(), id)

			// The instance name alone: a reverse lookup wants the one name that
			// names this host and no other.
			addReverse(revs, netKey, host, inst.transfer, inst.ns, net.IPv4(), net.Prefixes())
			addReverse(revs, netKey, host, inst.transfer, inst.ns, net.IPv6(), net.Prefixes())
		}
	}

	// Aliases once every host name is known, since a name the fleet already
	// answers to is not one an alias may take.
	aliases := aliasRecords(held, hosts)

	for zoneName, names := range hosts {
		ns := sortedUnique(nsSets[zoneName])

		// Transfer stays out of the hash: it changes who may take the zone, not
		// what is in it, so flipping the label must not step the serial.
		z := &ecs_view.Zone{
			Hash:     hashHosts(names, aliases[zoneName], ns),
			Transfer: transfers[zoneName],
			NS:       ns,
		}
		z.Serial = nextSerial(prev, zoneName, z.Hash)

		byNetwork(z, names, ttl)

		snap.ByZone[zoneName] = z
	}

	// A zone per absolute alias that landed outside all of them, then the
	// aliases themselves - which need the host names rendered first.
	aliasZones(snap, prev, aliases)
	renderAliases(snap, aliases, ttl)

	for zoneName, z := range reverseZones(revs, prev, ttl) {
		// A forward zone of the same name is the one somebody asked for.
		_, taken := snap.ByZone[zoneName]
		if taken {
			continue
		}

		snap.ByZone[zoneName] = z
	}

	// Reverse zones are in place, and byView gathers over every zone there is.
	byView(snap, hosts)

	return snap
}

// nameOf is the instance name out of a held key, which carries the project too
// because two projects may each have a web.
func nameOf(key string) string {
	_, name, found := cut(key)
	if !found {
		return key
	}

	return name
}

// cut splits a held key into its project and name.
func cut(key string) (project, name string, found bool) {
	for i := range len(key) {
		if key[i] == '/' {
			return key[:i], key[i+1:], true
		}
	}

	return "", key, false
}

// heldKey is how an instance is held: by project and name, never by name alone.
func heldKey(project, name string) string { return project + "/" + name }

// nextSerial carries a zone's serial forward when its records are unchanged and
// steps it when they are not. A new zone starts at 1.
func nextSerial(prev *ecs_view.Snapshot, name string, hash uint64) uint32 {
	if prev == nil {
		return 1
	}

	old, existed := prev.ByZone[name]
	if !existed {
		return 1
	}

	if old.Hash == hash {
		return old.Serial
	}

	return old.Serial + 1
}

// viewOf names the set of networks an instance sits on.
func viewOf(inst *instance) (ecs_view.ViewID, []string) {
	keys := make([]string, 0, len(inst.nets))
	for key := range inst.nets {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return ecs_view.ViewOf(keys), keys
}

// indexAddrs makes every address resolve to the view its owner queries from.
// A clash between two projects' overlapping subnets is ambiguous, not map order.
func indexAddrs(snap *ecs_view.Snapshot, list []netip.Addr, id ecs_view.ViewID) {
	for _, addr := range list {
		held, taken := snap.ByAddr[addr]
		if taken && held != id {
			snap.ByAddr[addr] = ecs_view.AmbiguousView

			continue
		}

		snap.ByAddr[addr] = id
	}
}

// subnets maps every network's prefixes to its key. Duplicates go: a iutil
// network is listed by each project referencing it, and LookupNet needs one.
func subnets(held map[string]*instance) []ecs_view.NetEntry {
	var entries []ecs_view.NetEntry

	seen := map[ecs_view.NetEntry]struct{}{}

	for _, inst := range held {
		for key, net := range inst.nets {
			for _, prefix := range net.Prefixes() {
				entry := ecs_view.NetEntry{Prefix: prefix, Key: key}

				_, dup := seen[entry]
				if dup {
					continue
				}

				seen[entry] = struct{}{}
				entries = append(entries, entry)
			}
		}
	}

	return entries
}

// sortedUnique sorts and de-duplicates a name list, nil for an empty one so an
// unset NS still compares equal across builds.
func sortedUnique(list []string) []string {
	if len(list) == 0 {
		return nil
	}

	out := slices.Clone(list)
	sort.Strings(out)

	return slices.Compact(out)
}

// hashHosts digests one zone from the instances, not the rendered records, so
// it notices a reachability change too; aliases and the NS set go in the same
// digest, sorted first.
func hashHosts(names map[string][]*instance, aliases map[string]*aliasRecord, ns []string) uint64 {
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}

	sort.Strings(sorted)

	h := fnv.New64a()

	items := make([]string, 0, 16)
	buf := make([]byte, 0, 32)

	for _, name := range sorted {
		_, _ = h.Write([]byte(name))

		items = items[:0]

		for _, inst := range names[name] {
			for key, net := range inst.nets {
				for _, addr := range append(net.IPv4(), net.IPv6()...) {
					buf = addr.AppendTo(buf[:0])
					items = append(items, key+"\x00"+string(buf))
				}
			}
		}

		sort.Strings(items)

		for _, item := range items {
			_, _ = h.Write([]byte(item))
		}
	}

	sorted = sorted[:0]
	for name := range aliases {
		sorted = append(sorted, name)
	}

	sort.Strings(sorted)

	for _, name := range sorted {
		rec := aliases[name]

		_, _ = h.Write([]byte(name))
		_, _ = h.Write([]byte("\x00"))
		_, _ = h.Write([]byte(rec.target))

		for _, key := range rec.keys {
			_, _ = h.Write([]byte("\x00"))
			_, _ = h.Write([]byte(key))
		}
	}

	for _, name := range ns {
		_, _ = h.Write([]byte("\x00"))
		_, _ = h.Write([]byte(name))
	}

	return h.Sum64()
}

// byNetwork renders every name's records once per network it is reachable on,
// so every view that can see it holds the same records by reference.
func byNetwork(z *ecs_view.Zone, names map[string][]*instance, ttl uint32) {
	z.Names = make(map[string]map[string]ecs_view.RRSets, len(names))

	for name, list := range names {
		type addrs struct {
			v4 []netip.Addr
			v6 []netip.Addr
		}

		perNet := map[string]addrs{}

		for _, inst := range list {
			for key, net := range inst.nets {
				seen := perNet[key]
				seen.v4 = append(seen.v4, net.IPv4()...)
				seen.v6 = append(seen.v6, net.IPv6()...)
				perNet[key] = seen
			}
		}

		rendered := make(map[string]ecs_view.RRSets, len(perNet))
		for key, seen := range perNet {
			// Sorted, so two builds of one fleet render identical records.
			slices.SortFunc(seen.v4, netip.Addr.Compare)
			slices.SortFunc(seen.v6, netip.Addr.Compare)

			rendered[key] = ecs_view.Render(name, seen.v4, seen.v6, ttl)
		}

		z.Names[name] = rendered
	}
}

// byView precomputes, per view, every name's answer from there, so a query is
// a lookup rather than a gather across the whole fleet.
func byView(snap *ecs_view.Snapshot, hosts map[string]map[string][]*instance) {
	sets := map[ecs_view.ViewID][]string{}

	for _, names := range hosts {
		for _, list := range names {
			for _, inst := range list {
				id, keys := viewOf(inst)
				sets[id] = keys
			}
		}
	}

	snap.Views = make(map[ecs_view.ViewID]map[string]ecs_view.RRSets, len(sets))

	for id, keys := range sets {
		visible := map[string]ecs_view.RRSets{}

		for _, z := range snap.ByZone {
			for name, perNet := range z.Names {
				// Absent means invisible, which the query path reports as
				// NXDOMAIN exactly like a name that does not exist.
				gathered, reachable := ecs_view.Gather(perNet, keys)
				if !reachable {
					continue
				}

				visible[name] = gathered
			}
		}

		snap.Views[id] = visible
	}
}
