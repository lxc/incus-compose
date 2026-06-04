package project

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/suite"

	"gitlab.com/r3j0/incus-compose/client"
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

func (s *ProjectInternalSuite) TestNewLoadOptionsDefaultsToIncusProfile() {
	options := NewLoadOptions()

	s.Empty(options.Files)
	s.Equal([]string{"incus"}, options.Profiles)
	s.False(options.OsEnv)
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
	s.Equal([]string{"dev", "incus"}, options.Profiles)
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
	tests := []struct {
		name     string
		services types.Services
	}{
		{
			name: "missing dependency",
			services: types.Services{
				"api": {Name: "api", DependsOn: types.DependsOnConfig{"db": types.ServiceDependency{}}},
			},
		},
		{
			name: "cycle",
			services: types.Services{
				"api": {Name: "api", DependsOn: types.DependsOnConfig{"web": types.ServiceDependency{}}},
				"web": {Name: "web", DependsOn: types.DependsOnConfig{"api": types.ServiceDependency{}}},
			},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			_, err := ServiceGraph(tt.services, false)
			s.Error(err)
		})
	}
}

func (s *ProjectInternalSuite) TestProjectConfigExtractsXIncus() {
	proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("with-project-options")))
	s.Require().NoError(err)

	s.Equal(map[string]string{
		"limits.cpu":              "4",
		"limits.memory":           "2049MB",
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

func (s *ProjectInternalSuite) TestNetworkProfileConfigExtractsXIncusCompose() {
	proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("with-network-profile")))
	s.Require().NoError(err)

	config, err := proj.NetworkProfileConfig()
	s.Require().NoError(err)
	s.Equal("default", config.SourceProject)
	s.Equal("default", config.SourceProfile)
	s.True(config.NetworkOnly)
}

func (s *ProjectInternalSuite) TestNetworkProfileConfigReturnsNilWithoutExtension() {
	s.Nil((*Project)(nil).ProjectConfig())
	s.Nil((&Project{}).ProjectConfig())

	proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("simple-nginx")))
	s.Require().NoError(err)

	config, err := proj.NetworkProfileConfig()
	s.NoError(err)
	s.Nil(config)
}

func (s *ProjectInternalSuite) TestNetworkProfileConfigRejectsInvalidValues() {
	tests := []struct {
		name    string
		value   any
		wantErr string
	}{
		{name: "missing separator", value: "default", wantErr: "format"},
		{name: "empty project", value: ":default", wantErr: "empty project"},
		{name: "empty profile", value: "default:", wantErr: "empty profile"},
		{name: "not string", value: 1, wantErr: "must be a string"},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			proj := &Project{Project: &types.Project{Extensions: types.Extensions{
				"x-incus-compose": map[string]any{"network-profile": tt.value},
			}}}

			_, err := proj.NetworkProfileConfig()
			s.Error(err)
			s.Contains(err.Error(), tt.wantErr)
		})
	}
}

func (s *ProjectInternalSuite) TestToStackUsesNetworkProfileWithoutNetworkResources() {
	proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("with-network-profile")))
	s.Require().NoError(err)

	c := client.NewOfflineClient(s.ctx, proj.Name)
	stack := client.NewStack(c)
	s.Require().NoError(proj.ToStack(c, stack))

	var hasNetwork bool
	var profile *client.Profile
	var instance *client.Instance
	for _, resource := range stack.All() {
		s.False(resource.Kind() == client.KindNetwork, "network resource %s should be omitted", resource.Name())
		if resource.Kind() == client.KindNetwork {
			hasNetwork = true
		}
		if resource.Kind() == client.KindProfile {
			var ok bool
			profile, ok = resource.(*client.Profile)
			s.True(ok)
		}
		if resource.Kind() == client.KindInstance {
			var ok bool
			instance, ok = resource.(*client.Instance)
			s.True(ok)
		}
	}

	s.False(hasNetwork)
	s.Require().NotNil(profile)
	s.Equal("default", profile.Name())
	s.Equal("default", profile.Config.SourceProject)
	s.Equal("default", profile.Config.SourceProfile)
	s.True(profile.Config.NetworkOnly)
	s.Require().NotNil(instance)
	s.Empty(instance.Config.Devices)
	s.Contains(instance.Config.Resources, profile)
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

func (s *ProjectInternalSuite) TestToStackRejectsStaticIPsWithNetworkProfile() {
	proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("with-network-profile")))
	s.Require().NoError(err)
	proj.Services["web"].Networks["default"] = &types.ServiceNetworkConfig{Ipv4Address: "10.0.0.10"}

	c := client.NewOfflineClient(s.ctx, proj.Name)
	err = proj.ToStack(c, client.NewStack(c))
	s.Error(err)
	s.True(strings.Contains(err.Error(), "static addresses"))
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
		"limits.memory":    "512MB",
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
