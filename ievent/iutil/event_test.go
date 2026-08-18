package iutil

import (
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// No skip helper on any test here: this package talks to nothing.

// netOf is one network with one address on it.
func netOf(addr string) map[string]*Network {
	return map[string]*Network{
		"p/net0": NewNetwork("net0", "p", true,
			[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")},
			[]netip.Addr{netip.MustParseAddr(addr)}, nil),
	}
}

// TestEqual pins what counts as the same thing said twice, which is what decides
// whether an event the enricher invented is worth walking.
func TestEqual(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	base := func() *Event {
		return NewEvent(at, "instance-updated", "p", "web", "").
			WithInstance(true, map[string]string{"user.label.a": "1"}, netOf("10.0.0.2"))
	}

	cases := []struct {
		name  string
		other func() *Event
		equal bool
	}{
		{
			name:  "the same read twice",
			other: base,
			equal: true,
		},
		{
			// The read is what the event is about; when it was decoded is not.
			name: "decoded at another moment",
			other: func() *Event {
				return NewEvent(at.Add(time.Hour), "instance-updated", "p", "web", "").WithInstance(true, map[string]string{"user.label.a": "1"}, netOf("10.0.0.2"))
			},
			equal: true,
		},
		{
			// What the whole chain was doing is not about this subject, and the
			// brackets are what carry it.
			name:  "minted while the chain was elsewhere",
			other: func() *Event { return base().WithChainState(ChainWarm) },
			equal: true,
		},
		{
			name:  "a value only its own plugin reads",
			other: func() *Event { return base().WithValue("k", "v") },
			equal: true,
		},
		{
			// Included on purpose: a stop whose transition an update already
			// absorbed still has to reach whoever is counting stops.
			name: "another action about the same instance",
			other: func() *Event {
				return NewEvent(at, "instance-stopped", "p", "web", "").WithInstance(true, map[string]string{"user.label.a": "1"}, netOf("10.0.0.2"))
			},
		},
		{
			name: "the same name in another project",
			other: func() *Event {
				return NewEvent(at, "instance-updated", "other", "web", "").WithInstance(true, map[string]string{"user.label.a": "1"}, netOf("10.0.0.2"))
			},
		},
		{
			name: "an address that moved",
			other: func() *Event {
				return NewEvent(at, "instance-updated", "p", "web", "").WithInstance(true, map[string]string{"user.label.a": "1"}, netOf("10.0.0.3"))
			},
		},
		{
			name: "no longer running",
			other: func() *Event {
				return NewEvent(at, "instance-updated", "p", "web", "").WithInstance(false, map[string]string{"user.label.a": "1"}, netOf("10.0.0.2"))
			},
		},
		{
			name: "a label that changed",
			other: func() *Event {
				return NewEvent(at, "instance-updated", "p", "web", "").WithInstance(true, map[string]string{"user.label.a": "2"}, netOf("10.0.0.2"))
			},
		},
		{
			// A failure is never the same as the answer it failed to get.
			name:  "the same read, failed",
			other: func() *Event { return base().WithFailed("source/read") },
		},
		{
			name:  "the same read, dropped",
			other: func() *Event { return base().WithDropped("debounce") },
		},
		{
			// Reads that landed is part of what an event says: "no networks" and
			// "nobody asked for networks" are different answers.
			name:  "read for less",
			other: func() *Event { return NewEvent(at, "instance-updated", "p", "web", "") },
		},
		{
			name:  "the project's own labels filled in",
			other: func() *Event { return base().WithProject(map[string]string{"z": "1"}) },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.equal, base().Equal(tc.other()))
			assert.Equal(t, tc.equal, tc.other().Equal(base()), "the answer depends on which side is asked")
		})
	}
}

// TestEqualNil pins the one comparison with nothing to compare: the first event
// about a subject has no predecessor, and it is news.
func TestEqualNil(t *testing.T) {
	t.Parallel()

	ev := NewEvent(time.Now(), "instance-updated", "p", "web", "")

	assert.False(t, ev.Equal(nil))
	assert.True(t, (*Event)(nil).Equal(nil))
}
