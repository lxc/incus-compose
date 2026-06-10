package client

import (
	"archive/tar"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.yaml.in/yaml/v4"
)

type BuildSuite struct {
	suite.Suite
}

func TestBuildSuite(t *testing.T) {
	suite.Run(t, new(BuildSuite))
}

func (s *BuildSuite) TestBuildMetadataTar() {
	r, err := buildMetadataTar("local/myproject-myservice:latest", "x86_64", nil)
	s.Require().NoError(err)

	tr := tar.NewReader(r)
	hdr, err := tr.Next()
	s.Require().NoError(err)
	s.Equal("metadata.yaml", hdr.Name)

	data, err := io.ReadAll(tr)
	s.Require().NoError(err)

	var meta struct {
		Architecture string            `yaml:"architecture"`
		CreationDate int64             `yaml:"creation_date"`
		ExpiryDate   int64             `yaml:"expiry_date"`
		Properties   map[string]string `yaml:"properties"`
	}
	s.Require().NoError(yaml.Unmarshal(data, &meta))

	s.Equal("oci", meta.Properties["type"])
	s.Equal("x86_64", meta.Architecture)
	s.Greater(meta.CreationDate, int64(0))
	s.Equal(int64(0), meta.ExpiryDate)
}

func (s *BuildSuite) TestBuildMetadataTarWithoutConfigJSON() {
	r, err := buildMetadataTar("test-image", "x86_64", nil)
	s.Require().NoError(err)

	tr := tar.NewReader(r)
	_, err = tr.Next()
	s.Require().NoError(err)

	// No second entry when configJSON is nil.
	_, err = tr.Next()
	s.ErrorIs(err, io.EOF)
}

func (s *BuildSuite) TestBuildMetadataTarWithConfigJSON() {
	configJSON := []byte(`{"ociVersion":"1.2.0","process":{"args":["/bin/sh"]}}`)
	r, err := buildMetadataTar("test-image", "x86_64", configJSON)
	s.Require().NoError(err)

	tr := tar.NewReader(r)

	hdr, err := tr.Next()
	s.Require().NoError(err)
	s.Equal("metadata.yaml", hdr.Name)

	hdr, err = tr.Next()
	s.Require().NoError(err)
	s.Equal("config.json", hdr.Name)

	data, err := io.ReadAll(tr)
	s.Require().NoError(err)
	s.Equal(configJSON, data)

	_, err = tr.Next()
	s.ErrorIs(err, io.EOF)
}

func (s *BuildSuite) TestDetectBuilderEnvOverride() {
	s.T().Setenv("INCUS_COMPOSE_BUILDER", "echo")

	p, err := buildDetectBuilder()
	s.Require().NoError(err)
	s.Contains(p, "echo")
}

func (s *BuildSuite) TestDetectBuilderEnvOverrideMissing() {
	s.T().Setenv("INCUS_COMPOSE_BUILDER", "this-binary-does-not-exist-incus-compose-test")

	_, err := buildDetectBuilder()
	s.Error(err)
}

func (s *BuildSuite) TestDetectBuilderNoBuilderFound() {
	// Temporarily empty PATH to guarantee neither podman nor docker is found.
	orig := os.Getenv("PATH")
	s.T().Cleanup(func() { _ = os.Setenv("PATH", orig) })
	_ = os.Setenv("PATH", "")
	_ = os.Unsetenv("INCUS_COMPOSE_BUILDER")

	_, err := buildDetectBuilder()
	s.Error(err)
}

func TestOptionBuild(t *testing.T) {
	tests := []struct {
		mode BuildMode
	}{
		{BuildAuto},
		{BuildForce},
		{BuildNever},
	}
	for _, tt := range tests {
		opts := NewOptions(OptionBuild(tt.mode))
		assert.Equal(t, tt.mode, opts.Build)
	}
}

func TestIncusArchToPlatform(t *testing.T) {
	tests := []struct {
		arch     string
		platform string
	}{
		{"x86_64", "linux/amd64"},
		{"i686", "linux/386"},
		{"aarch64", "linux/arm64"},
		{"armv7l", "linux/arm/v7"},
		{"armv6l", "linux/arm/v6"},
		{"ppc64le", "linux/ppc64le"},
		{"s390x", "linux/s390x"},
		{"riscv64", "linux/riscv64"},
	}
	for _, tt := range tests {
		platform, ok := incusArchToPlatform(tt.arch)
		require.True(t, ok)
		assert.Equal(t, tt.platform, platform)
	}
}

func TestPlatformToIncusArch(t *testing.T) {
	arch, ok := platformToIncusArch("linux/amd64", []string{"x86_64", "i686"})
	require.True(t, ok)
	assert.Equal(t, "x86_64", arch)

	_, ok = platformToIncusArch("linux/arm64", []string{"x86_64", "i686"})
	assert.False(t, ok)
}

func TestBuildArgs_Podman(t *testing.T) {
	cfg := &BuildConfig{
		Context:    "/path/to/ctx",
		Dockerfile: "Containerfile",
		Args:       map[string]string{"FOO": "bar"},
		Platform:   "linux/amd64",
		Target:     "runtime",
		NoCache:    true,
		Pull:       false,
	}
	args := buildArgs("podman", cfg, "ic-compose-build-test", "/tmp/out.tar")
	require.Contains(t, args, "build")
	require.Contains(t, args, "-t")
	require.Contains(t, args, "ic-compose-build-test")
	require.Contains(t, args, "/path/to/ctx")
	require.Contains(t, args, "-f")
	require.Contains(t, args, "Containerfile")
	require.Contains(t, args, "--platform")
	require.Contains(t, args, "linux/amd64")
	require.Contains(t, args, "--target")
	require.Contains(t, args, "runtime")
	require.Contains(t, args, "--build-arg")
	require.Contains(t, args, "FOO=bar")
	require.Contains(t, args, "--no-cache")
	require.Contains(t, args, "type=tar,dest=/tmp/out.tar")
	// podman must NOT include "buildx"
	assert.NotContains(t, args, "buildx")
}

func TestBuildConfigWithInlineDockerfile(t *testing.T) {
	cfg, cleanup, err := buildConfigWithInlineDockerfile(&BuildConfig{
		Context:          "/path/to/ctx",
		DockerfileInline: "FROM docker.io/alpine:latest\n",
	})
	require.NoError(t, err)
	defer cleanup()

	assert.NotEmpty(t, cfg.Dockerfile)
	data, err := os.ReadFile(cfg.Dockerfile)
	require.NoError(t, err)
	assert.Equal(t, "FROM docker.io/alpine:latest\n", string(data))
}

func TestBuildConfigWithInlineDockerfileRejectsDockerfile(t *testing.T) {
	_, _, err := buildConfigWithInlineDockerfile(&BuildConfig{
		Dockerfile:       "Dockerfile",
		DockerfileInline: "FROM docker.io/alpine:latest\n",
	})
	assert.Error(t, err)
}

func TestBuildArgs_Docker(t *testing.T) {
	cfg := &BuildConfig{
		Context: "/path/to/ctx",
		Pull:    true,
	}
	args := buildArgs("/usr/bin/docker", cfg, "ic-compose-build-test", "/tmp/out.tar")
	require.Contains(t, args, "buildx")
	require.Contains(t, args, "build")
	require.Contains(t, args, "--pull")
	assert.NotContains(t, args, "--no-cache")
}
