package project_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

// fixturePath returns the path to a test fixture.
func fixturePath(name string) string {
	return filepath.Join("..", "test", "fixtures", name)
}

// instanceNamesInStack returns the Name() of all KindInstance resources in the stack.
func instanceNamesInStack(stack *client.Stack) []string {
	var names []string
	for _, r := range stack.All() {
		if r.Kind() == client.KindInstance {
			names = append(names, r.Name())
		}
	}
	return names
}

// TestLoadBasicProject tests basic project loading.
func TestLoadBasicProject(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(), project.LoadWorkingDir(fixturePath("simple-nginx")),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)
	assert.Equal(t, "simple-nginx", proj.Name)
	assert.Len(t, proj.Services, 1)

	service, exists := proj.Services["web"]
	assert.True(t, exists, "web service should exist")
	assert.Equal(t, "web", service.Name)
	assert.Equal(t, "docker.io/nginx:alpine", service.Image)
}

// TestLoadWordPressStack tests WordPress stack with volumes and dependencies.
func TestLoadWordPressStack(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(), project.LoadWorkingDir(fixturePath("wordpress")),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)
	assert.Equal(t, "wordpress", proj.Name)
	assert.Len(t, proj.Services, 2)

	// Check db service.
	db, exists := proj.Services["db"]
	assert.True(t, exists, "db service should exist")
	assert.Equal(t, "docker.io/library/mysql:9.5.0", db.Image)
	assert.Contains(t, db.Environment, "MYSQL_ROOT_PASSWORD")

	// Check wordpress service.
	wordpress, exists := proj.Services["wordpress"]
	assert.True(t, exists, "wordpress service should exist")
	assert.Equal(t, "docker.io/library/wordpress:6.9.0-php8.1-apache", wordpress.Image)
	assert.NotEmpty(t, wordpress.DependsOn)

	// Check volumes.
	assert.Len(t, proj.Volumes, 2)
	assert.Contains(t, proj.Volumes, "db_data")
	assert.Contains(t, proj.Volumes, "wordpress_data")
}

// TestLoadPostgresRedisWithEnv tests PostgreSQL + Redis with environment variables.
func TestLoadPostgresRedisWithEnv(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(), project.LoadWorkingDir(fixturePath("postgres-redis")),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)
	assert.Equal(t, "postgres-redis", proj.Name)
	assert.Len(t, proj.Services, 3)

	// Check postgres service.
	postgres, exists := proj.Services["postgres"]
	assert.True(t, exists, "postgres service should exist")
	assert.Contains(t, postgres.Environment, "POSTGRES_USER")
	assert.Contains(t, postgres.Environment, "POSTGRES_PASSWORD")
	assert.Contains(t, postgres.Environment, "POSTGRES_DB")
	assert.NotNil(t, postgres.HealthCheck)

	// Check redis service.
	redis, exists := proj.Services["redis"]
	assert.True(t, exists, "redis service should exist")
	assert.NotNil(t, redis.HealthCheck)

	// Check api service.
	_, exists = proj.Services["api"]
	assert.True(t, exists, "api service should exist")
}

// TestLoadNginxProxyMultiNetwork tests Nginx proxy with multiple networks.
func TestLoadNginxProxyMultiNetwork(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(), project.LoadWorkingDir(fixturePath("nginx-proxy")),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)
	assert.Len(t, proj.Services, 3)

	// Check networks.
	assert.Len(t, proj.Networks, 2)
	assert.Contains(t, proj.Networks, "frontend")
	assert.Contains(t, proj.Networks, "backend")

	// Verify backend network is internal.
	backend := proj.Networks["backend"]
	assert.NotNil(t, backend)
	assert.True(t, backend.Internal)
}

// TestLoadMicroservices tests microservices architecture.
func TestLoadMicroservices(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(), project.LoadWorkingDir(fixturePath("microservices")),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)
	assert.Equal(t, "microservices", proj.Name)

	// Should have many services.
	assert.GreaterOrEqual(t, len(proj.Services), 8)

	// Check for different database types.
	_, hasAuthDB := proj.Services["auth-db"]
	assert.True(t, hasAuthDB, "Should have auth-db (PostgreSQL)")

	_, hasUserDB := proj.Services["user-db"]
	assert.True(t, hasUserDB, "Should have user-db (MySQL)")

	_, hasOrderDB := proj.Services["order-db"]
	assert.True(t, hasOrderDB, "Should have order-db (MongoDB)")

	// Check multiple networks for isolation.
	assert.GreaterOrEqual(t, len(proj.Networks), 5)
}

// TestLoadWithCustomProjectName tests custom project name.
func TestLoadWithCustomProjectName(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(), project.LoadWorkingDir(fixturePath("simple-nginx")),
		project.LoadName("my-custom-project"),
	)

	require.NoError(t, err)
	assert.Equal(t, "my-custom-project", proj.Name)
}

// TestLoadWithCustomFiles tests custom compose file path.
func TestLoadWithCustomFiles(t *testing.T) {
	t.Parallel()

	composePath := filepath.Join(fixturePath("wordpress"), "compose.yaml")

	proj, err := project.New().Load(
		context.Background(), project.LoadFiles([]string{composePath}),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)
	assert.Len(t, proj.Services, 2)
}

// TestLoadWithDefaultEnvFile tests environment file loading.
func TestLoadWithDefaultEnvFile(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(), project.LoadWorkingDir(fixturePath("with-env")),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)

	// Environment variables should be loaded from .env.
	app, exists := proj.Services["app"]
	assert.True(t, exists, "app service should exist")

	// Check that env vars from .env file are present.
	env := app.Environment
	assert.Contains(t, env, "DB_HOST")
	assert.Contains(t, env, "DB_PORT")
	assert.Contains(t, env, "DB_NAME")
}

// TestLoadWithCustomEnvFile tests custom environment file.
func TestLoadWithCustomEnvFile(t *testing.T) {
	t.Parallel()

	prodEnvPath := filepath.Join(fixturePath("with-env"), "production.env")

	proj, err := project.New().Load(
		context.Background(), project.LoadWorkingDir(fixturePath("with-env")),
		project.LoadEnvFiles([]string{prodEnvPath}),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)

	// Should load production.env instead of .env.
	app, exists := proj.Services["app"]
	assert.True(t, exists, "app service should exist")
	env := app.Environment

	// These values should come from production.env.
	assert.Contains(t, env, "DB_HOST")
	assert.Contains(t, env, "API_KEY")
	assert.Contains(t, env, "API_URL")
}

// TestLoadWithMultipleEnvFiles tests multiple environment files.
func TestLoadWithMultipleEnvFiles(t *testing.T) {
	t.Parallel()

	basePath := fixturePath("with-env")
	prodEnv := filepath.Join(basePath, "production.env")
	stagingEnv := filepath.Join(basePath, "staging.env")

	proj, err := project.New().Load(
		context.Background(),
		project.LoadWorkingDir(basePath),
		project.LoadEnvFiles([]string{prodEnv, stagingEnv}),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)
}

// TestLoadWithoutProfiles tests profiles - no profiles (default services only).
func TestLoadWithoutProfiles(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(),
		project.LoadWorkingDir(fixturePath("with-profiles")),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)

	// Only services without profiles should be loaded.
	assert.Len(t, proj.Services, 1)
	_, exists := proj.Services["web"]
	assert.True(t, exists, "web service should exist")
}

// TestLoadWithSingleProfile tests profiles - single profile.
func TestLoadWithSingleProfile(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(),
		project.LoadWorkingDir(fixturePath("with-profiles")),
		project.LoadProfiles([]string{"dev"}),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)

	// Should have base service + dev profile services.
	assert.GreaterOrEqual(t, len(proj.Services), 3)

	_, exists := proj.Services["web"]
	assert.True(t, exists, "Should have web service")

	_, exists = proj.Services["webpack"]
	assert.True(t, exists, "Should have webpack from dev profile")

	_, exists = proj.Services["hot-reload"]
	assert.True(t, exists, "Should have hot-reload from dev profile")
}

// TestLoadWithMultipleProfiles tests profiles - multiple profiles.
func TestLoadWithMultipleProfiles(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(),
		project.LoadWorkingDir(fixturePath("with-profiles")),
		project.LoadProfiles([]string{"dev", "monitoring"}),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)

	// Base service.
	_, exists := proj.Services["web"]
	assert.True(t, exists, "web service should exist")

	// Dev profile services.
	_, exists = proj.Services["webpack"]
	assert.True(t, exists, "webpack service should exist")

	_, exists = proj.Services["hot-reload"]
	assert.True(t, exists, "hot-reload service should exist")

	// Monitoring profile service.
	_, exists = proj.Services["prometheus"]
	assert.True(t, exists, "prometheus service should exist")

	// Adminer is in both dev and debug, so it should appear with dev profile.
	_, exists = proj.Services["adminer"]
	assert.True(t, exists, "adminer service should exist")
}

// TestLoadDevEnvironment tests development environment with profiles.
func TestLoadDevEnvironment(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(),
		project.LoadWorkingDir(fixturePath("dev-environment")),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)

	// Without profiles, should only have core services.
	assert.Len(t, proj.Services, 2) // app and db
}

// TestLoadDevEnvironmentWithDebugProfile tests development environment with debug profile.
func TestLoadDevEnvironmentWithDebugProfile(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(),
		project.LoadWorkingDir(fixturePath("dev-environment")),
		project.LoadProfiles([]string{"debug"}),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)

	// Should have core + debug services.
	assert.GreaterOrEqual(t, len(proj.Services), 4)

	_, exists := proj.Services["app"]
	assert.True(t, exists, "app service should exist")

	_, exists = proj.Services["db"]
	assert.True(t, exists, "db service should exist")

	_, exists = proj.Services["pgadmin"]
	assert.True(t, exists, "pgadmin service should exist")

	_, exists = proj.Services["mailhog"]
	assert.True(t, exists, "mailhog service should exist")
}

// TestLoadMultipleComposeFiles tests multiple compose files (base + override).
func TestLoadMultipleComposeFiles(t *testing.T) {
	t.Parallel()

	basePath := fixturePath("multiple-files")
	baseFile := filepath.Join(basePath, "compose.yaml")
	overrideFile := filepath.Join(basePath, "compose.override.yaml")

	proj, err := project.New().Load(
		context.Background(),
		project.LoadFiles([]string{baseFile, overrideFile}),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)

	// Should have merged services from both files.
	assert.Len(t, proj.Services, 3) // app, db, adminer

	// Check that override values are applied.
	app, exists := proj.Services["app"]
	assert.True(t, exists, "app service should exist")
	env := app.Environment
	// NODE_ENV should be overridden to development.
	assert.Contains(t, env, "NODE_ENV")
	assert.Contains(t, env, "HOT_RELOAD")
}

// TestLoadMultipleComposeFilesCustomOrder tests multiple compose files with custom order.
func TestLoadMultipleComposeFilesCustomOrder(t *testing.T) {
	t.Parallel()

	basePath := fixturePath("multiple-files")
	baseFile := filepath.Join(basePath, "compose.yaml")
	testFile := filepath.Join(basePath, "compose.test.yaml")

	proj, err := project.New().Load(
		context.Background(),
		project.LoadFiles([]string{baseFile, testFile}),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)

	// Should have test-runner service from compose.test.yaml.
	_, exists := proj.Services["test-runner"]
	assert.True(t, exists, "test-runner service should exist")
}

// TestLoadWithResourceLimits tests loading a compose file with deploy resource limits.
func TestLoadWithResourceLimits(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(), project.LoadWorkingDir(fixturePath("with-resources")),
	)
	require.NoError(t, err)
	require.NotNil(t, proj)
	assert.Len(t, proj.Services, 2)

	limited := proj.Services["limited"]
	require.NotNil(t, limited.Deploy)
	require.NotNil(t, limited.Deploy.Resources.Limits)
	assert.Equal(t, float32(0.5), limited.Deploy.Resources.Limits.NanoCPUs.Value())
	assert.Equal(t, int64(512<<20), int64(limited.Deploy.Resources.Limits.MemoryBytes))

	pinned := proj.Services["pinned"]
	require.NotNil(t, pinned.Deploy)
	require.NotNil(t, pinned.Deploy.Resources.Limits)
	assert.Equal(t, float32(2), pinned.Deploy.Resources.Limits.NanoCPUs.Value())
	assert.Equal(t, int64(1<<30), int64(pinned.Deploy.Resources.Limits.MemoryBytes))
}

// TestLoadInvalidComposeFile tests invalid compose file.
func TestLoadInvalidComposeFile(t *testing.T) {
	t.Parallel()

	_, err := project.New().Load(
		context.Background(),
		project.LoadWorkingDir(fixturePath("invalid")),
	)

	// Should return an error for invalid compose file.
	assert.Error(t, err)
}

// TestLoadMissingComposeFile tests missing compose file.
func TestLoadMissingComposeFile(t *testing.T) {
	t.Parallel()

	_, err := project.New().Load(
		context.Background(),
		project.LoadWorkingDir(fixturePath("nonexistent")),
	)

	assert.Error(t, err)
}

// TestLoadWithAllOptions tests all options combined.
func TestLoadWithAllOptions(t *testing.T) {
	t.Parallel()

	basePath := fixturePath("postgres-redis")
	composePath := filepath.Join(basePath, "compose.yaml")
	envPath := filepath.Join(basePath, ".env")

	proj, err := project.New().Load(
		context.Background(),
		project.LoadName("my-combined-project"),
		project.LoadFiles([]string{composePath}),
		project.LoadWorkingDir(basePath),
		project.LoadEnvFiles([]string{envPath}),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)
	assert.Equal(t, "my-combined-project", proj.Name)
	assert.Len(t, proj.Services, 3)
}

// TestLoadWithSecrets tests loading a compose file with secrets.
func TestLoadWithSecrets(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(), project.LoadWorkingDir(fixturePath("with-secrets")),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)
	assert.Equal(t, "with-secrets", proj.Name)

	// Check secrets are defined.
	assert.Len(t, proj.Secrets, 2)
	assert.Contains(t, proj.Secrets, "db_password")
	assert.Contains(t, proj.Secrets, "api_key")

	// Check db_password secret (file-based).
	dbSecret := proj.Secrets["db_password"]
	assert.NotEmpty(t, dbSecret.File)

	// Check api_key secret (file-based).
	apiSecret := proj.Secrets["api_key"]
	assert.NotEmpty(t, apiSecret.File)

	// Check service has secrets configured.
	app, exists := proj.Services["app"]
	assert.True(t, exists, "app service should exist")
	assert.Len(t, app.Secrets, 2)

	// Check first secret (simple reference).
	assert.Equal(t, "db_password", app.Secrets[0].Source)

	// Check second secret (with custom target).
	assert.Equal(t, "api_key", app.Secrets[1].Source)
	assert.Equal(t, "/app/secrets/api.key", app.Secrets[1].Target)
	assert.Equal(t, "1000", app.Secrets[1].UID)
	assert.Equal(t, "1000", app.Secrets[1].GID)
	assert.NotNil(t, app.Secrets[1].Mode)
}

// TestLoadWithRestartPolicies tests loading a compose file with restart policies.
func TestLoadWithRestartPolicies(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(), project.LoadWorkingDir(fixturePath("with-restart")),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)
	assert.Equal(t, "with-restart", proj.Name)
	assert.Len(t, proj.Services, 5)

	// Check restart policies are parsed.
	always := proj.Services["always-restart"]
	assert.Equal(t, "always", always.Restart)

	onFailure := proj.Services["on-failure-restart"]
	assert.Equal(t, "on-failure", onFailure.Restart)

	unlessStopped := proj.Services["unless-stopped-restart"]
	assert.Equal(t, "unless-stopped", unlessStopped.Restart)

	noRestart := proj.Services["no-restart"]
	assert.Equal(t, "no", noRestart.Restart)

	defaultRestart := proj.Services["default-restart"]
	assert.Equal(t, "", defaultRestart.Restart)
}

// TestLoadWithXIncusOptions tests loading a compose file with x-incus extensions.
func TestLoadWithXIncusOptions(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(
		context.Background(), project.LoadWorkingDir(fixturePath("with-incus-options")),
	)

	require.NoError(t, err)
	require.NotNil(t, proj)
	assert.Equal(t, "with-incus-options", proj.Name)
	assert.Len(t, proj.Services, 2)

	// Verify services loaded
	web, exists := proj.Services["web"]
	assert.True(t, exists, "web service should exist")
	assert.Equal(t, "docker.io/nginx:alpine", web.Image)

	database, exists := proj.Services["database"]
	assert.True(t, exists, "database service should exist")
	assert.Equal(t, "docker.io/nginx:alpine", database.Image)
}

func TestExternalNetworkOverrideNameParsed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	proj, err := project.New().Load(
		ctx, project.LoadWorkingDir(fixturePath("with-external-network")),
	)
	require.NoError(t, err)

	c := client.NewOfflineClient(ctx, proj.Name)
	stack := client.NewStack(c)
	require.NoError(t, proj.ToStack(c, stack))

	var namedOverride *client.Network
	for _, r := range stack.All() {
		net, ok := r.(*client.Network)
		if ok && net.Name() == "named-override" {
			namedOverride = net
			break
		}
	}

	require.NotNil(t, namedOverride, "named-override network should be in stack")
	assert.Equal(t, "incusbr0", namedOverride.Config.OverrideName)
	assert.Equal(t, "incusbr0", namedOverride.IncusName(), "initial incusName uses raw override")
}

// TestShmSizeAddsDeviceAtDevShm verifies that shm_size produces a tmpfs device at /dev/shm.
func TestShmSizeAddsDeviceAtDevShm(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	proj, err := project.New().Load(
		ctx, project.LoadWorkingDir(fixturePath("with-shm-size")),
	)
	require.NoError(t, err)

	c := client.NewOfflineClient(ctx, proj.Name)
	stack := client.NewStack(c)
	require.NoError(t, proj.ToStack(c, stack))

	var instance *client.Instance
	for _, r := range stack.All() {
		if inst, ok := r.(*client.Instance); ok {
			instance = inst
			break
		}
	}
	require.NotNil(t, instance)

	var shmDev *client.InstanceDevice
	for i := range instance.Config.Devices {
		if instance.Config.Devices[i].Name == "shm" {
			shmDev = &instance.Config.Devices[i]
			break
		}
	}
	require.NotNil(t, shmDev, "shm device should be present")
	assert.Equal(t, client.InstanceDeviceTypeTmpfs, shmDev.Config.DeviceType)
	assert.Equal(t, "/dev/shm", shmDev.Config.Tmpfs.Path)
	assert.Equal(t, "134217728", shmDev.Config.Tmpfs.Size)
}

// TestContainerNameUsedAsInstanceName verifies that container_name overrides the default {service}-{index} naming.
func TestContainerNameUsedAsInstanceName(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	proj, err := project.New().Load(
		ctx, project.LoadWorkingDir(fixturePath("with-container-name")),
	)
	require.NoError(t, err)

	c := client.NewOfflineClient(ctx, proj.Name)
	stack := client.NewStack(c)
	require.NoError(t, proj.ToStack(c, stack))

	var instanceName string
	for _, r := range stack.All() {
		if r.Kind() == client.KindInstance {
			instanceName = r.IncusName()
			break
		}
	}

	assert.Equal(t, "my-nginx", instanceName, "container_name should be used as instance name")
}

// TestOnlyServicesStopDoesNotCascadeToDependencies verifies that stop web does not pull in database.
func TestOnlyServicesStopDoesNotCascadeToDependencies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	proj, err := project.New().Load(ctx, project.LoadWorkingDir(fixturePath("wordpress")))
	require.NoError(t, err)

	c := client.NewOfflineClient(ctx, proj.Name)
	stack := client.NewStack(c)
	require.NoError(t, proj.ToStack(c, stack,
		project.ToStackOnlyServices([]string{"wordpress"}),
		project.ToStackReverse(),
		project.ToStackNoImages(),
	))

	names := instanceNamesInStack(stack)
	assert.Contains(t, names, "wordpress-1", "wordpress instance should be in stop stack")
	assert.NotContains(t, names, "db-1", "db must not be stopped when only wordpress is requested")
}

// TestOnlyServicesStopIncludesDependants verifies that stop db also pulls in wordpress.
func TestOnlyServicesStopIncludesDependants(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	proj, err := project.New().Load(ctx, project.LoadWorkingDir(fixturePath("wordpress")))
	require.NoError(t, err)

	c := client.NewOfflineClient(ctx, proj.Name)
	stack := client.NewStack(c)
	require.NoError(t, proj.ToStack(c, stack,
		project.ToStackOnlyServices([]string{"db"}),
		project.ToStackReverse(),
		project.ToStackWithDeps(),
		project.ToStackNoImages(),
	))

	names := instanceNamesInStack(stack)
	assert.Contains(t, names, "db-1", "db instance should be in stop stack")
	assert.Contains(t, names, "wordpress-1", "wordpress must also be stopped because it depends on db")
}

// TestOnlyServicesStartIncludesDependencies verifies that start wordpress also pulls in db.
func TestOnlyServicesStartIncludesDependencies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	proj, err := project.New().Load(ctx, project.LoadWorkingDir(fixturePath("wordpress")))
	require.NoError(t, err)

	c := client.NewOfflineClient(ctx, proj.Name)
	stack := client.NewStack(c)
	require.NoError(t, proj.ToStack(c, stack,
		project.ToStackOnlyServices([]string{"wordpress"}),
		project.ToStackWithDeps(),
		project.ToStackNoImages(),
	))

	names := instanceNamesInStack(stack)
	assert.Contains(t, names, "wordpress-1", "wordpress instance should be in start stack")
	assert.Contains(t, names, "db-1", "db must also be started because wordpress depends on it")
}

// TestOnlyServicesStartWithoutDepsExcludesDependencies verifies that, without
// ToStackWithDeps, start wordpress does not pull in db.
func TestOnlyServicesStartWithoutDepsExcludesDependencies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	proj, err := project.New().Load(ctx, project.LoadWorkingDir(fixturePath("wordpress")))
	require.NoError(t, err)

	c := client.NewOfflineClient(ctx, proj.Name)
	stack := client.NewStack(c)
	require.NoError(t, proj.ToStack(c, stack,
		project.ToStackOnlyServices([]string{"wordpress"}),
		project.ToStackNoImages(),
	))

	names := instanceNamesInStack(stack)
	assert.Contains(t, names, "wordpress-1", "wordpress instance should be in start stack")
	assert.NotContains(t, names, "db-1", "db must not be started without --with-deps")
}

// TestNewLoadOptionsAppliesOptions verifies the functional options set fields.
func TestNewLoadOptionsAppliesOptions(t *testing.T) {
	t.Parallel()

	options := project.NewLoadOptions(
		project.LoadName("custom"),
		project.LoadFiles([]string{"compose.yaml", "compose.override.yaml"}),
		project.LoadWorkingDir("/tmp/project"),
		project.LoadEnvFiles([]string{".env", "prod.env"}),
		project.LoadProfiles([]string{"dev"}),
		project.LoadOsEnv(),
	)

	assert.Equal(t, "custom", options.Name)
	assert.Equal(t, []string{"compose.yaml", "compose.override.yaml"}, options.Files)
	assert.Equal(t, "/tmp/project", options.WorkingDir)
	assert.Equal(t, []string{".env", "prod.env"}, options.EnvFiles)
	assert.True(t, options.OsEnv)
}

func TestServiceGraphOrdersDependencies(t *testing.T) {
	t.Parallel()

	services := types.Services{
		"db":  {Name: "db"},
		"api": {Name: "api", DependsOn: types.DependsOnConfig{"db": types.ServiceDependency{}}},
		"web": {Name: "web", DependsOn: types.DependsOnConfig{"api": types.ServiceDependency{}}},
	}

	order, err := project.ServiceGraph(services, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"db", "api", "web"}, order)

	reverse, err := project.ServiceGraph(services, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"web", "api", "db"}, reverse)
}

func TestServiceGraphReturnsEdgeErrors(t *testing.T) {
	t.Parallel()

	// Cycles are still an error.
	_, err := project.ServiceGraph(types.Services{
		"api": {Name: "api", DependsOn: types.DependsOnConfig{"web": types.ServiceDependency{}}},
		"web": {Name: "web", DependsOn: types.DependsOnConfig{"api": types.ServiceDependency{}}},
	}, false)
	assert.Error(t, err)
}

func TestServiceGraphSkipsMissingDependency(t *testing.T) {
	t.Parallel()

	// A dependency not present in the service set is silently skipped (filtered subset case).
	order, err := project.ServiceGraph(types.Services{
		"api": {Name: "api", DependsOn: types.DependsOnConfig{"db": types.ServiceDependency{}}},
	}, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"api"}, order)
}

func TestProjectConfigExtractsXIncus(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(context.Background(), project.LoadWorkingDir(fixturePath("with-project-options")))
	require.NoError(t, err)

	assert.Equal(t, map[string]string{
		"limits.cpu":              "5",
		"limits.memory":           "2149MB",
		"limits.virtual-machines": "0",
	}, proj.ProjectConfig())
}

func TestProjectConfigReturnsNilWithoutExtensions(t *testing.T) {
	t.Parallel()

	assert.Nil(t, (*project.Project)(nil).ProjectConfig())
	assert.Nil(t, (&project.Project{}).ProjectConfig())

	proj, err := project.New().Load(context.Background(), project.LoadWorkingDir(fixturePath("simple-nginx")))
	require.NoError(t, err)
	assert.Nil(t, proj.ProjectConfig())
}

func TestHealthdConfigExtractsXIncusCompose(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(context.Background(), project.LoadWorkingDir(fixturePath("with-healthd-config")))
	require.NoError(t, err)

	incusURL, network := proj.HealthdConfig()
	assert.Equal(t, "https://10.0.0.1:8443", incusURL)
	assert.Equal(t, "healthd:default", network)
}

func TestHealthdConfigEmptyWithoutExtension(t *testing.T) {
	t.Parallel()

	proj, err := project.New().Load(context.Background(), project.LoadWorkingDir(fixturePath("simple-nginx")))
	require.NoError(t, err)

	incusURL, network := proj.HealthdConfig()
	assert.Empty(t, incusURL)
	assert.Empty(t, network)
}

func TestToStackKeepsNetworkResourcesWithoutNetworkProfile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	proj, err := project.New().Load(ctx, project.LoadWorkingDir(fixturePath("simple-nginx")))
	require.NoError(t, err)

	c := client.NewOfflineClient(ctx, proj.Name)
	stack := client.NewStack(c)
	require.NoError(t, proj.ToStack(c, stack))

	var hasNetwork bool
	for _, resource := range stack.All() {
		if resource.Kind() == client.KindNetwork {
			hasNetwork = true
		}
	}
	assert.True(t, hasNetwork)
}
