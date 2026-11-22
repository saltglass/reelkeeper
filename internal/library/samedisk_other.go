//go:build !unix

package library

// sameDisk can't be told here, so moves copy (the safe way).
func sameDisk(string, string) bool { return false }
