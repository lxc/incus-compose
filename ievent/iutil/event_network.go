package iutil

import (
	"iter"
	"maps"
	"net/netip"
	"slices"
)

// Network returns one network by Network.Key, or nil. Ask
// Enriched(EnrichedNetworks) to tell that from nothing having been read.
func (e *Event) Network(key string) *Network {
	return e.nets[key]
}

// Networks iterates every network the instance sits on.
func (e *Event) Networks() iter.Seq2[string, *Network] {
	return maps.All(e.nets)
}

// WithNetworks derives an event carrying the networks alone, for a network event
// where nothing about an instance changed. It takes ownership of nets.
func (e *Event) WithNetworks(nets map[string]*Network) *Event {
	next := *e
	next.nets = nets
	next.enriched |= EnrichedNetworks

	return &next
}

// Network is one network the event's instance sits on: the wire, and the addresses held on it,
// joined rather than carried as parallel maps that could drift apart.
type Network struct {
	name    string
	project string
	managed bool

	prefixes []netip.Prefix

	ipv4 []netip.Addr
	ipv6 []netip.Addr
}

// NewNetwork builds one network from plain values, keeping this package free of the Incus
// module. It takes ownership of all three slices - do not reuse them; sorting is the caller's.
func NewNetwork(name, project string, managed bool, prefixes []netip.Prefix, ipv4, ipv6 []netip.Addr) *Network {
	return &Network{
		name:     name,
		project:  project,
		managed:  managed,
		prefixes: prefixes,
		ipv4:     ipv4,
		ipv6:     ipv6,
	}
}

// Key identifies a network by the owning project, never the project looking at it, so a bridge
// two projects reference stays one network even though both may own the same name.
func (n *Network) Key() string { return n.project + "/" + n.name }

// IPv4 returns the instance's addresses on this network, as a clone. Empty while
// the instance is up but has no lease yet.
func (n *Network) IPv4() []netip.Addr { return slices.Clone(n.ipv4) }

// IPv6 returns the instance's addresses on this network, as a clone.
func (n *Network) IPv6() []netip.Addr { return slices.Clone(n.ipv6) }

// Addressed reports whether the instance holds anything on this network.
func (n *Network) Addressed() bool { return len(n.ipv4) > 0 || len(n.ipv6) > 0 }

// Name is the network's own name, which is not unique across projects.
func (n *Network) Name() string { return n.name }

// Project is the project that owns the network.
func (n *Network) Project() string { return n.project }

// Managed says Incus runs this network's addressing. An unmanaged network has no
// subnet to identify a querier by, but still keys records.
func (n *Network) Managed() bool { return n.managed }

// Prefixes returns the network's subnets, as a clone.
func (n *Network) Prefixes() []netip.Prefix {
	return slices.Clone(n.prefixes)
}

// equal reports whether two reads found the same wire and the same addresses on it.
func (n *Network) equal(other *Network) bool {
	if n == nil || other == nil {
		return n == other
	}

	return n.name == other.name &&
		n.project == other.project &&
		n.managed == other.managed &&
		slices.Equal(n.prefixes, other.prefixes) &&
		slices.Equal(n.ipv4, other.ipv4) &&
		slices.Equal(n.ipv6, other.ipv6)
}
