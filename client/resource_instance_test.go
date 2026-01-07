package client

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

// InstanceSecretSuite tests InstanceSecret configuration and defaults.
type InstanceSecretSuite struct {
	suite.Suite
}

func TestInstanceSecretSuite(t *testing.T) {
	suite.Run(t, new(InstanceSecretSuite))
}

func (s *InstanceSecretSuite) TestInstanceSecret_Defaults() {
	secret := InstanceSecret{
		Source:  "db_password",
		Content: []byte("secret-value"),
	}

	s.Equal("db_password", secret.Source)
	s.Equal([]byte("secret-value"), secret.Content)
	s.Empty(secret.Target, "Target should default to empty (handled in PushSecrets)")
	s.Zero(secret.UID)
	s.Zero(secret.GID)
	s.Zero(secret.Mode, "Mode should default to zero (handled in PushSecrets)")
}

func (s *InstanceSecretSuite) TestInstanceSecret_CustomTarget() {
	secret := InstanceSecret{
		Source:  "api_key",
		Target:  "/app/secrets/api.key",
		Content: []byte("my-api-key"),
		UID:     1000,
		GID:     1000,
		Mode:    0o440,
	}

	s.Equal("api_key", secret.Source)
	s.Equal("/app/secrets/api.key", secret.Target)
	s.Equal([]byte("my-api-key"), secret.Content)
	s.Equal(int64(1000), secret.UID)
	s.Equal(int64(1000), secret.GID)
	s.Equal(0o440, secret.Mode)
}

func (s *InstanceSecretSuite) TestInstanceConfig_WithSecrets() {
	secrets := []InstanceSecret{
		{Source: "db_password", Content: []byte("pass1")},
		{Source: "api_key", Target: "/custom/path", Content: []byte("key1"), Mode: 0o440},
	}

	config := InstanceConfig{
		Image:   "docker.io/alpine:latest",
		Secrets: secrets,
	}

	s.Len(config.Secrets, 2)
	s.Equal("db_password", config.Secrets[0].Source)
	s.Equal("api_key", config.Secrets[1].Source)
	s.Equal("/custom/path", config.Secrets[1].Target)
}

func TestSanitizeInstanceName(t *testing.T) {
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
			result := sanitizeInstanceName(tt.input)

			if tt.checkHashFallback {
				// Verify it's a valid 32-char hex string
				assert.Len(t, result, 32)
				assert.Regexp(t, "^[0-9a-f]{32}$", result)
			} else {
				assert.Equal(t, tt.expected, result)
			}
			assert.LessOrEqual(t, len(result), maxInstanceNameLen)
		})
	}
}
