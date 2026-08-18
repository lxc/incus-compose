// Package ecs_view serves DNS records filtered by who is asking. A querier is
// placed on a set of networks and sees only the names reachable there.
package ecs_view

import (
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/miekg/dns"
)

// NetEntry maps a network's subnet to the key that owns it.
type NetEntry struct {
	Prefix netip.Prefix
	Key    string
}

// Zone holds a whole zone's records, unfiltered, grouped by the network each is
// reachable on. Views share this storage rather than copying it.
type Zone struct {
	Names map[string]map[string]RRSets

	// Serial changes only when this zone's records change, never merely because
	// a snapshot was published.
	Serial uint32

	// Hash is what Serial is decided by. The source sets both, being the only
	// thing that knows what it published last.
	Hash uint64

	// Transfer says this zone may be handed over whole, off unless the source
	// says otherwise: the one answer here not filtered by who is asking.
	Transfer bool

	// NS is the zone's own name servers, absolute. Empty falls back to a
	// synthetic name nothing answers for - the source sets this only once an
	// operator has said who really serves the zone.
	NS []string

	// Fallthrough makes an invented zone claim the names in it and nothing else.
	// A held name still follows the ordinary visibility rule, invisible included.
	Fallthrough bool
}

// RRSets holds one name's records grouped by type, already filtered and
// rendered, so answering is a lookup; the grouping is what keeps NXDOMAIN and NODATA apart.
type RRSets map[uint16][]dns.RR

// ViewID names a set of networks, which is all the answer to a query depends on.
type ViewID string

// ViewOf canonicalises a set of network keys, sorted so any order names the same
// view.
func ViewOf(keys []string) ViewID {
	if len(keys) == 0 {
		return ""
	}

	sorted := slices.Clone(keys)
	sort.Strings(sorted)

	// A separator no network key can contain, so two sets cannot collide.
	return ViewID(strings.Join(sorted, "\x00"))
}

// Snapshot is an immutable view of every record. Updates build a new one.
type Snapshot struct {
	ByZone map[string]*Zone
	ByAddr map[netip.Addr]ViewID
	Views  map[ViewID]map[string]RRSets
	Nets   []NetEntry

	// TTL is what the records above were rendered with. A query wanting a
	// different one has to copy rather than retune a iutil header.
	TTL uint32
}

// MatchZone returns the longest zone holding qname, or "" when none does. It
// walks label boundaries, one map lookup per label; qname must be lowercase and fully qualified.
func (s *Snapshot) MatchZone(qname string) (string, *Zone) {
	// Longest first, so the first hit wins without comparing lengths.
	for i := 0; i < len(qname); {
		z, ok := s.ByZone[qname[i:]]
		if ok {
			return qname[i:], z
		}

		next := strings.IndexByte(qname[i:], '.')
		if next < 0 {
			break
		}

		i += next + 1
	}

	return "", nil
}

// LookupNet returns every network key whose subnet covers addr. All of them, or
// an anonymous querier lands in an arbitrary one.
func (s *Snapshot) LookupNet(addr netip.Addr) []string {
	var keys []string

	for _, entry := range s.Nets {
		if entry.Prefix.Contains(addr) {
			keys = append(keys, entry.Key)
		}
	}

	return keys
}

// AmbiguousView marks an address more than one host claims, refusing a
// querier from it rather than guessing. Exported: the source is what detects it.
const AmbiguousView ViewID = "\x00ambiguous"

// EmptySnapshot returns a usable snapshot with no records in it.
func EmptySnapshot() *Snapshot {
	return &Snapshot{
		ByZone: map[string]*Zone{},
		ByAddr: map[netip.Addr]ViewID{},
		Views:  map[ViewID]map[string]RRSets{},
	}
}
