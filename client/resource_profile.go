package client

import (
	"fmt"

	incusClient "github.com/lxc/incus/v6/client"
	incusApi "github.com/lxc/incus/v6/shared/api"
)

// ProfileConfig configures profile creation from a source profile.
type ProfileConfig struct {
	// SourceServer is the Incus server to copy the profile from.
	// If nil, uses the global Incus client.
	SourceServer *incusClient.ProtocolIncus

	// SourceProject is the project containing the source profile.
	SourceProject string

	// SourceProfile is the name of the profile to copy from.
	SourceProfile string
}

// Profile represents an Incus profile resource.
type Profile struct {
	*BaseResource

	incusName string

	client *Client
	Config ProfileConfig

	// State - nil means not ensured.
	IncusProfile *incusApi.Profile
	ETag         string
}

// GetConfig returns the configuration.
func (c *ProfileConfig) GetConfig() any {
	return c
}

// newProfile returns an existing Profile or creates a new one.
// If a profile with the same name exists, it is returned.
func newProfile(c *Client, name string, configGetter Config) (*Profile, error) {
	if configGetter == nil {
		return nil, ErrUnknownConfig.WithKindName(KindProfile, name)
	}

	var config *ProfileConfig
	cConfig, ok := configGetter.GetConfig().(*ProfileConfig)
	if !ok {
		return nil, ErrUnknownConfig.WithKindName(KindProfile, name)
	}
	config = cConfig

	profile := &Profile{
		BaseResource: NewBaseResource(KindProfile, name, PriorityProfile),
		incusName:    sanitizeProjectName(name),
		client:       c,
		Config:       *config,
	}

	return profile, nil
}

// IncusName returns the sanitized profile name used in Incus.
func (r *Profile) IncusName() string {
	return r.incusName
}

// IsEnsured returns true if the profile state has been fetched from Incus.
func (r *Profile) IsEnsured() bool {
	return r.IncusProfile != nil
}

// Ensure retrieves an existing resource or creates a new one if args.Create is true.
func (r *Profile) Ensure(opts ...Option) error {
	if r.IsEnsured() {
		return nil
	}

	options := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionEnsure, r, options, nil); err != nil {
			return err
		}
	}

	// Try to get existing
	err := r.get()
	if err == nil {
		if r.client.hookAfter != nil {
			err = r.client.hookAfter(ActionEnsure, r, options, err)
		}

		return err
	}

	if !options.Create {
		if r.client.hookAfter != nil {
			err = r.client.hookAfter(ActionEnsure, r, options, err)
		}

		return err
	}

	err = r.create()

	if r.client.hookAfter != nil {
		err = r.client.hookAfter(ActionEnsure, r, options, err)
	}

	return err
}

func (r *Profile) get() error {
	profile, eTag, err := r.client.incus.GetProfile(r.incusName)
	if err != nil {
		return ErrNotFound.WithResource(r).Wrap(err)
	}

	r.IncusProfile = profile
	r.ETag = eTag

	return err
}

func (r *Profile) create() error {
	var postArgs incusApi.ProfilesPost
	if r.Config.SourceProfile != "" {
		sourceServer := r.Config.SourceServer
		if sourceServer == nil {
			sourceServer = r.client.GlobalConnection()
		}

		sourceProfile, _, err := sourceServer.GetProfile(r.Config.SourceProfile)
		if err != nil {
			return fmt.Errorf("getting source profile %s: %w", r.Config.SourceProfile, err)
		}

		postArgs = incusApi.ProfilesPost{
			Name: r.incusName,
			ProfilePut: incusApi.ProfilePut{
				Config:      sourceProfile.Config,
				Devices:     sourceProfile.Devices,
				Description: fmt.Sprintf(r.client.Config().DescriptionFormat, r.Name()),
			},
		}
	} else {
		postArgs = incusApi.ProfilesPost{
			Name: r.incusName,
			ProfilePut: incusApi.ProfilePut{
				Description: fmt.Sprintf(r.client.Config().DescriptionFormat, r.Name()),
			},
		}
	}

	if err := r.client.incus.CreateProfile(postArgs); err != nil {
		return fmt.Errorf("creating profile %s: %w", r.Name(), err)
	}

	profile, eTag, err := r.client.incus.GetProfile(r.incusName)
	if err != nil {
		return fmt.Errorf("fetching created profile %s: %w", r.Name(), err)
	}

	r.IncusProfile = profile
	r.ETag = eTag
	return nil
}

// HasDevice returns true if the profile has a device with the given name.
func (r *Profile) HasDevice(name string) bool {
	if !r.IsEnsured() {
		return false
	}

	if len(r.IncusProfile.Devices) > 0 {
		for devName := range r.IncusProfile.Devices {
			if devName == name {
				return true
			}
		}
	}

	return false
}

// Delete removes the profile from Incus.
func (r *Profile) Delete(opts ...Option) error {
	if !r.IsEnsured() {
		return nil // Nothing to delete
	}

	options := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionDelete, r, options, nil); err != nil {
			return err
		}
	}

	// Do the actual work
	err := r.client.incus.DeleteProfile(r.incusName)

	if r.client.hookAfter != nil {
		err = r.client.hookAfter(ActionDelete, r, options, err)
	}

	if err != nil {
		return err
	}

	// Clear state
	r.IncusProfile = nil
	r.ETag = ""
	return nil
}

var (
	_ Resource   = (*Profile)(nil)
	_ EnsureAble = (*Profile)(nil)
	_ DeleteAble = (*Profile)(nil)
)
