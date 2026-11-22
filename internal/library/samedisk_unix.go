//go:build unix

package library

import "syscall"

// sameDisk reports whether two folders are on the same filesystem, so a
// file can be renamed from one to the other instead of copied. A move
// still falls back to copying if the rename says otherwise (two mounts of
// one disk, as in a container, share a device but can't rename across).
func sameDisk(a, b string) bool {
	var sa, sb syscall.Stat_t
	if syscall.Stat(a, &sa) != nil || syscall.Stat(b, &sb) != nil {
		return false
	}
	return sa.Dev == sb.Dev
}
