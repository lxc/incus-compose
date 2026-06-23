package client

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmitProgress(t *testing.T) {
	t.Parallel()

	r := newMockResource("web", KindInstance, 0, true)

	t.Run("forwards to handler", func(t *testing.T) {
		t.Parallel()

		var got Progress
		var gotAction Action
		var gotResource Resource
		gc := &GlobalClient{progressHandler: func(a Action, res Resource, _ Options, p Progress) {
			gotAction, gotResource, got = a, res, p
		}}

		want := Progress{Percent: -1, Text: "Waiting for dependency db"}
		gc.emitProgress(ActionStart, r, Options{}, want)

		require.Equal(t, ActionStart, gotAction)
		require.Same(t, r, gotResource)
		require.Equal(t, want, got)
	})

	t.Run("no handler is a no-op", func(t *testing.T) {
		t.Parallel()

		gc := &GlobalClient{}
		require.NotPanics(t, func() {
			gc.emitProgress(ActionStart, r, Options{}, Progress{Percent: -1, Text: "x"})
		})
	})
}

func TestParsePercent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want int
	}{
		{"native rootfs", "rootfs: 42% (3.10MB/s)", 42},
		{"native complete", "metadata: 100% (876B/s)", 100},
		{"leading zero progress", "rootfs: 0% (0B/s)", 0},
		{"oci status text", "Retrieving OCI image from registry", -1},
		{"oci tarball bytes", "Generating rootfs tarball: 12.3MB (4.1MB/s)", -1},
		{"empty", "", -1},
		{"bare percent sign", "%", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, parsePercent(tt.in))
		})
	}
}
