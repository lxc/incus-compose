package ecs_view

import (
	"log/slog"
	"net/netip"
	"slices"
	"sort"

	"github.com/coredns/coredns/request"
	"github.com/miekg/dns"
)

// maxEnvelope is how much of one transfer message is filled before another
// starts, leaving room under the 64 KiB wire limit for framing and a TSIG record.
const maxEnvelope = 63000

// transfer answers AXFR and IXFR from the current snapshot. A zone goes over
// whole or not at all, so both gates are checked before the snapshot is read.
func (v *ECSView) transfer(state request.Request) (int, error) {
	w, r := state.W, state.Req

	// A transfer is a stream of messages and UDP carries one message.
	if state.Proto() != "tcp" {
		return dns.RcodeRefused, nil
	}

	peer, known := sourceAddr(w.RemoteAddr())
	if !known || !v.allowedTransfer(peer) {
		return dns.RcodeRefused, nil
	}

	// Nil while no secrets are configured; once there are, this is the only
	// assertion of who the peer is, reported rather than dropped on failure.
	err := w.TsigStatus()
	if err != nil {
		return dns.RcodeRefused, err
	}

	snap := v.current.Load()

	zoneName := state.Name()

	// Exact rather than longest match: a transfer names an apex or nothing. An
	// invented zone carries no Transfer, so it is refused rather than handed over.
	z, served := snap.ByZone[zoneName]
	if !served || !z.Transfer {
		return dns.RcodeRefused, nil
	}

	var serial uint32

	if state.QType() == dns.TypeIXFR {
		if len(r.Ns) != 1 {
			return dns.RcodeServerFailure, nil
		}

		asked, isSOA := r.Ns[0].(*dns.SOA)
		if !isSOA {
			return dns.RcodeServerFailure, nil
		}

		serial = asked.Serial
	}

	slog.Debug("Transfer", "zone", zoneName, "qtype", state.QType(), "peer", peer, "serial", serial, "have", z.Serial)

	apex := soa(zoneName, z.NS, z.Serial, snap.TTL)

	// Already current. There is no journal here to cut a delta from, so an
	// older serial takes the whole zone instead.
	if serial != 0 && serial == z.Serial {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Authoritative = true
		m.Answer = []dns.RR{apex}

		err = w.WriteMsg(m)
		if err != nil {
			return dns.RcodeServerFailure, err
		}

		return dns.RcodeSuccess, nil
	}

	// Everything is rendered already, so the envelopes are cut and the channel
	// closed before Out reads any of it - no producer goroutine to hang on a peer.
	ch := envelopes(zoneRecords(zoneName, z, apex, snap.TTL))

	err = new(dns.Transfer).Out(w, r, ch)
	if err != nil {
		return dns.RcodeServerFailure, err
	}

	return dns.RcodeSuccess, nil
}

// allowedTransfer reports whether peer is one the operator named. No prefixes
// allow nobody, which is what keeps a transfer opt-in at both ends.
func (v *ECSView) allowedTransfer(peer netip.Addr) bool {
	for _, prefix := range v.AllowTransfer {
		if prefix.Contains(peer) {
			return true
		}
	}

	return false
}

// zoneRecords is the zone as a transfer carries it: apex SOA, NS, every name,
// then SOA again - unfiltered, since a serial is per zone and not per view.
func zoneRecords(zoneName string, z *Zone, apex *dns.SOA, ttl uint32) []dns.RR {
	names := make([]string, 0, len(z.Names))
	for name := range z.Names {
		names = append(names, name)
	}

	// Sorted, so two transfers of an unchanged zone are the same byte stream.
	sort.Strings(names)

	out := make([]dns.RR, 0, len(names)+3)

	out = append(out, apex)
	out = append(out, nsRecords(zoneName, z.NS, ttl)...)

	for _, name := range names {
		perNet := z.Names[name]

		keys := make([]string, 0, len(perNet))
		for key := range perNet {
			keys = append(keys, key)
		}

		sets, reachable := Gather(perNet, keys)
		if !reachable {
			continue
		}

		// An alias shares its name with the records it was chased into, so a
		// zone may not hold both sets; on the wire the CNAME goes alone.
		cname, aliased := sets[dns.TypeCNAME]
		if aliased {
			out = append(out, cname...)

			continue
		}

		types := make([]uint16, 0, len(sets))
		for qtype := range sets {
			types = append(types, qtype)
		}

		slices.Sort(types)

		for _, qtype := range types {
			out = append(out, sets[qtype]...)
		}
	}

	return append(out, apex)
}

// envelopes cuts records into messages that fit the wire, on a channel already
// closed: everything here came from an immutable snapshot, so nothing is left to produce.
func envelopes(rrs []dns.RR) chan *dns.Envelope {
	var (
		out   []*dns.Envelope
		batch []dns.RR
		size  int
	)

	for _, rr := range rrs {
		n := dns.Len(rr)

		if len(batch) > 0 && size+n > maxEnvelope {
			out = append(out, &dns.Envelope{RR: batch})
			batch = nil
			size = 0
		}

		batch = append(batch, rr)
		size += n
	}

	if len(batch) > 0 {
		out = append(out, &dns.Envelope{RR: batch})
	}

	ch := make(chan *dns.Envelope, len(out))
	for _, e := range out {
		ch <- e
	}

	close(ch)

	return ch
}
