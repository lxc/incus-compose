package client

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

// ----------------------------------------------------------------------------
// Unit Tests for sanitizeNetworkName
// ----------------------------------------------------------------------------

func TestNetworkNameForProject(t *testing.T) {
	tests := []struct {
		name        string
		projectName string
		prefix      string
		networkName string
		description string
	}{
		{
			name:        "short name no project",
			projectName: "",
			prefix:      "",
			networkName: "web",
			description: "Short names should pass through",
		},
		{
			name:        "short name with project",
			projectName: "test",
			prefix:      "",
			networkName: "web",
			description: "test-web should fit in 13 chars",
		},
		{
			name:        "long name gets hashed",
			projectName: "verylongproject",
			prefix:      "ic-",
			networkName: "verylongnetwork",
			description: "Long names should get ic-{hash10} format",
		},
		{
			name:        "underscore replacement",
			projectName: "my_project",
			prefix:      "",
			networkName: "my_net",
			description: "Underscores should become hyphens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeNetworkName(tt.projectName, tt.prefix, tt.networkName)

			// All results should fit within maxInterfaceNameLen
			assert.LessOrEqual(t, len(result), maxInterfaceNameLen, "Network name too long")

			// Hash-based names should have the ic- prefix
			if len(tt.projectName+"-"+tt.networkName) > maxInterfaceNameLen && tt.prefix != "" {
				assert.True(t, strings.HasPrefix(result, tt.prefix))
				assert.Len(t, result, len(tt.prefix)+networkNameHashLen) // prefix + 10 char hash
			}

			// Should not contain underscores
			assert.NotContains(t, result, "_")

			// Determinism: same input should produce same output
			result2 := sanitizeNetworkName(tt.projectName, tt.prefix, tt.networkName)
			assert.Equal(t, result, result2, "Network name generation should be deterministic")
		})
	}

	p1n1 := sanitizeNetworkName("p1", "ic-", "n1")
	p2n1 := sanitizeNetworkName("p2", "ic-", "n1")

	assert.NotEqual(t, p1n1, p2n1, "Same name different projects should give different names")
}

// ----------------------------------------------------------------------------
// Integration Tests
// ----------------------------------------------------------------------------

// NetworkSuite tests Network operations against a real Incus instance.
type NetworkSuite struct {
	suite.Suite
	ctx          context.Context
	globalClient *GlobalClient
	client       *Client
	projectName  string
}

// SetupSuite runs once before all tests.
func (s *NetworkSuite) SetupSuite() {
	s.ctx = context.Background()
	s.projectName = "network-test"

	gc, err := NewTestClient(s.ctx)
	if err != nil {
		s.T().Skipf("Skipping tests: %v", err)
		return
	}
	s.globalClient = gc
}

// SetupTest runs before each test - creates fresh project.
func (s *NetworkSuite) SetupTest() {
	client, err := createProjectClient(s.globalClient, s.projectName)
	if err != nil {
		s.T().Fatalf("Failed to create test project: %v", err)
	}
	s.client = client
}

// TearDownTest runs after each test - cleans up project.
func (s *NetworkSuite) TearDownTest() {
	_ = s.globalClient.DeleteProject(s.projectName, true)
}

// ----------------------------------------------------------------------------
// Ensure Tests
// ----------------------------------------------------------------------------

func (s *NetworkSuite) TestEnsure_WithCreate() {
	r, err := s.client.Resource(KindNetwork, "test-net", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())
}

func (s *NetworkSuite) TestEnsure_WithoutCreate_Fails() {
	r, err := s.client.Resource(KindNetwork, "non-existent", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure)
	s.Require().Error(err)
	s.False(r.IsEnsured())
	s.ErrorIs(err, ErrNotFound)
}

func (s *NetworkSuite) TestEnsure_Idempotent() {
	r, err := s.client.Resource(KindNetwork, "test-idempotent", &NetworkConfig{})
	s.Require().NoError(err)

	// First ensure
	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())

	// Second ensure - should return immediately
	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())
}

func (s *NetworkSuite) TestEnsure_WithoutCreate_ThenWithCreate() {
	r, err := s.client.Resource(KindNetwork, "test-retry", &NetworkConfig{})
	s.Require().NoError(err)

	// First attempt without create - fails
	err = RunAction(r, ActionEnsure)
	s.Require().Error(err)
	s.False(r.IsEnsured())

	// Second attempt with create - succeeds
	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())
}

func (s *NetworkSuite) TestEnsure_DefaultType() {
	r, err := s.client.Resource(KindNetwork, "test-default-type", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	network, ok := r.(*Network)
	s.Require().True(ok)
	s.NotNil(network.IncusNetwork)
	s.Equal("bridge", network.IncusNetwork.Type)
}

func (s *NetworkSuite) TestEnsure_CustomType() {
	r, err := s.client.Resource(KindNetwork, "test-custom-type", &NetworkConfig{
		Type: "bridge",
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	network, ok := r.(*Network)
	s.Require().True(ok)
	s.Equal("bridge", network.IncusNetwork.Type)
}

// ----------------------------------------------------------------------------
// ResourceStore Tests
// ----------------------------------------------------------------------------

func (s *NetworkSuite) TestResource_ReturnsSameInstance() {
	r1, err := s.client.Resource(KindNetwork, "test-same", &NetworkConfig{})
	s.Require().NoError(err)

	r2, err := s.client.Resource(KindNetwork, "test-same", &NetworkConfig{})
	s.Require().NoError(err)

	s.Same(r1, r2)
}

func (s *NetworkSuite) TestResource_DifferentNamesAreDifferent() {
	r1, err := s.client.Resource(KindNetwork, "network-a", &NetworkConfig{})
	s.Require().NoError(err)

	r2, err := s.client.Resource(KindNetwork, "network-b", &NetworkConfig{})
	s.Require().NoError(err)

	s.NotSame(r1, r2)
}

// ----------------------------------------------------------------------------
// Delete Tests
// ----------------------------------------------------------------------------

func (s *NetworkSuite) TestDelete_AfterEnsure() {
	r, err := s.client.Resource(KindNetwork, "test-delete", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())

	err = RunAction(r, ActionDelete, OptionForce())
	s.Require().NoError(err)
	s.False(r.IsEnsured())
}

func (s *NetworkSuite) TestDelete_NotEnsured_NoError() {
	r, err := s.client.Resource(KindNetwork, "never-created", &NetworkConfig{})
	s.Require().NoError(err)

	// Delete without ensure should not error
	err = RunAction(r, ActionDelete)
	s.Require().NoError(err)
}

// ----------------------------------------------------------------------------
// Hook Tests
// ----------------------------------------------------------------------------

func (s *NetworkSuite) TestHook_BeforeIsCalled() {
	called := false
	s.client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindNetwork {
			called = true
		}
		return err
	})

	r, err := s.client.Resource(KindNetwork, "test-before-hook", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(called, "before hook should have been called")
}

func (s *NetworkSuite) TestHook_AfterIsCalled() {
	called := false
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindNetwork {
			called = true
		}
		return err
	})

	r, err := s.client.Resource(KindNetwork, "test-after-hook", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(called, "after hook should have been called")
}

func (s *NetworkSuite) TestHook_AfterReceivesError() {
	var receivedErr error
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindNetwork {
			receivedErr = err
		}
		return err
	})

	r, err := s.client.Resource(KindNetwork, "non-existent", &NetworkConfig{})
	s.Require().NoError(err)

	_ = RunAction(r, ActionEnsure) // without create, will fail
	s.NotNil(receivedErr, "after hook should receive the error")
}

func (s *NetworkSuite) TestHook_BeforeCanAbort() {
	s.client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
		if r.Name() == "abort-me" {
			return ErrAborted
		}
		return err
	})

	r, err := s.client.Resource(KindNetwork, "abort-me", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.ErrorIs(err, ErrAborted)
	s.False(r.IsEnsured())
}

func (s *NetworkSuite) TestHook_AfterCanModifyError() {
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if err != nil {
			return ErrAborted // replace error
		}
		return nil
	})

	r, err := s.client.Resource(KindNetwork, "non-existent", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure) // will fail, hook replaces error
	s.ErrorIs(err, ErrAborted)
}

func (s *NetworkSuite) TestHook_DeleteAction() {
	var action Action
	s.client.AddHookBefore(func(a Action, r Resource, args Options, err error) error {
		action = a
		return err
	})

	r, err := s.client.Resource(KindNetwork, "test-delete-hook", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.Equal(ActionEnsure, action)

	err = RunAction(r, ActionDelete)
	s.Require().NoError(err)
	s.Equal(ActionDelete, action)
}

// ----------------------------------------------------------------------------
// New Client Tests (persistence)
// ----------------------------------------------------------------------------

func (s *NetworkSuite) TestEnsure_ExistsOnNewClient() {
	// Create network
	r, err := s.client.Resource(KindNetwork, "test-persist", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	// Get new client for same project
	newClient, err := s.globalClient.getProject(s.projectName)
	s.Require().NoError(err)

	// Ensure without create should find it
	r2, err := newClient.Resource(KindNetwork, "test-persist", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r2, ActionEnsure) // no create
	s.Require().NoError(err)
	s.True(r2.IsEnsured())
}

// ----------------------------------------------------------------------------
// IncusName Tests
// ----------------------------------------------------------------------------

func (s *NetworkSuite) TestIncusName_ShortName() {
	r, err := s.client.Resource(KindNetwork, "web", &NetworkConfig{})
	s.Require().NoError(err)

	network, ok := r.(*Network)
	s.Require().True(ok)
	// Project is "network-test", so full name would be "network-test-web" (15 chars)
	// This exceeds 13 chars, so it should be hashed
	s.LessOrEqual(len(network.IncusName()), maxInterfaceNameLen)
}

func (s *NetworkSuite) TestIncusName_LongNameHashed() {
	r, err := s.client.Resource(KindNetwork, "very-long-network-name", &NetworkConfig{})
	s.Require().NoError(err)

	network, ok := r.(*Network)
	s.Require().True(ok)
	s.LessOrEqual(len(network.IncusName()), maxInterfaceNameLen)
	s.True(strings.HasPrefix(network.IncusName(), "ic-"))
}

func (s *NetworkSuite) TestIncusName_Deterministic() {
	r1, err := s.client.Resource(KindNetwork, "det-test", &NetworkConfig{})
	s.Require().NoError(err)

	r2, err := s.client.Resource(KindNetwork, "det-test", &NetworkConfig{})
	s.Require().NoError(err)

	network1, ok := r1.(*Network)
	s.Require().True(ok)
	network2, ok := r2.(*Network)
	s.Require().True(ok)
	s.Equal(network1.IncusName(), network2.IncusName())
}

// ----------------------------------------------------------------------------
// Run the suite
// ----------------------------------------------------------------------------

func TestNetworkSuite(t *testing.T) {
	suite.Run(t, new(NetworkSuite))
}
