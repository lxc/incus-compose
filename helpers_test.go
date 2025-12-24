package incuscompose

import (
	"strings"
	"testing"

	cTypes "github.com/compose-spec/compose-go/v2/types"
)

func TestNetworkNameForProject_ShortNames(t *testing.T) {
	tests := []struct {
		name        string
		project     string
		network     string
		expected    string
		description string
	}{
		{
			name:        "very short",
			project:     "app",
			network:     "web",
			expected:    "app-web",
			description: "7 chars - should use full name",
		},
		{
			name:        "medium length",
			project:     "myapp",
			network:     "default",
			expected:    "myapp-default",
			description: "13 chars - should use full name (at limit)",
		},
		{
			name:        "single char names",
			project:     "a",
			network:     "b",
			expected:    "a-b",
			description: "3 chars - should use full name",
		},
		{
			name:        "exactly at limit",
			project:     "proj",
			network:     "network1",
			expected:    "proj-network1",
			description: "13 chars - exactly at MaxInterfaceNameLen",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := networkNameForProject(tt.project, tt.network)
			if result != tt.expected {
				t.Errorf("networkNameForProject(%q, %q) = %q, want %q (%s)",
					tt.project, tt.network, result, tt.expected, tt.description)
			}
			if len(result) > maxInterfaceNameLen {
				t.Errorf("result length %d exceeds MaxInterfaceNameLen %d",
					len(result), maxInterfaceNameLen)
			}
		})
	}
}

func TestNetworkNameForProject_LongNames(t *testing.T) {
	tests := []struct {
		name    string
		project string
		network string
	}{
		{
			name:    "one char over limit",
			project: "proj",
			network: "network12",
		},
		{
			name:    "typical long project",
			project: "myproject",
			network: "backend",
		},
		{
			name:    "very long names",
			project: "my-very-long-project-name",
			network: "my-very-long-network-name",
		},
		{
			name:    "long project short network",
			project: "superlongprojectname",
			network: "db",
		},
		{
			name:    "short project long network",
			project: "app",
			network: "verylongnetworkname",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := networkNameForProject(tt.project, tt.network)

			// Must fit within limit
			if len(result) > maxInterfaceNameLen {
				t.Errorf("result %q length %d exceeds MaxInterfaceNameLen %d",
					result, len(result), maxInterfaceNameLen)
			}

			// Must have the ic- prefix for hashed names
			if result[:3] != "ic-" {
				t.Errorf("hashed name %q should start with 'ic-'", result)
			}

			// Must be exactly 13 chars (3 prefix + 10 hash)
			if len(result) != 13 {
				t.Errorf("hashed name %q should be exactly 13 chars, got %d",
					result, len(result))
			}
		})
	}
}

func TestNetworkNameForProject_Deterministic(t *testing.T) {
	// Same inputs should always produce the same output
	testCases := []struct {
		project string
		network string
	}{
		{"myproject", "backend"},
		{"app", "database"},
		{"long-project-name", "long-network-name"},
	}

	for _, tc := range testCases {
		t.Run(tc.project+"-"+tc.network, func(t *testing.T) {
			result1 := networkNameForProject(tc.project, tc.network)
			result2 := networkNameForProject(tc.project, tc.network)
			result3 := networkNameForProject(tc.project, tc.network)

			if result1 != result2 || result2 != result3 {
				t.Errorf("networkNameForProject is not deterministic: got %q, %q, %q",
					result1, result2, result3)
			}
		})
	}
}

func TestNetworkNameForProject_Sanitization(t *testing.T) {
	// Network names must not contain underscores (invalid for interface names)
	tests := []struct {
		name     string
		project  string
		network  string
		expected string
	}{
		{
			name:     "underscore in project",
			project:  "h_w",
			network:  "net",
			expected: "h-w-net", // 7 chars, fits in 13
		},
		{
			name:     "underscore in network",
			project:  "app",
			network:  "my_net",
			expected: "app-my-net",
		},
		{
			name:     "underscores in both short",
			project:  "a_b",
			network:  "c_d",
			expected: "a-b-c-d",
		},
		{
			name:    "long name with underscores gets hashed",
			project: "my_long_project",
			network: "my_long_network",
			// This is > 13 chars so gets hashed, but we verify no underscores
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := networkNameForProject(tt.project, tt.network)
			if tt.expected != "" && result != tt.expected {
				t.Errorf("networkNameForProject(%q, %q) = %q, want %q",
					tt.project, tt.network, result, tt.expected)
			}
			// Verify no underscores in result
			if strings.Contains(result, "_") {
				t.Errorf("network name %q contains underscore, which is invalid", result)
			}
		})
	}
}

func TestNetworkNameForProject_Uniqueness(t *testing.T) {
	// Different inputs should produce different outputs
	// This tests the old truncation bug where these would collide
	testCases := []struct {
		name         string
		inputs       [][2]string
		shouldDiffer bool
	}{
		{
			name: "networks that would collide with truncation",
			inputs: [][2]string{
				{"myproject", "network1"},
				{"myproject", "network2"},
			},
			shouldDiffer: true,
		},
		{
			name: "similar long names",
			inputs: [][2]string{
				{"production", "api-gateway"},
				{"production", "api-backend"},
			},
			shouldDiffer: true,
		},
		{
			name: "same network different projects",
			inputs: [][2]string{
				{"project-a", "default"},
				{"project-b", "default"},
			},
			shouldDiffer: true,
		},
		{
			name: "prefix similarity",
			inputs: [][2]string{
				{"myproject-abc", "net"},
				{"myproject-abd", "net"},
			},
			shouldDiffer: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(map[string]string) // result -> input description

			for _, input := range tc.inputs {
				result := networkNameForProject(input[0], input[1])
				inputDesc := input[0] + "-" + input[1]

				if existing, ok := seen[result]; ok && tc.shouldDiffer {
					t.Errorf("collision detected: %q and %q both produce %q",
						existing, inputDesc, result)
				}
				seen[result] = inputDesc
			}
		})
	}
}

func TestNetworkNameForProject_ValidCharacters(t *testing.T) {
	// All generated names should only contain valid interface name characters
	testCases := []struct {
		project string
		network string
	}{
		{"myproject", "backend"},
		{"test-project", "test-network"},
		{"a1b2c3", "x9y8z7"},
	}

	isValidChar := func(c rune) bool {
		return (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_'
	}

	for _, tc := range testCases {
		result := networkNameForProject(tc.project, tc.network)
		for i, c := range result {
			if !isValidChar(c) {
				t.Errorf("invalid character %q at position %d in %q",
					c, i, result)
			}
		}
	}
}

func TestShortNetworkName(t *testing.T) {
	tests := []struct {
		input string
	}{
		{"myproject-backend"},
		{"a-b"},
		{"very-long-project-name-with-very-long-network-name"},
		{"special-chars-123"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := shortNetworkName(tt.input)

			// Check format: ic-{10 chars}
			if len(result) != 13 {
				t.Errorf("shortNetworkName(%q) = %q, want 13 chars, got %d",
					tt.input, result, len(result))
			}

			if result[:3] != "ic-" {
				t.Errorf("shortNetworkName(%q) = %q, should start with 'ic-'",
					tt.input, result)
			}

			// Hash part should be lowercase alphanumeric (base32)
			// base32 uses a-z and 2-7
			hashPart := result[3:]
			for _, c := range hashPart {
				isLowerAlpha := c >= 'a' && c <= 'z'
				isBase32Digit := c >= '2' && c <= '7'
				if !isLowerAlpha && !isBase32Digit {
					t.Errorf("invalid base32 character %q in hash part of %q",
						c, result)
				}
			}
		})
	}
}

func TestShortNetworkName_Deterministic(t *testing.T) {
	input := "myproject-backend"
	result1 := shortNetworkName(input)
	result2 := shortNetworkName(input)

	if result1 != result2 {
		t.Errorf("shortNetworkName is not deterministic: %q vs %q", result1, result2)
	}
}

func TestVolumeNameForProject(t *testing.T) {
	tests := []struct {
		project  string
		volume   string
		expected string
	}{
		{"myapp", "data", "myapp-data"},
		{"project", "logs", "project-logs"},
		{"a", "b", "a-b"},
	}

	for _, tt := range tests {
		t.Run(tt.project+"-"+tt.volume, func(t *testing.T) {
			result := volumeNameForProject(tt.project, tt.volume)
			if result != tt.expected {
				t.Errorf("volumeNameForProject(%q, %q) = %q, want %q",
					tt.project, tt.volume, result, tt.expected)
			}
		})
	}
}

// TestSanitizeProjectName tests project name sanitization.
func TestSanitizeProjectName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "already valid",
			input:    "myproject",
			expected: "myproject",
		},
		{
			name:     "underscore to hyphen",
			input:    "hello_world",
			expected: "hello-world",
		},
		{
			name:     "multiple underscores",
			input:    "my_project_name",
			expected: "my-project-name",
		},
		{
			name:     "uppercase to lowercase",
			input:    "MyProject",
			expected: "myproject",
		},
		{
			name:     "mixed case with underscores",
			input:    "My_Project_Name",
			expected: "my-project-name",
		},
		{
			name:     "quotes removed",
			input:    "project'name",
			expected: "projectname",
		},
		{
			name:     "double quotes removed",
			input:    `project"name`,
			expected: "projectname",
		},
		{
			name:     "spaces to hyphens",
			input:    "my project",
			expected: "my-project",
		},
		{
			name:     "complex name",
			input:    "My_Complex 'Project\"Name",
			expected: "my-complex-projectname",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := sanitizeProjectName(tc.input)
			if result != tc.expected {
				t.Errorf("sanitizeProjectName(%q) = %q, want %q",
					tc.input, result, tc.expected)
			}
		})
	}
}

func BenchmarkNetworkNameForProject_Short(b *testing.B) {
	for i := 0; i < b.N; i++ {
		networkNameForProject("app", "web")
	}
}

func BenchmarkNetworkNameForProject_Long(b *testing.B) {
	for i := 0; i < b.N; i++ {
		networkNameForProject("my-long-project-name", "my-long-network-name")
	}
}

func BenchmarkShortNetworkName(b *testing.B) {
	input := "myproject-backend"
	for i := 0; i < b.N; i++ {
		shortNetworkName(input)
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "already valid",
			input:    "my-container",
			expected: "my-container",
		},
		{
			name:     "underscore to hyphen",
			input:    "hello_world",
			expected: "hello-world",
		},
		{
			name:     "multiple underscores",
			input:    "my_project_service",
			expected: "my-project-service",
		},
		{
			name:     "uppercase to lowercase",
			input:    "MyContainer",
			expected: "mycontainer",
		},
		{
			name:     "mixed case with underscores",
			input:    "My_Project_Service",
			expected: "my-project-service",
		},
		{
			name:     "spaces to hyphens",
			input:    "my container",
			expected: "my-container",
		},
		{
			name:     "dots handled",
			input:    "my.container.name",
			expected: "my-container-name",
		},
		{
			name:     "numbers preserved",
			input:    "service123",
			expected: "service123",
		},
		{
			name:     "complex name",
			input:    "My_Project-Service.v2",
			expected: "my-project-service-v2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeName(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeName(%q) = %q, want %q",
					tt.input, result, tt.expected)
			}
		})
	}
}

func TestSanitizeName_LongNames(t *testing.T) {
	// Very long names should be hashed
	longName := "this-is-a-very-long-container-name-that-exceeds-the-sixty-three-character-limit-for-incus"

	result := sanitizeName(longName)

	if len(result) > maxContainerNameLen {
		t.Errorf("sanitizeName should truncate long names, got length %d", len(result))
	}

	// Should be a hex hash (32 chars)
	if len(result) != 32 {
		t.Errorf("expected 32 char hex hash for long name, got %d chars: %q", len(result), result)
	}

	// Should be deterministic
	result2 := sanitizeName(longName)
	if result != result2 {
		t.Errorf("sanitizeName is not deterministic for long names")
	}
}

func TestContainerNameForService_Sanitization(t *testing.T) {
	tests := []struct {
		name     string
		service  *cTypes.ServiceConfig
		expected string
	}{
		{
			name:     "underscore in service name",
			service:  &cTypes.ServiceConfig{Name: "hello_world", ContainerName: ""},
			expected: "hello-world",
		},
		{
			name:     "explicit container name with underscore",
			service:  &cTypes.ServiceConfig{Name: "custom", ContainerName: "custom-container"},
			expected: "custom-container",
		},
		{
			name:     "already valid name",
			service:  &cTypes.ServiceConfig{Name: "web", ContainerName: ""},
			expected: "web",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containerNameForService(tt.service)
			if result != tt.expected {
				t.Errorf("containerNameForService(%q, {ContainerName: %q}) = %q, want %q",
					tt.service.Name, tt.service.ContainerName, result, tt.expected)
			}
		})
	}
}
