package project_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bradleyjkemp/cupaloy/v2"
	"github.com/stretchr/testify/suite"
	"go.yaml.in/yaml/v4"

	"gitlab.com/r3j0/incus-compose/project"
)

// ConfigSnapshotSuite tests config output against saved snapshots.
type ConfigSnapshotSuite struct {
	suite.Suite
	ctx         context.Context
	fixturesDir string
	snapshotter *cupaloy.Config
}

// ConfigTestCase represents a single test case.
type ConfigTestCase struct {
	Name     string
	Fixture  string
	Format   string
	Services []string
	Profiles []string
	EnvFiles []string
}

func (s *ConfigSnapshotSuite) SetupSuite() {
	s.fixturesDir = filepath.Join("..", "test", "fixtures")
	s.snapshotter = cupaloy.New(cupaloy.SnapshotSubdirectory(filepath.Join("..", "test", "snapshots")))
}

func (s *ConfigSnapshotSuite) SetupTest() {
	s.ctx = context.Background()
}

func (s *ConfigSnapshotSuite) fixturePath(name string) string {
	return filepath.Join(s.fixturesDir, name)
}

func (s *ConfigSnapshotSuite) runConfigTest(tc ConfigTestCase) {
	s.Run(tc.Name, func() {
		fixturePath := s.fixturePath(tc.Fixture)

		loadOpts := []project.LoadOption{
			project.LoadWorkingDir(fixturePath),
		}

		if len(tc.Profiles) > 0 {
			loadOpts = append(loadOpts, project.LoadProfiles(tc.Profiles))
		}

		if len(tc.EnvFiles) > 0 {
			absEnvFiles := make([]string, len(tc.EnvFiles))
			for i, f := range tc.EnvFiles {
				absEnvFiles[i] = filepath.Join(fixturePath, f)
			}
			loadOpts = append(loadOpts, project.LoadEnvFiles(absEnvFiles))
		}

		proj, err := project.New().Load(s.ctx, loadOpts...)
		s.Require().NoError(err)

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
			err := encoder.Encode(proj.Project)
			s.Require().NoError(err)
			output = strings.TrimSuffix(buf.String(), "\n")
		case "yaml":
			var buf bytes.Buffer
			encoder := yaml.NewEncoder(&buf)
			encoder.SetIndent(2)
			err := encoder.Encode(proj.Project)
			s.Require().NoError(err)
			s.Require().NoError(encoder.Close())
			output = strings.TrimSuffix(buf.String(), "\n")
		default:
			s.Fail("unsupported format: %s", format)
		}

		// Normalize paths for portability.
		absFixturePath, _ := filepath.Abs(fixturePath)
		output = strings.ReplaceAll(output, absFixturePath, "$FIXTURE_PATH")

		s.snapshotter.SnapshotT(s.T(), output)
	})
}

func (s *ConfigSnapshotSuite) TestConfigSnapshots() {
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
			Name:    "grafana_yaml",
			Fixture: "grafana",
		},
		{
			Name:    "immich_yaml",
			Fixture: "immich",
		},
		{
			Name:    "immich_json",
			Fixture: "immich",
			Format:  "json",
		},
		{
			Name:    "wordpress_yaml",
			Fixture: "wordpress",
		},
		{
			Name:    "wordpress_json",
			Fixture: "wordpress",
			Format:  "json",
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
	}

	for _, tc := range testCases {
		s.runConfigTest(tc)
	}
}

func (s *ConfigSnapshotSuite) TestConfigSnapshotsWithProfiles() {
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
		s.runConfigTest(tc)
	}
}

func (s *ConfigSnapshotSuite) TestConfigSnapshotsWithEnv() {
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
		s.runConfigTest(tc)
	}
}

func TestConfigSnapshotSuite(t *testing.T) {
	suite.Run(t, new(ConfigSnapshotSuite))
}
