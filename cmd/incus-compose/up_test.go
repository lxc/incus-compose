package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/cmd/incus-compose/version"
)

func TestVersionCommand(t *testing.T) {
	// Not parallel: mutates the package-global version.Version.
	oldVersion := version.Version
	version.Version = "v1.2.3"
	defer func() { version.Version = oldVersion }()

	out := &bytes.Buffer{}
	require.NoError(t, newVersionCommand().Action(t.Context(), &cli.Command{Writer: out}))
	assert.Equal(t, "incus-compose version v1.2.3\n", out.String())
}

func TestResolveHealthdImage(t *testing.T) {
	// Not parallel: mutates the package-global version.Version.
	oldVersion := version.Version
	version.Version = "v1.2.3"
	defer func() { version.Version = oldVersion }()

	assert.Equal(t,
		"ghcr.io/lxc/incus-compose/ic-healthd:1.2.3",
		resolveHealthdImage("ghcr.io/lxc/incus-compose/ic-healthd:{version}"),
	)
	assert.Equal(t, "custom:latest", resolveHealthdImage("custom:latest"))
}

func TestParseScale(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values []string
		want   map[string]int
	}{
		{name: "empty", values: nil, want: map[string]int{}},
		{name: "single", values: []string{"web=3"}, want: map[string]int{"web": 3}},
		{name: "multiple", values: []string{"web=3", "api=2"}, want: map[string]int{"web": 3, "api": 2}},
		{name: "invalid ignored", values: []string{"web", "api=bad", "db=1"}, want: map[string]int{"db": 1}},
		{name: "last wins", values: []string{"web=2", "web=4"}, want: map[string]int{"web": 4}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, parseScale(tt.values))
		})
	}
}
