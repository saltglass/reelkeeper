package grab

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// fixFixture is a series whose listed episodes already have files, each from
// the given download imported daysAgo days before the fixture's "now".
func fixFixture(t *testing.T, db *store.Store, daysAgo int, have map[string]string) (*fixture, *library.SeriesVersion, *fakeGrabber, *fakeQueue) {
	t.Helper()
	f := newFixture(t, db)
	g := &fakeGrabber{}
	q := &fakeQueue{current: map[int64]decision.Current{}}
	f.svc.SetDownloads(g, q)
	f.mode = settings.Grabbing{Mode: settings.GrabOn, TVMode: settings.GrabOn, MinSeeders: 1, QueueLimit: 5}
	sv := addSeries(t, db, f, "The Expanse", "standard", map[string]string{
		"S06E04": "2026-10-02", "S06E05": "2026-10-09", "S06E06": "2026-10-16",
	})
	i := int64(0)
	for label, release := range have {
		i++
		sv.HasFile[epID(sv, label)] = true
		q.current[epID(sv, label)] = decision.Current{DownloadID: i, Release: release,
			ImportedAt: f.now.Add(-time.Duration(daysAgo) * 24 * time.Hour)}
	}
	return f, sv, g, q
}

const (
	e5NTb = "The.Expanse.S06E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb"
	e5Rep = "The.Expanse.S06E05.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb"
)

// RSS offers a REPACK from the same group of an episode imported two days
// ago: it's grabbed as a fix, History says so, and the grab is marked as
// one that replaces the file.
func TestRSSTakesARepackOfARecentEpisode(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, sv, g, _ := fixFixture(t, db, 2, map[string]string{"S06E05": e5NTb})
		f.idx.releases = []indexer.Release{tvRel(e5Rep, 2<<30, 10)}
		if _, quiet, err := f.svc.RSSSync(ctx); err != nil || quiet {
			t.Fatalf("quiet %v, %v", quiet, err)
		}
		if len(g.grabs) != 1 || g.got[0] != e5Rep {
			t.Fatalf("grabbed %v", g.got)
		}
		got := g.grabs[0]
		if !got.Upgrade || got.TV == nil || len(got.TV.EpisodeIDs) != 1 || got.TV.EpisodeIDs[0] != epID(sv, "S06E05") {
			t.Errorf("grab = %+v", got)
		}
		ev := f.events(t, EventGrabbed)
		if len(ev) != 1 || !strings.Contains(ev[0].Message, "Grabbed a REPACK of S06E05 from the same group (NTb), to fix the file you have. Release: "+e5Rep) {
			t.Errorf("grabbed events = %+v", ev)
		}
		// The same feed again is old news.
		if _, quiet, _ := f.svc.RSSSync(ctx); !quiet || len(g.grabs) != 1 {
			t.Errorf("repeat: quiet %v, grabs %d", quiet, len(g.grabs))
		}
	})
}

// A PROPER from another group fixes a bad release; History names both.
func TestRSSTakesAProperFromAnotherGroup(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, _, g, _ := fixFixture(t, db, 2, map[string]string{"S06E05": e5NTb})
		const proper = "The.Expanse.S06E05.PROPER.1080p.AMZN.WEB-DL.DDP5.1.H.264-FLUX"
		f.idx.releases = []indexer.Release{tvRel(proper, 2<<30, 10)}
		if _, _, err := f.svc.RSSSync(ctx); err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 1 {
			t.Fatalf("grabbed %v", g.got)
		}
		ev := f.events(t, EventGrabbed)
		if len(ev) != 1 || !strings.Contains(ev[0].Message, "a PROPER of S06E05 (FLUX) to fix the NTb release you have") {
			t.Errorf("grabbed events = %+v", ev)
		}
	})
}

// The same REPACK for an episode imported 20 days ago, a REPACK from
// another group, and a profile that doesn't prefer propers: none is taken.
func TestRSSLeavesOtherFixesAlone(t *testing.T) {
	for _, c := range []struct {
		name    string
		days    int
		release string
		profile bool // the profile prefers propers
		reason  string
	}{
		{"imported 20 days ago", 20, e5Rep, true, ""},
		{"REPACK from another group", 2, "The.Expanse.S06E05.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-FLUX", true, "only taken from the group"},
		{"other quality", 2, "The.Expanse.S06E05.PROPER.720p.HDTV.x264-FLUX", true, "same quality"},
		{"profile off", 2, e5Rep, false, ""}, // said nothing: RSS doesn't even look
	} {
		t.Run(c.name, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, db *store.Store) {
				ctx := context.Background()
				f, _, g, _ := fixFixture(t, db, c.days, map[string]string{"S06E05": e5NTb})
				f.lib.profile.PreferPropers = c.profile
				f.idx.releases = []indexer.Release{tvRel(c.release, 2<<30, 10)}
				if _, _, err := f.svc.RSSSync(ctx); err != nil {
					t.Fatal(err)
				}
				if len(g.grabs) != 0 || len(f.events(t, EventGrabbed, EventWouldGrab)) != 0 {
					t.Fatalf("grabbed %v", g.got)
				}
				if c.reason == "" {
					return
				}
				evs := f.events(t, EventRSS)
				if len(evs) != 1 || !strings.Contains(evs[0].Data, c.reason) {
					t.Errorf("RSS events = %+v, want a reason mentioning %q", evs, c.reason)
				}
			})
		})
	}
}

// A REPACK of an episode with no file isn't a fix; the normal rules apply
// (here the episode is wanted, so it's simply taken).
func TestRSSNormalBehaviourForEpisodesWithoutFiles(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, sv, g, _ := fixFixture(t, db, 2, map[string]string{"S06E04": "The.Expanse.S06E04.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb"})
		sv.Wanted = append(sv.Wanted, sv.Episodes[1]) // S06E05, no file
		f.idx.releases = []indexer.Release{tvRel(e5Rep, 2<<30, 10)}
		if _, _, err := f.svc.RSSSync(ctx); err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 1 || g.grabs[0].Upgrade {
			t.Fatalf("grabs = %+v", g.grabs)
		}
		if ev := f.events(t, EventGrabbed); len(ev) != 1 || strings.Contains(ev[0].Message, "to fix") {
			t.Errorf("events = %+v", ev)
		}
	})
}

// A season pack REPACK replaces a season only when every file came from the
// one pack.
func TestRSSSeasonPackFix(t *testing.T) {
	const pack = "The.Expanse.S06.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb"
	const packRep = "The.Expanse.S06.REPACK.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb"
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, sv, g, q := fixFixture(t, db, 2, nil)
		for _, l := range []string{"S06E04", "S06E05"} {
			sv.HasFile[epID(sv, l)] = true
			q.current[epID(sv, l)] = decision.Current{DownloadID: 9, Release: pack, ImportedAt: f.now.Add(-48 * time.Hour)}
		}
		f.idx.releases = []indexer.Release{tvRel(packRep, 20<<30, 10)}
		if _, _, err := f.svc.RSSSync(ctx); err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 1 || !g.grabs[0].Upgrade || len(g.grabs[0].TV.EpisodeIDs) != 2 {
			t.Fatalf("grabs = %+v", g.grabs)
		}
		if ev := f.events(t, EventGrabbed); len(ev) != 1 || !strings.Contains(ev[0].Message, "a REPACK of season 6 from the same group (NTb)") {
			t.Errorf("events = %+v", ev)
		}
	})
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, sv, g, q := fixFixture(t, db, 2, nil)
		// Mixed sources: one episode from the pack, one from a download of its own.
		sv.HasFile[epID(sv, "S06E04")] = true
		q.current[epID(sv, "S06E04")] = decision.Current{DownloadID: 9, Release: pack, ImportedAt: f.now.Add(-48 * time.Hour)}
		sv.HasFile[epID(sv, "S06E05")] = true
		q.current[epID(sv, "S06E05")] = decision.Current{DownloadID: 10, Release: e5NTb, ImportedAt: f.now.Add(-48 * time.Hour)}
		f.idx.releases = []indexer.Release{tvRel(packRep, 20<<30, 10)}
		if _, _, err := f.svc.RSSSync(ctx); err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 0 {
			t.Fatalf("grabbed %v", g.got)
		}
		if evs := f.events(t, EventRSS); len(evs) != 1 || !strings.Contains(evs[0].Data, "didn't all come from one season pack") {
			t.Errorf("RSS events = %+v", evs)
		}
	})
}

// A fix is held back like any other grab: an unmanaged folder, and the
// queue limit.
func TestRSSFixHeldBack(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, sv, g, _ := fixFixture(t, db, 2, map[string]string{"S06E05": e5NTb})
		f.lib.unmanaged = map[int64]bool{sv.Version.RootFolderID: true}
		f.idx.releases = []indexer.Release{tvRel(e5Rep, 2<<30, 10)}
		if _, _, err := f.svc.RSSSync(ctx); err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 0 {
			t.Fatalf("grabbed %v into an unmanaged folder", g.got)
		}
		if ev := f.events(t, EventGrabSkipped); len(ev) != 1 || !strings.Contains(ev[0].Message, `"Manage files" is off`) {
			t.Errorf("skipped = %+v", ev)
		}
	})
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, _, g, q := fixFixture(t, db, 2, map[string]string{"S06E05": e5NTb})
		f.mode.QueueLimit = 1
		q.size = 1
		f.idx.releases = []indexer.Release{tvRel(e5Rep, 2<<30, 10)}
		if _, _, err := f.svc.RSSSync(ctx); err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 0 {
			t.Fatalf("grabbed %v with the queue full", g.got)
		}
	})
}
