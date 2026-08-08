package client

import (
	"context"
	"strings"
	"testing"
	"time"

	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/shared"
)

// newUnopenedTestClient builds a project client with no event listener on it.
//
// Every test here writes a config key behind the client's back. On an opened
// client that write comes back as an instance-updated event, and the listener
// refreshes the resource on its own - so the test would pass with the sweep
// removed entirely. No listener means the sweep is the only thing that can.
func newUnopenedTestClient(t *testing.T, prefix string) *Client {
	t.Helper()

	gc, err := NewTestClient(t.Context())
	require.NoError(t, err)

	name := prefix + strings.ToLower(RandString(12))

	c, err := createProjectClient(gc, name)
	require.NoError(t, err)

	t.Cleanup(func() { _ = gc.DeleteProject(name, true) })

	return c
}

// newTestInstance ensures one instance and hands back the resource.
func newTestInstance(t *testing.T, c *Client, name string) *Instance {
	t.Helper()

	ctx := t.Context()

	imageResource, err := c.Resource(KindImage, "docker.io/nginx:alpine", &ImageConfig{})
	require.NoError(t, err)
	image, ok := imageResource.(*Image)
	require.True(t, ok)

	instResource, err := c.Resource(KindInstance, name, &InstanceConfig{
		Image:      image.Name(),
		Extensions: map[string]string{HealthStatusKey: shared.HealthStatusStarting},
	})
	require.NoError(t, err)
	inst, ok := instResource.(*Instance)
	require.True(t, ok)

	stack := NewStack(c)
	stack.Add(image, inst)
	require.NoError(t, stack.ForAction(ActionEnsure).Run(ctx, ActionEnsure, OptionCreate()))

	return inst
}

// writeStatus changes the health status the way ic-healthd does, behind the
// client's back.
func writeStatus(t *testing.T, c *Client, incusName, status string) {
	t.Helper()

	conn, err := c.Connection()
	require.NoError(t, err)

	info, err := conn.GetConnectionInfo()
	require.NoError(t, err)

	_, _, err = conn.RawQuery("PATCH", incusApi.NewURL().
		Path("1.0", "instances", incusName).
		Project(info.Project).
		Target(info.Target).
		String(), instanceConfigPatch{
		Config: map[string]string{HealthStatusKey: status},
	}, "")
	require.NoError(t, err)
}

// TestRefreshInstancesCoversAnEventGap is the half of the reconnect that
// carries meaning. A status that changed while the listener was down produces
// no event of its own, so without this sweep nothing would ever wake a waiter
// on it and `up` would sit until --dependency-timeout.
func TestRefreshInstancesCoversAnEventGap(t *testing.T) {
	t.Parallel()
	skipLocal(t)

	c := newUnopenedTestClient(t, "event-gap-")
	inst := newTestInstance(t, c, "web")

	writeStatus(t, c, inst.IncusName(), HealthStatusHealthy)

	// The resource still holds what it was created with: no event carried the
	// change, and nothing has re-read it.
	require.Equal(t, shared.HealthStatusStarting, inst.IncusInstance.Config[HealthStatusKey],
		"precondition: the out-of-band write must not be visible yet")

	c.refreshInstances(nil, "refresh")

	require.Equal(t, HealthStatusHealthy, inst.IncusInstance.Config[HealthStatusKey])
}

// TestRefreshInstancesWakesAWaiter pins the reason the sweep re-reads through
// fetch rather than reading the API directly: fetch broadcasts Updated.
func TestRefreshInstancesWakesAWaiter(t *testing.T) {
	t.Parallel()
	skipLocal(t)

	c := newUnopenedTestClient(t, "event-wake-")
	inst := newTestInstance(t, c, "web")

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	waited := make(chan error, 1)
	go func() {
		waited <- inst.waitForHealthCheck(ctx)
	}()

	// waitForHealthCheck fetches once before it waits, so let it reach the wait
	// before the status changes; otherwise it returns without needing a wakeup.
	require.Eventually(t, func() bool {
		return inst.IncusInstance.Config[HealthStatusKey] == shared.HealthStatusStarting
	}, 10*time.Second, 50*time.Millisecond)

	writeStatus(t, c, inst.IncusName(), HealthStatusHealthy)

	c.refreshInstances(nil, "refresh")

	select {
	case err := <-waited:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("the sweep did not wake the waiter")
	}
}

// TestAddEventHandlerSeesTheEnvelope pins what logs.go depends on: a project
// event names itself only on the envelope, never in the decoded payload.
func TestAddEventHandlerSeesTheEnvelope(t *testing.T) {
	t.Parallel()

	c := &Client{}

	seen := make(chan incusApi.Event, 1)
	c.AddEventHandler(func(event incusApi.Event, _ incusApi.EventLifecycle) {
		seen <- event
	})

	c.handleEvent(incusApi.Event{
		Type:     incusApi.EventTypeLifecycle,
		Project:  "blog",
		Metadata: []byte(`{"action":"project-deleted","source":"/1.0/projects/blog"}`),
	})

	select {
	case event := <-seen:
		require.Equal(t, "blog", event.Project)
	default:
		t.Fatal("the handler was never called")
	}
}
