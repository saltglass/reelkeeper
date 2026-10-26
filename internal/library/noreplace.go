package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// errNoSafeRename means neither way of renaming without overwriting works
// on this filesystem, so the rename wasn't done.
var errNoSafeRename = errors.New("this filesystem can't rename a file without the risk of overwriting another, so it was left alone")

// errNoAtomicRename is what renameExclusive says when the operating system
// or filesystem has no rename that refuses to replace.
var errNoAtomicRename = errors.New("no exclusive rename here")

// renameNoReplace renames from to to, and never replaces a file already at
// to, even one created a moment ago by another program: checking first and
// then renaming leaves a gap in which a new file would be overwritten. The
// refusal is an error matching fs.ErrExist.
//
//   - First the system's own exclusive rename (renameat2 RENAME_NOREPLACE on
//     Linux, renamex_np RENAME_EXCL on macOS), which checks and renames in
//     one step.
//   - Where the filesystem doesn't have it (NFS, for one), a hard link to
//     the new name, which also fails if the name is taken, then removing
//     the old name. Both names briefly point to the same file; nothing is
//     copied.
//   - When neither works, nothing is renamed (errNoSafeRename).
//
// A change of case only ("movie.mkv" to "Movie.mkv") on a filesystem that
// ignores case finds the file itself at the new name; that one is renamed
// normally, since the new name can't be anything but the file itself.
func renameNoReplace(from, to string) error {
	err := renameExclusive(from, to)
	if errors.Is(err, errNoAtomicRename) {
		err = linkThenUnlink(from, to)
	}
	if errors.Is(err, fs.ErrExist) && strings.EqualFold(from, to) && sameFile(from, to) {
		return os.Rename(from, to)
	}
	return err
}

// linkThenUnlink renames by hard-linking to the new name and then removing
// the old one.
func linkThenUnlink(from, to string) error {
	if err := os.Link(from, to); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return err
		}
		return fmt.Errorf("%w (%w)", errNoSafeRename, err)
	}
	if err := os.Remove(from); err != nil {
		// Take back the new name, which is still our own link.
		if sameFile(from, to) {
			_ = os.Remove(to)
		}
		return err
	}
	return nil
}

// sameFile reports whether a and b are the same file on disk (without
// following links).
func sameFile(a, b string) bool {
	ai, err := os.Lstat(a)
	if err != nil {
		return false
	}
	bi, err := os.Lstat(b)
	return err == nil && os.SameFile(ai, bi)
}
