package client

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"

	incusApi "github.com/lxc/incus/v6/shared/api"
)

// Network interface name limits.
const (
	// maxInterfaceNameLen is the maximum safe length for Linux interface names.
	// While IFNAMSIZ allows 15 chars, some dhclient versions have bugs with names > 13.
	maxInterfaceNameLen = 13

	// networkNameHashLen is the number of base32 characters for the hash portion.
	networkNameHashLen = 10
)

// NetworkConfig configures network creation.
type NetworkConfig struct {
	// Type is the network type (default: "bridge").
	Type string
}

// Network represents an Incus bridge network.
type Network struct {
	*BaseResource

	client    *Client
	incusName string
	Config    NetworkConfig

	// State - nil means not ensured.
	IncusNetwork *incusApi.Network
	ETag         string
}

// GetConfig returns the configuration.
func (c *NetworkConfig) GetConfig() any {
	return c
}

// newNetwork returns an existing Network or creates a new one.
func newNetwork(c *Client, name string, configGetter Config) (*Network, error) {
	if configGetter == nil {
		return nil, ErrUnknownConfig.WithKindName(KindNetwork, name)
	}

	var config *NetworkConfig
	cConfig, ok := configGetter.GetConfig().(*NetworkConfig)
	if !ok {
		return nil, ErrUnknownConfig.WithKindName(KindNetwork, name)
	}
	config = cConfig

	if config.Type == "" {
		config.Type = "bridge"
	}

	network := &Network{
		BaseResource: NewBaseResource(KindNetwork, name, PriorityNetwork),
		client:       c,
		Config:       *config,
	}

	// Generate sanitized Incus name
	network.incusName = sanitizeNetworkName(c.project, c.Config().NetworkPrefix, name)

	return network, nil
}

// IncusName returns the sanitized network name used in Incus.
func (r *Network) IncusName() string {
	return r.incusName
}

// IsEnsured returns true if the network state has been fetched from Incus.
func (r *Network) IsEnsured() bool {
	return r.IncusNetwork != nil
}

// Ensure retrieves an existing network or creates a new one if args.Create is true.
func (r *Network) Ensure(opts ...Option) error {
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

func (r *Network) get() error {
	network, eTag, err := r.client.incus.GetNetwork(r.incusName)
	if err != nil {
		return ErrNotFound.WithResource(r).Wrap(err)
	}

	r.IncusNetwork = network
	r.ETag = eTag

	return err
}

func (r *Network) create() error {
	req := incusApi.NetworksPost{
		Name: r.incusName,
		Type: r.Config.Type,
	}

	if err := r.client.incus.CreateNetwork(req); err != nil {
		return fmt.Errorf("creating network %q: %w", r.Name(), err)
	}

	network, eTag, err := r.client.incus.GetNetwork(r.incusName)
	if err != nil {
		return fmt.Errorf("fetching created network %q: %w", r.Name(), err)
	}

	r.IncusNetwork = network
	r.ETag = eTag
	return nil
}

// Delete removes the network from Incus.
func (r *Network) Delete(opts ...Option) error {
	if !r.IsEnsured() {
		return nil
	}

	options := NewOptions(opts...)

	if r.client.hookBefore != nil {
		if err := r.client.hookBefore(ActionDelete, r, options, nil); err != nil {
			return err
		}
	}

	err := r.client.incus.DeleteNetwork(r.incusName)

	if r.client.hookAfter != nil {
		err = r.client.hookAfter(ActionDelete, r, options, err)
	}

	if err != nil {
		return err
	}

	r.IncusNetwork = nil
	r.ETag = ""
	return nil
}

var (
	_ Resource   = (*Network)(nil)
	_ EnsureAble = (*Network)(nil)
	_ DeleteAble = (*Network)(nil)
)

// sanitizeNetworkName generates a network interface name from project and network name.
// Returns a deterministic, unique name that fits within Linux interface name limits.
func sanitizeNetworkName(projectName, prefix, networkName string) string {
	full := networkName
	if projectName != "" {
		full = fmt.Sprintf("%s-%s", projectName, networkName)
	}

	// Sanitize: replace underscores with hyphens
	full = strings.ReplaceAll(full, "_", "-")

	if len(full) <= maxInterfaceNameLen {
		return full
	}

	return shortNetworkName(prefix, full)
}

// shortNetworkName generates a short, deterministic name from a longer input.
func shortNetworkName(prefix, full string) string {
	hash := sha256.Sum256([]byte(full))

	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash[:])
	encoded = strings.ToLower(encoded)

	hashPart := encoded[:networkNameHashLen]

	return prefix + hashPart
}
