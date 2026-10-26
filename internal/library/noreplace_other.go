//go:build !linux && !darwin

package library

// renameExclusive isn't available here; renameNoReplace falls back to a
// hard link.
func renameExclusive(string, string) error { return errNoAtomicRename }
