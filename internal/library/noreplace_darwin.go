package library

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameExclusive renames with renamex_np's RENAME_EXCL, which fails with
// EEXIST if to exists. Filesystems without it answer ENOTSUP or EINVAL.
func renameExclusive(from, to string) error {
	err := unix.RenamexNp(from, to, unix.RENAME_EXCL)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOTSUP):
		return errNoAtomicRename
	}
	return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
}
