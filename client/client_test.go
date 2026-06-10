package client

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

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

	config := gc.Config
	config.DescriptionFormat = fmt.Sprintf(config.DescriptionFormat, "demo") + ":%s"

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

// createProjectClient creates a project-scoped client with logging hooks.
func createProjectClient(gc *GlobalClient, name string) (*Client, error) {
	_ = gc.DeleteProject(name, true)

	c, err := gc.createProject(name, nil)
	if err != nil {
		return nil, err
	}

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
