//go:build !unix

package library

// freeSpace isn't implemented on this platform.
func freeSpace(string) int64 { return -1 }
