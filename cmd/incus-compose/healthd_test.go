package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHealthdNetwork(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		network string
		want    healthdNetworkRef
		wantErr bool
	}{
		{
			name:    "empty is the project default network",
			network: "",
			want:    healthdNetworkRef{name: "default", deflt: true},
		},
		{
			name:    "project:network references a managed network",
			network: "default:default",
			want:    healthdNetworkRef{project: "default", name: "default"},
		},
		{
			name:    "project:network with distinct names",
			network: "infra:backend",
			want:    healthdNetworkRef{project: "infra", name: "backend"},
		},
		{
			name:    "no colon is a bridge name",
			network: "incusbr0",
			want:    healthdNetworkRef{name: "incusbr0"},
		},
		{
			name:    "missing project errors",
			network: ":default",
			wantErr: true,
		},
		{
			name:    "missing network errors",
			network: "default:",
			wantErr: true,
		},
		{
			name:    "too many colons errors",
			network: "a:b:c",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseHealthdNetwork(tt.network)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
