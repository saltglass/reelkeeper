package builtin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
)

// A move that fails part way puts back what it had already moved: the
// torrent's files are never left split between two folders (its record still
// names the old one), and trying again isn't blocked by its own files.
func TestMoveFilesPutsBackWhatItMovedWhenOneFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	info := &metainfo.Info{Name: "Show", Files: []metainfo.FileInfo{
		{Path: []string{"a.mkv"}, Length: 1},
		{Path: []string{"sub", "b.mkv"}, Length: 1},
	}}
	from, to := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(from, "Show", "a.mkv"), "a")
	mustWrite(t, filepath.Join(from, "Show", "sub", "b.mkv"), "b")
	// The second file's new folder can't be written to.
	locked := filepath.Join(to, "Show", "sub")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil { //nolint:gosec // a test folder
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) }) //nolint:gosec // so TempDir can clean up

	if err := moveFiles(info, from, to); err == nil {
		t.Fatal("the move succeeded into a folder that can't be written to")
	}
	for _, name := range []string{"a.mkv", "sub/b.mkv"} {
		if b, err := os.ReadFile(filepath.Join(from, "Show", name)); err != nil || len(b) != 1 {
			t.Errorf("%s isn't back in the old folder: %q %v", name, b, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(to, "Show", "a.mkv")); err == nil {
		t.Error("a.mkv was left in the new folder")
	}

	// Once the folder can be written to, trying again works.
	if err := os.Chmod(locked, 0o755); err != nil { //nolint:gosec // a test folder
		t.Fatal(err)
	}
	if err := moveFiles(info, from, to); err != nil {
		t.Fatalf("trying again: %v", err)
	}
	for _, name := range []string{"a.mkv", "sub/b.mkv"} {
		if _, err := os.Stat(filepath.Join(to, "Show", name)); err != nil {
			t.Errorf("%s didn't move: %v", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(from, "Show")); err == nil {
		t.Error("the old torrent folder was left behind, empty")
	}
}
