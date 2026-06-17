package project_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

type LoadProjectTestSuite struct {
	suite.Suite
	fixturesDir string
	ctx         context.Context
}

func (s *LoadProjectTestSuite) SetupSuite() {
	// Get fixtures directory relative to project root.
	s.fixturesDir = filepath.Join("..", "test", "fixtures")
}

func (s *LoadProjectTestSuite) SetupTest() {
	// Reset context for each test.
	s.ctx = context.Background()
}

// fixturePath returns the path to a test fixture.
func (s *LoadProjectTestSuite) fixturePath(name string) string {
	return filepath.Join(s.fixturesDir, name)
}

// TestLoadBasicProject tests basic project loading.
func (s *LoadProjectTestSuite) TestLoadBasicProject() {
	proj, err := project.New().Load(
		s.ctx, project.LoadWorkingDir(s.fixturePath("simple-nginx")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)
	s.Equal("simple-nginx", proj.Name)
	s.Len(proj.Services, 1)

	service, exists := proj.Services["web"]
	s.True(exists, "web service should exist")
	s.Equal("web", service.Name)
	s.Equal("docker.io/nginx:alpine", service.Image)
}

// TestLoadWordPressStack tests WordPress stack with volumes and dependencies.
func (s *LoadProjectTestSuite) TestLoadWordPressStack() {
	proj, err := project.New().Load(
		s.ctx, project.LoadWorkingDir(s.fixturePath("wordpress")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)
	s.Equal("wordpress", proj.Name)
	s.Len(proj.Services, 2)

	// Check db service.
	db, exists := proj.Services["db"]
	s.True(exists, "db service should exist")
	s.Equal("docker.io/library/mysql:9.5.0", db.Image)
	s.Contains(db.Environment, "MYSQL_ROOT_PASSWORD")

	// Check wordpress service.
	wordpress, exists := proj.Services["wordpress"]
	s.True(exists, "wordpress service should exist")
	s.Equal("docker.io/library/wordpress:6.9.0-php8.1-apache", wordpress.Image)
	s.NotEmpty(wordpress.DependsOn)

	// Check volumes.
	s.Len(proj.Volumes, 2)
	s.Contains(proj.Volumes, "db_data")
	s.Contains(proj.Volumes, "wordpress_data")
}

// TestLoadPostgresRedisWithEnv tests PostgreSQL + Redis with environment variables.
func (s *LoadProjectTestSuite) TestLoadPostgresRedisWithEnv() {
	proj, err := project.New().Load(
		s.ctx, project.LoadWorkingDir(s.fixturePath("postgres-redis")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)
	s.Equal("postgres-redis", proj.Name)
	s.Len(proj.Services, 3)

	// Check postgres service.
	postgres, exists := proj.Services["postgres"]
	s.True(exists, "postgres service should exist")
	s.Contains(postgres.Environment, "POSTGRES_USER")
	s.Contains(postgres.Environment, "POSTGRES_PASSWORD")
	s.Contains(postgres.Environment, "POSTGRES_DB")
	s.NotNil(postgres.HealthCheck)

	// Check redis service.
	redis, exists := proj.Services["redis"]
	s.True(exists, "redis service should exist")
	s.NotNil(redis.HealthCheck)

	// Check api service.
	_, exists = proj.Services["api"]
	s.True(exists, "api service should exist")
}

// TestLoadNginxProxyMultiNetwork tests Nginx proxy with multiple networks.
func (s *LoadProjectTestSuite) TestLoadNginxProxyMultiNetwork() {
	proj, err := project.New().Load(
		s.ctx, project.LoadWorkingDir(s.fixturePath("nginx-proxy")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)
	s.Len(proj.Services, 3)

	// Check networks.
	s.Len(proj.Networks, 2)
	s.Contains(proj.Networks, "frontend")
	s.Contains(proj.Networks, "backend")

	// Verify backend network is internal.
	backend := proj.Networks["backend"]
	s.NotNil(backend)
	s.True(backend.Internal)
}

// TestLoadMicroservices tests microservices architecture.
func (s *LoadProjectTestSuite) TestLoadMicroservices() {
	proj, err := project.New().Load(
		s.ctx, project.LoadWorkingDir(s.fixturePath("microservices")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)
	s.Equal("microservices", proj.Name)

	// Should have many services.
	s.GreaterOrEqual(len(proj.Services), 8)

	// Check for different database types.
	_, hasAuthDB := proj.Services["auth-db"]
	s.True(hasAuthDB, "Should have auth-db (PostgreSQL)")

	_, hasUserDB := proj.Services["user-db"]
	s.True(hasUserDB, "Should have user-db (MySQL)")

	_, hasOrderDB := proj.Services["order-db"]
	s.True(hasOrderDB, "Should have order-db (MongoDB)")

	// Check multiple networks for isolation.
	s.GreaterOrEqual(len(proj.Networks), 5)
}

// TestLoadWithCustomProjectName tests custom project name.
func (s *LoadProjectTestSuite) TestLoadWithCustomProjectName() {
	proj, err := project.New().Load(
		s.ctx, project.LoadWorkingDir(s.fixturePath("simple-nginx")),
		project.LoadName("my-custom-project"),
	)

	s.Require().NoError(err)
	s.Equal("my-custom-project", proj.Name)
}

// TestLoadWithCustomFiles tests custom compose file path.
func (s *LoadProjectTestSuite) TestLoadWithCustomFiles() {
	composePath := filepath.Join(s.fixturePath("wordpress"), "compose.yaml")

	proj, err := project.New().Load(
		s.ctx, project.LoadFiles([]string{composePath}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)
	s.Len(proj.Services, 2)
}

// TestLoadWithDefaultEnvFile tests environment file loading.
func (s *LoadProjectTestSuite) TestLoadWithDefaultEnvFile() {
	proj, err := project.New().Load(

		s.ctx, project.LoadWorkingDir(s.fixturePath("with-env")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)

	// Environment variables should be loaded from .env.
	app, exists := proj.Services["app"]
	s.True(exists, "app service should exist")

	// Check that env vars from .env file are present.
	env := app.Environment
	s.Contains(env, "DB_HOST")
	s.Contains(env, "DB_PORT")
	s.Contains(env, "DB_NAME")
}

// TestLoadWithCustomEnvFile tests custom environment file.
func (s *LoadProjectTestSuite) TestLoadWithCustomEnvFile() {
	prodEnvPath := filepath.Join(s.fixturePath("with-env"), "production.env")

	proj, err := project.New().Load(
		s.ctx, project.LoadWorkingDir(s.fixturePath("with-env")),
		project.LoadEnvFiles([]string{prodEnvPath}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)

	// Should load production.env instead of .env.
	app, exists := proj.Services["app"]
	s.True(exists, "app service should exist")
	env := app.Environment

	// These values should come from production.env.
	s.Contains(env, "DB_HOST")
	s.Contains(env, "API_KEY")
	s.Contains(env, "API_URL")
}

// TestLoadWithMultipleEnvFiles tests multiple environment files.
func (s *LoadProjectTestSuite) TestLoadWithMultipleEnvFiles() {
	basePath := s.fixturePath("with-env")
	prodEnv := filepath.Join(basePath, "production.env")
	stagingEnv := filepath.Join(basePath, "staging.env")

	proj, err := project.New().Load(
		s.ctx,
		project.LoadWorkingDir(basePath),
		project.LoadEnvFiles([]string{prodEnv, stagingEnv}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)
}

// TestLoadWithoutProfiles tests profiles - no profiles (default services only).
func (s *LoadProjectTestSuite) TestLoadWithoutProfiles() {
	proj, err := project.New().Load(
		s.ctx,
		project.LoadWorkingDir(s.fixturePath("with-profiles")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)

	// Only services without profiles should be loaded.
	s.Len(proj.Services, 1)
	_, exists := proj.Services["web"]
	s.True(exists, "web service should exist")
}

// TestLoadWithSingleProfile tests profiles - single profile.
func (s *LoadProjectTestSuite) TestLoadWithSingleProfile() {
	proj, err := project.New().Load(
		s.ctx,
		project.LoadWorkingDir(s.fixturePath("with-profiles")),
		project.LoadProfiles([]string{"dev"}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)

	// Should have base service + dev profile services.
	s.GreaterOrEqual(len(proj.Services), 3)

	_, exists := proj.Services["web"]
	s.True(exists, "Should have web service")

	_, exists = proj.Services["webpack"]
	s.True(exists, "Should have webpack from dev profile")

	_, exists = proj.Services["hot-reload"]
	s.True(exists, "Should have hot-reload from dev profile")
}

// TestLoadWithMultipleProfiles tests profiles - multiple profiles.
func (s *LoadProjectTestSuite) TestLoadWithMultipleProfiles() {
	proj, err := project.New().Load(
		s.ctx,
		project.LoadWorkingDir(s.fixturePath("with-profiles")),
		project.LoadProfiles([]string{"dev", "monitoring"}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)

	// Base service.
	_, exists := proj.Services["web"]
	s.True(exists, "web service should exist")

	// Dev profile services.
	_, exists = proj.Services["webpack"]
	s.True(exists, "webpack service should exist")

	_, exists = proj.Services["hot-reload"]
	s.True(exists, "hot-reload service should exist")

	// Monitoring profile service.
	_, exists = proj.Services["prometheus"]
	s.True(exists, "prometheus service should exist")

	// Adminer is in both dev and debug, so it should appear with dev profile.
	_, exists = proj.Services["adminer"]
	s.True(exists, "adminer service should exist")
}

// TestLoadDevEnvironment tests development environment with profiles.
func (s *LoadProjectTestSuite) TestLoadDevEnvironment() {
	proj, err := project.New().Load(
		s.ctx,
		project.LoadWorkingDir(s.fixturePath("dev-environment")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)

	// Without profiles, should only have core services.
	s.Len(proj.Services, 2) // app and db
}

// TestLoadDevEnvironmentWithDebugProfile tests development environment with debug profile.
func (s *LoadProjectTestSuite) TestLoadDevEnvironmentWithDebugProfile() {
	proj, err := project.New().Load(
		s.ctx,
		project.LoadWorkingDir(s.fixturePath("dev-environment")),
		project.LoadProfiles([]string{"debug"}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)

	// Should have core + debug services.
	s.GreaterOrEqual(len(proj.Services), 4)

	_, exists := proj.Services["app"]
	s.True(exists, "app service should exist")

	_, exists = proj.Services["db"]
	s.True(exists, "db service should exist")

	_, exists = proj.Services["pgadmin"]
	s.True(exists, "pgadmin service should exist")

	_, exists = proj.Services["mailhog"]
	s.True(exists, "mailhog service should exist")
}

// TestLoadMultipleComposeFiles tests multiple compose files (base + override).
func (s *LoadProjectTestSuite) TestLoadMultipleComposeFiles() {
	basePath := s.fixturePath("multiple-files")
	baseFile := filepath.Join(basePath, "compose.yaml")
	overrideFile := filepath.Join(basePath, "compose.override.yaml")

	proj, err := project.New().Load(
		s.ctx,
		project.LoadFiles([]string{baseFile, overrideFile}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)

	// Should have merged services from both files.
	s.Len(proj.Services, 3) // app, db, adminer

	// Check that override values are applied.
	app, exists := proj.Services["app"]
	s.True(exists, "app service should exist")
	env := app.Environment
	// NODE_ENV should be overridden to development.
	s.Contains(env, "NODE_ENV")
	s.Contains(env, "HOT_RELOAD")
}

// TestLoadMultipleComposeFilesCustomOrder tests multiple compose files with custom order.
func (s *LoadProjectTestSuite) TestLoadMultipleComposeFilesCustomOrder() {
	basePath := s.fixturePath("multiple-files")
	baseFile := filepath.Join(basePath, "compose.yaml")
	testFile := filepath.Join(basePath, "compose.test.yaml")

	proj, err := project.New().Load(
		s.ctx,
		project.LoadFiles([]string{baseFile, testFile}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)

	// Should have test-runner service from compose.test.yaml.
	_, exists := proj.Services["test-runner"]
	s.True(exists, "test-runner service should exist")
}

// TestLoadWithResourceLimits tests loading a compose file with deploy resource limits.
func (s *LoadProjectTestSuite) TestLoadWithResourceLimits() {
	proj, err := project.New().Load(
		s.ctx, project.LoadWorkingDir(s.fixturePath("with-resources")),
	)
	s.Require().NoError(err)
	s.Require().NotNil(proj)
	s.Len(proj.Services, 2)

	limited := proj.Services["limited"]
	s.Require().NotNil(limited.Deploy)
	s.Require().NotNil(limited.Deploy.Resources.Limits)
	s.Equal(float32(0.5), limited.Deploy.Resources.Limits.NanoCPUs.Value())
	s.Equal(int64(512<<20), int64(limited.Deploy.Resources.Limits.MemoryBytes))

	pinned := proj.Services["pinned"]
	s.Require().NotNil(pinned.Deploy)
	s.Require().NotNil(pinned.Deploy.Resources.Limits)
	s.Equal(float32(2), pinned.Deploy.Resources.Limits.NanoCPUs.Value())
	s.Equal(int64(1<<30), int64(pinned.Deploy.Resources.Limits.MemoryBytes))
}

// TestLoadInvalidComposeFile tests invalid compose file.
func (s *LoadProjectTestSuite) TestLoadInvalidComposeFile() {
	_, err := project.New().Load(
		s.ctx,
		project.LoadWorkingDir(s.fixturePath("invalid")),
	)

	// Should return an error for invalid compose file.
	s.Error(err)
}

// TestLoadMissingComposeFile tests missing compose file.
func (s *LoadProjectTestSuite) TestLoadMissingComposeFile() {
	_, err := project.New().Load(
		s.ctx,
		project.LoadWorkingDir(s.fixturePath("nonexistent")),
	)

	s.Error(err)
}

// TestLoadWithAllOptions tests all options combined.
func (s *LoadProjectTestSuite) TestLoadWithAllOptions() {
	basePath := s.fixturePath("postgres-redis")
	composePath := filepath.Join(basePath, "compose.yaml")
	envPath := filepath.Join(basePath, ".env")

	proj, err := project.New().Load(
		s.ctx,
		project.LoadName("my-combined-project"),
		project.LoadFiles([]string{composePath}),
		project.LoadWorkingDir(basePath),
		project.LoadEnvFiles([]string{envPath}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)
	s.Equal("my-combined-project", proj.Name)
	s.Len(proj.Services, 3)
}

// TestLoadWithSecrets tests loading a compose file with secrets.
func (s *LoadProjectTestSuite) TestLoadWithSecrets() {
	proj, err := project.New().Load(
		s.ctx, project.LoadWorkingDir(s.fixturePath("with-secrets")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)
	s.Equal("with-secrets", proj.Name)

	// Check secrets are defined.
	s.Len(proj.Secrets, 2)
	s.Contains(proj.Secrets, "db_password")
	s.Contains(proj.Secrets, "api_key")

	// Check db_password secret (file-based).
	dbSecret := proj.Secrets["db_password"]
	s.NotEmpty(dbSecret.File)

	// Check api_key secret (file-based).
	apiSecret := proj.Secrets["api_key"]
	s.NotEmpty(apiSecret.File)

	// Check service has secrets configured.
	app, exists := proj.Services["app"]
	s.True(exists, "app service should exist")
	s.Len(app.Secrets, 2)

	// Check first secret (simple reference).
	s.Equal("db_password", app.Secrets[0].Source)

	// Check second secret (with custom target).
	s.Equal("api_key", app.Secrets[1].Source)
	s.Equal("/app/secrets/api.key", app.Secrets[1].Target)
	s.Equal("1000", app.Secrets[1].UID)
	s.Equal("1000", app.Secrets[1].GID)
	s.NotNil(app.Secrets[1].Mode)
}

// TestLoadWithRestartPolicies tests loading a compose file with restart policies.
func (s *LoadProjectTestSuite) TestLoadWithRestartPolicies() {
	proj, err := project.New().Load(
		s.ctx, project.LoadWorkingDir(s.fixturePath("with-restart")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)
	s.Equal("with-restart", proj.Name)
	s.Len(proj.Services, 5)

	// Check restart policies are parsed.
	always := proj.Services["always-restart"]
	s.Equal("always", always.Restart)

	onFailure := proj.Services["on-failure-restart"]
	s.Equal("on-failure", onFailure.Restart)

	unlessStopped := proj.Services["unless-stopped-restart"]
	s.Equal("unless-stopped", unlessStopped.Restart)

	noRestart := proj.Services["no-restart"]
	s.Equal("no", noRestart.Restart)

	defaultRestart := proj.Services["default-restart"]
	s.Equal("", defaultRestart.Restart)
}

// TestLoadWithXIncusOptions tests loading a compose file with x-incus extensions.
func (s *LoadProjectTestSuite) TestLoadWithXIncusOptions() {
	proj, err := project.New().Load(
		s.ctx, project.LoadWorkingDir(s.fixturePath("with-incus-options")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(proj)
	s.Equal("with-incus-options", proj.Name)
	s.Len(proj.Services, 2)

	// Verify services loaded
	web, exists := proj.Services["web"]
	s.True(exists, "web service should exist")
	s.Equal("docker.io/nginx:alpine", web.Image)

	database, exists := proj.Services["database"]
	s.True(exists, "database service should exist")
	s.Equal("docker.io/nginx:alpine", database.Image)
}

func (s *LoadProjectTestSuite) TestExternalNetworkOverrideNameParsed() {
	proj, err := project.New().Load(
		s.ctx, project.LoadWorkingDir(s.fixturePath("with-external-network")),
	)
	s.Require().NoError(err)

	c := client.NewOfflineClient(s.ctx, proj.Name)
	stack := client.NewStack(c)
	s.Require().NoError(proj.ToStack(c, stack))

	var namedOverride *client.Network
	for _, r := range stack.All() {
		net, ok := r.(*client.Network)
		if ok && net.Name() == "named-override" {
			namedOverride = net
			break
		}
	}

	s.Require().NotNil(namedOverride, "named-override network should be in stack")
	s.Equal("incusbr0", namedOverride.Config.OverrideName)
	s.Equal("incusbr0", namedOverride.IncusName(), "initial incusName uses raw override")
}

// TestShmSizeAddsDeviceAtDevShm verifies that shm_size produces a tmpfs device at /dev/shm.
func (s *LoadProjectTestSuite) TestShmSizeAddsDeviceAtDevShm() {
	proj, err := project.New().Load(
		s.ctx, project.LoadWorkingDir(s.fixturePath("with-shm-size")),
	)
	s.Require().NoError(err)

	c := client.NewOfflineClient(s.ctx, proj.Name)
	stack := client.NewStack(c)
	s.Require().NoError(proj.ToStack(c, stack))

	var instance *client.Instance
	for _, r := range stack.All() {
		if inst, ok := r.(*client.Instance); ok {
			instance = inst
			break
		}
	}
	s.Require().NotNil(instance)

	var shmDev *client.InstanceDevice
	for i := range instance.Config.Devices {
		if instance.Config.Devices[i].Name == "shm" {
			shmDev = &instance.Config.Devices[i]
			break
		}
	}
	s.Require().NotNil(shmDev, "shm device should be present")
	s.Equal(client.InstanceDeviceTypeTmpfs, shmDev.Config.DeviceType)
	s.Equal("/dev/shm", shmDev.Config.Tmpfs.Path)
	s.Equal("134217728", shmDev.Config.Tmpfs.Size)
}

// TestContainerNameUsedAsInstanceName verifies that container_name overrides the default {service}-{index} naming.
func (s *LoadProjectTestSuite) TestContainerNameUsedAsInstanceName() {
	proj, err := project.New().Load(
		s.ctx, project.LoadWorkingDir(s.fixturePath("with-container-name")),
	)
	s.Require().NoError(err)

	c := client.NewOfflineClient(s.ctx, proj.Name)
	stack := client.NewStack(c)
	s.Require().NoError(proj.ToStack(c, stack))

	var instanceName string
	for _, r := range stack.All() {
		if r.Kind() == client.KindInstance {
			instanceName = r.IncusName()
			break
		}
	}

	s.Equal("my-nginx", instanceName, "container_name should be used as instance name")
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

// TestOnlyServicesStopDoesNotCascadeToDependencies verifies that stop web does not pull in database.
func (s *LoadProjectTestSuite) TestOnlyServicesStopDoesNotCascadeToDependencies() {
	proj, err := project.New().Load(s.ctx, project.LoadWorkingDir(s.fixturePath("wordpress")))
	s.Require().NoError(err)

	c := client.NewOfflineClient(s.ctx, proj.Name)
	stack := client.NewStack(c)
	s.Require().NoError(proj.ToStack(c, stack,
		project.ToStackOnlyServices([]string{"wordpress"}),
		project.ToStackReverse(),
		project.ToStackNoImages(),
	))

	names := instanceNamesInStack(stack)
	s.Contains(names, "wordpress-1", "wordpress instance should be in stop stack")
	s.NotContains(names, "db-1", "db must not be stopped when only wordpress is requested")
}

// TestOnlyServicesStopIncludesDependants verifies that stop db also pulls in wordpress.
func (s *LoadProjectTestSuite) TestOnlyServicesStopIncludesDependants() {
	proj, err := project.New().Load(s.ctx, project.LoadWorkingDir(s.fixturePath("wordpress")))
	s.Require().NoError(err)

	c := client.NewOfflineClient(s.ctx, proj.Name)
	stack := client.NewStack(c)
	s.Require().NoError(proj.ToStack(c, stack,
		project.ToStackOnlyServices([]string{"db"}),
		project.ToStackReverse(),
		project.ToStackWithDeps(),
		project.ToStackNoImages(),
	))

	names := instanceNamesInStack(stack)
	s.Contains(names, "db-1", "db instance should be in stop stack")
	s.Contains(names, "wordpress-1", "wordpress must also be stopped because it depends on db")
}

// TestOnlyServicesStartIncludesDependencies verifies that start wordpress also pulls in db.
func (s *LoadProjectTestSuite) TestOnlyServicesStartIncludesDependencies() {
	proj, err := project.New().Load(s.ctx, project.LoadWorkingDir(s.fixturePath("wordpress")))
	s.Require().NoError(err)

	c := client.NewOfflineClient(s.ctx, proj.Name)
	stack := client.NewStack(c)
	s.Require().NoError(proj.ToStack(c, stack,
		project.ToStackOnlyServices([]string{"wordpress"}),
		project.ToStackWithDeps(),
		project.ToStackNoImages(),
	))

	names := instanceNamesInStack(stack)
	s.Contains(names, "wordpress-1", "wordpress instance should be in start stack")
	s.Contains(names, "db-1", "db must also be started because wordpress depends on it")
}

// TestOnlyServicesStartWithoutDepsExcludesDependencies verifies that, without
// ToStackWithDeps, start wordpress does not pull in db.
func (s *LoadProjectTestSuite) TestOnlyServicesStartWithoutDepsExcludesDependencies() {
	proj, err := project.New().Load(s.ctx, project.LoadWorkingDir(s.fixturePath("wordpress")))
	s.Require().NoError(err)

	c := client.NewOfflineClient(s.ctx, proj.Name)
	stack := client.NewStack(c)
	s.Require().NoError(proj.ToStack(c, stack,
		project.ToStackOnlyServices([]string{"wordpress"}),
		project.ToStackNoImages(),
	))

	names := instanceNamesInStack(stack)
	s.Contains(names, "wordpress-1", "wordpress instance should be in start stack")
	s.NotContains(names, "db-1", "db must not be started without --with-deps")
}

// TestLoadProjectSuite runs the test suite.
func TestLoadProjectSuite(t *testing.T) {
	// Skip if fixtures don't exist.
	if _, err := os.Stat("../test/fixtures"); os.IsNotExist(err) {
		t.Skip("Fixtures directory not found")
	}

	suite.Run(t, new(LoadProjectTestSuite))
}
