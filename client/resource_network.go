package client

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"time"

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

	// External marks the network as externally managed.
	// External networks must already exist and won't be created or deleted.
	External bool

	// Extensions are Incus network config key-value pairs sourced from the
	// x-incus compose extension. All entries pass through verbatim to the
	// Incus network config on creation.
	Extensions map[string]string
}

// Network represents an Incus bridge network.
type Network struct {
	*BaseResource

	client    *Client
	incusName string
	created   bool
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

	// Generate Incus name: external networks use raw name, others get sanitized
	if config.External {
		network.incusName = name
	} else {
		network.incusName = sanitizeNetworkName(c.project, c.Config().NetworkPrefix, name)
	}

	return network, nil
}

// String is for debugging.
func (r *Network) String() string {
	return fmt.Sprintf("%v(%v)", r.kind, r.incusName)
}

// IncusName returns the sanitized network name used in Incus.
func (r *Network) IncusName() string {
	return r.incusName
}

// IsEnsured returns true if the network state has been fetched from Incus.
func (r *Network) IsEnsured() bool {
	return r.IncusNetwork != nil
}

// Created returns true if the network was created during the last Ensure call.
func (r *Network) Created() bool {
	return r.created
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

	// External networks must exist - don't create them
	if r.Config.External {
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
	config, err := networkCreateConfig(r.Config.Extensions)
	if err != nil {
		return fmt.Errorf("preparing network config for %q: %w", r.Name(), err)
	}

	// Use client's configured description format for consistency with other resources.
	req := incusApi.NetworksPost{
		Name: r.incusName,
		Type: r.Config.Type,
		NetworkPut: incusApi.NetworkPut{
			Description: fmt.Sprintf(r.client.Config().DescriptionFormat, r.Name()),
			Config:      config,
		},
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
	r.created = true

	// Wait for the network to become ready (Status == Created) using context-aware timeout.
	// This mirrors the pattern used for instances to avoid races in tests that act
	// on networks immediately after creation.
	ctx, cancel := context.WithTimeout(r.client.globalClient.Ctx, 5*time.Second)
	defer cancel()
	interval := 100 * time.Millisecond
	for {
		nw, eTag, err := r.client.incus.GetNetwork(r.incusName)
		if err == nil {
			if nw.Status == incusApi.NetworkStatusCreated || nw.Status == "Created" {
				r.IncusNetwork = nw
				r.ETag = eTag
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for network %q readiness: %w", r.Name(), ctx.Err())
		case <-time.After(interval):
			// retry
		}
	}
}

// networkCreateConfig returns the Incus network config to use during creation.
// DHCP ranges are added only when the address is explicit. Auto addresses are
// resolved by Incus during creation, and updating immediately afterward restarts
// dnsmasq and can race with the old dnsmasq process still holding its socket.
func networkCreateConfig(extensions map[string]string) (map[string]string, error) {
	if len(extensions) == 0 {
		return nil, nil
	}

	config := maps.Clone(extensions)

	if addr := config["ipv4.address"]; addr != "" && addr != "none" && addr != "auto" && config["ipv4.dhcp.ranges"] == "" {
		dhcpRange, err := calcIPv4DHCPRange(addr)
		if err != nil {
			return nil, fmt.Errorf("calculating IPv4 DHCP range: %w", err)
		}
		config["ipv4.dhcp.ranges"] = dhcpRange
	}

	if addr := config["ipv6.address"]; addr != "" && addr != "none" && addr != "auto" && config["ipv6.dhcp.ranges"] == "" {
		dhcpRange, err := calcIPv6DHCPRange(addr)
		if err != nil {
			return nil, fmt.Errorf("calculating IPv6 DHCP range: %w", err)
		}
		config["ipv6.dhcp.ranges"] = dhcpRange
		if config["ipv6.dhcp.stateful"] == "" {
			config["ipv6.dhcp.stateful"] = "true"
		}
	}

	return config, nil
}

// Delete removes the network from Incus.
// External networks are never deleted.
func (r *Network) Delete(opts ...Option) error {
	if !r.IsEnsured() {
		return nil
	}

	// External networks are not managed - don't delete them
	if r.Config.External {
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

// UpdateDNSAliases rewrites the network's raw.dnsmasq with an "address" record
// per service IP, so each service name resolves to its instance addresses
// (round-robin when scaled). External and unensured networks are skipped.
// Setting raw.dnsmasq disables AppArmor for the dnsmasq process (not containers).
// The update is idempotent: if the resulting config is unchanged, dnsmasq is not
// restarted.
func (r *Network) UpdateDNSAliases(serviceIPs map[string][]string) error {
	if r.Config.External || !r.IsEnsured() {
		return nil
	}

	raw := dnsmasqRecords(serviceIPs)

	if r.IncusNetwork.Config["raw.dnsmasq"] == raw {
		return nil // No change - avoid restarting dnsmasq.
	}

	put := r.IncusNetwork.Writable()
	if put.Config == nil {
		put.Config = map[string]string{}
	}
	if raw == "" {
		delete(put.Config, "raw.dnsmasq")
	} else {
		put.Config["raw.dnsmasq"] = raw
	}

	if err := r.client.incus.UpdateNetwork(r.incusName, put, r.ETag); err != nil {
		return fmt.Errorf("updating dnsmasq records for network %q: %w", r.Name(), err)
	}

	return nil
}

// dnsmasqRecords builds the raw.dnsmasq content: one "address" record per
// service IP, sorted by service name for deterministic output.
func dnsmasqRecords(serviceIPs map[string][]string) string {
	var b strings.Builder
	for _, service := range slices.Sorted(maps.Keys(serviceIPs)) {
		for _, ip := range serviceIPs[service] {
			fmt.Fprintf(&b, "address=/%s/%s\n", service, ip)
		}
	}
	return b.String()
}

var (
	_ Resource   = (*Network)(nil)
	_ EnsureAble = (*Network)(nil)
	_ DeleteAble = (*Network)(nil)
)

// calcIPv4DHCPRange calculates an Incus-format DHCP range for an IPv4 bridge network.
// The first quarter of the address block (1 << (hostBits-2)) is reserved for static
// assignment; DHCP starts at that boundary and runs to the last usable address.
// Returns a range string in "FIRST-LAST" format.
func calcIPv4DHCPRange(cidr string) (string, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", fmt.Errorf("parsing IPv4 CIDR %q: %w", cidr, err)
	}

	prefix = prefix.Masked()

	bits := prefix.Bits()
	if bits > 30 {
		return "", fmt.Errorf("IPv4 prefix /%d too small for DHCP range (need at most /30)", bits)
	}

	hostBits := uint(32 - bits)
	// staticEnd: first address of the DHCP zone = 1/4 of the block, derived by shifting.
	// Example: /24 → hostBits=8 → staticEnd = 1<<6 = 64 → DHCP starts at .64
	staticEnd := uint64(1) << (hostBits - 2)
	// Last usable address = total - 2 (total - 1 is broadcast).
	lastUsable := (uint64(1) << hostBits) - 2

	networkAddr := prefix.Addr()
	dhcpStart := addOffset(networkAddr, staticEnd)
	dhcpEnd := addOffset(networkAddr, lastUsable)

	return fmt.Sprintf("%s-%s", dhcpStart, dhcpEnd), nil
}

// calcIPv6DHCPRange calculates an Incus-format DHCP range for an IPv6 bridge network.
// The first 256 addresses (::0–::ff) are reserved for static assignment.
// Returns a range string in "FIRST-LAST" format.
func calcIPv6DHCPRange(cidr string) (string, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", fmt.Errorf("parsing IPv6 CIDR %q: %w", cidr, err)
	}

	prefix = prefix.Masked()
	networkAddr := prefix.Addr()

	dhcpStart := addOffset(networkAddr, 0x100)
	dhcpEnd := addOffset(networkAddr, 0xffff)

	return fmt.Sprintf("%s-%s", dhcpStart, dhcpEnd), nil
}

// addOffset adds an integer offset to a netip.Addr (IPv4 or IPv6).
// For IPv4 the offset must fit in 32 bits; for IPv6, in 64 bits.
func addOffset(addr netip.Addr, offset uint64) netip.Addr {
	b := addr.As16()

	// Treat the last 8 bytes as a big-endian uint64 and add the offset.
	v := uint64(b[8])<<56 | uint64(b[9])<<48 | uint64(b[10])<<40 | uint64(b[11])<<32 |
		uint64(b[12])<<24 | uint64(b[13])<<16 | uint64(b[14])<<8 | uint64(b[15])
	v += offset

	b[8] = byte(v >> 56)
	b[9] = byte(v >> 48)
	b[10] = byte(v >> 40)
	b[11] = byte(v >> 32)
	b[12] = byte(v >> 24)
	b[13] = byte(v >> 16)
	b[14] = byte(v >> 8)
	b[15] = byte(v)

	result := netip.AddrFrom16(b)
	if addr.Is4() {
		result = result.Unmap()
	}

	return result
}

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
