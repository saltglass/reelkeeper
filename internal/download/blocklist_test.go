package download

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/jobs"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// block adds an entry for a title; temporary ones last TemporaryBlockLife.
func (f *dfix) block(t *testing.T, titleID int64, name, hash, rule string, temporary bool) {
	t.Helper()
	err := f.svc.AddToBlocklist(context.Background(), BlockedRelease{TitleID: titleID, ReleaseTitle: name, Hash: hash,
		Indexer: "Tracker", Quality: "WEBDL-1080p", Reason: "Because " + rule, Rule: rule, Temporary: temporary})
	if err != nil {
		t.Fatal(err)
	}
}

// Temporary entries stop blocking when they run out (even before the daily
// task deletes them); permanent ones never do.
func TestBlocklistExpiry(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		start := time.Now().UTC()
		f.svc.now = func() time.Time { return start }
		id := f.mv.Title.ID
		f.block(t, id, "Stuck.Release", "", "stalled", true)
		f.block(t, id, "Mine.Release", "", BlockRemoved, false)
		f.block(t, id, "Gone.Release", "", "unregistered", false)

		blocked, _ := f.svc.Blocklisted(ctx, id)
		if !blocked("Stuck.Release", "") || !blocked("Mine.Release", "") || !blocked("Gone.Release", "") {
			t.Fatal("a fresh entry doesn't block")
		}
		page, _ := f.svc.Blocklist(ctx, BlocklistQuery{})
		if page.Total != 3 || page.Temporary != 1 || len(page.Items) != 3 {
			t.Fatalf("page = %+v", page)
		}
		// Newest first: Gone, Mine, Stuck. Only the stuck one expires.
		if g, m, s := page.Items[0], page.Items[1], page.Items[2]; !g.Permanent() || !m.Permanent() || s.Permanent() ||
			!s.ExpiresAt.Equal(s.CreatedAt.Add(30*24*time.Hour)) {
			t.Errorf("entries = %+v", page.Items)
		}

		// Nothing has expired after 29 days.
		f.svc.now = func() time.Time { return start.Add(29 * 24 * time.Hour) }
		if n, err := f.svc.ExpireBlocklist(ctx); err != nil || n != 0 {
			t.Errorf("expired after 29 days = %d, %v", n, err)
		}

		// After 31 days it no longer blocks, whether or not it was deleted yet.
		f.svc.now = func() time.Time { return start.Add(31 * 24 * time.Hour) }
		blocked, _ = f.svc.Blocklisted(ctx, id)
		if blocked("Stuck.Release", "") || !blocked("Mine.Release", "") || !blocked("Gone.Release", "") {
			t.Error("expired entry still blocks, or a permanent one stopped")
		}
		if page, _ = f.svc.Blocklist(ctx, BlocklistQuery{}); page.Total != 2 || page.Temporary != 0 {
			t.Errorf("page after expiry = %+v", page)
		}
		if n, err := f.svc.ExpireBlocklist(ctx); err != nil || n != 1 {
			t.Errorf("expired = %d, %v", n, err)
		}
		if n, _ := db.DB.NewSelect().Model((*BlockedRelease)(nil)).Count(ctx); n != 2 {
			t.Errorf("%d rows left, want the 2 permanent ones", n)
		}
		if n, err := f.svc.ExpireBlocklist(ctx); err != nil || n != 0 {
			t.Errorf("second run expired %d, %v", n, err)
		}
	})
}

// Entries of a title with nothing wanted are dormant: they still block, but
// are only listed when asked for. TV entries say which episodes they were for.
func TestBlocklistDormantAndEpisodes(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		now := time.Now().UTC()
		show := &library.Title{MediaType: library.Series, TMDBID: 1399, Title: "The Expanse", Year: 2015, CreatedAt: now, Genres: "[]", AltTitles: "[]"}
		if _, err := db.DB.NewInsert().Model(show).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		const hashS = "cccccccccccccccccccccccccccccccccccccccc"
		d := &Download{CreatedAt: now, TitleID: &show.ID, Hash: hashS, Category: "tv", ReleaseTitle: "The.Expanse.S02.WEB", Status: StatusRemoved,
			EpisodesLabel: "season 2 (8 wanted episodes)"}
		if _, err := db.DB.NewInsert().Model(d).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		movie, tv := f.mv.Title.ID, show.ID
		f.block(t, movie, "Ronin.Stuck", "", "slow", true)
		f.block(t, tv, "The.Expanse.S02.WEB", hashS, "stalled", true)
		f.block(t, tv, "The.Expanse.S03.WEB", "", BlockSonarr, false)

		// Without a way to tell, nothing is dormant.
		page, _ := f.svc.Blocklist(ctx, BlocklistQuery{})
		if page.Total != 3 || page.Dormant != 0 || len(page.Items) != 3 {
			t.Fatalf("no wanted test: %+v", page)
		}

		// Only the movie still wants something.
		wanted := map[int64]bool{movie: true}
		f.svc.SetWantedTitles(func(context.Context) (map[int64]bool, error) { return wanted, nil })
		page, _ = f.svc.Blocklist(ctx, BlocklistQuery{})
		if page.Total != 3 || page.Dormant != 2 || page.Temporary != 2 || len(page.Items) != 1 || page.Items[0].ReleaseTitle != "Ronin.Stuck" || page.Items[0].Dormant {
			t.Fatalf("active only: %+v", page)
		}
		page, _ = f.svc.Blocklist(ctx, BlocklistQuery{IncludeDormant: true})
		if len(page.Items) != 3 {
			t.Fatalf("with dormant: %+v", page)
		}
		// Newest first: the Sonarr one, then the stalled season, then the movie.
		sonarr, season, ronin := page.Items[0], page.Items[1], page.Items[2]
		if !sonarr.Dormant || sonarr.Episodes != "" || sonarr.MediaType != "series" {
			t.Errorf("sonarr entry = %+v", sonarr)
		}
		if season.Episodes != "season 2 (8 wanted episodes)" || !season.Dormant || season.MediaType != "series" {
			t.Errorf("season entry = %+v", season)
		}
		if ronin.Episodes != "" || ronin.MediaType != "movie" {
			t.Errorf("movie entry = %+v", ronin)
		}

		// One title's entries, dormant or not; the counts are for that title.
		page, _ = f.svc.Blocklist(ctx, BlocklistQuery{TitleID: tv})
		if page.Total != 2 || page.Dormant != 2 || page.Temporary != 1 || len(page.Items) != 0 {
			t.Errorf("one title, active only: %+v", page)
		}
		page, _ = f.svc.Blocklist(ctx, BlocklistQuery{TitleID: tv, IncludeDormant: true})
		if len(page.Items) != 2 || page.Items[0].TitleID != tv {
			t.Errorf("one title: %+v", page)
		}

		// It wants something again: no longer dormant.
		wanted[tv] = true
		page, _ = f.svc.Blocklist(ctx, BlocklistQuery{})
		if page.Dormant != 0 || len(page.Items) != 3 {
			t.Errorf("after it wants something: %+v", page)
		}
	})
}

// Unblocking the temporary entries leaves permanent ones, and can be limited
// to one title (dormant ones are included).
func TestUnblockTemporary(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		now := time.Now().UTC()
		other := &library.Title{MediaType: library.Movie, TMDBID: 603, Title: "The Matrix", Year: 1999, CreatedAt: now, Genres: "[]", AltTitles: "[]"}
		if _, err := db.DB.NewInsert().Model(other).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		f.svc.SetWantedTitles(func(context.Context) (map[int64]bool, error) { return map[int64]bool{}, nil }) // all dormant
		a, b := f.mv.Title.ID, other.ID
		f.block(t, a, "A.Temp", "", "stalled", true)
		f.block(t, a, "A.Perm", "", BlockRemoved, false)
		f.block(t, b, "B.Temp1", "", "slow", true)
		f.block(t, b, "B.Temp2", "", "metadata", true)

		if n, err := f.svc.UnblockTemporary(ctx, b); err != nil || n != 2 {
			t.Fatalf("unblocked for one title = %d, %v", n, err)
		}
		page, _ := f.svc.Blocklist(ctx, BlocklistQuery{IncludeDormant: true})
		if page.Total != 2 || page.Temporary != 1 {
			t.Fatalf("after one title: %+v", page)
		}
		if n, err := f.svc.UnblockTemporary(ctx, 0); err != nil || n != 1 {
			t.Fatalf("unblocked all = %d, %v", n, err)
		}
		if n, err := f.svc.UnblockTemporary(ctx, 0); err != nil || n != 0 {
			t.Fatalf("unblocked again = %d, %v", n, err)
		}
		page, _ = f.svc.Blocklist(ctx, BlocklistQuery{IncludeDormant: true})
		if page.Total != 1 || page.Items[0].ReleaseTitle != "A.Perm" {
			t.Errorf("what's left: %+v", page)
		}
	})
}

// A deleted title takes its blocklist entries with it (the table's foreign
// key; deleting a title in the library is that one delete).
func TestBlocklistGoesWithTheTitle(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		f.block(t, f.mv.Title.ID, "Ronin.Stuck", "", "slow", true)
		f.block(t, f.mv.Title.ID, "Ronin.Mine", "", BlockRemoved, false)
		if _, err := db.DB.NewDelete().Model((*library.Title)(nil)).Where("id = ?", f.mv.Title.ID).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if n, _ := db.DB.NewSelect().Model((*BlockedRelease)(nil)).Count(ctx); n != 0 {
			t.Errorf("%d entries outlived their title", n)
		}
	})
}

// The daily task deletes expired entries, says so, and stays quiet in the
// Tasks list when there was nothing to delete.
func TestExpireTask(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		m := jobs.New(db, slog.New(slog.DiscardHandler))
		m.Register(f.svc.expireTask())
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { _ = m.Start(runCtx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })

		// The scheduler runs the task once as soon as it starts; let that
		// finish, so it can't land in the middle of the runs below.
		for range 500 {
			js, _, _ := m.Recent(ctx, 10, true)
			if slices.ContainsFunc(js, func(j jobs.Job) bool { return j.Kind == ExpireKind && j.Status.Finished() }) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}

		run := func() jobs.Job {
			t.Helper()
			j, _, err := m.Enqueue(ctx, ExpireKind, nil, jobs.Schedule, "")
			if err != nil {
				t.Fatal(err)
			}
			for range 500 {
				got, err := m.Get(ctx, j.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status.Finished() {
					return *got
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("the task didn't finish")
			return jobs.Job{}
		}

		start := time.Now().UTC()
		f.svc.now = func() time.Time { return start }
		f.block(t, f.mv.Title.ID, "Ronin.Stuck", "", "slow", true)
		if j := run(); j.Status != jobs.Succeeded || !j.Quiet || j.Message != "No blocklist entries had expired." {
			t.Errorf("nothing expired: %+v", j)
		}
		f.svc.now = func() time.Time { return start.Add(31 * 24 * time.Hour) }
		if j := run(); j.Status != jobs.Succeeded || j.Quiet || j.Message != "Removed 1 expired blocklist entry." {
			t.Errorf("one expired: %+v", j)
		}
	})
}
