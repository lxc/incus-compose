package main

import (
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// sidecarNeedsUpgrade reports whether a sidecar on have should be replaced by
// want. Two semver tags only move forwards, so an older incus-compose cannot
// downgrade a shared daemon; anything else replaces on any difference.
func sidecarNeedsUpgrade(have, want string) bool {
	if have == want {
		return false
	}

	haveVersion, wantVersion := imageSemver(have), imageSemver(want)
	if haveVersion != nil && wantVersion != nil {
		return wantVersion.GreaterThan(haveVersion)
	}

	return true
}

// devTag matches what `git describe --long --dirty` appends. Such a tag parses
// as a semver pre-release and so sorts below the release it was built from,
// which would pin a dev image out of ever being installed.
var devTag = regexp.MustCompile(`-g[0-9a-f]{7,}|-dirty$`)

// imageSemver returns the release in an image tag, nil when there is none.
func imageSemver(image string) *semver.Version {
	tag := image[strings.LastIndex(image, ":")+1:]

	// A registry port is not a tag: "localhost:5000/img" is untagged.
	if tag == image || strings.Contains(tag, "/") || devTag.MatchString(tag) {
		return nil
	}

	version, err := semver.NewVersion(tag)
	if err != nil {
		return nil
	}

	return version
}
