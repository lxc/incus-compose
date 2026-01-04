package client

import (
	"fmt"
	"maps"
	"strconv"

	incusApi "github.com/lxc/incus/v6/shared/api"
)

// StorageVolumeConfig configures storage volume creation.
type StorageVolumeConfig struct {
	// Pool is the storage pool to create the volume in.
	// Defaults to ClientProject.Config.DefaultStoragePool.
	Pool string

	// Shifted enables UID/GID shifting for the volume.
	Shifted bool

	// UID is the initial UID for shifted volumes.
	UID uint32

	// GID is the initial GID for shifted volumes.
	GID uint32

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

	// Prefix volume name with project name for isolation
	vol.incusName = c.project + "-" + name

	return vol, nil
}

// IncusName returns the prefixed volume name used in Incus.
func (r *StorageVolume) IncusName() string {
	return r.incusName
}

// IsEnsured returns true if the volume has been fetched/created.
func (r *StorageVolume) IsEnsured() bool {
	return r.IncusVolume != nil
}

// Ensure retrieves an existing storage volume or creates a new one if Create option is set.
func (r *StorageVolume) Ensure(opts ...Option) error {
	if r.IsEnsured() {
		return nil
	}

	args := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionEnsure, r, args, nil); err != nil {
			return err
		}
	}

	// Try to get existing
	err := r.get()
	if err == nil {
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

	err = r.create()

	if r.client.hookAfter != nil {
		err = r.client.hookAfter(ActionEnsure, r, args, err)
	}

	return err
}

func (r *StorageVolume) get() error {
	// Try to get existing volume
	volume, eTag, err := r.client.incus.GetStoragePoolVolume(r.Config.Pool, "custom", r.incusName)
	if err != nil {
		return ErrNotFound.WithResource(r).Wrap(err)
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

	// Check shifted is enabled
	if volume.Config["security.shifted"] != "true" {
		return fmt.Errorf("storage volume %q: expected security.shifted=true", r.Name())
	}

	// Check UID/GID match
	expectedUID := strconv.FormatUint(uint64(r.Config.UID), 10)
	expectedGID := strconv.FormatUint(uint64(r.Config.GID), 10)

	if volume.Config["initial.uid"] != expectedUID {
		return fmt.Errorf("storage volume %q: UID mismatch (expected %s, got %s)",
			r.Name(), expectedUID, volume.Config["initial.uid"])
	}

	if volume.Config["initial.gid"] != expectedGID {
		return fmt.Errorf("storage volume %q: GID mismatch (expected %s, got %s)",
			r.Name(), expectedGID, volume.Config["initial.gid"])
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
		config["security.shifted"] = "true"
		config["initial.uid"] = strconv.FormatUint(uint64(r.Config.UID), 10)
		config["initial.gid"] = strconv.FormatUint(uint64(r.Config.GID), 10)
	}

	volReq := incusApi.StorageVolumesPost{
		Name:        r.incusName,
		Type:        "custom",
		ContentType: "filesystem",
		StorageVolumePut: incusApi.StorageVolumePut{
			Config: config,
		},
	}

	if err := r.client.incus.CreateStoragePoolVolume(r.Config.Pool, volReq); err != nil {
		return fmt.Errorf("creating storage volume %q: %w", r.Name(), err)
	}

	volume, eTag, err := r.client.incus.GetStoragePoolVolume(r.Config.Pool, "custom", r.incusName)
	if err != nil {
		return fmt.Errorf("fetching created storage volume %q: %w", r.incusName, err)
	}

	r.IncusVolume = volume
	r.ETag = eTag
	return nil
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
	return nil
}

var (
	_ Resource   = (*StorageVolume)(nil)
	_ EnsureAble = (*StorageVolume)(nil)
	_ DeleteAble = (*StorageVolume)(nil)
)
