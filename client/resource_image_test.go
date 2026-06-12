package client

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// ----------------------------------------------------------------------------
// Local-only Tests (no Incus required)
// ----------------------------------------------------------------------------

func TestImageParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		imageName         string
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
			t.Parallel()
			c := NewOfflineClient(context.Background(), "test")
			img, err := newImage(c, tt.imageName, &ImageConfig{})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expectedRemote, img.Remote())
			require.Equal(t, tt.expectedImage, img.image)
			require.Equal(t, tt.expectedIncusName, img.IncusName())
		})
	}
}

func TestImageResource_SameIncusNameReturnsSameObject(t *testing.T) {
	t.Parallel()
	c := NewOfflineClient(context.Background(), "test")

	r1, err := c.Resource(KindImage, "docker.io/nginx:alpine", &ImageConfig{})
	require.NoError(t, err)

	r2, err := c.Resource(KindImage, "docker.io/nginx:alpine", &ImageConfig{})
	require.NoError(t, err)

	require.Same(t, r1, r2, "same image name must return the same object")
}

func TestImageResource_NormalizedFormReturnsSameObject(t *testing.T) {
	t.Parallel()
	c := NewOfflineClient(context.Background(), "test")

	r1, err := c.Resource(KindImage, "docker.io/nginx:alpine", &ImageConfig{})
	require.NoError(t, err)

	r2, err := c.Resource(KindImage, "docker.io/library/nginx:alpine", &ImageConfig{})
	require.NoError(t, err)

	require.Same(t, r1, r2, "short and canonical form must return the same object")
}

func TestImageResource_ReturnsSameInstance(t *testing.T) {
	t.Parallel()
	c := NewOfflineClient(context.Background(), "test")

	r1, err := c.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{})
	require.NoError(t, err)

	r2, err := c.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{})
	require.NoError(t, err)

	require.Same(t, r1, r2)
}

func TestImageResource_DifferentNamesAreDifferent(t *testing.T) {
	t.Parallel()
	c := NewOfflineClient(context.Background(), "test")

	r1, err := c.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{})
	require.NoError(t, err)

	r2, err := c.Resource(KindImage, "docker.io/library/busybox:1.37", &ImageConfig{})
	require.NoError(t, err)

	require.NotSame(t, r1, r2)
}

func TestImageIncusName_MatchesInput(t *testing.T) {
	t.Parallel()
	c := NewOfflineClient(context.Background(), "test")

	r, err := c.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{})
	require.NoError(t, err)

	image, ok := r.(*Image)
	require.True(t, ok)
	require.Equal(t, "docker.io/library/busybox:latest", image.Name())
	require.Equal(t, "docker.io/library/busybox:latest", image.IncusName())
}

func TestImageConfig_RemoteAndImageParsed(t *testing.T) {
	t.Parallel()
	c := NewOfflineClient(context.Background(), "test")

	r, err := c.Resource(KindImage, "docker.io/library/alpine:3.18", &ImageConfig{})
	require.NoError(t, err)

	image, ok := r.(*Image)
	require.True(t, ok)
	require.Equal(t, "docker.io", image.Remote())
	require.Equal(t, "library/alpine:3.18", image.image)
}

// ----------------------------------------------------------------------------
// Ensure Tests
// ----------------------------------------------------------------------------

func TestImageEnsure(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()

	tests := []struct {
		name    string
		image   string
		opts    []Option
		wantErr bool
	}{
		{
			name:  "with create busybox",
			image: "docker.io/library/busybox:latest",
			opts:  []Option{OptionCreate()},
		},
		{
			name:  "with create github",
			image: "ghcr.io/linuxcontainers/alpine:latest",
			opts:  []Option{OptionCreate()},
		},
		{
			name:    "without create fails",
			image:   "docker.io/library/nonexistent-image-xyz123:latest",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newRandomTestClient(t, ctx, "image-ensure-")

			r, err := c.Resource(KindImage, tt.image, &ImageConfig{})
			require.NoError(t, err)

			err = RunAction(ctx, r, ActionEnsure, tt.opts...)
			if tt.wantErr {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrNotFound)
				require.False(t, r.IsEnsured())
			} else {
				require.NoError(t, err)
				require.True(t, r.IsEnsured())

				image, ok := r.(*Image)
				require.True(t, ok)
				require.NotNil(t, image.IncusAlias)
			}
		})
	}
}

func TestImageEnsure_Idempotent(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()
	c := newRandomTestClient(t, ctx, "image-idempotent-")

	r, err := c.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{})
	require.NoError(t, err)

	require.NoError(t, RunAction(ctx, r, ActionEnsure, OptionCreate()))
	require.True(t, r.IsEnsured())

	require.NoError(t, RunAction(ctx, r, ActionEnsure, OptionCreate()))
	require.True(t, r.IsEnsured())
}

func TestImageEnsure_WithoutCreate_ThenWithCreate(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()
	c := newRandomTestClient(t, ctx, "image-retry-")

	r, err := c.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{})
	require.NoError(t, err)

	err = RunAction(ctx, r, ActionEnsure)
	require.Error(t, err)
	require.False(t, r.IsEnsured())

	err = RunAction(ctx, r, ActionEnsure, OptionCreate())
	require.NoError(t, err)
	require.True(t, r.IsEnsured())
}

func TestImageEnsure_ExistingImage_NewResource(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()
	c := newRandomTestClient(t, ctx, "image-existing-")

	r1, err := c.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{})
	require.NoError(t, err)
	require.NoError(t, RunAction(ctx, r1, ActionEnsure, OptionCreate()))
	require.True(t, r1.IsEnsured())

	newClient, err := c.globalClient.getProject(c.project)
	require.NoError(t, err)

	r2, err := newClient.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{})
	require.NoError(t, err)

	require.NoError(t, RunAction(ctx, r2, ActionEnsure))
	require.True(t, r2.IsEnsured())
	require.False(t, r2.Created(), "fetched resource should have Created() false")
}

func TestImageEnsure_ExistsOnNewClient(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()
	c := newRandomTestClient(t, ctx, "image-persist-")

	r, err := c.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{})
	require.NoError(t, err)
	require.NoError(t, RunAction(ctx, r, ActionEnsure, OptionCreate()))

	newClient, err := c.globalClient.getProject(c.project)
	require.NoError(t, err)

	r2, err := newClient.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{})
	require.NoError(t, err)

	require.NoError(t, RunAction(ctx, r2, ActionEnsure))
	require.True(t, r2.IsEnsured())
}

// ----------------------------------------------------------------------------
// Delete Tests
// ----------------------------------------------------------------------------

func TestImageDelete(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()

	tests := []struct {
		name   string
		ensure bool
	}{
		{
			name:   "image exists removed",
			ensure: true,
		},
		{
			name: "no image is noop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newRandomTestClient(t, ctx, "image-delete-")

			r, err := c.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{})
			require.NoError(t, err)

			if tt.ensure {
				require.NoError(t, RunAction(ctx, r, ActionEnsure, OptionCreate()))
				require.True(t, r.IsEnsured())
			}

			require.NoError(t, RunAction(ctx, r, ActionDelete, OptionForce()))

			if tt.ensure {
				alias, _, _ := c.incus.GetImageAlias(r.(*Image).IncusName())
				require.Nil(t, alias, "image should be gone after Delete")
			}
		})
	}
}

func TestImageDelete_NotEnsured_NoError(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()
	c := newRandomTestClient(t, ctx, "image-delne-")

	r, err := c.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{})
	require.NoError(t, err)

	require.NoError(t, RunAction(ctx, r, ActionDelete))
}

// ----------------------------------------------------------------------------
// Properties Test
// ----------------------------------------------------------------------------

func TestImageProperties(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()
	c := newRandomTestClient(t, ctx, "image-props-")

	r, err := c.Resource(KindImage, "registry.gitlab.com/r3j0/incus-compose/ic-healthd:latest", &ImageConfig{})
	require.NoError(t, err)

	require.NoError(t, RunAction(ctx, r, ActionEnsure, OptionCreate()))

	image, ok := r.(*Image)
	require.True(t, ok)
	require.Equal(t, "registry.gitlab.com/r3j0/incus-compose/ic-healthd:latest", image.Name())
	require.Equal(t, "/", image.Cwd)
	require.Equal(t, 65534, int(image.UID))
	require.Equal(t, 65534, int(image.GID))
}

// ----------------------------------------------------------------------------
// Hook Tests
// ----------------------------------------------------------------------------

func TestImageHooks(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()

	tests := []struct {
		name string
		run  func(*testing.T, *Client)
	}{
		{
			name: "before is called",
			run: func(t *testing.T, c *Client) {
				t.Helper()
				called := false
				c.AddHookBefore(func(_ context.Context, action Action, r Resource, _ Options, err error) error {
					if action == ActionEnsure && r.Kind() == KindImage {
						called = true
					}
					return err
				})
				r, err := c.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{})
				require.NoError(t, err)
				require.NoError(t, RunAction(ctx, r, ActionEnsure, OptionCreate()))
				require.True(t, called, "before hook should have been called")
			},
		},
		{
			name: "after is called",
			run: func(t *testing.T, c *Client) {
				t.Helper()
				called := false
				c.AddHookAfter(func(_ context.Context, action Action, r Resource, _ Options, err error) error {
					if action == ActionEnsure && r.Kind() == KindImage {
						called = true
					}
					return err
				})
				r, err := c.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{})
				require.NoError(t, err)
				require.NoError(t, RunAction(ctx, r, ActionEnsure, OptionCreate()))
				require.True(t, called, "after hook should have been called")
			},
		},
		{
			name: "after receives error",
			run: func(t *testing.T, c *Client) {
				t.Helper()
				var receivedErr error
				c.AddHookAfter(func(_ context.Context, action Action, r Resource, _ Options, err error) error {
					if action == ActionEnsure && r.Kind() == KindImage {
						receivedErr = err
					}
					return err
				})
				r, err := c.Resource(KindImage, "docker.io/library/nonexistent:latest", &ImageConfig{})
				require.NoError(t, err)
				_ = RunAction(ctx, r, ActionEnsure)
				require.NotNil(t, receivedErr, "after hook should receive the error")
			},
		},
		{
			name: "before can abort",
			run: func(t *testing.T, c *Client) {
				t.Helper()
				c.AddHookBefore(func(_ context.Context, _ Action, r Resource, _ Options, err error) error {
					if r.Name() == "docker.io/library/abort-me:latest" {
						return ErrAborted
					}
					return err
				})
				r, err := c.Resource(KindImage, "docker.io/library/abort-me:latest", &ImageConfig{})
				require.NoError(t, err)
				err = RunAction(ctx, r, ActionEnsure, OptionCreate())
				require.ErrorIs(t, err, ErrAborted)
				require.False(t, r.IsEnsured())
			},
		},
		{
			name: "after can modify error",
			run: func(t *testing.T, c *Client) {
				t.Helper()
				c.AddHookAfter(func(_ context.Context, _ Action, _ Resource, _ Options, err error) error {
					if err != nil {
						return ErrAborted
					}
					return nil
				})
				r, err := c.Resource(KindImage, "docker.io/library/nonexistent:latest", &ImageConfig{})
				require.NoError(t, err)
				err = RunAction(ctx, r, ActionEnsure)
				require.ErrorIs(t, err, ErrAborted)
			},
		},
		{
			name: "delete action",
			run: func(t *testing.T, c *Client) {
				t.Helper()
				var lastAction Action
				c.AddHookBefore(func(_ context.Context, a Action, _ Resource, _ Options, err error) error {
					lastAction = a
					return err
				})
				r, err := c.Resource(KindImage, "docker.io/library/busybox:latest", &ImageConfig{})
				require.NoError(t, err)
				require.NoError(t, RunAction(ctx, r, ActionEnsure, OptionCreate()))
				require.Equal(t, ActionEnsure, lastAction)
				require.NoError(t, RunAction(ctx, r, ActionDelete))
				require.Equal(t, ActionDelete, lastAction)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newRandomTestClient(t, ctx, "image-hook-")
			tt.run(t, c)
		})
	}
}
