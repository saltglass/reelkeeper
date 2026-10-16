package library

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// importFixture is a managed TV media folder with one series version and its
// episodes, for import tests.
type importFixture struct {
	*fixture
	root  *RootFolder
	show  *Title
	v     *Version
	ep    map[string]*Episode // "S01E02" → episode
	shows string              // the TV media folder
}

func newImportFixture(t *testing.T, db *store.Store, seriesType string, episodes map[string]string) *importFixture {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t, db)
	dir := t.TempDir()
	root, err := f.svc.AddRootFolder(ctx, RootInput{Path: dir, MediaType: Series, DefaultProfileID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if root, err = f.svc.UpdateRootFolder(ctx, root.ID, 1, true, nil); err != nil {
		t.Fatal(err)
	}
	now := f.svc.now()
	show := &Title{MediaType: Series, TMDBID: 1399, TVDBID: 121361, Title: "The Show", Year: 2020, SeriesType: seriesType,
		Genres: "[]", AltTitles: "[]", CreatedAt: now}
	if _, err := db.DB.NewInsert().Model(show).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	tf := &importFixture{fixture: f, root: root, show: show, ep: map[string]*Episode{}, shows: dir}
	for label, air := range episodes {
		var s, n int
		if _, err := fmt.Sscanf(label, "S%dE%d", &s, &n); err != nil {
			t.Fatal(err)
		}
		e := &Episode{TitleID: show.ID, TMDBID: s*100 + n, AiredSeason: s, AiredNumber: n, Season: ptr(s), Number: ptr(n),
			AirDate: air, Name: "Episode " + label}
		if _, err := db.DB.NewInsert().Model(e).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		tf.ep[label] = e
	}
	tf.v = &Version{TitleID: show.ID, ProfileID: 1, RootFolderID: root.ID, Monitored: true, Upgrades: true, CreatedAt: now}
	if _, err := db.DB.NewInsert().Model(tf.v).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	return tf
}

func (tf *importFixture) ids(labels ...string) []int64 {
	out := make([]int64, len(labels))
	for i, l := range labels {
		out[i] = tf.ep[l].ID
	}
	return out
}

// filesOfEpisode is the paths of the version's files holding an episode.
func (tf *importFixture) filesOfEpisode(t *testing.T, label string) []string {
	t.Helper()
	var paths []string
	err := tf.db.DB.NewSelect().Table("episode_files").ColumnExpr("mf.path").
		Join("JOIN media_files mf ON mf.id = episode_files.media_file_id").
		Where("episode_files.episode_id = ?", tf.ep[label].ID).Scan(context.Background(), &paths)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestImportSeasonPack(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		tf := newImportFixture(t, db, "standard", map[string]string{
			"S01E01": "2020-01-01", "S01E02": "2020-01-08", "S01E03": "2020-01-15", "S01E04": "2020-01-22", "S01E05": "2020-01-29",
		})
		// S01E05 already has a file: TV doesn't upgrade, so it's skipped.
		have := &MediaFile{VersionID: tf.v.ID, RootFolderID: tf.root.ID, Path: "The Show (2020)/Season 1/old.mkv", Size: 1,
			AddedAt: time.Now(), MediaInfo: "{}", ReleaseInfo: "{}", EpisodeClaim: "{}"}
		if _, err := db.DB.NewInsert().Model(have).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.NewInsert().Model(&EpisodeFile{EpisodeID: tf.ep["S01E05"].ID, MediaFileID: have.ID}).Exec(ctx); err != nil {
			t.Fatal(err)
		}

		dl := t.TempDir()
		const pack = "The.Show.S01.1080p.WEB-DL.DDP5.1.H.264-GRP"
		body := bytes.Repeat([]byte("e"), 2<<20)
		touch(t, dl, pack+"/The.Show.S01E01.1080p.WEB-DL.DDP5.1.H.264-GRP.mkv", body)
		touch(t, dl, pack+"/The.Show.S01E02E03.1080p.WEB-DL.DDP5.1.H.264-GRP.mkv", body)
		touch(t, dl, pack+"/The.Show.S01E04.1080p.WEB-DL.DDP5.1.H.264-GRP.mkv", body) // not grabbed for
		touch(t, dl, pack+"/The.Show.S01E05.1080p.WEB-DL.DDP5.1.H.264-GRP.mkv", body) // already has a file
		touch(t, dl, pack+"/Sample/the.show.s01e01.sample.mkv", []byte("s"))

		res, err := tf.svc.ImportEpisodes(ctx, EpisodeImportRequest{
			VersionID: tf.v.ID, Source: filepath.Join(dl, pack), ReleaseName: pack,
			EpisodeIDs: tf.ids("S01E01", "S01E02", "S01E03", "S01E05"),
		})
		if err != nil {
			t.Fatal(err)
		}
		folder := "The Show (2020) {tvdb-121361}/Season 1/"
		want := []string{
			folder + "The Show (2020) - S01E01 - Episode S01E01 [WEBDL-1080p][EAC3 5.1][h264]-GRP.mkv",
			folder + "The Show (2020) - S01E02-E03 - Episode S01E02 Episode S01E03 [WEBDL-1080p][EAC3 5.1][h264]-GRP.mkv",
		}
		if len(res.Imported) != len(want) {
			t.Fatalf("imported %v, skipped %v", res.Imported, res.Skipped)
		}
		for i, w := range want {
			if res.Imported[i] != w {
				t.Errorf("imported[%d] = %q, want %q", i, res.Imported[i], w)
			}
			if got, err := os.ReadFile(filepath.Join(tf.shows, res.Imported[i])); err != nil || !bytes.Equal(got, body) {
				t.Errorf("%s: the copy differs (%v)", res.Imported[i], err)
			}
		}
		if len(res.Skipped) != 2 || !strings.Contains(strings.Join(res.Skipped, "|"), "S01E05 already has a file") ||
			!strings.Contains(strings.Join(res.Skipped, "|"), "not an episode this download was grabbed for") {
			t.Errorf("skipped %v", res.Skipped)
		}
		// Recorded: one file for E01, one shared by E02 and E03; E04 none.
		if p := tf.filesOfEpisode(t, "S01E02"); len(p) != 1 || p[0] != res.Imported[1] {
			t.Errorf("S01E02 files %v", p)
		}
		if p := tf.filesOfEpisode(t, "S01E03"); len(p) != 1 || p[0] != res.Imported[1] {
			t.Errorf("S01E03 files %v", p)
		}
		if p := tf.filesOfEpisode(t, "S01E04"); len(p) != 0 {
			t.Errorf("S01E04 files %v", p)
		}
		// The download is untouched (it keeps seeding).
		if !exists(filepath.Join(dl, pack, "The.Show.S01E01.1080p.WEB-DL.DDP5.1.H.264-GRP.mkv")) {
			t.Error("the download was moved or deleted")
		}

		// Importing again does nothing and isn't an error: everything it
		// was for has a file.
		res, err = tf.svc.ImportEpisodes(ctx, EpisodeImportRequest{
			VersionID: tf.v.ID, Source: filepath.Join(dl, pack), ReleaseName: pack,
			EpisodeIDs: tf.ids("S01E01", "S01E02", "S01E03", "S01E05"),
		})
		if err != nil || len(res.Imported) != 0 {
			t.Errorf("again: %v, %v", res.Imported, err)
		}
	})
}

// A single-episode download is a lone file; its quality comes from the
// release name when the file's own name says less.
func TestImportSingleEpisodeFile(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		tf := newImportFixture(t, db, "standard", map[string]string{"S02E07": "2026-10-05"})
		dl := t.TempDir()
		touch(t, dl, "show.s02e07.mkv", []byte("x"))
		res, err := tf.svc.ImportEpisodes(ctx, EpisodeImportRequest{
			VersionID: tf.v.ID, Source: filepath.Join(dl, "show.s02e07.mkv"),
			ReleaseName: "The.Show.S02E07.720p.HDTV.x264-GRP", EpisodeIDs: tf.ids("S02E07"),
		})
		if err != nil || len(res.Imported) != 1 {
			t.Fatalf("%v, %v", res, err)
		}
		if !strings.HasPrefix(res.Imported[0], "The Show (2020) {tvdb-121361}/Season 2/The Show (2020) - S02E07 - ") ||
			!strings.Contains(res.Imported[0], "HDTV-720p") {
			t.Errorf("imported as %q", res.Imported[0])
		}
	})
}

func TestImportDailyEpisode(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		tf := newImportFixture(t, db, "daily", map[string]string{"S2026E120": "2026-10-08", "S2026E121": "2026-10-09"})
		dl := t.TempDir()
		const rel = "The.Show.2026.10.09.1080p.WEB.h264-GRP"
		touch(t, dl, rel+"/"+rel+".mkv", []byte("x"))
		res, err := tf.svc.ImportEpisodes(ctx, EpisodeImportRequest{
			VersionID: tf.v.ID, Source: filepath.Join(dl, rel), ReleaseName: rel, EpisodeIDs: tf.ids("S2026E121"),
		})
		if err != nil || len(res.Imported) != 1 {
			t.Fatalf("%v, %v", res, err)
		}
		if !strings.Contains(res.Imported[0], "/Season 2026/The Show (2020) - 2026-10-09 - ") {
			t.Errorf("imported as %q", res.Imported[0])
		}
		if p := tf.filesOfEpisode(t, "S2026E121"); len(p) != 1 {
			t.Errorf("not recorded: %v", p)
		}
	})
}

// A download with none of the wanted episodes fails, so it shows in the
// queue as needing attention instead of looking imported.
func TestImportEpisodesNoneWanted(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		tf := newImportFixture(t, db, "standard", map[string]string{"S01E01": "2020-01-01", "S01E02": "2020-01-08"})
		dl := t.TempDir()
		touch(t, dl, "The.Show.S01E02.1080p.WEB-DL.H.264-GRP.mkv", []byte("x"))
		_, err := tf.svc.ImportEpisodes(ctx, EpisodeImportRequest{
			VersionID: tf.v.ID, Source: filepath.Join(dl, "The.Show.S01E02.1080p.WEB-DL.H.264-GRP.mkv"),
			ReleaseName: "The.Show.S01E02.1080p.WEB-DL.H.264-GRP", EpisodeIDs: tf.ids("S01E01"),
		})
		if err == nil || !strings.Contains(err.Error(), "none of the download's files are the wanted episodes") {
			t.Errorf("err = %v", err)
		}
		// And a folder Reelkeeper doesn't manage is never written to.
		if _, err := tf.svc.UpdateRootFolder(ctx, tf.root.ID, 1, false, nil); err != nil {
			t.Fatal(err)
		}
		_, err = tf.svc.ImportEpisodes(ctx, EpisodeImportRequest{VersionID: tf.v.ID, Source: dl, EpisodeIDs: tf.ids("S01E02")})
		if !errors.Is(err, ErrNotManaged) {
			t.Errorf("unmanaged: %v", err)
		}
	})
}

// A fix (Replace) puts the new file in place of the one the episode has: the
// old file and its subtitle go to .reelkeeper-replaced, the record is
// swapped, and the result says what was replaced. Without Replace the
// episode is skipped, as before.
func TestImportEpisodesReplacesForAFix(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		tf := newImportFixture(t, db, "standard", map[string]string{"S01E01": "2020-01-01", "S01E02": "2020-01-08"})
		dl := t.TempDir()
		const orig = "The.Show.S01E01.1080p.WEB-DL.H.264-GRP"
		const fixed = "The.Show.S01E01.REPACK.1080p.WEB-DL.H.264-GRP"
		touch(t, dl, orig+".mkv", []byte("the broken one"))
		touch(t, dl, fixed+".mkv", []byte("the fixed one"))

		first, err := tf.svc.ImportEpisodes(ctx, EpisodeImportRequest{
			VersionID: tf.v.ID, Source: filepath.Join(dl, orig+".mkv"), ReleaseName: orig, EpisodeIDs: tf.ids("S01E01"),
		})
		if err != nil || len(first.Imported) != 1 {
			t.Fatalf("first import: %v, %v", first, err)
		}
		oldRel := first.Imported[0]
		sub := strings.TrimSuffix(oldRel, ".mkv") + ".en.srt"
		touch(t, tf.shows, sub, []byte("subtitles"))

		// Without Replace the episode keeps its file.
		res, err := tf.svc.ImportEpisodes(ctx, EpisodeImportRequest{
			VersionID: tf.v.ID, Source: filepath.Join(dl, fixed+".mkv"), ReleaseName: fixed, EpisodeIDs: tf.ids("S01E01"),
		})
		if err != nil || len(res.Imported) != 0 || len(res.Skipped) != 1 {
			t.Fatalf("without Replace: %v, %v", res, err)
		}

		res, err = tf.svc.ImportEpisodes(ctx, EpisodeImportRequest{
			VersionID: tf.v.ID, Source: filepath.Join(dl, fixed+".mkv"), ReleaseName: fixed, EpisodeIDs: tf.ids("S01E01"), Replace: true,
		})
		if err != nil || len(res.Imported) != 1 {
			t.Fatalf("with Replace: %v, %v", res, err)
		}
		newRel := res.Imported[0]
		if got, _ := os.ReadFile(filepath.Join(tf.shows, newRel)); string(got) != "the fixed one" {
			t.Errorf("the new file holds %q", got)
		}
		// The old file and its subtitle are aside, with their folders, and
		// gone from where they were.
		if len(res.Replaced) != 2 {
			t.Fatalf("replaced %v", res.Replaced)
		}
		for _, aside := range res.Replaced {
			if !strings.HasPrefix(aside, ReplacedDir+string(filepath.Separator)) || !exists(filepath.Join(tf.shows, aside)) {
				t.Errorf("%s isn't in %s", aside, ReplacedDir)
			}
		}
		if got, _ := os.ReadFile(filepath.Join(tf.shows, res.Replaced[0])); string(got) != "the broken one" {
			t.Errorf("the old file aside holds %q", got)
		}
		if oldRel != newRel && exists(filepath.Join(tf.shows, oldRel)) {
			t.Error("the old file is still in place")
		}
		if exists(filepath.Join(tf.shows, sub)) {
			t.Error("the old subtitle is still in place")
		}
		// The library knows only the new file.
		if p := tf.filesOfEpisode(t, "S01E01"); len(p) != 1 || p[0] != newRel {
			t.Errorf("S01E01 files %v, want [%s]", p, newRel)
		}
		if n, err := db.DB.NewSelect().Table("media_files").Where("version_id = ?", tf.v.ID).Count(ctx); err != nil || n != 1 {
			t.Errorf("%d media files, %v", n, err)
		}
		// The download is untouched.
		if !exists(filepath.Join(dl, fixed+".mkv")) {
			t.Error("the download was moved or deleted")
		}

		// Retrying a fix whose file has the same name as the one it replaces
		// (a failed import being retried) works too: the file is its own
		// replacement, and nothing is overwritten.
		again, err := tf.svc.ImportEpisodes(ctx, EpisodeImportRequest{
			VersionID: tf.v.ID, Source: filepath.Join(dl, fixed+".mkv"), ReleaseName: fixed, EpisodeIDs: tf.ids("S01E01"), Replace: true,
		})
		if err != nil || len(again.Imported) != 1 || again.Imported[0] != newRel || len(again.Replaced) != 1 {
			t.Fatalf("again: %v, %v", again, err)
		}
		if p := tf.filesOfEpisode(t, "S01E01"); len(p) != 1 || p[0] != newRel {
			t.Errorf("S01E01 files %v", p)
		}
	})
}

// A fix never takes a file from an episode it doesn't cover: a two-episode
// file isn't replaced by a release of one of them.
func TestImportEpisodesFixKeepsMultiEpisodeFile(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		tf := newImportFixture(t, db, "standard", map[string]string{"S01E01": "2020-01-01", "S01E02": "2020-01-08"})
		dl := t.TempDir()
		const both = "The.Show.S01E01E02.1080p.WEB-DL.H.264-GRP"
		const fixed = "The.Show.S01E01.REPACK.1080p.WEB-DL.H.264-GRP"
		touch(t, dl, both+".mkv", []byte("two episodes"))
		touch(t, dl, fixed+".mkv", []byte("one episode"))
		first, err := tf.svc.ImportEpisodes(ctx, EpisodeImportRequest{
			VersionID: tf.v.ID, Source: filepath.Join(dl, both+".mkv"), ReleaseName: both, EpisodeIDs: tf.ids("S01E01", "S01E02"),
		})
		if err != nil || len(first.Imported) != 1 {
			t.Fatalf("first import: %v, %v", first, err)
		}
		res, err := tf.svc.ImportEpisodes(ctx, EpisodeImportRequest{
			VersionID: tf.v.ID, Source: filepath.Join(dl, fixed+".mkv"), ReleaseName: fixed, EpisodeIDs: tf.ids("S01E01"), Replace: true,
		})
		if err != nil || len(res.Imported) != 0 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "also holds other episodes") {
			t.Fatalf("%v, %v", res, err)
		}
		if p := tf.filesOfEpisode(t, "S01E02"); len(p) != 1 || p[0] != first.Imported[0] || !exists(filepath.Join(tf.shows, p[0])) {
			t.Errorf("S01E02 files %v", p)
		}
	})
}

// A fix is refused in a media folder Reelkeeper doesn't manage, like any
// import.
func TestImportEpisodesFixRefusedWhenUnmanaged(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		tf := newImportFixture(t, db, "standard", map[string]string{"S01E01": "2020-01-01"})
		dl := t.TempDir()
		const orig = "The.Show.S01E01.1080p.WEB-DL.H.264-GRP"
		touch(t, dl, orig+".mkv", []byte("a"))
		first, err := tf.svc.ImportEpisodes(ctx, EpisodeImportRequest{
			VersionID: tf.v.ID, Source: filepath.Join(dl, orig+".mkv"), ReleaseName: orig, EpisodeIDs: tf.ids("S01E01"),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tf.svc.UpdateRootFolder(ctx, tf.root.ID, 1, false, nil); err != nil {
			t.Fatal(err)
		}
		touch(t, dl, "fixed.mkv", []byte("b"))
		_, err = tf.svc.ImportEpisodes(ctx, EpisodeImportRequest{
			VersionID: tf.v.ID, Source: filepath.Join(dl, "fixed.mkv"), ReleaseName: "The.Show.S01E01.REPACK.1080p.WEB-DL.H.264-GRP",
			EpisodeIDs: tf.ids("S01E01"), Replace: true,
		})
		if !errors.Is(err, ErrNotManaged) {
			t.Errorf("err = %v", err)
		}
		if !exists(filepath.Join(tf.shows, first.Imported[0])) || exists(filepath.Join(tf.shows, ReplacedDir)) {
			t.Error("something was moved in an unmanaged folder")
		}
	})
}
