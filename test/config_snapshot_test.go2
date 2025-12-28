package test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bradleyjkemp/cupaloy/v2"
	"github.com/stretchr/testify/require"

	"gitlab.com/r3j0/incuscompose"
)

// ConfigOptions holds all possible configuration options for testing.
type ConfigOptions struct {
	Format   string
	Services []string
	Profiles []string
	EnvFiles []string
}

// ConfigTestCase represents a single test case.
type ConfigTestCase struct {
	Name    string
	Fixture string
	Options *ConfigOptions
}

// runConfigTest executes a single test case with incus-compose.
func runConfigTest(t *testing.T, testCase ConfigTestCase) {
	t.Run(testCase.Name, func(t *testing.T) {
		ctx := context.Background()

		// Get absolute path to fixtures
		cwd, err := os.Getwd()
		require.NoError(t, err)
		fixturePath := filepath.Join(cwd, "fixtures", testCase.Fixture)

		// Load project using the library
		loadOpts := []incuscompose.LoadProjectOption{
			incuscompose.LoadProjectWorkingDir(fixturePath),
		}

		if len(testCase.Options.Profiles) > 0 {
			loadOpts = append(loadOpts, incuscompose.LoadProjectProfiles(testCase.Options.Profiles))
		}

		if len(testCase.Options.EnvFiles) > 0 {
			// Convert relative paths to absolute
			absEnvFiles := make([]string, len(testCase.Options.EnvFiles))
			for i, f := range testCase.Options.EnvFiles {
				absEnvFiles[i] = filepath.Join(fixturePath, f)
			}
			loadOpts = append(loadOpts, incuscompose.LoadProjectEnvFiles(absEnvFiles))
		}

		project, err := incuscompose.LoadProject(ctx, loadOpts...)
		require.NoError(t, err)

		// Build config options
		var configOpts []incuscompose.ConfigOption
		if testCase.Options.Format != "" {
			configOpts = append(configOpts, incuscompose.ConfigFormat(testCase.Options.Format))
		}
		if len(testCase.Options.Services) > 0 {
			configOpts = append(configOpts, incuscompose.ConfigServices(testCase.Options.Services))
		}

		// Capture output
		tmpFile, err := os.CreateTemp("", "config-output-*.txt")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())
		defer tmpFile.Close()

		configOpts = append(configOpts, incuscompose.ConfigOutput(tmpFile.Name()))

		err = incuscompose.Config(ctx, project, configOpts...)
		require.NoError(t, err)

		_, _ = tmpFile.Seek(0, io.SeekStart)
		var buf bytes.Buffer
		_, err = io.Copy(&buf, tmpFile)
		require.NoError(t, err)

		output := strings.ReplaceAll(buf.String(), fixturePath, "$FIXTURE_PATH")

		// Snapshot the output
		snapshotter := cupaloy.New(cupaloy.SnapshotSubdirectory("snapshots"))
		snapshotter.SnapshotT(t, output)
	})
}

// TestConfigSnapshots tests config command output against saved snapshots.
func TestConfigSnapshots(t *testing.T) {
	testCases := []ConfigTestCase{
		{
			Name:    "hello_world_yaml",
			Fixture: "hello_world",
			Options: &ConfigOptions{},
		},
		{
			Name:    "hello_world_json",
			Fixture: "hello_world",
			Options: &ConfigOptions{Format: "json"},
		},
		{
			Name:    "wordpress_yaml",
			Fixture: "wordpress",
			Options: &ConfigOptions{},
		},
		{
			Name:    "wordpress_json",
			Fixture: "wordpress",
			Options: &ConfigOptions{Format: "json"},
		},
		{
			Name:    "wordpress_filter_by_service",
			Fixture: "wordpress",
			Options: &ConfigOptions{Services: []string{"wordpress"}},
		},
		{
			Name:    "nginx_proxy_yaml",
			Fixture: "nginx_proxy",
			Options: &ConfigOptions{},
		},
		{
			Name:    "dev_environment_yaml",
			Fixture: "dev_environment",
			Options: &ConfigOptions{},
		},
	}

	for _, testCase := range testCases {
		runConfigTest(t, testCase)
	}
}

// TestConfigSnapshotsWithProfiles tests config output with different profile combinations.
func TestConfigSnapshotsWithProfiles(t *testing.T) {
	testCases := []ConfigTestCase{
		{
			Name:    "with_profiles_dev_profile",
			Fixture: "with_profiles",
			Options: &ConfigOptions{
				Profiles: []string{"dev"},
			},
		},
		{
			Name:    "with_profiles_monitoring_profile",
			Fixture: "with_profiles",
			Options: &ConfigOptions{
				Profiles: []string{"monitoring"},
			},
		},
		{
			Name:    "with_profiles_dev_and_monitoring",
			Fixture: "with_profiles",
			Options: &ConfigOptions{
				Profiles: []string{"dev", "monitoring"},
			},
		},
		{
			Name:    "dev_environment_debug_profile",
			Fixture: "dev_environment",
			Options: &ConfigOptions{
				Profiles: []string{"debug"},
			},
		},
		{
			Name:    "dev_environment_debug_profile_yaml",
			Fixture: "dev_environment",
			Options: &ConfigOptions{
				Profiles: []string{"debug"},
			},
		},
	}

	for _, testCase := range testCases {
		runConfigTest(t, testCase)
	}
}

// TestConfigSnapshotsWithEnv tests config output with different environment files.
func TestConfigSnapshotsWithEnv(t *testing.T) {
	testCases := []ConfigTestCase{
		{
			Name:    "with_env_default_yaml",
			Fixture: "with_env",
			Options: &ConfigOptions{}, // Use default .env
		},
		{
			Name:    "with_env_production_yaml",
			Fixture: "with_env",
			Options: &ConfigOptions{
				EnvFiles: []string{"production.env"},
			},
		},
		{
			Name:    "with_env_staging_yaml",
			Fixture: "with_env",
			Options: &ConfigOptions{
				EnvFiles: []string{"staging.env"},
			},
		},
	}

	for _, testCase := range testCases {
		runConfigTest(t, testCase)
	}
}

// TestConfigSnapshotsCi runs tests only for incus-compose in CI environments.
func TestConfigSnapshotsCi(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("This test only runs in a CI environment")
	}

	testCases := []ConfigTestCase{
		{
			Name:    "hello_world_yaml",
			Fixture: "hello_world",
			Options: &ConfigOptions{},
		},
		{
			Name:    "wordpress_yaml",
			Fixture: "wordpress",
			Options: &ConfigOptions{},
		},
	}

	for _, testCase := range testCases {
		runConfigTest(t, testCase)
	}
}
