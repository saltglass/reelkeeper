// Package buildinfo holds the version stamped into the binary at build time.
package buildinfo

// Version is set by the build (see the Justfile's build recipe) with
// -ldflags "-X github.com/saltglass/reelkeeper/internal/buildinfo.Version=...".
// Local builds without it report "dev".
var Version = "dev"
