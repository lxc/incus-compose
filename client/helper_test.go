package client

import (
	"strings"
	"testing"

	cTypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"
)

// Type aliases for test readability
type (
	DependsOnConfig   = cTypes.DependsOnConfig
	ServiceDependency = cTypes.ServiceDependency
)

func TestSanitizeProjectName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple name",
			input:    "myproject",
			expected: "myproject",
		},
		{
			name:     "underscore replacement",
			input:    "my_project",
			expected: "my-project",
		},
		{
			name:     "uppercase to lowercase",
			input:    "MyProject",
			expected: "myproject",
		},
		{
			name:     "special characters",
			input:    "my project!",
			expected: "my-project",
		},
		{
			name:     "quotes removed",
			input:    `my"project"`,
			expected: "myproject",
		},
		{
			name:     "multiple special chars",
			input:    "my__project--name",
			expected: "my--project-name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeProjectName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
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

func TestNetworkNameForProject(t *testing.T) {
	tests := []struct {
		name        string
		projectName string
		prefix      string
		networkName string
		description string
	}{
		{
			name:        "short name no project",
			projectName: "",
			prefix:      "",
			networkName: "web",
			description: "Short names should pass through",
		},
		{
			name:        "short name with project",
			projectName: "test",
			prefix:      "",
			networkName: "web",
			description: "test-web should fit in 13 chars",
		},
		{
			name:        "long name gets hashed",
			projectName: "verylongproject",
			prefix:      "ic-",
			networkName: "verylongnetwork",
			description: "Long names should get ic-{hash10} format",
		},
		{
			name:        "underscore replacement",
			projectName: "my_project",
			prefix:      "",
			networkName: "my_net",
			description: "Underscores should become hyphens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := networkNameForProject(tt.projectName, tt.prefix, tt.networkName)

			// All results should fit within maxInterfaceNameLen
			assert.LessOrEqual(t, len(result), maxInterfaceNameLen, "Network name too long")

			// Hash-based names should have the ic- prefix
			if len(tt.projectName+"-"+tt.networkName) > maxInterfaceNameLen && tt.prefix != "" {
				assert.True(t, strings.HasPrefix(result, tt.prefix))
				assert.Len(t, result, len(tt.prefix)+networkNameHashLen) // prefix + 10 char hash
			}

			// Should not contain underscores
			assert.NotContains(t, result, "_")

			// Determinism: same input should produce same output
			result2 := networkNameForProject(tt.projectName, tt.prefix, tt.networkName)
			assert.Equal(t, result, result2, "Network name generation should be deterministic")
		})
	}
}

func TestShortNetworkName(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		full   string
	}{
		{
			name:   "basic hash generation",
			prefix: "ic-",
			full:   "very-long-project-name-that-needs-hashing",
		},
		{
			name:   "deterministic output",
			prefix: "ic-",
			full:   "myproject-mynetwork",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shortNetworkName(tt.prefix, tt.full)

			// Should have the right format
			assert.True(t, strings.HasPrefix(result, tt.prefix))
			assert.Len(t, result, len(tt.prefix)+networkNameHashLen)

			// Should be lowercase
			assert.Equal(t, result, strings.ToLower(result))

			// Should be deterministic
			result2 := shortNetworkName(tt.prefix, tt.full)
			assert.Equal(t, result, result2)

			// Different inputs should produce different outputs
			result3 := shortNetworkName(tt.prefix, tt.full+"different")
			assert.NotEqual(t, result, result3)
		})
	}
}
