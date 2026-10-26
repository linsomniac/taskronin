// Package buildinfo holds build-time version information.
package buildinfo

// Version is the release version, set by -ldflags at build time.
var Version = "dev"

// Commit is the short git commit hash, set by -ldflags at build time.
var Commit = ""

// APIVersion is the HTTP API version served and expected by Taskronin.
const APIVersion = "v1"
