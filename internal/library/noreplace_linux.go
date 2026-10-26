package library

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameExclusive renames with renameat2's RENAME_NOREPLACE, which fails
// with EEXIST if to exists. Filesystems without it (NFS, CIFS, older
// kernels) answer EINVAL, ENOSYS or ENOTSUP.
func renameExclusive(from, to string) error {
	err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOSYS), errors.Is(err, unix.ENOTSUP): // ENOTSUP is also EOPNOTSUPP on Linux
		return errNoAtomicRename
	}
	return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
}
