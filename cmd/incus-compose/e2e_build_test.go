package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type E2EBuildSuite struct {
	suite.Suite
	ctx    context.Context
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

func TestE2EBuildSuite(t *testing.T) {
	if os.Getenv("INCUS_COMPOSE_TEST_SLOW") == "" {
		t.Skip("Skipping: env INCUS_COMPOSE_TEST_SLOW is not set")
	}

	suite.Run(t, new(E2EBuildSuite))
}

func (s *E2EBuildSuite) SetupSuite() {
	s.ctx = context.Background()
	s.stdout = &bytes.Buffer{}
	s.stderr = &bytes.Buffer{}
}

func (s *E2EBuildSuite) run(args ...string) error {
	s.stdout.Reset()
	s.stderr.Reset()
	cmd := newRootCommand()
	cmd.Writer = s.stdout
	cmd.ErrWriter = s.stderr
	return cmd.Run(s.ctx, append([]string{"incus-compose"}, args...))
}

func (s *E2EBuildSuite) skipIfLocal() {
	if os.Getenv("INCUS_COMPOSE_TEST_LOCAL") != "" {
		s.T().Skip("Skipping: env INCUS_COMPOSE_TEST_LOCAL is set")
	}
}

func (s *E2EBuildSuite) skipIfNoBuilder() {
	if override := os.Getenv("INCUS_COMPOSE_BUILDER"); override != "" {
		if _, err := exec.LookPath(override); err != nil {
			s.T().Skipf("Skipping: INCUS_COMPOSE_BUILDER=%q not found", override)
		}
		return
	}
	if _, err := exec.LookPath("podman"); err == nil {
		return
	}
	if _, err := exec.LookPath("docker"); err == nil {
		return
	}
	s.T().Skip("Skipping: podman or docker not found")
}

func (s *E2EBuildSuite) writeCompose(files map[string]string) string {
	dir := s.T().TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		s.Require().NoError(os.MkdirAll(filepath.Dir(path), 0o700))
		s.Require().NoError(os.WriteFile(path, []byte(content), 0o600))
	}
	return filepath.Join(dir, "compose.yaml")
}

func (s *E2EBuildSuite) TestBuildCommandWithBuildFixture() {
	s.skipIfLocal()
	s.skipIfNoBuilder()

	fixture := "../../test/fixtures/with-build/compose.yaml"
	defer func() {
		_ = s.run("-f", fixture, "down", "--project")
	}()

	err := s.run("-f", fixture, "build")
	s.NoError(err)
	s.Contains(s.stdout.String(), "Built image for service \"app\": localhost/with-build-app")
	s.Contains(s.stdout.String(), "Built image for service \"app2\": localhost/app2:latest")
}

func (s *E2EBuildSuite) TestBuildCommandWithServiceFilter() {
	s.skipIfLocal()
	s.skipIfNoBuilder()

	fixture := "../../test/fixtures/with-build/compose.yaml"
	defer func() {
		_ = s.run("-f", fixture, "down", "--project")
	}()

	err := s.run("-f", fixture, "build", "app")
	s.NoError(err)
	s.Contains(s.stdout.String(), "Built image for service \"app\": localhost/with-build-app")
	s.NotContains(s.stdout.String(), "Built image for service \"app2\"")
}

func (s *E2EBuildSuite) TestBuildCommandWithNoBuildServices() {
	s.skipIfLocal()

	fixture := "../../test/fixtures/simple-nginx/compose.yaml"
	defer func() {
		_ = s.run("-f", fixture, "down", "--project")
	}()

	err := s.run("-f", fixture, "build")
	s.NoError(err)
	s.Contains(s.stdout.String(), "No services have a build: configuration.")
}

func (s *E2EBuildSuite) TestBuildCommandWithNoMatchingBuildServices() {
	s.skipIfLocal()

	fixture := "../../test/fixtures/with-build/compose.yaml"
	defer func() {
		_ = s.run("-f", fixture, "down", "--project")
	}()

	err := s.run("-f", fixture, "build", "missing")
	s.NoError(err)
	s.Contains(s.stdout.String(), "No build-configured services matched the filter.")
}

func (s *E2EBuildSuite) TestBuildCommandWithNonBuildServiceFilter() {
	s.skipIfLocal()

	fixture := s.writeCompose(map[string]string{
		"compose.yaml": `services:
  app:
    build: .
  sidecar:
    image: docker.io/nginx:alpine
`,
		"Dockerfile": "FROM docker.io/alpine:latest\n",
	})
	defer func() {
		_ = s.run("-f", fixture, "down", "--project")
	}()

	err := s.run("-f", fixture, "build", "sidecar")
	s.NoError(err)
	s.Contains(s.stdout.String(), "No build-configured services matched the filter.")
}

func (s *E2EBuildSuite) TestBuildCommandRejectsMultiplePlatforms() {
	s.skipIfLocal()

	fixture := s.writeCompose(map[string]string{
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
	defer func() {
		_ = s.run("-f", fixture, "down", "--project")
	}()

	err := s.run("-f", fixture, "build")
	s.Error(err)
	s.Contains(err.Error(), "build.platforms with multiple platforms is not supported")
}

func (s *E2EBuildSuite) TestBuildCommandRejectsUnsupportedPlatform() {
	s.skipIfLocal()

	fixture := s.writeCompose(map[string]string{
		"compose.yaml": `services:
  app:
    build:
      context: .
      platforms:
        - linux/unsupported
`,
		"Dockerfile": "FROM docker.io/alpine:latest\n",
	})
	defer func() {
		_ = s.run("-f", fixture, "down", "--project")
	}()

	err := s.run("-f", fixture, "build")
	s.Error(err)
	s.Contains(err.Error(), "unsupported build platform linux/unsupported")
}

func (s *E2EBuildSuite) TestBuildCommandReportsMissingBuilder() {
	s.skipIfLocal()
	s.T().Setenv("INCUS_COMPOSE_BUILDER", "this-builder-does-not-exist-incus-compose-test")

	fixture := s.writeCompose(map[string]string{
		"compose.yaml": `services:
  app:
    build: .
`,
		"Dockerfile": "FROM docker.io/alpine:latest\n",
	})
	defer func() {
		_ = s.run("-f", fixture, "down", "--project")
	}()

	err := s.run("-f", fixture, "build")
	s.Error(err)
	s.Contains(err.Error(), "no container builder")
}
