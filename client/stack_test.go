package client

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// ----------------------------------------------------------------------------
// Unit Tests (no Incus required)
// ----------------------------------------------------------------------------

// TestGroupByKind tests the batch grouping logic without Incus.
func TestGroupByKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		tasks       []Resource
		wantBatches int
		wantSizes   []int
	}{
		{
			name:        "empty tasks",
			tasks:       []Resource{},
			wantBatches: 0,
			wantSizes:   nil,
		},
		{
			name: "single task",
			tasks: []Resource{
				newMockResource("a", "", 0, false),
			},
			wantBatches: 1,
			wantSizes:   []int{1},
		},
		{
			name: "same kind groups together",
			tasks: []Resource{
				newMockResource("a", "", 0, false),
				newMockResource("b", "", 0, false),
				newMockResource("c", "", 0, false),
			},
			wantBatches: 1,
			wantSizes:   []int{3},
		},
		{
			name: "different kinds create separate batches",
			tasks: []Resource{
				newMockResource("profile", KindProfile, 0, false),
				newMockResource("volume", KindStorageVolume, 0, false),
				newMockResource("instance", KindInstance, 0, false),
			},
			wantBatches: 3,
			wantSizes:   []int{1, 1, 1},
		},
		{
			name: "mixed kinds with multiple per batch",
			tasks: []Resource{
				newMockResource("profile", KindProfile, 0, false),
				newMockResource("image", KindImage, 0, false),
				newMockResource("image2", KindImage, 0, false),
				newMockResource("volume", KindStorageVolume, 0, false),
				newMockResource("volume2", KindStorageVolume, 0, false),
				newMockResource("instance", KindInstance, 0, false),
			},
			wantBatches: 4,
			wantSizes:   []int{1, 2, 2, 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stack := NewStack(nil)
			stack.Add(tc.tasks...)

			batches := stack.groupByKind()

			require.Len(t, batches, tc.wantBatches)

			if tc.wantSizes != nil {
				for i, size := range tc.wantSizes {
					require.Len(t, batches[i], size, "batch %d should have %d tasks", i, size)
				}
			}
		})
	}
}

// TestAddDeduplicatesSamePointer is a regression test for the "Alias already exists"
// race: two services sharing the same image resolve to the same Resource pointer via
// Client.Resource(), but Stack.Add used to append it twice, causing parallel Ensure
// calls on the same object.
func TestAddDeduplicatesSamePointer(t *testing.T) {
	t.Parallel()
	r := newMockResource("nginx", KindImage, PriorityImage, false)

	stack := NewStack(nil)
	stack.Add(r, r) // same pointer twice, as mkUpStack does for shared images

	require.Len(t, stack.resources, 1, "same resource added twice must appear only once")
}

// ----------------------------------------------------------------------------
// Integration Tests
// ----------------------------------------------------------------------------

// TestParallelImageDownload verifies multiple images download in parallel.
// Uses tiny busybox variants to minimize bandwidth.
func TestParallelImageDownload(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()
	c := newRandomTestClient(t, ctx, "stack-parallel-")

	imageNames := []string{
		"docker.io/library/busybox:1.36",
		"docker.io/library/busybox:1.35",
		"docker.io/library/busybox:1.34",
	}

	stack := NewStack(c, StackWorkers(3))
	for _, name := range imageNames {
		img, err := c.Resource(KindImage, name, &ImageConfig{})
		require.NoError(t, err)
		stack.Add(img)
	}

	batches := stack.groupByKind()
	require.Len(t, batches, 1, "all images should be in one batch")
	require.Len(t, batches[0], 3, "batch should have 3 images")

	require.NoError(t, stack.Run(ctx, ActionEnsure, OptionCreate()))

	for _, name := range imageNames {
		img, err := c.Resource(KindImage, name, &ImageConfig{})
		require.NoError(t, err)
		require.True(t, img.IsEnsured(), "image %s should be ensured", name)
	}

	t.Logf("Successfully downloaded %d images in parallel", len(imageNames))
}

func TestStackHooksWithStack(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()
	c := newRandomTestClient(t, ctx, "stack-hooks-")

	var beforeCalled, afterCalled bool
	var afterErr error

	c.AddHookBefore(func(_ context.Context, action Action, r Resource, _ Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindProfile {
			if _, ok := r.(*Profile); ok {
				beforeCalled = true
			}
		}
		return err
	})

	c.AddHookAfter(func(_ context.Context, action Action, r Resource, _ Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindProfile {
			if _, ok := r.(*Profile); ok {
				afterCalled = true
				afterErr = err
			}
		}
		return err
	})

	stack := NewStack(c)
	profile, err := c.Resource(KindProfile, "test-hooks-stack", &ProfileConfig{})
	require.NoError(t, err)

	stack.Add(profile)
	require.NoError(t, stack.Run(ctx, ActionEnsure, OptionCreate()))
	require.True(t, beforeCalled, "before hook should be called")
	require.True(t, afterCalled, "after hook should be called")
	require.NoError(t, afterErr, "after hook should receive nil error")
}

func TestStackErrorAggregation(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()
	c := newRandomTestClient(t, ctx, "stack-erragg-")

	stack := NewStack(c)

	p1, err := c.Resource(KindProfile, "error-test-1", &ProfileConfig{})
	require.NoError(t, err)

	p2, err := c.Resource(KindProfile, "error-test-2", &ProfileConfig{})
	require.NoError(t, err)

	stack.Add(p1, p2)

	err = stack.Run(ctx, ActionEnsure)
	require.Error(t, err)
	require.Contains(t, err.Error(), "error-test-1")
	require.Contains(t, err.Error(), "error-test-2")
}

func TestStackInstanceWithSecrets(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()
	c := newRandomTestClient(t, ctx, "stack-secrets-")

	network, err := c.Resource(KindNetwork, "default", &NetworkConfig{})
	require.NoError(t, err)

	imageResource, err := c.Resource(KindImage, "docker.io/alpine:latest", &ImageConfig{})
	require.NoError(t, err)

	image, ok := imageResource.(*Image)
	require.True(t, ok)

	devices := []InstanceDevice{
		{
			Name: "eth0",
			Config: InstanceDeviceConfig{
				DeviceType: InstanceDeviceTypeNic,
				Network:    network,
			},
		},
	}

	secrets := []InstanceSecret{
		{
			Source:  "db_password",
			Content: []byte("super-secret-password"),
		},
		{
			Source:  "api_key",
			Target:  "/app/secrets/api.key",
			Content: []byte("my-api-key-value"),
			UID:     0,
			GID:     0,
			Mode:    0o440,
		},
	}

	instance, err := c.Resource(KindInstance, "app-with-secrets", &InstanceConfig{
		Image:   image.Name(),
		Devices: devices,
		Secrets: secrets,
	})
	require.NoError(t, err)

	stack := NewStack(c)
	stack.Add(network, image, instance)

	ensureStack := stack.ForAction(ActionEnsure)
	require.NoError(t, ensureStack.Run(ctx, ActionEnsure, OptionCreate()))
	for _, r := range ensureStack.All() {
		require.True(t, r.IsEnsured(), "resource %q should be ensured", r.Name())
	}
	require.NoError(t, stack.ForAction(ActionStart).Run(ctx, ActionStart))
	require.NoError(t, stack.ForAction(ActionStop).Run(ctx, ActionStop, OptionForce()))
}

func TestStackEnsureWithoutCreate_Fails(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()
	c := newRandomTestClient(t, ctx, "stack-nocreate-")

	profile, err := c.Resource(KindProfile, "p1", &ProfileConfig{})
	require.NoError(t, err)

	stack := NewStack(c)
	stack.Add(profile)
	require.Error(t, stack.ForAction(ActionEnsure).Run(ctx, ActionEnsure))
}

func TestStackSingleProfileEnsure(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()
	c := newRandomTestClient(t, ctx, "stack-profile-")

	profile, err := c.Resource(KindProfile, "p1", &ProfileConfig{})
	require.NoError(t, err)

	stack := NewStack(c)
	stack.Add(profile)

	ensureStack := stack.ForAction(ActionEnsure)
	require.NoError(t, ensureStack.Run(ctx, ActionEnsure, OptionCreate()))
	for _, r := range ensureStack.All() {
		require.True(t, r.IsEnsured(), "resource %q should be ensured", r.Name())
	}
}

func TestStackProfileAndNetworkMixedPriorities(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()
	c := newRandomTestClient(t, ctx, "stack-mixed-")

	profile, err := c.Resource(KindProfile, "p1", &ProfileConfig{})
	require.NoError(t, err)

	network, err := c.Resource(KindNetwork, "n1", &NetworkConfig{})
	require.NoError(t, err)

	stack := NewStack(c)
	stack.Add(profile, network)

	ensureStack := stack.ForAction(ActionEnsure)
	require.NoError(t, ensureStack.Run(ctx, ActionEnsure, OptionCreate()))
	for _, r := range ensureStack.All() {
		require.True(t, r.IsEnsured(), "resource %q should be ensured", r.Name())
	}
}

func TestStackSimpleNginx(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()
	c := newRandomTestClient(t, ctx, "stack-nginx-")

	network, err := c.Resource(KindNetwork, "default", &NetworkConfig{})
	require.NoError(t, err)

	imageResource, err := c.Resource(KindImage, "docker.io/nginx:alpine", &ImageConfig{})
	require.NoError(t, err)

	image, ok := imageResource.(*Image)
	require.True(t, ok)

	devices := []InstanceDevice{
		{
			Name: "eth0",
			Config: InstanceDeviceConfig{
				DeviceType: InstanceDeviceTypeNic,
				Network:    network,
			},
		},
	}

	instance, err := c.Resource(KindInstance, "web", &InstanceConfig{
		Image:   image.Name(),
		Devices: devices,
	})
	require.NoError(t, err)

	stack := NewStack(c)
	stack.Add(network, image, instance)

	ensureStack := stack.ForAction(ActionEnsure)
	require.NoError(t, ensureStack.Run(ctx, ActionEnsure, OptionCreate()))
	for _, r := range ensureStack.All() {
		require.True(t, r.IsEnsured(), "resource %q should be ensured", r.Name())
	}
	require.NoError(t, stack.ForAction(ActionStart).Run(ctx, ActionStart))
	require.NoError(t, stack.ForAction(ActionStop).Run(ctx, ActionStop, OptionForce()))
}

func TestStackNginxScale(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := context.Background()
	c := newRandomTestClient(t, ctx, "stack-scale-")

	network, err := c.Resource(KindNetwork, "default", &NetworkConfig{})
	require.NoError(t, err)

	imageResource, err := c.Resource(KindImage, "docker.io/nginx:alpine", &ImageConfig{})
	require.NoError(t, err)

	image, ok := imageResource.(*Image)
	require.True(t, ok)

	devices := []InstanceDevice{
		{
			Name: "eth0",
			Config: InstanceDeviceConfig{
				DeviceType: InstanceDeviceTypeNic,
				Network:    network,
			},
		},
	}

	resources := []Resource{network, image}
	for i := 1; i <= 3; i++ {
		instance, err := c.Resource(KindInstance, fmt.Sprintf("web-%d", i), &InstanceConfig{
			Image:   image.Name(),
			Devices: devices,
		})
		require.NoError(t, err)
		resources = append(resources, instance)
	}

	stack := NewStack(c)
	stack.Add(resources...)

	ensureStack := stack.ForAction(ActionEnsure)
	require.NoError(t, ensureStack.Run(ctx, ActionEnsure, OptionCreate()))
	for _, r := range ensureStack.All() {
		require.True(t, r.IsEnsured(), "resource %q should be ensured", r.Name())
	}
	require.NoError(t, stack.ForAction(ActionStart).Run(ctx, ActionStart))
	require.NoError(t, stack.ForAction(ActionStop).Run(ctx, ActionStop, OptionForce()))
}
