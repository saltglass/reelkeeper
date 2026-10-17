package builtin

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadTorrentFile(t *testing.T) {
	c := makeContent(t, "Some.Movie.2020", contentOpts{})
	s := startSeeder(t, seederOpts{}, c)
	nw := &loopbackNet{}
	e, cfg := openEngine(t, nw, engineOpts{})

	hash, err := e.Add(AddRequest{Torrent: c.torrent, Category: cat, Tags: []string{"reelkeeper", "indexer"}})
	if err != nil {
		t.Fatal(err)
	}
	if hash != c.hash {
		t.Fatalf("hash = %s, want %s", hash, c.hash)
	}
	st := status(t, e, hash)
	if st.Name != c.name || st.Size != c.totalLen || st.SavePath != cfg.Folder || st.Category != cat {
		t.Errorf("status = %+v", st)
	}
	feed(t, e, hash, s)
	done := waitFor(t, e, hash, "finishing", 30*time.Second, func(s Status) bool { return s.Progress >= 1 && sharing(s.State) })
	if done.Left != 0 || done.Have != c.totalLen || done.Completed == nil {
		t.Errorf("finished status = %+v", done)
	}
	sameFiles(t, cfg.Folder, c)
	if done.ContentPath != filepath.Join(cfg.Folder, c.name) {
		t.Errorf("content path = %q", done.ContentPath)
	}
	if _, err := os.Stat(e.torrentPath(hash)); err != nil {
		t.Errorf("the .torrent file wasn't kept: %v", err)
	}
	if nw.refused.Load() != 0 {
		t.Errorf("the engine tried to reach beyond loopback %d times", nw.refused.Load())
	}
}
