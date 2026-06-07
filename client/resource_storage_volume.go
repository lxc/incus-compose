package client

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strconv"

	incusClient "github.com/lxc/incus/v6/client"
	incusApi "github.com/lxc/incus/v6/shared/api"
)

// StorageVolumeConfig configures storage volume creation.
type StorageVolumeConfig struct {
	// Pool is the storage pool to create the volume in.
	// Defaults to ClientProject.Config.DefaultStoragePool.
	Pool string

	// Shifted enables UID/GID shifting for the volume.
	Shifted bool

	// UID/GID for shifting ImageResource will overwrite this if given.
	UID uint64
	GID uint64

	// ImageResource to take UID/GID from for shifting, only
	// needed if shifting is true.
	ImageResource Resource

	// HostPath, when set, seeds the volume with the local directory contents on first creation.
	HostPath string

	// ExtraConfig contains additional volume configuration options.
	ExtraConfig map[string]string
}

// GetConfig returns the configuration.
func (c *StorageVolumeConfig) GetConfig() any {
	return c
}

// StorageVolume represents a custom storage volume with optional UID/GID shifting.
// Storage volumes provide persistent storage that can be attached to instances.
type StorageVolume struct {
	*BaseResource

	client    *Client
	incusName string
	created   bool
	Config    StorageVolumeConfig

	// State - nil means not ensured.
	IncusVolume *incusApi.StorageVolume
	ETag        string
}

// newStorageVolume returns an existing StorageVolume resource or creates a new one.
// The volume name is automatically prefixed with the project name for isolation.
func newStorageVolume(c *Client, name string, configGetter Config) (*StorageVolume, error) {
	if configGetter == nil {
		return nil, ErrUnknownConfig.WithKindName(KindStorageVolume, name)
	}

	var config *StorageVolumeConfig
	cConfig, ok := configGetter.GetConfig().(*StorageVolumeConfig)
	if !ok {
		return nil, ErrUnknownConfig.WithKindName(KindStorageVolume, name)
	}
	config = cConfig

	// Set defaults
	if config.Pool == "" {
		config.Pool = c.Config().DefaultStoragePool
	}

	vol := &StorageVolume{
		BaseResource: NewBaseResource(KindStorageVolume, name, PriorityVolume),
		client:       c,
		Config:       *config,
	}

	vol.incusName = SanitizeIncusName(name, -1)
	return vol, nil
}

// String is for debugging.
func (r *StorageVolume) String() string {
	return fmt.Sprintf("%v(%v)", r.kind, r.incusName)
}

// IncusName returns the prefixed volume name used in Incus.
func (r *StorageVolume) IncusName() string {
	return r.incusName
}

// IsEnsured returns true if the volume has been fetched/created.
func (r *StorageVolume) IsEnsured() bool {
	return r.IncusVolume != nil
}

// Created returns true if the volume was created during the last Ensure call.
func (r *StorageVolume) Created() bool {
	return r.created
}

// Ensure retrieves an existing storage volume or creates a new one if Create option is set.
func (r *StorageVolume) Ensure(opts ...Option) error {
	if r.IncusVolume != nil {
		return nil
	}

	args := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionEnsure, r, args, nil); err != nil {
			return err
		}
	}

	err := r.get()
	if err != nil {
		if args.Create {
			err = r.create()
		}
	}

	if r.client.hookAfter != nil {
		err = r.client.hookAfter(ActionEnsure, r, args, err)
	}

	return err
}

func (r *StorageVolume) get() error {
	// r.client.LogDebug("getting volume", "pool", r.Config.Pool, "volume", r.incusName)

	// Try to get existing volume
	volume, eTag, err := r.client.incus.GetStoragePoolVolume(r.Config.Pool, "custom", r.incusName)
	if err != nil {
		return ErrNotFound.Wrap(err)
	}

	// Validate configuration matches
	if err := r.validate(volume); err != nil {
		return err
	}

	r.IncusVolume = volume
	r.ETag = eTag
	return nil
}

func (r *StorageVolume) validate(volume *incusApi.StorageVolume) error {
	if !r.Config.Shifted {
		return nil
	}

	if r.Config.ImageResource == nil {
		return ErrVolumeMismatch.WithText("no image resource given")
	}

	img, ok := r.Config.ImageResource.(*Image)
	if !ok {
		return ErrUnknownResource.WithResource(r.Config.ImageResource)
	}

	// Check shifted is enabled
	if volume.Config["security.shifted"] != "true" {
		return ErrVolumeMismatch.WithText("expected security.shifted=true")
	}

	// Check UID/GID match
	expectedUID := strconv.FormatUint(uint64(img.UID), 10)
	expectedGID := strconv.FormatUint(uint64(img.GID), 10)

	if volume.Config["initial.uid"] != expectedUID {
		return ErrVolumeMismatch.WithText("UID mismatch, expected " + expectedUID + " got " + volume.Config["initial.uid"])
	}

	if volume.Config["initial.gid"] != expectedGID {
		return ErrVolumeMismatch.WithText("GID mismatch, expected " + expectedGID + " got " + volume.Config["initial.gid"])
	}

	return nil
}

func (r *StorageVolume) create() error {
	var config map[string]string

	if r.Config.ExtraConfig != nil {
		config = maps.Clone(r.Config.ExtraConfig)
	} else {
		config = map[string]string{}
	}

	if r.Config.Shifted {
		if r.Config.ImageResource != nil {
			img, ok := r.Config.ImageResource.(*Image)
			if !ok {
				return ErrUnknownResource.WithResource(r.Config.ImageResource)
			}

			r.Config.UID = img.UID
			r.Config.GID = img.GID
		}

		config["security.shifted"] = "true"
		config["initial.uid"] = strconv.FormatUint(r.Config.UID, 10)
		config["initial.gid"] = strconv.FormatUint(r.Config.GID, 10)
	}

	volReq := incusApi.StorageVolumesPost{
		Name:        r.incusName,
		Type:        "custom",
		ContentType: "filesystem",
		StorageVolumePut: incusApi.StorageVolumePut{
			Description: fmt.Sprintf(r.client.Config().DescriptionFormat, r.Name()),
			Config:      config,
		},
	}

	if err := r.client.incus.CreateStoragePoolVolume(r.Config.Pool, volReq); err != nil {
		return ErrCreate.Wrap(err)
	}

	volume, eTag, err := r.client.incus.GetStoragePoolVolume(r.Config.Pool, "custom", r.incusName)
	if err != nil {
		return ErrCreate.WithText("fetching created volume").Wrap(err)
	}

	r.IncusVolume = volume
	r.ETag = eTag
	r.created = true

	if r.Config.HostPath != "" {
		if err := r.pushDirectoryContent(); err != nil {
			return ErrCreate.WithText("seeding volume from " + r.Config.HostPath).Wrap(err)
		}
	}

	return nil
}

// pushDirectoryContent walks HostPath and copies every file and directory into
// the volume via CreateStorageVolumeFile. Only called on first creation.
func (r *StorageVolume) pushDirectoryContent() error {
	var uid, gid int64

	if r.Config.ImageResource != nil {
		if img, ok := r.Config.ImageResource.(*Image); ok {
			uid, gid = int64(img.UID), int64(img.GID)
		}
	}

	return filepath.WalkDir(r.Config.HostPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(r.Config.HostPath, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		perm := 0o644
		if d.IsDir() {
			perm = 0o755
		}

		args := incusClient.InstanceFileArgs{
			Mode: perm,
			UID:  uid,
			GID:  gid,
		}

		if d.IsDir() {
			args.Type = "directory"
			return r.client.incus.CreateStorageVolumeFile(r.Config.Pool, "custom", r.incusName, rel, args)
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		args.Type = "file"
		args.Content = f
		return r.client.incus.CreateStorageVolumeFile(r.Config.Pool, "custom", r.incusName, rel, args)
	})
}

// Delete removes the storage volume from Incus.
func (r *StorageVolume) Delete(opts ...Option) error {
	if !r.IsEnsured() {
		return nil
	}

	options := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionDelete, r, options, nil); err != nil {
			return err
		}
	}

	err := r.client.incus.DeleteStoragePoolVolume(r.Config.Pool, "custom", r.incusName)

	if r.client.hookAfter != nil {
		err = r.client.hookAfter(ActionDelete, r, options, err)
	}

	if err != nil {
		return err
	}

	// Clear state
	r.IncusVolume = nil
	r.ETag = ""
	r.client.resources.Remove(r)
	return nil
}

var (
	_ Resource   = (*StorageVolume)(nil)
	_ EnsureAble = (*StorageVolume)(nil)
	_ DeleteAble = (*StorageVolume)(nil)
)
