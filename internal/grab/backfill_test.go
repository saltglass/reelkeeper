package grab

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// bfShow adds a show with a special that has aired, six regular episodes
// of season 1 (the first four have files), and a season 2 episode that
// hasn't aired. Nothing is wanted until backfill turns episodes on. It
// returns the version's id.
func bfShow(t *testing.T, db *store.Store, f *fixture, title string) int64 {
	t.Helper()
	sv := addSeries(t, db, f, title, "standard", map[string]string{
		"S00E01": "2006-12-25",
		"S01E01": "2006-02-03", "S01E02": "2006-02-10", "S01E03": "2006-02-17",
		"S01E04": "2006-02-24", "S01E05": "2006-03-03", "S01E06": "2006-03-10",
		"S02E01": "2027-01-01",
	})
	for _, l := range []string{"S01E01", "S01E02", "S01E03", "S01E04"} {
		sv.HasFile[epID(sv, l)] = true
	}
	return sv.Version.ID
}

// bfOn makes TV grabbing On with a download client, and returns them.
func bfOn(f *fixture) (*fakeGrabber, *fakeQueue) {
	f.mode = settings.Grabbing{Mode: settings.GrabOn, TVMode: settings.GrabOn, MinSeeders: 1, QueueLimit: 50}
	g, q := &fakeGrabber{}, &fakeQueue{active: map[int64]bool{}, blocked: map[string]bool{}}
	f.setDownloads(g, q)
	return g, q
}

func (f *fixture) sv(versionID int64) *library.SeriesVersion {
	for i := range f.lib.series {
		if f.lib.series[i].Version.ID == versionID {
			return &f.lib.series[i]
		}
	}
	panic("no such version")
}

// importEpisodes plays out a finished import: the episodes now have files.
func (f *fixture) importEpisodes(versionID int64, labels ...string) {
	sv := f.sv(versionID)
	for _, l := range labels {
		id := epID(sv, l)
		sv.HasFile[id] = true
		sv.Wanted = slices.DeleteFunc(sv.Wanted, func(e library.Episode) bool { return e.ID == id })
	}
}

func (f *fixture) backfillMessages(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, e := range f.events(t, EventBackfill) {
		out = append(out, e.Message)
	}
	slices.Reverse(out) // oldest first
	return out
}

func stateOf(t *testing.T, f *fixture, id int64) string {
	t.Helper()
	row, err := f.svc.backfillRow(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row.State
}

func runBackfill(t *testing.T, f *fixture) (string, bool) {
	t.Helper()
	msg, quiet, err := f.svc.BackfillRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return msg, quiet
}

func queriesOf(f *fixture) []string {
	var out []string
	for _, q := range f.idx.queries {
		out = append(out, fmt.Sprintf("S%02dE%s", q.Season, q.Episode))
	}
	return out
}

// Adding a show to an empty queue starts it: every aired episode without a
// file is turned on, specials and unaired episodes are not.
func TestBackfillStartTurnsOnMissingEpisodes(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		bfOn(f)
		vid := bfShow(t, db, f, "The IT Crowd")

		row, err := f.svc.BackfillAdd(ctx, vid)
		if err != nil {
			t.Fatal(err)
		}
		if row.State != BackfillActive || row.StartedAt == nil {
			t.Fatalf("row = %+v", row)
		}
		sv := f.sv(vid)
		var on []string
		for id, v := range f.lib.monitored[vid] {
			if v {
				for _, e := range sv.Episodes {
					if e.ID == id {
						on = append(on, fmt.Sprintf("S%02dE%02d", *e.Season, *e.Number))
					}
				}
			}
		}
		slices.Sort(on)
		if strings.Join(on, ",") != "S01E05,S01E06" {
			t.Errorf("turned on %v; want S01E05 and S01E06 (not the special, not episodes with files, not unaired ones)", on)
		}
		if got := f.backfillMessages(t); len(got) != 1 || !strings.HasPrefix(got[0], "Backfill started The IT Crowd") {
			t.Errorf("History = %q", got)
		}
		// A show is in the queue once.
		if _, err := f.svc.BackfillAdd(ctx, vid); !errors.Is(err, ErrBackfillQueued) {
			t.Errorf("adding again: %v", err)
		}
	})
}

// With nothing queued the task has nothing to say.
func TestBackfillRunWithAnEmptyQueueIsQuiet(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		bfOn(f)
		if msg, quiet := runBackfill(t, f); !quiet || !strings.Contains(msg, "Nothing is waiting") {
			t.Errorf("run = %q, quiet %v", msg, quiet)
		}
	})
}

func TestBackfillAddRefusals(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		bfOn(f)
		if _, err := f.svc.BackfillAdd(ctx, 9999); !errors.Is(err, ErrBackfillNotFound) {
			t.Errorf("unknown version: %v", err)
		}
		// Nothing missing: every aired episode has a file.
		full := addSeries(t, db, f, "Complete", "standard", map[string]string{"S01E01": "2006-02-03"})
		full.HasFile[epID(full, "S01E01")] = true
		if _, err := f.svc.BackfillAdd(ctx, full.Version.ID); err == nil || !strings.Contains(err.Error(), "no aired episodes missing") {
			t.Errorf("nothing missing: %v", err)
		}
	})
}

// While anything for the show is downloading, a run does nothing. Once it
// has been imported, the show is searched again, and with nothing left to
// find it is finished with leftovers and the next show starts.
func TestBackfillWaitsThenFinishesWithLeftovers(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		g, q := bfOn(f)
		a := bfShow(t, db, f, "The IT Crowd")
		b := bfShow(t, db, f, "Black Books")
		rowA, err := f.svc.BackfillAdd(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		rowB, err := f.svc.BackfillAdd(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		if rowB.State != BackfillQueued {
			t.Fatalf("second show = %s; one show is worked on at a time", rowB.State)
		}

		// First run: searches for both missing episodes and grabs the one
		// that exists.
		f.idx.releases = []indexer.Release{
			tvRel("The.IT.Crowd.S01E05.1080p.WEB-DL.H.264-GRP", 2<<30, 30),
			tvRel("Black.Books.S01E05.1080p.WEB-DL.H.264-GRP", 2<<30, 30),
		}
		f.now = f.now.Add(time.Minute)
		msg, quiet := runBackfill(t, f)
		if quiet || len(g.got) != 1 || !strings.Contains(msg, "grabbed 1 release") {
			t.Errorf("first run: %q quiet=%v grabbed=%v", msg, quiet, g.got)
		}
		// S01E06 has no release of its own, so its finished season is searched
		// as a whole too.
		if got := queriesOf(f); !slices.Equal(got, []string{"S01E5", "S01E6", "S01E"}) {
			t.Errorf("searched %v; want both missing episodes, then the season", got)
		}

		// It's downloading: the next run searches nothing and is quiet.
		f.idx.queries = nil
		q.active[a] = true
		f.now = f.now.Add(15 * time.Minute)
		msg, quiet = runBackfill(t, f)
		if !quiet || len(f.idx.queries) != 0 || !strings.Contains(msg, "waiting for downloads") {
			t.Errorf("while downloading: %q quiet=%v queries=%v", msg, quiet, queriesOf(f))
		}

		// Imported. One episode is still missing and nothing acceptable
		// exists: finished with leftovers, and Black Books starts.
		delete(q.active, a)
		f.importEpisodes(a, "S01E05")
		f.idx.queries = nil
		f.now = f.now.Add(15 * time.Minute)
		runBackfill(t, f)
		if st := stateOf(t, f, rowA.ID); st != BackfillDecision {
			t.Fatalf("The IT Crowd = %s, want decision", st)
		}
		if st := stateOf(t, f, rowB.ID); st != BackfillActive {
			t.Errorf("Black Books = %s, want active", st)
		}
		done, _ := f.svc.backfillRow(ctx, rowA.ID)
		if done.Found != 5 || done.Missing != 1 || done.FinishedAt == nil {
			t.Errorf("summary = found %d missing %d", done.Found, done.Missing)
		}
		want := "Backfill finished The IT Crowd: 5 of 6 episodes; 1 still wanted."
		if got := f.backfillMessages(t); !slices.Contains(got, want) {
			t.Errorf("History = %q, want %q", got, want)
		}

		// The tab lists it under Needs a decision until it's hidden.
		l, err := f.svc.BackfillListAll(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if l.Now == nil || l.Now.Title != "Black Books" || len(l.Decision) != 1 || l.Decision[0].Missing != 1 || l.Decision[0].Found != 5 {
			t.Errorf("list = %+v", l)
		}
		if err := f.svc.BackfillHide(ctx, rowA.ID); err != nil {
			t.Fatal(err)
		}
		if l, _ = f.svc.BackfillListAll(ctx); len(l.Decision) != 0 {
			t.Errorf("hidden show still listed: %+v", l.Decision)
		}
		// Hidden or not, its episodes stay wanted.
		if len(f.sv(a).Wanted) != 1 {
			t.Errorf("wanted = %v", f.sv(a).Wanted)
		}
		// When the last one turns up, it's done.
		f.importEpisodes(a, "S01E06")
		runBackfill(t, f)
		if st := stateOf(t, f, rowA.ID); st != BackfillDone {
			t.Errorf("after the last episode = %s, want done", st)
		}
	})
}

// A show with everything finishes complete, and the queue moves on in the
// same run.
func TestBackfillFinishesComplete(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		bfOn(f)
		a := bfShow(t, db, f, "The IT Crowd")
		b := bfShow(t, db, f, "Black Books")
		rowA, _ := f.svc.BackfillAdd(ctx, a)
		rowB, _ := f.svc.BackfillAdd(ctx, b)
		f.importEpisodes(a, "S01E05", "S01E06")
		f.idx.releases = []indexer.Release{tvRel("Black.Books.S01E05.1080p.WEB-DL.H.264-GRP", 2<<30, 30)}
		runBackfill(t, f)
		if st := stateOf(t, f, rowA.ID); st != BackfillDone {
			t.Errorf("The IT Crowd = %s", st)
		}
		if st := stateOf(t, f, rowB.ID); st != BackfillActive {
			t.Errorf("Black Books = %s", st)
		}
		want := "Backfill finished The IT Crowd: all 6 episodes."
		if got := f.backfillMessages(t); !slices.Contains(got, want) {
			t.Errorf("History = %q, want %q", got, want)
		}
		// Only one show a run: Black Books is searched in this run (its two
		// episodes, then its season for the one with nothing), not finished
		// early.
		if len(f.idx.queries) != 3 {
			t.Errorf("queries = %v", queriesOf(f))
		}
	})
}

// A show isn't finished just because nothing was grabbed: while a limit,
// the disk, the mode or an unanswered indexer stands in the way, it waits.
func TestBackfillWaitsWhenHeldBack(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		g, q := bfOn(f)
		a := bfShow(t, db, f, "The IT Crowd")
		row, _ := f.svc.BackfillAdd(ctx, a)
		f.idx.releases = []indexer.Release{tvRel("The.IT.Crowd.S01E05.1080p.WEB-DL.H.264-GRP", 2<<30, 30)}
		active := func(why string) {
			t.Helper()
			if st := stateOf(t, f, row.ID); st != BackfillActive {
				t.Errorf("%s: state = %s, want it to keep waiting", why, st)
			}
		}

		// TV grabbing is a dry run: no search at all.
		f.mode.TVMode = settings.GrabDryRun
		runBackfill(t, f)
		if len(f.idx.queries) != 0 {
			t.Errorf("dry run searched: %v", queriesOf(f))
		}
		active("dry run")

		// The queue is full.
		f.mode = settings.Grabbing{Mode: settings.GrabOn, TVMode: settings.GrabOn, MinSeeders: 1, QueueLimit: 1}
		q.size = 1
		if msg, _ := runBackfill(t, f); !strings.Contains(msg, "waiting; the queue is full.") {
			t.Errorf("message = %q", msg)
		}
		if len(f.idx.queries) != 0 {
			t.Errorf("searched with the queue full: %v", queriesOf(f))
		}
		active("full queue")
		f.mode.QueueLimit = 50
		q.size = 0

		// The disk is short of space: no search either.
		f.svc.SetDownloads(g, &diskQueue{fakeQueue: q, why: "the download disk has 50.0 GB free."})
		runBackfill(t, f)
		if len(f.idx.queries) != 0 {
			t.Errorf("searched with a full disk: %v", queriesOf(f))
		}
		active("disk space")
		f.svc.SetDownloads(g, q)

		// An acceptable release the grab can't take (the disk is full only
		// once the size is known): it waits.
		g.err = fmt.Errorf("%w: the download disk has 50.0 GB free", ErrNotEnoughSpace)
		runBackfill(t, f)
		if len(f.idx.queries) == 0 {
			t.Fatal("expected a search")
		}
		active("held by space")
		g.err = nil

		// Folder with "Manage files" off: held too.
		f.lib.unmanaged = map[int64]bool{f.sv(a).Version.RootFolderID: true}
		runBackfill(t, f)
		active("unmanaged folder")
		f.lib.unmanaged = nil

		// No indexer answered: nothing was really searched.
		f.idx.releases = nil
		f.idx.failures = []indexer.IndexerError{{Indexer: "1337x", Message: "resting"}}
		runBackfill(t, f)
		active("no indexer answered")
		f.idx.failures = nil

		// The same search with answers and nothing acceptable finishes it.
		runBackfill(t, f)
		if st := stateOf(t, f, row.ID); st != BackfillDecision {
			t.Errorf("nothing acceptable: state = %s, want decision", st)
		}
	})
}

// diskQueue is a fakeQueue that says the download disk is short of space.
type diskQueue struct {
	*fakeQueue
	why string
}

func (d *diskQueue) DiskShort(context.Context) (string, error) { return d.why, nil }

// Five backfill grabs a day: used up, the show waits for tomorrow.
func TestBackfillWaitsForTomorrowWhenTheQuotaIsUsed(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		bfOn(f)
		a := bfShow(t, db, f, "The IT Crowd")
		row, _ := f.svc.BackfillAdd(ctx, a)
		for range BackfillGrabsPerDay {
			if err := f.hist.Add(ctx, &Event{VersionID: &a, Event: EventGrabbed, CreatedAt: f.now}, EventData{}); err != nil {
				t.Fatal(err)
			}
		}
		f.idx.releases = nil
		msg, _ := runBackfill(t, f)
		if len(f.idx.queries) != 0 || !strings.Contains(msg, "continues tomorrow") {
			t.Errorf("run = %q, queries %v", msg, queriesOf(f))
		}
		if st := stateOf(t, f, row.ID); st != BackfillActive {
			t.Errorf("state = %s; using up the quota is not finishing", st)
		}
		l, _ := f.svc.BackfillListAll(ctx)
		if l.GrabsToday != 5 || l.GrabsPerDay != 5 {
			t.Errorf("grabs today = %d of %d", l.GrabsToday, l.GrabsPerDay)
		}
		// Tomorrow it searches again.
		f.now = f.now.Add(24 * time.Hour)
		runBackfill(t, f)
		if len(f.idx.queries) == 0 {
			t.Error("expected a search the next day")
		}
	})
}

// The quota holds back every way a grab for the active show can happen
// (manual search, RSS), says why in History, and leaves other shows alone.
func TestBackfillQuotaAppliesToEverySearchPath(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		g, _ := bfOn(f)
		a := bfShow(t, db, f, "The IT Crowd")
		b := bfShow(t, db, f, "Black Books")
		if _, err := f.svc.BackfillAdd(ctx, a); err != nil {
			t.Fatal(err)
		}
		// Black Books is not in the queue, but wants an episode.
		if err := f.lib.SetEpisodesMonitored(ctx, b, []int64{epID(f.sv(b), "S01E05")}, true); err != nil {
			t.Fatal(err)
		}
		for range BackfillGrabsPerDay {
			if err := f.hist.Add(ctx, &Event{VersionID: &a, Event: EventGrabbed, CreatedAt: f.now}, EventData{}); err != nil {
				t.Fatal(err)
			}
		}
		f.idx.releases = []indexer.Release{
			tvRel("The.IT.Crowd.S01E05.1080p.WEB-DL.H.264-GRP", 2<<30, 30),
			tvRel("Black.Books.S01E05.1080p.WEB-DL.H.264-GRP", 2<<30, 30),
		}
		held := "Backfill's 5 grabs for today are used; it continues tomorrow."

		// A manual search.
		msg, err := f.svc.SearchVersion(ctx, a, TriggerManual)
		if err != nil || !strings.Contains(msg, held) || len(g.got) != 0 {
			t.Errorf("manual search: %q, %v (grabbed %v)", msg, err, g.got)
		}
		// RSS.
		if _, _, err := f.svc.RSSSync(ctx); err != nil {
			t.Fatal(err)
		}
		skipped := f.events(t, EventGrabSkipped)
		if len(skipped) < 2 {
			t.Fatalf("skipped = %+v", skipped)
		}
		for _, e := range skipped {
			if !strings.Contains(e.Message, held) {
				t.Errorf("skip says %q", e.Message)
			}
		}
		// The held episodes stay first in line: they aren't marked searched.
		if _, marked := f.lib.epSearched[a][epID(f.sv(a), "S01E05")]; marked {
			t.Error("an episode whose grab the quota held back was marked searched")
		}
		// Another show is unaffected.
		if _, err := f.svc.SearchVersion(ctx, b, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(g.got, func(n string) bool { return strings.HasPrefix(n, "Black.Books") }) ||
			slices.ContainsFunc(g.got, func(n string) bool { return strings.HasPrefix(n, "The.IT.Crowd") }) {
			t.Errorf("grabbed %v; only Black Books should be", g.got)
		}
		// The quota still counts grabs toward the overall limit and vice
		// versa: the first grab for the active show tomorrow goes through.
		f.now = f.now.Add(24 * time.Hour)
		if _, err := f.svc.SearchVersion(ctx, a, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if last := g.got[len(g.got)-1]; !strings.HasPrefix(last, "The.IT.Crowd") {
			t.Errorf("grabbed %v; the active show should be grabbed again after midnight", g.got)
		}
	})
}

func TestBackfillRemoveAndMove(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		bfOn(f)
		var rows []*BackfillRow
		for _, name := range []string{"One", "Two", "Three", "Four"} {
			r, err := f.svc.BackfillAdd(ctx, bfShow(t, db, f, name))
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, r)
		}
		order := func() string {
			t.Helper()
			l, err := f.svc.BackfillListAll(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			if l.Now != nil {
				names = append(names, "*"+l.Now.Title)
			}
			for _, s := range l.Next {
				names = append(names, s.Title)
			}
			return strings.Join(names, ",")
		}
		if got := order(); got != "*One,Two,Three,Four" {
			t.Fatalf("order = %s", got)
		}
		// Move down and up among the waiting ones; the ends do nothing.
		if err := f.svc.BackfillMove(ctx, rows[1].ID, "down"); err != nil {
			t.Fatal(err)
		}
		if got := order(); got != "*One,Three,Two,Four" {
			t.Errorf("after moving Two down: %s", got)
		}
		if err := f.svc.BackfillMove(ctx, rows[3].ID, "up"); err != nil {
			t.Fatal(err)
		}
		if got := order(); got != "*One,Three,Four,Two" {
			t.Errorf("after moving Four up: %s", got)
		}
		_ = f.svc.BackfillMove(ctx, rows[1].ID, "down")
		_ = f.svc.BackfillMove(ctx, rows[2].ID, "up")
		if got := order(); got != "*One,Four,Three,Two" && got != "*One,Three,Four,Two" {
			t.Errorf("after moving the ends: %s", got)
		}
		// The one being worked on can't be moved.
		if err := f.svc.BackfillMove(ctx, rows[0].ID, "down"); err == nil {
			t.Error("moved the active show")
		}
		// Removing a waiting show only removes it; its episodes stay as they are.
		if err := f.svc.BackfillRemove(ctx, rows[2].ID); err != nil {
			t.Fatal(err)
		}
		// Removing the active show starts the next in line.
		if err := f.svc.BackfillRemove(ctx, rows[0].ID); err != nil {
			t.Fatal(err)
		}
		l, _ := f.svc.BackfillListAll(ctx)
		if l.Now == nil || len(l.Next) != 1 {
			t.Errorf("after removing: %+v", l)
		}
		if err := f.svc.BackfillRemove(ctx, rows[0].ID); !errors.Is(err, ErrBackfillNotFound) {
			t.Errorf("removing twice: %v", err)
		}
		// Removed shows can be added again.
		if _, err := f.svc.BackfillAdd(ctx, rows[0].VersionID); err != nil {
			t.Error(err)
		}
	})
}

// Deleting a series takes its queue entry with it.
func TestBackfillRowGoesWithTheVersion(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		bfOn(f)
		a := bfShow(t, db, f, "The IT Crowd")
		if _, err := f.svc.BackfillAdd(ctx, a); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.NewDelete().Model((*library.Version)(nil)).Where("id = ?", a).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if n, err := db.DB.NewSelect().Model((*BackfillRow)(nil)).Count(ctx); err != nil || n != 0 {
			t.Errorf("rows left = %d, %v", n, err)
		}
	})
}

// A show with more single episodes than one search takes isn't finished
// until every one of them has been tried.
func TestBackfillTriesEveryUnitBeforeFinishing(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		bfOn(f)
		// Twelve seasons of two episodes, the first of each on disk: twelve
		// single-episode searches, two more than one search makes. Each finds
		// nothing, so each one's season is searched as a whole too.
		eps := map[string]string{}
		for n := 1; n <= maxUnitsPerSearch+2; n++ {
			eps[fmt.Sprintf("S%02dE01", n)] = "2006-02-03"
			eps[fmt.Sprintf("S%02dE02", n)] = "2006-02-10"
		}
		sv := addSeries(t, db, f, "Long Show", "standard", eps)
		for n := 1; n <= maxUnitsPerSearch+2; n++ {
			sv.HasFile[epID(sv, fmt.Sprintf("S%02dE01", n))] = true
		}
		row, err := f.svc.BackfillAdd(ctx, sv.Version.ID)
		if err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(time.Minute)
		runBackfill(t, f)
		if len(f.idx.queries) != 2*maxUnitsPerSearch {
			t.Errorf("first run searched %d, want %d", len(f.idx.queries), 2*maxUnitsPerSearch)
		}
		if st := stateOf(t, f, row.ID); st != BackfillActive {
			t.Fatalf("state = %s after searching only some of the episodes", st)
		}
		f.now = f.now.Add(15 * time.Minute)
		runBackfill(t, f)
		if st := stateOf(t, f, row.ID); st != BackfillDecision {
			t.Errorf("state = %s after every episode was tried", st)
		}
	})
}

// Options: the best of what the profile turned down, never what is
// blocklisted, unseeded, or for something else; and taking one is a grab
// outside the profile that still keeps its safety checks.
func TestBackfillOptionsAndGrabAnyway(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		g, q := bfOn(f)
		a := bfShow(t, db, f, "The IT Crowd")
		row, _ := f.svc.BackfillAdd(ctx, a)
		f.idx.releases = nil
		runBackfill(t, f)
		if st := stateOf(t, f, row.ID); st != BackfillDecision {
			t.Fatalf("state = %s", st)
		}

		q.blocked["The.IT.Crowd.S01E05.720p.HDTV.x264-BLOCKED"] = true
		f.idx.releases = []indexer.Release{
			tvRel("The.IT.Crowd.S01E05.720p.HDTV.x264-FEW", 1<<30, 3),
			tvRel("The.IT.Crowd.S01E05.720p.HDTV.x264-MOST", 1<<30, 90),
			tvRel("The.IT.Crowd.S01E05.480p.DVDRip.x264-MID", 1<<30, 40),
			tvRel("The.IT.Crowd.S01E05.720p.HDTV.x264-LEAST", 1<<30, 1),
			tvRel("The.IT.Crowd.S01E05.720p.HDTV.x264-BLOCKED", 1<<30, 500), // blocklisted
			tvRel("The.IT.Crowd.S01E05.720p.HDTV.x264-DEAD", 1<<30, 0),      // nobody seeds it
			tvRel("Black.Books.S01E05.720p.HDTV.x264-OTHERSHOW", 1<<30, 400),
			tvRel("The.IT.Crowd.S01E04.720p.HDTV.x264-OTHEREP", 1<<30, 300),
			tvRel("The.IT.Crowd.S03E05.720p.HDTV.x264-OTHERSEASON", 1<<30, 300),
			tvRel("The.IT.Crowd.S01E05.1080p.WEB-DL.H.264-GOOD", 2<<30, 800), // acceptable: the normal passes take it
		}
		f.idx.queries = nil
		opts, err := f.svc.BackfillOptions(ctx, row.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(opts.Units) != 2 || opts.Units[0].Label != "S01E05" || opts.Units[1].Label != "S01E06" || opts.Omitted != 0 {
			t.Fatalf("units = %+v", opts.Units)
		}
		var titles []string
		for _, o := range opts.Units[0].Options {
			titles = append(titles, o.Title[strings.LastIndex(o.Title, "-")+1:])
		}
		if strings.Join(titles, ",") != "MOST,MID,FEW" {
			t.Errorf("options for S01E05 = %v; want the three best-seeded rejects, best first", titles)
		}
		top := opts.Units[0].Options[0]
		if top.Seeders != 90 || top.Indexer != "Knaben" || top.Size != 1<<30 || top.Quality == "" || top.Token == "" ||
			len(top.Reasons) == 0 || !strings.Contains(strings.Join(top.Reasons, " "), "profile accepts") {
			t.Errorf("top option = %+v", top)
		}
		if len(opts.Units[1].Options) != 0 {
			t.Errorf("S01E06 was offered %+v; everything found was for another episode", opts.Units[1].Options)
		}
		tokens := map[string]string{}
		for _, o := range opts.Units[0].Options {
			tokens[o.Title[strings.LastIndex(o.Title, "-")+1:]] = o.Token
		}

		// Refused: a release blocklisted since, an unmanaged folder, no
		// room on the disk, no download client. Nothing is grabbed.
		q.blocked["The.IT.Crowd.S01E05.720p.HDTV.x264-FEW"] = true
		if _, err := f.svc.BackfillGrabOption(ctx, row.ID, tokens["FEW"]); !refusedWith(err, "blocklist") {
			t.Errorf("blocklisted: %v", err)
		}
		f.lib.unmanaged = map[int64]bool{f.sv(a).Version.RootFolderID: true}
		if _, err := f.svc.BackfillGrabOption(ctx, row.ID, tokens["MID"]); !refusedWith(err, "Manage files") {
			t.Errorf("unmanaged: %v", err)
		}
		f.lib.unmanaged = nil
		g.err = fmt.Errorf("%w: the download disk has 50.0 GB free", ErrNotEnoughSpace)
		if _, err := f.svc.BackfillGrabOption(ctx, row.ID, tokens["MID"]); !refusedWith(err, "50.0 GB free") {
			t.Errorf("no space: %v", err)
		}
		g.err = nil
		if len(g.got) != 0 {
			t.Fatalf("grabbed %v despite refusals", g.got)
		}
		// A token is for its own queue entry only.
		if _, err := f.svc.BackfillGrabOption(ctx, row.ID+1000, tokens["MID"]); !errors.Is(err, ErrOptionGone) {
			t.Errorf("another row's token: %v", err)
		}

		// Taken: outside the profile, and past both the queue
		// limit and backfill's quota (so are grabs counted, but not stopped).
		f.mode.QueueLimit = 1
		q.size = 1
		for range BackfillGrabsPerDay {
			if err := f.hist.Add(ctx, &Event{VersionID: &a, Event: EventGrabbed, CreatedAt: f.now}, EventData{}); err != nil {
				t.Fatal(err)
			}
		}
		msg, err := f.svc.BackfillGrabOption(ctx, row.ID, tokens["MOST"])
		if err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 1 || g.grabs[0].Release.Title != "The.IT.Crowd.S01E05.720p.HDTV.x264-MOST" {
			t.Fatalf("grabbed %v", g.got)
		}
		tg := g.grabs[0].TV
		if tg == nil || len(tg.EpisodeIDs) != 1 || tg.EpisodeIDs[0] != epID(f.sv(a), "S01E05") || tg.Label != "S01E05" {
			t.Errorf("grab = %+v", tg)
		}
		if !strings.Contains(msg, "Picked by you outside the profile: ") || !strings.Contains(msg, "profile accepts") {
			t.Errorf("message = %q", msg)
		}
		ev := f.events(t, EventGrabbed)[0]
		if ev.Message != msg || ev.SourceTitle != "The.IT.Crowd.S01E05.720p.HDTV.x264-MOST" {
			t.Errorf("History = %+v", ev)
		}
		// One use only.
		if _, err := f.svc.BackfillGrabOption(ctx, row.ID, tokens["MOST"]); !errors.Is(err, ErrOptionGone) {
			t.Errorf("reused token: %v", err)
		}
		// Offers expire.
		f.now = f.now.Add(optionsKept + time.Minute)
		if _, err := f.svc.BackfillGrabOption(ctx, row.ID, tokens["MID"]); !errors.Is(err, ErrOptionGone) {
			t.Errorf("expired token: %v", err)
		}
	})
}

func refusedWith(err error, text string) bool {
	var r BackfillRefusal
	return errors.As(err, &r) && strings.Contains(r.Error(), text)
}

func TestBackfillTokenCannotBeForged(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		g, _ := bfOn(f)
		a := bfShow(t, db, f, "The IT Crowd")
		row, _ := f.svc.BackfillAdd(ctx, a)
		for _, token := range []string{"", "0000", "https://evil.example/x.torrent", "magnet:?xt=urn:btih:abc"} {
			if _, err := f.svc.BackfillGrabOption(ctx, row.ID, token); !errors.Is(err, ErrOptionGone) {
				t.Errorf("token %q: %v", token, err)
			}
		}
		if len(g.got) != 0 {
			t.Errorf("grabbed %v with a forged token", g.got)
		}
	})
}

// Options include a finished season's packs for its single missing
// episodes, from one season search; taking one grabs it for every missing
// episode of the season.
func TestBackfillOptionsOfferSeasonPacks(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		g, _ := bfOn(f)
		a := bfShow(t, db, f, "The IT Crowd")
		row, _ := f.svc.BackfillAdd(ctx, a)
		f.idx.releases = nil
		runBackfill(t, f)

		f.idx.releases = []indexer.Release{tvRel("The.IT.Crowd.S01.720p.HDTV.x264-PACK", 4<<30, 90)}
		f.idx.queries = nil
		opts, err := f.svc.BackfillOptions(ctx, row.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got := queriesOf(f); !slices.Equal(got, []string{"S01E5", "S01E", "S01E6"}) {
			t.Errorf("queries = %v; the season once", got)
		}
		if len(opts.Units) != 2 {
			t.Fatalf("units = %+v", opts.Units)
		}
		for _, u := range opts.Units {
			if len(u.Options) != 1 || u.Options[0].Title != "The.IT.Crowd.S01.720p.HDTV.x264-PACK" {
				t.Errorf("%s options = %+v", u.Label, u.Options)
			}
			for _, r := range u.Options[0].Reasons {
				if strings.Contains(r, "whole season") {
					t.Errorf("%s: the pack is offered as a pack, not turned down for being one: %q", u.Label, r)
				}
			}
		}
		if _, err := f.svc.BackfillGrabOption(ctx, row.ID, opts.Units[1].Options[0].Token); err != nil {
			t.Fatal(err)
		}
		sv := f.sv(a)
		if len(g.grabs) != 1 || !slices.Equal(g.grabs[0].TV.EpisodeIDs, []int64{epID(sv, "S01E05"), epID(sv, "S01E06")}) {
			t.Errorf("grabs = %+v", g.grabs)
		}
	})
}
