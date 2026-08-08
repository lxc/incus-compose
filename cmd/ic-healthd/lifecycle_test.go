package main

import (
	"encoding/json"
	"testing"

	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/require"
)

// rawEvent builds the envelope Incus delivers, whose Project is the only place
// a project event ever names itself.
func rawEvent(t *testing.T, project string, lc incusApi.EventLifecycle) incusApi.Event {
	t.Helper()

	metadata, err := json.Marshal(lc)
	require.NoError(t, err)

	return incusApi.Event{
		Type:     incusApi.EventTypeLifecycle,
		Project:  project,
		Metadata: metadata,
	}
}

func TestDecodeLifecycleInstanceActions(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		action string
		want   instanceEventAction
	}{
		{incusApi.EventLifecycleInstanceStarted, instanceRestarted},
		{incusApi.EventLifecycleInstanceRestarted, instanceRestarted},
		{incusApi.EventLifecycleInstanceUpdated, instanceUpdated},
		{incusApi.EventLifecycleInstanceStopped, instanceStopped},
		{incusApi.EventLifecycleInstanceShutdown, instanceStopped},
		{incusApi.EventLifecycleInstanceDeleted, instanceDeleted},
	} {
		t.Run(tc.action, func(t *testing.T) {
			t.Parallel()

			ev, err := decodeLifecycle(rawEvent(t, "blog", incusApi.EventLifecycle{
				Action: tc.action,
				Name:   "web-1",
			}))

			require.NoError(t, err)
			require.Equal(t, "blog", ev.Project)
			require.Equal(t, "web-1", ev.Instance.Instance)
			require.Equal(t, tc.want, ev.Instance.Action)
			require.Empty(t, ev.ProjectAction)
		})
	}
}

func TestDecodeLifecycleProjectActions(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		action string
		want   projectEventAction
	}{
		{incusApi.EventLifecycleProjectCreated, projectCreated},
		{incusApi.EventLifecycleProjectUpdated, projectUpdated},
		{incusApi.EventLifecycleProjectDeleted, projectDeleted},
	} {
		t.Run(tc.action, func(t *testing.T) {
			t.Parallel()

			// A project event sets neither Name nor Project on the payload.
			ev, err := decodeLifecycle(rawEvent(t, "blog", incusApi.EventLifecycle{Action: tc.action}))

			require.NoError(t, err)
			require.Equal(t, "blog", ev.Project)
			require.Equal(t, tc.want, ev.ProjectAction)
			require.Empty(t, ev.Instance.Action)
		})
	}
}

func TestDecodeLifecycleProjectRenamed(t *testing.T) {
	t.Parallel()

	ev, err := decodeLifecycle(rawEvent(t, "new-name", incusApi.EventLifecycle{
		Action:  incusApi.EventLifecycleProjectRenamed,
		Context: map[string]any{"old_name": "old-name"},
	}))

	require.NoError(t, err)
	require.Equal(t, projectRenamed, ev.ProjectAction)
	require.Equal(t, "new-name", ev.Project)
	require.Equal(t, "old-name", ev.OldName)
}

// A rename we cannot attribute would stop the wrong scheduler, so it is an
// error rather than a silently ignored event.
func TestDecodeLifecycleProjectRenamedWithoutOldName(t *testing.T) {
	t.Parallel()

	for name, ctx := range map[string]map[string]any{
		"missing":    nil,
		"empty":      {"old_name": ""},
		"wrong type": {"old_name": 42},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := decodeLifecycle(rawEvent(t, "new-name", incusApi.EventLifecycle{
				Action:  incusApi.EventLifecycleProjectRenamed,
				Context: ctx,
			}))

			require.Error(t, err)
			require.NotErrorIs(t, err, errEventIgnored)
		})
	}
}

func TestDecodeLifecycleIgnores(t *testing.T) {
	t.Parallel()

	for name, lc := range map[string]incusApi.EventLifecycle{
		"instance action we do not route": {Action: incusApi.EventLifecycleInstanceCreated, Name: "web-1"},
		"instance event without a name":   {Action: incusApi.EventLifecycleInstanceStarted},
		"an unrelated resource":           {Action: incusApi.EventLifecycleNetworkCreated, Name: "br0"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := decodeLifecycle(rawEvent(t, "blog", lc))

			require.ErrorIs(t, err, errEventIgnored)
		})
	}
}

func TestDecodeLifecycleRejectsBadMetadata(t *testing.T) {
	t.Parallel()

	_, err := decodeLifecycle(incusApi.Event{
		Type:     incusApi.EventTypeLifecycle,
		Project:  "blog",
		Metadata: []byte("not json"),
	})

	require.Error(t, err)
	require.NotErrorIs(t, err, errEventIgnored)
}
