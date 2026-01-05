package client

import (
	"errors"
	"fmt"
	"strings"

	"github.com/distribution/reference"
	incusClient "github.com/lxc/incus/v6/client"
	incusApi "github.com/lxc/incus/v6/shared/api"
)

// ImageConfig contains the source and cache configuration for an image.
type ImageConfig struct {
	// Source is the image server to copy the image from.
	// For NativeIncus images, this can be nil and will be resolved from Remote.
	Source incusClient.ImageServer

	// Cache is the instance server where images are cached.
	// Defaults to ClientProject.imageCache if not specified.
	Cache incusClient.InstanceServer

	// Remote is the domain part of the image reference.
	Remote string

	// Image is the image reference without the remote prefix.
	Image string

	// NativeIncus indicates this is an Incus native image (e.g., "images:alpine/edge")
	// rather than an OCI image (e.g., "docker.io/library/alpine:latest").
	NativeIncus bool
}

// GetConfig returns the configuration.
func (c *ImageConfig) GetConfig() any {
	return c
}

var _ Config = (*ImageConfig)(nil)

// Image represents an OCI image copied to the Incus image cache.
type Image struct {
	*BaseResource

	client    *Client
	Config    ImageConfig
	incusName string
	created   bool

	// State - nil means not ensured.
	IncusAlias *incusApi.ImageAliasesEntry
	ETag       string
}

// newImage returns an existing Image resource or creates a new one.
// The name should be a Docker-style image reference.
func newImage(c *Client, name string, configGetter Config) (*Image, error) {
	if configGetter == nil {
		return nil, ErrUnknownConfig.WithKindName(KindImage, name)
	}

	var config *ImageConfig
	cConfig, ok := configGetter.GetConfig().(*ImageConfig)
	if !ok {
		return nil, ErrUnknownConfig.WithKindName(KindImage, name)
	}
	config = cConfig

	// Set cache default
	if config.Cache == nil {
		config.Cache = c.imageCache
	}

	var incusName string

	if config.NativeIncus {
		// Parse native Incus format: "images:alpine/edge" or "remote:image/path"
		if config.Remote == "" || config.Image == "" {
			parts := strings.SplitN(name, ":", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid native Incus image format %q, expected remote:image", name)
			}
			config.Remote = parts[0]
			config.Image = parts[1]
		}
		incusName = name
	} else {
		// Parse Docker/OCI reference if Remote or Image is not set
		if config.Remote == "" || config.Image == "" {
			ref, err := reference.ParseDockerRef(name)
			if err != nil {
				return nil, fmt.Errorf("parsing image reference %s: %w", name, err)
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
	}

	img := &Image{
		BaseResource: NewBaseResource(KindImage, name, PriorityImage),
		client:       c,
		incusName:    incusName,
		Config:       *config,
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

// Remote returns the image remote.
func (r *Image) Remote() string {
	return r.Config.Remote
}

// SetSource sets the source image server.
func (r *Image) SetSource(imageServer incusClient.ImageServer) {
	r.Config.Source = imageServer
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
	alias, eTag, err := r.Config.Cache.GetImageAlias(r.incusName)
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
	if r.Config.Source == nil {
		return errors.New("image source not configured")
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
	op, err := r.Config.Cache.CopyImage(r.Config.Source, *imgInfo, copyArgs)

	// Wait for copy to complete
	if err = r.client.hookRemoteOperation(ActionEnsure, r, args, op, err); err != nil {
		return err
	}

	// Fetch the created alias
	alias, eTag, err := r.Config.Cache.GetImageAlias(r.incusName)
	if err != nil {
		return fmt.Errorf("fetching image alias after copy: %w", err)
	}

	r.IncusAlias = alias
	r.ETag = eTag
	r.created = true
	return nil
}

// Delete removes the image from the cache.
func (r *Image) Delete(opts ...Option) error {
	if !r.IsEnsured() {
		return nil
	}

	options := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionDelete, r, options, nil); err != nil {
			return err
		}
	}

	// Delete the image by fingerprint
	op, err := r.Config.Cache.DeleteImage(r.IncusAlias.Target)

	// Do the delete
	err = r.client.hookOperation(ActionDelete, r, options, op, err)
	if r.client.hookAfter != nil {
		if err := r.client.hookAfter(ActionDelete, r, options, err); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// Clear state
	r.IncusAlias = nil
	r.ETag = ""
	return nil
}

var (
	_ Resource   = (*Image)(nil)
	_ EnsureAble = (*Image)(nil)
	_ DeleteAble = (*Image)(nil)
)
