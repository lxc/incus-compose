package client

import (
	"context"
	"testing"

	"github.com/stretchr/testify/suite"
)

// StorageVolumeSuite tests StorageVolume operations against a real Incus instance.
type StorageVolumeSuite struct {
	suite.Suite
	ctx          context.Context
	globalClient *GlobalClient
	client       *Client
	projectName  string
}

// SetupSuite runs once before all tests.
func (s *StorageVolumeSuite) SetupSuite() {
	s.ctx = context.Background()
	s.projectName = "volume-test"

	gc, err := NewTestClient(s.ctx)
	if err != nil {
		s.T().Skipf("Skipping tests: %v", err)
		return
	}
	s.globalClient = gc
}

// SetupTest runs before each test - creates fresh project.
func (s *StorageVolumeSuite) SetupTest() {
	client, err := createProjectClient(s.globalClient, s.projectName)
	if err != nil {
		s.T().Fatalf("Failed to create test project: %v", err)
	}
	s.client = client
}

// TearDownTest runs after each test - cleans up project.
func (s *StorageVolumeSuite) TearDownTest() {
	_ = s.globalClient.DeleteProject(s.projectName, true)
}

// ----------------------------------------------------------------------------
// Ensure Tests
// ----------------------------------------------------------------------------

func (s *StorageVolumeSuite) TestEnsure_WithCreate() {
	r, err := s.client.Resource(KindStorageVolume, "test-vol", &StorageVolumeConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())
}

func (s *StorageVolumeSuite) TestEnsure_WithoutCreate_Fails() {
	r, err := s.client.Resource(KindStorageVolume, "non-existent", &StorageVolumeConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure)
	s.Require().Error(err)
	s.False(r.IsEnsured())
	s.ErrorIs(err, ErrNotFound)
}

func (s *StorageVolumeSuite) TestEnsure_Idempotent() {
	r, err := s.client.Resource(KindStorageVolume, "test-idempotent", &StorageVolumeConfig{})
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

func (s *StorageVolumeSuite) TestEnsure_WithoutCreate_ThenWithCreate() {
	r, err := s.client.Resource(KindStorageVolume, "test-retry", &StorageVolumeConfig{})
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

func (s *StorageVolumeSuite) TestEnsure_ShiftedVolume() {
	ir, err := s.client.Resource(KindImage, "docker.io/library/nginx:latest", &ImageConfig{})
	s.Require().NoError(err)

	// Create the image
	s.Require().NoError(RunAction(ir, ActionEnsure, OptionCreate()))

	r, err := s.client.Resource(KindStorageVolume, "test-shifted", &StorageVolumeConfig{
		Shifted:       true,
		ImageResource: ir,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	vol, ok := r.(*StorageVolume)
	s.Require().True(ok)
	s.NotNil(vol.IncusVolume)
	s.Equal("true", vol.IncusVolume.Config["security.shifted"])
	s.Equal("1000", vol.IncusVolume.Config["initial.uid"])
	s.Equal("1000", vol.IncusVolume.Config["initial.gid"])
}

func (s *StorageVolumeSuite) TestEnsure_ExtraConfig() {
	r, err := s.client.Resource(KindStorageVolume, "test-extra", &StorageVolumeConfig{
		ExtraConfig: map[string]string{
			"size": "5GiB",
		},
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	vol, ok := r.(*StorageVolume)
	s.Require().True(ok)
	s.Equal("5GiB", vol.IncusVolume.Config["size"])
}

// ----------------------------------------------------------------------------
// ResourceStore Tests
// ----------------------------------------------------------------------------

func (s *StorageVolumeSuite) TestResource_ReturnsSameInstance() {
	r1, err := s.client.Resource(KindStorageVolume, "test-same", &StorageVolumeConfig{})
	s.Require().NoError(err)

	r2, err := s.client.Resource(KindStorageVolume, "test-same", &StorageVolumeConfig{})
	s.Require().NoError(err)

	s.Same(r1, r2)
}

func (s *StorageVolumeSuite) TestResource_DifferentNamesAreDifferent() {
	r1, err := s.client.Resource(KindStorageVolume, "volume-a", &StorageVolumeConfig{})
	s.Require().NoError(err)

	r2, err := s.client.Resource(KindStorageVolume, "volume-b", &StorageVolumeConfig{})
	s.Require().NoError(err)

	s.NotSame(r1, r2)
}

// ----------------------------------------------------------------------------
// Delete Tests
// ----------------------------------------------------------------------------

func (s *StorageVolumeSuite) TestDelete_AfterEnsure() {
	r, err := s.client.Resource(KindStorageVolume, "test-delete", &StorageVolumeConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())

	err = RunAction(r, ActionDelete, OptionForce())
	s.Require().NoError(err)
	s.False(r.IsEnsured())
}

func (s *StorageVolumeSuite) TestDelete_NotEnsured_NoError() {
	r, err := s.client.Resource(KindStorageVolume, "never-created", &StorageVolumeConfig{})
	s.Require().NoError(err)

	// Delete without ensure should not error
	err = RunAction(r, ActionDelete)
	s.Require().NoError(err)
}

// ----------------------------------------------------------------------------
// Hook Tests
// ----------------------------------------------------------------------------

func (s *StorageVolumeSuite) TestHook_BeforeIsCalled() {
	called := false
	s.client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindStorageVolume {
			called = true
		}
		return err
	})

	r, err := s.client.Resource(KindStorageVolume, "test-before-hook", &StorageVolumeConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(called, "before hook should have been called")
}

func (s *StorageVolumeSuite) TestHook_AfterIsCalled() {
	called := false
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindStorageVolume {
			called = true
		}
		return err
	})

	r, err := s.client.Resource(KindStorageVolume, "test-after-hook", &StorageVolumeConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(called, "after hook should have been called")
}

func (s *StorageVolumeSuite) TestHook_AfterReceivesError() {
	var receivedErr error
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindStorageVolume {
			receivedErr = err
		}
		return err
	})

	r, err := s.client.Resource(KindStorageVolume, "non-existent", &StorageVolumeConfig{})
	s.Require().NoError(err)

	_ = RunAction(r, ActionEnsure) // without create, will fail
	s.NotNil(receivedErr, "after hook should receive the error")
}

func (s *StorageVolumeSuite) TestHook_BeforeCanAbort() {
	s.client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
		if r.Name() == "abort-me" {
			return ErrAborted
		}
		return err
	})

	r, err := s.client.Resource(KindStorageVolume, "abort-me", &StorageVolumeConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.ErrorIs(err, ErrAborted)
	s.False(r.IsEnsured())
}

func (s *StorageVolumeSuite) TestHook_AfterCanModifyError() {
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if err != nil {
			return ErrAborted // replace error
		}
		return nil
	})

	r, err := s.client.Resource(KindStorageVolume, "non-existent", &StorageVolumeConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure) // will fail, hook replaces error
	s.ErrorIs(err, ErrAborted)
}

func (s *StorageVolumeSuite) TestHook_DeleteAction() {
	var action Action
	s.client.AddHookBefore(func(a Action, r Resource, args Options, err error) error {
		action = a
		return err
	})

	r, err := s.client.Resource(KindStorageVolume, "test-delete-hook", &StorageVolumeConfig{})
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

func (s *StorageVolumeSuite) TestEnsure_ExistsOnNewClient() {
	// Create volume
	r, err := s.client.Resource(KindStorageVolume, "test-persist", &StorageVolumeConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	// Get new client for same project
	newClient, err := s.globalClient.getProject(s.projectName)
	s.Require().NoError(err)

	// Ensure without create should find it
	r2, err := newClient.Resource(KindStorageVolume, "test-persist", &StorageVolumeConfig{})
	s.Require().NoError(err)

	err = RunAction(r2, ActionEnsure) // no create
	s.Require().NoError(err)
	s.True(r2.IsEnsured())
}

// ----------------------------------------------------------------------------
// IncusName Tests
// ----------------------------------------------------------------------------

func (s *StorageVolumeSuite) TestIncusName_PrefixedWithProject() {
	r, err := s.client.Resource(KindStorageVolume, "mydata", &StorageVolumeConfig{})
	s.Require().NoError(err)

	vol, ok := r.(*StorageVolume)
	s.Require().True(ok)
	s.Equal("mydata", vol.Name())
	s.Equal("volume-test-mydata", vol.IncusName())
}

func (s *StorageVolumeSuite) TestConfig_DefaultPool() {
	r, err := s.client.Resource(KindStorageVolume, "default-pool", &StorageVolumeConfig{})
	s.Require().NoError(err)

	vol, ok := r.(*StorageVolume)
	s.Require().True(ok)
	s.Equal(s.client.Config().DefaultStoragePool, vol.Config.Pool)
}

func (s *StorageVolumeSuite) TestConfig_CustomPool() {
	r, err := s.client.Resource(KindStorageVolume, "custom-pool", &StorageVolumeConfig{
		Pool: "mypool",
	})
	s.Require().NoError(err)

	vol, ok := r.(*StorageVolume)
	s.Require().True(ok)
	s.Equal("mypool", vol.Config.Pool)
}

// ----------------------------------------------------------------------------
// Run the suite
// ----------------------------------------------------------------------------

func TestStorageVolumeSuite(t *testing.T) {
	suite.Run(t, new(StorageVolumeSuite))
}
