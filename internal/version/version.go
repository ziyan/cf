// Package version is what this build is.
package version

var (
	version = "dev"
	commit  = "none"
)

// Version is the release this was built from.
func Version() string { return version }

// Commit is the revision this was built from.
func Commit() string { return commit }
