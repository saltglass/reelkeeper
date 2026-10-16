package download

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/cleanup"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// withCleanup turns cleanup on in the fixture, in the given mode.
func withCleanup(f *dfix, mode string) (*settings.Cleanup, *[]int64) {
	cfg := settings.DefaultCleanup()
	cfg.Mode = mode
	var searched []int64
	f.svc.SetCleanup(func() settings.Cleanup { return cfg }, func(_ context.Context, v int64) error {
		searched = append(searched, v)
		return nil
	}, nil)
	return &cfg, &searched
}

func (f *dfix) download(t *testing.T, db *store.Store, hash string) *Download {
	t.Helper()
	d := new(Download)
	if err := db.DB.NewSelect().Model(d).Where("hash = ?", hash).Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	return d
}

func events(t *testing.T, s *Service) []CleanupEvent {
	t.Helper()
	evs, err := s.CleanupEvents(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestCleanupSeededDryRunThenOn(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		cfg, _ := withCleanup(f, settings.CleanupDryRun)
		f.grab(t, hashA)
		if len(f.mem.lifted) != 1 {
			t.Errorf("share limits not lifted at grab: %v", f.mem.lifted)
		}
		// Finished and imported, then seeded past the goal.
		f.mem.torrents[hashA].Progress = 1
		if _, _, err := f.svc.Check(ctx); err != nil {
			t.Fatal(err)
		}
		tor := f.mem.torrents[hashA]
		tor.Ratio, tor.SeedingTime, tor.State = 1.3, 2*time.Hour, torrent.Seeding

		// Dry run: recorded once, nothing removed.
		for i := range 3 {
			_, quiet, err := f.svc.Cleanup(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// Only the first pass marks anything; the repeats change nothing.
			if quiet != (i > 0) {
				t.Errorf("dry run pass %d: quiet = %v", i, quiet)
			}
		}
		evs := events(t, f.svc)
		if len(evs) != 1 || evs[0].Action != ActionWouldRemove || evs[0].Rule != "seeded" || !strings.Contains(evs[0].Message, "ratio 1.30") {
			t.Fatalf("dry-run events = %+v", evs)
		}
		if len(f.mem.removed) != 0 {
			t.Fatal("dry run removed a torrent")
		}

		// On: removed; the movie's record becomes "seeded".
		cfg.Mode = settings.CleanupOn
		if msg, quiet, err := f.svc.Cleanup(ctx); err != nil || !strings.Contains(msg, "1 removed") || quiet {
			t.Fatalf("cleanup = %q, quiet %v, %v", msg, quiet, err)
		}
		if _, quiet, _ := f.svc.Cleanup(ctx); !quiet {
			t.Error("a pass with nothing left to do should be quiet")
		}
		if len(f.mem.removed) != 1 {
			t.Error("not removed from qBittorrent")
		}
		if d := f.download(t, db, hashA); d.Status != StatusSeeded {
			t.Errorf("status = %s", d.Status)
		}
		if bl, _ := f.svc.Blocklist(ctx, BlocklistQuery{}); bl.Total != 0 {
			t.Error("a seeded download was blocklisted")
		}
	})
}

func TestCleanupKeepsTrackerMinimum(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		withCleanup(f, settings.CleanupOn)
		f.grab(t, hashA) // the fixture's release asks for ratio 2
		f.mem.torrents[hashA].Progress = 1
		if _, _, err := f.svc.Check(ctx); err != nil {
			t.Fatal(err)
		}
		// Past your goal (ratio 1.0), short of the tracker's (ratio 2 or 60 minutes).
		f.mem.torrents[hashA].Ratio, f.mem.torrents[hashA].SeedingTime = 1.5, 30*time.Minute
		if _, _, err := f.svc.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		if len(f.mem.removed) != 0 {
			t.Fatal("removed before the tracker's minimum (a hit-and-run)")
		}
	})
}

func TestCleanupStuckDownload(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		_, searched := withCleanup(f, settings.CleanupOn)
		f.grab(t, hashA)
		f.mem.torrents[hashA].State, f.mem.torrents[hashA].Left = torrent.Stalled, 500
		// It has been stuck for 7 hours already.
		long := f.svc.now().UTC().Add(-7 * time.Hour)
		b, _ := json.Marshal(cleanup.Track{Left: 500, ProgressAt: &long})
		if _, err := db.DB.NewUpdate().Model((*Download)(nil)).Set("cleanup_track = ?", string(b)).Where("hash = ?", hashA).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.svc.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		if len(f.mem.removed) != 1 || len(*searched) != 1 || (*searched)[0] != f.mv.Version.ID {
			t.Fatalf("removed %v, searched %v", f.mem.removed, *searched)
		}
		if d := f.download(t, db, hashA); d.Status != StatusRemoved || !strings.Contains(d.Message, "No progress for 7 hours") {
			t.Errorf("download = %s %q", d.Status, d.Message)
		}
		blocked, _ := f.svc.Blocklisted(ctx, f.mv.Title.ID)
		if !blocked("", hashA) {
			t.Error("not blocklisted")
		}
		// A stuck download is blocked for 30 days, not for good.
		bl, _ := f.svc.Blocklist(ctx, BlocklistQuery{})
		if len(bl.Items) != 1 || bl.Items[0].Rule != "stalled" || bl.Items[0].Permanent() ||
			!bl.Items[0].ExpiresAt.Equal(bl.Items[0].CreatedAt.Add(TemporaryBlockLife)) {
			t.Errorf("blocklist = %+v", bl.Items)
		}
		evs, _ := f.hist.List(ctx, grab.ListOptions{Events: []string{grab.EventRemoved}})
		if len(evs) != 1 {
			t.Errorf("history = %+v", evs)
		}
	})
}

func TestCleanupOffAndUnregistered(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		cfg, _ := withCleanup(f, settings.CleanupOff)
		f.grab(t, hashA)
		if len(f.mem.lifted) != 0 {
			t.Error("lifted share limits while cleanup is off")
		}
		f.mem.torrents[hashA].State = torrent.Stalled
		f.mem.trackers = map[string][]string{hashA: {"Unregistered torrent"}}
		if msg, quiet, _ := f.svc.Cleanup(ctx); !strings.Contains(msg, "off") || !quiet {
			t.Errorf("off: %q", msg)
		}
		cfg.Mode = settings.CleanupOn
		for range 3 { // three checks in a row
			if _, _, err := f.svc.Cleanup(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if len(f.mem.removed) != 1 {
			t.Errorf("unregistered torrent not removed: %v", f.mem.removed)
		}
		// A torrent the tracker deleted stays blocked for good.
		if bl, _ := f.svc.Blocklist(ctx, BlocklistQuery{}); len(bl.Items) != 1 || bl.Items[0].Rule != "unregistered" || !bl.Items[0].Permanent() {
			t.Errorf("blocklist = %+v", bl.Items)
		}
		if len(f.mem.lifted) != 1 {
			t.Errorf("limits should be lifted once cleanup runs: %v", f.mem.lifted)
		}
	})
}

func TestOrphans(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		dir := t.TempDir()
		f.cfg.Folder = dir
		f.grab(t, hashA)
		if f.mem.savePath != dir {
			t.Errorf("save path = %q", f.mem.savePath)
		}
		f.mem.torrents[hashA].ContentPath = filepath.Join(dir, "Ronin.1998.1080p")
		const other = "cccccccccccccccccccccccccccccccccccccccc"
		f.mem.torrents[other] = &Torrent{Hash: other, Category: settings.MovieCategory, ContentPath: filepath.Join(dir, "Added.By.Hand")}
		for _, n := range []string{"Ronin.1998.1080p", "Added.By.Hand", "leftover.mkv"} {
			if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		o, err := f.svc.FindOrphans(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(o.Torrents) != 1 || o.Torrents[0].Hash != other || len(o.Files) != 1 || o.Files[0] != "leftover.mkv" {
			t.Fatalf("orphans = %+v", o)
		}
		if err := f.svc.RemoveOrphanTorrent(ctx, hashA); err == nil {
			t.Error("removed a tracked download as an orphan")
		}
		if err := f.svc.RemoveOrphanTorrent(ctx, other); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.DeleteStrayFile(ctx, "Ronin.1998.1080p"); err == nil {
			t.Error("deleted a file a torrent owns")
		}
		if err := f.svc.DeleteStrayFile(ctx, "../etc"); err == nil {
			t.Error("deleted outside the download folder")
		}
		if err := f.svc.DeleteStrayFile(ctx, "leftover.mkv"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "leftover.mkv")); !os.IsNotExist(err) {
			t.Error("stray file still there")
		}
	})
}

// An imported download whose torrent was removed in qBittorrent by hand
// leaves Seeding (marked removed, recorded in History) instead of staying
// there forever.
func TestCleanupForgetsTorrentsRemovedByHand(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		withCleanup(f, settings.CleanupDryRun)
		const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		for _, h := range []string{hashA, hashB} {
			f.grab(t, h)
			f.mem.torrents[h].Progress = 1
		}
		if _, _, err := f.svc.Check(ctx); err != nil {
			t.Fatal(err)
		}
		// A is removed in qBittorrent by hand; B is still there.
		delete(f.mem.torrents, hashA)
		if _, quiet, err := f.svc.Cleanup(ctx); err != nil || quiet {
			t.Fatalf("cleanup quiet=%v err=%v", quiet, err)
		}
		if d := f.download(t, db, hashA); d.Status != StatusRemoved || !strings.Contains(d.Message, "outside Reelkeeper") {
			t.Errorf("A = %s %q", d.Status, d.Message)
		}
		if d := f.download(t, db, hashB); d.Status != StatusImported {
			t.Errorf("B = %s, want still imported", d.Status)
		}
		if items, _ := f.svc.Seeding(ctx); len(items) != 1 || items[0].Download.Hash != hashB {
			t.Errorf("seeding = %+v", items)
		}
	})
}

// A grab that wouldn't leave the space kept free (Settings → Cleanup) is
// refused before anything reaches qBittorrent, counting what downloads in
// progress still need.
func TestGrabNeedsRoom(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newDFix(t, db)
		withCleanup(f, settings.CleanupOn) // keeps 100 GB free by default
		f.cfg.Folder = "/downloads"
		free := int64(120 << 30)
		f.svc.freeSpace = func(string) int64 { return free }
		const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

		f.grab(t, hashA) // 8 GB: 120 - 8 leaves 112
		f.mem.torrents[hashA].Left = 8 << 30
		err := f.svc.Grab(context.Background(), grab.Grabbed{
			Release: indexer.Release{Release: core.Release{Title: "Heat.1995.1080p.BluRay.x264-GRP", InfoHash: hashB, Size: 15 << 30},
				IndexerID: f.ixID, Indexer: "Tracker"},
			Version: f.mv, Quality: "Bluray-1080p",
		})
		// 120 free - 8 still to come for Ronin - 15 = 97 < 100.
		if !errors.Is(err, grab.ErrNotEnoughSpace) || !strings.Contains(err.Error(), "120.0 GB free") {
			t.Fatalf("err = %v", err)
		}
		if _, added := f.mem.torrents[hashB]; added {
			t.Error("sent to qBittorrent anyway")
		}
		// Can't tell the free space: the grab goes through.
		f.svc.freeSpace = func(string) int64 { return -1 }
		if err := f.svc.Grab(context.Background(), grab.Grabbed{
			Release: indexer.Release{Release: core.Release{Title: "Heat.1995.1080p.BluRay.x264-GRP", InfoHash: hashB, Size: 15 << 30},
				IndexerID: f.ixID, Indexer: "Tracker"},
			Version: f.mv, Quality: "Bluray-1080p",
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// Backfill asks whether the disk is already short before it searches: free
// space, less what downloads in progress still need, below the space kept
// free.
func TestDiskShort(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		withCleanup(f, settings.CleanupOn) // keeps 100 GB free by default
		f.cfg.Folder = "/downloads"
		free := int64(150 << 30)
		f.svc.freeSpace = func(string) int64 { return free }
		if why, err := f.svc.DiskShort(ctx); err != nil || why != "" {
			t.Fatalf("plenty of room: %q, %v", why, err)
		}
		f.grab(t, hashA)
		f.mem.torrents[hashA].Left = 60 << 30 // 150 - 60 = 90 < 100
		if why, err := f.svc.DiskShort(ctx); err != nil || !strings.Contains(why, "150.0 GB free") {
			t.Errorf("short: %q, %v", why, err)
		}
		f.svc.freeSpace = func(string) int64 { return -1 }
		if why, _ := f.svc.DiskShort(ctx); why != "" {
			t.Errorf("space unknown: %q", why)
		}
	})
}

// When the download disk is below the space kept free, cleanup removes
// imported torrents whose tracker minimum is met, before the user's own
// seeding goal; a torrent still owed to its tracker stays.
func TestCleanupMakesRoom(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		cfg, _ := withCleanup(f, settings.CleanupOn)
		cfg.SeedRatio, cfg.SeedHours = 5, 0 // the user's goal is far off
		f.cfg.Folder = "/downloads"
		free := int64(150 << 30)
		f.svc.freeSpace = func(string) int64 { return free }

		f.grab(t, hashA) // its tracker asks for ratio 2
		tor := f.mem.torrents[hashA]
		tor.Progress, tor.Size = 1, 8<<30
		if _, _, err := f.svc.Check(ctx); err != nil {
			t.Fatal(err)
		}
		tor.Ratio, tor.State = 1.2, torrent.Seeding

		run := func() {
			t.Helper()
			if _, _, err := f.svc.Cleanup(ctx); err != nil {
				t.Fatal(err)
			}
		}
		run()
		if len(f.mem.removed) != 0 {
			t.Fatal("removed with room to spare")
		}
		free = 60 << 30
		run()
		if len(f.mem.removed) != 0 {
			t.Fatal("removed while the tracker's minimum (ratio 2) isn't met")
		}
		tor.Ratio = 2.1
		run()
		if len(f.mem.removed) != 1 {
			t.Fatalf("removed = %v", f.mem.removed)
		}
		evs := events(t, f.svc)
		if len(evs) == 0 || evs[0].Rule != "space" || !strings.Contains(evs[0].Message, "had 60.0 GB free, below the 100.0 GB you keep free") {
			t.Errorf("events = %+v", evs)
		}
		if d := f.download(t, db, hashA); d.Status != StatusSeeded {
			t.Errorf("status = %s", d.Status)
		}
	})
}
