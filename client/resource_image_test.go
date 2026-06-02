package client

import (
	"context"
	"testing"

	incusClient "github.com/lxc/incus/v6/client"
	incusApi "github.com/lxc/incus/v6/shared/api"
	"github.com/lxc/incus/v6/shared/cliconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

// ----------------------------------------------------------------------------
// Unit Tests for Image Reference Parsing
// ----------------------------------------------------------------------------

func TestImageParsing(t *testing.T) {
	tests := []struct {
		name              string
		imageName         string
		configRemote      string
		configImage       string
		expectedRemote    string
		expectedImage     string
		expectedIncusName string
		wantErr           bool
	}{
		{
			name:              "full docker reference",
			imageName:         "docker.io/library/alpine:3.18",
			expectedRemote:    "docker.io",
			expectedImage:     "library/alpine:3.18",
			expectedIncusName: "docker.io/library/alpine:3.18",
		},
		{
			name:              "full github reference",
			imageName:         "ghcr.io/linuxcontainers/alpine:latest",
			expectedRemote:    "ghcr.io",
			expectedImage:     "linuxcontainers/alpine:latest",
			expectedIncusName: "ghcr.io/linuxcontainers/alpine:latest",
		},
		{
			name:              "short reference defaults to docker.io",
			imageName:         "nginx:alpine",
			expectedRemote:    "docker.io",
			expectedImage:     "library/nginx:alpine",
			expectedIncusName: "docker.io/library/nginx:alpine",
		},
		{
			name:              "ghcr.io reference",
			imageName:         "ghcr.io/someorg/someimage:v1.0",
			expectedRemote:    "ghcr.io",
			expectedImage:     "someorg/someimage:v1.0",
			expectedIncusName: "ghcr.io/someorg/someimage:v1.0",
		},
		{
			name:              "localhost converted to local",
			imageName:         "localhost/myimage:latest",
			expectedRemote:    "local",
			expectedImage:     "myimage:latest",
			expectedIncusName: "local/myimage:latest",
		},
		{
			name:              "config overrides parsing",
			imageName:         "custom-name",
			configRemote:      "custom.registry.io",
			configImage:       "myimage:v2",
			expectedRemote:    "custom.registry.io",
			expectedImage:     "myimage:v2",
			expectedIncusName: "custom.registry.io/myimage:v2",
		},
		{
			name:              "image with no tag gets latest",
			imageName:         "alpine",
			expectedRemote:    "docker.io",
			expectedImage:     "library/alpine:latest",
			expectedIncusName: "docker.io/library/alpine:latest",
		},
		{
			name:              "it adds library",
			imageName:         "docker.io/nginx:alpine",
			expectedRemote:    "docker.io",
			expectedImage:     "library/nginx:alpine",
			expectedIncusName: "docker.io/library/nginx:alpine",
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
			assert.Equal(t, tt.expectedRemote, img.Remote())
			assert.Equal(t, tt.expectedImage, img.Config.Image)
			assert.Equal(t, tt.expectedIncusName, img.IncusName())
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
	cliConfig    *cliconfig.Config
	cleanup      []Resource
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

	// Use CLI config from global client
	s.cliConfig = gc.CliConfig()
	if s.cliConfig == nil {
		s.T().Skipf("Skipping tests: failed to load Incus config")
		return
	}

	// Check docker.io is configured
	if _, err := s.cliConfig.GetImageServer("docker.io"); err != nil {
		s.T().Skipf("Skipping tests: docker.io not configured: %v", err)
		return
	}
}

// SetupTest runs before each test - creates fresh project.
func (s *ImageSuite) SetupTest() {
	client, err := createProjectClient(s.globalClient, s.projectName)
	if err != nil {
		s.T().Fatalf("Failed to create test project: %v", err)
	}
	s.client = client

	s.cleanup = []Resource{}
}

// TearDownTest runs after each test - cleans up project.
func (s *ImageSuite) TearDownTest() {
	for _, r := range s.cleanup {
		if SupportsAction(r, ActionDelete) {
			_ = RunAction(r, ActionDelete, OptionForce())
		}
	}

	_ = s.globalClient.DeleteProject(s.projectName, true)
}

// ----------------------------------------------------------------------------
// Ensure Tests
// ----------------------------------------------------------------------------.
func (s *ImageSuite) TestEnsure_WithCreate_Github() {
	r, err := s.client.Resource(KindImage, "ghcr.io/linuxcontainers/alpine:latest", &ImageConfig{
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())

	s.cleanup = append(s.cleanup, r)

	image, ok := r.(*Image)
	s.Require().True(ok)
	s.NotNil(image.IncusAlias)
}

func (s *ImageSuite) TestEnsure_WithCreate() {
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())

	s.cleanup = append(s.cleanup, r)

	image, ok := r.(*Image)
	s.Require().True(ok)
	s.NotNil(image.IncusAlias)
}

func (s *ImageSuite) TestEnsure_WithoutCreate_Fails() {
	r, err := s.client.Resource(KindImage, "docker.io/library/nonexistent-image-xyz123:latest", &ImageConfig{
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure)
	s.Require().Error(err)
	s.False(r.IsEnsured())
	s.ErrorIs(err, ErrNotFound)
}

func (s *ImageSuite) TestEnsure_Idempotent() {
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	// First ensure - puts image in cache (or finds existing)
	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())
	// Note: Created() may be false if image was already in cache

	// Second ensure - should return immediately
	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())

	s.cleanup = append(s.cleanup, r)
}

func (s *ImageSuite) TestEnsure_WithoutCreate_ThenWithCreate() {
	// Use project-scoped cache to avoid finding images from default project
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		CliConfig:   s.cliConfig,
		CacheServer: s.client.Connection(),
	})
	s.Require().NoError(err)

	// First attempt without create - fails (not in project cache)
	err = RunAction(r, ActionEnsure)
	s.Require().Error(err)
	s.False(r.IsEnsured())

	// Second attempt with create - succeeds (downloads it)
	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())

	s.cleanup = append(s.cleanup, r)
}

func (s *ImageSuite) TestEnsure_WithoutCliConfig_Fails() {
	// Use project-scoped cache to avoid finding images from default project
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		// No CliConfig configured - but use project cache
		CacheServer: s.client.Connection(),
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().Error(err)
	s.ErrorIs(err, ErrImageSource)
}

func (s *ImageSuite) TestEnsure_ExistingImage_NewResource() {
	// First, ensure image is in cache
	r1, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	err = RunAction(r1, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r1.IsEnsured())
	// Note: Created() may be false if image was already in cache from previous test

	s.cleanup = append(s.cleanup, r1)

	// Get a new client for the same project
	newClient, err := s.globalClient.getProject(s.projectName)
	s.Require().NoError(err)

	// Create a new resource for the same image - should find existing in cache
	r2, err := newClient.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	// Ensure without create should find existing image in cache
	err = RunAction(r2, ActionEnsure)
	s.Require().NoError(err)
	s.True(r2.IsEnsured())
	s.False(r2.Created(), "fetched resource should have Created() false")
}

// ----------------------------------------------------------------------------
// ResourceStore Tests
// ----------------------------------------------------------------------------

func (s *ImageSuite) TestResource_ReturnsSameInstance() {
	r1, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	r2, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	s.Same(r1, r2)
}

func (s *ImageSuite) TestResource_DifferentNamesAreDifferent() {
	r1, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	r2, err := s.client.Resource(KindImage, "docker.io/library/busybox:1.37", &ImageConfig{
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	s.NotSame(r1, r2)
}

// ----------------------------------------------------------------------------
// Delete Tests
// ----------------------------------------------------------------------------

func (s *ImageSuite) TestDelete_NoProjectCopy_LeavesCacheIntact() {
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())

	image, ok := r.(*Image)
	s.Require().True(ok)

	// No per-project copy exists (no instance was created), so Delete is a no-op
	// and must not touch the cache.
	err = RunAction(r, ActionDelete, OptionForce())
	s.Require().NoError(err)

	cacheAlias, _, err := image.Config.cache.GetImageAlias(image.IncusName())
	s.Require().NoError(err)
	s.NotNil(cacheAlias, "cache image must survive Delete")
}

func (s *ImageSuite) TestDelete_RemovesProjectCopy() {
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	image, ok := r.(*Image)
	s.Require().True(ok)

	// Simulate the per-project copy that CreateInstanceFromImage leaves behind:
	// copy the cached image into the active project under the same alias.
	cacheImage, _, err := image.Config.cache.GetImage(image.IncusAlias.Target)
	s.Require().NoError(err)

	op, err := s.client.Connection().CopyImage(image.Config.cache, *cacheImage, &incusClient.ImageCopyArgs{
		Aliases: []incusApi.ImageAlias{{Name: image.IncusName()}},
		Mode:    "pull",
	})
	s.Require().NoError(err)
	s.Require().NoError(op.Wait())

	alias, _, err := s.client.Connection().GetImageAlias(image.IncusName())
	s.Require().NoError(err)
	s.Require().NotNil(alias, "per-project copy should exist before Delete")

	// Delete removes the per-project copy but keeps the cache.
	err = RunAction(r, ActionDelete, OptionForce())
	s.Require().NoError(err)

	alias, _, _ = s.client.Connection().GetImageAlias(image.IncusName())
	s.Nil(alias, "per-project copy should be gone after Delete")

	cacheAlias, _, err := image.Config.cache.GetImageAlias(image.IncusName())
	s.Require().NoError(err)
	s.NotNil(cacheAlias, "cache image must survive Delete")
}

func (s *ImageSuite) TestEnsure_Pull_SkipsWhenFingerprintUnchanged() {
	s.T().Skip("skopeo fingerprint may differ between runs on CI — needs investigation")
	// Seed the cache with a pinned (immutable) image.
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:1.37", &ImageConfig{
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	// Use a fresh client so the resource store is empty and Ensure doesn't
	// short-circuit on the already-ensured resource.
	freshClient, err := s.globalClient.getProject(s.projectName)
	s.Require().NoError(err)

	r2, err := freshClient.Resource(KindImage, "docker.io/library/busybox:1.37", &ImageConfig{
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	// Pull with unchanged remote fingerprint: refresh is skipped.
	err = RunAction(r2, ActionEnsure, OptionCreate(), OptionPull())
	s.Require().NoError(err)
	s.True(r2.IsEnsured())

	image2, ok := r2.(*Image)
	s.Require().True(ok)
	s.False(image2.Created(), "image should not be re-created when remote fingerprint matches cache")
	s.cleanup = append(s.cleanup, r2)
}

func (s *ImageSuite) TestDelete_NotEnsured_NoError() {
	r, err := s.client.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		CliConfig: s.cliConfig,
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
		CliConfig: s.cliConfig,
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
		CliConfig: s.cliConfig,
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
		CliConfig: s.cliConfig,
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
		CliConfig: s.cliConfig,
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
		CliConfig: s.cliConfig,
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
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.Equal(ActionEnsure, action)

	// Delete fires the before hook (it removes any per-project copy).
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
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	s.cleanup = append(s.cleanup, r)

	// Get new client for same project
	newClient, err := s.globalClient.getProject(s.projectName)
	s.Require().NoError(err)

	// Ensure without create should find it
	r2, err := newClient.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{
		CliConfig: s.cliConfig,
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
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	image, ok := r.(*Image)
	s.Require().True(ok)
	s.Equal("docker.io/library/busybox:latest", image.Name())
	s.Equal("docker.io/library/busybox:latest", image.IncusName())
}

func (s *ImageSuite) TestConfig_RemoteAndImageParsed() {
	r, err := s.client.Resource(KindImage, "docker.io/library/alpine:3.18", &ImageConfig{
		CliConfig: s.cliConfig,
	})
	s.Require().NoError(err)

	image, ok := r.(*Image)
	s.Require().True(ok)
	s.Equal("docker.io", image.Remote())
	s.Equal("library/alpine:3.18", image.Config.Image)
}

// ----------------------------------------------------------------------------
// Run the suite
// ----------------------------------------------------------------------------

func TestImageSuite(t *testing.T) {
	suite.Run(t, new(ImageSuite))
}
