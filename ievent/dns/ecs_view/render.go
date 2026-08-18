package ecs_view

import (
	"net/netip"

	"github.com/miekg/dns"
)

// Render turns one name's addresses on one network into records, iutil by
// every view that can see this network. v4 holds only IPv4, v6 only IPv6, pre-sorted.
func Render(name string, v4, v6 []netip.Addr, ttl uint32) RRSets {
	out := make(RRSets, 2)

	if len(v4) > 0 {
		rrs := make([]dns.A, len(v4))
		buf := make([]byte, 0, 4*len(v4))
		list := make([]dns.RR, 0, len(v4))

		for i, addr := range v4 {
			b := addr.As4()
			at := len(buf)
			buf = append(buf, b[:]...)

			rrs[i] = dns.A{
				Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl},
				A:   buf[at : at+4 : at+4],
			}
			list = append(list, &rrs[i])
		}

		out[dns.TypeA] = list
	}

	if len(v6) > 0 {
		rrs := make([]dns.AAAA, len(v6))
		buf := make([]byte, 0, 16*len(v6))
		list := make([]dns.RR, 0, len(v6))

		for i, addr := range v6 {
			b := addr.As16()
			at := len(buf)
			buf = append(buf, b[:]...)

			rrs[i] = dns.AAAA{
				Hdr:  dns.RR_Header{Name: name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: ttl},
				AAAA: buf[at : at+16 : at+16],
			}
			list = append(list, &rrs[i])
		}

		out[dns.TypeAAAA] = list
	}

	return out
}

// RenderCName turns one name into a CNAME onto target, per network, chased at
// build time so answering through it stays three map lookups.
func RenderCName(name, target string, base map[string]RRSets, keys []string, ttl uint32) map[string]RRSets {
	cname := dns.RR(&dns.CNAME{
		Hdr:    dns.RR_Header{Name: name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: ttl},
		Target: target,
	})

	out := make(map[string]RRSets, len(keys))

	for _, key := range keys {
		sets, reachable := base[key]
		if !reachable {
			continue
		}

		rendered := make(RRSets, len(sets)+1)
		rendered[dns.TypeCNAME] = []dns.RR{cname}

		for qtype, rrs := range sets {
			// Canonical name first, in its own array so the target's records
			// stay iutil.
			chased := make([]dns.RR, 0, len(rrs)+1)
			chased = append(chased, cname)
			chased = append(chased, rrs...)

			rendered[qtype] = chased
		}

		out[key] = rendered
	}

	return out
}

// RenderPTR turns the names one address answers to into records, sharing storage
// as Render does; sorting the targets is the caller's.
func RenderPTR(name string, targets []string, ttl uint32) RRSets {
	if len(targets) == 0 {
		return nil
	}

	rrs := make([]dns.PTR, len(targets))
	list := make([]dns.RR, 0, len(targets))

	for i, target := range targets {
		rrs[i] = dns.PTR{
			Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: ttl},
			Ptr: target,
		}
		list = append(list, &rrs[i])
	}

	return RRSets{dns.TypePTR: list}
}
