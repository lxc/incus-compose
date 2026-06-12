package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
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

	stdout, _, err := runCommand(t, ctx, pn, "-f", fixture, "build")
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "Built image for service \"app\": localhost/with-build-app")
	require.Contains(t, stdout.String(), "Built image for service \"app2\": localhost/app2:latest")
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

	stdout, _, err := runCommand(t, ctx, pn, "-f", fixture, "build", "app")
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "Built image for service \"app\": localhost/with-build-app")
	require.NotContains(t, stdout.String(), "Built image for service \"app2\"")
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

	stdout, _, err := runCommand(t, ctx, pn, "-f", fixture, "build")
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "No services have a build: configuration.")
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

	stdout, _, err := runCommand(t, ctx, pn, "-f", fixture, "build", "missing")
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "No build-configured services matched the filter.")
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

	stdout, _, err := runCommand(t, ctx, pn, "-f", filepath.Join(dir, "compose.yaml"), "build", "sidecar")
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "No build-configured services matched the filter.")
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
