package client

import (
	"fmt"
	"maps"
	"strconv"
	"strings"

	"github.com/distribution/reference"
	incusClient "github.com/lxc/incus/v6/client"
	incusApi "github.com/lxc/incus/v6/shared/api"
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

// NativeIncus returns true if this is a native Incus image.
func (r *Image) NativeIncus() bool {
	return r.nativeIncus
}

// Ensure retrieves an existing image from cache or copies it if Create option is set.
// With the Pull option, a cached image is refreshed from its source registry.
func (r *Image) Ensure(opts ...Option) error {
	args := NewOptions(opts...)
	if r.IsEnsured() {
		return nil
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

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionEnsure, r, args, nil); err != nil {
			return err
		}
	}

	// Try to get existing image
	err := r.get()
	if err == nil {
		if args.Pull {
			err = r.refresh(args)
		}

		if r.client.hookAfter != nil {
			err = r.client.hookAfter(ActionEnsure, r, args, err)
		}

		return err
	}

	if !args.Create {
		if r.client.hookAfter != nil {
			err = r.client.hookAfter(ActionEnsure, r, args, err)
		}

		return err
	}

	err = r.create(args)

	if r.client.hookAfter != nil {
		err = r.client.hookAfter(ActionEnsure, r, args, err)
	}

	return err
}

func (r *Image) get() error {
	// Check if image alias exists in cache
	alias, eTag, err := r.client.incus.GetImageAlias(r.incusName)
	if err != nil {
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
func (r *Image) refresh(args Options) error {
	// No source || no cache || no alias, no download.
	if r.source == nil || r.cache == nil || r.IncusAlias == nil {
		return nil
	}

	// Check if the remote image has the same fingerprint
	remoteAlias, _, err := r.source.GetImageAlias(r.image)
	if err == nil && remoteAlias != nil && remoteAlias.Target == r.IncusAlias.Target {
		return nil
	}

	op, err := r.client.incus.DeleteImage(r.IncusAlias.Target)
	if err = r.client.hookOperation(r.client.globalClient.Ctx, ActionEnsure, r, args, op, err); err != nil {
		r.client.LogDebug("deleting stale cached image for refresh", "error", err)
		return nil
	}

	r.IncusAlias = nil
	r.ETag = ""
	return r.create(args)
}

func (r *Image) create(args Options) error {
	if r.source == nil {
		return ErrImageSource.WithText("not configured")
	}

	copyArgs := &incusClient.ImageCopyArgs{
		Aliases:    []incusApi.ImageAlias{{Name: r.incusName}},
		AutoUpdate: true,
		Public:     false,
		Mode:       "pull",
	}

	// Check if the cache has the alias
	_, _, err := r.cache.GetImageAlias(r.incusName)
	if err != nil {
		// Build image info for copy
		sourceImgInfo := &incusApi.Image{
			Fingerprint: r.image,
			ImagePut: incusApi.ImagePut{
				Public: true,
			},
		}

		// Copy from source to cache
		op, err := r.cache.CopyImage(r.source, *sourceImgInfo, copyArgs)

		// Wait for copy to complete
		if err = r.client.hookRemoteOperation(r.client.globalClient.Ctx, ActionEnsure, r, args, op, err); err != nil {
			return ErrCreate.WithText("caching image").Wrap(err)
		}
	}

	_, _, err = r.client.incus.GetImageAlias(r.incusName)
	if err != nil {
		cacheAlias, _, err := r.cache.GetImageAlias(r.incusName)
		if err != nil {
			return ErrCreate.WithText("cache alias after copy").Wrap(err)
		}

		// Build image info for copy
		cacheImgInfo := &incusApi.Image{
			Fingerprint: cacheAlias.Target,
		}

		// Copy from cache to project
		op, err := r.client.incus.CopyImage(r.cache, *cacheImgInfo, copyArgs)

		// Wait for copy to complete
		if err = r.client.hookRemoteOperation(r.client.globalClient.Ctx, ActionEnsure, r, args, op, err); err != nil {
			return ErrCreate.WithText("project image").Wrap(err)
		}
	}

	// Fetch the created alias
	alias, eTag, err := r.client.incus.GetImageAlias(r.incusName)
	if err != nil {
		return ErrCreate.WithText("fetching image alias after copy").Wrap(err)
	}

	// r.client.LogDebug("create setting the alias")
	r.IncusAlias = alias
	r.ETag = eTag
	r.created = true

	if err := r.extractAndStoreOCIConfig(); err != nil {
		r.client.LogDebug("extracting OCI config from image", "image", r.incusName, "error", err)
	}

	return nil
}

// extractAndStoreOCIConfig creates a temporary stopped container from this image,
// reads oci.uid/oci.gid/oci.entrypoint/oci.cwd from its config, stores them as
// image properties, then deletes the container.
// Non-fatal: callers should log and continue on error.
func (r *Image) extractAndStoreOCIConfig() error {
	tempName := sanitizeInstanceName("ic-uid-" + r.IncusAlias.Target)

	req := incusApi.InstancesPost{
		Name: tempName,
		Type: incusApi.InstanceTypeContainer,
		Source: incusApi.InstanceSource{
			Type:        "image",
			Fingerprint: r.IncusAlias.Target,
		},
		InstancePut: incusApi.InstancePut{
			Devices: map[string]map[string]string{
				"root": {
					"type": "disk",
					"path": "/",
					"pool": r.client.Config().DefaultStoragePool,
				},
			},
		},
	}

	createOp, err := r.client.incus.CreateInstance(req)
	if err != nil {
		return fmt.Errorf("creating temp instance: %w", err)
	}
	if err = createOp.Wait(); err != nil {
		return fmt.Errorf("waiting for temp instance: %w", err)
	}

	defer func() {
		if deleteOp, err := r.client.incus.DeleteInstance(tempName); err == nil {
			_ = deleteOp.Wait()
		}
	}()

	instance, _, err := r.client.incus.GetInstance(tempName)
	if err != nil {
		return fmt.Errorf("getting temp instance: %w", err)
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

	img, eTag, err := r.cache.GetImage(r.IncusAlias.Target)
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

	if err := r.cache.UpdateImage(r.IncusAlias.Target, incusApi.ImagePut{
		AutoUpdate: img.AutoUpdate,
		Properties: props,
		Public:     img.Public,
		ExpiresAt:  img.ExpiresAt,
		Profiles:   img.Profiles,
	}, eTag); err != nil {
		return fmt.Errorf("storing OCI config as image properties: %w", err)
	}

	r.UID = uid
	r.GID = gid
	r.Entrypoint = entrypoint
	r.Cwd = cwd
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

// Delete removes the per-project copy of the image from the active project.
//
// Projects are created with features.images=true, so creating an instance
// copies the image into the active project. Those copies are removed here on
// down; without it they accumulate and go stale relative to the auto-updated
// cache (see issue #29). The cache lives in a separate project and is left
// untouched, so cached images persist across down/up cycles.
//
// Delete is idempotent: a missing per-project copy is not an error.
func (r *Image) Delete(opts ...Option) error {
	options := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionDelete, r, options, nil); err != nil {
			return err
		}
	}

	// Resolve the per-project copy in the active project (not the cache). A
	// missing alias means nothing was copied here, so there is nothing to do.
	alias, _, err := r.client.incus.GetImageAlias(r.incusName)
	if err != nil || alias == nil {
		r.client.resources.Remove(r)
		if r.client.hookAfter != nil {
			return r.client.hookAfter(ActionDelete, r, options, nil)
		}
		return nil
	}

	op, err := r.client.incus.DeleteImage(alias.Target)
	err = r.client.hookOperation(r.client.globalClient.Ctx, ActionDelete, r, options, op, err)

	if err == nil {
		r.client.resources.Remove(r)
	}

	if r.client.hookAfter != nil {
		return r.client.hookAfter(ActionDelete, r, options, err)
	}

	return err
}

var (
	_ Resource   = (*Image)(nil)
	_ EnsureAble = (*Image)(nil)
	_ DeleteAble = (*Image)(nil)
)
