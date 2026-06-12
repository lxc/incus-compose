package client

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/distribution/reference"
	incusClient "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"
)

// ImageConfig contains the source and cache configuration for an image.
type ImageConfig struct {
	// CacheServer is an image server to use as cache (for library users).
	// Takes precedence over CacheProject.
	CacheServer incusClient.InstanceServer

	// CacheProject is the project name to use as cache (for CLI users).
	// The project will be created if it doesn't exist.
	// Ignored if CacheServer is set.
	CacheProject string

	// Build, when set, marks this image as locally built rather than pulled
	// from a registry. Ensure will shell out to podman/docker instead of
	// calling CopyImage.
	Build *BuildConfig
}

// GetConfig returns the configuration.
func (c *ImageConfig) GetConfig() any {
	return c
}

var _ Config = (*ImageConfig)(nil)

// Image represents an OCI or native Incus image copied to the Incus image cache.
type Image struct {
	*BaseResource

	client    *Client
	Config    ImageConfig
	incusName string
	created   bool

	// remote is the domain part of the image reference
	remote string

	// image is the image reference without the remote prefix
	image string

	// cache is the resolved instance server for caching
	cache incusClient.InstanceServer

	// source is the resolved image server for this image.
	source incusClient.ImageServer

	// nativeIncus indicates this is a native Incus image (protocol "incus")
	// rather than an OCI image (protocol "oci").
	nativeIncus bool

	// State - nil means not ensured.
	IncusAlias *incusApi.ImageAliasesEntry
	ETag       string

	// OCI metadata extracted from the image (empty/0 for native Incus images).
	UID        uint64
	GID        uint64
	Entrypoint string
	Cwd        string

	// size is the total image size in bytes as reported by the source server,
	// resolved best-effort before a download. 0 when unknown.
	size int64
}

// newImage returns an existing Image resource or creates a new one.
// The name should be a Docker-style image reference or native Incus reference (remote:image).
func newImage(c *Client, name string, configGetter Config) (*Image, error) {
	if configGetter == nil {
		return nil, ErrUnknownConfig.WithKindName(KindImage, name)
	}

	cConfig, ok := configGetter.GetConfig().(*ImageConfig)
	if !ok {
		return nil, ErrUnknownConfig.WithKindName(KindImage, name)
	}
	configCopy := *cConfig
	config := &configCopy

	var remote, image, incusName string

	// Try to parse as native Incus format first: "remote:image/path"
	// This takes precedence if CliConfig is provided and remote exists in the config.
	if c.globalClient.cliConfig != nil && strings.Contains(name, ":") {
		parts := strings.SplitN(name, ":", 2)
		remoteName := parts[0]

		if _, ok := c.globalClient.cliConfig.Remotes[remoteName]; ok {
			remote = remoteName
			image = parts[1]
			incusName = name
		}
	}

	// If not resolved as native, try Docker/OCI reference
	if incusName == "" {
		ref, err := reference.ParseDockerRef(name)
		if err != nil {
			return nil, ErrInvalidFormat.WithKindName(KindImage, name).Wrap(err)
		}

		originalDomain := reference.Domain(ref)
		remote = originalDomain
		if remote == "localhost" {
			// Handle podman style "localhost" images.
			remote = "local"
		}

		image, _ = strings.CutPrefix(ref.String(), originalDomain+"/")
		incusName = remote + "/" + image
	}

	return &Image{
		BaseResource: NewBaseResource(KindImage, name, PriorityImage),
		client:       c,
		incusName:    incusName,
		Config:       *config,
		remote:       remote,
		image:        image,
	}, nil
}

// String is for debugging.
func (r *Image) String() string {
	return fmt.Sprintf("%v(%v)", r.kind, r.incusName)
}

// IncusName returns the image alias name used in Incus.
func (r *Image) IncusName() string {
	return r.incusName
}

// IsEnsured returns true if the image has been fetched/copied to cache.
func (r *Image) IsEnsured() bool {
	return r.IncusAlias != nil
}

// Created returns true if the image was created during the last Ensure call.
func (r *Image) Created() bool {
	return r.created
}

// Status returns the image status: "Unknown" or "Cached".
func (r *Image) Status() string {
	if r.IsEnsured() {
		return "Cached"
	}
	return "Unknown"
}

// Remote returns the image remote.
func (r *Image) Remote() string {
	return r.remote
}

// Size returns the total image size in bytes as reported by the source server,
// or 0 when unknown. It is resolved best-effort before a download starts.
func (r *Image) Size() int64 {
	return r.size
}

// NativeIncus returns true if this is a native Incus image.
func (r *Image) NativeIncus() bool {
	return r.nativeIncus
}

// Ensure retrieves an existing image from cache or copies it if Create option is set.
// With the Pull option, a cached image is refreshed from its source registry.
// When ImageConfig.Build is set the image is built locally via podman/docker.
func (r *Image) Ensure(ctx context.Context, opts ...Option) error {
	args := NewOptions(opts...)

	if r.Config.Build != nil {
		return r.ensureBuild(ctx, args)
	}

	// Resolve cache: CacheServer > CacheProject > default imageCache
	if r.cache == nil {
		if r.Config.CacheServer != nil {
			r.cache = r.Config.CacheServer
		} else if r.Config.CacheProject != "" {
			cacheClient, err := r.client.globalClient.EnsureProject(r.Config.CacheProject, EnsureProjectWithCreate())
			if err != nil {
				return fmt.Errorf("ensuring cache project %s: %w", r.Config.CacheProject, err)
			}
			r.cache = cacheClient.incus
		} else {
			r.cache = r.client.imageCache
		}
	}

	// Resolve source image server
	if r.source == nil {
		if r.client.globalClient.cliConfig != nil {
			is, err := r.client.globalClient.cliConfig.GetImageServer(r.remote)
			if err != nil {
				return ErrImageSource.WithText("getting image server for " + r.remote).Wrap(err)
			}
			r.source = is
			if connInfo, err := is.GetConnectionInfo(); err == nil && connInfo.Protocol == "incus" {
				r.nativeIncus = true
			}
		}

		if r.source == nil {
			return ErrImageSource.WithText("couldn't find an image server")
		}
	}

	if err := r.client.hookBefore(ctx, ActionEnsure, r, args, nil); err != nil {
		return err
	}

	// Try to get existing image
	err := r.get()
	if err == nil {
		if args.Pull {
			err = r.refresh(ctx, args)
		}

		err = r.client.hookAfter(ctx, ActionEnsure, r, args, err)

		return err
	}

	if !args.Create || !errors.Is(err, ErrNotFound) {
		err = r.client.hookAfter(ctx, ActionEnsure, r, args, err)

		return err
	}

	err = r.create(ctx, args)
	err = r.client.hookAfter(ctx, ActionEnsure, r, args, err)

	return err
}

func (r *Image) get() error {
	// Check if image alias exists in cache
	alias, eTag, err := r.client.incus.GetImageAlias(r.incusName)
	if err != nil {
		r.IncusAlias = nil
		r.ETag = ""
		return ErrNotFound.Wrap(err)
	}

	r.IncusAlias = alias
	r.ETag = eTag

	if img, _, err := r.client.incus.GetImage(alias.Target); err == nil {
		r.readOCIConfigFromProperties(img.Properties)
	}

	return nil
}

// refresh updates the cached image from its source registry if the remote
// fingerprint has changed.
//
// RefreshImage (incus image refresh) is unreliable for OCI floating tags:
// Incus fingerprints are computed from layer digests, not manifest SHAs, so a
// registry update that only changes manifest metadata is invisible to refresh.
// The only reliable approach is to delete the stale cache entry and re-copy,
// which always runs skopeo copy against the current registry state.
//
// Before deleting, the remote fingerprint is queried (skopeo inspect for OCI,
// no layer pull). If it matches the cached fingerprint, the refresh is skipped.
// On query failure the refresh proceeds to be safe.
func (r *Image) refresh(ctx context.Context, args Options) error {
	// No source || no cache || no alias, no download.
	if r.source == nil || r.cache == nil || r.IncusAlias == nil {
		return nil
	}

	// Check if the remote image has the same fingerprint
	remoteAlias, _, err := r.source.GetImageAlias(r.image)
	if err == nil && remoteAlias != nil && remoteAlias.Target == r.IncusAlias.Target {
		return nil
	}

	if r.cache != nil {
		op, err := r.cache.DeleteImage(r.IncusAlias.Target)

		// On the cache the error is ignored.
		if err = r.client.hookOperation(ctx, ActionDelete, r, args, op, err); err != nil {
			r.client.LogDebug("deleting stale cache image for refresh", "error", err)
			return nil
		}
	}

	op, err := r.client.incus.DeleteImage(r.IncusAlias.Target)
	if err = r.client.hookOperation(ctx, ActionEnsure, r, args, op, err); err != nil {
		r.client.LogDebug("deleting stale project image for refresh", "error", err)
		return nil
	}

	r.IncusAlias = nil
	r.ETag = ""
	return r.create(ctx, args)
}

func (r *Image) create(ctx context.Context, args Options) error {
	if r.source == nil {
		return ErrImageSource.WithText("not configured")
	}

	var cacheAlias *incusApi.ImageAliasesEntry
	var err error

	// Check if the cache has the alias
	cacheAlias, _, err = r.cache.GetImageAlias(r.incusName)
	if err != nil {
		// Resolve the total size up front so progress display can show scale
		// even for OCI pulls, which report no percentage. Best-effort: the
		// alias lookup is cached by the source server and reused by CopyImage.
		sourceAlias, _, err := r.source.GetImageAlias(r.image)
		if err != nil {
			r.client.LogDebug("Image not found on source", "image", r.image, "error", err)
			return ErrNotFound.WithText("on source")
		}

		sourceImage, _, err := r.source.GetImage(sourceAlias.Target)
		if err != nil {
			r.size = sourceImage.Size
		}

		cacheCopyArgs := &incusClient.ImageCopyArgs{
			Mode: "pull",
		}

		// Build image info for copy
		cacheImgInfo := incusApi.Image{
			Fingerprint: r.image,
			ImagePut: incusApi.ImagePut{
				Public: true,
			},
		}

		// Copy from source to cache
		op, err := r.cache.CopyImage(r.source, cacheImgInfo, cacheCopyArgs)

		// Wait for copy to complete
		if err = r.client.hookRemoteOperation(ctx, ActionEnsure, r, args, op, err); err != nil {
			return ErrCreate.WithText("caching image").Wrap(err)
		}

		if err := extractAndStoreOCIConfig(ctx, r.cache, sourceAlias.Target, r.client.Config().DefaultStoragePool); err != nil {
			return ErrCreate.WithText("extracting OCI config from image").Wrap(err)
		}

		cacheAlias = &incusApi.ImageAliasesEntry{
			ImageAliasesEntryPut: incusApi.ImageAliasesEntryPut{Target: sourceAlias.Target},
		}
	}

	_, _, err = r.client.incus.GetImageAlias(r.incusName)
	if err != nil {
		projectCopyArgs := &incusClient.ImageCopyArgs{
			Aliases:    []incusApi.ImageAlias{{Name: r.incusName}},
			AutoUpdate: true,
			Mode:       "pull",
		}

		if r.Cwd == "/" {
			// Copy from cache, read oci.* from it.
			if img, _, err := r.cache.GetImage(cacheAlias.Target); err == nil {
				r.readOCIConfigFromProperties(img.Properties)
			}
		}

		// Build image info for copy
		projectImageInfo := incusApi.Image{
			Fingerprint: cacheAlias.Target,
			ImagePut: incusApi.ImagePut{
				Properties: map[string]string{
					"oci.uid":        strconv.FormatUint(r.UID, 10),
					"oci.gid":        strconv.FormatUint(r.GID, 10),
					"oci.cwd":        r.Cwd,
					"oci.entrypoint": r.Entrypoint,
				},
			},
		}

		// Copy from cache to project
		op, err := r.client.incus.CopyImage(r.cache, projectImageInfo, projectCopyArgs)

		// Wait for copy to complete
		if err = r.client.hookRemoteOperation(ctx, ActionEnsure, r, args, op, err); err != nil {
			return ErrCreate.WithText("project image").Wrap(err)
		}
	}

	return r.get()
}

func waitInstance(ctx context.Context, server incusClient.InstanceServer, name string, inErr error) error {
	var outErr error
	if inErr != nil {
		outErr = inErr
	}

	if _, _, err := server.GetInstance(name); err != nil {
		return fmt.Errorf("getting the temp instance after create: %w", outErr)
	}

	ticker := time.NewTicker(100 * time.Millisecond)

	for {
		select {
		case <-ctx.Done():
			return errors.New("failed to wait for a temp instance")

		case <-ticker.C:
			// A concurrent process may be running the same extraction; check if the
			// instance already exists and read from it below without deleting it.
			if _, _, err := server.GetInstance(name); err != nil {
				return nil
			}
		}
	}
}

// extractAndStoreOCIConfig creates a temporary stopped container from this image,
// reads oci.uid/oci.gid/oci.entrypoint/oci.cwd from its config, stores them as
// image properties, then deletes the container.
// If a concurrent process already created the temp instance, it reads from that
// instance instead (and skips deletion).
// Non-fatal: callers should log and continue on error.
func extractAndStoreOCIConfig(ctx context.Context, server incusClient.InstanceServer, fingerprint string, pool string) error {
	tempName := SanitizeIncusName("ic-uid-"+fingerprint, MaxIncusNameLen-7)

	req := incusApi.InstancesPost{
		Name: tempName,
		Type: incusApi.InstanceTypeContainer,
		Source: incusApi.InstanceSource{
			Type:        "image",
			Fingerprint: fingerprint,
		},
		InstancePut: incusApi.InstancePut{
			Devices: map[string]map[string]string{
				"root": {
					"type": "disk",
					"path": "/",
					"pool": pool,
				},
			},
		},
	}

	op, err := server.CreateInstance(req)
	if err != nil {
		waitCtx, cancel := context.WithTimeout(ctx, 1*time.Minute)

		err = waitInstance(waitCtx, server, tempName, err)
		cancel()
		return err
	}

	err = op.WaitContext(ctx)
	if err != nil {
		waitCtx, cancel := context.WithTimeout(ctx, 1*time.Minute)

		err = waitInstance(waitCtx, server, tempName, err)
		cancel()
		return err
	}

	instance, _, err := server.GetInstance(tempName)
	if err != nil {
		return fmt.Errorf("getting the temp instance after create: %w", err)
	}

	defer func() {
		if deleteOp, err := server.DeleteInstance(tempName); err == nil {
			_ = deleteOp.Wait()
		}
	}()

	uid, gid, err := extractUIDGID(instance)
	if err != nil {
		return fmt.Errorf("extracting uid/gid: %w", err)
	}

	entrypoint := instance.Config["oci.entrypoint"]
	cwd := instance.Config["oci.cwd"]

	if uid == 0 && gid == 0 && entrypoint == "" && cwd == "" {
		return nil
	}

	img, eTag, err := server.GetImage(fingerprint)
	if err != nil {
		return fmt.Errorf("getting image for property update: %w", err)
	}

	props := maps.Clone(img.Properties)
	if props == nil {
		props = make(map[string]string)
	}
	props["oci.uid"] = strconv.FormatUint(uint64(uid), 10)
	props["oci.gid"] = strconv.FormatUint(uint64(gid), 10)
	props["oci.entrypoint"] = entrypoint
	props["oci.cwd"] = cwd

	if err := server.UpdateImage(fingerprint, incusApi.ImagePut{
		AutoUpdate: img.AutoUpdate,
		Properties: props,
		Public:     img.Public,
		ExpiresAt:  img.ExpiresAt,
		Profiles:   img.Profiles,
	}, eTag); err != nil {
		return fmt.Errorf("storing OCI config as image properties: %w", err)
	}

	return nil
}

// readOCIConfigFromProperties reads oci.* values from image properties.
func (r *Image) readOCIConfigFromProperties(props map[string]string) {
	if uidStr, ok := props["oci.uid"]; ok {
		if uid64, err := strconv.ParseUint(uidStr, 10, 32); err == nil {
			r.UID = uid64
		}
	}
	if gidStr, ok := props["oci.gid"]; ok {
		if gid64, err := strconv.ParseUint(gidStr, 10, 32); err == nil {
			r.GID = gid64
		}
	}
	r.Entrypoint = props["oci.entrypoint"]
	r.Cwd = props["oci.cwd"]
}

// ensureBuild handles the Ensure lifecycle for locally-built images. It does not
// touch the remote-pull machinery (source image server, cache project).
func (r *Image) ensureBuild(ctx context.Context, args Options) error {
	if err := r.client.hookBefore(ctx, ActionEnsure, r, args, nil); err != nil {
		return err
	}

	err := r.get()
	if err == nil {
		// Image already present in the project.
		if args.Build.Mode == BuildForce {
			// Delete the existing image so we can replace it.
			if r.IncusAlias != nil {
				op, delErr := r.client.incus.DeleteImage(r.IncusAlias.Target)
				if hookErr := r.client.hookOperation(ctx, ActionEnsure, r, args, op, delErr); hookErr != nil {
					r.client.LogDebug("deleting image for rebuild", "error", hookErr)
				}
			}
			r.IncusAlias = nil
			r.ETag = ""
			err = r.buildImage(ctx, args)
		}
		// BuildAuto or BuildNever with an existing image: nothing to do.
	} else {
		if args.Build.Mode == BuildNever {
			err = fmt.Errorf("image %q is missing and --no-build was set", r.incusName)
		} else if args.Create {
			err = r.buildImage(ctx, args)
		}
		// !args.Create and BuildAuto: leave err non-nil (not found, don't create).
	}

	err = r.client.hookAfter(ctx, ActionEnsure, r, args, err)
	return err
}

// buildImage shells out to the detected container builder, imports the rootfs
// into Incus as a split (metadata + rootfs) image, and records the alias.
func (r *Image) buildImage(ctx context.Context, args Options) error {
	server, _, err := r.client.incus.GetServer()
	if err != nil {
		return ErrCreate.WithText("getting Incus server info").Wrap(err)
	}
	if len(server.Environment.Architectures) == 0 {
		return ErrCreate.WithText("Incus server has no supported architectures")
	}

	buildCfg := *r.Config.Build
	incusArch := server.Environment.Architectures[0]
	if buildCfg.Platform != "" {
		var ok bool
		incusArch, ok = platformToIncusArch(buildCfg.Platform, server.Environment.Architectures)
		if !ok {
			return ErrCreate.WithText("unsupported build platform " + buildCfg.Platform)
		}
	} else {
		platform, ok := incusArchToPlatform(incusArch)
		if !ok {
			return ErrCreate.WithText("unsupported Incus architecture " + incusArch)
		}
		buildCfg.Platform = platform
	}

	builder, err := buildDetectBuilder(args.Build.PreferredBuilder)
	if err != nil {
		return ErrCreate.WithText("no container builder").Wrap(err)
	}

	rootfs, configJSON, err := buildRootfs(ctx, builder, &buildCfg, os.Stderr)
	if err != nil {
		return ErrCreate.WithText("building container image").Wrap(err)
	}
	defer rootfs.Close()

	meta, err := buildMetadataTar(r.incusName, incusArch, configJSON)
	if err != nil {
		return ErrCreate.WithText("building image metadata").Wrap(err)
	}

	op, err := r.client.incus.CreateImage(incusApi.ImagesPost{
		Aliases: []incusApi.ImageAlias{{Name: r.incusName}},
	}, &incusClient.ImageCreateArgs{
		MetaFile:   meta,
		MetaName:   "metadata.tar",
		RootfsFile: rootfs,
		RootfsName: "rootfs.tar",
	})
	if err = r.client.hookOperation(ctx, ActionEnsure, r, args, op, err); err != nil {
		return ErrCreate.WithText("importing built image").Wrap(err)
	}

	alias, eTag, err := r.client.incus.GetImageAlias(r.incusName)
	if err != nil {
		return ErrCreate.WithText("fetching alias after build").Wrap(err)
	}

	r.IncusAlias = alias
	r.ETag = eTag
	r.created = true
	return nil
}

// Delete removes the per-project copy of the image from the active project.
//
// Projects are created with features.images=true, so creating an instance
// copies the image into the active project. Those copies are removed here on
// down; without it they accumulate and go stale relative to the auto-updated
// cache (see issue #29). The cache lives in a separate project and is left
// untouched, so cached images persist across down/up cycles.
//
// Delete is idempotent: a missing per-project copy is not an error.
func (r *Image) Delete(ctx context.Context, opts ...Option) error {
	if !r.IsEnsured() {
		r.IncusAlias = nil
		r.ETag = ""

		r.client.resources.Remove(r)
		return nil
	}

	if err := r.get(); err != nil {
		// Already gone server side
		r.client.resources.Remove(r)
		return err
	}

	options := NewOptions(opts...)

	if err := r.client.hookBefore(ctx, ActionDelete, r, options, nil); err != nil {
		r.IncusAlias = nil
		r.ETag = ""

		r.client.resources.Remove(r)
		return err
	}

	// Resolve the per-project copy in the active project (not the cache). A
	// missing alias means nothing was copied here, so there is nothing to do.
	alias, _, err := r.client.incus.GetImageAlias(r.incusName)
	if err != nil || alias == nil {
		r.IncusAlias = nil
		r.ETag = ""

		r.client.resources.Remove(r)

		return r.client.hookAfter(ctx, ActionDelete, r, options, err)
	}

	op, err := r.client.incus.DeleteImage(alias.Target)

	err = r.client.hookOperation(ctx, ActionDelete, r, options, op, err)
	r.IncusAlias = nil
	r.ETag = ""

	r.client.resources.Remove(r)
	return r.client.hookAfter(ctx, ActionDelete, r, options, err)
}

var (
	_ Resource   = (*Image)(nil)
	_ EnsureAble = (*Image)(nil)
	_ DeleteAble = (*Image)(nil)
)
