package client

import (
	"context"
	"testing"

	"github.com/stretchr/testify/suite"
)

// ProfileSuite tests Profile operations against a real Incus instance.
type ProfileSuite struct {
	suite.Suite
	ctx          context.Context
	globalClient *GlobalClient
	client       *Client
	projectName  string
}

// SetupSuite runs once before all tests.
func (s *ProfileSuite) SetupSuite() {
	s.ctx = context.Background()
	s.projectName = "profile-test"

	gc, err := NewTestClient(s.ctx)
	if err != nil {
		s.T().Skipf("Skipping tests: %v", err)
		return
	}
	s.globalClient = gc
}

// SetupTest runs before each test - creates fresh project.
func (s *ProfileSuite) SetupTest() {
	client, err := createProjectClient(s.globalClient, s.projectName)
	if err != nil {
		s.T().Fatalf("Failed to create test project: %v", err)
	}
	s.client = client
}

// TearDownTest runs after each test - cleans up project.
func (s *ProfileSuite) TearDownTest() {
	_ = s.globalClient.DeleteProject(s.projectName, true)
}

// ----------------------------------------------------------------------------
// Ensure Tests
// ----------------------------------------------------------------------------

func (s *ProfileSuite) TestEnsure_WithCreate() {
	r, err := s.client.Resource(KindProfile, "test-profile", &ProfileConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())
}

func (s *ProfileSuite) TestEnsure_WithoutCreate_Fails() {
	r, err := s.client.Resource(KindProfile, "non-existent", &ProfileConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure)
	s.Require().Error(err)
	s.False(r.IsEnsured())
	s.ErrorIs(err, ErrNotFound)
}

func (s *ProfileSuite) TestEnsure_Idempotent() {
	r, err := s.client.Resource(KindProfile, "test-idempotent", &ProfileConfig{})
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

func (s *ProfileSuite) TestEnsure_WithoutCreate_ThenWithCreate() {
	r, err := s.client.Resource(KindProfile, "test-retry", &ProfileConfig{})
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

func (s *ProfileSuite) TestEnsure_LongName() {
	longName := "this-is-a-very-long-profile-name-that-exceeds-normal-limits"
	r, err := s.client.Resource(KindProfile, longName, &ProfileConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())
}

func (s *ProfileSuite) TestEnsure_CopiesFromDefaultProfile() {
	r, err := s.client.Resource(KindProfile, "test-default-copy", &ProfileConfig{SourceProject: "default", SourceProfile: "default"})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	profile, ok := r.(*Profile)
	s.Require().True(ok)
	s.NotNil(profile.IncusProfile)
	// Default profile should have devices copied
	s.NotEmpty(profile.IncusProfile.Devices)
}

// ----------------------------------------------------------------------------
// ResourceStore Tests
// ----------------------------------------------------------------------------

func (s *ProfileSuite) TestResource_ReturnsSameInstance() {
	r1, err := s.client.Resource(KindProfile, "test-same", &ProfileConfig{})
	s.Require().NoError(err)

	r2, err := s.client.Resource(KindProfile, "test-same", &ProfileConfig{})
	s.Require().NoError(err)

	s.Same(r1, r2)
}

func (s *ProfileSuite) TestResource_DifferentNamesAreDifferent() {
	r1, err := s.client.Resource(KindProfile, "profile-a", &ProfileConfig{})
	s.Require().NoError(err)

	r2, err := s.client.Resource(KindProfile, "profile-b", &ProfileConfig{})
	s.Require().NoError(err)

	s.NotSame(r1, r2)
}

// ----------------------------------------------------------------------------
// Delete Tests
// ----------------------------------------------------------------------------

func (s *ProfileSuite) TestDelete_AfterEnsure() {
	r, err := s.client.Resource(KindProfile, "test-delete", &ProfileConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())

	err = RunAction(r, ActionDelete, OptionForce())
	s.Require().NoError(err)
	s.False(r.IsEnsured())
}

func (s *ProfileSuite) TestDelete_NotEnsured_NoError() {
	r, err := s.client.Resource(KindProfile, "never-created", &ProfileConfig{})
	s.Require().NoError(err)

	// Delete without ensure should not error
	err = RunAction(r, ActionDelete)
	s.Require().NoError(err)
}

// ----------------------------------------------------------------------------
// Hook Tests
// ----------------------------------------------------------------------------

func (s *ProfileSuite) TestHook_BeforeIsCalled() {
	called := false
	s.client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindProfile {
			called = true
		}
		return err
	})

	r, err := s.client.Resource(KindProfile, "test-before-hook", &ProfileConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(called, "before hook should have been called")
}

func (s *ProfileSuite) TestHook_AfterIsCalled() {
	called := false
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindProfile {
			called = true
		}
		return err
	})

	r, err := s.client.Resource(KindProfile, "test-after-hook", &ProfileConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(called, "after hook should have been called")
}

func (s *ProfileSuite) TestHook_AfterReceivesError() {
	var receivedErr error
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindProfile {
			receivedErr = err
		}
		return err
	})

	r, err := s.client.Resource(KindProfile, "non-existent", &ProfileConfig{})
	s.Require().NoError(err)

	_ = RunAction(r, ActionEnsure) // without create, will fail
	s.NotNil(receivedErr, "after hook should receive the error")
}

func (s *ProfileSuite) TestHook_BeforeCanAbort() {
	s.client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
		if r.Name() == "abort-me" {
			return ErrAborted
		}
		return err
	})

	r, err := s.client.Resource(KindProfile, "abort-me", &ProfileConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.ErrorIs(err, ErrAborted)
	s.False(r.IsEnsured())
}

func (s *ProfileSuite) TestHook_AfterCanModifyError() {
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if err != nil {
			return ErrAborted // replace error
		}
		return nil
	})

	r, err := s.client.Resource(KindProfile, "non-existent", &ProfileConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure) // will fail, hook replaces error
	s.ErrorIs(err, ErrAborted)
}

func (s *ProfileSuite) TestHook_BeforeFIFO() {
	var order []int
	s.client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
		order = append(order, 1)
		return err
	})
	s.client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
		order = append(order, 2)
		return err
	})
	s.client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
		order = append(order, 3)
		return err
	})

	r, err := s.client.Resource(KindProfile, "test-fifo", &ProfileConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.Equal([]int{1, 2, 3}, order, "before hooks should run FIFO")
}

func (s *ProfileSuite) TestHook_AfterLIFO() {
	var order []int
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		order = append(order, 1)
		return err
	})
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		order = append(order, 2)
		return err
	})
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		order = append(order, 3)
		return err
	})

	r, err := s.client.Resource(KindProfile, "test-lifo", &ProfileConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.Equal([]int{3, 2, 1}, order, "after hooks should run LIFO")
}

func (s *ProfileSuite) TestHook_DeleteAction() {
	var action Action
	s.client.AddHookBefore(func(a Action, r Resource, args Options, err error) error {
		action = a
		return err
	})

	r, err := s.client.Resource(KindProfile, "test-delete-hook", &ProfileConfig{})
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

func (s *ProfileSuite) TestEnsure_ExistsOnNewClient() {
	// Create profile
	r, err := s.client.Resource(KindProfile, "test-persist", &ProfileConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	// Get new client for same project
	newClient, err := s.globalClient.getProject(s.projectName)
	s.Require().NoError(err)

	// Ensure without create should find it
	r2, err := newClient.Resource(KindProfile, "test-persist", &ProfileConfig{})
	s.Require().NoError(err)

	err = RunAction(r2, ActionEnsure) // no create
	s.Require().NoError(err)
	s.True(r2.IsEnsured())
}

// ----------------------------------------------------------------------------
// IncusName Tests
// ----------------------------------------------------------------------------

func (s *ProfileSuite) TestIncusName_Sanitized() {
	r, err := s.client.Resource(KindProfile, "Test_Profile", &ProfileConfig{})
	s.Require().NoError(err)

	profile, ok := r.(*Profile)
	s.Require().True(ok)
	s.Equal("Test_Profile", profile.Name())
	s.Equal("test-profile", profile.IncusName())
}

// ----------------------------------------------------------------------------
// Run the suite
// ----------------------------------------------------------------------------

func TestProfileSuite(t *testing.T) {
	suite.Run(t, new(ProfileSuite))
}
