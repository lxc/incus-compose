package project_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bradleyjkemp/cupaloy/v2"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"

	"github.com/lxc/incus-compose/project"
)

// ConfigTestCase represents a single config snapshot test case.
type ConfigTestCase struct {
	Name     string
	Fixture  string
	Format   string
	Services []string
	Profiles []string
	EnvFiles []string
}

// newSnapshotter returns a cupaloy config writing to the project snapshot dir.
func newSnapshotter() *cupaloy.Config {
	return cupaloy.New(cupaloy.SnapshotSubdirectory(filepath.Join("..", "test", "snapshots", "project")))
}

func runConfigTest(t *testing.T, tc ConfigTestCase) {
	t.Helper()

	t.Run(tc.Name, func(t *testing.T) {
		t.Parallel()

		fixture := fixturePath(tc.Fixture)

		loadOpts := []project.LoadOption{
			project.LoadWorkingDir(fixture),
		}

		if _, err := os.Stat(filepath.Join(fixture, "compose.incus.yaml")); err == nil {
			loadOpts = append(loadOpts, project.LoadFiles([]string{filepath.Join(fixture, "compose.yaml"), filepath.Join(fixture, "compose.incus.yaml")}))
		}

		if len(tc.Profiles) > 0 {
			loadOpts = append(loadOpts, project.LoadProfiles(tc.Profiles))
		}

		if len(tc.EnvFiles) > 0 {
			absEnvFiles := make([]string, len(tc.EnvFiles))
			for i, f := range tc.EnvFiles {
				absEnvFiles[i] = filepath.Join(fixture, f)
			}
			loadOpts = append(loadOpts, project.LoadEnvFiles(absEnvFiles))
		}

		proj, err := project.New().Load(context.Background(), loadOpts...)
		require.NoError(t, err)

		// Filter services if specified.
		if len(tc.Services) > 0 {
			keep := make(map[string]bool)
			for _, name := range tc.Services {
				if svc, ok := proj.Services[name]; ok {
					keep[name] = true
					for depName := range svc.DependsOn {
						if _, ok := proj.Services[depName]; ok {
							keep[depName] = true
						}
					}
				}
			}
			// Rebuild services map with only filtered services.
			for name := range proj.Services {
				if !keep[name] {
					delete(proj.Services, name)
				}
			}
		}

		var output string
		format := tc.Format
		if format == "" {
			format = "yaml"
		}

		switch format {
		case "json":
			var buf bytes.Buffer
			encoder := json.NewEncoder(&buf)
			encoder.SetIndent("", "  ")
			require.NoError(t, encoder.Encode(proj.Project))
			output = strings.TrimSuffix(buf.String(), "\n")
		case "yaml":
			var buf bytes.Buffer
			encoder := yaml.NewEncoder(&buf)
			encoder.SetIndent(2)
			require.NoError(t, encoder.Encode(proj.Project))
			require.NoError(t, encoder.Close())
			output = strings.TrimSuffix(buf.String(), "\n")
		default:
			t.Fatalf("unsupported format: %s", format)
		}

		// Normalize paths for portability.
		absFixturePath, _ := filepath.Abs(fixture)
		output = strings.ReplaceAll(output, absFixturePath, "$FIXTURE_PATH")

		newSnapshotter().SnapshotT(t, output)
	})
}

func TestConfigSnapshots(t *testing.T) {
	t.Parallel()

	testCases := []ConfigTestCase{
		{
			Name:    "simple-nginx_yaml",
			Fixture: "simple-nginx",
		},
		{
			Name:    "simple-nginx_json",
			Fixture: "simple-nginx",
			Format:  "json",
		},
		{
			Name:    "test-external-network_yaml",
			Fixture: "test-external-network",
		},
		{
			Name:    "grafana_yaml",
			Fixture: "grafana",
		},
		{
			Name:    "wordpress_yaml",
			Fixture: "wordpress",
		},
		{
			Name:     "wordpress_filter_by_service",
			Fixture:  "wordpress",
			Services: []string{"wordpress"},
		},
		{
			Name:    "nginx-proxy_yaml",
			Fixture: "nginx-proxy",
		},
		{
			Name:    "nginx-scale_yaml",
			Fixture: "nginx-scale",
		},
		{
			Name:    "dev-environment_yaml",
			Fixture: "dev-environment",
		},
		{
			Name:    "with-tmpfs_yaml",
			Fixture: "with-tmpfs",
		},
		{
			Name:    "with-resources_yaml",
			Fixture: "with-resources",
		},
		{
			Name:    "with-network-ranges_yaml",
			Fixture: "with-network-ranges",
		},
		{
			Name:    "with-nat-proxy_yaml",
			Fixture: "with-nat-proxy",
		},
		{
			Name:    "with-build_yaml",
			Fixture: "with-build",
		},
		{
			Name:    "with-container-name_yaml",
			Fixture: "with-container-name",
		},
		{
			Name:    "with-shm-size_yaml",
			Fixture: "with-shm-size",
		},
	}

	for _, tc := range testCases {
		runConfigTest(t, tc)
	}
}

func TestConfigSnapshotsWithProfiles(t *testing.T) {
	t.Parallel()

	testCases := []ConfigTestCase{
		{
			Name:     "with-profiles_dev_profile",
			Fixture:  "with-profiles",
			Profiles: []string{"dev"},
		},
		{
			Name:     "with-profiles_monitoring_profile",
			Fixture:  "with-profiles",
			Profiles: []string{"monitoring"},
		},
		{
			Name:     "with-profiles_dev_and_monitoring",
			Fixture:  "with-profiles",
			Profiles: []string{"dev", "monitoring"},
		},
		{
			Name:     "dev-environment_debug_profile",
			Fixture:  "dev-environment",
			Profiles: []string{"debug"},
		},
	}

	for _, tc := range testCases {
		runConfigTest(t, tc)
	}
}

func TestConfigSnapshotsWithEnv(t *testing.T) {
	t.Parallel()

	testCases := []ConfigTestCase{
		{
			Name:    "with-env_default_yaml",
			Fixture: "with-env",
		},
		{
			Name:     "with-env_production_yaml",
			Fixture:  "with-env",
			EnvFiles: []string{"production.env"},
		},
		{
			Name:     "with-env_staging_yaml",
			Fixture:  "with-env",
			EnvFiles: []string{"staging.env"},
		},
	}

	for _, tc := range testCases {
		runConfigTest(t, tc)
	}
}
