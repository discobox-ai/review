//go:build !windows

package git

import "os"

// readFile reads a file, and rename moves one over another. They are the
// halves of [Local.ReadFile] and [Local.Rename] that each kind of system
// spells its own way. Here an open file never stands in the way of a rename
// over its name, so the standard calls are the right ones.
func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

func rename(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }
