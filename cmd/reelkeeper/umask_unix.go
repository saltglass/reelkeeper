//go:build unix

package main

import "syscall"

// Files and folders Reelkeeper creates (imports, the .reelkeeper-replaced folder,
// renamed media) must stay writable by the library's group (for example on a NAS
// that shares the library), so they can be managed from other machines and apps. Containers
// start with a umask of 022, which would make them owner-only; 002 keeps
// group write.
func init() { syscall.Umask(0o002) }
