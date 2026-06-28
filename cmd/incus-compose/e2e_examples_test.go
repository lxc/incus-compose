package main

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func skipExamples(t *testing.T) {
	_, ok := os.LookupEnv("INCUS_COMPOSE_TEST_EXAMPLES")
	if !ok {
		t.Skip("Skipping: env INCUS_COMPOSE_TEST_EXAMPLES is not set, run `just test-slow` for this test")
	}
}

func TestExampleHugo(t *testing.T) {
	skipExamples(t)

	ctx := context.Background()
	pn := t.Name()
	project := "../../examples/hugo"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "--project-directory", project, "down", "--project")
	})

	tests := []struct {
		name     string
		args     []string
		wantErr  bool
		snapshot bool
	}{
		{
			name:    "up",
			args:    []string{"--project-directory", project, "up", "--detach", "--timeout", "10m", "--dependency-timeout", "10m"},
			wantErr: false,
		},
		{
			name:     "list",
			args:     []string{"--project-directory", project, "list", "--format", "json"},
			wantErr:  false,
			snapshot: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if tt.snapshot {
				snapshotter.SnapshotT(t, normalizeListOutput(t, stdout))
			}
		})
	}
}

func TestExampleImmich(t *testing.T) {
	skipExamples(t)

	ctx := context.Background()
	pn := t.Name()
	project := "../../examples/immich"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "--project-directory", project, "down", "--project")
	})

	tests := []struct {
		name     string
		args     []string
		wantErr  bool
		snapshot bool
	}{
		{
			name:    "up",
			args:    []string{"--project-directory", project, "up", "--detach", "--timeout", "10m", "--dependency-timeout", "10m"},
			wantErr: false,
		},
		{
			name:     "list",
			args:     []string{"--project-directory", project, "list", "--format", "json"},
			wantErr:  false,
			snapshot: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if tt.snapshot {
				snapshotter.SnapshotT(t, normalizeListOutput(t, stdout))
			}
		})
	}
}

func TestExampleManyDependencies(t *testing.T) {
	skipExamples(t)

	ctx := context.Background()
	pn := t.Name()
	project := "../../examples/many-dependencies"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "--project-directory", project, "down", "--project")
	})

	tests := []struct {
		name     string
		args     []string
		wantErr  bool
		snapshot bool
	}{
		{
			name:    "up",
			args:    []string{"--project-directory", project, "up", "--detach", "--timeout", "10m", "--dependency-timeout", "10m"},
			wantErr: false,
		},
		{
			name:     "list",
			args:     []string{"--project-directory", project, "list", "--format", "json"},
			wantErr:  false,
			snapshot: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if tt.snapshot {
				snapshotter.SnapshotT(t, normalizeListOutput(t, stdout))
			}
		})
	}
}

func TestExampleWikijs(t *testing.T) {
	skipExamples(t)

	ctx := context.Background()
	pn := t.Name()
	project := "../../examples/wikijs"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "--project-directory", project, "down", "--project")
	})

	tests := []struct {
		name     string
		args     []string
		wantErr  bool
		snapshot bool
	}{
		{
			name:    "up",
			args:    []string{"--project-directory", project, "up", "--detach", "--timeout", "10m", "--dependency-timeout", "10m"},
			wantErr: false,
		},
		{
			name:     "list",
			args:     []string{"--project-directory", project, "list", "--format", "json"},
			wantErr:  false,
			snapshot: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := runCommand(t, ctx, pn, tt.args...)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if tt.snapshot {
				snapshotter.SnapshotT(t, normalizeListOutput(t, stdout))
			}
		})
	}
}
