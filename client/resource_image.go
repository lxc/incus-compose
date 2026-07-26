package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/avast/retry-go/v5"
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

	// A list of service dependencies for log output.
	Services []string
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

	// conn is this resource's own event-isolated Incus connection, set in
	// Ensure() (which always runs before any other action) so concurrent
	// workers never share a *ProtocolIncus. See Client.Connection.
	conn *incusClient.ProtocolIncus

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

	conn, err := r.client.Connection()
	if err != nil {
		return err
	}
	r.conn = conn

	err = r.setupCacheAndSource()
	if err != nil {
		err = r.client.hookBefore(ctx, ActionEnsure, r, args, nil)
		err = r.client.hookAfter(ctx, ActionEnsure, r, args, err)
		return err
	}

	if r.Config.Build != nil {
		return r.ensureBuild(ctx, args)
	}

	// Just run refresh (with create) if pulling.
	if args.Pull {
		err = r.deleteCached(ctx, args)

		if err == nil {
			err = r.client.hookBefore(ctx, ActionEnsure, r, args, nil)
			if err != nil {
				return err
			}

			r.IncusAlias = nil
			r.ETag = ""
			err = r.create(ctx, args)
			return r.client.hookAfter(ctx, ActionEnsure, r, args, err)
		}
	}

	if err := r.client.hookBefore(ctx, ActionEnsure, r, args, nil); err != nil {
		return err
	}

	// Try to get existing image
	err = r.get()
	if err == nil {
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

func (r *Image) setupCacheAndSource() error {
	// Resolve cache: CacheServer > CacheProject > default imageCache which might be nil
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

			connInfo, err := r.source.GetConnectionInfo()
			if err == nil && connInfo.Protocol == "incus" {
				r.nativeIncus = true
			}
		}

		if r.source == nil {
			return ErrImageSource.WithText("couldn't find an image server")
		}
	}

	return nil
}

func (r *Image) get() error {
	// Check if image alias exists in cache
	alias, eTag, err := r.conn.GetImageAlias(r.incusName)
	if err != nil {
		r.IncusAlias = nil
		r.ETag = ""
		return ErrNotFound.Wrap(err)
	}

	r.IncusAlias = alias
	r.ETag = eTag

	img, _, err := r.conn.GetImage(alias.Target)
	if err == nil {
		r.size = img.Size
		r.readOCIConfigFromProperties(img.Properties)
	}

	return nil
}

// deleteCached deletes the image from the cache and the project.
func (r *Image) deleteCached(ctx context.Context, args Options) error {
	err := r.client.hookBefore(ctx, ActionDelete, r, args, nil)
	if err != nil {
		return err
	}

	// Check if the remote image has the same fingerprint
	sourceAlias, _, err := r.source.GetImageAlias(r.image)
	if err != nil && r.IncusAlias == nil {
		// Image not found on the source, stop here but go on with ensure.
		r.client.LogDebug("Image not found on the source", "resource", r)
		return r.client.hookAfter(ctx, ActionDelete, r, args, nil)
	}

	if r.cache != nil {
		cacheAlias, _, err := r.cache.GetImageAlias(r.incusName)
		if err == nil && (sourceAlias == nil || sourceAlias.Target != cacheAlias.Target) {
			r.client.LogDebug("Deleting from cache", "resource", r)
			op, err := r.cache.DeleteImage(cacheAlias.Target)

			// On the cache the error is ignored.
			if err = r.client.hookOperation(ctx, ActionDelete, r, args, op, err); err != nil {
				r.client.LogDebug("Deleting stale cache image for refresh", "error", err)
			}
		} else {
			r.client.LogDebug("Image not found on the cache or it is recent", "resource", r)
		}
	}

	err = r.get()
	if err != nil {
		// Project doesn't have the image, ignore this.
		return r.client.hookAfter(ctx, ActionDelete, r, args, nil)
	}

	r.client.LogDebug("Deleting from project", "resource", r)
	op, err := r.conn.DeleteImage(r.IncusAlias.Target)
	if err = r.client.hookOperation(ctx, ActionEnsure, r, args, op, err); err != nil {
		r.client.LogDebug("deleting stale project image for refresh", "error", err)
		return r.client.hookAfter(ctx, ActionDelete, r, args, err)
	}

	err = r.client.hookAfter(ctx, ActionDelete, r, args, err)
	if err != nil {
		return err
	}

	return nil
}

func (r *Image) copyToCache(ctx context.Context, args Options) (*incusApi.ImageAliasesEntry, error) {
	if r.source == nil {
		return nil, ErrImageSource.WithText("not configured")
	}

	var cacheImgInfo incusApi.Image
	if r.NativeIncus() {
		alias, _, err := r.source.GetImageAlias(r.image)
		if err != nil {
			return nil, ErrNotFound.WithText("image not found on source").Wrap(err)
		}
		image, _, err := r.source.GetImage(alias.Target)
		if err != nil {
			return nil, ErrNotFound.WithText("resolved alias not found on source").Wrap(err)
		}
		cacheImgInfo = incusApi.Image{
			Fingerprint: image.Fingerprint,
			ImagePut: incusApi.ImagePut{
				Public: true,
			},
		}
	} else {
		cacheImgInfo = incusApi.Image{
			Fingerprint: r.image,
			ImagePut: incusApi.ImagePut{
				Public: true,
			},
		}
	}

	cacheCopyArgs := &incusClient.ImageCopyArgs{
		Aliases: []incusApi.ImageAlias{
			{
				Name: r.incusName,
			},
		},
		Mode: "pull",
	}

	// Copy from source to cache, we just warn on error as parallel operations might have caused this.
	op, err := r.cache.CopyImage(r.source, cacheImgInfo, cacheCopyArgs)
	if err != nil {
		r.client.LogWarn("Creating a copy operation failed", "resource", r, "error", err)
	} else {
		// Wait for copy to complete
		err = r.client.hookRemoteOperation(ctx, ActionEnsure, r, args, op, err)
		if err != nil {
			if strings.Contains(err.Error(), "Failed remote image download") {
				return nil, ErrNotFound.Wrap(err)
			}

			r.client.LogWarn("Copy to cache failed", "resource", r, "error", err)
		}
	}

	// Retry fetch for up to 5 minutes, this is required because multiple workers may try to copy it.
	cacheAlias, err := retry.NewWithData[*incusApi.ImageAliasesEntry](
		retry.Attempts(10),
		retry.Delay(30*time.Second),
	).Do(func() (*incusApi.ImageAliasesEntry, error) {
		alias, _, err := r.cache.GetImageAlias(r.incusName)
		return alias, err
	})
	if err != nil {
		return nil, ErrNotFound.WithText("on cache after copy").Wrap(err)
	}

	// Extract oci informations with a temporary instance.
	err = extractAndStoreOCIConfig(ctx, r.cache, cacheAlias.Target, r.client.Config().DefaultStoragePool)
	if err != nil {
		return nil, ErrCreate.WithText("extracting OCI config from the image").Wrap(err)
	}

	return cacheAlias, nil
}

func (r *Image) create(ctx context.Context, args Options) error {
	if r.cache != nil {
		cacheAlias, _, err := r.cache.GetImageAlias(r.incusName)
		if err != nil {
			var cacheErr error
			cacheAlias, cacheErr = r.copyToCache(ctx, args)
			if cacheErr != nil && errors.Is(cacheErr, ErrNotFound) {
				cacheAlias, _, err = r.cache.GetImageAlias(r.incusName)
				if err != nil {
					return ErrNotFound.WithText("on cache and source").Wrap(cacheErr)
				}

				// Extract oci informations with a temporary instance.
				err = extractAndStoreOCIConfig(ctx, r.cache, cacheAlias.Target, r.client.Config().DefaultStoragePool)
				if err != nil {
					return ErrCreate.WithText("extracting OCI config from the image").Wrap(err)
				}
			} else if cacheErr != nil {
				return err
			}
		}
		projectCopyArgs := &incusClient.ImageCopyArgs{
			Aliases: []incusApi.ImageAlias{
				{
					Name: r.incusName,
				},
			},
			Mode: "pull",
		}

		// Copy from cache, read oci.* from it.
		img, _, err := r.cache.GetImage(cacheAlias.Target)
		if err != nil {
			return ErrCreate.WithText("cannot resolve the image from cache after copy")
		}

		r.size = img.Size
		r.readOCIConfigFromProperties(img.Properties)

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
		op, err := r.conn.CopyImage(r.cache, projectImageInfo, projectCopyArgs)

		// Wait for copy to complete
		if err = r.client.hookRemoteOperation(ctx, ActionEnsure, r, args, op, err); err != nil {
			return ErrCreate.WithText("project image").Wrap(err)
		}
	} else {
		if r.source == nil {
			return ErrImageSource.WithText("not configured")
		}

		var targetImageInfo incusApi.Image
		if r.NativeIncus() {
			alias, _, err := r.source.GetImageAlias(r.image)
			if err != nil {
				return ErrNotFound.WithText("on source").Wrap(err)
			}

			image, _, err := r.source.GetImage(alias.Target)
			if err != nil {
				return ErrNotFound.WithText("resolved alias not found").Wrap(err)
			}

			r.size = image.Size

			targetImageInfo = incusApi.Image{
				Fingerprint: image.Fingerprint,
				ImagePut: incusApi.ImagePut{
					Public: true,
				},
			}
		} else {
			targetImageInfo = incusApi.Image{
				Fingerprint: r.image,
				ImagePut: incusApi.ImagePut{
					Public: true,
				},
			}
		}

		targetCopyArgs := &incusClient.ImageCopyArgs{
			Aliases: []incusApi.ImageAlias{
				{
					Name: r.incusName,
				},
			},
			Mode: "pull",
		}

		op, err := r.conn.CopyImage(r.source, targetImageInfo, targetCopyArgs)
		if err != nil {
			r.client.LogWarn("Creating a copy operation failed", "resource", r, "error", err)
		} else {
			// Wait for copy to complete
			err = r.client.hookRemoteOperation(ctx, ActionEnsure, r, args, op, err)
			if err != nil {
				r.client.LogWarn("Copy to project failed", "resource", r, "error", err)
			}
		}

		targetAlias, _, err := r.conn.GetImageAlias(r.incusName)
		if err != nil {
			return ErrNotFound.WithText("on project after copy").Wrap(err)
		}

		// Extract oci informations with a temporary instance.
		err = extractAndStoreOCIConfig(ctx, r.conn, targetAlias.Target, r.client.Config().DefaultStoragePool)
		if err != nil {
			return ErrCreate.WithText("extracting OCI config from the image").Wrap(err)
		}
	}

	return r.get()
}

// extractAndStoreOCIConfig creates a temporary stopped container from this image,
// reads oci.uid/oci.gid/oci.entrypoint/oci.cwd from its config, stores them as
// image properties, then deletes the container.
func extractAndStoreOCIConfig(ctx context.Context, server incusClient.InstanceServer, fingerprint string, pool string) error {
	img, _, err := server.GetImage(fingerprint)
	if err != nil {
		return err
	}

	// Check if already extracted
	if _, ok := img.Properties["oci.uid"]; ok {
		return nil
	}

	tempName := "ic-uid-" + SanitizeIncusName(RandString(16), MaxIncusNameLen-7)

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

	// Create
	op, err := server.CreateInstance(req)
	if err == nil {
		// Execute create, ignore error.
		err = op.WaitContext(ctx)
		if err == nil {
			defer func() {
				if deleteOp, err := server.DeleteInstance(tempName); err == nil {
					_ = deleteOp.Wait()
				}
			}()
		} else {
			slog.Warn("Failed to create a temp instance for an image (1)", "fingerprint", fingerprint[16:], "error", err)
		}
	} else {
		slog.Warn("Failed to create a temp instance for an image (2)", "fingerprint", fingerprint[16:], "error", err)
	}

	// fetch
	instance, _, err := server.GetInstance(tempName)
	if err != nil {
		return err
	}

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
	props["oci.uid"] = strconv.FormatUint(uid, 10)
	props["oci.gid"] = strconv.FormatUint(gid, 10)
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
			r.IncusAlias = nil
			r.ETag = ""
			err = r.buildImage(ctx, args)
		}
		// BuildAuto or BuildNever with an existing image: nothing to do.
	} else {
		if args.Build.Mode == BuildNever {
			err = errors.New("image is missing and building is disabled")
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
	server, _, err := r.conn.GetServer()
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

	rootfs, configJSON, err := buildRootfs(ctx, r.client, builder, &buildCfg, args.Stdout, args.Stderr)
	if err != nil {
		return ErrCreate.WithText("building container image").Wrap(err)
	}
	defer r.client.WarnError(rootfs.Close, "Failure during close")

	meta, err := buildMetadataTar(r.incusName, incusArch, configJSON)
	if err != nil {
		return ErrCreate.WithText("building image metadata").Wrap(err)
	}

	if r.cache == nil {
		op, err := r.conn.CreateImage(incusApi.ImagesPost{
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

		alias, eTag, err := r.conn.GetImageAlias(r.incusName)
		if err != nil {
			return ErrCreate.WithText("fetching alias after build").Wrap(err)
		}

		err = extractAndStoreOCIConfig(ctx, r.conn, r.IncusAlias.Target, r.client.Config().DefaultStoragePool)
		if err != nil {
			return err
		}

		r.IncusAlias = alias
		r.ETag = eTag
		r.created = true
	} else {
		cacheAlias, _, err := r.cache.GetImageAlias(r.incusName)
		if err == nil {
			_, err := r.cache.DeleteImage(cacheAlias.Target)
			if err != nil {
				return ErrCreate.WithText("while removing the image from the cache").Wrap(err)
			}
		}

		op, err := r.cache.CreateImage(incusApi.ImagesPost{
			Aliases: []incusApi.ImageAlias{{Name: r.incusName}},
		}, &incusClient.ImageCreateArgs{
			MetaFile:   meta,
			MetaName:   "metadata.tar",
			RootfsFile: rootfs,
			RootfsName: "rootfs.tar",
		})
		err = r.client.hookOperation(ctx, ActionEnsure, r, args, op, err)
		if err != nil {
			return ErrCreate.WithText("importing built image on cache").Wrap(err)
		}

		alias, eTag, err := r.cache.GetImageAlias(r.incusName)
		if err != nil {
			return ErrCreate.WithText("fetching alias after build").Wrap(err)
		}

		err = extractAndStoreOCIConfig(ctx, r.cache, alias.Target, r.client.Config().DefaultStoragePool)
		if err != nil {
			return err
		}

		projectCopyArgs := &incusClient.ImageCopyArgs{
			Aliases: []incusApi.ImageAlias{
				{
					Name: r.incusName,
				},
			},
			Mode: "pull",
		}

		// Copy from cache, read oci.* from it.
		img, _, err := r.cache.GetImage(alias.Target)
		if err != nil {
			return ErrCreate.WithText("cannot resolve the image from cache after copy")
		}

		r.size = img.Size
		r.readOCIConfigFromProperties(img.Properties)

		// Build image info for copy
		projectImageInfo := incusApi.Image{
			Fingerprint: alias.Target,
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
		rop, err := r.conn.CopyImage(r.cache, projectImageInfo, projectCopyArgs)

		// Wait for copy to complete
		if err = r.client.hookRemoteOperation(ctx, ActionEnsure, r, args, rop, err); err != nil {
			return ErrCreate.WithText("project image").Wrap(err)
		}

		r.IncusAlias = alias
		r.ETag = eTag
		r.created = true
	}

	r.client.LogInfo("Built image for", "image", r.incusName, "services", r.Config.Services)

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
	alias, _, err := r.conn.GetImageAlias(r.incusName)
	if err != nil || alias == nil {
		r.IncusAlias = nil
		r.ETag = ""

		r.client.resources.Remove(r)

		return r.client.hookAfter(ctx, ActionDelete, r, options, err)
	}

	op, err := r.conn.DeleteImage(alias.Target)

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
