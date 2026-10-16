package library

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

const daysLater = "28 Days Later (2002)/28.Days.Later.2002.1080p.BluRay.DDP5.1.x265.10bit-GRP.mkv"

func TestDeleteMovieKeepingFiles(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		movie := f.titleByTMDB(t, 170)
		res, err := f.svc.DeleteMovie(ctx, movie.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Deleted) != 0 || len(res.Skipped) != 1 || !exists(filepath.Join(f.dir, daysLater)) {
			t.Fatalf("result = %+v", res)
		}
		if _, err := f.svc.MovieDetail(ctx, movie.ID); !errors.Is(err, ErrNotFound) {
			t.Error("the movie is still there")
		}
		// The next scan leaves the folder alone...
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		if n, _ := db.DB.NewSelect().Model((*Title)(nil)).Where("tmdb_id = 170").Count(ctx); n != 0 {
			t.Error("a scan added the deleted movie back")
		}
		// ...until it's un-skipped.
		list, _ := f.svc.SkippedFolders(ctx)
		if len(list) != 1 || list[0].Path != "28 Days Later (2002)" || !strings.Contains(list[0].Title, "28 Days Later") {
			t.Fatalf("skipped = %+v", list)
		}
		if err := f.svc.UnskipFolder(ctx, list[0].ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		f.titleByTMDB(t, 170) // back
	})
}

func TestDeleteMovieWithFiles(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		touch(t, f.dir, "28 Days Later (2002)/28.Days.Later.2002.1080p.BluRay.DDP5.1.x265.10bit-GRP.en.srt", []byte("subs"))
		touch(t, f.dir, "28 Days Later (2002)/poster.jpg", []byte("art"))
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		movie := f.titleByTMDB(t, 170)

		// Read-only media folder: nothing is deleted, the movie stays.
		if _, err := f.svc.DeleteMovie(ctx, movie.ID, true); !errors.Is(err, ErrNotManaged) {
			t.Fatalf("unmanaged: %v", err)
		}
		if !exists(filepath.Join(f.dir, daysLater)) {
			t.Fatal("deleted a file in a folder Reelkeeper doesn't manage")
		}
		f.titleByTMDB(t, 170)

		if _, err := f.svc.UpdateRootFolder(ctx, f.hd.ID, 1, true, nil); err != nil {
			t.Fatal(err)
		}
		res, err := f.svc.DeleteMovie(ctx, movie.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		if exists(filepath.Join(f.dir, "28 Days Later (2002)")) {
			t.Error("the movie's folder is still there")
		}
		if len(res.Skipped) != 0 || len(res.Deleted) != 3 { // video, subtitles, folder
			t.Errorf("result = %+v", res)
		}
		if list, _ := f.svc.SkippedFolders(ctx); len(list) != 0 {
			t.Error("a folder whose files were deleted was put on the skip list")
		}
	})
}

func TestDeleteMovieKeepsFoldersWithOtherVideos(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.UpdateRootFolder(ctx, f.hd.ID, 1, true, nil); err != nil {
			t.Fatal(err)
		}
		movie := f.titleByTMDB(t, 170)
		// Something else turns up in the folder after the scan.
		other := "28 Days Later (2002)/Something.Else.2010.720p.WEB-DL.mkv"
		touch(t, f.dir, other, []byte("x"))
		if _, err := f.svc.DeleteMovie(ctx, movie.ID, true); err != nil {
			t.Fatal(err)
		}
		if exists(filepath.Join(f.dir, daysLater)) || !exists(filepath.Join(f.dir, other)) {
			t.Error("deleted the wrong file, or the folder with another video in it")
		}
	})
}

func TestDeleteOneVersion(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		movie := f.titleByTMDB(t, 170)
		// A Remux version too (no files, in the same media folder).
		m, err := f.svc.AddMovie(ctx, 170, 2, f.hd.ID, true)
		if err != nil || len(m.Versions) != 2 {
			t.Fatalf("add remux: %+v, %v", m, err)
		}
		var remux, hd VersionView
		for _, v := range m.Versions {
			if v.ProfileID == 2 {
				remux = v
			} else {
				hd = v
			}
		}
		// Removing the Remux version keeps the movie, its HD file and its
		// folder (which HD still uses), and skips nothing.
		res, err := f.svc.DeleteVersion(ctx, movie.ID, remux.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Skipped) != 0 || len(res.Deleted) != 0 {
			t.Errorf("result = %+v", res)
		}
		d, err := f.svc.MovieDetail(ctx, movie.ID)
		if err != nil || len(d.Versions) != 1 || d.Versions[0].ID != hd.ID || d.Versions[0].Files != 1 {
			t.Fatalf("after = %+v, %v", d, err)
		}
		// The last version takes the movie with it.
		if _, err := f.svc.DeleteVersion(ctx, movie.ID, hd.ID, false); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.MovieDetail(ctx, movie.ID); !errors.Is(err, ErrNotFound) {
			t.Error("the movie should go with its last version")
		}
		if _, err := f.svc.DeleteVersion(ctx, movie.ID, 99999, false); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown version: %v", err)
		}
	})
}
