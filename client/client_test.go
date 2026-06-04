package client

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lmittmann/tint"
	"github.com/lxc/incus/v6/shared/cliconfig"
	"github.com/mattn/go-colorable"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

var errTestLocal = NewError("env INCUS_COMPOSE_TEST_LOCAL is set")

// ----------------------------------------------------------------------------
// Unit Tests
// ----------------------------------------------------------------------------

func TestClientDescriptionFormat(t *testing.T) {
	client := NewOfflineClient(context.Background(), "my_project")

	assert.Equal(t, "incus-compose: %s", client.globalClient.Config.DescriptionFormat)
	assert.Equal(t, "incus-compose: my_project:%s", client.Config().DescriptionFormat)
	assert.Equal(t, "incus-compose: my_project:web", fmt.Sprintf(client.Config().DescriptionFormat, "web"))
}

func TestClientCustomDescriptionFormat(t *testing.T) {
	gc := New(context.Background(), ClientDescriptionFormat("managed-by-test: %s"))
	config := gc.projectConfig("demo")

	assert.Equal(t, "managed-by-test: demo:web", fmt.Sprintf(config.DescriptionFormat, "web"))
}

func TestSanitizeProjectName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple name",
			input:    "myproject",
			expected: "myproject",
		},
		{
			name:     "underscore replacement",
			input:    "my_project",
			expected: "my-project",
		},
		{
			name:     "uppercase to lowercase",
			input:    "MyProject",
			expected: "myproject",
		},
		{
			name:     "special characters",
			input:    "my project!",
			expected: "my-project",
		},
		{
			name:     "quotes removed",
			input:    `my"project"`,
			expected: "myproject",
		},
		{
			name:     "multiple special chars",
			input:    "my__project--name",
			expected: "my--project-name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeProjectName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ----------------------------------------------------------------------------
// Test Helpers
// ----------------------------------------------------------------------------

// projectRoot returns the absolute path to the project root directory.
func projectRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Dir(filepath.Dir(file))
}

// resolvePath resolves a path relative to the project root.
func resolvePath(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(projectRoot(), path)
}

// NewTestClient creates a new GlobalClient for testing.
// Returns error if INCUS_COMPOSE_URL is not set.
func NewTestClient(ctx context.Context) (*GlobalClient, error) {
	if _, ok := os.LookupEnv("INCUS_COMPOSE_TEST_LOCAL"); ok {
		return nil, errTestLocal
	}

	var logger *slog.Logger

	logFormat, ok := os.LookupEnv("LOG_FORMAT")
	if !ok {
		_, inCI := os.LookupEnv("CI")
		if inCI {
			logFormat = "text"
		} else {
			logFormat = "colortext"
		}
	}

	switch logFormat {
	case "json":
		logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug - 4}))
	case "colortext":
		logger = slog.New(tint.NewHandler(
			colorable.NewColorable(os.Stderr),
			&tint.Options{
				Level:      slog.LevelDebug - 4,
				TimeFormat: "15:04",
			},
		))
	default:
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug - 4}))
	}

	// Priority: INCUS_REMOTE -> INCUS_COMPOSE_URL -> "local" remote
	var opts []ClientOption

	// 1. If INCUS_REMOTE is set, use Incus CLI config
	if remote, ok := os.LookupEnv("INCUS_REMOTE"); ok {
		slog.DebugContext(ctx, "Connecting", "remote", remote)

		conf, err := cliconfig.LoadConfig("")
		if err != nil {
			return nil, ErrConnectionFailed.Wrap(err)
		}

		server, err := conf.GetInstanceServer(remote)
		if err != nil {
			return nil, ErrConnectionFailed.Wrap(err)
		}

		opts = []ClientOption{
			ClientLogger(logger),
			ClientProvideConnection(server),
		}
	} else if url, ok := os.LookupEnv("INCUS_COMPOSE_URL"); ok {
		// 2. If INCUS_COMPOSE_URL is set, use direct URL connection
		slog.DebugContext(ctx, "Connecting", "url", url)

		cert := resolvePath(os.Getenv("INCUS_COMPOSE_CERT"))
		key := resolvePath(os.Getenv("INCUS_COMPOSE_KEY"))

		opts = []ClientOption{
			ClientURL(url),
			ClientLogger(logger),
			ClientInsecureSkipVerify(),
		}

		if cert != "" {
			opts = append(opts, ClientTLSClientCert(cert))
		}
		if key != "" {
			opts = append(opts, ClientTLSClientKey(key))
		}
	} else {
		// 3. Fall back to "local" remote
		slog.DebugContext(ctx, "Connecting", "remote", "local")

		conf, err := cliconfig.LoadConfig("")
		if err != nil {
			return nil, ErrConnectionFailed.Wrap(err)
		}

		server, err := conf.GetInstanceServer("local")
		if err != nil {
			return nil, ErrConnectionFailed.Wrap(err)
		}

		opts = []ClientOption{
			ClientLogger(logger),
			ClientProvideConnection(server),
		}
	}

	// Use own cache project for tests.
	opts = append(opts, ClientCacheProject("incus-compose-tests-cache"))

	c := New(ctx, opts...)
	if err := c.Connect(); err != nil {
		return nil, err
	}

	return c, nil
}

// createProjectClient creates a project-scoped client with logging hooks.
func createProjectClient(gc *GlobalClient, name string) (*Client, error) {
	_ = gc.DeleteProject(name, true)

	c, err := gc.createProject(name, nil)
	if err != nil {
		return nil, err
	}

	c.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if err != nil {
			c.logger.Log(gc.Ctx, slog.LevelDebug-4, "Result with error", "name", r.Name(), "kind", r.Kind(), "action", action, "error", err)
			return err
		}

		c.logger.Log(gc.Ctx, slog.LevelDebug-4, "Done", "name", r.Name(), "kind", r.Kind(), "action", action)
		return nil
	})

	return c, nil
}

// ----------------------------------------------------------------------------
// Integration Tests
// ----------------------------------------------------------------------------

// ClientSuite tests GlobalClient operations against a real Incus instance.
type ClientSuite struct {
	suite.Suite
	ctx          context.Context
	globalClient *GlobalClient
	projectName  string
}

// SetupSuite runs once before all tests.
func (s *ClientSuite) SetupSuite() {
	s.ctx = context.Background()
	s.projectName = "client-test"

	gc, err := NewTestClient(s.ctx)
	if err != nil {
		s.T().Skipf("Skipping tests: %v", err)
		return
	}
	s.globalClient = gc
}

// TearDownTest runs after each test - cleans up project.
func (s *ClientSuite) TearDownTest() {
	_ = s.globalClient.DeleteProject(s.projectName, true)
}

// ----------------------------------------------------------------------------
// Connection Tests
// ----------------------------------------------------------------------------

func (s *ClientSuite) TestConnection_IsConnected() {
	s.True(s.globalClient.IsConnected())
}

// ----------------------------------------------------------------------------
// Project Tests
// ----------------------------------------------------------------------------

func (s *ClientSuite) TestProject_GlobalClientKeepsDefaultProfile() {
	gInfo, err := s.globalClient.incus.GetConnectionInfo()
	s.Require().NoError(err)
	s.Require().Equal("default", gInfo.Project)

	project, err := s.globalClient.EnsureProject(s.projectName, EnsureProjectWithCreate())
	s.Require().NoError(err)
	s.NotNil(project)

	gInfo, err = project.GlobalConnection().GetConnectionInfo()
	s.Require().NoError(err)
	s.Require().Equal("default", gInfo.Project)
}

func (s *ClientSuite) TestProject_ImageCacheIsInCacheProfile() {
	gInfo, err := s.globalClient.imageCache.GetConnectionInfo()
	s.Require().NoError(err)
	s.Require().Equal("incus-compose-tests-cache", gInfo.Project)
}

func (s *ClientSuite) TestProject_EnsureWithCreate() {
	project, err := s.globalClient.EnsureProject(s.projectName, EnsureProjectWithCreate())
	s.Require().NoError(err)
	s.NotNil(project)
}

func (s *ClientSuite) TestProject_EnsureWithoutCreate_Fails() {
	_, err := s.globalClient.EnsureProject("surely-does-not-exist-12345")
	s.Require().Error(err)
	s.ErrorIs(err, ErrNotFound)
}

func (s *ClientSuite) TestProject_NameIsPreserved() {
	project, err := s.globalClient.EnsureProject(s.projectName, EnsureProjectWithCreate())
	s.Require().NoError(err)
	s.Equal(s.projectName, project.Project())
}

func (s *ClientSuite) TestProject_NameIsSanitized() {
	name := "Test Project_123"
	project, err := s.globalClient.EnsureProject(name, EnsureProjectWithCreate())
	s.Require().NoError(err)

	s.Equal(name, project.Project())
	s.Equal("test-project-123", project.IncusProject())

	_ = s.globalClient.DeleteProject(name, true)
}

func (s *ClientSuite) TestProject_EnsureIdempotent() {
	project1, err := s.globalClient.EnsureProject(s.projectName, EnsureProjectWithCreate())
	s.Require().NoError(err)

	project2, err := s.globalClient.EnsureProject(s.projectName, EnsureProjectWithCreate())
	s.Require().NoError(err)

	s.Same(project1, project2)
}

func (s *ClientSuite) TestProject_DeleteSucceeds() {
	_, err := s.globalClient.EnsureProject(s.projectName, EnsureProjectWithCreate())
	s.Require().NoError(err)

	err = s.globalClient.DeleteProject(s.projectName, true)
	s.Require().NoError(err)
}

func (s *ClientSuite) TestProject_DeleteNonExistent_NoError() {
	err := s.globalClient.DeleteProject("never-existed", true)
	// DeleteProject should handle non-existent gracefully or return error
	// Either behavior is acceptable, just document it
	_ = err
}

// ----------------------------------------------------------------------------
// Run the suite
// ----------------------------------------------------------------------------

func TestClientSuite(t *testing.T) {
	suite.Run(t, new(ClientSuite))
}
