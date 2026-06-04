// Package version provides build version information.
package version

import "runtime/debug"

// Version is the incus-compose version injected by release builds.
var Version = "latest"

// Current returns the current incus-compose version.
func Current() string {
	if Version != "" && Version != "latest" {
		return Version
	}

	info, ok := debug.ReadBuildInfo()
	if ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	return Version
}
