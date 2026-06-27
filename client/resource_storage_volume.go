package client

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strconv"

	incusClient "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"
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

	// Extensions contains additional volume configuration options.
	Extensions map[string]string
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

	// conn is this resource's own event-isolated Incus connection, set in
	// Ensure() (which always runs before any other action) so concurrent
	// workers never share a *ProtocolIncus. See Client.Connection.
	conn *incusClient.ProtocolIncus

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

	shifted, ok := config.Extensions["security.shifted"]
	if ok && shifted != "true" {
		config.Shifted = false
	}

	vol := &StorageVolume{
		BaseResource: NewBaseResource(KindStorageVolume, name, PriorityVolume),
		client:       c,
		Config:       *config,
	}

	vol.incusName = "vol-" + SanitizeIncusName(name, MaxIncusNameLen-4)
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
func (r *StorageVolume) Ensure(ctx context.Context, opts ...Option) error {
	options := NewOptions(opts...)

	if err := r.client.hookBefore(ctx, ActionEnsure, r, options, nil); err != nil {
		return err
	}

	conn, err := r.client.Connection()
	if err != nil {
		return r.client.hookAfter(ctx, ActionEnsure, r, options, err)
	}
	r.conn = conn

	err = r.get()
	if err != nil {
		if options.Create && errors.Is(err, ErrNotFound) {
			err = r.create()
		}
	}

	err = r.client.hookAfter(ctx, ActionEnsure, r, options, err)

	return err
}

func (r *StorageVolume) get() error {
	// r.client.LogDebug("getting volume", "pool", r.Config.Pool, "volume", r.incusName)

	// Try to get existing volume
	volume, eTag, err := r.conn.GetStoragePoolVolume(r.Config.Pool, "custom", r.incusName)
	if err != nil {
		r.IncusVolume = nil
		r.ETag = ""
		return ErrNotFound.Wrap(err)
	}

	r.IncusVolume = volume
	r.ETag = eTag
	return nil
}

// Start validates the storage volume.
func (r *StorageVolume) Start(_ context.Context, _ ...Option) error {
	if !r.IsEnsured() || !r.Config.Shifted {
		return nil
	}

	// if r.Config.ImageResource == nil {
	// 	return ErrVolumeMismatch.WithText("no image resource given")
	// }

	var errs error

	// Check shifted is enabled
	if r.IncusVolume.Config["security.shifted"] != "true" {
		errs = errors.Join(errors.New("expected security.shifted=true"))
	}

	expectedUID := strconv.FormatUint(r.Config.UID, 10)
	expectedGID := strconv.FormatUint(r.Config.GID, 10)
	if r.Config.ImageResource != nil {
		img, ok := r.Config.ImageResource.(*Image)
		if !ok {
			errs = errors.Join(errs, ErrUnknownResource.WithResource(r.Config.ImageResource))
			return errs
		}

		if !img.IsEnsured() {
			errs = errors.Join(errs, ErrNotEnsured.WithResource(img))
			return errs
		}

		// Check UID/GID match
		expectedUID = strconv.FormatUint(img.UID, 10)
		expectedGID = strconv.FormatUint(img.GID, 10)
	}

	if r.IncusVolume.Config["initial.uid"] != expectedUID {
		errs = errors.Join(errs, fmt.Errorf("UID mismatch, expected %s got %s", expectedUID, r.IncusVolume.Config["initial.uid"]))
	}

	if r.IncusVolume.Config["initial.gid"] != expectedGID {
		errs = errors.Join(errs, fmt.Errorf("GID mismatch, expected %s got %s", expectedGID, r.IncusVolume.Config["initial.gid"]))
	}

	if errs != nil {
		return ErrVolumeMismatch.Wrap(errs)
	}

	return nil
}

func (r *StorageVolume) create() error {
	config := map[string]string{}

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

	// Set users config afterwards this allows them to override the above.
	if r.Config.Extensions != nil {
		maps.Copy(config, r.Config.Extensions)
	}

	// r.client.LogDebug("creating volume", "pool", r.Config.Pool, "volume", r.incusName)

	if err := r.conn.CreateStoragePoolVolume(r.Config.Pool, volReq); err != nil {
		return ErrCreate.Wrap(err)
	}

	volume, eTag, err := r.conn.GetStoragePoolVolume(r.Config.Pool, "custom", r.incusName)
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
			return r.conn.CreateStorageVolumeFile(r.Config.Pool, "custom", r.incusName, rel, args)
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		args.Type = "file"
		args.Content = f
		return r.conn.CreateStorageVolumeFile(r.Config.Pool, "custom", r.incusName, rel, args)
	})
}

// Delete removes the storage volume from Incus.
func (r *StorageVolume) Delete(ctx context.Context, opts ...Option) error {
	if !r.IsEnsured() {
		r.IncusVolume = nil
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
		r.IncusVolume = nil
		r.ETag = ""

		r.client.resources.Remove(r)
		return err
	}

	err := r.conn.DeleteStoragePoolVolume(r.Config.Pool, "custom", r.incusName)
	err = r.client.hookAfter(ctx, ActionDelete, r, options, err)
	if err != nil {
		r.IncusVolume = nil
		r.ETag = ""

		r.client.resources.Remove(r)
		return err
	}

	r.conn = nil
	r.IncusVolume = nil
	r.ETag = ""

	r.client.resources.Remove(r)
	return nil
}

var (
	_ Resource   = (*StorageVolume)(nil)
	_ EnsureAble = (*StorageVolume)(nil)
	_ StartAble  = (*StorageVolume)(nil)
	_ DeleteAble = (*StorageVolume)(nil)
)

// extractUIDGID extracts UID and GID from a container instance.
func extractUIDGID(instance *incusApi.Instance) (uint64, uint64, error) {
	if incusApi.InstanceType(instance.Type) != incusApi.InstanceTypeContainer {
		return 0, 0, nil
	}

	// oci.uid/gid only exist for OCI images, not native Incus images
	uidStr, hasUID := instance.Config["oci.uid"]
	gidStr, hasGID := instance.Config["oci.gid"]
	if !hasUID || !hasGID {
		return 0, 0, nil
	}

	uid, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		return 0, 0, err
	}

	gid, err := strconv.ParseUint(gidStr, 10, 32)
	if err != nil {
		return 0, 0, err
	}

	return uid, gid, nil
}
