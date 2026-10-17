package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rktorrent "github.com/saltglass/reelkeeper/internal/torrent"
)

func TestMagnetGetsItsDetailsFromAPeer(t *testing.T) {
	c := makeContent(t, "Magnet.Movie.2021", contentOpts{})
	s := startSeeder(t, seederOpts{}, c)
	e, cfg := openEngine(t, &loopbackNet{}, engineOpts{})

	hash, err := e.Add(AddRequest{Magnet: c.magnet(), Category: cat})
	if err != nil {
		t.Fatal(err)
	}
	st := status(t, e, hash)
	if st.State != rktorrent.Metadata || st.Size != 0 {
		t.Fatalf("before any peer: %+v", st)
	}
	if src, err := e.Source(cat, hash); err != nil || src.Magnet == "" || src.Torrent != nil {
		t.Fatalf("source of a magnet: %+v %v", src, err)
	}
	if l, err := e.Layout(cat, hash); err != nil || len(l.Files) != 0 || l.Folder != cfg.Folder {
		t.Fatalf("layout before details: %+v %v", l, err)
	}
	feed(t, e, hash, s)
	st = waitFor(t, e, hash, "finishing", 30*time.Second, func(s Status) bool { return s.Progress >= 1 })
	if st.Name != c.name || st.Size != c.totalLen {
		t.Errorf("after details: %+v", st)
	}
	sameFiles(t, cfg.Folder, c)
	// The details were saved as a .torrent file, and the magnet link let go.
	src, err := e.Source(cat, hash)
	if err != nil || len(src.Torrent) == 0 || src.Magnet != "" {
		t.Fatalf("source after details: %d bytes, magnet %q, %v", len(src.Torrent), src.Magnet, err)
	}
	l, err := e.Layout(cat, hash)
	if err != nil || len(l.Files) != 3 || l.Files[0].Name == "" || !strings.HasPrefix(l.Files[0].Name, c.name+"/") {
		t.Fatalf("layout: %+v %v", l, err)
	}
}

func TestPauseAndResume(t *testing.T) {
	c := makeContent(t, "Pause.Me.2022", contentOpts{big: 3 << 20})
	s := startSeeder(t, seederOpts{}, c)
	nw := &loopbackNet{}
	e, cfg := openEngine(t, nw, engineOpts{cfg: func(c *Config) { c.DownloadLimitKBs = 600 }})

	// Added stopped: no network use at all.
	hash, err := e.Add(AddRequest{Torrent: c.torrent, Category: cat, Stopped: true})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if st := status(t, e, hash); st.State != rktorrent.Paused || isRunning(e, hash) {
		t.Fatalf("added stopped: %+v", st)
	}
	if err := e.Start(cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	feed(t, e, hash, s)
	waitFor(t, e, hash, "some progress", 20*time.Second, func(s Status) bool { return s.Have > 300<<10 })
	if err := e.Stop(cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	st := status(t, e, hash)
	if st.State != rktorrent.Paused || isRunning(e, hash) || st.Progress >= 1 {
		t.Fatalf("after Stop: %+v", st)
	}
	had := st.Have
	dials := nw.dials.Load()
	time.Sleep(500 * time.Millisecond)
	if nw.dials.Load() != dials {
		t.Error("a paused torrent kept dialling")
	}
	if got := status(t, e, hash).Have; got < had {
		t.Errorf("pausing lost progress: %d -> %d", had, got)
	}
	// Resume: it carries on and finishes.
	if err := e.Start(cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	feed(t, e, hash, s)
	waitFor(t, e, hash, "finishing", 30*time.Second, func(s Status) bool { return s.Progress >= 1 })
	sameFiles(t, cfg.Folder, c)
	// A finished torrent can be paused too.
	if err := e.Stop(cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	if st := status(t, e, hash); st.State != rktorrent.SeedingPaused {
		t.Errorf("finished and stopped: %s", st.State)
	}
}

func TestRemoveDeletesOnlyTheTorrentsOwnFiles(t *testing.T) {
	cases := []struct {
		name        string
		deleteFiles bool
	}{{"keep the files", false}, {"delete the files", true}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := makeContent(t, "Remove.Me.2020", contentOpts{})
			e, cfg := openEngine(t, &loopbackNet{}, engineOpts{})
			// The torrent's files are already in the download folder.
			copyTree(t, c.root, filepath.Join(cfg.Folder, c.name))
			// Things that are not the torrent's, around and inside its folder.
			other := filepath.Join(cfg.Folder, "Somebody.Elses.Download")
			mustWrite(t, filepath.Join(other, "keep.mkv"), "precious")
			mustWrite(t, filepath.Join(cfg.Folder, "loose.txt"), "precious")
			mustWrite(t, filepath.Join(cfg.Folder, c.name, "extra-not-in-torrent.txt"), "precious")
			// The same name one folder up must be safe too.
			outside := filepath.Join(filepath.Dir(cfg.Folder), "movie.mkv")
			mustWrite(t, outside, "precious")

			hash, err := e.Add(AddRequest{Torrent: c.torrent, Category: cat, Stopped: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := e.Remove(cat, []string{hash}, tc.deleteFiles); err != nil {
				t.Fatal(err)
			}
			if _, ok := tryStatus(e, hash); ok {
				t.Error("still listed after removing")
			}
			if _, err := os.Stat(e.torrentPath(hash)); err == nil {
				t.Error("the kept .torrent file wasn't removed")
			}
			for _, p := range []string{filepath.Join(other, "keep.mkv"), filepath.Join(cfg.Folder, "loose.txt"), filepath.Join(cfg.Folder, c.name, "extra-not-in-torrent.txt"), outside} {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("%s was deleted: %v", p, err)
				}
			}
			_, err = os.Stat(filepath.Join(cfg.Folder, c.name, "movie.mkv"))
			if tc.deleteFiles && err == nil {
				t.Error("the torrent's file wasn't deleted")
			}
			if !tc.deleteFiles && err != nil {
				t.Error("the torrent's file was deleted")
			}
			// Removing it for good: the engine forgets it on restart too.
			closeEngine(t, e)
			e2, err := Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer closeEngine(t, e2)
			if all, _ := e2.List(cat); len(all) != 0 {
				t.Errorf("%d torrents came back after being removed", len(all))
			}
		})
	}
}

func TestOnlyReelkeepersCategoryIsTouched(t *testing.T) {
	c := makeContent(t, "Category.Test.2020", contentOpts{})
	e, _ := openEngine(t, &loopbackNet{}, engineOpts{})
	hash, err := e.Add(AddRequest{Torrent: c.torrent, Category: cat, Stopped: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.List(""); err == nil {
		t.Error("listing without a category should fail")
	}
	const other = "somebody-elses"
	calls := map[string]error{
		"Remove":          e.Remove(other, []string{hash}, true),
		"Stop":            e.Stop(other, []string{hash}),
		"Start":           e.Start(other, []string{hash}),
		"Top":             e.Top(other, []string{hash}),
		"MoveCategory":    e.MoveCategory(other, "x", []string{hash}),
		"Recheck":         e.Recheck(other, hash),
		"SetFolder":       e.SetFolder(other, hash, "/tmp"),
		"Add (same hash)": addErr(e, c, other),
	}
	for name, err := range calls {
		if err == nil {
			t.Errorf("%s changed a torrent outside its category", name)
		}
	}
	if _, err := e.TrackerMessages(other, hash); err == nil {
		t.Error("TrackerMessages answered for another category")
	}
	if _, err := e.Source(other, hash); err == nil {
		t.Error("Source answered for another category")
	}
	if _, err := e.Layout(other, hash); err == nil {
		t.Error("Layout answered for another category")
	}
	if _, ok := tryStatus(e, hash); !ok {
		t.Error("the torrent is gone")
	}
	// Moving it to another of Reelkeeper's categories does work.
	if err := e.MoveCategory(cat, "reelkeeper-tv", []string{hash}); err != nil {
		t.Fatal(err)
	}
	if all, _ := e.List("reelkeeper-tv"); len(all) != 1 {
		t.Errorf("after MoveCategory: %d in the new category", len(all))
	}
}

func addErr(e *Engine, c *content, category string) error {
	_, err := e.Add(AddRequest{Torrent: c.torrent, Category: category})
	return err
}

func TestAddRejectsBadInput(t *testing.T) {
	e, _ := openEngine(t, &loopbackNet{}, engineOpts{})
	for name, req := range map[string]AddRequest{
		"nothing":        {Category: cat},
		"no category":    {Magnet: "magnet:?xt=urn:btih:" + strings.Repeat("ab", 20)},
		"not a torrent":  {Torrent: []byte("hello"), Category: cat},
		"not a magnet":   {Magnet: "https://example.com", Category: cat},
		"magnet no hash": {Magnet: "magnet:?dn=x", Category: cat},
		"relative path":  {Magnet: "magnet:?xt=urn:btih:" + strings.Repeat("ab", 20), Category: cat, SavePath: "relative/dir"},
	} {
		if _, err := e.Add(req); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestInsideKeepsPathsInTheFolder(t *testing.T) {
	for name, want := range map[string]bool{
		"a/b.mkv":       true,
		"b.mkv":         true,
		"../b.mkv":      false,
		"a/../../b.mkv": false,
		"a/..":          false,
		"..":            false,
		".":             false,
	} {
		if _, ok := inside("/downloads", name); ok != want {
			t.Errorf("inside(%q) = %v, want %v", name, ok, want)
		}
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	for rel, b := range readTree(t, from) {
		mustWrite(t, filepath.Join(to, filepath.FromSlash(rel)), string(b))
	}
}

func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
