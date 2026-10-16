package library

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

func TestMovieViews(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		movies, err := f.svc.Movies(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(movies) != 10 {
			t.Fatalf("movies = %d", len(movies))
		}
		// Sorted ignoring "The": "1917", "28 Days Later", ... "The Dark Knight" under D.
		if movies[0].Title.Title != "1917" {
			t.Errorf("first = %s", movies[0].Title.Title)
		}
		var b *MovieView
		for i := range movies {
			if movies[i].TMDBID == 365177 {
				b = &movies[i]
			}
		}
		if b == nil || len(b.Versions) != 1 || b.Versions[0].Files != 2 || b.Versions[0].ProfileName != "HD" || b.Versions[0].Quality != "2160p" {
			t.Errorf("Borderlands view = %+v", b)
		}

		d, err := f.svc.MovieDetail(ctx, b.ID)
		if err != nil || len(d.Versions[0].MediaFiles) != 2 {
			t.Fatalf("detail = %+v, %v", d, err)
		}
		if _, err := f.svc.MovieDetail(ctx, 999999); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown movie: %v", err)
		}
	})
}

func TestAddLookupMonitorRemove(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}

		// Lookup marks what's already here; "Dune 2021" narrows by year.
		res, err := f.svc.Lookup(ctx, "Dune 2021")
		if err != nil || len(res) != 1 || res[0].ID != 438631 || res[0].MovieID == 0 {
			t.Errorf("lookup = %+v, %v", res, err)
		}

		// Add Dune: Part Two (not on disk) to HD.
		m, err := f.svc.AddMovie(ctx, 693134, 1, f.hd.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Versions) != 1 || m.Versions[0].Folder != "Dune - Part Two (2024)" || m.Versions[0].Files != 0 || !m.Versions[0].Monitored {
			t.Errorf("added = %+v", m.Versions)
		}
		if _, err := f.svc.AddMovie(ctx, 693134, 1, f.hd.ID, true); !errors.Is(err, ErrInUse) {
			t.Errorf("adding the same version twice: %v", err)
		}
		if _, err := f.svc.AddMovie(ctx, 693134, 99, f.hd.ID, true); err == nil {
			t.Error("unknown profile should be refused")
		}
		// Another version of it (Remux) is fine.
		m, err = f.svc.AddMovie(ctx, 693134, 2, f.hd.ID, false)
		if err != nil || len(m.Versions) != 2 {
			t.Fatalf("second version: %+v, %v", m, err)
		}

		if err := f.svc.SetMonitored(ctx, m.ID, m.Versions[1].ID, true); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.SetMonitored(ctx, m.ID, 99999, true); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown version: %v", err)
		}

		// Versions without files can be removed; the movie goes with the last.
		if err := f.svc.RemoveVersion(ctx, m.ID, m.Versions[1].ID); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.RemoveVersion(ctx, m.ID, m.Versions[0].ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.MovieDetail(ctx, m.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("movie should be gone with its last version: %v", err)
		}
		// Versions with files can't.
		dune := f.titleByTMDB(t, 438631)
		d, _ := f.svc.MovieDetail(ctx, dune.ID)
		if err := f.svc.RemoveVersion(ctx, dune.ID, d.Versions[0].ID); !errors.Is(err, ErrHasFiles) {
			t.Errorf("removing a version with files: %v", err)
		}
	})
}

func TestRefresh(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		dune := f.titleByTMDB(t, 438631)
		f.tmdb.mu.Lock()
		f.tmdb.movies[438631].Overview = "Updated overview"
		f.tmdb.mu.Unlock()

		m, err := f.svc.RefreshMovie(ctx, dune.ID)
		if err != nil || m.Overview != "Updated overview" || m.ID != dune.ID {
			t.Errorf("refresh = %+v, %v", m, err)
		}

		// Nothing is stale yet; a week later everything is.
		if n, _ := f.svc.RefreshStale(ctx, 7*24*time.Hour, 100, nil); n != 0 {
			t.Errorf("fresh library refreshed %d", n)
		}
		f.svc.now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
		if n, err := f.svc.RefreshStale(ctx, 7*24*time.Hour, 3, nil); err != nil || n != 3 {
			t.Errorf("stale refresh = %d, %v (limit 3)", n, err)
		}
	})
}

// A movie added by hand whose folder already holds a copied-in file gets
// that file at once, so Reelkeeper doesn't search for a movie that's there.
func TestAddMovieAdoptsFilesInItsFolder(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		touch(t, f.dir, "Dune - Part Two (2024)/Dune.Part.Two.2024.1080p.WEB-DL.H.264-GRP.mkv", []byte("movie"))
		touch(t, f.dir, "Dune - Part Two (2024)/Sample/sample.mkv", []byte("s"))
		m, err := f.svc.AddMovie(ctx, 693134, 1, f.hd.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Versions) != 1 || m.Versions[0].Files != 1 {
			t.Fatalf("added = %+v", m.Versions)
		}
		// A second version in the same folder doesn't claim the file too.
		m, err = f.svc.AddMovie(ctx, 693134, 2, f.hd.ID, true)
		if err != nil || len(m.Versions) != 2 || m.Versions[0].Files+m.Versions[1].Files != 1 {
			t.Errorf("second version = %+v, %v", m, err)
		}
	})
}
