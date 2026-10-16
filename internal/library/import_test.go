package library

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

func TestImportMovie(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		movie := f.titleByTMDB(t, 170) // 28 Days Later, with a 1080p file
		v := new(Version)
		if err := db.DB.NewSelect().Model(v).Where("title_id = ?", movie.ID).Scan(ctx); err != nil {
			t.Fatal(err)
		}
		var before []MediaFile
		_ = db.DB.NewSelect().Model(&before).Where("version_id = ?", v.ID).Scan(ctx)
		if len(before) != 1 {
			t.Fatalf("files before = %d", len(before))
		}

		// A finished download: the movie and a sample, in a folder.
		dl := t.TempDir()
		const rel = "28.Days.Later.2002.2160p.UHD.BluRay.x265-GRP"
		movieBytes := bytes.Repeat([]byte("m"), 3<<20)
		touch(t, dl, rel+"/28.Days.Later.2002.2160p.UHD.BluRay.x265-GRP.mkv", movieBytes)
		touch(t, dl, rel+"/Sample/sample.mkv", []byte("s"))
		req := ImportRequest{VersionID: v.ID, Source: filepath.Join(dl, rel), ReleaseName: rel}

		// The media folder is read-only until "Manage files" is on.
		if _, err := f.svc.ImportMovie(ctx, req); !errors.Is(err, ErrNotManaged) {
			t.Fatalf("unmanaged: %v", err)
		}
		if _, err := f.svc.UpdateRootFolder(ctx, f.hd.ID, 1, true, nil); err != nil {
			t.Fatal(err)
		}

		res, err := f.svc.ImportMovie(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		const want = "28 Days Later (2002)/28 Days Later (2002) [Bluray-2160p]-GRP.mkv"
		if res.Path != want {
			t.Errorf("imported as %q", res.Path)
		}
		got, err := os.ReadFile(filepath.Join(f.dir, want))
		if err != nil || !bytes.Equal(got, movieBytes) {
			t.Fatalf("the copy differs from the download (%v)", err)
		}
		// The download is untouched (it keeps seeding).
		if !exists(filepath.Join(dl, rel, "28.Days.Later.2002.2160p.UHD.BluRay.x265-GRP.mkv")) {
			t.Error("the download was moved or deleted")
		}
		// The old file is moved aside, not deleted.
		if len(res.Replaced) != 1 || !strings.HasPrefix(res.Replaced[0], ReplacedDir+"/") ||
			!exists(filepath.Join(f.dir, res.Replaced[0])) || exists(filepath.Join(f.dir, before[0].Path)) {
			t.Errorf("replaced = %v", res.Replaced)
		}
		var after []MediaFile
		_ = db.DB.NewSelect().Model(&after).Where("version_id = ?", v.ID).Scan(ctx)
		if len(after) != 1 || after[0].Path != want || after[0].Quality != "Bluray-2160p" || after[0].Size != int64(len(movieBytes)) {
			t.Errorf("recorded %+v", after)
		}
		// No temporary file is left behind.
		entries, _ := os.ReadDir(filepath.Join(f.dir, "28 Days Later (2002)"))
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".reelkeeper-importing-") {
				t.Errorf("left %s", e.Name())
			}
		}

		// A movie file Reelkeeper doesn't know about in the folder (copied in by
		// hand) stops the import, and nothing changes.
		other := "28.Days.Later.2002.1080p.WEB-DL.H.264-NEW"
		touch(t, dl, other+".mkv", []byte("web"))
		touch(t, f.dir, "28 Days Later (2002)/28 Days Later (2002) [WEBDL-1080p]-NEW.mkv", []byte("someone else's"))
		_, err = f.svc.ImportMovie(ctx, ImportRequest{VersionID: v.ID, Source: filepath.Join(dl, other+".mkv"), ReleaseName: other})
		if err == nil || !strings.Contains(err.Error(), "Reelkeeper doesn't know it") {
			t.Errorf("foreign file: %v", err)
		}
		if !exists(filepath.Join(f.dir, "28 Days Later (2002)/28 Days Later (2002) [WEBDL-1080p]-NEW.mkv")) {
			t.Error("a refused import touched the unknown file")
		}
		if !exists(filepath.Join(f.dir, want)) {
			t.Error("a refused import touched the current file")
		}

		// A download with no movie in it.
		touch(t, dl, "empty/readme.txt", []byte("x"))
		if _, err := f.svc.ImportMovie(ctx, ImportRequest{VersionID: v.ID, Source: filepath.Join(dl, "empty")}); err == nil ||
			!strings.Contains(err.Error(), "no movie file") {
			t.Errorf("empty download: %v", err)
		}
	})
}

// A release whose name looks like an ordinary Blu-ray can hold a disc
// image (Insidious, 2026-10-12). It isn't imported unless the profile
// allows BR-DISK, and the movie's file stays as it was.
func TestImportRefusesDiscImage(t *testing.T) {
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
		v := new(Version)
		if err := db.DB.NewSelect().Model(v).Where("title_id = ?", movie.ID).Scan(ctx); err != nil {
			t.Fatal(err)
		}
		var before []MediaFile
		_ = db.DB.NewSelect().Model(&before).Where("version_id = ?", v.ID).Scan(ctx)

		dl := t.TempDir()
		const rel = "28.Days.Later.2002.1080p.BluRay-R-US"
		touch(t, dl, rel+"/28.Days.Later.2002.1080p.BluRay-R-US.iso", bytes.Repeat([]byte("d"), 3<<20))
		_, err := f.svc.ImportMovie(ctx, ImportRequest{VersionID: v.ID, Source: filepath.Join(dl, rel), ReleaseName: rel})
		if err == nil || !strings.Contains(err.Error(), "disc image") {
			t.Fatalf("err = %v", err)
		}
		var after []MediaFile
		_ = db.DB.NewSelect().Model(&after).Where("version_id = ?", v.ID).Scan(ctx)
		if len(after) != len(before) || after[0].Path != before[0].Path || !exists(filepath.Join(f.dir, before[0].Path)) {
			t.Errorf("the movie's file changed: %+v", after)
		}
	})
}

func TestImportCancelledLeavesNothing(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		ctx := context.Background()
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.UpdateRootFolder(ctx, f.hd.ID, 1, true, nil); err != nil {
			t.Fatal(err)
		}
		movie := f.titleByTMDB(t, 170)
		v := new(Version)
		_ = db.DB.NewSelect().Model(v).Where("title_id = ?", movie.ID).Scan(ctx)
		dl := t.TempDir()
		touch(t, dl, "28.Days.Later.2002.2160p.BluRay.x265-GRP.mkv", bytes.Repeat([]byte("m"), 1<<20))
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, err := f.svc.ImportMovie(cancelled, ImportRequest{VersionID: v.ID, Source: filepath.Join(dl, "28.Days.Later.2002.2160p.BluRay.x265-GRP.mkv")})
		if err == nil {
			t.Fatal("a cancelled import succeeded")
		}
		entries, _ := os.ReadDir(filepath.Join(f.dir, "28 Days Later (2002)"))
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".reelkeeper") {
				t.Errorf("left %s", e.Name())
			}
		}
	})
}

func TestEmptyReplaced(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		now := f.svc.now()
		old := now.Add(-31 * 24 * time.Hour).UTC().Format(replacedStamp)
		recent := now.Add(-2 * 24 * time.Hour).UTC().Format(replacedStamp)
		touch(t, f.dir, ReplacedDir+"/"+old+"/Movie (2001)/old.mkv", []byte("x"))
		touch(t, f.dir, ReplacedDir+"/"+recent+"/Movie (2001)/newer.mkv", []byte("x"))
		touch(t, f.dir, ReplacedDir+"/not-ours/keep.mkv", []byte("x"))

		// Read-only media folder: untouched.
		if got, _ := f.svc.EmptyReplaced(ctx, 30*24*time.Hour, false); len(got) != 0 {
			t.Fatalf("emptied in an unmanaged folder: %v", got)
		}
		if _, err := f.svc.UpdateRootFolder(ctx, f.hd.ID, 1, true, nil); err != nil {
			t.Fatal(err)
		}
		got, err := f.svc.EmptyReplaced(ctx, 30*24*time.Hour, true)
		if err != nil || len(got) != 1 || !exists(filepath.Join(f.dir, ReplacedDir, old)) {
			t.Fatalf("dry run = %v, %v (and must not delete)", got, err)
		}
		if got, err = f.svc.EmptyReplaced(ctx, 30*24*time.Hour, false); err != nil || len(got) != 1 {
			t.Fatalf("empty = %v, %v", got, err)
		}
		if exists(filepath.Join(f.dir, ReplacedDir, old)) || !exists(filepath.Join(f.dir, ReplacedDir, recent)) ||
			!exists(filepath.Join(f.dir, ReplacedDir, "not-ours")) {
			t.Error("deleted the wrong batch")
		}
	})
}

// A download that isn't where the client said it was is reported with the
// client's name, whichever client it is.
func TestMissingDownloadNamesTheClient(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "not-there")
	for name, want := range map[string]string{
		"Transmission": "the download isn't where Transmission says (",
		"":             "the download isn't where the download client says (",
	} {
		if _, _, err := mainVideo(gone, name); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("mainVideo, client %q: %v", name, err)
		}
		if _, _, err := episodeVideos(gone, name); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("episodeVideos, client %q: %v", name, err)
		}
	}
}
