package main

import (
	"net/url"
	"testing"

	"github.com/lxc/incus/v7/shared/units"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/shared"
)

func TestHealthdCarriedConfig(t *testing.T) {
	t.Parallel()

	config := map[string]string{
		"environment.INCUS_COMPOSE_HEALTHD_INCUS":           "https://10.0.0.1:8443",
		"environment.INCUS_COMPOSE_HEALTHD_WORKERS":         "64",
		"environment.INCUS_COMPOSE_HEALTHD_RESTART_WORKERS": "8",
		"environment.INCUS_COMPOSE_HEALTHD_DEBUG":           "true",
		"environment.INCUS_COMPOSE_HEALTHD_PROJECT_MARKER":  "user.healthcheck.scope=global",
		"environment.INCUS_COMPOSE_HEALTHD_TOKEN":           "a-consumed-token",
		"limits.cpu":                         "4",
		"limits.memory":                      "512MB",
		"user.image_alias":                   "ghcr.io/lxc/incus-compose/ic-healthd:1.1.0",
		"user.incus-compose.managed":         "true",
		client.HealthKeyPrefix + "daemon":    "true",
		client.HealthKeyPrefix + "ignore":    "true",
		client.HealthKeyPrefix + "restart":   "unless-stopped",
		shared.HealthStatusKey:               "healthy",
		client.HealthKeyPrefix + "stopped":   "true",
		"oci.entrypoint":                     "/usr/local/bin/ic-healthd run",
		"image.architecture":                 "x86_64",
		"volatile.eth0.hwaddr":               "00:16:3e:00:00:01",
		"volatile.base_image":                "deadbeef",
		"user.something.a.newer.version.set": "keep me",
	}

	carried := healthdCarriedConfig(config)

	for _, key := range []string{
		"environment.INCUS_COMPOSE_HEALTHD_INCUS",
		"environment.INCUS_COMPOSE_HEALTHD_WORKERS",
		"environment.INCUS_COMPOSE_HEALTHD_RESTART_WORKERS",
		"environment.INCUS_COMPOSE_HEALTHD_DEBUG",
		"environment.INCUS_COMPOSE_HEALTHD_PROJECT_MARKER",
		"limits.cpu",
		"limits.memory",
		"user.incus-compose.managed",
		client.HealthKeyPrefix + "daemon",
		client.HealthKeyPrefix + "ignore",
		client.HealthKeyPrefix + "restart",
		"user.something.a.newer.version.set",
	} {
		assert.Equal(t, config[key], carried[key], "%s must survive the replace", key)
	}

	for _, key := range []string{
		"user.image_alias",
		"environment.INCUS_COMPOSE_HEALTHD_TOKEN",
		shared.HealthStatusKey,
		client.HealthKeyPrefix + "stopped",
		"oci.entrypoint",
		"image.architecture",
		"volatile.eth0.hwaddr",
		"volatile.base_image",
	} {
		assert.NotContains(t, carried, key, "%s must be derived again", key)
	}
}

func TestHealthdCarriedConfigEmpty(t *testing.T) {
	t.Parallel()

	assert.Empty(t, healthdCarriedConfig(map[string]string{}))
}

// A default that does not parse would silently disable the memory floor, since
// healthdFloorLimits has nothing to compare against and gives up.
func TestHealthdDefaultMemoryLimitParses(t *testing.T) {
	t.Parallel()

	memory, err := units.ParseByteSizeString(defaultHealthdMemoryLimit)
	require.NoError(t, err)
	assert.Positive(t, memory)
}

func TestHealthdFloorLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cpu        string
		memory     string
		wantCPU    string
		wantMemory string
	}{
		{
			name:       "a 1.1.0 sidecar is raised to the current defaults",
			cpu:        "1",
			memory:     "50MB",
			wantCPU:    "2",
			wantMemory: "256MiB",
		},
		{
			name:       "a larger limit is left alone",
			cpu:        "8",
			memory:     "1GiB",
			wantCPU:    "8",
			wantMemory: "1GiB",
		},
		{
			name:       "the default itself is not rewritten",
			cpu:        "2",
			memory:     "256MiB",
			wantCPU:    "2",
			wantMemory: "256MiB",
		},
		{
			name:       "a CPU pin is deliberate, whatever its width",
			cpu:        "0-3",
			memory:     "512MB",
			wantCPU:    "0-3",
			wantMemory: "512MB",
		},
		{
			name:       "a single-CPU pin survives even though it is below the floor",
			cpu:        "1-1",
			memory:     "512MB",
			wantCPU:    "1-1",
			wantMemory: "512MB",
		},
		{
			name:       "a CPU set is deliberate",
			cpu:        "1,2,3",
			memory:     "512MB",
			wantCPU:    "1,2,3",
			wantMemory: "512MB",
		},
		{
			name:       "a memory percentage has no byte value to compare",
			cpu:        "4",
			memory:     "10%",
			wantCPU:    "4",
			wantMemory: "10%",
		},
		{
			name:       "an unparseable value is left for Incus to reject",
			cpu:        "lots",
			memory:     "plenty",
			wantCPU:    "lots",
			wantMemory: "plenty",
		},
		{
			name: "an absent limit is not invented",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			carried := map[string]string{}
			if tt.cpu != "" {
				carried["limits.cpu"] = tt.cpu
			}
			if tt.memory != "" {
				carried["limits.memory"] = tt.memory
			}

			healthdFloorLimits(carried)

			assert.Equal(t, tt.wantCPU, carried["limits.cpu"])
			assert.Equal(t, tt.wantMemory, carried["limits.memory"])
		})
	}
}

// x-incus is applied after the floor, so a small limit is still reachable.
func TestHealthdSettingsXIncusBeatsTheFloor(t *testing.T) {
	t.Parallel()

	carried := healthdCarriedConfig(map[string]string{
		"limits.cpu":    "1",
		"limits.memory": "50MB",
	})
	assert.Equal(t, "2", carried["limits.cpu"], "the floor applies to what is carried")

	settings := healthdSettings(healthdParams{
		carry:  carried,
		xIncus: map[string]string{"limits.cpu": "1", "limits.memory": "50MB"},
	}, "", false)

	assert.Equal(t, "1", settings["limits.cpu"], "an explicit small limit still wins")
	assert.Equal(t, "50MB", settings["limits.memory"], "an explicit small limit still wins")
}

// carried is what a daemon another project set up leaves behind.
func carried() map[string]string {
	return map[string]string{
		envIncus:             "https://10.0.0.1:8443",
		envServerFingerprint: "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90",
		envWorkers:           "64",
		envRestartWorkers:    "8",
		envDebug:             "true",
		envTrace:             "true",
		"limits.cpu":         "4",
		"limits.memory":      "512MB",
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	u, err := url.Parse(raw)
	require.NoError(t, err)

	return u
}

func TestHealthdSettingsOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  healthdParams
		debug   bool
		derived string
		want    map[string]string
	}{
		{
			name:   "a run naming nothing keeps every carried setting",
			params: healthdParams{carry: carried()},
			// Derivation is skipped when the carried endpoint stands.
			want: carried(),
		},
		{
			name: "--healthd-incus overrides the carried endpoint",
			params: healthdParams{
				carry: carried(),
				incus: mustURL(t, "https://192.168.0.2:8443"),
			},
			derived: "https://192.168.0.2:8443",
			want:    map[string]string{envIncus: "https://192.168.0.2:8443"},
		},
		{
			name: "server fingerprint overrides carried fingerprint",
			params: healthdParams{
				carry:             carried(),
				serverFingerprint: "b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1",
			},
			want: map[string]string{envServerFingerprint: "b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1"},
		},
		{
			name: "compose workers and restart-workers override the carried pools",
			params: healthdParams{
				carry:          carried(),
				workers:        16,
				restartWorkers: 4,
			},
			want: map[string]string{envWorkers: "16", envRestartWorkers: "4"},
		},
		{
			name: "x-incus overrides carried limits",
			params: healthdParams{
				carry:  carried(),
				xIncus: map[string]string{"limits.cpu": "8"},
			},
			want: map[string]string{"limits.cpu": "8", "limits.memory": "512MB"},
		},
		{
			name:   "a derived endpoint fills the gap when nothing carried one",
			params: healthdParams{carry: map[string]string{}},
			// A fresh create: no carry, so the derived URL is all there is.
			derived: "https://10.0.0.1:8443",
			want:    map[string]string{envIncus: "https://10.0.0.1:8443"},
		},
		{
			name:   "--trace on a run that did not carry it",
			params: healthdParams{carry: map[string]string{}, trace: true},
			want:   map[string]string{envTrace: "true"},
		},
		{
			name:   "--debug on a run that did not carry it",
			params: healthdParams{carry: map[string]string{}},
			debug:  true,
			want:   map[string]string{envDebug: "true"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			settings := healthdSettings(tt.params, tt.derived, tt.debug)

			for key, value := range tt.want {
				assert.Equal(t, value, settings[key], "%s", key)
			}
		})
	}
}

// A recreate must not quietly drop what this run did not mention.
func TestHealthdSettingsKeepsUnrelatedCarriedKeys(t *testing.T) {
	t.Parallel()

	params := healthdParams{
		carry:   carried(),
		workers: 16,
	}
	params.carry["environment.INCUS_COMPOSE_HEALTHD_SOMETHING_NEWER"] = "keep me"

	settings := healthdSettings(params, "", false)

	assert.Equal(t, "16", settings[envWorkers], "the run's value wins")
	assert.Equal(t, "https://10.0.0.1:8443", settings[envIncus], "the carried endpoint stands")
	assert.Equal(t, "8", settings[envRestartWorkers], "an untouched pool is kept")
	assert.Equal(t, "4", settings["limits.cpu"], "an untouched limit is kept")
	assert.Equal(t, "keep me", settings["environment.INCUS_COMPOSE_HEALTHD_SOMETHING_NEWER"],
		"a key this version does not know about is kept")
}

// Without a carry there is nothing to preserve, so a plain create is unchanged.
func TestHealthdSettingsWithoutCarry(t *testing.T) {
	t.Parallel()

	settings := healthdSettings(healthdParams{workers: 32}, "https://10.0.0.1:8443", false)

	assert.Equal(t, map[string]string{
		envIncus:   "https://10.0.0.1:8443",
		envWorkers: "32",
	}, settings)
}
