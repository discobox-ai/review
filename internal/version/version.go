// Package version is the version discobox-review reports.
package version

import "runtime/debug"

// Version is set at build time with
// -ldflags "-X github.com/discobox-ai/review/internal/version.Version=v1.2.3".
// Left empty, Get falls back to what the Go toolchain recorded.
var Version string

// Get returns the version this binary was built as: the one the build set,
// else the module version `go install` recorded, else the VCS revision, else
// "dev".
func Get() string {
	if Version != "" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}
