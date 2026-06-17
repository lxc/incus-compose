package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
)

func skipIfNoBuilder(t *testing.T) {
	t.Helper()
	if override := os.Getenv("INCUS_COMPOSE_BUILDER"); override != "" {
		if _, err := exec.LookPath(override); err != nil {
			t.Skipf("Skipping: INCUS_COMPOSE_BUILDER=%q not found", override)
		}
		return
	}
	if _, err := exec.LookPath("podman"); err == nil {
		return
	}
	if _, err := exec.LookPath("docker"); err == nil {
		return
	}
	t.Skip("Skipping: podman or docker not found")
}

func writeTempFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	return dir
}

func TestBuildCommandWithBuildFixture(t *testing.T) {
	skipSlow(t)
	skipLocal(t)
	skipIfNoBuilder(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	fixture := "../../test/fixtures/with-build/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", fixture, "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", fixture, "build")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)

	r, err := c.Resource(client.KindImage, "localhost/app:latest", &client.ImageConfig{})
	require.NoError(t, err)
	require.NoError(t, client.RunAction(ctx, r, client.ActionEnsure))

	r, err = c.Resource(client.KindImage, "localhost/app2:latest", &client.ImageConfig{})
	require.NoError(t, err)
	require.NoError(t, client.RunAction(ctx, r, client.ActionEnsure))
}

func TestBuildCommandWithServiceFilter(t *testing.T) {
	skipSlow(t)
	skipLocal(t)
	skipIfNoBuilder(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	fixture := "../../test/fixtures/with-build/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", fixture, "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", fixture, "build", "app")
	require.NoError(t, err)

	c := projectClient(t, ctx, pn)
	r, err := c.Resource(client.KindImage, "localhost/app:latest", &client.ImageConfig{})
	require.NoError(t, err)
	require.NoError(t, client.RunAction(ctx, r, client.ActionEnsure))

	r, err = c.Resource(client.KindImage, "localhost/app2:latest", &client.ImageConfig{})
	require.NoError(t, err)
	require.Error(t, client.RunAction(ctx, r, client.ActionEnsure))
}

func TestBuildCommandWithNoBuildServices(t *testing.T) {
	skipLocal(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	fixture := "../../test/fixtures/simple-nginx/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", fixture, "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", fixture, "build")
	require.NoError(t, err)
}

func TestBuildCommandWithNoMatchingBuildServices(t *testing.T) {
	skipLocal(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	fixture := "../../test/fixtures/with-build/compose.yaml"

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", fixture, "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", fixture, "build", "missing")
	require.Error(t, err)
}

func TestBuildCommandWithNonBuildServiceFilter(t *testing.T) {
	skipLocal(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	dir := writeTempFiles(t, map[string]string{
		"compose.yaml": `services:
  app:
    build: .
  sidecar:
    image: docker.io/nginx:alpine
`,
		"Dockerfile": "FROM docker.io/alpine:latest\n",
	})

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", filepath.Join(dir, "compose.yaml"), "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", filepath.Join(dir, "compose.yaml"), "build", "sidecar")
	require.Error(t, err)
}

func TestBuildCommandRejectsMultiplePlatforms(t *testing.T) {
	skipLocal(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	dir := writeTempFiles(t, map[string]string{
		"compose.yaml": `services:
  app:
    build:
      context: .
      platforms:
        - linux/amd64
        - linux/arm64
`,
		"Dockerfile": "FROM docker.io/alpine:latest\n",
	})

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", filepath.Join(dir, "compose.yaml"), "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", filepath.Join(dir, "compose.yaml"), "build")
	require.Error(t, err)
	require.Contains(t, err.Error(), "build.platforms with multiple platforms is not supported")
}

func TestBuildCommandRejectsUnsupportedPlatform(t *testing.T) {
	skipLocal(t)
	t.Parallel()

	ctx := context.Background()
	pn := t.Name()
	dir := writeTempFiles(t, map[string]string{
		"compose.yaml": `services:
  app:
    build:
      context: .
      platforms:
        - linux/unsupported
`,
		"Dockerfile": "FROM docker.io/alpine:latest\n",
	})

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", filepath.Join(dir, "compose.yaml"), "down", "--project")
	})

	_, _, err := runCommand(t, ctx, pn, "-f", filepath.Join(dir, "compose.yaml"), "build")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported build platform linux/unsupported")
}

func TestBuildCommandReportsMissingBuilder(t *testing.T) {
	skipLocal(t)

	ctx := context.Background()
	pn := t.Name()
	dir := writeTempFiles(t, map[string]string{
		"compose.yaml": `services:
  app:
    build: .
`,
		"Dockerfile": "FROM docker.io/alpine:latest\n",
	})

	t.Cleanup(func() {
		_, _, _ = runCommand(t, ctx, pn, "-f", filepath.Join(dir, "compose.yaml"), "down", "--project")
	})

	_, _, err := runCommand(
		t,
		ctx,
		pn,
		"-f", filepath.Join(dir, "compose.yaml"), "build", "--builder", "ic-unknown-builder",
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no container builder")
}
