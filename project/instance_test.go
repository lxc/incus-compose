package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
)

// fixturePath returns the path to a test fixture.
func fixturePath(name string) string {
	return filepath.Join("..", "test", "fixtures", name)
}

func TestFormatMemoryLimit(t *testing.T) {
	t.Parallel()

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
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, formatMemoryLimit(tt.bytes))
		})
	}
}

func TestApplyRestartPolicy(t *testing.T) {
	t.Parallel()

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
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := map[string]string{}
			applyRestartPolicy(config, tt.policy)
			assert.Equal(t, tt.want, config)
		})
	}
}

func TestFormatTmpfsSize(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "", formatTmpfsSize(nil))
	assert.Equal(t, "", formatTmpfsSize(&types.ServiceVolumeTmpfs{}))
	assert.Equal(t, "4096", formatTmpfsSize(&types.ServiceVolumeTmpfs{Size: 4096}))
}

func TestParseSecretOwnershipAndMode(t *testing.T) {
	t.Parallel()

	mode := types.FileMode(0o440)

	assert.Equal(t, int64(0), parseSecretUID(""))
	assert.Equal(t, int64(1000), parseSecretUID("1000"))
	assert.Equal(t, int64(0), parseSecretUID("not-a-number"))
	assert.Equal(t, int64(0), parseSecretGID(""))
	assert.Equal(t, int64(1001), parseSecretGID("1001"))
	assert.Equal(t, int64(0), parseSecretGID("not-a-number"))
	assert.Equal(t, 0, parseSecretMode(nil))
	assert.Equal(t, 0o440, parseSecretMode(&mode))
}

func TestFormatCommand(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "", formatCommand(nil))
	assert.Equal(t, "/bin/sh", formatCommand([]string{"/bin/sh"}))
	assert.Equal(t, "/bin/sh \"-c\" \"echo hello\"", formatCommand([]string{"/bin/sh", "-c", "echo hello"}))
}

func TestNetworkExtensionsExtractsXIncus(t *testing.T) {
	t.Parallel()

	proj, err := New().Load(context.Background(), LoadWorkingDir(fixturePath("with-network-ranges")))
	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"ipv4.address":     "10.200.0.1/24",
		"ipv4.dhcp.ranges": "10.200.0.100-10.200.0.200",
		"ipv6.address":     "fd42:1::1/64",
	}, networkExtensions(proj.Networks["backend"]))
	assert.Nil(t, networkExtensions(types.NetworkConfig{}))
}

func TestServiceXIncusExtensionsExtractsXIncus(t *testing.T) {
	t.Parallel()

	proj, err := New().Load(context.Background(), LoadWorkingDir(fixturePath("with-incus-options")))
	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"limits.memory":    "1024MB",
		"limits.cpu":       "2",
		"security.nesting": "false",
	}, serviceXIncusExtensions(proj.Services["web"]))
	assert.Nil(t, serviceXIncusExtensions(types.ServiceConfig{}))
}

func TestServiceXIncusComposeExtensionsExtractsXIncusCompose(t *testing.T) {
	t.Parallel()

	proj, err := New().Load(context.Background(), LoadWorkingDir(fixturePath("with-nat-proxy")))
	require.NoError(t, err)

	extensions := serviceXIncusComposeExtensions(proj.Services["web"])
	require.Contains(t, extensions, "nat-proxy")
	assert.Len(t, extensions["nat-proxy"], 2)
	assert.Nil(t, serviceXIncusComposeExtensions(types.ServiceConfig{}))
}

func TestInstanceName(t *testing.T) {
	t.Parallel()

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
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, instanceName(tt.service, tt.index, tt.scale))
		})
	}
}

func TestInstanceConfig(t *testing.T) {
	t.Parallel()

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
	require.NoError(t, err)

	assert.Equal(t, "bar", config["environment.FOO"])
	assert.NotContains(t, config, "environment.SKIP")
	assert.Equal(t, "x", config["user.com.example"])
	assert.Equal(t, `/bin/sh "-c" "echo hi"`, config["oci.entrypoint"])
	assert.Equal(t, "true", config["boot.autostart"])
	assert.Equal(t, "always", config["user.restart"])
	assert.Equal(t, "2", config["limits.cpu"])
	assert.Equal(t, "512MiB", config["limits.memory"])
	assert.Equal(t, client.HealthStatusUnknown, config[client.HealthStatusKey])
	assert.Equal(t, `["CMD","curl","-f","http://localhost"]`, config[client.HealthKeyPrefix+"test"])
	assert.Equal(t, "3", config[client.HealthKeyPrefix+"retries"])
}

func TestInstanceConfigMinimal(t *testing.T) {
	t.Parallel()

	config, err := instanceConfig(types.ServiceConfig{Name: "web"})
	require.NoError(t, err)
	// Only the default restart policy is applied.
	assert.Equal(t, map[string]string{"boot.autostart": "false", client.HealthStatusKey: client.HealthStatusUnknown}, config)
}

func TestInstanceConfigXIncusOverrides(t *testing.T) {
	t.Parallel()

	proj, err := New().Load(context.Background(), LoadWorkingDir(fixturePath("with-incus-options")))
	require.NoError(t, err)

	config, err := instanceConfig(proj.Services["web"])
	require.NoError(t, err)
	assert.Equal(t, "1024MB", config["limits.memory"])
	assert.Equal(t, "2", config["limits.cpu"])
	assert.Equal(t, "false", config["security.nesting"])
}

func TestInstanceDependencyWaits(t *testing.T) {
	t.Parallel()

	t.Run("scale from options", func(t *testing.T) {
		t.Parallel()
		p := &types.Project{Services: types.Services{"db": {Name: "db"}}}
		service := types.ServiceConfig{Name: "web", DependsOn: types.DependsOnConfig{
			"db": {Condition: types.ServiceConditionHealthy},
		}}
		deps := instanceDependencyWaits(p, service, &ToStackOptions{Scale: map[string]int{"db": 2}})
		assert.Equal(t, map[string]string{
			"db-1": client.HealthStatusHealthy,
			"db-2": client.HealthStatusHealthy,
		}, deps)
	})

	t.Run("scale from replicas", func(t *testing.T) {
		t.Parallel()
		reps := 3
		p := &types.Project{Services: types.Services{"db": {Name: "db", Deploy: &types.DeployConfig{Replicas: &reps}}}}
		service := types.ServiceConfig{Name: "web", DependsOn: types.DependsOnConfig{
			"db": {Condition: types.ServiceConditionHealthy},
		}}
		deps := instanceDependencyWaits(p, service, &ToStackOptions{})
		assert.Equal(t, map[string]string{
			"db-1": client.HealthStatusHealthy,
			"db-2": client.HealthStatusHealthy,
			"db-3": client.HealthStatusHealthy,
		}, deps)
	})

	t.Run("container name", func(t *testing.T) {
		t.Parallel()
		p := &types.Project{Services: types.Services{"db": {Name: "db", ContainerName: "mydb"}}}
		service := types.ServiceConfig{Name: "web", DependsOn: types.DependsOnConfig{
			"db": {Condition: types.ServiceConditionHealthy},
		}}
		deps := instanceDependencyWaits(p, service, &ToStackOptions{})
		assert.Equal(t, map[string]string{"mydb": client.HealthStatusHealthy}, deps)
	})

	t.Run("non-healthy condition skipped", func(t *testing.T) {
		t.Parallel()
		p := &types.Project{Services: types.Services{"db": {Name: "db"}}}
		service := types.ServiceConfig{Name: "web", DependsOn: types.DependsOnConfig{
			"db": {Condition: types.ServiceConditionStarted},
		}}
		assert.Nil(t, instanceDependencyWaits(p, service, &ToStackOptions{}))
	})

	t.Run("no dependencies", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, instanceDependencyWaits(&types.Project{}, types.ServiceConfig{Name: "web"}, &ToStackOptions{}))
	})
}

func TestInstanceSecrets(t *testing.T) {
	// Not parallel: the "environment source" subtest uses t.Setenv, which panics
	// under a parallel ancestor.

	t.Run("file source", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "secret.txt")
		require.NoError(t, os.WriteFile(path, []byte("s3cr3t"), 0o600))
		p := &types.Project{Secrets: types.Secrets{"db_pw": {File: path}}}
		service := types.ServiceConfig{Name: "web", Secrets: []types.ServiceSecretConfig{{Source: "db_pw", Target: "db_pw"}}}

		secrets, err := instanceSecrets(p, service)
		require.NoError(t, err)
		require.Len(t, secrets, 1)
		assert.Equal(t, "db_pw", secrets[0].Source)
		assert.Equal(t, []byte("s3cr3t"), secrets[0].Content)
	})

	t.Run("environment source", func(t *testing.T) {
		// Not parallel: uses t.Setenv.
		t.Setenv("MY_SECRET", "envval")
		p := &types.Project{Secrets: types.Secrets{"s": {Environment: "MY_SECRET"}}}
		service := types.ServiceConfig{Name: "web", Secrets: []types.ServiceSecretConfig{{Source: "s"}}}

		secrets, err := instanceSecrets(p, service)
		require.NoError(t, err)
		require.Len(t, secrets, 1)
		assert.Equal(t, []byte("envval"), secrets[0].Content)
	})

	t.Run("undefined secret", func(t *testing.T) {
		t.Parallel()
		service := types.ServiceConfig{Name: "web", Secrets: []types.ServiceSecretConfig{{Source: "missing"}}}
		_, err := instanceSecrets(&types.Project{}, service)
		require.Error(t, err)
	})

	t.Run("no source", func(t *testing.T) {
		t.Parallel()
		p := &types.Project{Secrets: types.Secrets{"s": {}}}
		service := types.ServiceConfig{Name: "web", Secrets: []types.ServiceSecretConfig{{Source: "s"}}}
		_, err := instanceSecrets(p, service)
		require.Error(t, err)
	})

	t.Run("missing file", func(t *testing.T) {
		t.Parallel()
		p := &types.Project{Secrets: types.Secrets{"s": {File: filepath.Join(t.TempDir(), "nope")}}}
		service := types.ServiceConfig{Name: "web", Secrets: []types.ServiceSecretConfig{{Source: "s"}}}
		_, err := instanceSecrets(p, service)
		require.Error(t, err)
	})
}

func TestInstanceImage(t *testing.T) {
	t.Parallel()

	c := client.NewOfflineClient(context.Background(), "test")

	t.Run("pull", func(t *testing.T) {
		t.Parallel()
		res, err := instanceImage(c, types.ServiceConfig{Name: "web", Image: "docker.io/nginx:alpine"})
		require.NoError(t, err)
		img, ok := res.(*client.Image)
		require.True(t, ok)
		assert.Contains(t, img.IncusName(), "nginx")
		assert.Contains(t, img.Config.Services, "web")
	})

	t.Run("build defaults image name", func(t *testing.T) {
		t.Parallel()
		res, err := instanceImage(c, types.ServiceConfig{Name: "web", Build: &types.BuildConfig{Context: "."}})
		require.NoError(t, err)
		img, ok := res.(*client.Image)
		require.True(t, ok)
		assert.Contains(t, img.IncusName(), "web")
	})

	t.Run("build with multiple platforms errors but still builds image", func(t *testing.T) {
		t.Parallel()
		res, err := instanceImage(c, types.ServiceConfig{
			Name:  "web",
			Build: &types.BuildConfig{Context: ".", Platforms: []string{"linux/amd64", "linux/arm64"}},
		})
		require.Error(t, err)
		assert.NotNil(t, res)
	})
}

func TestInstanceNetworkDevices(t *testing.T) {
	t.Parallel()

	c := client.NewOfflineClient(context.Background(), "test")

	t.Run("with static ip", func(t *testing.T) {
		t.Parallel()
		p := &types.Project{Networks: types.Networks{"frontend": {}}}
		service := types.ServiceConfig{Name: "web", Networks: map[string]*types.ServiceNetworkConfig{
			"frontend": {Ipv4Address: "10.0.0.5"},
		}}

		devices, resources, err := instanceNetworkDevices(c, p, service)
		require.NoError(t, err)
		require.Len(t, devices, 1)
		assert.Equal(t, "eth0", devices[0].Name)
		assert.Equal(t, client.InstanceDeviceTypeNic, devices[0].Config.DeviceType)
		assert.Equal(t, "10.0.0.5", devices[0].Config.Ipv4Address)
		require.Len(t, resources, 1)
		assert.Equal(t, client.KindNetwork, resources[0].Kind())
	})

	t.Run("no networks", func(t *testing.T) {
		t.Parallel()
		devices, resources, err := instanceNetworkDevices(c, &types.Project{}, types.ServiceConfig{Name: "web"})
		require.NoError(t, err)
		assert.Empty(t, devices)
		assert.Empty(t, resources)
	})
}

func TestInstanceProxyDevices(t *testing.T) {
	t.Parallel()

	c := client.NewOfflineClient(context.Background(), "test")

	t.Run("published port", func(t *testing.T) {
		t.Parallel()
		service := types.ServiceConfig{Name: "web", Ports: []types.ServicePortConfig{
			{Published: "8080", Target: 80, Protocol: "tcp"},
		}}

		devices, post, err := instanceProxyDevices(c, service, nil)
		require.NoError(t, err)
		assert.Empty(t, post)
		require.Len(t, devices, 1)
		assert.Equal(t, "proxy-8080", devices[0].Name)
		proxy := devices[0].Config.Proxy
		assert.Equal(t, "0.0.0.0", proxy.ListenAddr)
		assert.Equal(t, uint32(8080), proxy.ListenPort)
		assert.Equal(t, "127.0.0.1", proxy.ConnectAddr)
		assert.Equal(t, uint32(80), proxy.ConnectPort)
	})

	t.Run("bad published port", func(t *testing.T) {
		t.Parallel()
		service := types.ServiceConfig{Name: "web", Ports: []types.ServicePortConfig{
			{Published: "not-a-port", Target: 80},
		}}
		_, _, err := instanceProxyDevices(c, service, nil)
		require.Error(t, err)
	})

	t.Run("nat-proxy without nic is skipped", func(t *testing.T) {
		t.Parallel()
		proj, err := New().Load(context.Background(), LoadWorkingDir(fixturePath("with-nat-proxy")))
		require.NoError(t, err)

		_, post, err := instanceProxyDevices(c, proj.Services["web"], nil)
		require.NoError(t, err)
		assert.Empty(t, post)
	})

	t.Run("nat-proxy with nic creates post-start devices", func(t *testing.T) {
		t.Parallel()
		proj, err := New().Load(context.Background(), LoadWorkingDir(fixturePath("with-nat-proxy")))
		require.NoError(t, err)

		nicDevices := []client.InstanceDevice{
			{Config: client.InstanceDeviceConfig{DeviceType: client.InstanceDeviceTypeNic}},
		}
		_, post, err := instanceProxyDevices(c, proj.Services["web"], nicDevices)
		require.NoError(t, err)
		assert.Len(t, post, 2)
		for _, dev := range post {
			assert.True(t, dev.Config.Proxy.Nat)
			assert.Equal(t, "0.0.0.0", dev.Config.Proxy.ListenAddr)
		}
	})
}

func TestInstanceVolumeDevices(t *testing.T) {
	t.Parallel()

	c := client.NewOfflineClient(context.Background(), "test")
	opts := &ToStackOptions{StorageVolumes: true}

	t.Run("named volume", func(t *testing.T) {
		t.Parallel()
		p := &types.Project{Volumes: types.Volumes{"data": {}}}
		service := types.ServiceConfig{Name: "web", Volumes: []types.ServiceVolumeConfig{
			{Type: "volume", Source: "data", Target: "/data"},
		}}

		devices, files, resources, err := instanceVolumeDevices(c, p, service, nil, opts)
		require.NoError(t, err)
		assert.Empty(t, files)
		require.Len(t, devices, 1)
		assert.Equal(t, client.InstanceDeviceTypeDisk, devices[0].Config.DeviceType)
		assert.Equal(t, "/data", devices[0].Config.Disk.Path)
		assert.Len(t, resources, 1)
	})

	t.Run("tmpfs", func(t *testing.T) {
		t.Parallel()
		service := types.ServiceConfig{Name: "web", Volumes: []types.ServiceVolumeConfig{
			{Type: "tmpfs", Target: "/cache"},
		}}

		devices, _, _, err := instanceVolumeDevices(c, &types.Project{}, service, nil, opts)
		require.NoError(t, err)
		require.Len(t, devices, 1)
		assert.Equal(t, client.InstanceDeviceTypeTmpfs, devices[0].Config.DeviceType)
		assert.Equal(t, "/cache", devices[0].Config.Tmpfs.Path)
	})

	t.Run("bind directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		service := types.ServiceConfig{Name: "web", Volumes: []types.ServiceVolumeConfig{
			{Type: "bind", Source: dir, Target: "/mnt"},
		}}

		devices, files, resources, err := instanceVolumeDevices(c, &types.Project{}, service, nil, opts)
		require.NoError(t, err)
		assert.Empty(t, files)
		require.Len(t, devices, 1)
		assert.Equal(t, client.InstanceDeviceTypeDisk, devices[0].Config.DeviceType)
		assert.Empty(t, resources)
	})

	t.Run("unknown type", func(t *testing.T) {
		t.Parallel()
		service := types.ServiceConfig{Name: "web", Volumes: []types.ServiceVolumeConfig{
			{Type: "weird", Source: "x", Target: "/y"},
		}}
		_, _, _, err := instanceVolumeDevices(c, &types.Project{}, service, nil, opts)
		require.Error(t, err)
	})

	t.Run("missing bind source", func(t *testing.T) {
		t.Parallel()
		service := types.ServiceConfig{Name: "web", Volumes: []types.ServiceVolumeConfig{
			{Type: "bind", Source: filepath.Join(t.TempDir(), "nope"), Target: "/m"},
		}}
		_, _, _, err := instanceVolumeDevices(c, &types.Project{}, service, nil, opts)
		require.Error(t, err)
	})
}
