// Package version exposes build metadata, overridable via -ldflags.
package version

var (
	// Version is the semantic version, set at build time.
	Version = "dev"
	// Commit is the git SHA, set at build time.
	Commit = "none"
)
