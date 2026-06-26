// Package version provides build version information.
package version

// Version is the incus-compose version injected by release builds.
var Version = "latest"

// Current returns the current incus-compose version.
func Current() string {
	return Version
}
