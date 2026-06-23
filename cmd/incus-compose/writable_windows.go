//go:build windows

package main

import "os"

// dirWritable reports whether the current user can create files in dir. Windows
// has no access(2) and proper effective-access checking needs the AccessCheck
// API, so we probe with a temp file. Acceptable here because this runs once per
// process and only when self-update is otherwise eligible.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".incus-compose-selfupdate-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	return os.Remove(name) == nil
}
