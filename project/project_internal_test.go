package project

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/suite"

	"github.com/lxc/incus-compose/client"
)

type ProjectInternalSuite struct {
	suite.Suite
	ctx         context.Context
	fixturesDir string
}

func (s *ProjectInternalSuite) SetupSuite() {
	s.fixturesDir = filepath.Join("..", "test", "fixtures")
}

func (s *ProjectInternalSuite) SetupTest() {
	s.ctx = context.Background()
}

func (s *ProjectInternalSuite) fixturePath(name string) string {
	return filepath.Join(s.fixturesDir, name)
}

func (s *ProjectInternalSuite) TestNewLoadOptionsAppliesOptions() {
	options := NewLoadOptions(
		LoadName("custom"),
		LoadFiles([]string{"compose.yaml", "compose.override.yaml"}),
		LoadWorkingDir("/tmp/project"),
		LoadEnvFiles([]string{".env", "prod.env"}),
		LoadProfiles([]string{"dev"}),
		LoadOsEnv(),
	)

	s.Equal("custom", options.Name)
	s.Equal([]string{"compose.yaml", "compose.override.yaml"}, options.Files)
	s.Equal("/tmp/project", options.WorkingDir)
	s.Equal([]string{".env", "prod.env"}, options.EnvFiles)
	s.True(options.OsEnv)
}

func (s *ProjectInternalSuite) TestFormatMemoryLimit() {
	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{name: "gib", bytes: 2 << 30, want: "2GiB"},
		{name: "mib", bytes: 512 << 20, want: "512MiB"},
		{name: "kib", bytes: 64 << 10, want: "64KiB"},
		{name: "bytes", bytes: 1537, want: "1537B"},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Equal(tt.want, formatMemoryLimit(tt.bytes))
		})
	}
}

func (s *ProjectInternalSuite) TestApplyRestartPolicy() {
	tests := []struct {
		name   string
		policy string
		want   map[string]string
	}{
		{name: "always", policy: "always", want: map[string]string{"boot.autostart": "true"}},
		{name: "on failure", policy: "on-failure", want: map[string]string{"boot.autostart": "true", "boot.autorestart": "true"}},
		{name: "unless stopped", policy: "unless-stopped", want: map[string]string{}},
		{name: "no", policy: "no", want: map[string]string{"boot.autostart": "false"}},
		{name: "default", policy: "", want: map[string]string{"boot.autostart": "false"}},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			config := map[string]string{}

			applyRestartPolicy(config, tt.policy)

			s.Equal(tt.want, config)
		})
	}
}

func (s *ProjectInternalSuite) TestFormatTmpfsSize() {
	s.Equal("", formatTmpfsSize(nil))
	s.Equal("", formatTmpfsSize(&types.ServiceVolumeTmpfs{}))
	s.Equal("4096", formatTmpfsSize(&types.ServiceVolumeTmpfs{Size: 4096}))
}

func (s *ProjectInternalSuite) TestParseSecretOwnershipAndMode() {
	mode := types.FileMode(0o440)

	s.Equal(int64(0), parseSecretUID(""))
	s.Equal(int64(1000), parseSecretUID("1000"))
	s.Equal(int64(0), parseSecretUID("not-a-number"))
	s.Equal(int64(0), parseSecretGID(""))
	s.Equal(int64(1001), parseSecretGID("1001"))
	s.Equal(int64(0), parseSecretGID("not-a-number"))
	s.Equal(0, parseSecretMode(nil))
	s.Equal(0o440, parseSecretMode(&mode))
}

func (s *ProjectInternalSuite) TestFormatCommand() {
	s.Equal("", formatCommand(nil))
	s.Equal("/bin/sh", formatCommand([]string{"/bin/sh"}))
	s.Equal("/bin/sh \"-c\" \"echo hello\"", formatCommand([]string{"/bin/sh", "-c", "echo hello"}))
}

func (s *ProjectInternalSuite) TestServiceGraphOrdersDependencies() {
	services := types.Services{
		"db":  {Name: "db"},
		"api": {Name: "api", DependsOn: types.DependsOnConfig{"db": types.ServiceDependency{}}},
		"web": {Name: "web", DependsOn: types.DependsOnConfig{"api": types.ServiceDependency{}}},
	}

	order, err := ServiceGraph(services, false)
	s.Require().NoError(err)
	s.Equal([]string{"db", "api", "web"}, order)

	reverse, err := ServiceGraph(services, true)
	s.Require().NoError(err)
	s.Equal([]string{"web", "api", "db"}, reverse)
}

func (s *ProjectInternalSuite) TestServiceGraphReturnsEdgeErrors() {
	// Cycles are still an error.
	_, err := ServiceGraph(types.Services{
		"api": {Name: "api", DependsOn: types.DependsOnConfig{"web": types.ServiceDependency{}}},
		"web": {Name: "web", DependsOn: types.DependsOnConfig{"api": types.ServiceDependency{}}},
	}, false)
	s.Error(err)
}

func (s *ProjectInternalSuite) TestServiceGraphSkipsMissingDependency() {
	// A dependency not present in the service set is silently skipped (filtered subset case).
	order, err := ServiceGraph(types.Services{
		"api": {Name: "api", DependsOn: types.DependsOnConfig{"db": types.ServiceDependency{}}},
	}, false)
	s.Require().NoError(err)
	s.Equal([]string{"api"}, order)
}

func (s *ProjectInternalSuite) TestProjectConfigExtractsXIncus() {
	proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("with-project-options")))
	s.Require().NoError(err)

	s.Equal(map[string]string{
		"limits.cpu":              "5",
		"limits.memory":           "2149MB",
		"limits.virtual-machines": "0",
	}, proj.ProjectConfig())
}

func (s *ProjectInternalSuite) TestProjectConfigReturnsNilWithoutExtensions() {
	s.Nil((*Project)(nil).ProjectConfig())
	s.Nil((&Project{}).ProjectConfig())

	proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("simple-nginx")))
	s.Require().NoError(err)
	s.Nil(proj.ProjectConfig())
}

func (s *ProjectInternalSuite) TestNetworkConfigExtractsXIncusCompose() {
	proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("with-network-config")))
	s.Require().NoError(err)

	project, profile, err := proj.NetworkConfig()
	s.Require().NoError(err)
	s.Equal("my-project", project)
	s.Equal("my-profile", profile)
}

func (s *ProjectInternalSuite) TestNetworkConfigReturnsEmptyWithoutExtension() {
	proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("simple-nginx")))
	s.Require().NoError(err)

	project, profile, err := proj.NetworkConfig()
	s.Require().NoError(err)
	s.Empty(project)
	s.Empty(profile)
}

func (s *ProjectInternalSuite) TestToStackKeepsNetworkResourcesWithoutNetworkProfile() {
	proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("simple-nginx")))
	s.Require().NoError(err)

	c := client.NewOfflineClient(s.ctx, proj.Name)
	stack := client.NewStack(c)
	s.Require().NoError(proj.ToStack(c, stack))

	var hasNetwork bool
	for _, resource := range stack.All() {
		if resource.Kind() == client.KindNetwork {
			hasNetwork = true
		}
	}
	s.True(hasNetwork)
}

func (s *ProjectInternalSuite) TestNetworkExtensionsExtractsXIncus() {
	proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("with-network-ranges")))
	s.Require().NoError(err)

	s.Equal(map[string]string{
		"ipv4.address":     "10.200.0.1/24",
		"ipv4.dhcp.ranges": "10.200.0.100-10.200.0.200",
		"ipv6.address":     "fd42:1::1/64",
	}, networkExtensions(proj.Networks["backend"]))
	s.Nil(networkExtensions(types.NetworkConfig{}))
}

func (s *ProjectInternalSuite) TestServiceXIncusExtensionsExtractsXIncus() {
	proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("with-incus-options")))
	s.Require().NoError(err)

	s.Equal(map[string]string{
		"limits.memory":    "1024MB",
		"limits.cpu":       "2",
		"security.nesting": "false",
	}, serviceXIncusExtensions(proj.Services["web"]))
	s.Nil(serviceXIncusExtensions(types.ServiceConfig{}))
}

func (s *ProjectInternalSuite) TestServiceXIncusComposeExtensionsExtractsXIncusCompose() {
	proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("with-nat-proxy")))
	s.Require().NoError(err)

	extensions := serviceXIncusComposeExtensions(proj.Services["web"])
	s.Require().Contains(extensions, "nat-proxy")
	s.Len(extensions["nat-proxy"], 2)
	s.Nil(serviceXIncusComposeExtensions(types.ServiceConfig{}))
}

func TestProjectInternalSuite(t *testing.T) {
	suite.Run(t, new(ProjectInternalSuite))
}
