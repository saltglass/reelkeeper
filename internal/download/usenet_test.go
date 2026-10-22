package download

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// memUsenet is a Usenet client in memory.
type memUsenet struct {
	jobs    map[string]*Torrent
	added   []Add
	removed []string
	deleted []string // removed with their files
	paused  []string
	free    int64
	next    int
}

func newMemUsenet() *memUsenet { return &memUsenet{jobs: map[string]*Torrent{}, free: -1} }

func (m *memUsenet) Name() string                                    { return "SABnzbd" }
func (m *memUsenet) Version(context.Context) (string, error)         { return "5.1.3", nil }
func (m *memUsenet) EnsureCategory(context.Context, string) error    { return nil }
func (m *memUsenet) FreeSpace(context.Context) (int64, error)        { return m.free, nil }
func (m *memUsenet) TopJobs(context.Context, string, []string) error { return nil }
func (m *memUsenet) Problems(context.Context) ([]string, []string, error) {
	return nil, nil, nil
}

func (m *memUsenet) AddNZB(_ context.Context, nzb []byte, name, category string, top bool) (string, error) {
	m.next++
	id := "SABnzbd_nzo_" + string(rune('a'+m.next))
	m.added = append(m.added, Add{NZB: nzb, Name: name, Category: category, Top: top})
	m.jobs[id] = &Torrent{Hash: id, Name: name, Category: category, State: torrent.Downloading, Size: 8 << 30, Left: 6 << 30, Progress: 0.25}
	return id, nil
}

func (m *memUsenet) Jobs(_ context.Context, category string) ([]Torrent, error) {
	var out []Torrent
	for _, t := range m.jobs {
		if t.Category == category {
			out = append(out, *t)
		}
	}
	return out, nil
}

func (m *memUsenet) RemoveJobs(_ context.Context, _ string, ids []string, deleteFiles bool) error {
	for _, id := range ids {
		delete(m.jobs, id)
		m.removed = append(m.removed, id)
		if deleteFiles {
			m.deleted = append(m.deleted, id)
		}
	}
	return nil
}

func (m *memUsenet) PauseJobs(_ context.Context, _ string, ids []string) error {
	for _, id := range ids {
		m.jobs[id].State = torrent.Paused
		m.paused = append(m.paused, id)
	}
	return nil
}

func (m *memUsenet) ResumeJobs(_ context.Context, _ string, ids []string) error {
	for _, id := range ids {
		m.jobs[id].State = torrent.Downloading
	}
	return nil
}

// finish makes a job Completed in the history, at path.
func (m *memUsenet) finish(id, path string) {
	j := m.jobs[id]
	j.State, j.Progress, j.Left, j.ContentPath = torrent.Completed, 1, 0, path
}

// nzbFetcher hands out .nzb files for Usenet releases.
type nzbFetcher struct{ fakeFetcher }

func (nzbFetcher) Fetch(_ context.Context, r indexer.Release) (indexer.Fetched, error) {
	if r.Usenet() {
		return indexer.Fetched{NZB: []byte("<nzb>" + r.Title + "</nzb>")}, nil
	}
	return indexer.Fetched{Magnet: "magnet:?xt=urn:btih:" + r.InfoHash}, nil
}

type ufix struct {
	*dfix
	news  *memUsenet
	ucfg  settings.UsenetClient
	clean settings.Cleanup
	again []int64
}

func newUFix(t *testing.T, db *store.Store) *ufix {
	f := &ufix{dfix: newDFix(t, db), news: newMemUsenet(),
		ucfg: settings.UsenetClient{Type: settings.UsenetSABnzbd, URL: "http://sab:8080", APIKey: "k"}}
	f.svc.idx = nzbFetcher{fakeFetcher{ratio: 1.5}}
	f.svc.SetUsenet(func() settings.UsenetClient { return f.ucfg })
	f.svc.NewUsenet = func(settings.UsenetClient) UsenetClient { return f.news }
	f.clean = settings.DefaultCleanup()
	f.clean.Mode = settings.CleanupOn
	f.svc.SetCleanup(func() settings.Cleanup { return f.clean }, func(_ context.Context, id int64) error {
		f.again = append(f.again, id)
		return nil
	}, nil)
	return f
}

func (f *ufix) grabUsenet(t *testing.T, requested bool) *Download {
	t.Helper()
	err := f.svc.Grab(context.Background(), grab.Grabbed{
		Release: indexer.Release{Release: core.Release{Title: "Ronin.1998.1080p.BluRay.x264-NZB", Size: 8 << 30, MinimumRatio: 2},
			IndexerID: f.ixID, Indexer: "NZBIndexer", Protocol: indexer.ProtocolUsenet},
		Version: f.mv, Quality: "Bluray-1080p", Score: 10, Requested: requested,
	})
	if err != nil {
		t.Fatal(err)
	}
	var d Download
	if err := f.svc.db.DB.NewSelect().Model(&d).Where("protocol = ?", indexer.ProtocolUsenet).Order("id DESC").Limit(1).Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &d
}

func TestUsenetGrab(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newUFix(t, db)
		d := f.grabUsenet(t, true)
		a := f.news.added[0]
		if string(a.NZB) != "<nzb>Ronin.1998.1080p.BluRay.x264-NZB</nzb>" || a.Name != "Ronin.1998.1080p.BluRay.x264-NZB" ||
			a.Category != settings.MovieCategory || !a.Top {
			t.Errorf("added = %+v", a)
		}
		// A Usenet record: the job's ID, the client's type, no seeding rules,
		// and nothing kept for moves between torrent clients.
		if d.Hash != "SABnzbd_nzo_b" || d.Client != settings.UsenetSABnzbd || !d.Usenet() || d.SeedRatio != nil ||
			d.SeedTimeMinutes != nil || !d.LimitsSet || !d.Requested {
			t.Errorf("download = %+v", d)
		}
		if _, ok, _ := f.svc.storedSource(ctx, d.ID); ok {
			t.Error("a Usenet download's .nzb was kept as a torrent source")
		}
		if len(f.mem.torrents) != 0 {
			t.Error("the torrent client was given a Usenet release")
		}
		// The Queue shows the job's live state from the Usenet client.
		items, cerr, err := f.svc.Queue(ctx)
		if err != nil || cerr != nil || len(items) != 1 || items[0].Torrent == nil || items[0].Torrent.Progress != 0.25 {
			t.Errorf("queue = %+v, %v, %v", items, cerr, err)
		}
		// Without a Usenet client, a Usenet grab fails plainly; a torrent one
		// doesn't need it.
		f.ucfg = settings.UsenetClient{}
		err = f.svc.Grab(ctx, grab.Grabbed{Release: indexer.Release{Protocol: indexer.ProtocolUsenet}, Version: f.mv})
		if !errors.Is(err, ErrNoUsenetClient) {
			t.Errorf("no Usenet client: %v", err)
		}
		f.grab(t, hashA)
	})
}

// A torrent client isn't needed for Usenet downloads, and a move between
// torrent clients doesn't hold them up.
func TestUsenetCheckImports(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newUFix(t, db)
		d := f.grabUsenet(t, false)
		f.cfg = settings.DownloadClient{} // no torrent client at all
		if msg, quiet, err := f.svc.Check(ctx); err != nil || msg != "1 downloading on Usenet." || !quiet {
			t.Fatalf("check = %q, quiet %v, %v", msg, quiet, err)
		}
		// Downloaded, being repaired: still waiting.
		f.news.jobs[d.Hash].State, f.news.jobs[d.Hash].Progress = torrent.Repairing, 0.99
		if _, _, err := f.svc.Check(ctx); err != nil || len(f.imp.reqs) != 0 {
			t.Fatalf("imported while repairing: %v", err)
		}
		f.news.finish(d.Hash, "/mnt/downloads/usenet/reelkeeper-movies/Ronin/Ronin.mkv")
		msg, quiet, err := f.svc.Check(ctx)
		if err != nil || !strings.Contains(msg, "1 imported") || quiet {
			t.Fatalf("check = %q, quiet %v, %v", msg, quiet, err)
		}
		r := f.imp.reqs[0]
		if r.Source != "/mnt/downloads/usenet/reelkeeper-movies/Ronin/Ronin.mkv" || r.ClientName != "SABnzbd" {
			t.Errorf("import request = %+v", r)
		}
		if got := f.download(t, db, d.Hash); got.Status != StatusImported {
			t.Errorf("status = %s", got.Status)
		}
		// Imported isn't removed by Check: that's cleanup's (with dry run).
		if len(f.news.removed) != 0 {
			t.Error("Check removed the job")
		}
	})
}

func TestUsenetCheckFailedAndGone(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newUFix(t, db)
		failed := f.grabUsenet(t, false)
		gone := f.grabUsenet(t, false)
		f.news.jobs[failed.Hash].State, f.news.jobs[failed.Hash].Message = torrent.Failed, "Aborted, cannot be completed"
		delete(f.news.jobs, gone.Hash)
		msg, _, err := f.svc.Check(ctx)
		if err != nil || !strings.Contains(msg, "1 failed in SABnzbd") || !strings.Contains(msg, "1 removed outside Reelkeeper") {
			t.Fatalf("check = %q, %v", msg, err)
		}
		if f.download(t, db, failed.Hash).Status != StatusDownloading || f.download(t, db, gone.Hash).Status != StatusRemoved {
			t.Error("statuses")
		}
	})
}

func TestUsenetCleanup(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newUFix(t, db)
		done := f.grabUsenet(t, false)
		failed := f.grabUsenet(t, false)
		dupe := f.grabUsenet(t, false)
		f.news.finish(done.Hash, "/mnt/downloads/usenet/reelkeeper-movies/Ronin/Ronin.mkv")
		if _, _, err := f.svc.Check(ctx); err != nil {
			t.Fatal(err)
		}
		f.news.jobs[failed.Hash].State, f.news.jobs[failed.Hash].Message = torrent.Failed, "Aborted, cannot be completed - https://sabnzbd.org/not-complete"
		f.news.jobs[dupe.Hash].State, f.news.jobs[dupe.Hash].Message = torrent.Failed, "Duplicate NZB"
		f.cfg = settings.DownloadClient{} // no torrent client: cleanup still runs for Usenet

		// Dry run first: nothing removed, both recorded once.
		f.clean.Mode = settings.CleanupDryRun
		for range 2 {
			if _, _, err := f.svc.Cleanup(ctx); err != nil {
				t.Fatal(err)
			}
		}
		evs, _ := f.svc.CleanupEvents(ctx, 0, 50)
		if len(f.news.removed) != 0 || len(evs) != 2 {
			t.Fatalf("dry run: removed %v, events %+v", f.news.removed, evs)
		}

		f.clean.Mode = settings.CleanupOn
		msg, _, err := f.svc.Cleanup(ctx)
		if err != nil || !strings.Contains(msg, "2 removed from SABnzbd") || !strings.Contains(msg, "1 kept") {
			t.Fatalf("cleanup = %q, %v", msg, err)
		}
		// Both removed with their files; the imported one is done, the failed
		// one blocklisted and searched again. The duplicate stays.
		if !slices.Contains(f.news.deleted, done.Hash) || !slices.Contains(f.news.deleted, failed.Hash) || slices.Contains(f.news.removed, dupe.Hash) {
			t.Errorf("removed = %v, deleted %v", f.news.removed, f.news.deleted)
		}
		if got := f.download(t, db, done.Hash); got.Status != StatusSeeded || !strings.Contains(got.Message, "nothing to seed") {
			t.Errorf("imported = %s %q", got.Status, got.Message)
		}
		if got := f.download(t, db, failed.Hash); got.Status != StatusRemoved || !strings.Contains(got.Message, "SABnzbd couldn't download it") {
			t.Errorf("failed = %s %q", got.Status, got.Message)
		}
		blocked, err := f.svc.Blocklisted(ctx, f.mv.Title.ID)
		if err != nil || !blocked("Ronin.1998.1080p.BluRay.x264-NZB", "") {
			t.Errorf("the failed release isn't blocklisted: %v", err)
		}
		if len(f.again) != 1 || f.again[0] != f.mv.Version.ID {
			t.Errorf("searched again = %v", f.again)
		}
	})
}

func TestUsenetPauseResumeRemove(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newUFix(t, db)
		d := f.grabUsenet(t, false)
		if err := f.svc.Pause(ctx, d.ID); err != nil || !slices.Equal(f.news.paused, []string{d.Hash}) {
			t.Fatalf("pause: %v, %v", err, f.news.paused)
		}
		if err := f.svc.Resume(ctx, d.ID); err != nil || f.news.jobs[d.Hash].State != torrent.Downloading {
			t.Fatalf("resume: %v", err)
		}
		if _, err := f.svc.Remove(ctx, d.ID, RemoveOptions{}); err != nil || !slices.Equal(f.news.deleted, []string{d.Hash}) {
			t.Fatalf("remove: %v, %v", err, f.news.deleted)
		}
		if len(f.mem.removed) != 0 {
			t.Error("the torrent client was asked to remove a Usenet job")
		}
	})
}

// Changing the torrent client never touches Usenet downloads: they don't
// move, and aren't "left seeding" in the old client.
func TestTorrentClientChangeLeavesUsenetAlone(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newUFix(t, db)
		d := f.grabUsenet(t, false)
		f.news.finish(d.Hash, "/mnt/downloads/usenet/reelkeeper-movies/Ronin/Ronin.mkv")
		if _, _, err := f.svc.Check(ctx); err != nil {
			t.Fatal(err)
		}
		cands, err := f.svc.candidates(ctx)
		if err != nil || len(cands) != 0 {
			t.Errorf("move candidates = %+v, %v", cands, err)
		}
		if _, err := f.svc.ClientChanged(ctx, "qBittorrent", "Transmission"); err != nil {
			t.Fatal(err)
		}
		if got := f.download(t, db, d.Hash); got.Status != StatusImported {
			t.Errorf("a Usenet download was closed by a torrent client change: %s %q", got.Status, got.Message)
		}
		seeding, err := f.svc.Seeding(ctx)
		if err != nil || len(seeding) != 0 {
			t.Errorf("a Usenet download listed as seeding: %+v, %v", seeding, err)
		}
	})
}

// The free-space check allows for unpacking: about twice the release.
func TestUsenetRoom(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newUFix(t, db)
		f.clean.KeepFreeGB = 10
		f.news.free = 30 << 30 // 30 GB free; an 8 GB release needs 16 while unpacking
		f.grabUsenet(t, false)
		// Now 8 GB is promised to the job in progress (6 left + room to
		// unpack 8): 30 - 14 - 16 < 10.
		err := f.svc.Grab(context.Background(), grab.Grabbed{
			Release: indexer.Release{Release: core.Release{Title: "Ronin.1998.2160p.WEB-DL-NZB", Size: 8 << 30},
				IndexerID: f.ixID, Protocol: indexer.ProtocolUsenet},
			Version: f.mv,
		})
		if !errors.Is(err, grab.ErrNotEnoughSpace) || !strings.Contains(err.Error(), "while it unpacks") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestUsenetTestConnection(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newUFix(t, db)
		if v, w, err := f.svc.TestUsenet(context.Background(), f.ucfg); err != nil || v != "5.1.3" || len(w) != 0 {
			t.Errorf("test = %q %v %v", v, w, err)
		}
		if _, _, err := f.svc.TestUsenet(context.Background(), settings.UsenetClient{}); !errors.Is(err, ErrNoUsenetClient) {
			t.Errorf("not set up: %v", err)
		}
	})
}

// The settings pick the client: SABnzbd unless it's NZBGet, with the login
// each takes. The connection test works through either, and refuses what
// breaks Reelkeeper.
func TestNewUsenetPicksByType(t *testing.T) {
	if c, ok := newUsenet(settings.UsenetClient{URL: "http://x", APIKey: "k"}).(*SABnzbd); !ok || c.APIKey != "k" || c.Name() != "SABnzbd" {
		t.Errorf("no type: %#v", c)
	}
	n, ok := newUsenet(settings.UsenetClient{Type: settings.UsenetNZBGet, URL: "http://x", Username: "u", Password: "p"}).(*NZBGet)
	if !ok || n.Username != "u" || n.Password != "p" || n.URL != "http://x" || n.Name() != "NZBGet" {
		t.Errorf("nzbget: %#v", n)
	}
}

func TestUsenetTestConnectionNZBGet(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		nz := newFakeNZBGet(t)
		svc := newUFix(t, db).svc
		svc.NewUsenet = newUsenet
		cfg := settings.UsenetClient{Type: settings.UsenetNZBGet, URL: nz.srv.URL, Username: nzbgetUser, Password: nzbgetPass}
		if v, w, err := svc.TestUsenet(context.Background(), cfg); err != nil || v != "26.3" || len(w) != 0 {
			t.Errorf("test = %q %v %v", v, w, err)
		}
		cfg.Password = "wrong"
		if _, _, err := svc.TestUsenet(context.Background(), cfg); !errors.Is(err, ErrLogin) {
			t.Errorf("wrong password: %v", err)
		}
		cfg.Password = nzbgetPass
		nz.mu.Lock()
		nz.answers["config"] = `{"version":"1.1","id":1,"result":[{"Name":"KeepHistory","Value":"0"}]}`
		nz.mu.Unlock()
		if _, _, err := svc.TestUsenet(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "KeepHistory") {
			t.Errorf("no history: %v", err)
		}
	})
}

// The adapter refuses everything a move between clients would ask.
func TestUsenetAdapterRefusesMoves(t *testing.T) {
	a := usenetAdapter{newMemUsenet()}
	ctx := context.Background()
	if _, err := a.Source(ctx, "c", "h"); !errors.Is(err, errUsenetNoMove) {
		t.Error("Source")
	}
	if err := a.Recheck(ctx, "c", "h"); !errors.Is(err, errUsenetNoMove) {
		t.Error("Recheck")
	}
	if _, err := a.Add(ctx, Add{Magnet: "magnet:?x"}); err == nil {
		t.Error("a magnet link was accepted by a Usenet client")
	}
}

// removeCompletedFolder is the only place Reelkeeper deletes a Usenet
// download's files; it never follows a path outside the category folder.
func TestRemoveCompletedFolderStaysInside(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "precious")
	mustMkdir(t, outside)
	if err := removeCompletedFolder(filepath.Join(root, "reelkeeper-movies", "..", "precious"), "reelkeeper-movies"); err == nil {
		t.Error("a path climbing out of the category folder was accepted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("deleted a folder outside the category folder")
	}
}

// Each protocol's indexers are searched while a client for it is set up;
// with no client at all, torrent indexers still are (dry run).
func TestSearchable(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newUFix(t, db)
		qbit, sab := f.cfg, f.ucfg
		for _, c := range []struct {
			torrent, usenet       bool
			wantTorrent, wantNews bool
		}{
			{true, true, true, true},
			{true, false, true, false},
			{false, true, false, true},
			{false, false, true, false},
		} {
			f.cfg, f.ucfg = settings.DownloadClient{}, settings.UsenetClient{}
			if c.torrent {
				f.cfg = qbit
			}
			if c.usenet {
				f.ucfg = sab
			}
			if got := f.svc.Searchable(indexer.ProtocolTorrent); got != c.wantTorrent {
				t.Errorf("torrent client %v, Usenet client %v: torrent indexers searched = %v", c.torrent, c.usenet, got)
			}
			if got := f.svc.Searchable(indexer.ProtocolUsenet); got != c.wantNews {
				t.Errorf("torrent client %v, Usenet client %v: Usenet indexers searched = %v", c.torrent, c.usenet, got)
			}
		}
	})
}
