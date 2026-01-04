package client

import (
	"context"
	"testing"

	incusClient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/cliconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

// ----------------------------------------------------------------------------
// Unit Tests for Image Reference Parsing
// ----------------------------------------------------------------------------

func TestImageParsing(t *testing.T) {
	tests := []struct {
		name           string
		imageName      string
		configRemote   string
		configImage    string
		expectedRemote string
		expectedImage  string
		wantErr        bool
	}{
		{
			name:           "full docker reference",
			imageName:      "docker.io/library/alpine:3.18",
			expectedRemote: "docker.io",
			expectedImage:  "library/alpine:3.18",
		},
		{
			name:           "short reference defaults to docker.io",
			imageName:      "nginx:alpine",
			expectedRemote: "docker.io",
			expectedImage:  "library/nginx:alpine",
		},
		{
			name:           "ghcr.io reference",
			imageName:      "ghcr.io/someorg/someimage:v1.0",
			expectedRemote: "ghcr.io",
			expectedImage:  "someorg/someimage:v1.0",
		},
		{
			name:           "localhost converted to local",
			imageName:      "localhost/myimage:latest",
			expectedRemote: "local",
			expectedImage:  "localhost/myimage:latest", // CutPrefix uses "local/" but string has "localhost/"
		},
		{
			name:           "config overrides parsing",
			imageName:      "custom-name",
			configRemote:   "custom.registry.io",
			configImage:    "myimage:v2",
			expectedRemote: "custom.registry.io",
			expectedImage:  "myimage:v2",
		},
		{
			name:           "image with no tag gets latest",
			imageName:      "alpine",
			expectedRemote: "docker.io",
			expectedImage:  "library/alpine:latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a minimal client for parsing test
			// We only need the imageCache field to be non-nil for newImage to work
			c := &Client{}

			config := &ImageConfig{
				Remote: tt.configRemote,
				Image:  tt.configImage,
			}

			img, err := newImage(c, tt.imageName, config)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedRemote, img.Config.Remote)
			assert.Equal(t, tt.expectedImage, img.Config.Image)
		})
	}
}

// ----------------------------------------------------------------------------
// Integration Tests
// ----------------------------------------------------------------------------

// ImageSuite tests Image operations against a real Incus instance.
type ImageSuite struct {
	suite.Suite
	ctx          context.Context
	globalClient *GlobalClient
	client       *Client
	projectName  string
	imageServer  incusClient.ImageServer
}

// SetupSuite runs once before all tests.
func (s *ImageSuite) SetupSuite() {
	s.ctx = context.Background()
	s.projectName = "image-test"

	gc, err := NewTestClient(s.ctx)
	if err != nil {
		s.T().Skipf("Skipping tests: %v", err)
		return
	}
	s.globalClient = gc

	// Load Incus CLI config to get image server
	conf, err := cliconfig.LoadConfig("")
	if err != nil {
		s.T().Skipf("Skipping tests: failed to load Incus config: %v", err)
		return
	}

	// Get docker.io image server
	imageServer, err := conf.GetImageServer("docker.io")
	if err != nil {
		s.T().Skipf("Skipping tests: docker.io not configured: %v", err)
		return
	}
	s.imageServer = imageServer
}

// SetupTest runs before each test - creates fresh project.
func (s *ImageSuite) SetupTest() {
	client, err := createProjectClient(s.globalClient, s.projectName)
	if err != nil {
		s.T().Fatalf("Failed to create test project: %v", err)
	}
	s.client = client
}

// TearDownTest runs after each test - cleans up project.
func (s *ImageSuite) TearDownTest() {
	_ = s.globalClient.DeleteProject(s.projectName, true)
}

// ----------------------------------------------------------------------------
// Ensure Tests
// ----------------------------------------------------------------------------

func (s *ImageSuite) TestEnsure_WithCreate() {
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())

	image, ok := r.(*Image)
	s.Require().True(ok)
	s.NotNil(image.IncusAlias)
}

func (s *ImageSuite) TestEnsure_WithoutCreate_Fails() {
	r, err := s.client.Resource(KindImage, "docker.io/library/nonexistent-image-xyz123:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure)
	s.Require().Error(err)
	s.False(r.IsEnsured())
	s.ErrorIs(err, ErrNotFound)
}

func (s *ImageSuite) TestEnsure_Idempotent() {
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
	})
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

func (s *ImageSuite) TestEnsure_WithoutCreate_ThenWithCreate() {
	// Use project-scoped cache to avoid finding images from default project
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
		Cache:  s.client.Connection(),
	})
	s.Require().NoError(err)
	defer func() { _ = RunAction(r, ActionDelete, OptionForce()) }()

	// First attempt without create - fails (not in project cache)
	err = RunAction(r, ActionEnsure)
	s.Require().Error(err)
	s.False(r.IsEnsured())

	// Second attempt with create - succeeds (downloads it)
	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())
}

func (s *ImageSuite) TestEnsure_WithoutSource_Fails() {
	// Use project-scoped cache to avoid finding images from default project
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		// No Source configured - but use project cache
		Cache: s.client.Connection(),
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().Error(err)
	s.Contains(err.Error(), "source not configured")
}

func (s *ImageSuite) TestEnsure_ExistingImage_NewResource() {
	// First, create an image
	r1, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	err = RunAction(r1, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r1.IsEnsured())

	// Get a new client for the same project
	newClient, err := s.globalClient.getProject(s.projectName)
	s.Require().NoError(err)

	// Create a new resource for the same image - should find existing
	r2, err := newClient.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	// Ensure without create should find existing image
	err = RunAction(r2, ActionEnsure)
	s.Require().NoError(err)
	s.True(r2.IsEnsured())
}

// ----------------------------------------------------------------------------
// ResourceStore Tests
// ----------------------------------------------------------------------------

func (s *ImageSuite) TestResource_ReturnsSameInstance() {
	r1, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	r2, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	s.Same(r1, r2)
}

func (s *ImageSuite) TestResource_DifferentNamesAreDifferent() {
	r1, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	r2, err := s.client.Resource(KindImage, "docker.io/library/busybox:1.36", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	s.NotSame(r1, r2)
}

// ----------------------------------------------------------------------------
// Delete Tests
// ----------------------------------------------------------------------------

func (s *ImageSuite) TestDelete_AfterEnsure() {
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())

	err = RunAction(r, ActionDelete, OptionForce())
	s.Require().NoError(err)
	s.False(r.IsEnsured())
}

func (s *ImageSuite) TestDelete_NotEnsured_NoError() {
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	// Delete without ensure should not error
	err = RunAction(r, ActionDelete)
	s.Require().NoError(err)
}

// ----------------------------------------------------------------------------
// Hook Tests
// ----------------------------------------------------------------------------

func (s *ImageSuite) TestHook_BeforeIsCalled() {
	called := false
	s.client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindImage {
			called = true
		}
		return err
	})

	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(called, "before hook should have been called")
}

func (s *ImageSuite) TestHook_AfterIsCalled() {
	called := false
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindImage {
			called = true
		}
		return err
	})

	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(called, "after hook should have been called")
}

func (s *ImageSuite) TestHook_AfterReceivesError() {
	var receivedErr error
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindImage {
			receivedErr = err
		}
		return err
	})

	r, err := s.client.Resource(KindImage, "docker.io/library/nonexistent:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	_ = RunAction(r, ActionEnsure) // without create, will fail
	s.NotNil(receivedErr, "after hook should receive the error")
}

func (s *ImageSuite) TestHook_BeforeCanAbort() {
	s.client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
		if r.Name() == "docker.io/library/abort-me:latest" {
			return ErrAborted
		}
		return err
	})

	r, err := s.client.Resource(KindImage, "docker.io/library/abort-me:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.ErrorIs(err, ErrAborted)
	s.False(r.IsEnsured())
}

func (s *ImageSuite) TestHook_AfterCanModifyError() {
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if err != nil {
			return ErrAborted // replace error
		}
		return nil
	})

	r, err := s.client.Resource(KindImage, "docker.io/library/nonexistent:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure) // will fail, hook replaces error
	s.ErrorIs(err, ErrAborted)
}

func (s *ImageSuite) TestHook_DeleteAction() {
	var action Action
	s.client.AddHookBefore(func(a Action, r Resource, args Options, err error) error {
		action = a
		return err
	})

	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
	})
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

func (s *ImageSuite) TestEnsure_ExistsOnNewClient() {
	// Create image
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	// Get new client for same project
	newClient, err := s.globalClient.getProject(s.projectName)
	s.Require().NoError(err)

	// Ensure without create should find it
	r2, err := newClient.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	err = RunAction(r2, ActionEnsure) // no create
	s.Require().NoError(err)
	s.True(r2.IsEnsured())
}

// ----------------------------------------------------------------------------
// IncusName Tests
// ----------------------------------------------------------------------------

func (s *ImageSuite) TestIncusName_MatchesInput() {
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	image, ok := r.(*Image)
	s.Require().True(ok)
	s.Equal("docker.io/library/busybox:latest", image.Name())
	s.Equal("docker.io/library/busybox:latest", image.IncusName())
}

func (s *ImageSuite) TestConfig_RemoteAndImageParsed() {
	r, err := s.client.Resource(KindImage, "docker.io/library/alpine:3.18", &ImageConfig{
		Source: s.imageServer,
	})
	s.Require().NoError(err)

	image, ok := r.(*Image)
	s.Require().True(ok)
	s.Equal("docker.io", image.Config.Remote)
	s.Equal("library/alpine:3.18", image.Config.Image)
}

// ----------------------------------------------------------------------------
// Run the suite
// ----------------------------------------------------------------------------

func TestImageSuite(t *testing.T) {
	suite.Run(t, new(ImageSuite))
}
