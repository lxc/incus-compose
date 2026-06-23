//go:build !windows

package main

import "golang.org/x/sys/unix"

// dirWritable reports whether the current user can create and rename files in
// dir. It uses access(2) so there are no filesystem side effects, and correctly
// reports failure for read-only mounts (EROFS), wrong ownership, or missing
// permission bits.
func dirWritable(dir string) bool {
	return unix.Access(dir, unix.W_OK|unix.X_OK) == nil
}
