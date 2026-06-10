// Package client - container image building.
//
// Build support intentionally shells out to podman or docker rather than using
// the buildah Go library. The buildah library pulls in containers/storage,
// containers/image, BuildKit, and an OCI runtime, which is too heavy a
// dependency for this project. The CLI path gives the same result with zero
// additional Go dependencies.
package client

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	incusApi "github.com/lxc/incus/v7/shared/api"
	ispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opencontainers/umoci/oci/cas/dir"
	"github.com/opencontainers/umoci/oci/casext"
	"github.com/opencontainers/umoci/oci/layer"
)

// BuildMode controls how build-configured images are treated during Ensure.
type BuildMode int

const (
	// BuildAuto builds the image only when it is missing (default).
	BuildAuto BuildMode = iota
	// BuildForce rebuilds the image even if an existing one is present.
	BuildForce
	// BuildNever never builds; returns an error if the image is missing.
	BuildNever
)

// BuildConfig holds the parameters read from a compose service's build: block.
type BuildConfig struct {
	// Context is the build context directory (absolute path).
	Context string

	// Dockerfile is an optional path to the Containerfile/Dockerfile.
	// Empty means the builder uses its default (Containerfile or Dockerfile in Context).
	Dockerfile string

	// DockerfileInline is inline Dockerfile content from compose build.dockerfile_inline.
	DockerfileInline string

	// Target is the Dockerfile stage to build.
	Target string

	// Platform is the OCI platform to build for, for example linux/amd64.
	Platform string

	// Args are build-time variables (--build-arg).
	Args map[string]string

	// NoCache disables layer caching during the build.
	NoCache bool

	// Pull always attempts to pull a newer version of the base image.
	Pull bool
}

// buildDetectBuilder returns the path to the first available container builder.
// The INCUS_COMPOSE_BUILDER env var overrides auto-detection.
func buildDetectBuilder() (string, error) {
	if override := os.Getenv("INCUS_COMPOSE_BUILDER"); override != "" {
		p, err := exec.LookPath(override)
		if err != nil {
			return "", fmt.Errorf("INCUS_COMPOSE_BUILDER=%q not found: %w", override, err)
		}
		return p, nil
	}
	for _, name := range []string{"podman", "docker"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no container builder found; install podman or docker, or set INCUS_COMPOSE_BUILDER")
}

// buildRootfs runs the container builder and returns both the rootfs tar and
// the OCI runtime config.json bytes. The rootfs is a ReadCloser that deletes
// its temp file on Close. stderr is forwarded to logW.
func buildRootfs(ctx context.Context, builder string, cfg *BuildConfig, logW io.Writer) (io.ReadCloser, []byte, error) {
	tmpTag := fmt.Sprintf("ic-compose-build-%x", time.Now().UnixNano())

	rootfsTmp, err := os.CreateTemp("", "incus-compose-rootfs-*.tar")
	if err != nil {
		return nil, nil, fmt.Errorf("creating temp file: %w", err)
	}
	rootfsPath := rootfsTmp.Name()
	rootfsTmp.Close()

	buildCfg, cleanup, err := buildConfigWithInlineDockerfile(cfg)
	if err != nil {
		_ = os.Remove(rootfsPath)
		return nil, nil, err
	}
	defer cleanup()

	args := buildArgs(builder, buildCfg, tmpTag, rootfsPath)
	cmd := exec.CommandContext(ctx, builder, args...)
	cmd.Stderr = logW
	if err := cmd.Run(); err != nil {
		_ = os.Remove(rootfsPath)
		return nil, nil, fmt.Errorf("building container image: %w", err)
	}

	// Generate config.json from the stored image (podman only).
	var configJSON []byte
	if !strings.HasSuffix(builder, "docker") {
		configJSON, err = buildConfigJSON(ctx, builder, tmpTag, logW)
		if err != nil {
			_ = os.Remove(rootfsPath)
			return nil, nil, err
		}
	}

	// Remove the temporary image tag; ignore errors (best-effort cleanup).
	rmi := exec.CommandContext(ctx, builder, "rmi", tmpTag)
	rmi.Stderr = logW
	_ = rmi.Run()

	f, err := os.Open(rootfsPath)
	if err != nil {
		_ = os.Remove(rootfsPath)
		return nil, nil, fmt.Errorf("opening rootfs: %w", err)
	}
	return &tempFile{File: f, path: rootfsPath}, configJSON, nil
}

// buildConfigJSON saves the image as an OCI directory layout, then uses umoci
// to convert it to an OCI Runtime Spec config.json.
func buildConfigJSON(ctx context.Context, builder, tmpTag string, logW io.Writer) ([]byte, error) {
	ociDir, err := os.MkdirTemp("", "incus-compose-oci-*")
	if err != nil {
		return nil, fmt.Errorf("creating OCI temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(ociDir) }()

	save := exec.CommandContext(ctx, builder, "save", "--format", "oci-dir", "-o", ociDir, tmpTag)
	save.Stderr = logW
	if err := save.Run(); err != nil {
		return nil, fmt.Errorf("saving OCI image: %w", err)
	}

	engine, err := dir.Open(ociDir)
	if err != nil {
		return nil, fmt.Errorf("opening OCI layout: %w", err)
	}
	defer func() { _ = engine.Close() }()

	engineExt := casext.NewEngine(engine)

	refs, err := engineExt.ListReferences(ctx)
	if err != nil || len(refs) == 0 {
		return nil, fmt.Errorf("listing OCI references: %w", err)
	}

	paths, err := engineExt.ResolveReference(ctx, refs[0])
	if err != nil || len(paths) == 0 {
		return nil, fmt.Errorf("resolving OCI reference %q: %w", refs[0], err)
	}

	manifestBlob, err := engineExt.FromDescriptor(ctx, paths[0].Descriptor())
	if err != nil {
		return nil, fmt.Errorf("loading OCI manifest: %w", err)
	}
	defer func() { _ = manifestBlob.Close() }()

	manifest, ok := manifestBlob.Data.(ispec.Manifest)
	if !ok {
		return nil, fmt.Errorf("unexpected OCI blob type: %s", manifestBlob.Descriptor.MediaType)
	}

	var buf bytes.Buffer
	if err := layer.UnpackRuntimeJSON(ctx, engine, &buf, "", manifest, &layer.MapOptions{}); err != nil {
		return nil, fmt.Errorf("generating config.json: %w", err)
	}
	return buf.Bytes(), nil
}

func buildConfigWithInlineDockerfile(cfg *BuildConfig) (*BuildConfig, func(), error) {
	if cfg.DockerfileInline == "" {
		return cfg, func() {}, nil
	}
	if cfg.Dockerfile != "" {
		return nil, func() {}, fmt.Errorf("build.dockerfile and build.dockerfile_inline cannot both be set")
	}

	f, err := os.CreateTemp("", "incus-compose-Dockerfile-*")
	if err != nil {
		return nil, func() {}, fmt.Errorf("creating inline Dockerfile: %w", err)
	}
	path := f.Name()
	if _, err := f.WriteString(cfg.DockerfileInline); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, func() {}, fmt.Errorf("writing inline Dockerfile: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, func() {}, fmt.Errorf("closing inline Dockerfile: %w", err)
	}

	buildCfg := *cfg
	buildCfg.Dockerfile = path
	return &buildCfg, func() { _ = os.Remove(path) }, nil
}

func buildArgs(builder string, cfg *BuildConfig, tmpTag, dest string) []string {
	args := []string{}
	if strings.HasSuffix(builder, "docker") {
		args = append(args, "buildx")
	}
	args = append(args, "build", "-t", tmpTag, cfg.Context)
	if cfg.Dockerfile != "" {
		args = append(args, "-f", cfg.Dockerfile)
	}
	if cfg.Platform != "" {
		args = append(args, "--platform", cfg.Platform)
	}
	if cfg.Target != "" {
		args = append(args, "--target", cfg.Target)
	}
	for k, v := range cfg.Args {
		args = append(args, "--build-arg", k+"="+v)
	}
	if cfg.NoCache {
		args = append(args, "--no-cache")
	}
	if cfg.Pull {
		args = append(args, "--pull")
	}
	args = append(args, "--output", "type=tar,dest="+dest)
	return args
}

type tempFile struct {
	*os.File
	path string
}

// Close closes the file and removes it from disk.
func (t *tempFile) Close() error {
	err := t.File.Close()
	_ = os.Remove(t.path)
	return err
}

// incusArchToPlatform maps an Incus architecture name to an OCI platform.
func incusArchToPlatform(arch string) (string, bool) {
	switch arch {
	case "x86_64":
		return "linux/amd64", true
	case "i686":
		return "linux/386", true
	case "aarch64":
		return "linux/arm64", true
	case "armv7", "armv7l":
		return "linux/arm/v7", true
	case "armv6", "armv6l":
		return "linux/arm/v6", true
	case "ppc64le":
		return "linux/ppc64le", true
	case "s390x":
		return "linux/s390x", true
	case "riscv64":
		return "linux/riscv64", true
	}
	return "", false
}

func platformToIncusArch(platform string, arches []string) (string, bool) {
	for _, arch := range arches {
		candidate, ok := incusArchToPlatform(arch)
		if ok && candidate == platform {
			return arch, true
		}
	}
	return "", false
}

// buildMetadataTar returns an in-memory tar containing metadata.yaml (JSON
// content per Incus convention) and, when provided, an OCI config.json.
func buildMetadataTar(name, arch string, configJSON []byte) (io.Reader, error) {
	metaJSON, err := json.Marshal(incusApi.ImageMetadata{
		Architecture: arch,
		CreationDate: time.Now().Unix(),
		Properties: map[string]string{
			"description": name + " (built by incus-compose)",
			"type":        "oci",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling image metadata: %w", err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	if err := tw.WriteHeader(&tar.Header{
		Name: "metadata.yaml",
		Mode: 0o644,
		Size: int64(len(metaJSON)),
	}); err != nil {
		return nil, fmt.Errorf("writing metadata tar header: %w", err)
	}
	if _, err := tw.Write(metaJSON); err != nil {
		return nil, fmt.Errorf("writing metadata.yaml: %w", err)
	}

	if len(configJSON) > 0 {
		if err := tw.WriteHeader(&tar.Header{
			Name: "config.json",
			Mode: 0o644,
			Size: int64(len(configJSON)),
		}); err != nil {
			return nil, fmt.Errorf("writing config.json tar header: %w", err)
		}
		if _, err := tw.Write(configJSON); err != nil {
			return nil, fmt.Errorf("writing config.json: %w", err)
		}
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("closing metadata tar: %w", err)
	}
	return &buf, nil
}
