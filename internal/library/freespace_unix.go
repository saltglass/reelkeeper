//go:build unix

package library

import "syscall"

// freeSpace returns the bytes available on the filesystem holding path,
// or -1 if it can't be told.
func freeSpace(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1
	}
	// Bsize is int64 on Linux but uint32 on macOS, so the conversion is
	// needed on one and redundant on the other. Block counts and sizes fit
	// in int64.
	return int64(st.Bavail) * int64(st.Bsize) //nolint:unconvert,gosec // see above
}
