package library

// FreeSpace returns the bytes available on the filesystem holding path, or
// -1 if it can't be told (for example, on a system without statfs).
func FreeSpace(path string) int64 { return freeSpace(path) }
