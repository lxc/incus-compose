package icclient

import (
	"strings"
	"testing"

	cTypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestServiceOrder(t *testing.T) {
	tests := []struct {
		name        string
		project     *Project
		reverse     bool
		expected    []string
		shouldError bool
		description string
	}{
		{
			name: "simple linear dependencies - stop order",
			project: &Project{
				Services: map[string]ServiceConfig{
					"db": {},
					"api": {
						DependsOn: DependsOnConfig{
							"db": {},
						},
					},
					"web": {
						DependsOn: DependsOnConfig{
							"api": {},
						},
					},
				},
			},
			reverse:     false,
			expected:    []string{"db", "api", "web"},
			description: "Default topological sort: dependencies first (stop order)",
		},
		{
			name: "simple linear dependencies - start order reversed",
			project: &Project{
				Services: map[string]ServiceConfig{
					"db": {},
					"api": {
						DependsOn: DependsOnConfig{
							"db": {},
						},
					},
					"web": {
						DependsOn: DependsOnConfig{
							"api": {},
						},
					},
				},
			},
			reverse:     true,
			expected:    []string{"web", "api", "db"},
			description: "Reversed: dependents first (start order)",
		},
		{
			name: "multiple dependencies",
			project: &Project{
				Services: map[string]ServiceConfig{
					"db":    {},
					"cache": {},
					"api": {
						DependsOn: DependsOnConfig{
							"db":    {},
							"cache": {},
						},
					},
				},
			},
			reverse:     false,
			description: "Service with multiple dependencies",
		},
		{
			name: "no dependencies",
			project: &Project{
				Services: map[string]ServiceConfig{
					"web": {},
					"api": {},
					"db":  {},
				},
			},
			reverse:     false,
			description: "Services without dependencies",
		},
		{
			name: "circular dependency",
			project: &Project{
				Services: map[string]ServiceConfig{
					"a": {
						DependsOn: DependsOnConfig{
							"b": {},
						},
					},
					"b": {
						DependsOn: DependsOnConfig{
							"c": {},
						},
					},
					"c": {
						DependsOn: DependsOnConfig{
							"a": {},
						},
					},
				},
			},
			reverse:     true,
			shouldError: true,
			description: "Circular dependencies should error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ServiceOrder(tt.project, tt.reverse)

			if tt.shouldError {
				require.Error(t, err, "Expected error for circular dependencies")
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)

			// Check that all services are included
			assert.Len(t, result, len(tt.project.Services))

			// If expected order is specified, check it
			if tt.expected != nil {
				assert.Equal(t, tt.expected, result)
			}

			// For dependency tests, verify dependencies come before dependents (if reverse=false)
			// or dependents come before dependencies (if reverse=true)
			if !tt.shouldError {
				servicePos := make(map[string]int)
				for i, svc := range result {
					servicePos[svc] = i
				}

				for svcName, svcCfg := range tt.project.Services {
					for depName := range svcCfg.DependsOn {
						depPos, depExists := servicePos[depName]
						svcPos, svcExists := servicePos[svcName]
						require.True(t, depExists, "Dependency %s not found in result", depName)
						require.True(t, svcExists, "Service %s not found in result", svcName)

						if tt.reverse {
							// reverse=true: dependents first (for stop order)
							assert.Greater(t, depPos, svcPos, "Dependency %s should come after %s when reversed", depName, svcName)
						} else {
							// reverse=false: dependencies first (for start order)
							assert.Less(t, depPos, svcPos, "Dependency %s should come before %s", depName, svcName)
						}
					}
				}
			}
		})
	}
}

func TestParseDockerRef(t *testing.T) {
	tests := []struct {
		name        string
		serviceName string
		image       string
		shouldError bool
		description string
	}{
		{
			name:        "simple image",
			serviceName: "web",
			image:       "nginx",
			shouldError: false,
			description: "Simple image name should parse",
		},
		{
			name:        "image with tag",
			serviceName: "web",
			image:       "nginx:1.21",
			shouldError: false,
			description: "Image with tag should parse",
		},
		{
			name:        "image with registry",
			serviceName: "web",
			image:       "docker.io/library/nginx:latest",
			shouldError: false,
			description: "Full reference with registry should parse",
		},
		{
			name:        "image with digest",
			serviceName: "web",
			image:       "nginx@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			shouldError: false,
			description: "Image with digest should parse",
		},
		{
			name:        "invalid image",
			serviceName: "web",
			image:       "INVALID::IMAGE",
			shouldError: true,
			description: "Invalid image reference should error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseDockerRef(tt.serviceName, tt.image)

			if tt.shouldError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.serviceName)
				assert.Contains(t, err.Error(), tt.image)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
			}
		})
	}
}
