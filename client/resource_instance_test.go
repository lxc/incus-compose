package client

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
