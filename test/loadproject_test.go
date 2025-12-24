package test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"gitlab.com/r3j0/incuscompose"
)

type LoadProjectTestSuite struct {
	suite.Suite
	fixturesDir string
	ctx         context.Context
}

func (s *LoadProjectTestSuite) SetupSuite() {
	// Get fixtures directory relative to test file
	s.fixturesDir = filepath.Join("fixtures")
	s.ctx = context.Background()
}

func (s *LoadProjectTestSuite) SetupTest() {
	// Reset context for each test
	s.ctx = context.Background()
}

// fixturePath returns the path to a test fixture.
func (s *LoadProjectTestSuite) fixturePath(name string) string {
	return filepath.Join(s.fixturesDir, name)
}

// TestLoadBasicProject tests basic project loading.
func (s *LoadProjectTestSuite) TestLoadBasicProject() {
	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(s.fixturePath("hello_world")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)
	s.Equal("hello_world", project.Name)
	s.Len(project.Services, 1)

	service, exists := project.Services["hello_world"]
	s.True(exists, "hello_world service should exist")
	s.Equal("hello_world", service.Name)
	s.Equal("docker.io/hello-world:latest", service.Image)
}

// TestLoadWordPressStack tests WordPress stack with volumes and dependencies.
func (s *LoadProjectTestSuite) TestLoadWordPressStack() {
	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(s.fixturePath("wordpress")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)
	s.Equal("wordpress", project.Name)
	s.Len(project.Services, 2)

	// Check db service
	db, exists := project.Services["db"]
	s.True(exists, "db service should exist")
	s.Equal("docker.io/library/mysql:8.0", db.Image)
	s.Contains(db.Environment, "MYSQL_ROOT_PASSWORD")

	// Check wordpress service
	wordpress, exists := project.Services["wordpress"]
	s.True(exists, "wordpress service should exist")
	s.Equal("docker.io/library/wordpress:latest", wordpress.Image)
	s.NotEmpty(wordpress.DependsOn)

	// Check volumes
	s.Len(project.Volumes, 2)
	s.Contains(project.Volumes, "db_data")
	s.Contains(project.Volumes, "wordpress_data")
}

// TestLoadPostgresRedisWithEnv tests PostgreSQL + Redis with environment variables.
func (s *LoadProjectTestSuite) TestLoadPostgresRedisWithEnv() {
	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(s.fixturePath("postgres_redis")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)
	s.Equal("postgres_redis", project.Name)
	s.Len(project.Services, 3)

	// Check postgres service
	postgres, exists := project.Services["postgres"]
	s.True(exists, "postgres service should exist")
	s.Contains(postgres.Environment, "POSTGRES_USER")
	s.Contains(postgres.Environment, "POSTGRES_PASSWORD")
	s.Contains(postgres.Environment, "POSTGRES_DB")
	s.NotNil(postgres.HealthCheck)

	// Check redis service
	redis, exists := project.Services["redis"]
	s.True(exists, "redis service should exist")
	s.NotNil(redis.HealthCheck)

	// Check api service
	_, exists = project.Services["api"]
	s.True(exists, "api service should exist")
}

// TestLoadNginxProxyMultiNetwork tests Nginx proxy with multiple networks.
func (s *LoadProjectTestSuite) TestLoadNginxProxyMultiNetwork() {
	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(s.fixturePath("nginx_proxy")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)
	s.Len(project.Services, 3)

	// Check networks
	s.Len(project.Networks, 2)
	s.Contains(project.Networks, "frontend")
	s.Contains(project.Networks, "backend")

	// Verify backend network is internal
	backend := project.Networks["backend"]
	s.NotNil(backend)
	s.True(backend.Internal)
}

// TestLoadMicroservices tests microservices architecture.
func (s *LoadProjectTestSuite) TestLoadMicroservices() {
	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(s.fixturePath("microservices")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)
	s.Equal("microservices", project.Name)

	// Should have many services
	s.GreaterOrEqual(len(project.Services), 8)

	// Check for different database types
	_, hasAuthDB := project.Services["auth-db"]
	s.True(hasAuthDB, "Should have auth-db (PostgreSQL)")

	_, hasUserDB := project.Services["user-db"]
	s.True(hasUserDB, "Should have user-db (MySQL)")

	_, hasOrderDB := project.Services["order-db"]
	s.True(hasOrderDB, "Should have order-db (MongoDB)")

	// Check multiple networks for isolation
	s.GreaterOrEqual(len(project.Networks), 5)
}

// TestLoadWithCustomProjectName tests custom project name.
func (s *LoadProjectTestSuite) TestLoadWithCustomProjectName() {
	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(s.fixturePath("hello_world")),
		incuscompose.LoadProjectName("my-custom-project"),
	)

	s.Require().NoError(err)
	s.Equal("my-custom-project", project.Name)
}

// TestLoadWithCustomFiles tests custom compose file path.
func (s *LoadProjectTestSuite) TestLoadWithCustomFiles() {
	composePath := filepath.Join(s.fixturePath("wordpress"), "compose.yaml")

	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectFiles([]string{composePath}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)
	s.Len(project.Services, 2)
}

// TestLoadWithDefaultEnvFile tests environment file loading.
func (s *LoadProjectTestSuite) TestLoadWithDefaultEnvFile() {
	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(s.fixturePath("with_env")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)

	// Environment variables should be loaded from .env
	app, exists := project.Services["app"]
	s.True(exists, "app service should exist")

	// Check that env vars from .env file are present
	env := app.Environment
	s.Contains(env, "DB_HOST")
	s.Contains(env, "DB_PORT")
	s.Contains(env, "DB_NAME")
}

// TestLoadWithCustomEnvFile tests custom environment file.
func (s *LoadProjectTestSuite) TestLoadWithCustomEnvFile() {
	prodEnvPath := filepath.Join(s.fixturePath("with_env"), "production.env")

	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(s.fixturePath("with_env")),
		incuscompose.LoadProjectEnvFiles([]string{prodEnvPath}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)

	// Should load production.env instead of .env
	app, exists := project.Services["app"]
	s.True(exists, "app service should exist")
	env := app.Environment

	// These values should come from production.env
	s.Contains(env, "DB_HOST")
	s.Contains(env, "API_KEY")
	s.Contains(env, "API_URL")
}

// TestLoadWithMultipleEnvFiles tests multiple environment files.
func (s *LoadProjectTestSuite) TestLoadWithMultipleEnvFiles() {
	basePath := s.fixturePath("with_env")
	prodEnv := filepath.Join(basePath, "production.env")
	stagingEnv := filepath.Join(basePath, "staging.env")

	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(basePath),
		incuscompose.LoadProjectEnvFiles([]string{prodEnv, stagingEnv}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)
}

// TestLoadWithoutProfiles tests profiles - no profiles (default services only).
func (s *LoadProjectTestSuite) TestLoadWithoutProfiles() {
	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(s.fixturePath("with_profiles")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)

	// Only services without profiles should be loaded
	s.Len(project.Services, 1)
	_, exists := project.Services["web"]
	s.True(exists, "web service should exist")
}

// TestLoadWithSingleProfile tests profiles - single profile.
func (s *LoadProjectTestSuite) TestLoadWithSingleProfile() {
	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(s.fixturePath("with_profiles")),
		incuscompose.LoadProjectProfiles([]string{"dev"}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)

	// Should have base service + dev profile services
	s.GreaterOrEqual(len(project.Services), 3)

	_, exists := project.Services["web"]
	s.True(exists, "Should have web service")

	_, exists = project.Services["webpack"]
	s.True(exists, "Should have webpack from dev profile")

	_, exists = project.Services["hot-reload"]
	s.True(exists, "Should have hot-reload from dev profile")
}

// TestLoadWithMultipleProfiles tests profiles - multiple profiles.
func (s *LoadProjectTestSuite) TestLoadWithMultipleProfiles() {
	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(s.fixturePath("with_profiles")),
		incuscompose.LoadProjectProfiles([]string{"dev", "monitoring"}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)

	// Base service
	_, exists := project.Services["web"]
	s.True(exists, "web service should exist")

	// Dev profile services
	_, exists = project.Services["webpack"]
	s.True(exists, "webpack service should exist")

	_, exists = project.Services["hot-reload"]
	s.True(exists, "hot-reload service should exist")

	// Monitoring profile service
	_, exists = project.Services["prometheus"]
	s.True(exists, "prometheus service should exist")

	// Adminer is in both dev and debug, so it should appear with dev profile
	_, exists = project.Services["adminer"]
	s.True(exists, "adminer service should exist")
}

// TestLoadDevEnvironment tests development environment with profiles.
func (s *LoadProjectTestSuite) TestLoadDevEnvironment() {
	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(s.fixturePath("dev_environment")),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)

	// Without profiles, should only have core services
	s.Len(project.Services, 2) // app and db
}

// TestLoadDevEnvironmentWithDebugProfile tests development environment with debug profile.
func (s *LoadProjectTestSuite) TestLoadDevEnvironmentWithDebugProfile() {
	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(s.fixturePath("dev_environment")),
		incuscompose.LoadProjectProfiles([]string{"debug"}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)

	// Should have core + debug services
	s.GreaterOrEqual(len(project.Services), 4)

	_, exists := project.Services["app"]
	s.True(exists, "app service should exist")

	_, exists = project.Services["db"]
	s.True(exists, "db service should exist")

	_, exists = project.Services["pgadmin"]
	s.True(exists, "pgadmin service should exist")

	_, exists = project.Services["mailhog"]
	s.True(exists, "mailhog service should exist")
}

// TestLoadMultipleComposeFiles tests multiple compose files (base + override).
func (s *LoadProjectTestSuite) TestLoadMultipleComposeFiles() {
	basePath := s.fixturePath("multiple_files")
	baseFile := filepath.Join(basePath, "compose.yaml")
	overrideFile := filepath.Join(basePath, "compose.override.yaml")

	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectFiles([]string{baseFile, overrideFile}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)

	// Should have merged services from both files
	s.Len(project.Services, 3) // app, db, adminer

	// Check that override values are applied
	app, exists := project.Services["app"]
	s.True(exists, "app service should exist")
	env := app.Environment
	// NODE_ENV should be overridden to development
	s.Contains(env, "NODE_ENV")
	s.Contains(env, "HOT_RELOAD")
}

// TestLoadMultipleComposeFilesCustomOrder tests multiple compose files with custom order.
func (s *LoadProjectTestSuite) TestLoadMultipleComposeFilesCustomOrder() {
	basePath := s.fixturePath("multiple_files")
	baseFile := filepath.Join(basePath, "compose.yaml")
	testFile := filepath.Join(basePath, "compose.test.yaml")

	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectFiles([]string{baseFile, testFile}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)

	// Should have test-runner service from compose.test.yaml
	_, exists := project.Services["test-runner"]
	s.True(exists, "test-runner service should exist")
}

// TestLoadInvalidComposeFile tests invalid compose file.
func (s *LoadProjectTestSuite) TestLoadInvalidComposeFile() {
	_, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(s.fixturePath("invalid")),
	)

	// Should return an error for invalid compose file
	s.Error(err)
}

// TestLoadMissingComposeFile tests missing compose file.
func (s *LoadProjectTestSuite) TestLoadMissingComposeFile() {
	_, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectWorkingDir(s.fixturePath("nonexistent")),
	)

	s.Error(err)
}

// TestLoadWithAllOptions tests all options combined.
func (s *LoadProjectTestSuite) TestLoadWithAllOptions() {
	basePath := s.fixturePath("postgres_redis")
	composePath := filepath.Join(basePath, "compose.yaml")
	envPath := filepath.Join(basePath, ".env")

	project, err := incuscompose.LoadProject(
		s.ctx,
		incuscompose.LoadProjectName("my-combined-project"),
		incuscompose.LoadProjectFiles([]string{composePath}),
		incuscompose.LoadProjectWorkingDir(basePath),
		incuscompose.LoadProjectEnvFiles([]string{envPath}),
	)

	s.Require().NoError(err)
	s.Require().NotNil(project)
	s.Equal("my-combined-project", project.Name)
	s.Len(project.Services, 3)
}

// TestLoadProjectSuite runs the test suite.
func TestLoadProjectSuite(t *testing.T) {
	// Skip if fixtures don't exist
	if _, err := os.Stat("fixtures"); os.IsNotExist(err) {
		t.Skip("Fixtures directory not found")
	}

	suite.Run(t, new(LoadProjectTestSuite))
}
