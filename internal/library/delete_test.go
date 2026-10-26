package library

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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

// deleteDaysLater scans the fixture library, lets Reelkeeper manage its
// files, runs prepare (which sets up the folder as a test needs), then
// deletes 28 Days Later with its files.
func deleteDaysLater(t *testing.T, db *store.Store, prepare func(f *fixture)) (*fixture, DeleteResult) {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t, db)
	if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpdateRootFolder(ctx, f.hd.ID, 1, true, nil); err != nil {
		t.Fatal(err)
	}
	movie := f.titleByTMDB(t, 170)
	prepare(f)
	res, err := f.svc.DeleteMovie(ctx, movie.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(f.dir, daysLater)) {
		t.Error("the movie's own video is still there")
	}
	return f, res
}

func TestDeleteVideoLeavesOtherVideosWithTheSameStart(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"Movie.mkv", "Movie.2160p.mkv", "Movie.en.srt", "Movie.nfo", "Movie.mkv.part", "Moviegoer.srt"} {
		touch(t, dir, "M/"+n, []byte(n))
	}
	s := New(nil, nil, slog.New(slog.DiscardHandler))
	gone, err := s.deleteVideo(dir, "M/Movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range gone {
		names = append(names, filepath.Base(p))
	}
	slices.Sort(names)
	if want := []string{"Movie.en.srt", "Movie.mkv", "Movie.mkv.part", "Movie.nfo"}; !slices.Equal(names, want) {
		t.Errorf("deleted %v, want %v", names, want)
	}
	for _, keep := range []string{"Movie.2160p.mkv", "Moviegoer.srt"} {
		if b, err := os.ReadFile(filepath.Join(dir, "M", keep)); err != nil || string(b) != keep {
			t.Errorf("%s was touched: %q, %v", keep, b, err)
		}
	}
}

func TestDeleteMovieKeepsAnotherVersionsVideo(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		stem := strings.TrimSuffix(daysLater, ".mkv")
		f, res := deleteDaysLater(t, db, func(f *fixture) {
			touch(t, f.dir, stem+".en.srt", []byte("subs"))
			touch(t, f.dir, stem+".2160p.mkv", []byte("the 4K copy"))
		})
		if exists(filepath.Join(f.dir, stem+".en.srt")) {
			t.Error("the movie's subtitles are still there")
		}
		if b, err := os.ReadFile(filepath.Join(f.dir, stem+".2160p.mkv")); err != nil || string(b) != "the 4K copy" {
			t.Errorf("deleted another video named like the movie: %q, %v", b, err)
		}
		if len(res.Deleted) != 2 { // video and subtitles; the folder stays
			t.Errorf("result = %+v", res)
		}
	})
}

func TestDeleteMovieFolderSafety(t *testing.T) {
	folder := "28 Days Later (2002)"
	cases := []struct {
		name    string
		prepare func(t *testing.T, f *fixture)
		// keep is the file that must survive, relative to the media folder;
		// "" means the folder must go.
		keep string
	}{
		{"only sidecars and art", func(t *testing.T, f *fixture) {
			touch(t, f.dir, folder+"/poster.jpg", []byte("x"))
			touch(t, f.dir, folder+"/.DS_Store", []byte("x"))
			touch(t, f.dir, folder+"/._poster.mkv", []byte("macOS metadata"))
		}, ""},
		{"a video in a hidden folder", func(t *testing.T, f *fixture) {
			touch(t, f.dir, folder+"/.stash/Home.Movie.mkv", []byte("x"))
		}, folder + "/.stash/Home.Movie.mkv"},
		{"a hidden video", func(t *testing.T, f *fixture) {
			touch(t, f.dir, folder+"/.Home.Movie.mp4", []byte("x"))
		}, folder + "/.Home.Movie.mp4"},
		{"a video deeper than scans look", func(t *testing.T, f *fixture) {
			touch(t, f.dir, folder+"/a/b/c/d/e/f/Home.Movie.mkv", []byte("x"))
		}, folder + "/a/b/c/d/e/f/Home.Movie.mkv"},
		{"the movie's own extras", func(t *testing.T, f *fixture) {
			touch(t, f.dir, folder+"/Extras/a/b/c/d/e/Deep Extra.mkv", []byte("x"))
			touch(t, f.dir, folder+"/Sample/Sample.mkv", []byte("x"))
		}, ""},
		{"a video hidden in an extras folder", func(t *testing.T, f *fixture) {
			touch(t, f.dir, folder+"/Featurettes/.Home.Movie.mkv", []byte("x"))
		}, folder + "/Featurettes/.Home.Movie.mkv"},
		{"a video in a folder that isn't extras", func(t *testing.T, f *fixture) {
			touch(t, f.dir, folder+"/Plex Versions/Optimized/Other.mp4", []byte("x"))
		}, folder + "/Plex Versions/Optimized/Other.mp4"},
		{"a link to a folder elsewhere", func(t *testing.T, f *fixture) {
			outside := t.TempDir()
			touch(t, outside, "Other.mkv", []byte("x"))
			if err := os.Symlink(outside, filepath.Join(f.dir, folder, "elsewhere")); err != nil {
				t.Skip("no symlinks here:", err)
			}
		}, folder + "/elsewhere"},
		{"a link to a file", func(t *testing.T, f *fixture) {
			touch(t, f.dir, "target.txt", []byte("x"))
			if err := os.Symlink(filepath.Join(f.dir, "target.txt"), filepath.Join(f.dir, folder, "notes.txt")); err != nil {
				t.Skip("no symlinks here:", err)
			}
		}, folder + "/notes.txt"},
		{"a folder that can't be read", func(t *testing.T, f *fixture) {
			if os.Geteuid() == 0 {
				t.Skip("root reads any folder")
			}
			locked := filepath.Join(f.dir, folder, "locked")
			touch(t, locked, "Hidden.Away.mkv", []byte("x"))
			if err := os.Chmod(locked, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(locked, 0o750) })
		}, folder + "/locked"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, db *store.Store) {
				f, res := deleteDaysLater(t, db, func(f *fixture) { c.prepare(t, f) })
				gone := !exists(filepath.Join(f.dir, folder))
				if c.keep == "" {
					if !gone {
						t.Errorf("the folder should have gone; result = %+v", res)
					}
					return
				}
				if gone || !exists(filepath.Join(f.dir, c.keep)) {
					t.Errorf("deleted %s; result = %+v", c.keep, res)
				}
			})
		})
	}
}

func TestDeleteMovieKeepsAFolderThatIsALink(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		// The movie's folder is a link to a folder outside the media folder.
		outside := t.TempDir()
		real := filepath.Join(outside, "real")
		if err := os.Rename(filepath.Join(f.dir, "28 Days Later (2002)"), real); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, filepath.Join(f.dir, "28 Days Later (2002)")); err != nil {
			t.Skip("no symlinks here:", err)
		}
		touch(t, real, "poster.jpg", []byte("x"))
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.UpdateRootFolder(ctx, f.hd.ID, 1, true, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.DeleteMovie(ctx, f.titleByTMDB(t, 170).ID, true); err != nil {
			t.Fatal(err)
		}
		if !exists(filepath.Join(f.dir, "28 Days Later (2002)")) || !exists(filepath.Join(real, "poster.jpg")) {
			t.Error("removed a linked folder, or what it points to")
		}
	})
}
