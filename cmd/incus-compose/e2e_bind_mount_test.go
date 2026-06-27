package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	incusClient "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
)

func TestBindMounts(t *testing.T) {
	t.Parallel()
	skipLocal(t)

	pn := t.Name()
	compose := "../../test/fixtures/with-bind-mounts/compose.yaml"
	ctx := context.Background()

	gc, err := client.NewTestClient(ctx)
	if err != nil {
		t.Skip(err.Error())
	}

	skipNotSameHost(t, gc)

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, _, err = runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	c, err := gc.EnsureProject(pn)
	require.NoError(t, err)

	t.Run("file bind-mount", func(t *testing.T) {
		t.Parallel()
		err := pollContainerHTTP(c, "file-web-1", "file-bind-mount-ok", 60*time.Second)
		require.NoError(t, err)
	})

	t.Run("dir bind-mount", func(t *testing.T) {
		t.Parallel()
		err := pollContainerHTTP(c, "dir-web-1", "dir-bind-mount-ok", 60*time.Second)
		require.NoError(t, err)
	})
}

func TestBindMountErrorsOnRemote(t *testing.T) {
	t.Parallel()
	skipLocal(t)

	pn := t.Name()
	compose := "../../test/fixtures/with-bind-mounts/compose.yaml"
	ctx := context.Background()

	gc, err := client.NewTestClient(ctx)
	if err != nil {
		t.Skip(err.Error())
	}

	if gc.SameHost() == nil {
		t.Skip("this test requires the incus server to be on a remote host")
	}

	_, _, err = runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.Error(t, err)
}

func TestSeededBindMounts(t *testing.T) {
	t.Parallel()
	skipLocal(t)

	pn := t.Name()
	compose := "../../test/fixtures/with-seeded-bind-mounts/compose.yaml"
	ctx := context.Background()

	gc, err := client.NewTestClient(ctx)
	if err != nil {
		t.Skip(err.Error())
	}

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, _, err = runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	c, err := gc.EnsureProject(pn)
	require.NoError(t, err)

	t.Run("file bind-mount", func(t *testing.T) {
		t.Parallel()
		err := pollContainerHTTP(c, "file-web-1", "file-bind-mount-ok", 60*time.Second)
		require.NoError(t, err)
	})

	t.Run("dir bind-mount", func(t *testing.T) {
		t.Parallel()
		err := pollContainerHTTP(c, "dir-web-1", "dir-bind-mount-ok", 60*time.Second)
		require.NoError(t, err)
	})
}

func TestBindMountNoShift(t *testing.T) {
	t.Parallel()
	skipLocal(t)

	pn := t.Name()
	compose := "../../test/fixtures/with-bind-mount-no-shift/compose.yaml"
	ctx := context.Background()

	gc, err := client.NewTestClient(ctx)
	if err != nil {
		t.Skip(err.Error())
	}

	skipNotSameHost(t, gc)

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", compose, "down", "--project")
	})

	_, _, err = runCommand(t, ctx, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	c, err := gc.EnsureProject(pn)
	require.NoError(t, err)

	// With security.shifted=false the bind mount is not id-shifted, so the host
	// file shows up as nobody (65534) inside the unprivileged container.
	err = pollContainerExec(c, "web-1",
		[]string{"ls", "-ln", "/usr/share/nginx/html/index.html"}, "65534", 60*time.Second)
	require.NoError(t, err)
}

// pollContainerHTTP execs wget inside the named instance until the response
// body contains want or timeout elapses.
func pollContainerHTTP(c *client.Client, instance, want string, timeout time.Duration) error {
	return pollContainerExec(c, instance, []string{"wget", "-q", "-O", "-", "http://127.0.0.1:8080/"}, want, timeout)
}

// pollContainerExec runs cmd inside the named instance until stdout contains
// want or timeout elapses. Checks before sleeping so the last attempt is never
// skipped.
func pollContainerExec(c *client.Client, instance string, cmd []string, want string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastOut string
	var lastErr error

	for {
		conn, err := c.Connection()
		if err != nil {
			return err
		}
		out, err := containerExec(conn, instance, cmd)
		if err == nil && strings.Contains(out, want) {
			return nil
		}
		lastOut, lastErr = out, err

		if time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}

	return fmt.Errorf("timed out after %s: last output=%q: %w", timeout, lastOut, lastErr)
}

// containerExec runs cmd inside the container and returns stdout.
func containerExec(conn *incusClient.ProtocolIncus, instance string, cmd []string) (string, error) {
	req := incusApi.InstanceExecPost{
		Command:     cmd,
		WaitForWS:   true,
		Interactive: false,
	}

	var stdout, stderr bytes.Buffer
	args := incusClient.InstanceExecArgs{
		Stdin:    nil,
		Stdout:   &stdout,
		Stderr:   &stderr,
		DataDone: make(chan bool),
	}

	op, err := conn.ExecInstance(instance, req, &args)
	if err != nil {
		return "", err
	}

	<-args.DataDone

	if err := op.Wait(); err != nil {
		return "", err
	}

	opAPI := op.Get()
	if rc, ok := opAPI.Metadata["return"].(float64); ok && int(rc) != 0 {
		return "", fmt.Errorf("exit code %d: %s", int(rc), stderr.String())
	}

	return stdout.String(), nil
}
