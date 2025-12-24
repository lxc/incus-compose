package incuscompose

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/gosimple/slug"
)

// maxInterfaceNameLen is the maximum safe length for Linux interface names.
// While IFNAMSIZ allows 15 chars, some dhclient versions have bugs with names > 13.
// See: https://bugs.debian.org/cgi-bin/bugreport.cgi?bug=858580
const maxInterfaceNameLen = 13

// networkNamePrefix is used for hash-based short network names.
// "ic-" stands for incus-compose.
const networkNamePrefix = "ic-"

// networkNameHashLen is the number of base32 characters to use for the hash portion.
// 10 chars of base32 = 50 bits of entropy = ~1 quadrillion combinations.
const networkNameHashLen = 10

// networkNameForProject generates a network interface name from project and network name.
// Returns a deterministic, unique name that fits within Linux interface name limits.
//
// If the full name "{project}-{network}" fits in 13 chars, it's used as-is for readability.
// Otherwise, a hash-based short name is generated: "ic-{hash10}".
//
// The hash-based approach ensures:
//   - Determinism: same input always produces same output
//   - Uniqueness: different inputs produce different outputs (collision-resistant)
//   - Safety: always fits within the 13-char limit for dhclient compatibility
func networkNameForProject(projectName, networkName string) string {
	full := fmt.Sprintf("%s-%s", projectName, networkName)

	// Sanitize: replace underscores with hyphens (must be valid interface name)
	full = strings.ReplaceAll(full, "_", "-")

	if len(full) <= maxInterfaceNameLen {
		return full
	}

	return shortNetworkName(full)
}

// shortNetworkName generates a short, deterministic name from a longer input.
// Format: "ic-{base32hash}" where hash is 10 chars of lowercase base32-encoded SHA256.
//
// This provides 50 bits of entropy, making collision probability negligible
// (birthday bound ~2^25 ≈ 33 million networks before 50% collision chance).
func shortNetworkName(full string) string {
	hash := sha256.Sum256([]byte(full))

	// base32 encode without padding, then lowercase for readability
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash[:])
	encoded = strings.ToLower(encoded)

	// Take first 10 characters (50 bits of entropy)
	hashPart := encoded[:networkNameHashLen]

	return networkNamePrefix + hashPart
}

// maxContainerNameLen is the maximum length for Incus container names.
// Incus allows up to 63 characters, but we use 64 for the hash fallback.
const maxContainerNameLen = 63

// containerNameForService generates a container name
// either from the explict set ContainerName or the ServiceName.
func containerNameForService(service *types.ServiceConfig) string {
	if service.ContainerName != "" {
		return sanitizeName(service.ContainerName)
	}
	return sanitizeName(service.Name)
}

// sanitizeName converts a string to a valid Incus container/DNS name.
// - Replaces underscores and special chars with hyphens
// - Converts to lowercase
// - If result exceeds 63 chars, uses a hash instead.
func sanitizeName(name string) string {
	// slug.Make converts to lowercase, replaces special chars with hyphens
	// but keeps underscores, so we replace them explicitly
	safe := slug.Make(name)
	safe = strings.ReplaceAll(safe, "_", "-")

	if len(safe) > maxContainerNameLen {
		// Fall back to hash for very long names
		sha256sum := sha256.Sum256([]byte(name))
		safe = hex.EncodeToString(sha256sum[:16]) // 32 hex chars
	}

	return safe
}

// volumeNameForProject generates a volume name from project and volume name.
func volumeNameForProject(projectName, name string) string {
	return fmt.Sprintf("%s-%s", projectName, name)
}

// sanitizeProjectName converts a string to a valid Incus project name.
// Incus project names may not contain:
// - Underscores (replaced with hyphens)
// - Quotes (removed via slug)
// - Cannot be "*", ".", or "..".
func sanitizeProjectName(name string) string {
	safe := slug.Make(name)
	safe = strings.ReplaceAll(safe, "_", "-")
	return safe
}
