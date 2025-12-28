package client

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/gosimple/slug"
)

// maxInterfaceNameLen is the maximum safe length for Linux interface names.
// While IFNAMSIZ allows 15 chars, some dhclient versions have bugs with names > 13.
// See: https://bugs.debian.org/cgi-bin/bugreport.cgi?bug=858580
const maxInterfaceNameLen = 13

// networkNameHashLen is the number of base32 characters to use for the hash portion.
// 10 chars of base32 = 50 bits of entropy = ~1 quadrillion combinations.
const networkNameHashLen = 10

// maxInstanceNameLen is the maximum length for Incus instance names.
// Incus allows up to 63 characters (DNS hostname limit).
const maxInstanceNameLen = 63

// shortNetworkName generates a short, deterministic name from a longer input.
// Format: "ic-{base32hash}" where hash is 10 chars of lowercase base32-encoded SHA256.
//
// This provides 50 bits of entropy, making collision probability negligible
// (birthday bound ~2^25 ≈ 33 million networks before 50% collision chance).
func shortNetworkName(prefix, full string) string {
	hash := sha256.Sum256([]byte(full))

	// base32 encode without padding, then lowercase for readability
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash[:])
	encoded = strings.ToLower(encoded)

	// Take first 10 characters (50 bits of entropy)
	hashPart := encoded[:networkNameHashLen]

	return prefix + hashPart
}

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
func networkNameForProject(projectName, prefix, networkName string) string {
	full := networkName
	if projectName != "" {
		full = fmt.Sprintf("%s-%s", projectName, networkName)
	}

	// Sanitize: replace underscores with hyphens (must be valid interface name)
	full = strings.ReplaceAll(full, "_", "-")

	if len(full) <= maxInterfaceNameLen {
		return full
	}

	return shortNetworkName(prefix, full)
}

// sanitizeProjectName converts a string to a valid Incus project name.
// Replaces underscores with hyphens and removes special characters via slug.
func sanitizeProjectName(name string) string {
	safe := slug.Make(name)
	safe = strings.ReplaceAll(safe, "_", "-")
	return safe
}

// sanitizeInstanceName converts a string to a valid Incus instance name.
// Converts to lowercase, replaces special chars and underscores with hyphens.
// Names exceeding 63 chars are replaced with a 32-char hex hash for DNS compatibility.
func sanitizeInstanceName(name string) string {
	// slug.Make converts to lowercase, replaces special chars with hyphens
	// but keeps underscores, so we replace them explicitly
	safe := slug.Make(name)
	safe = strings.ReplaceAll(safe, "_", "-")

	if len(safe) > maxInstanceNameLen {
		// Fall back to hash for very long names
		sha256sum := sha256.Sum256([]byte(name))
		safe = hex.EncodeToString(sha256sum[:16]) // 32 hex chars
	}

	return safe
}
