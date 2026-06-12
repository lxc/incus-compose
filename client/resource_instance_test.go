package client

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ----------------------------------------------------------------------------
// InstanceSecret Tests
// ----------------------------------------------------------------------------

func TestInstanceSecret_Defaults(t *testing.T) {
	t.Parallel()
	secret := InstanceSecret{
		Source:  "db_password",
		Content: []byte("secret-value"),
	}

	require.Equal(t, "db_password", secret.Source)
	require.Equal(t, []byte("secret-value"), secret.Content)
	require.Empty(t, secret.Target, "Target should default to empty (handled in PushSecrets)")
	require.Zero(t, secret.UID)
	require.Zero(t, secret.GID)
	require.Zero(t, secret.Mode, "Mode should default to zero (handled in PushSecrets)")
}

func TestInstanceSecret_CustomTarget(t *testing.T) {
	t.Parallel()
	secret := InstanceSecret{
		Source:  "api_key",
		Target:  "/app/secrets/api.key",
		Content: []byte("my-api-key"),
		UID:     1000,
		GID:     1000,
		Mode:    0o440,
	}

	require.Equal(t, "api_key", secret.Source)
	require.Equal(t, "/app/secrets/api.key", secret.Target)
	require.Equal(t, []byte("my-api-key"), secret.Content)
	require.Equal(t, int64(1000), secret.UID)
	require.Equal(t, int64(1000), secret.GID)
	require.Equal(t, 0o440, secret.Mode)
}

func TestInstanceConfig_WithSecrets(t *testing.T) {
	t.Parallel()
	secrets := []InstanceSecret{
		{Source: "db_password", Content: []byte("pass1")},
		{Source: "api_key", Target: "/custom/path", Content: []byte("key1"), Mode: 0o440},
	}

	config := InstanceConfig{
		Image:   "docker.io/alpine:latest",
		Secrets: secrets,
	}

	require.Len(t, config.Secrets, 2)
	require.Equal(t, "db_password", config.Secrets[0].Source)
	require.Equal(t, "api_key", config.Secrets[1].Source)
	require.Equal(t, "/custom/path", config.Secrets[1].Target)
}

// ----------------------------------------------------------------------------
// SanitizeInstanceName Tests
// ----------------------------------------------------------------------------

func TestSanitizeInstanceName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		input             string
		expected          string
		checkHashFallback bool
	}{
		{
			name:     "simple name",
			input:    "web",
			expected: "web",
		},
		{
			name:     "underscore replacement",
			input:    "my_service",
			expected: "my-service",
		},
		{
			name:     "uppercase to lowercase",
			input:    "MyService",
			expected: "myservice",
		},
		{
			name:     "special characters",
			input:    "my service!",
			expected: "my-service",
		},
		{
			name:              "very long name uses hash",
			input:             "this-is-a-very-long-service-name-that-exceeds-the-63-character-limit-for-incus-instances",
			checkHashFallback: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := SanitizeIncusName(tt.input, -1)

			if tt.checkHashFallback {
				require.Len(t, result, 32)
				require.Regexp(t, "^[0-9a-f]{32}$", result)
			} else {
				require.Equal(t, tt.expected, result)
			}
			require.LessOrEqual(t, len(result), MaxIncusNameLen)
		})
	}
}
