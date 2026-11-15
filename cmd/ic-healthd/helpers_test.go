package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/avast/retry-go/v5"
	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/iclient"
	"github.com/lxc/incus-compose/internal/testlib"
	"github.com/lxc/incus-compose/shared"
)

// testImage is warm in the test cache and carries the busybox tools the health
// checks use: true, false, wget and httpd.
const testImage = "docker.io/library/busybox:glibc"

// testProject creates a throwaway Incus project, deleted on teardown. Each test
// gets its own, which is also how the daemon is used.
func testProject(t *testing.T, prefix string) *client.Client {
	t.Helper()

	// Not t.Context(): it is canceled before t.Cleanup runs, so the teardown delete would never reach Incus.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	gc, err := client.NewTestClient(ctx)
	require.NoError(t, err)
	require.NoError(t, gc.Connect())

	name := prefix + strings.ToLower(shared.RandString(12))

	c, err := gc.EnsureProject(name,
		client.EnsureProjectWithCreate(),
		client.EnsureProjectWithSkipHealthd(),
	)
	require.NoError(t, err)
	require.NoError(t, c.Open())

	// The client and Incus log to the shared stderr, so name the project here.
	fmt.Fprintf(t.Output(), "incus project: %s\n", name)

	t.Cleanup(func() {
		err := c.Done()
		if err != nil {
			t.Errorf("closing client for project %s: %v", name, err)
		}

		// Canceling the daemon abandons its calls, not the operations Incus has
		// already started: a status write can still hold an instance lock here.
		err = retry.New(
			retry.Attempts(20),
			retry.Delay(250*time.Millisecond),
			retry.LastErrorOnly(true),
			retry.RetryIf(func(err error) bool {
				return errors.Is(err, iclient.ErrInstanceBusy)
			}),
		).Do(func() error {
			return gc.DeleteProject(name, true)
		})

		// A test that renames its project leaves nothing behind under this one.
		if err != nil && !incusApi.StatusErrorCheck(err, http.StatusNotFound) {
			t.Errorf("deleting project %s: %v", name, err)
		}
	})

	return c
}

// testContainer creates a busybox container carrying keys and returns its Incus
// name. It idles rather than exiting, so a check has something to exec into.
func testContainer(t *testing.T, c *client.Client, name string, keys map[string]string, start bool) string {
	t.Helper()

	ctx := t.Context()

	imgRes, err := c.Resource(client.KindImage, testImage, &client.ImageConfig{})
	require.NoError(t, err)

	img, ok := imgRes.(*client.Image)
	require.True(t, ok)

	instRes, err := c.Resource(client.KindInstance, name, &client.InstanceConfig{
		Image:      img.Name(),
		Extensions: keys,
		Entrypoint: []string{"/bin/sh"},
	})
	require.NoError(t, err)

	inst, ok := instRes.(*client.Instance)
	require.True(t, ok)

	// NoHealthd throughout, or user.healthcheck.stopped makes the instance
	// look deliberately stopped to the daemon under test.
	stack := client.NewStack(c)
	stack.Add(img, inst)
	require.NoError(t, stack.ForAction(client.ActionEnsure).
		Run(ctx, client.ActionEnsure, client.OptionCreate(), client.OptionNoHealthd()))

	if start {
		require.NoError(t, client.RunAction(ctx, inst,
			client.ActionStart, client.OptionNoHealthd()))
	}

	return inst.IncusName()
}

// testConn returns the Incus connection the assertions read through.
func testConn(t *testing.T, _ *client.Client) *iclient.Connection {
	t.Helper()

	config, err := iclient.ReadConfig("")
	require.NoError(t, err)

	info, err := config.RemoteInfos(os.Getenv("INCUS_REMOTE"))
	require.NoError(t, err)

	conn, err := iclient.NewConnection(info)
	require.NoError(t, err)

	return conn
}

// healthKeys is shorthand for the user.healthcheck.* map a test wants, carrying
// the opt-in. Tests about its absence set the keys by hand.
func healthKeys(pairs map[string]string) map[string]string {
	out := map[string]string{
		shared.HealthEnabledKey: "true",
	}

	for k, v := range pairs {
		out[shared.HealthKeyPrefix+k] = v
	}

	return out
}

func TestMain(m *testing.M) {
	os.Exit(testlib.Main(m))
}
