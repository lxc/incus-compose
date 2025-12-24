package icclient

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/distribution/reference"
	"github.com/dominikbraun/graph"
	"github.com/gosimple/slug"
)

// maxInterfaceNameLen is the maximum safe length for Linux interface names.
// While IFNAMSIZ allows 15 chars, some dhclient versions have bugs with names > 13.
// See: https://bugs.debian.org/cgi-bin/bugreport.cgi?bug=858580
const maxInterfaceNameLen = 13

// networkNameHashLen is the number of base32 characters to use for the hash portion.
// 10 chars of base32 = 50 bits of entropy = ~1 quadrillion combinations.
const networkNameHashLen = 10

// maxInstanceNameLen is the maximum length for incus instance names.
// incus allows up to 63 characters, but we use 64 for the hash fallback.
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

// sanitizeProjectName converts a string to a valid incus project name.
// incus project names may not contain:
// - Underscores (replaced with hyphens)
// - Quotes (removed via slug)
// - Cannot be "*", ".", or "..".
func sanitizeProjectName(name string) string {
	safe := slug.Make(name)
	safe = strings.ReplaceAll(safe, "_", "-")
	return safe
}

// sanitizeInstanceName converts a string to a valid incus instance/DNS name.
// - Replaces underscores and special chars with hyphens
// - Converts to lowercase
// - If result exceeds 63 chars, uses a hash instead.
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

// ServiceOrder returns services in dependency order.
// If reverse is true, returns start order (dependencies first).
// If reverse is false, returns stop order (dependents first).
func ServiceOrder(project *Project, reverse bool) ([]string, error) {
	g := graph.New(graph.StringHash, graph.Directed(), graph.PreventCycles())

	// Add vertices
	for name := range project.Services {
		_ = g.AddVertex(name)
	}

	// Add edges for dependencies
	for name, service := range project.Services {
		for dep := range service.DependsOn {
			// Edge from dependency to dependent (dep must start before name)
			err := g.AddEdge(dep, name)
			if err != nil && err != graph.ErrEdgeAlreadyExists {
				return nil, fmt.Errorf("adding dependency edge %s -> %s: %w", dep, name, err)
			}
		}
	}

	order, err := graph.TopologicalSort(g)
	if err != nil {
		return nil, fmt.Errorf("topological sort: %w", err)
	}

	if reverse {
		slices.Reverse(order)
	}

	return order, nil
}

// ParseDockerRef is a wrapper with a nice error.
func ParseDockerRef(serviceName, image string) (reference.Named, error) {
	ref, err := reference.ParseDockerRef(image)
	if err != nil {
		return nil, fmt.Errorf("failed to parse service %q image %q: %w", serviceName, image, err)
	}

	return ref, nil
}
