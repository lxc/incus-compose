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

	"gitlab.com/r3j0/incus-compose/client"
)

const (
	bindMountsCompose     = "../../test/fixtures/with-bind-mounts/compose.yaml"
	bindMountsProjectName = "with-bind-mounts"
)

func TestBindMounts(t *testing.T) {
	ctx := context.Background()
	gc, err := client.NewTestClient(ctx)
	if err != nil {
		t.Skip(err.Error())
	}

	run := func(args ...string) error {
		var stdout, stderr bytes.Buffer
		cmd := newRootCommand()
		cmd.Writer = &stdout
		cmd.ErrWriter = &stderr
		return cmd.Run(ctx, append([]string{"incus-compose"}, args...))
	}

	t.Cleanup(func() {
		_ = run("-f", bindMountsCompose, "down", "--project")
	})

	if err := run("-f", bindMountsCompose, "up", "--detach"); err != nil {
		t.Fatalf("up: %v", err)
	}

	c, err := gc.EnsureProject(bindMountsProjectName)
	require.NoError(t, err)

	t.Run("file bind-mount", func(t *testing.T) {
		if err := pollContainerHTTP(c, "file-web-1", "file-bind-mount-ok", 60*time.Second); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("dir bind-mount", func(t *testing.T) {
		if err := pollContainerHTTP(c, "dir-web-1", "dir-bind-mount-ok", 60*time.Second); err != nil {
			t.Fatal(err)
		}
	})
}

// pollContainerHTTP execs wget inside the named instance until the response
// body contains want or timeout elapses. Checks before sleeping so the last
// attempt is never skipped.
func pollContainerHTTP(c *client.Client, instance, want string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastOut string
	var lastErr error

	for {
		out, err := containerGet(c.Connection(), instance, "http://127.0.0.1:8080/")
		if err == nil && strings.Contains(out, want) {
			return nil
		}
		lastOut, lastErr = out, err

		if time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}

	return fmt.Errorf("timed out after %s: err=%v last output=%q", timeout, lastErr, lastOut)
}

// containerGet runs wget inside the container and returns stdout.
func containerGet(conn *incusClient.ProtocolIncus, instance, url string) (string, error) {
	req := incusApi.InstanceExecPost{
		Command:     []string{"wget", "-q", "-O", "-", url},
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
