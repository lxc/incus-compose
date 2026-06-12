package client

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
