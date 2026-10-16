package library

import (
	"context"
	"errors"
	"testing"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// upgradesOf maps each title's TMDB ID to whether its version has upgrades on.
func upgradesOf(t *testing.T, db *store.Store, where string, args ...any) map[int]bool {
	t.Helper()
	var rows []struct {
		TMDBID   int  `bun:"tmdb_id"`
		Upgrades bool `bun:"upgrades"`
	}
	err := db.DB.NewSelect().Model((*Version)(nil)).
		ColumnExpr("t.tmdb_id AS tmdb_id").ColumnExpr("version.upgrades AS upgrades").
		Join("JOIN titles t ON t.id = version.title_id").
		Where(where, args...).Scan(context.Background(), &rows)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]bool{}
	for _, r := range rows {
		out[r.TMDBID] = r.Upgrades
	}
	return out
}

func TestScanSetsUpgradesFromTheFolder(t *testing.T) {
	for _, upgradeFound := range []bool{false, true} {
		name := map[bool]string{false: "off", true: "on"}[upgradeFound]
		t.Run("movie folder "+name, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, db *store.Store) {
				ctx := context.Background()
				f := newFixture(t, db)
				if f.hd.UpgradeFound {
					t.Fatal("a movie folder added without a choice starts with upgrades off")
				}
				if _, err := f.svc.UpdateRootFolder(ctx, f.hd.ID, 1, false, &upgradeFound); err != nil {
					t.Fatal(err)
				}
				if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
					t.Fatal(err)
				}
				got := upgradesOf(t, db, "version.root_folder_id = ?", f.hd.ID)
				if len(got) != 10 {
					t.Fatalf("scan found %d movies, want 10", len(got))
				}
				for id, on := range got {
					if on != upgradeFound {
						t.Errorf("movie %d: upgrades = %v, want %v", id, on, upgradeFound)
					}
				}

				// A movie accepted from the review screen follows the folder too.
				items, _ := f.svc.ReviewItems(ctx)
				for _, it := range items {
					if it.Path != "Dune" {
						continue
					}
					if _, err := f.svc.ResolveReview(ctx, it.ID, 841); err != nil {
						t.Fatal(err)
					}
				}
				if got := upgradesOf(t, db, "version.root_folder_id = ?", f.hd.ID); got[841] != upgradeFound {
					t.Errorf("accepted from review: upgrades = %v, want %v", got[841], upgradeFound)
				}
			})
		})
	}
}

func TestSeriesFolderIgnoresUpgradeFound(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newTVFixture(t, db)
		if !f.tv.UpgradeFound {
			t.Error("a series folder stores true, whatever was asked")
		}
		if _, err := f.svc.Scan(ctx, f.tv.ID, nil); err != nil {
			t.Fatal(err)
		}
		got := upgradesOf(t, db, "version.root_folder_id = ?", f.tv.ID)
		if len(got) == 0 {
			t.Fatal("the scan should have found series")
		}
		for id, on := range got {
			if !on {
				t.Errorf("series %d: upgrades = false, want true", id)
			}
		}
		// Switching it does nothing to a series folder.
		off := false
		if r, err := f.svc.UpdateRootFolder(ctx, f.tv.ID, 1, false, &off); err != nil || !r.UpgradeFound {
			t.Errorf("series folder after switching: %+v, %v", r, err)
		}
		for id, on := range upgradesOf(t, db, "version.root_folder_id = ?", f.tv.ID) {
			if !on {
				t.Errorf("series %d: upgrades = false after switching", id)
			}
		}
	})
}

func TestAddMovieAlwaysHasUpgradesOn(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db) // upgrades off for this folder
		if _, err := f.svc.AddMovie(context.Background(), 693134, 1, f.hd.ID, true); err != nil {
			t.Fatal(err)
		}
		if got := upgradesOf(t, db, "version.root_folder_id = ?", f.hd.ID); !got[693134] {
			t.Error("a movie added by hand (or Seerr) is upgraded whatever the folder says")
		}
	})
}

func TestEnsureVersionFollowsTheFolder(t *testing.T) {
	for _, upgradeFound := range []bool{false, true} {
		name := map[bool]string{false: "off", true: "on"}[upgradeFound]
		t.Run("folder "+name, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, db *store.Store) {
				ctx := context.Background()
				f := newFixture(t, db)
				if _, err := f.svc.UpdateRootFolder(ctx, f.hd.ID, 1, false, &upgradeFound); err != nil {
					t.Fatal(err)
				}
				// A movie the folder already has, added by hand: upgrades on.
				if _, err := f.svc.AddMovie(ctx, 693134, 1, f.hd.ID, true); err != nil {
					t.Fatal(err)
				}
				// The Radarr import: a new version follows the folder, an
				// existing one is returned as it is.
				v, created, err := f.svc.EnsureVersion(ctx, 438631, 1, f.hd.ID, "", true)
				if err != nil || !created || v.Upgrades != upgradeFound {
					t.Errorf("new version = %+v, created %v, %v; want upgrades %v", v, created, err, upgradeFound)
				}
				v, created, err = f.svc.EnsureVersion(ctx, 693134, 1, f.hd.ID, "", true)
				if err != nil || created || !v.Upgrades {
					t.Errorf("existing version = %+v, created %v, %v; want it untouched (upgrades on)", v, created, err)
				}
			})
		})
	}
}

func TestUpdateRootFolderSwitchesUpgradesForThatFolderOnly(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		otherDir := t.TempDir()
		touch(t, otherDir, "Underworld (2003)/Underworld (2003).mkv", []byte("u"))
		other, err := f.svc.AddRootFolder(ctx, RootInput{Path: otherDir, MediaType: Movie, DefaultProfileID: 2, UpgradeFound: true})
		if err != nil || !other.UpgradeFound {
			t.Fatalf("other folder = %+v, %v", other, err)
		}
		if _, err := f.svc.Scan(ctx, other.ID, nil); err != nil {
			t.Fatal(err)
		}
		// One movie in the first folder was downloaded by Reelkeeper later,
		// with upgrades on: the switch covers it too.
		if _, err := f.svc.AddMovie(ctx, 693134, 1, f.hd.ID, true); err != nil {
			t.Fatal(err)
		}

		count := func(rootID int64, on bool) int {
			n := 0
			for _, v := range upgradesOf(t, db, "version.root_folder_id = ?", rootID) {
				if v == on {
					n++
				}
			}
			return n
		}
		total := len(upgradesOf(t, db, "version.root_folder_id = ?", f.hd.ID))
		if total != 11 || count(f.hd.ID, false) != 10 || count(f.hd.ID, true) != 1 {
			t.Fatalf("before: %d versions, %d off, %d on", total, count(f.hd.ID, false), count(f.hd.ID, true))
		}

		// nil leaves everything alone.
		if _, err := f.svc.UpdateRootFolder(ctx, f.hd.ID, 1, false, nil); err != nil {
			t.Fatal(err)
		}
		if count(f.hd.ID, false) != 10 || count(other.ID, true) != 1 {
			t.Error("nil must change nothing")
		}
		// The same value changes nothing either.
		off := false
		if _, err := f.svc.UpdateRootFolder(ctx, f.hd.ID, 1, false, &off); err != nil || count(f.hd.ID, true) != 1 {
			t.Errorf("setting the value it already has: %v, %d on", err, count(f.hd.ID, true))
		}

		on := true
		r, err := f.svc.UpdateRootFolder(ctx, f.hd.ID, 1, false, &on)
		if err != nil || !r.UpgradeFound {
			t.Fatalf("turn on: %+v, %v", r, err)
		}
		if count(f.hd.ID, true) != total || count(other.ID, true) != 1 {
			t.Errorf("after on: folder has %d of %d on, other has %d on", count(f.hd.ID, true), total, count(other.ID, true))
		}
		if r, _ := f.svc.RootFolder(ctx, f.hd.ID); !r.UpgradeFound {
			t.Error("the folder's own setting should be saved")
		}

		r, err = f.svc.UpdateRootFolder(ctx, f.hd.ID, 1, false, &off)
		if err != nil || r.UpgradeFound {
			t.Fatalf("turn off: %+v, %v", r, err)
		}
		if count(f.hd.ID, false) != total || count(other.ID, true) != 1 {
			t.Errorf("after off: folder has %d of %d off, other has %d on", count(f.hd.ID, false), total, count(other.ID, true))
		}
	})
}

func TestSetUpgrades(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		m, err := f.svc.AddMovie(ctx, 693134, 1, f.hd.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		vid := m.Versions[0].ID
		if err := f.svc.SetUpgrades(ctx, m.ID, vid, false); err != nil {
			t.Fatal(err)
		}
		if got := upgradesOf(t, db, "version.id = ?", vid); got[693134] {
			t.Error("upgrades should be off")
		}
		if err := f.svc.SetUpgrades(ctx, m.ID, 99999, true); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown version: %v", err)
		}
	})
}
