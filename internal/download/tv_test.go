package download

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

const hashTV = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// A TV grab goes into the TV category, records the episodes it's for, and
// on finishing imports just those episodes; cleanup sees it like a movie.
func TestTVDownload(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		now := time.Now().UTC()
		show := &library.Title{MediaType: library.Series, TMDBID: 1399, Title: "The Expanse", Year: 2015, CreatedAt: now, Genres: "[]", AltTitles: "[]"}
		if _, err := db.DB.NewInsert().Model(show).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		v := &library.Version{TitleID: show.ID, ProfileID: 1, RootFolderID: f.mv.Version.RootFolderID, Monitored: true, Upgrades: true, CreatedAt: now}
		if _, err := db.DB.NewInsert().Model(v).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for n := 1; n <= 3; n++ {
			s, num := 1, n
			e := &library.Episode{TitleID: show.ID, TMDBID: 100 + n, AiredSeason: 1, AiredNumber: n, Season: &s, Number: &num}
			if _, err := db.DB.NewInsert().Model(e).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, e.ID)
		}

		err := f.svc.Grab(ctx, grab.Grabbed{
			Release: indexer.Release{Release: core.Release{Title: "The.Expanse.S01.1080p.BluRay.x264-ROVERS", InfoHash: hashTV, Size: 9 << 30},
				IndexerID: f.ixID, Indexer: "Tracker"},
			Quality: "Bluray-1080p", Score: 5,
			TV: &grab.TVGrab{Title: *show, Version: *v, EpisodeIDs: ids[:2], Label: "season 1 (2 wanted episodes)"},
		})
		if err != nil {
			t.Fatal(err)
		}
		var d Download
		if err := db.DB.NewSelect().Model(&d).Where("hash = ?", hashTV).Scan(ctx); err != nil {
			t.Fatal(err)
		}
		if d.Category != settings.TVCategory || *d.VersionID != v.ID || *d.TitleID != show.ID {
			t.Errorf("download = %+v", d)
		}
		if f.mem.torrents[hashTV].Category != settings.TVCategory {
			t.Errorf("qBittorrent category = %q", f.mem.torrents[hashTV].Category)
		}
		// In progress for those two episodes of this version only.
		active, err := f.svc.ActiveEpisodes(ctx)
		if err != nil || !active[v.ID][ids[0]] || !active[v.ID][ids[1]] || active[v.ID][ids[2]] || len(active) != 1 {
			t.Errorf("active = %v, %v", active, err)
		}
		// The queue lists it from the TV category.
		items, cerr, err := f.svc.Queue(ctx)
		if err != nil || cerr != nil || len(items) != 1 || items[0].Torrent == nil ||
			len(items[0].EpisodeIDs) != 2 || items[0].EpisodeIDs[0] != ids[0] {
			t.Errorf("queue = %+v, %v, %v", items, cerr, err)
		}

		// Finished: imported as episodes, not as a movie.
		f.mem.torrents[hashTV].Progress = 1
		f.imp.tvRes = library.EpisodeImportResult{
			Imported:  []string{"The Expanse (2015)/Season 1/a.mkv", "The Expanse (2015)/Season 1/b.mkv"},
			FullPaths: []string{"/shows/The Expanse (2015)/Season 1/a.mkv", "/shows/The Expanse (2015)/Season 1/b.mkv"},
			Skipped:   []string{"c.mkv: not an episode this download was grabbed for"},
		}
		msg, _, err := f.svc.Check(ctx)
		if err != nil || !strings.Contains(msg, "1 imported") {
			t.Fatalf("check = %q, %v", msg, err)
		}
		if len(f.imp.reqs) != 0 || len(f.imp.tvReqs) != 1 {
			t.Fatalf("imports: movie %d, TV %d", len(f.imp.reqs), len(f.imp.tvReqs))
		}
		r := f.imp.tvReqs[0]
		if r.VersionID != v.ID || len(r.EpisodeIDs) != 2 || r.EpisodeIDs[0] != ids[0] || r.Source != "/downloads/"+hashTV {
			t.Errorf("import request = %+v", r)
		}
		evs, _ := f.hist.List(ctx, grab.ListOptions{Events: []string{grab.EventImported}})
		if len(evs) != 1 || !strings.Contains(evs[0].Message, "Imported 2 episode files into The Expanse (2015)/Season 1.") ||
			!strings.Contains(evs[0].Message, "Skipped: c.mkv") {
			t.Errorf("history = %+v", evs)
		}
		if active, _ := f.svc.ActiveEpisodes(ctx); len(active) != 0 {
			t.Errorf("still active after import: %v", active)
		}

		// Cleanup lists it under Seeding from the TV category.
		seeding, err := f.svc.Seeding(ctx)
		if err != nil || len(seeding) != 1 {
			t.Errorf("seeding = %+v, %v", seeding, err)
		}
	})
}

const (
	hashFixA = "cccccccccccccccccccccccccccccccccccccccc"
	hashFixB = "dddddddddddddddddddddddddddddddddddddddd"
)

// tvShow stores a series with two episodes in the fixture's media folder.
func tvShow(t *testing.T, db *store.Store, f *dfix) (*library.Title, *library.Version, []int64) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	show := &library.Title{MediaType: library.Series, TMDBID: 1399, Title: "The Expanse", Year: 2015, CreatedAt: now, Genres: "[]", AltTitles: "[]"}
	if _, err := db.DB.NewInsert().Model(show).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	v := &library.Version{TitleID: show.ID, ProfileID: 1, RootFolderID: f.mv.Version.RootFolderID, Monitored: true, Upgrades: true, CreatedAt: now}
	if _, err := db.DB.NewInsert().Model(v).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for n := 1; n <= 2; n++ {
		s, num := 1, n
		e := &library.Episode{TitleID: show.ID, TMDBID: 100 + n, AiredSeason: 1, AiredNumber: n, Season: &s, Number: &num}
		if _, err := db.DB.NewInsert().Model(e).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	return show, v, ids
}

// A TV grab marked as a fix (Upgrade) is imported in replace mode, and
// History says what was moved aside.
func TestTVFixDownloadReplaces(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		show, v, ids := tvShow(t, db, f)
		err := f.svc.Grab(ctx, grab.Grabbed{
			Release: indexer.Release{Release: core.Release{Title: "The.Expanse.S01E01.REPACK.1080p.WEB-DL.H.264-NTb", InfoHash: hashFixA, Size: 2 << 30},
				IndexerID: f.ixID, Indexer: "Tracker"},
			Quality: "WEBDL-1080p", Score: 5, Upgrade: true,
			TV: &grab.TVGrab{Title: *show, Version: *v, EpisodeIDs: ids[:1], Label: "S01E01"},
		})
		if err != nil {
			t.Fatal(err)
		}
		f.mem.torrents[hashFixA].Progress = 1
		f.imp.tvRes = library.EpisodeImportResult{
			Imported:  []string{"The Expanse (2015)/Season 1/new.mkv"},
			FullPaths: []string{"/shows/The Expanse (2015)/Season 1/new.mkv"},
			Replaced:  []string{".reelkeeper-replaced/20261012-120000/The Expanse (2015)/Season 1/old.mkv"},
		}
		if _, _, err := f.svc.Check(ctx); err != nil {
			t.Fatal(err)
		}
		if len(f.imp.tvReqs) != 1 || !f.imp.tvReqs[0].Replace {
			t.Fatalf("import requests = %+v", f.imp.tvReqs)
		}
		evs, _ := f.hist.List(ctx, grab.ListOptions{Events: []string{grab.EventImported}})
		if len(evs) != 1 || !strings.Contains(evs[0].Message, "The files it replaced were moved to .reelkeeper-replaced/20261012-120000/") {
			t.Errorf("history = %+v", evs)
		}
	})
}

// An ordinary TV download never replaces anything.
func TestTVDownloadDoesNotReplace(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		show, v, ids := tvShow(t, db, f)
		err := f.svc.Grab(ctx, grab.Grabbed{
			Release: indexer.Release{Release: core.Release{Title: "The.Expanse.S01E01.1080p.WEB-DL.H.264-NTb", InfoHash: hashFixA, Size: 2 << 30},
				IndexerID: f.ixID, Indexer: "Tracker"},
			Quality: "WEBDL-1080p", Score: 5,
			TV: &grab.TVGrab{Title: *show, Version: *v, EpisodeIDs: ids[:1], Label: "S01E01"},
		})
		if err != nil {
			t.Fatal(err)
		}
		f.mem.torrents[hashFixA].Progress = 1
		f.imp.tvRes = library.EpisodeImportResult{Imported: []string{"a/b.mkv"}, FullPaths: []string{"/shows/a/b.mkv"}}
		if _, _, err := f.svc.Check(ctx); err != nil {
			t.Fatal(err)
		}
		if len(f.imp.tvReqs) != 1 || f.imp.tvReqs[0].Replace {
			t.Fatalf("import requests = %+v", f.imp.tvReqs)
		}
	})
}

// CurrentEpisodeDownloads names the recent download an episode's present
// file came from, and leaves out files that came from elsewhere.
func TestCurrentEpisodeDownloads(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		show, v, ids := tvShow(t, db, f)
		now := time.Now().UTC().Truncate(time.Second)
		ago := func(h int) time.Time { return now.Add(-time.Duration(h) * time.Hour) }
		grabTV := func(hash, name string, ep int64) int64 {
			t.Helper()
			err := f.svc.Grab(ctx, grab.Grabbed{
				Release: indexer.Release{Release: core.Release{Title: name, InfoHash: hash, Size: 2 << 30}, IndexerID: f.ixID, Indexer: "Tracker"},
				Quality: "WEBDL-1080p", TV: &grab.TVGrab{Title: *show, Version: *v, EpisodeIDs: []int64{ep}, Label: "x"},
			})
			if err != nil {
				t.Fatal(err)
			}
			var id int64
			if err := db.DB.NewSelect().Model((*Download)(nil)).Column("id").Where("hash = ?", hash).Scan(ctx, &id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		// settle makes a download imported at the given time, grabbed 3
		// hours before.
		settle := func(id int64, imported time.Time, path string) {
			t.Helper()
			if _, err := db.DB.NewUpdate().Model((*Download)(nil)).Set("status = ?", StatusImported).Set("created_at = ?", imported.Add(-3*time.Hour)).
				Set("imported_at = ?", imported).Set("imported_path = ?", path).Where("id = ?", id).Exec(ctx); err != nil {
				t.Fatal(err)
			}
		}
		fileFor := func(ep int64, name string, added time.Time) {
			t.Helper()
			mf := &library.MediaFile{VersionID: v.ID, RootFolderID: v.RootFolderID, Path: name, Size: 1, AddedAt: added,
				MediaInfo: "{}", ReleaseInfo: "{}", EpisodeClaim: "{}", ModifiedAt: added}
			if _, err := db.DB.NewInsert().Model(mf).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := db.DB.NewInsert().Model(&library.EpisodeFile{EpisodeID: ep, MediaFileID: mf.ID}).Exec(ctx); err != nil {
				t.Fatal(err)
			}
		}

		// Episode 1: its file arrived with a download imported 2 days ago.
		d1 := grabTV(hashFixA, "The.Expanse.S01E01.1080p.WEB-DL.H.264-NTb", ids[0])
		settle(d1, ago(48), "show/Season 1/e1.mkv")
		fileFor(ids[0], "e1.mkv", ago(48).Add(-time.Minute))
		// Episode 2: its file was added by hand after the download finished
		// (the download had found the episode already filled).
		d2 := grabTV(hashFixB, "The.Expanse.S01E02.1080p.WEB-DL.H.264-NTb", ids[1])
		settle(d2, ago(48), "")
		fileFor(ids[1], "e2.mkv", ago(24))

		got, err := f.svc.CurrentEpisodeDownloads(ctx, v.ID, ago(14*24))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[ids[0]].DownloadID != d1 || got[ids[0]].Release != "The.Expanse.S01E01.1080p.WEB-DL.H.264-NTb" ||
			got[ids[0]].ImportedAt.Sub(ago(48)).Abs() > time.Second {
			t.Errorf("got %+v", got)
		}
		// Imported longer ago than asked about: not eligible.
		if got, err = f.svc.CurrentEpisodeDownloads(ctx, v.ID, ago(1)); err != nil || len(got) != 0 {
			t.Errorf("old download: %v, %v", got, err)
		}
		// A download whose episode got a different file afterwards (added
		// after the import) doesn't count for it.
		if _, err := db.DB.NewUpdate().Table("media_files").Set("added_at = ?", ago(24)).Where("path = ?", "e1.mkv").Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if got, err = f.svc.CurrentEpisodeDownloads(ctx, v.ID, ago(14*24)); err != nil || len(got) != 0 {
			t.Errorf("file from elsewhere: %v, %v", got, err)
		}
	})
}
