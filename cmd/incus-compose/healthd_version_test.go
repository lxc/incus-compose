package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSidecarNeedsUpgrade(t *testing.T) {
	t.Parallel()

	const repo = "ghcr.io/lxc/incus-compose/ic-healthd:"

	tests := []struct {
		name string
		have string
		want string
		res  bool
	}{
		{
			name: "the same image is left alone",
			have: repo + "1.0.0",
			want: repo + "1.0.0",
		},
		{
			name: "a newer semver upgrades",
			have: repo + "1.0.0",
			want: repo + "1.1.0",
			res:  true,
		},
		{
			name: "an older semver does not downgrade the shared daemon",
			have: repo + "1.1.0",
			want: repo + "1.0.0",
		},
		{
			name: "a pre-release loses to its release",
			have: repo + "1.0.0-rc.1",
			want: repo + "1.0.0",
			res:  true,
		},
		{
			name: "a release is not replaced by a pre-release",
			have: repo + "1.0.0",
			want: repo + "1.0.0-rc.1",
		},
		{
			name: "a leading v still parses",
			have: repo + "v1.0.0",
			want: repo + "v1.2.0",
			res:  true,
		},
		{
			name: "a moving tag replaces on any difference",
			have: repo + "latest",
			want: repo + "edge",
			res:  true,
		},
		{
			name: "a pre-release is not downgraded by an older one",
			have: repo + "1.0.0-rc.5",
			want: repo + "1.0.0-rc.4",
		},
		{
			name: "a git describe replaces a release",
			have: repo + "1.2.0",
			want: repo + "1.2.0-29-g57f305c-dirty",
			res:  true,
		},
		{
			name: "a release replaces a git describe",
			have: repo + "1.2.0-29-g57f305c-dirty",
			want: repo + "1.2.0",
			res:  true,
		},
		{
			name: "a clean git describe still replaces",
			have: repo + "v1.0.0-beta.20",
			want: repo + "v1.0.0-beta.20-29-g57f305c",
			res:  true,
		},
		{
			name: "an untagged image is not a downgrade of itself",
			have: "images:alpine/edge",
			want: "images:alpine/edge",
		},
		{
			name: "a registry port is not a tag",
			have: "localhost:5000/ic-healthd",
			want: "localhost:5000/ic-healthd",
		},
		{
			name: "a tagged image on a ported registry still compares",
			have: "localhost:5000/ic-healthd:1.0.0",
			want: "localhost:5000/ic-healthd:0.9.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.res, sidecarNeedsUpgrade(tt.have, tt.want))
		})
	}
}
