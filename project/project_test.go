package project_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"gitlab.com/r3j0/incus-compose/project"
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

// TestLoadProjectSuite runs the test suite.
func TestLoadProjectSuite(t *testing.T) {
	// Skip if fixtures don't exist.
	if _, err := os.Stat("../test/fixtures"); os.IsNotExist(err) {
		t.Skip("Fixtures directory not found")
	}

	suite.Run(t, new(LoadProjectTestSuite))
}
