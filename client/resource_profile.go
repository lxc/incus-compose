package client

import (
	"fmt"
	"maps"

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

	// NetworkOnly copies only NIC devices from the source profile.
	NetworkOnly bool
}

// Profile represents an Incus profile resource.
type Profile struct {
	*BaseResource

	incusName string
	created   bool

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

// String is for debugging.
func (r *Profile) String() string {
	return fmt.Sprintf("%v(%v)", r.kind, r.incusName)
}

// IncusName returns the sanitized profile name used in Incus.
func (r *Profile) IncusName() string {
	return r.incusName
}

// IsEnsured returns true if the profile state has been fetched from Incus.
func (r *Profile) IsEnsured() bool {
	return r.IncusProfile != nil
}

// Created returns true if the profile was created during the last Ensure call.
func (r *Profile) Created() bool {
	return r.created
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
		if r.Config.SourceProfile != "" {
			err = r.updateFromSource()
		}
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
		sourceProfile, err := r.sourceProfile()
		if err != nil {
			return err
		}

		profilePut := r.profilePutFromSource(sourceProfile)
		profilePut.Description = fmt.Sprintf(r.client.Config().DescriptionFormat, r.Name())
		postArgs = incusApi.ProfilesPost{
			Name:       r.incusName,
			ProfilePut: profilePut,
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
	r.created = true
	return nil
}

func (r *Profile) sourceProfile() (*incusApi.Profile, error) {
	sourceServer := r.Config.SourceServer
	if sourceServer == nil {
		sourceServer = r.client.GlobalConnection()
	}

	if r.Config.SourceProject != "" {
		projectServer, ok := sourceServer.UseProject(r.Config.SourceProject).(*incusClient.ProtocolIncus)
		if !ok {
			return nil, fmt.Errorf("using source project %s: cannot cast project-scoped client to ProtocolIncus", r.Config.SourceProject)
		}
		sourceServer = projectServer
	}

	sourceProfile, _, err := sourceServer.GetProfile(r.Config.SourceProfile)
	if err != nil {
		return nil, fmt.Errorf("getting source profile %s:%s: %w", r.Config.SourceProject, r.Config.SourceProfile, err)
	}

	return sourceProfile, nil
}

func (r *Profile) profilePutFromSource(sourceProfile *incusApi.Profile) incusApi.ProfilePut {
	if !r.Config.NetworkOnly {
		return incusApi.ProfilePut{
			Config:      sourceProfile.Config,
			Devices:     sourceProfile.Devices,
			Description: fmt.Sprintf(r.client.Config().DescriptionFormat, r.Name()),
		}
	}

	devices := map[string]map[string]string{}
	for name, device := range sourceProfile.Devices {
		if device["type"] == "nic" {
			devices[name] = maps.Clone(device)
		}
	}

	return incusApi.ProfilePut{Devices: devices}
}

func (r *Profile) updateFromSource() error {
	sourceProfile, err := r.sourceProfile()
	if err != nil {
		return err
	}

	profilePut := r.profilePutFromSource(sourceProfile)
	if r.Config.NetworkOnly {
		profilePut.Config = maps.Clone(r.IncusProfile.Config)
		profilePut.Description = r.IncusProfile.Description
		for name, device := range r.IncusProfile.Devices {
			if device["type"] != "nic" {
				profilePut.Devices[name] = maps.Clone(device)
			}
		}
	}

	if err := r.client.incus.UpdateProfile(r.incusName, profilePut, r.ETag); err != nil {
		return fmt.Errorf("updating profile %s from source %s:%s: %w", r.Name(), r.Config.SourceProject, r.Config.SourceProfile, err)
	}

	return r.get()
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
