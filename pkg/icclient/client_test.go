package icclient_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lxc/incus/v6/shared/api"
	"github.com/stretchr/testify/suite"

	"gitlab.com/r3j0/incuscompose/pkg/icclient"
)

// ClientTestSuite is the test suite for icclient package.
type ClientTestSuite struct {
	suite.Suite
	ctx    context.Context
	client *icclient.Client
	logger *slog.Logger
	url    string
}

// SetupSuite runs once before all tests in the suite.
func (s *ClientTestSuite) SetupSuite() {
	s.ctx = context.Background()
	s.logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError, // Quiet during tests
	}))

	// Check for URL-based connection (preferred for nested testing)
	testURL, ok := os.LookupEnv("INCUS_COMPOSE_URL")
	if !ok {
		s.T().Skip("Skipping icclient integration test - INCUS_COMPOSE_URL is not set.")
		return
	}
	s.url = testURL

	// Get project root
	_, filename, _, _ := runtime.Caller(0)
	projectRoot := filepath.Dir(filepath.Dir(filepath.Dir(filename)))

	opts := []icclient.Option{
		icclient.InsecureSkipVerify(),
	}

	// Add TLS client certificate if provided
	if cert, ok := os.LookupEnv("INCUS_COMPOSE_CERT"); ok {
		if !filepath.IsAbs(cert) {
			cert = filepath.Join(projectRoot, cert)
		}
		opts = append(opts, icclient.TLSClientCert(cert))
	}
	if key, ok := os.LookupEnv("INCUS_COMPOSE_KEY"); ok {
		if !filepath.IsAbs(key) {
			key = filepath.Join(projectRoot, key)
		}
		opts = append(opts, icclient.TLSClientKey(key))
	}

	// Create and connect client
	s.client = icclient.New(s.ctx, s.logger, testURL, opts...)
	err := s.client.Connect()
	s.Require().NoError(err, "Failed to connect to test Incus server")
}

// TearDownSuite runs once after all tests in the suite.
func (s *ClientTestSuite) TearDownSuite() {
	// Cleanup any test projects
	if s.client != nil {
		// Clean up test projects
		projects := []string{"test-icclient", "test-network", "test-volume"}
		for _, proj := range projects {
			_ = s.client.GlobalIncus().DeleteProject(proj)
		}
	}
}

// TestClientTestSuite runs the test suite.
func TestClientTestSuite(t *testing.T) {
	suite.Run(t, new(ClientTestSuite))
}

// TestConnect tests basic connection.
func (s *ClientTestSuite) TestConnect() {
	s.Require().NotNil(s.client)
	s.Require().NotNil(s.client.Incus())
	s.Require().NotNil(s.client.GlobalIncus())
}

// TestIsRemote tests remote detection.
func (s *ClientTestSuite) TestIsRemote() {
	// With INCUS_COMPOSE_URL set, we're always remote (HTTPS)
	s.True(s.client.IsRemote(), "Connection via HTTPS should be detected as remote")
}

// TestContextIntegration tests storing and retrieving client from context.
func (s *ClientTestSuite) TestContextIntegration() {
	// Store client in context
	ctx := s.client.ToContext(s.ctx)

	// Retrieve client from context
	retrieved, err := icclient.FromContext(ctx)
	s.Require().NoError(err)
	s.Require().Equal(s.client, retrieved)

	// Test error when not in context
	emptyCtx := context.Background()
	_, err = icclient.FromContext(emptyCtx)
	s.Require().Error(err)
	s.Contains(err.Error(), "failed to get the client from the context")
}

// TestEnsureProject tests project creation and validation.
func (s *ClientTestSuite) TestEnsureProject() {
	projectName := "test-icclient"

	// Create project
	incusProject, err := s.client.EnsureProject(projectName, icclient.EnsureProjectCreate())
	s.Require().NoError(err)
	s.Require().NotEmpty(incusProject)

	// Verify project exists
	projects, err := s.client.GlobalIncus().GetProjectNames()
	s.Require().NoError(err)
	s.Contains(projects, incusProject)

	// Switch to project
	err = s.client.UseProject(projectName)
	s.Require().NoError(err)
	s.True(s.client.HasProject())

	// Ensure again (should not error)
	_, err = s.client.EnsureProject(projectName)
	s.Require().NoError(err)

	// Cleanup
	err = s.client.GlobalIncus().DeleteProject(incusProject)
	s.Require().NoError(err)
}

// TestEnsureProjectWithoutCreate tests project validation without creation.
func (s *ClientTestSuite) TestEnsureProjectWithoutCreate() {
	projectName := "nonexistent-project"

	// Should error when project doesn't exist and Create=false
	_, err := s.client.EnsureProject(projectName)
	s.Require().Error(err)
	s.Contains(err.Error(), "does not exist")
}

// TestEnsureNetwork tests network creation.
func (s *ClientTestSuite) TestEnsureNetwork() {
	projectName := "test-network"

	// Create and switch to test project
	_, err := s.client.EnsureProject(projectName, icclient.EnsureProjectCreate())
	s.Require().NoError(err)
	err = s.client.UseProject(projectName)
	s.Require().NoError(err)

	// Create network
	networkName := "testnet"
	incusNetworkName, err := s.client.EnsureNetwork(networkName)
	s.Require().NoError(err)
	s.Require().NotEmpty(incusNetworkName)

	// Verify network exists
	network, _, err := s.client.Incus().GetNetwork(incusNetworkName)
	s.Require().NoError(err)
	s.Require().Equal(incusNetworkName, network.Name)

	// Ensure again (should be idempotent)
	incusNetworkName2, err := s.client.EnsureNetwork(networkName)
	s.Require().NoError(err)
	s.Require().Equal(incusNetworkName, incusNetworkName2)

	// Cleanup
	err = s.client.RemoveNetwork(networkName)
	s.Require().NoError(err)

	err = s.client.GlobalIncus().DeleteProject(projectName)
	s.Require().NoError(err)
}

// TestRemoveNetworkNonexistent tests removing a network that doesn't exist.
func (s *ClientTestSuite) TestRemoveNetworkNonexistent() {
	projectName := "test-network-remove"

	// Create and switch to test project
	_, err := s.client.EnsureProject(projectName, icclient.EnsureProjectCreate())
	s.Require().NoError(err)
	err = s.client.UseProject(projectName)
	s.Require().NoError(err)

	// Remove nonexistent network (should not error)
	err = s.client.RemoveNetwork("nonexistent")
	s.Require().NoError(err)

	// Cleanup
	err = s.client.GlobalIncus().DeleteProject(projectName)
	s.Require().NoError(err)
}

// TestEnsurePoolVolume tests storage volume creation.
func (s *ClientTestSuite) TestEnsurePoolVolume() {
	projectName := "test-volume"

	// Create and switch to test project
	_, err := s.client.EnsureProject(projectName, icclient.EnsureProjectCreate())
	s.Require().NoError(err)
	err = s.client.UseProject(projectName)
	s.Require().NoError(err)

	// Create volume
	volumeName := "test-vol"
	uid := "1000"
	gid := "1000"

	volume, etag, err := s.client.EnsurePoolVolume(volumeName, uid, gid, "")
	s.Require().NoError(err)
	s.Require().NotNil(volume)
	s.Require().NotEmpty(etag)

	// Verify volume config
	s.Equal("true", volume.Config["security.shifted"])
	s.Equal(uid, volume.Config["initial.uid"])
	s.Equal(gid, volume.Config["initial.gid"])

	// Ensure again (should be idempotent)
	volume2, _, err := s.client.EnsurePoolVolume(volumeName, uid, gid, "")
	s.Require().NoError(err)
	s.Require().Equal(volume.Name, volume2.Name)

	// Cleanup
	err = s.client.RemovePoolVolume(volumeName, "")
	s.Require().NoError(err)

	err = s.client.GlobalIncus().DeleteProject(projectName)
	s.Require().NoError(err)
}

// TestRemovePoolVolumeNonexistent tests removing a volume that doesn't exist.
func (s *ClientTestSuite) TestRemovePoolVolumeNonexistent() {
	projectName := "test-volume-remove"

	// Create and switch to test project
	_, err := s.client.EnsureProject(projectName, icclient.EnsureProjectCreate())
	s.Require().NoError(err)
	err = s.client.UseProject(projectName)
	s.Require().NoError(err)

	// Remove nonexistent volume (should not error)
	err = s.client.RemovePoolVolume("nonexistent", "")
	s.Require().NoError(err)

	// Cleanup
	err = s.client.GlobalIncus().DeleteProject(projectName)
	s.Require().NoError(err)
}

// TestAttachBindMountRemote tests that bind mounts are blocked on remote connections.
func (s *ClientTestSuite) TestAttachBindMountRemote() {
	s.Require().True(s.client.IsRemote(), "Test requires remote connection")

	// Create a dummy instance struct (won't actually create it)
	instance := &api.Instance{
		Name: "test",
	}

	// Attempt to attach bind mount should fail
	err := s.client.AttachBindMount(instance, "", "/tmp/test", "/data", false)
	s.Require().Error(err)
	s.Contains(err.Error(), "bind mounts are not supported with remote Incus connections")
	s.Contains(err.Error(), "/tmp/test")
}

// TestIncusServiceName tests service name sanitization.
func (s *ClientTestSuite) TestIncusServiceName() {
	tests := []struct {
		name        string
		service     icclient.ServiceConfig
		expected    string
		description string
	}{
		{
			name: "simple name",
			service: icclient.ServiceConfig{
				Name: "web",
			},
			expected:    "web",
			description: "Simple names should pass through",
		},
		{
			name: "underscore replacement",
			service: icclient.ServiceConfig{
				Name: "my_service",
			},
			expected:    "my-service",
			description: "Underscores should become hyphens",
		},
		{
			name: "container_name override",
			service: icclient.ServiceConfig{
				Name:          "service1",
				ContainerName: "custom_name",
			},
			expected:    "custom-name",
			description: "ContainerName should override Name",
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			result := s.client.IncusServiceName(tt.service)
			s.Equal(tt.expected, result, tt.description)
		})
	}
}

// TestLogger tests logger access.
func (s *ClientTestSuite) TestLogger() {
	logger := s.client.Logger()
	s.Require().NotNil(logger)
}

// TestIsDebugIsTrace tests verbosity level checks.
func (s *ClientTestSuite) TestIsDebugIsTrace() {
	// Default verbosity is Info
	s.False(s.client.IsDebug())
	s.False(s.client.IsTrace())

	// Create client with debug verbosity
	client := icclient.New(s.ctx, s.logger, s.url)
	client.Config.Verbosity = icclient.VerbosityDebug
	s.True(client.IsDebug())
	s.False(client.IsTrace())

	// Create client with trace verbosity
	client.Config.Verbosity = icclient.VerbosityTrace
	s.True(client.IsDebug())
	s.True(client.IsTrace())
}

// TestUseProjectAndHasProject tests project switching.
func (s *ClientTestSuite) TestUseProjectAndHasProject() {
	projectName := "test-project-switch"

	// Create a project
	_, err := s.client.EnsureProject(projectName, icclient.EnsureProjectCreate())
	s.Require().NoError(err)

	// Switch to the project
	err = s.client.UseProject(projectName)
	s.Require().NoError(err)
	s.True(s.client.HasProject())

	// Cleanup
	err = s.client.GlobalIncus().DeleteProject(projectName)
	s.Require().NoError(err)
}

// TestConnectError tests disconnected client errors.
func (s *ClientTestSuite) TestConnectError() {
	// Create a client but don't connect
	client := icclient.New(s.ctx, s.logger, s.url)

	// Operations should fail with ErrDisconnected
	_, err := client.EnsureProject("test", icclient.EnsureProjectCreate())
	s.Require().Error(err)
	s.Equal(icclient.ErrDisconnected, err)
}

// TestEnsureProjectWithProfile tests project creation with profile configuration.
func (s *ClientTestSuite) TestEnsureProjectWithProfile() {
	projectName := "test-proj-profile"

	// Create project with profile options
	_, err := s.client.EnsureProject(
		projectName,
		icclient.EnsureProjectCreate(),
		icclient.EnsureProjectProfile("default"),
		icclient.EnsureProjecSourceProject("default"),
		icclient.EnsureProjectSourceProfile("default"),
	)
	s.Require().NoError(err)

	// Verify project exists
	projects, err := s.client.GlobalIncus().GetProjectNames()
	s.Require().NoError(err)
	s.Contains(projects, projectName)

	// Cleanup
	err = s.client.GlobalIncus().DeleteProject(projectName)
	s.Require().NoError(err)
}

// TestConfigOptions tests various client configuration options.
func (s *ClientTestSuite) TestConfigOptions() {
	// Test DefaultStoragePool option
	client := icclient.New(s.ctx, s.logger, s.url, icclient.DefaultStoragePool("custom-pool"))
	s.Equal("custom-pool", client.Config.DefaultStoragePool)

	// Test NetworkPrefix option
	client = icclient.New(s.ctx, s.logger, s.url, icclient.NetworkPrefix("net-"))
	s.Equal("net-", client.Config.NetworkPrefix)

	// Test DescriptionFormat option
	client = icclient.New(s.ctx, s.logger, s.url, icclient.DescriptionFormat("custom: %s"))
	s.Equal("custom: %s", client.Config.DescriptionFormat)
}

// TestConnectOptions tests connection with various TLS options.
func (s *ClientTestSuite) TestConnectOptions() {
	// Test that InsecureSkipVerify option works
	client := icclient.New(s.ctx, s.logger, s.url, icclient.InsecureSkipVerify())
	s.True(client.Config.InsecureSkipVerify)

	err := client.Connect()
	s.Require().NoError(err)
	s.True(client.IsRemote())
}

// TestEnsureProfile tests profile management with custom profiles.
func (s *ClientTestSuite) TestEnsureProfile() {
	projectName := "test-profile"

	// Create and switch to test project
	_, err := s.client.EnsureProject(projectName, icclient.EnsureProjectCreate())
	s.Require().NoError(err)
	err = s.client.UseProject(projectName)
	s.Require().NoError(err)

	// Test EnsureProfile with custom project
	err = s.client.EnsureProfile(projectName, "default", "default", "default", nil)
	s.Require().NoError(err)

	// Cleanup
	err = s.client.GlobalIncus().DeleteProject(projectName)
	s.Require().NoError(err)
}
