package client

import (
	"fmt"
	"strings"

	"github.com/distribution/reference"
	incusClient "github.com/lxc/incus/v6/client"
	incusApi "github.com/lxc/incus/v6/shared/api"
	"github.com/lxc/incus/v6/shared/cliconfig"
)

// ImageConfig contains the source and cache configuration for an image.
type ImageConfig struct {
	// CliConfig is the Incus CLI config used to resolve image servers.
	// If set, the source is resolved automatically from the remote name.
	CliConfig *cliconfig.Config

	// CacheServer is an image server to use as cache (for library users).
	// Takes precedence over CacheProject.
	CacheServer incusClient.InstanceServer

	// CacheProject is the project name to use as cache (for CLI users).
	// The project will be created if it doesn't exist.
	// Ignored if CacheServer is set.
	CacheProject string

	// cache is the resolved instance server for caching (internal use).
	cache incusClient.InstanceServer

	// Remote is the domain part of the image reference (set automatically if not provided).
	Remote string

	// Image is the image reference without the remote prefix (set automatically if not provided).
	Image string
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

	// source is the resolved image server for this image.
	source incusClient.ImageServer

	// nativeIncus indicates this is a native Incus image (protocol "incus")
	// rather than an OCI image (protocol "oci").
	nativeIncus bool

	// State - nil means not ensured.
	IncusAlias *incusApi.ImageAliasesEntry
	ETag       string
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

	// Resolve cache: CacheServer > CacheProject > default imageCache
	if config.CacheServer != nil {
		config.cache = config.CacheServer
	} else if config.CacheProject != "" {
		// Ensure cache project exists
		cacheClient, err := c.globalClient.EnsureProject(config.CacheProject, EnsureProjectWithCreate())
		if err != nil {
			return nil, fmt.Errorf("ensuring cache project %s: %w", config.CacheProject, err)
		}
		config.cache = cacheClient.incus
	} else {
		config.cache = c.imageCache
	}

	// Try to parse as native Incus format first: "remote:image/path"
	// This takes precedence if CliConfig is provided and remote exists
	var source incusClient.ImageServer
	var nativeIncus bool
	var incusName string

	if config.CliConfig != nil && strings.Contains(name, ":") {
		parts := strings.SplitN(name, ":", 2)
		remoteName := parts[0]

		// Check if this remote exists in CLI config
		if _, ok := config.CliConfig.Remotes[remoteName]; ok {
			is, err := config.CliConfig.GetImageServer(remoteName)
			if err != nil {
				return nil, ErrImageSource.WithText("getting image server for " + remoteName).Wrap(err)
			}

			source = is
			config.Remote = remoteName
			config.Image = parts[1]

			// Detect protocol from connection info
			connInfo, err := is.GetConnectionInfo()
			if err == nil && connInfo.Protocol == "incus" {
				nativeIncus = true
			}

			incusName = name
		}
	}

	// If not resolved as native, try Docker/OCI reference
	if source == nil {
		if config.Remote == "" || config.Image == "" {
			ref, err := reference.ParseDockerRef(name)
			if err != nil {
				return nil, ErrInvalidFormat.WithKindName(KindImage, name).Wrap(err)
			}

			originalDomain := reference.Domain(ref)
			config.Remote = originalDomain
			if config.Remote == "localhost" {
				// Handle podman style "localhost" images.
				config.Remote = "local"
			}

			image, _ := strings.CutPrefix(ref.String(), originalDomain+"/")
			config.Image = image
		}

		// Build incusName from parsed/converted values
		incusName = config.Remote + "/" + config.Image

		// Resolve source from CLI config if available
		if config.CliConfig != nil {
			is, err := config.CliConfig.GetImageServer(config.Remote)
			if err != nil {
				return nil, ErrImageSource.WithText("getting image server for " + config.Remote).Wrap(err)
			}
			source = is
		}
	}

	img := &Image{
		BaseResource: NewBaseResource(KindImage, name, PriorityImage),
		client:       c,
		incusName:    incusName,
		Config:       *config,
		source:       source,
		nativeIncus:  nativeIncus,
	}

	return img, nil
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
	return r.Config.Remote
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
	alias, eTag, err := r.Config.cache.GetImageAlias(r.incusName)
	if err != nil {
		return ErrNotFound.Wrap(err)
	}

	if alias == nil {
		return ErrNilPointer
	}

	r.IncusAlias = alias
	r.ETag = eTag
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
	if r.source == nil || r.Config.cache == nil || r.IncusAlias == nil {
		return nil
	}

	// Check if the remote image has the same fingerprint
	remoteAlias, _, err := r.source.GetImageAlias(r.Config.Image)
	if err == nil && remoteAlias != nil && remoteAlias.Target == r.IncusAlias.Target {
		return nil
	}

	op, err := r.Config.cache.DeleteImage(r.IncusAlias.Target)
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

	// Build image info for copy
	imgInfo := &incusApi.Image{
		Fingerprint: r.Config.Image,
	}
	imgInfo.Public = true // Needed to copy from public image servers.

	copyArgs := &incusClient.ImageCopyArgs{
		Aliases:    []incusApi.ImageAlias{{Name: r.incusName}},
		AutoUpdate: true,
		Public:     false,
		Mode:       "pull",
	}

	// Start the copy operation
	op, err := r.Config.cache.CopyImage(r.source, *imgInfo, copyArgs)

	// Wait for copy to complete
	if err = r.client.hookRemoteOperation(r.client.globalClient.Ctx, ActionEnsure, r, args, op, err); err != nil {
		return err
	}

	// Fetch the created alias
	alias, eTag, err := r.Config.cache.GetImageAlias(r.incusName)
	if err != nil {
		return ErrCreate.WithText("fetching image alias after copy").Wrap(err)
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
