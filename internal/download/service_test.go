package download

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// memClient is an in-memory download client.
type memClient struct {
	name      string // what Name answers; "qBittorrent" when empty
	torrents  map[string]*Torrent
	removed   []string
	addErr    error
	lifted    []string            // hashes whose share limits were lifted
	trackers  map[string][]string // tracker messages by hash
	savePath  string              // the last Add's save path
	manualTMM []string            // hashes moved with automatic management off
	tags      []string            // the last Add's tags
	topped    []string            // hashes moved to the top of the queue
	topErr    error               // what TopPriority answers
	// calls records, in order, every call that changes a torrent's running
	// state: "lift <hash>" (which also starts it, as qBittorrent does),
	// "stop <hash>", "start <hash>" and "top <hash>".
	calls []string
}

// setState changes a torrent's state, if it exists.
func (m *memClient) setState(hash string, state torrent.State) {
	if t := m.torrents[hash]; t != nil {
		t.State = state
	}
}

func (m *memClient) Stop(_ context.Context, _ string, hashes []string) error {
	for _, h := range hashes {
		m.calls = append(m.calls, "stop "+h)
		m.setState(h, torrent.Paused)
	}
	return nil
}

func (m *memClient) Start(_ context.Context, _ string, hashes []string) error {
	for _, h := range hashes {
		m.calls = append(m.calls, "start "+h)
		m.setState(h, torrent.Downloading)
	}
	return nil
}

func (m *memClient) TopPriority(_ context.Context, _ string, hashes []string) error {
	if m.topErr != nil {
		return m.topErr
	}
	m.topped = append(m.topped, hashes...)
	for _, h := range hashes {
		m.calls = append(m.calls, "top "+h)
	}
	return nil
}

func (m *memClient) NoShareLimits(_ context.Context, _ string, hashes []string) error {
	m.lifted = append(m.lifted, hashes...)
	// Lifting the limits starts the torrents, as the real client does.
	for _, h := range hashes {
		m.calls = append(m.calls, "lift "+h)
		if t := m.torrents[h]; t != nil && t.Paused() {
			t.State = torrent.Downloading
		}
	}
	return nil
}

func (m *memClient) TrackerMessages(_ context.Context, _, hash string) ([]string, error) {
	return m.trackers[hash], nil
}

// MoveCategory records which torrents had automatic management turned
// off, so a test can check it happened before the category changed.
func (m *memClient) MoveCategory(_ context.Context, from, to string, hashes []string) error {
	for _, h := range hashes {
		if t := m.torrents[h]; t != nil && t.Category == from {
			m.manualTMM = append(m.manualTMM, h)
			t.Category = to
		}
	}
	return nil
}

// The parts of Client that only moving a torrent to another client uses are
// tested with simClient (move_test.go).
func (m *memClient) Source(_ context.Context, _, hash string) (Source, error) {
	return Source{Magnet: "magnet:?xt=urn:btih:" + hash}, nil
}
func (m *memClient) Layout(context.Context, string, string) (Layout, error)  { return Layout{}, nil }
func (m *memClient) Recheck(context.Context, string, string) error           { return nil }
func (m *memClient) SetFolder(context.Context, string, string, string) error { return nil }

func (m *memClient) Name() string {
	if m.name == "" {
		return "qBittorrent"
	}
	return m.name
}
func (m *memClient) Version(context.Context) (string, error)              { return "v5.2.2", nil }
func (m *memClient) EnsureCategory(context.Context, string, string) error { return nil }
func (m *memClient) Add(_ context.Context, a Add) (string, error) {
	if m.addErr != nil {
		return "", m.addErr
	}
	h, err := MagnetHash(a.Magnet)
	if err != nil {
		return "", err
	}
	m.savePath = a.SavePath
	m.tags = a.Tags
	m.torrents[h] = &Torrent{Hash: h, Category: a.Category, Progress: 0.2, ContentPath: "/downloads/" + h}
	return h, nil
}
func (m *memClient) Torrents(_ context.Context, cat string) ([]Torrent, error) {
	var out []Torrent
	for _, t := range m.torrents {
		if t.Category == cat {
			out = append(out, *t)
		}
	}
	return out, nil
}
func (m *memClient) Remove(_ context.Context, _ string, hashes []string, _ bool) error {
	for _, h := range hashes {
		delete(m.torrents, h)
		m.removed = append(m.removed, h)
	}
	return nil
}

type fakeFetcher struct{ ratio float64 }

func (f fakeFetcher) Fetch(_ context.Context, r indexer.Release) (indexer.Fetched, error) {
	return indexer.Fetched{Magnet: "magnet:?xt=urn:btih:" + r.InfoHash}, nil
}
func (f fakeFetcher) Get(context.Context, int64) (*indexer.Indexer, error) {
	mins := 60
	return &indexer.Indexer{SeedRatio: &f.ratio, SeedTimeMinutes: &mins}, nil
}

type fakeImporter struct {
	err    error
	reqs   []library.ImportRequest
	tvReqs []library.EpisodeImportRequest
	tvRes  library.EpisodeImportResult
}

func (f *fakeImporter) ImportEpisodes(_ context.Context, r library.EpisodeImportRequest) (library.EpisodeImportResult, error) {
	f.tvReqs = append(f.tvReqs, r)
	return f.tvRes, f.err
}

func (f *fakeImporter) ImportMovie(_ context.Context, r library.ImportRequest) (library.ImportResult, error) {
	f.reqs = append(f.reqs, r)
	if f.err != nil {
		return library.ImportResult{}, f.err
	}
	return library.ImportResult{Path: "Ronin (1998)/Ronin (1998) [Bluray-1080p].mkv", FullPath: "/movies/Ronin (1998)/Ronin (1998) [Bluray-1080p].mkv", Replaced: []string{".reelkeeper-replaced/x/old.mkv"}}, nil
}

type dfix struct {
	ixID int64
	svc  *Service
	mem  *memClient
	imp  *fakeImporter
	hist *grab.History
	mv   library.MovieVersion
	cfg  settings.DownloadClient
}

func newDFix(t *testing.T, db *store.Store) *dfix {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	root := &library.RootFolder{Path: "/movies", MediaType: library.Movie, DefaultProfileID: 1, CreatedAt: now}
	title := &library.Title{MediaType: library.Movie, TMDBID: 8195, Title: "Ronin", Year: 1998, CreatedAt: now, Genres: "[]", AltTitles: "[]"}
	for _, m := range []any{root, title} {
		if _, err := db.DB.NewInsert().Model(m).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	v := &library.Version{TitleID: title.ID, ProfileID: 1, RootFolderID: root.ID, Monitored: true, Upgrades: true, CreatedAt: now}
	ix := &indexer.Indexer{Name: "Tracker", Kind: "knaben", Enabled: true, Settings: "{}", Categories: "[]", CreatedAt: now, UpdatedAt: now}
	for _, m := range []any{v, ix} {
		if _, err := db.DB.NewInsert().Model(m).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	f := &dfix{mem: &memClient{torrents: map[string]*Torrent{}}, imp: &fakeImporter{}, hist: grab.NewHistory(db),
		ixID: ix.ID, mv: library.MovieVersion{Version: *v, Title: *title}, cfg: settings.DownloadClient{URL: "http://qbit", Username: "a"}}
	f.svc = NewService(db, func() settings.DownloadClient { return f.cfg }, fakeFetcher{ratio: 1.5}, f.imp, f.hist, slog.New(slog.DiscardHandler))
	f.svc.NewClient = func(settings.DownloadClient) Client { return f.mem }
	f.svc.exists = func(string) bool { return true } // fake paths: treat downloads as on disk
	// Free space unknown: the real disk running tests must not decide grabs
	// (tests about space set their own).
	f.svc.freeSpace = func(string) int64 { return -1 }
	return f
}

func (f *dfix) grab(t *testing.T, hash string) {
	t.Helper()
	err := f.svc.Grab(context.Background(), grab.Grabbed{
		Release: indexer.Release{Release: core.Release{Title: "Ronin.1998.1080p.BluRay.x264-GRP", InfoHash: hash, Size: 8 << 30, MinimumRatio: 2},
			IndexerID: f.ixID, Indexer: "Tracker"},
		Version: f.mv, Quality: "Bluray-1080p", Score: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
}

const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestGrabTracksTheDownload(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		f.grab(t, hashA)
		var d Download
		if err := db.DB.NewSelect().Model(&d).Where("hash = ?", hashA).Scan(ctx); err != nil {
			t.Fatal(err)
		}
		// The tracker's own rule for the release (ratio 2) beats the
		// indexer's setting (1.5); the seed time comes from the indexer.
		if d.Status != StatusDownloading || d.Category != settings.MovieCategory || d.IndexerID == nil ||
			*d.SeedRatio != 2 || d.SeedTimeMinutes == nil || *d.SeedTimeMinutes != 60 {
			t.Errorf("download = %+v", d)
		}
		active, _ := f.svc.ActiveVersions(ctx)
		if !active[f.mv.Version.ID] {
			t.Error("the version isn't marked as downloading")
		}
		items, cerr, err := f.svc.Queue(ctx)
		if err != nil || cerr != nil || len(items) != 1 || items[0].Torrent == nil || items[0].Torrent.Progress != 0.2 {
			t.Errorf("queue = %+v, %v, %v", items, cerr, err)
		}

		// No download client: a clear error.
		f.cfg = settings.DownloadClient{}
		if err := f.svc.Grab(ctx, grab.Grabbed{Version: f.mv}); !errors.Is(err, ErrNoClient) {
			t.Errorf("no client: %v", err)
		}
	})
}

// A grab that came from a request (Seerr) is tagged and moved to the top of
// the client's queue; any other isn't. A failure to reorder the queue never
// fails the grab.
func TestGrabRequestedGoesToTheTop(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested bool
		topErr    error
		wantTag   bool
		wantTop   bool
	}{
		{"ordinary grab", false, nil, false, false},
		{"requested", true, nil, true, true},
		{"requested, queue refuses", true, errors.New("queue broke"), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, db *store.Store) {
				f := newDFix(t, db)
				f.mem.topErr = tc.topErr
				err := f.svc.Grab(context.Background(), grab.Grabbed{
					Release:   indexer.Release{Release: core.Release{Title: "Ronin.1998.1080p.BluRay.x264-GRP", InfoHash: hashA, Size: 8 << 30}, IndexerID: f.ixID, Indexer: "Tracker"},
					Version:   f.mv,
					Requested: tc.requested,
				})
				if err != nil {
					t.Fatalf("the grab failed: %v", err)
				}
				if has := slices.Contains(f.mem.tags, "requested"); has != tc.wantTag {
					t.Errorf("tags = %v, want requested tag: %v", f.mem.tags, tc.wantTag)
				}
				if tc.wantTop && !slices.Equal(f.mem.topped, []string{hashA}) {
					t.Errorf("moved to the top = %v, want [%s]", f.mem.topped, hashA)
				}
				if !tc.wantTop && len(f.mem.topped) > 0 {
					t.Errorf("moved to the top = %v, want none", f.mem.topped)
				}
				// Tracked either way.
				if n, _ := db.DB.NewSelect().Model((*Download)(nil)).Where("hash = ?", hashA).Count(context.Background()); n != 1 {
					t.Errorf("download rows = %d, want 1", n)
				}
				// Reelkeeper's own record says it was a request too (Deluge has no
				// tags to hold it, and a move reads it from here).
				if d := f.download(t, db, hashA); d.Requested != tc.requested {
					t.Errorf("requested on the record = %v, want %v", d.Requested, tc.requested)
				}
			})
		})
	}
}

// The same torrent grabbed again later is a new grab: whether it is a request
// follows the new one.
func TestRegrabReplacesTheRequestedNote(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newDFix(t, db)
		grabAs := func(requested bool) {
			t.Helper()
			err := f.svc.Grab(context.Background(), grab.Grabbed{
				Release:   indexer.Release{Release: core.Release{Title: "Ronin.1998.1080p.BluRay.x264-GRP", InfoHash: hashA, Size: 8 << 30}, IndexerID: f.ixID, Indexer: "Tracker"},
				Version:   f.mv,
				Requested: requested,
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		grabAs(true)
		if !f.download(t, db, hashA).Requested {
			t.Fatal("a request wasn't noted")
		}
		grabAs(false)
		if f.download(t, db, hashA).Requested {
			t.Error("the old request note survived a new, ordinary grab")
		}
	})
}

func TestCheckImports(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		f.grab(t, hashA)
		if msg, quiet, err := f.svc.Check(ctx); err != nil || msg != "1 downloading." || !quiet {
			t.Fatalf("check = %q, quiet %v, %v", msg, quiet, err)
		}

		// Finished, but the import fails: kept in the queue with the reason.
		f.mem.torrents[hashA].Progress = 1
		f.imp.err = errors.New("the movie's folder is read-only")
		msg, quiet, err := f.svc.Check(ctx)
		if err != nil || !strings.Contains(msg, "1 failed to import") || quiet {
			t.Fatalf("check = %q, quiet %v, %v", msg, quiet, err)
		}
		if f.imp.reqs[0].Source != "/downloads/"+hashA || f.imp.reqs[0].ReleaseName != "Ronin.1998.1080p.BluRay.x264-GRP" {
			t.Errorf("import request = %+v", f.imp.reqs[0])
		}
		items, _, _ := f.svc.Queue(ctx)
		if len(items) != 1 || items[0].Download.Status != StatusImportFailed || !strings.Contains(items[0].Download.Message, "read-only") {
			t.Fatalf("queue = %+v", items)
		}
		// A failed import isn't retried on its own, but Retry works.
		if _, _, err := f.svc.Check(ctx); err != nil || len(f.imp.reqs) != 1 {
			t.Error("a failed import was retried without being asked")
		}
		f.imp.err = nil
		if err := f.svc.RetryImport(ctx, items[0].Download.ID); err != nil {
			t.Fatal(err)
		}
		items, _, _ = f.svc.Queue(ctx)
		if len(items) != 0 {
			t.Errorf("imported download still queued: %+v", items)
		}
		evs, _ := f.hist.List(ctx, grab.ListOptions{})
		if len(evs) != 2 || evs[0].Event != grab.EventImported || evs[1].Event != grab.EventImportFailed ||
			!strings.Contains(evs[0].Message, "moved to .reelkeeper-replaced") {
			t.Errorf("history = %+v", evs)
		}
		// The torrent keeps seeding: nothing was removed.
		if len(f.mem.removed) != 0 {
			t.Error("importing removed the torrent")
		}
	})
}

func TestRemovedOutsideAndRemove(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		f.grab(t, hashA)
		delete(f.mem.torrents, hashA) // someone removed it in qBittorrent
		if msg, quiet, _ := f.svc.Check(ctx); !strings.Contains(msg, "removed outside Reelkeeper") || quiet {
			t.Errorf("check = %q", msg)
		}
		if active, _ := f.svc.ActiveVersions(ctx); active[f.mv.Version.ID] {
			t.Error("a removed download still counts as active")
		}

		const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		f.grab(t, hashB)
		items, _, _ := f.svc.Queue(ctx)
		if _, err := f.svc.Remove(ctx, items[0].Download.ID, RemoveOptions{Blocklist: true, Reason: "Fake release"}); err != nil {
			t.Fatal(err)
		}
		if len(f.mem.removed) != 1 || f.mem.removed[0] != hashB {
			t.Errorf("removed %v", f.mem.removed)
		}
		blocked, _ := f.svc.Blocklisted(ctx, f.mv.Title.ID)
		if !blocked("anything", hashB) || !blocked("ronin.1998.1080p.bluray.x264-grp", "") || blocked("Other", "") {
			t.Error("blocklist check")
		}
		page, _ := f.svc.Blocklist(ctx, BlocklistQuery{})
		list := page.Items
		// Removing a download yourself blocks it for good.
		if len(list) != 1 || list[0].Reason != "Fake release" || list[0].Rule != BlockRemoved || !list[0].Permanent() || list[0].MediaType != "movie" {
			t.Fatalf("blocklist = %+v", list)
		}
		if err := f.svc.Unblock(ctx, list[0].ID); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.Unblock(ctx, list[0].ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("second unblock: %v", err)
		}
	})
}

func TestRemoveForTitle(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		f.grab(t, hashA)
		if err := f.svc.RemoveForTitle(ctx, f.mv.Title.ID); err != nil {
			t.Fatal(err)
		}
		if len(f.mem.removed) != 1 {
			t.Errorf("removed %v", f.mem.removed)
		}
		if active, _ := f.svc.ActiveVersions(ctx); len(active) != 0 {
			t.Error("still active")
		}
	})
}

// Grabbing a torrent again after removing it tracks it again (it once
// stayed "removed", so the finished download was never imported).
func TestGrabAgainAfterRemove(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		f.grab(t, hashA)
		items, _, _ := f.svc.Queue(ctx)
		if _, err := f.svc.Remove(ctx, items[0].Download.ID, RemoveOptions{}); err != nil {
			t.Fatal(err)
		}
		f.grab(t, hashA)
		items, _, _ = f.svc.Queue(ctx)
		if len(items) != 1 || items[0].Download.Status != StatusDownloading || items[0].Download.Message != "" {
			t.Fatalf("queue after grabbing again = %+v", items)
		}
		f.mem.torrents[hashA].Progress = 1
		if msg, _, err := f.svc.Check(ctx); err != nil || !strings.Contains(msg, "1 imported") {
			t.Errorf("check = %q, %v", msg, err)
		}
	})
}

type failingClient struct{ *memClient }

func (failingClient) Torrents(context.Context, string) ([]Torrent, error) {
	return nil, errors.New("connection refused")
}

func TestNotifications(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		var got []notify.Event
		f.svc.SetNotifier(func(_ context.Context, ev notify.Event) { got = append(got, ev) })
		f.grab(t, hashA)
		f.mem.torrents[hashA].Progress = 1
		f.imp.err = errors.New("no space left")
		if _, _, err := f.svc.Check(ctx); err != nil {
			t.Fatal(err)
		}
		f.imp.err = nil
		items, _, _ := f.svc.Queue(ctx)
		if err := f.svc.RetryImport(ctx, items[0].Download.ID); err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Kind != notify.KindImportFailed || got[1].Kind != notify.KindImported || got[1].Path == "" {
			t.Fatalf("events = %+v", got)
		}
		// The download client unreachable: told once, on the third check.
		f.grab(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
		f.svc.NewClient = func(settings.DownloadClient) Client { return failingClient{f.mem} }
		for range 5 {
			_, _, _ = f.svc.Check(ctx)
		}
		n := 0
		for _, ev := range got {
			if ev.Kind == notify.KindClientUnreachable {
				n++
			}
		}
		if n != 1 {
			t.Errorf("client-unreachable sent %d times", n)
		}
	})
}

func TestQueueCounts(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		if c, err := f.svc.QueueCounts(ctx); err != nil || c != (QueueCount{}) {
			t.Fatalf("empty = %+v, %v", c, err)
		}
		hashes := []string{hashA, strings.Repeat("b", 40), strings.Repeat("c", 40)}
		for _, h := range hashes {
			f.grab(t, h)
		}
		set := func(hash, status string) {
			t.Helper()
			if _, err := db.DB.NewUpdate().Model((*Download)(nil)).Set("status = ?", status).Where("hash = ?", hash).Exec(ctx); err != nil {
				t.Fatal(err)
			}
		}
		set(hashes[1], StatusImportFailed)
		set(hashes[2], StatusImported)
		// One of the two left is TV.
		if _, err := db.DB.NewUpdate().Model((*Download)(nil)).Set("category = ?", settings.TVCategory).Where("hash = ?", hashes[1]).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		c, err := f.svc.QueueCounts(ctx)
		if err != nil || c != (QueueCount{Movies: 1, TV: 1, NeedsAttention: 1}) {
			t.Errorf("QueueCounts = %+v, %v; want 1 movie, 1 TV, 1 needing attention", c, err)
		}
		// It must agree with Queue.
		items, _, _ := f.svc.Queue(ctx)
		if len(items) != c.Movies+c.TV {
			t.Errorf("Queue lists %d, counts say %+v", len(items), c)
		}
		if n, err := f.svc.QueueSize(ctx); err != nil || n != len(items) || n != 2 {
			t.Errorf("QueueSize = %d, %v; Queue lists %d", n, err, len(items))
		}
	})
}

// A finished download Reelkeeper can't see waits instead of failing: up to
// MissingGrace while the folder looks fine, and as long as it takes while
// the whole download folder looks unmounted (reported once).
func TestFinishedButNotVisible(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		now := time.Date(2026, 10, 10, 14, 0, 0, 0, time.UTC)
		f.svc.now = func() time.Time { return now }
		var sent []notify.Event
		f.svc.SetNotifier(func(_ context.Context, ev notify.Event) { sent = append(sent, ev) })
		visible := map[string]bool{}
		f.svc.exists = func(p string) bool { return visible[p] }
		f.grab(t, hashA)
		f.mem.torrents[hashA].Progress = 1
		path := "/downloads/" + hashA

		// The whole folder is unseen: wait, say so once, never fail.
		for range 3 {
			msg, _, err := f.svc.Check(ctx)
			if err != nil || !strings.Contains(msg, "1 finished but not visible") {
				t.Fatalf("check = %q, %v", msg, err)
			}
			now = now.Add(2 * MissingGrace)
		}
		if len(f.imp.reqs) != 0 {
			t.Fatal("imported while the files couldn't be seen")
		}
		if len(sent) != 1 || sent[0].Kind != notify.KindDownloadFolderMissing || !strings.Contains(sent[0].Message, "/downloads") {
			t.Fatalf("notifications = %+v", sent)
		}
		items, _, _ := f.svc.Queue(ctx)
		if items[0].Download.Status != StatusDownloading || !strings.Contains(items[0].Download.Message, "isn't mounted") {
			t.Fatalf("queue = %+v", items[0].Download)
		}

		// The folder comes back: it imports, and a later problem would be
		// reported again.
		visible[path] = true
		if msg, quiet, _ := f.svc.Check(ctx); !strings.Contains(msg, "1 imported") || quiet {
			t.Fatalf("after the folder came back: %q", msg)
		}
		if f.svc.vis.warned {
			t.Error("the folder warning should reset once it's visible")
		}

		// One download missing while others are visible: waits up to
		// MissingGrace, then fails with the importer's reason.
		const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		f.grab(t, hashB)
		f.mem.torrents[hashB].Progress = 1
		f.imp.err = errors.New("the download isn't where qBittorrent says")
		if msg, quiet, _ := f.svc.Check(ctx); !strings.Contains(msg, "1 finished but not visible") || quiet {
			t.Fatalf("one missing: %q", msg)
		}
		items, _, _ = f.svc.Queue(ctx)
		if len(items) != 1 || !strings.Contains(items[0].Download.Message, "keeps checking until") {
			t.Fatalf("queue = %+v", items)
		}
		now = now.Add(MissingGrace)
		if msg, _, _ := f.svc.Check(ctx); !strings.Contains(msg, "1 failed to import") {
			t.Fatalf("after the grace period: %q", msg)
		}
		folder := 0
		for _, ev := range sent {
			if ev.Kind == notify.KindDownloadFolderMissing {
				folder++
			}
		}
		if folder != 1 || sent[len(sent)-1].Kind != notify.KindImportFailed {
			t.Errorf("notifications = %+v", sent)
		}
	})
}

func TestCheckIsQuietWhenNothingIsDownloading(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newDFix(t, db)
		msg, quiet, err := f.svc.Check(context.Background())
		if err != nil || msg != "Nothing is downloading." || !quiet {
			t.Errorf("check = %q, quiet %v, %v", msg, quiet, err)
		}
	})
}

// setTorrent puts the fixture's torrent in a state, with some progress.
func (f *dfix) setTorrent(hash string, state torrent.State, progress float64) {
	tor := f.mem.torrents[hash]
	tor.State, tor.Progress = state, progress
}

// Pause, Resume and Move to top: what each allows, what each refuses and
// says, what History records, and that the download's status never changes.
func TestPauseResumeAndMoveToTop(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		withCleanup(f, settings.CleanupOn) // so the grab lifts the seeding limits
		f.grab(t, hashA)
		f.setTorrent(hashA, torrent.Downloading, 0.4)
		id := f.download(t, db, hashA).ID
		f.mem.calls = nil

		check := func(what string, err error, want string) {
			t.Helper()
			switch {
			case want == "" && err != nil:
				t.Errorf("%s: %v", what, err)
			case want != "" && (err == nil || err.Error() != want):
				t.Errorf("%s: error = %v, want %q", what, err, want)
			}
		}

		// Move to top while it downloads.
		check("move to top", f.svc.MoveToTop(ctx, id), "")
		// It isn't paused, so there's nothing to resume.
		check("resume a running download", f.svc.Resume(ctx, id), "this download isn't paused")
		check("pause", f.svc.Pause(ctx, id), "")
		if got := f.mem.torrents[hashA].State; got != torrent.Paused {
			t.Errorf("state after Pause = %s", got)
		}
		check("pause twice", f.svc.Pause(ctx, id), "this download is already paused")
		check("move a paused download", f.svc.MoveToTop(ctx, id), "resume this download first")
		check("resume", f.svc.Resume(ctx, id), "")
		if got := f.mem.torrents[hashA].State; got != torrent.Downloading {
			t.Errorf("state after Resume = %s", got)
		}
		// Paused again, as if from the client's own page: Resume works on it too.
		f.setTorrent(hashA, torrent.Paused, 0.4)
		check("resume a 4.x paused download", f.svc.Resume(ctx, id), "")

		want := []string{"top " + hashA, "stop " + hashA, "start " + hashA, "start " + hashA}
		if !slices.Equal(f.mem.calls, want) {
			t.Errorf("client calls = %v, want %v", f.mem.calls, want)
		}

		// History has one event per success, in order, and nothing else new.
		evs, _ := f.hist.List(ctx, grab.ListOptions{Events: []string{grab.EventPaused, grab.EventResumed, grab.EventMovedToTop}})
		var got []string
		for i := len(evs) - 1; i >= 0; i-- {
			got = append(got, evs[i].Event+": "+evs[i].Message)
		}
		wantEvs := []string{
			"moved-to-top: Moved to the top of qBittorrent's queue by you.",
			"paused: Paused by you.",
			"resumed: Resumed by you.",
			"resumed: Resumed by you.",
		}
		if !slices.Equal(got, wantEvs) {
			t.Errorf("history = %q, want %q", got, wantEvs)
		}
		if evs[0].SourceTitle != "Ronin.1998.1080p.BluRay.x264-GRP" || evs[0].TitleID == nil || evs[0].VersionID == nil || evs[0].Quality != "Bluray-1080p" {
			t.Errorf("event fields = %+v", evs[0])
		}

		// Through all that the download stayed in the queue, unchanged.
		d := f.download(t, db, hashA)
		if d.Status != StatusDownloading || d.Message != "" {
			t.Errorf("download = %s %q", d.Status, d.Message)
		}
		// Nothing was lifted again: the grab already did it.
		if len(f.mem.lifted) != 1 {
			t.Errorf("seeding limits lifted %d times", len(f.mem.lifted))
		}
	})
}

// Nothing here is allowed once a download has finished, never was
// Reelkeeper's to pause, or has gone from the client.
func TestPauseResumeAndMoveToTopRefusals(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		f.grab(t, hashA)
		id := f.download(t, db, hashA).ID
		actions := map[string]func(context.Context, int64) error{
			"pause": f.svc.Pause, "resume": f.svc.Resume, "move to top": f.svc.MoveToTop,
		}
		refuse := func(want string) {
			t.Helper()
			for name, act := range actions {
				if err := act(ctx, id); err == nil || err.Error() != want {
					t.Errorf("%s: error = %v, want %q", name, err, want)
				}
			}
		}
		setStatus := func(status string) {
			t.Helper()
			if _, err := db.DB.NewUpdate().Model((*Download)(nil)).Set("status = ?", status).Where("id = ?", id).Exec(ctx); err != nil {
				t.Fatal(err)
			}
		}

		for name, act := range actions {
			if err := act(ctx, 999); !errors.Is(err, ErrNotFound) {
				t.Errorf("%s: unknown download: %v", name, err)
			}
		}

		// The torrent finished downloading (it's seeding or waiting to import).
		f.setTorrent(hashA, torrent.Seeding, 1)
		refuse("this download has already finished")
		f.setTorrent(hashA, torrent.SeedingPaused, 1)
		refuse("this download has already finished")
		f.setTorrent(hashA, torrent.Downloading, 0.5)

		// qBittorrent lost it.
		saved := f.mem.torrents[hashA]
		delete(f.mem.torrents, hashA)
		refuse("qBittorrent no longer has this download")
		f.mem.torrents[hashA] = saved

		// Not downloading any more.
		setStatus(StatusImportFailed)
		refuse("a failed import has nothing left to download")
		for _, st := range []string{StatusImported, StatusRemoved, StatusSeeded} {
			setStatus(st)
			refuse("this download has already finished")
		}

		// No download client.
		setStatus(StatusDownloading)
		f.cfg = settings.DownloadClient{}
		refuse(ErrNoClient.Error())

		if len(f.mem.calls) != 0 {
			t.Errorf("a refused action reached the client: %v", f.mem.calls)
		}
		if evs, _ := f.hist.List(ctx, grab.ListOptions{Events: []string{grab.EventPaused, grab.EventResumed, grab.EventMovedToTop}}); len(evs) != 0 {
			t.Errorf("refused actions wrote History: %+v", evs)
		}
	})
}

// Cleanup lifts qBittorrent's seeding limits on its first pass over a
// download, and that also starts the torrent. Pausing a download whose
// limits aren't lifted yet does it first, so no later pass resumes it.
func TestPauseSurvivesCleanup(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		f.grab(t, hashA) // cleanup isn't set up yet, so the limits aren't lifted
		if f.download(t, db, hashA).LimitsSet {
			t.Fatal("the grab lifted the limits; the test needs them not lifted")
		}
		f.setTorrent(hashA, torrent.Downloading, 0.3)
		f.mem.calls = nil

		if err := f.svc.Pause(ctx, f.download(t, db, hashA).ID); err != nil {
			t.Fatal(err)
		}
		if !f.download(t, db, hashA).LimitsSet {
			t.Error("Pause didn't record that the limits are lifted")
		}
		want := []string{"lift " + hashA, "stop " + hashA}
		if !slices.Equal(f.mem.calls, want) {
			t.Fatalf("client calls = %v, want %v", f.mem.calls, want)
		}

		// Cleanup (even on) must neither lift again nor start it.
		withCleanup(f, settings.CleanupOn)
		for range 3 {
			if _, _, err := f.svc.Cleanup(ctx); err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.svc.Check(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if !slices.Equal(f.mem.calls, want) {
			t.Errorf("client calls after cleanup = %v, want only %v", f.mem.calls, want)
		}
		if got := f.mem.torrents[hashA].State; got != torrent.Paused {
			t.Errorf("state after cleanup = %s, want it still paused", got)
		}
		// Paused means waiting, not stuck: it's still downloading, not removed.
		if d := f.download(t, db, hashA); d.Status != StatusDownloading || len(f.mem.removed) != 0 {
			t.Errorf("status = %s, removed = %v", d.Status, f.mem.removed)
		}
	})
}

// A client that refuses to lift the limits leaves the torrent running: a
// pause that cleanup could undo isn't a pause.
func TestPauseNeedsTheLimitsLifted(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		f.grab(t, hashA)
		f.setTorrent(hashA, torrent.Downloading, 0.3)
		f.svc.NewClient = func(settings.DownloadClient) Client { return liftFails{f.mem} }
		if err := f.svc.Pause(ctx, f.download(t, db, hashA).ID); err == nil {
			t.Error("paused although the limits couldn't be lifted")
		}
		if slices.Contains(f.mem.calls, "stop "+hashA) || f.download(t, db, hashA).LimitsSet {
			t.Errorf("calls = %v", f.mem.calls)
		}
	})
}

// liftFails is memClient, but qBittorrent won't change share limits.
type liftFails struct{ *memClient }

func (liftFails) NoShareLimits(context.Context, string, []string) error {
	return errors.New("qBittorrent couldn't change the torrents' seeding limits (500)")
}

// Every message the user can read names the download client in use, not
// always qBittorrent: here the client calls itself Transmission.
func TestMessagesNameTheClient(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		const hashC = "cccccccccccccccccccccccccccccccccccccccc"
		f.mem.name = "Transmission"
		withCleanup(f, settings.CleanupDryRun)
		var sent []notify.Event
		f.svc.SetNotifier(func(_ context.Context, ev notify.Event) { sent = append(sent, ev) })

		if got := f.svc.ClientName(); got != "Transmission" {
			t.Errorf("ClientName = %q", got)
		}

		// A finished download that can't be imported: the importer is told
		// which client reported the files.
		f.grab(t, hashA)
		f.mem.torrents[hashA].Progress = 1
		f.imp.err = errors.New("nope")
		if _, _, err := f.svc.Check(ctx); err != nil {
			t.Fatal(err)
		}
		if len(f.imp.reqs) != 1 || f.imp.reqs[0].ClientName != "Transmission" {
			t.Errorf("import requests = %+v", f.imp.reqs)
		}
		f.imp.err = nil

		// The torrent is gone from the client.
		saved := f.mem.torrents[hashA]
		delete(f.mem.torrents, hashA)
		err := f.svc.RetryImport(ctx, f.download(t, db, hashA).ID)
		if err == nil || err.Error() != "Transmission no longer has this download" {
			t.Errorf("RetryImport = %v", err)
		}
		f.mem.torrents[hashA] = saved

		// Moved to the top, and then removed behind the app's back.
		f.grab(t, hashB)
		f.setTorrent(hashB, torrent.Downloading, 0.5)
		if err := f.svc.MoveToTop(ctx, f.download(t, db, hashB).ID); err != nil {
			t.Fatal(err)
		}
		evs, _ := f.hist.List(ctx, grab.ListOptions{Events: []string{grab.EventMovedToTop}})
		if len(evs) != 1 || evs[0].Message != "Moved to the top of Transmission's queue by you." {
			t.Errorf("history = %+v", evs)
		}
		delete(f.mem.torrents, hashB)
		if msg, _, _ := f.svc.Check(ctx); !strings.Contains(msg, "removed outside Reelkeeper") {
			t.Errorf("check = %q", msg)
		}
		if d := f.download(t, db, hashB); d.Message != "It was removed from Transmission outside Reelkeeper." {
			t.Errorf("message = %q", d.Message)
		}

		// An imported download whose torrent is gone.
		f.mem.torrents[hashA] = saved
		if err := f.svc.RetryImport(ctx, f.download(t, db, hashA).ID); err != nil {
			t.Fatal(err)
		}
		delete(f.mem.torrents, hashA)
		if _, _, err := f.svc.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		if d := f.download(t, db, hashA); !strings.HasPrefix(d.Message, "Its torrent was removed from Transmission outside Reelkeeper") {
			t.Errorf("message = %q", d.Message)
		}

		// The client can't be reached: told once, by name.
		f.svc.NewClient = func(settings.DownloadClient) Client { return failingClient{f.mem} }
		f.grab(t, hashC)
		for range 3 {
			_, _, _ = f.svc.Check(ctx)
		}
		last := sent[len(sent)-1]
		if last.Kind != notify.KindClientUnreachable || last.Title != "Transmission" ||
			!strings.HasPrefix(last.Message, "Reelkeeper can't reach Transmission,") {
			t.Errorf("notification = %+v", last)
		}

		// No client set up.
		f.cfg = settings.DownloadClient{}
		if got := f.svc.ClientName(); got != "the download client" {
			t.Errorf("ClientName without a client = %q", got)
		}
	})
}

// Switching the download client leaves imported downloads seeding in the old
// client when the user chose to leave them (or the client was removed): they
// are marked "seeded" (done, and gone from the client), nothing is deleted,
// History says so, and cleanup never touches them again. A download still in
// the Queue isn't ClientChanged's business: it moves (see move_test.go).
func TestClientChanged(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		withCleanup(f, settings.CleanupOn)
		hashB := strings.Repeat("b", 40)
		f.grab(t, hashA)
		f.grab(t, hashB)
		// A is imported and still seeding; B is still downloading.
		f.mem.torrents[hashA].Progress = 1
		if _, _, err := f.svc.Check(ctx); err != nil {
			t.Fatal(err)
		}
		if d := f.download(t, db, hashA); d.Status != StatusImported {
			t.Fatalf("setup: A is %q", d.Status)
		}

		f.cfg = settings.DownloadClient{Type: settings.ClientTransmission, URL: "http://tm:9091"}
		n, err := f.svc.ClientChanged(ctx, "qBittorrent", "Transmission")
		if err != nil || n != 1 {
			t.Fatalf("ClientChanged = %d, %v", n, err)
		}
		const msg = "Left seeding in qBittorrent when the download client changed to Transmission; Reelkeeper no longer tracks it."
		d := f.download(t, db, hashA)
		if d.Status != StatusSeeded || d.Message != msg || d.ImportedPath == "" || d.ImportedAt == nil {
			t.Errorf("A = %q %q (imported path %q)", d.Status, d.Message, d.ImportedPath)
		}
		if b := f.download(t, db, hashB); b.Status != StatusDownloading {
			t.Errorf("a download in the Queue was changed: %q", b.Status)
		}
		// Nothing was deleted from the old client.
		if len(f.mem.removed) != 0 || f.mem.torrents[hashA] == nil {
			t.Errorf("removed %v", f.mem.removed)
		}
		// History records it.
		evs, _ := f.hist.List(ctx, grab.ListOptions{})
		if len(evs) == 0 || evs[0].Event != grab.EventRemoved || evs[0].Message != msg {
			t.Errorf("history = %+v", evs)
		}
		// The new client doesn't have it, and cleanup leaves the record alone:
		// no "removed outside Reelkeeper", no cleanup events, no seeding row.
		delete(f.mem.torrents, hashA)
		if _, _, err := f.svc.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		if d := f.download(t, db, hashA); d.Status != StatusSeeded || d.Message != msg {
			t.Errorf("after cleanup A = %q %q", d.Status, d.Message)
		}
		if evs := events(t, f.svc); len(evs) != 0 {
			t.Errorf("cleanup events: %+v", evs)
		}
		if items, err := f.svc.Seeding(ctx); err != nil || len(items) != 0 {
			t.Errorf("Seeding lists %+v, %v", items, err)
		}
		// Calling it again finds nothing more.
		if n, err := f.svc.ClientChanged(ctx, "qBittorrent", "Transmission"); err != nil || n != 0 {
			t.Errorf("again = %d, %v", n, err)
		}
	})
}

func TestNewClientFollowsTheType(t *testing.T) {
	for _, c := range []struct {
		cfg  settings.DownloadClient
		want string
	}{
		{settings.DownloadClient{URL: "http://x:8080"}, "qBittorrent"}, // saved before there was a choice
		{settings.DownloadClient{Type: settings.ClientQBittorrent, URL: "http://x:8080"}, "qBittorrent"},
		{settings.DownloadClient{Type: settings.ClientTransmission, URL: "http://x:9091", Username: "u", Password: "p"}, "Transmission"},
	} {
		got := newClient(c.cfg)
		if got.Name() != c.want || c.cfg.ClientName() != c.want {
			t.Errorf("%+v: client %s, settings say %s; want %s", c.cfg, got.Name(), c.cfg.ClientName(), c.want)
		}
	}
	if tm, ok := newClient(settings.DownloadClient{Type: settings.ClientTransmission, URL: "http://x:9091", Username: "u", Password: "p"}).(*Transmission); !ok ||
		tm.URL != "http://x:9091" || tm.Username != "u" || tm.Password != "p" {
		t.Errorf("Transmission client = %+v", tm)
	}
}
