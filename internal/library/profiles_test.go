package library

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/validation"
)

func fieldError(t *testing.T, err error, field string) string {
	t.Helper()
	errs, ok := validation.As(err)
	if !ok || errs[field] == "" {
		t.Fatalf("want a %q field error, got %v", field, err)
	}
	return errs[field]
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"HD":             "hd",
		"  4K  Remux! ":  "4k-remux",
		"Kids -- Movies": "kids-movies",
		"Été":            "t",
		"???":            "profile",
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateProfile(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		svc := newFixture(t, db).svc

		p, err := svc.CreateProfile(ctx, "  4K Remux ", "movie")
		if err != nil || p.Name != "4K Remux" || p.Slug != "4k-remux" || p.AppliesTo != "movie" {
			t.Fatalf("create = %+v, %v", p, err)
		}
		// Slugs that collide get a number: "!!!" and "???" both fall back to "profile".
		a, _ := svc.CreateProfile(ctx, "!!!", "both")
		b, err := svc.CreateProfile(ctx, "???", "both")
		if err != nil || a.Slug != "profile" || b.Slug != "profile-2" {
			t.Errorf("slugs = %q, %q (%v)", a.Slug, b.Slug, err)
		}
		// "Hd!" would slug to "hd", which the seeded HD profile owns.
		c, err := svc.CreateProfile(ctx, "Hd!", "both")
		if err != nil || c.Slug != "hd-2" {
			t.Errorf("collision slug = %+v, %v", c, err)
		}

		_, err = svc.CreateProfile(ctx, "", "both")
		fieldError(t, err, "name")
		_, err = svc.CreateProfile(ctx, strings.Repeat("x", 41), "both")
		fieldError(t, err, "name")
		if _, err = svc.CreateProfile(ctx, strings.Repeat("x", 40), "both"); err != nil {
			t.Errorf("40 characters should be fine: %v", err)
		}
		_, err = svc.CreateProfile(ctx, "remux", "both") // case-insensitive duplicate
		fieldError(t, err, "name")
		_, err = svc.CreateProfile(ctx, "Fresh", "anime")
		fieldError(t, err, "appliesTo")
	})
}

func TestUpdateProfile(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		svc := f.svc

		p, err := svc.UpdateProfile(ctx, 1, "High definition", "both")
		if err != nil || p.Name != "High definition" || p.Slug != "hd" {
			t.Fatalf("rename = %+v, %v (slug must stay)", p, err)
		}
		// Renaming to its own name in another case is not a duplicate.
		if _, err := svc.UpdateProfile(ctx, 1, "HIGH DEFINITION", "both"); err != nil {
			t.Errorf("same name, new case: %v", err)
		}
		_, err = svc.UpdateProfile(ctx, 1, "remux", "both")
		fieldError(t, err, "name")
		_, err = svc.UpdateProfile(ctx, 999, "Nope", "both")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown profile: %v", err)
		}

		// Profile 1 is used by a movie folder (and, after a scan, movies).
		msg := fieldError(t, mustErr(svc.UpdateProfile(ctx, 1, "HD", "series")), "appliesTo")
		if !strings.Contains(msg, "1 media folder") {
			t.Errorf("message = %q", msg)
		}
		if _, err := svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		msg = fieldError(t, mustErr(svc.UpdateProfile(ctx, 1, "HD", "series")), "appliesTo")
		if !strings.Contains(msg, "1 media folder and 10 movies") {
			t.Errorf("message = %q", msg)
		}
		// Remux (profile 2) is unused, so it may narrow either way.
		if _, err := svc.UpdateProfile(ctx, 2, "Remux", "series"); err != nil {
			t.Errorf("unused profile should narrow: %v", err)
		}
	})
}

func mustErr(_ *Profile, err error) error { return err }

func TestDeleteProfile(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		svc := f.svc

		msg := fieldError(t, svc.DeleteProfile(ctx, 1), "profile")
		if !strings.Contains(msg, "HD is used by 1 media folder.") {
			t.Errorf("message = %q", msg)
		}
		if _, err := svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		msg = fieldError(t, svc.DeleteProfile(ctx, 1), "profile")
		if !strings.Contains(msg, "HD is used by 1 media folder and 10 movies. Switch those media folders") {
			t.Errorf("message = %q", msg)
		}
		if err := svc.DeleteProfile(ctx, 999); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown: %v", err)
		}

		// Remux is unused: it goes. Nothing else changes.
		if err := svc.DeleteProfile(ctx, 2); err != nil {
			t.Fatalf("delete unused: %v", err)
		}
		ps, _ := svc.Profiles(ctx)
		if len(ps) != 1 || ps[0].ID != 1 {
			t.Errorf("profiles = %+v", ps)
		}

		// A lone profile can't go, even when nothing uses it.
		if err := svc.RemoveRootFolder(ctx, f.hd.ID); err != nil {
			t.Fatal(err)
		}
		msg = fieldError(t, svc.DeleteProfile(ctx, 1), "profile")
		if !strings.Contains(msg, "at least one") {
			t.Errorf("message = %q", msg)
		}
	})
}

func TestProfileUsageAndFit(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		svc := f.svc
		if _, err := svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		ps, err := svc.ProfilesWithUsage(ctx)
		if err != nil || len(ps) != 2 {
			t.Fatal(ps, err)
		}
		if u := ps[0].Usage; u.MediaFolders != 1 || u.Movies != 10 || u.Series != 0 {
			t.Errorf("HD usage = %+v", u)
		}
		if u := ps[1].Usage; u != (ProfileUsage{}) {
			t.Errorf("Remux usage = %+v", u)
		}

		// A TV-only profile is refused for movie folders and for adding movies.
		tv, err := svc.CreateProfile(ctx, "TV only", "series")
		if err != nil {
			t.Fatal(err)
		}
		_, err = svc.UpdateRootFolder(ctx, f.hd.ID, tv.ID, false)
		if msg := fieldError(t, err, "defaultProfileId"); !strings.Contains(msg, "TV only isn't for movies") {
			t.Errorf("message = %q", msg)
		}
		_, err = svc.AddMovie(ctx, 603, tv.ID, f.hd.ID, true)
		fieldError(t, err, "profileId")
	})
}

// Switching a media folder to another profile takes its movies along, so a
// rescan finds them where they are instead of adding empty duplicates, and
// the old profile can then be deleted.
func TestSwitchingFolderProfileMovesItsMovies(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		count := func(where string, args ...any) int {
			t.Helper()
			n, err := db.DB.NewSelect().Model((*Version)(nil)).Where(where, args...).Count(ctx)
			if err != nil {
				t.Fatal(err)
			}
			return int(n)
		}
		before := count("root_folder_id = ?", f.hd.ID)
		if before == 0 {
			t.Fatal("the scan should have found movies")
		}

		p1080, err := f.svc.CreateProfile(ctx, "1080p", Movie)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.UpdateRootFolder(ctx, f.hd.ID, p1080.ID, false); err != nil {
			t.Fatal(err)
		}
		if n := count("root_folder_id = ? AND profile_id = ?", f.hd.ID, p1080.ID); n != before {
			t.Errorf("%d of %d versions moved to the new profile", n, before)
		}
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		if n := count("root_folder_id = ?", f.hd.ID); n != before {
			t.Errorf("rescan left %d versions, want %d (no duplicates)", n, before)
		}
		if n, _ := db.DB.NewSelect().Model((*MediaFile)(nil)).
			Where("version_id IN (SELECT id FROM versions WHERE profile_id = ?)", p1080.ID).Count(ctx); n == 0 {
			t.Error("the files should belong to the moved versions")
		}
		if err := f.svc.DeleteProfile(ctx, 1); err != nil {
			t.Errorf("HD is no longer used and can be deleted: %v", err)
		}

		// 1917 also has a Remux copy in another folder: switching this
		// folder to Remux would give it two, so the switch is refused and
		// nothing changes.
		remuxDir := t.TempDir()
		touch(t, remuxDir, "1917 (2019)/1917 (2019) Remux-2160p.mkv", []byte("z"))
		remux, err := f.svc.AddRootFolder(ctx, RootInput{Path: remuxDir, MediaType: Movie, DefaultProfileID: 2})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Scan(ctx, remux.ID, nil); err != nil {
			t.Fatal(err)
		}
		_, err = f.svc.UpdateRootFolder(ctx, f.hd.ID, 2, false)
		var errs validation.Errors
		if !errors.As(err, &errs) || !strings.Contains(errs["defaultProfileId"], "1 movie here already has a Remux copy in another folder (1917)") {
			t.Errorf("err = %v", err)
		}
		if n := count("root_folder_id = ? AND profile_id = ?", f.hd.ID, p1080.ID); n != before {
			t.Errorf("a refused switch must not move anything (%d of %d left)", n, before)
		}
		if r, _ := f.svc.RootFolder(ctx, f.hd.ID); r.DefaultProfileID != p1080.ID {
			t.Error("a refused switch must not change the folder")
		}
	})
}
