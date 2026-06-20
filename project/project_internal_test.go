package project

import (
	"context"
	"os"
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

func (s *ProjectInternalSuite) TestNetworkConfigReturnsErrorWithoutExtension() {
	proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("simple-nginx")))
	s.Require().NoError(err)

	_, _, err = proj.NetworkConfig()
	s.Require().Error(err)
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

func (s *ProjectInternalSuite) TestInstanceName() {
	tests := []struct {
		name    string
		service types.ServiceConfig
		index   int
		scale   int
		want    string
	}{
		{name: "default", service: types.ServiceConfig{Name: "web"}, index: 1, scale: 1, want: "web-1"},
		{name: "container name single", service: types.ServiceConfig{Name: "web", ContainerName: "mydb"}, index: 1, scale: 1, want: "mydb"},
		{name: "container name scaled", service: types.ServiceConfig{Name: "web", ContainerName: "mydb"}, index: 2, scale: 3, want: "mydb-2"},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Equal(tt.want, instanceName(tt.service, tt.index, tt.scale))
		})
	}
}

func (s *ProjectInternalSuite) TestInstanceConfig() {
	val := "bar"
	retries := uint64(3)
	service := types.ServiceConfig{
		Name:        "web",
		Environment: types.MappingWithEquals{"FOO": &val, "SKIP": nil},
		Labels:      types.Labels{"com.example": "x"},
		Command:     types.ShellCommand{"/bin/sh", "-c", "echo hi"},
		Restart:     "always",
		Deploy: &types.DeployConfig{
			Resources: types.Resources{
				Limits: &types.Resource{NanoCPUs: 2, MemoryBytes: 512 << 20},
			},
		},
		HealthCheck: &types.HealthCheckConfig{
			Test:    types.HealthCheckTest{"CMD", "curl", "-f", "http://localhost"},
			Retries: &retries,
		},
	}

	config, err := instanceConfig(service)
	s.Require().NoError(err)

	s.Equal("bar", config["environment.FOO"])
	s.NotContains(config, "environment.SKIP")
	s.Equal("x", config["user.com.example"])
	s.Equal(`/bin/sh "-c" "echo hi"`, config["oci.entrypoint"])
	s.Equal("true", config["boot.autostart"])
	s.Equal("always", config["user.restart"])
	s.Equal("2", config["limits.cpu"])
	s.Equal("512MiB", config["limits.memory"])
	s.Equal(client.HealthStatusStarting, config[client.HealthConfigKey])
	s.Equal(`["CMD","curl","-f","http://localhost"]`, config["user.healthcheck.test"])
	s.Equal("3", config["user.healthcheck.retries"])
}

func (s *ProjectInternalSuite) TestInstanceConfigMinimal() {
	config, err := instanceConfig(types.ServiceConfig{Name: "web"})
	s.Require().NoError(err)
	// Only the default restart policy is applied.
	s.Equal(map[string]string{"boot.autostart": "false"}, config)
}

func (s *ProjectInternalSuite) TestInstanceConfigXIncusOverrides() {
	proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("with-incus-options")))
	s.Require().NoError(err)

	config, err := instanceConfig(proj.Services["web"])
	s.Require().NoError(err)
	s.Equal("1024MB", config["limits.memory"])
	s.Equal("2", config["limits.cpu"])
	s.Equal("false", config["security.nesting"])
}

func (s *ProjectInternalSuite) TestInstanceDependencyWaits() {
	s.Run("scale from options", func() {
		p := &types.Project{Services: types.Services{"db": {Name: "db"}}}
		service := types.ServiceConfig{Name: "web", DependsOn: types.DependsOnConfig{
			"db": {Condition: types.ServiceConditionHealthy},
		}}
		deps := instanceDependencyWaits(p, service, &ToStackOptions{Scale: map[string]int{"db": 2}})
		s.Equal(map[string]string{
			"db-1": client.HealthStatusHealthy,
			"db-2": client.HealthStatusHealthy,
		}, deps)
	})

	s.Run("scale from replicas", func() {
		reps := 3
		p := &types.Project{Services: types.Services{"db": {Name: "db", Deploy: &types.DeployConfig{Replicas: &reps}}}}
		service := types.ServiceConfig{Name: "web", DependsOn: types.DependsOnConfig{
			"db": {Condition: types.ServiceConditionHealthy},
		}}
		deps := instanceDependencyWaits(p, service, &ToStackOptions{})
		s.Equal(map[string]string{
			"db-1": client.HealthStatusHealthy,
			"db-2": client.HealthStatusHealthy,
			"db-3": client.HealthStatusHealthy,
		}, deps)
	})

	s.Run("container name", func() {
		p := &types.Project{Services: types.Services{"db": {Name: "db", ContainerName: "mydb"}}}
		service := types.ServiceConfig{Name: "web", DependsOn: types.DependsOnConfig{
			"db": {Condition: types.ServiceConditionHealthy},
		}}
		deps := instanceDependencyWaits(p, service, &ToStackOptions{})
		s.Equal(map[string]string{"mydb": client.HealthStatusHealthy}, deps)
	})

	s.Run("non-healthy condition skipped", func() {
		p := &types.Project{Services: types.Services{"db": {Name: "db"}}}
		service := types.ServiceConfig{Name: "web", DependsOn: types.DependsOnConfig{
			"db": {Condition: types.ServiceConditionStarted},
		}}
		s.Nil(instanceDependencyWaits(p, service, &ToStackOptions{}))
	})

	s.Run("no dependencies", func() {
		s.Nil(instanceDependencyWaits(&types.Project{}, types.ServiceConfig{Name: "web"}, &ToStackOptions{}))
	})
}

func (s *ProjectInternalSuite) TestInstanceSecrets() {
	s.Run("file source", func() {
		path := filepath.Join(s.T().TempDir(), "secret.txt")
		s.Require().NoError(os.WriteFile(path, []byte("s3cr3t"), 0o600))
		p := &types.Project{Secrets: types.Secrets{"db_pw": {File: path}}}
		service := types.ServiceConfig{Name: "web", Secrets: []types.ServiceSecretConfig{{Source: "db_pw", Target: "db_pw"}}}

		secrets, err := instanceSecrets(p, service)
		s.Require().NoError(err)
		s.Require().Len(secrets, 1)
		s.Equal("db_pw", secrets[0].Source)
		s.Equal([]byte("s3cr3t"), secrets[0].Content)
	})

	s.Run("environment source", func() {
		s.T().Setenv("MY_SECRET", "envval")
		p := &types.Project{Secrets: types.Secrets{"s": {Environment: "MY_SECRET"}}}
		service := types.ServiceConfig{Name: "web", Secrets: []types.ServiceSecretConfig{{Source: "s"}}}

		secrets, err := instanceSecrets(p, service)
		s.Require().NoError(err)
		s.Require().Len(secrets, 1)
		s.Equal([]byte("envval"), secrets[0].Content)
	})

	s.Run("undefined secret", func() {
		service := types.ServiceConfig{Name: "web", Secrets: []types.ServiceSecretConfig{{Source: "missing"}}}
		_, err := instanceSecrets(&types.Project{}, service)
		s.Require().Error(err)
	})

	s.Run("no source", func() {
		p := &types.Project{Secrets: types.Secrets{"s": {}}}
		service := types.ServiceConfig{Name: "web", Secrets: []types.ServiceSecretConfig{{Source: "s"}}}
		_, err := instanceSecrets(p, service)
		s.Require().Error(err)
	})

	s.Run("missing file", func() {
		p := &types.Project{Secrets: types.Secrets{"s": {File: filepath.Join(s.T().TempDir(), "nope")}}}
		service := types.ServiceConfig{Name: "web", Secrets: []types.ServiceSecretConfig{{Source: "s"}}}
		_, err := instanceSecrets(p, service)
		s.Require().Error(err)
	})
}

func (s *ProjectInternalSuite) TestInstanceImage() {
	c := client.NewOfflineClient(s.ctx, "test")

	s.Run("pull", func() {
		res, err := instanceImage(c, types.ServiceConfig{Name: "web", Image: "docker.io/nginx:alpine"})
		s.Require().NoError(err)
		img, ok := res.(*client.Image)
		s.Require().True(ok)
		s.Contains(img.IncusName(), "nginx")
		s.Contains(img.Config.Services, "web")
	})

	s.Run("build defaults image name", func() {
		res, err := instanceImage(c, types.ServiceConfig{Name: "web", Build: &types.BuildConfig{Context: "."}})
		s.Require().NoError(err)
		img, ok := res.(*client.Image)
		s.Require().True(ok)
		s.Contains(img.IncusName(), "web")
	})

	s.Run("build with multiple platforms errors but still builds image", func() {
		res, err := instanceImage(c, types.ServiceConfig{
			Name:  "web",
			Build: &types.BuildConfig{Context: ".", Platforms: []string{"linux/amd64", "linux/arm64"}},
		})
		s.Require().Error(err)
		s.NotNil(res)
	})
}

func (s *ProjectInternalSuite) TestInstanceNetworkDevices() {
	c := client.NewOfflineClient(s.ctx, "test")

	s.Run("with static ip", func() {
		p := &types.Project{Networks: types.Networks{"frontend": {}}}
		service := types.ServiceConfig{Name: "web", Networks: map[string]*types.ServiceNetworkConfig{
			"frontend": {Ipv4Address: "10.0.0.5"},
		}}

		devices, resources, err := instanceNetworkDevices(c, p, service)
		s.Require().NoError(err)
		s.Require().Len(devices, 1)
		s.Equal("eth0", devices[0].Name)
		s.Equal(client.InstanceDeviceTypeNic, devices[0].Config.DeviceType)
		s.Equal("10.0.0.5", devices[0].Config.Ipv4Address)
		s.Require().Len(resources, 1)
		s.Equal(client.KindNetwork, resources[0].Kind())
	})

	s.Run("no networks", func() {
		devices, resources, err := instanceNetworkDevices(c, &types.Project{}, types.ServiceConfig{Name: "web"})
		s.Require().NoError(err)
		s.Empty(devices)
		s.Empty(resources)
	})
}

func (s *ProjectInternalSuite) TestInstanceProxyDevices() {
	c := client.NewOfflineClient(s.ctx, "test")

	s.Run("published port", func() {
		service := types.ServiceConfig{Name: "web", Ports: []types.ServicePortConfig{
			{Published: "8080", Target: 80, Protocol: "tcp"},
		}}

		devices, post, err := instanceProxyDevices(c, service, nil)
		s.Require().NoError(err)
		s.Empty(post)
		s.Require().Len(devices, 1)
		s.Equal("proxy-8080", devices[0].Name)
		proxy := devices[0].Config.Proxy
		s.Equal("0.0.0.0", proxy.ListenAddr)
		s.Equal(uint32(8080), proxy.ListenPort)
		s.Equal("127.0.0.1", proxy.ConnectAddr)
		s.Equal(uint32(80), proxy.ConnectPort)
	})

	s.Run("bad published port", func() {
		service := types.ServiceConfig{Name: "web", Ports: []types.ServicePortConfig{
			{Published: "not-a-port", Target: 80},
		}}
		_, _, err := instanceProxyDevices(c, service, nil)
		s.Require().Error(err)
	})

	s.Run("nat-proxy without nic is skipped", func() {
		proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("with-nat-proxy")))
		s.Require().NoError(err)

		_, post, err := instanceProxyDevices(c, proj.Services["web"], nil)
		s.Require().NoError(err)
		s.Empty(post)
	})

	s.Run("nat-proxy with nic creates post-start devices", func() {
		proj, err := New().Load(s.ctx, LoadWorkingDir(s.fixturePath("with-nat-proxy")))
		s.Require().NoError(err)

		nicDevices := []client.InstanceDevice{
			{Config: client.InstanceDeviceConfig{DeviceType: client.InstanceDeviceTypeNic}},
		}
		_, post, err := instanceProxyDevices(c, proj.Services["web"], nicDevices)
		s.Require().NoError(err)
		s.Len(post, 2)
		for _, dev := range post {
			s.True(dev.Config.Proxy.Nat)
			s.Equal("0.0.0.0", dev.Config.Proxy.ListenAddr)
		}
	})
}

func (s *ProjectInternalSuite) TestInstanceVolumeDevices() {
	c := client.NewOfflineClient(s.ctx, "test")
	opts := &ToStackOptions{StorageVolumes: true}

	s.Run("named volume", func() {
		p := &types.Project{Volumes: types.Volumes{"data": {}}}
		service := types.ServiceConfig{Name: "web", Volumes: []types.ServiceVolumeConfig{
			{Type: "volume", Source: "data", Target: "/data"},
		}}

		devices, files, resources, err := instanceVolumeDevices(c, p, service, nil, opts)
		s.Require().NoError(err)
		s.Empty(files)
		s.Require().Len(devices, 1)
		s.Equal(client.InstanceDeviceTypeDisk, devices[0].Config.DeviceType)
		s.Equal("/data", devices[0].Config.Disk.Path)
		s.Len(resources, 1)
	})

	s.Run("tmpfs", func() {
		service := types.ServiceConfig{Name: "web", Volumes: []types.ServiceVolumeConfig{
			{Type: "tmpfs", Target: "/cache"},
		}}

		devices, _, _, err := instanceVolumeDevices(c, &types.Project{}, service, nil, opts)
		s.Require().NoError(err)
		s.Require().Len(devices, 1)
		s.Equal(client.InstanceDeviceTypeTmpfs, devices[0].Config.DeviceType)
		s.Equal("/cache", devices[0].Config.Tmpfs.Path)
	})

	s.Run("bind directory", func() {
		dir := s.T().TempDir()
		service := types.ServiceConfig{Name: "web", Volumes: []types.ServiceVolumeConfig{
			{Type: "bind", Source: dir, Target: "/mnt"},
		}}

		devices, files, resources, err := instanceVolumeDevices(c, &types.Project{}, service, nil, opts)
		s.Require().NoError(err)
		s.Empty(files)
		s.Require().Len(devices, 1)
		s.Equal(client.InstanceDeviceTypeDisk, devices[0].Config.DeviceType)
		s.Len(resources, 1)
	})

	s.Run("unknown type", func() {
		service := types.ServiceConfig{Name: "web", Volumes: []types.ServiceVolumeConfig{
			{Type: "weird", Source: "x", Target: "/y"},
		}}
		_, _, _, err := instanceVolumeDevices(c, &types.Project{}, service, nil, opts)
		s.Require().Error(err)
	})

	s.Run("missing bind source", func() {
		service := types.ServiceConfig{Name: "web", Volumes: []types.ServiceVolumeConfig{
			{Type: "bind", Source: filepath.Join(s.T().TempDir(), "nope"), Target: "/m"},
		}}
		_, _, _, err := instanceVolumeDevices(c, &types.Project{}, service, nil, opts)
		s.Require().Error(err)
	})
}

func TestProjectInternalSuite(t *testing.T) {
	suite.Run(t, new(ProjectInternalSuite))
}
