package incuscompose

import (
	"fmt"
	"log/slog"
	"slices"

	incusClient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
)

// EnsureProjectOptions holds configuration for project creation/validation.
type EnsureProjectOptions struct {
	Options

	// Whether to create the project if it doesn't exist
	// If false, will error if project doesn't exist
	Create bool

	// Profile to create / update.
	Profile string

	// Source project to copy profile from (defaults to "default")
	SourceProject string

	// Profile to copy from (defaults to "default")
	SourceProfile string
}

func NewEnsureProjectOptions(opts []EnsureProjectOption) EnsureProjectOptions {
	options := EnsureProjectOptions{
		Profile:       "default",
		SourceProject: "default",
		SourceProfile: "default",
	}

	for _, o := range opts {
		o(&options)
	}

	return options
}

func (o *EnsureProjectOptions) options() *Options {
	return &o.Options
}

var _ (OptionsType) = (*EnsureProjectOptions)(nil)

type EnsureProjectOption func(*EnsureProjectOptions)

func EnsureProjectCreate() EnsureProjectOption {
	return func(o *EnsureProjectOptions) {
		o.Create = true
	}
}

func EnsureProjectProfile(n string) EnsureProjectOption {
	return func(o *EnsureProjectOptions) {
		o.Profile = n
	}
}

func EnsureProjecSourceProject(n string) EnsureProjectOption {
	return func(o *EnsureProjectOptions) {
		o.SourceProject = n
	}
}

func EnsureProjectSourceProfile(n string) EnsureProjectOption {
	return func(o *EnsureProjectOptions) {
		o.SourceProfile = n
	}
}

// EnsureProject ensures a project exists with a properly configured default profile.
func EnsureProject(logger *slog.Logger, server *incusClient.ProtocolIncus, composeProject, name string, opts ...EnsureProjectOption) error {
	options := NewEnsureProjectOptions(opts)

	// Check if project exists
	projectNames, err := server.GetProjectNames()
	if err != nil {
		logger.Debug("Failed to fetch project names", slog.Any("error", err))
		return fmt.Errorf("fetching project names: %w", err)
	}
	if slices.Contains(projectNames, name) {
		return ensureProjectProfile(server, composeProject, options.SourceProject, options.SourceProfile, name, options.Profile)
	}

	if !options.Create {
		return fmt.Errorf("project %q does not exist", name)
	}

	// Create project
	projectArgs := api.ProjectsPost{
		Name: name,
		ProjectPut: api.ProjectPut{
			Description: fmt.Sprintf("incus-compose: %s", composeProject),
			Config:      api.ConfigMap{"features.profiles": "true"},
		},
	}

	err = server.CreateProject(projectArgs)
	if err != nil {
		return fmt.Errorf("creating project %q: %w", name, err)
	}

	// Check and populate default profile if needed
	err = ensureProjectProfile(server, composeProject, options.SourceProject, options.SourceProfile, name, options.Profile)
	if err != nil {
	}

	return nil
}

// ensureProjectProfile either creates the targetProfile or makes sures its properly configured.
func ensureProjectProfile(client *incusClient.ProtocolIncus, composeProject, sourceProject, sourceProfile, targetProject, targetProfile string) error {
	destServer := client.UseProject(targetProject)

	// Get the target default profile (in the project)
	apiTargetProfile, etag, err := destServer.GetProfile(targetProfile)
	if err != nil {
		err = destServer.CreateProfile(api.ProfilesPost{Name: targetProfile, ProfilePut: api.ProfilePut{Description: fmt.Sprintf("incus-compose: %s", composeProject)}})
		if err != nil {
			return fmt.Errorf("configuring default profile for project %q: %w", targetProject, err)
		}
		apiTargetProfile, etag, err = destServer.GetProfile(targetProfile)
		if err != nil {
			return fmt.Errorf("configuring default profile for project %q: %w", targetProject, err)
		}
	}

	if len(apiTargetProfile.Devices) > 0 {
		// Profile already configured, nothing to do
		return nil
	}

	sourceServer := client.UseProject(sourceProject)
	apiSourceProfile, _, err := sourceServer.GetProfile(sourceProfile)
	if err != nil {
		return fmt.Errorf("getting source profile %q from project %q: %w", sourceProfile, sourceProject, err)
	}

	apiTargetProfile.Devices = apiSourceProfile.Devices
	err = destServer.UpdateProfile(targetProfile, apiTargetProfile.Writable(), etag)
	if err != nil {
		return fmt.Errorf("updating default profile: %w", err)
	}

	return nil
}
