package client

import (
	"fmt"
	"strings"
	"sync"

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

	// target is the project-scoped instance server where image was copied.
	// Delete only removes from target, not from cache.
	target   incusClient.InstanceServer
	targetMu sync.Mutex
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
	config := cConfig

	// Resolve cache: CacheServer > CacheProject > default imageCache
	if config.CacheServer != nil {
		config.cache = config.CacheServer
	} else if config.CacheProject != "" {
		// Ensure cache project exists
		cacheClient, err := c.globalClient.EnsureProject(config.CacheProject, true)
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

// Status returns the image status: "Unknown", "Cached", or "Exists".
func (r *Image) Status() string {
	// Check if image exists in project (either via CopyTo or already there)
	if r.target != nil {
		return "Exists"
	}
	// Check if image exists in project by querying Incus
	if _, _, err := r.client.incus.GetImageAlias(r.incusName); err == nil {
		return "Exists"
	}
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
func (r *Image) Ensure(opts ...Option) error {
	args := NewOptions(opts...)
	if r.IsEnsured() {
		return nil
	}

	options := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionEnsure, r, args, nil); err != nil {
			return err
		}
	}

	// Try to get existing image
	err := r.get()
	if err == nil {
		if r.client.hookAfter != nil {
			err = r.client.hookAfter(ActionEnsure, r, args, err)
		}

		return err
	}

	if !options.Create {
		if r.client.hookAfter != nil {
			err = r.client.hookAfter(ActionEnsure, r, options, err)
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

// CopyTo copies image from cache to target instance server (project).
// The target is remembered for Delete operations.
func (r *Image) CopyTo(target incusClient.InstanceServer) error {
	if !r.IsEnsured() {
		return ErrNotEnsured.WithResource(r)
	}

	r.targetMu.Lock()
	defer r.targetMu.Unlock()

	if r.target != nil {
		return nil // Already copied
	}

	// Check if image alias already exists in target project
	_, _, err := target.GetImageAlias(r.incusName)
	if err == nil {
		// Already exists in target, just remember it
		r.target = target
		return nil
	}

	// Get image info from cache
	imgInfo, _, err := r.Config.cache.GetImage(r.IncusAlias.Target)
	if err != nil {
		return ErrNotFound.WithText("getting image from cache").Wrap(err)
	}

	// Copy from cache to target project
	copyArgs := &incusClient.ImageCopyArgs{
		Aliases: []incusApi.ImageAlias{{Name: r.incusName}},
	}

	op, err := target.CopyImage(r.Config.cache, *imgInfo, copyArgs)
	if err != nil {
		return ErrCreate.WithText("copying image to project").Wrap(err)
	}

	err = op.Wait()
	if err != nil {
		return ErrOperation.WithText("waiting for image copy").Wrap(err)
	}

	r.target = target
	return nil
}

// Delete removes the image from the project (target), not from cache.
func (r *Image) Delete(opts ...Option) error {
	if r.target == nil {
		return nil // Nothing copied, nothing to delete
	}

	options := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionDelete, r, options, nil); err != nil {
			return err
		}
	}

	// Delete the image from target (project), not from cache
	op, err := r.target.DeleteImage(r.IncusAlias.Target)

	// Do the delete
	err = r.client.hookOperation(r.client.globalClient.Ctx, ActionDelete, r, options, op, err)
	if r.client.hookAfter != nil {
		if err := r.client.hookAfter(ActionDelete, r, options, err); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// Clear target state, but keep IncusAlias (image still in cache)
	r.target = nil
	return nil
}

var (
	_ Resource   = (*Image)(nil)
	_ EnsureAble = (*Image)(nil)
	_ DeleteAble = (*Image)(nil)
)
