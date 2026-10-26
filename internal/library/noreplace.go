package library

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
// ignores case finds the file itself at the new name, so the exclusive
// rename refuses it. Such a rename goes through a temporary name instead
// (renameCaseOnly), each step exclusive too.
func renameNoReplace(from, to string) error {
	err := renameOnce(from, to)
	// sameFile is only a hint that this may be a case-insensitive
	// filesystem (two hard links look the same); renameCaseOnly's own steps
	// are what keep anything from being overwritten.
	if errors.Is(err, fs.ErrExist) && from != to && strings.EqualFold(from, to) && sameFile(from, to) {
		return renameCaseOnly(from, to)
	}
	return err
}

// renameOnce is one exclusive rename: the system's own, or the hard-link
// fallback where the filesystem doesn't have one.
func renameOnce(from, to string) error {
	err := renameExclusive(from, to)
	if errors.Is(err, errNoAtomicRename) {
		err = linkThenUnlink(from, to)
	}
	return err
}

// caseRenameStep, when set, is called between renameCaseOnly's two steps
// (tests use it to change the folder at that moment).
var caseRenameStep func(tmp string)

// renameCaseOnly changes only the case of a file's name, in two exclusive
// renames: to a temporary hidden name in the same folder, then to the new
// name. If the second step is refused (the new name is a different file:
// another hard link on a filesystem that minds case, or a file another
// program created meanwhile), the file goes back to its old name, again
// without overwriting anything; the answer is then fs.ErrExist. If even
// that is refused, the file stays at the temporary name, and the error
// says where it is.
func renameCaseOnly(from, to string) error {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(from), ".reelkeeper-rename-"+hex.EncodeToString(b[:]))
	if err := renameOnce(from, tmp); err != nil {
		return err
	}
	if caseRenameStep != nil {
		caseRenameStep(tmp)
	}
	err := renameOnce(tmp, to)
	if err == nil {
		return nil
	}
	if rerr := renameOnce(tmp, from); rerr != nil {
		return &strandedError{At: tmp, Want: to, Old: from, Err: err, UndoErr: rerr}
	}
	return err
}

// strandedError is a case-only rename that could neither finish nor be
// undone: the file is safe, but at a temporary name.
type strandedError struct {
	At, Want, Old string
	Err, UndoErr  error
}

func (e *strandedError) Error() string {
	return fmt.Sprintf("couldn't rename it to %s (%v) or give it back its old name %s (%v); the file is safe at %s",
		filepath.Base(e.Want), e.Err, filepath.Base(e.Old), e.UndoErr, e.At)
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
