package library

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/mediainfo"
	"github.com/saltglass/reelkeeper/internal/naming"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

// onlyFile returns the single file rename planned for a movie.
func onlyFile(t *testing.T, plan []VersionRename) (VersionRename, FileRename) {
	t.Helper()
	if len(plan) != 1 || len(plan[0].Files) != 1 {
		t.Fatalf("plan = %+v, want one version with one file", plan)
	}
	return plan[0], plan[0].Files[0]
}

func TestRename(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		const video = "28 Days Later (2002)/28.Days.Later.2002.1080p.BluRay.DDP5.1.x265.10bit-GRP.mkv"
		const subs = "28 Days Later (2002)/28.Days.Later.2002.1080p.BluRay.DDP5.1.x265.10bit-GRP.en.srt"
		touch(t, f.dir, subs, []byte("1"))
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		movie := f.titleByTMDB(t, 170)

		plan, err := f.svc.RenamePlan(ctx, movie.ID)
		if err != nil {
			t.Fatal(err)
		}
		v, fr := onlyFile(t, plan)
		const want = "28 Days Later (2002)/28 Days Later (2002) [Bluray-1080p]-GRP.mkv"
		if fr.From != video || fr.To != want || fr.Problem != "" {
			t.Errorf("plan = %+v", fr)
		}
		if len(fr.Extras) != 1 || fr.Extras[0].To != "28 Days Later (2002)/28 Days Later (2002) [Bluray-1080p]-GRP.en.srt" {
			t.Errorf("subtitles should go along: %+v", fr.Extras)
		}
		if v.Managed {
			t.Error("new media folders are read-only")
		}

		// Read-only folder: nothing happens.
		if _, err := f.svc.Rename(ctx, movie.ID); err != nil {
			t.Fatal(err)
		}
		if !exists(filepath.Join(f.dir, video)) || exists(filepath.Join(f.dir, want)) {
			t.Fatal("renamed a file in a folder Reelkeeper doesn't manage")
		}

		if _, err := f.svc.UpdateRootFolder(ctx, f.hd.ID, 1, true, nil); err != nil {
			t.Fatal(err)
		}
		done, err := f.svc.Rename(ctx, movie.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, fr := onlyFile(t, done); !fr.Done || fr.Problem != "" {
			t.Fatalf("outcome = %+v", fr)
		}
		if exists(filepath.Join(f.dir, video)) || !exists(filepath.Join(f.dir, want)) {
			t.Error("the video wasn't renamed")
		}
		if exists(filepath.Join(f.dir, subs)) || !exists(filepath.Join(f.dir, "28 Days Later (2002)/28 Days Later (2002) [Bluray-1080p]-GRP.en.srt")) {
			t.Error("the subtitles weren't renamed with the video")
		}
		if files := f.filesOf(t, 170); len(files) != 1 || files[0].Path != want {
			t.Errorf("library still has %+v", files)
		}

		// Names are now right: nothing left to do, and a rescan sees no change.
		plan, _ = f.svc.RenamePlan(ctx, movie.ID)
		if len(plan) != 1 || len(plan[0].Files) != 0 {
			t.Errorf("second plan = %+v", plan)
		}
		res, err := f.svc.Scan(ctx, f.hd.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Missing != 0 || res.Added != 0 {
			t.Errorf("rescan after rename: %+v", res)
		}
	})
}

func TestRenameNeverOverwrites(t *testing.T) {
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
		plan, _ := f.svc.RenamePlan(ctx, movie.ID)
		_, fr := onlyFile(t, plan)
		touch(t, f.dir, fr.To, []byte("someone else's file"))

		done, err := f.svc.Rename(ctx, movie.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, got := onlyFile(t, done); got.Done || !strings.Contains(got.Problem, "never overwrites") {
			t.Errorf("outcome = %+v", got)
		}
		if b, _ := os.ReadFile(filepath.Join(f.dir, fr.To)); string(b) != "someone else's file" {
			t.Error("the existing file was overwritten")
		}
		if !exists(filepath.Join(f.dir, fr.From)) {
			t.Error("the original file must stay put")
		}
	})
}

func TestRenameSkipsMissingFilesAndUsesTemplates(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		f.svc.SetNaming(func() naming.Templates {
			return naming.Templates{MovieFolder: "{Movie Title} [{TmdbId}]", MovieFile: "{Movie.Title}.{Release.Year}"}
		})
		movie := f.titleByTMDB(t, 170)
		now := time.Now()
		if _, err := db.DB.NewUpdate().Model((*MediaFile)(nil)).Set("missing_since = ?", now).
			Where("version_id IN (SELECT id FROM versions WHERE title_id = ?)", movie.ID).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		plan, _ := f.svc.RenamePlan(ctx, movie.ID)
		if _, fr := onlyFile(t, plan); fr.To != "28 Days Later (2002)/28.Days.Later.2002.mkv" || !strings.Contains(fr.Problem, "missing") {
			t.Errorf("plan = %+v", fr)
		}

		// A movie added by hand gets its folder from the folder template.
		m, err := f.svc.AddMovie(ctx, 841, 1, f.hd.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Versions[0].Folder; got != "Dune [841]" {
			t.Errorf("folder = %q", got)
		}
	})
}

func TestFileFacts(t *testing.T) {
	info := mediainfo.Info{
		Video: &mediainfo.Video{Codec: "H.265", Width: 3840, Height: 1600, BitDepth: 10, HDR: []string{"DV", "HDR10"}},
		Audio: []mediainfo.Audio{{Codec: "DTS", Channels: 8}},
	}
	f := MediaFile{
		Quality: "Remux-2160p", Source: "remux", ReleaseGroup: "FraMeSToR",
		ReleaseInfo: `{"audio":["DTS-HD MA"],"channels":"5.1","hdr":["HDR10+"],"videoCodec":"H.264"}`,
		MediaInfo:   mustJSON(t, info),
	}
	got := fileFacts(f)
	if got.VideoCodec != "H.265" || got.AudioCodec != "DTS-HD MA" || got.AudioChannels != "7.1" || got.VideoBitDepth != 10 {
		t.Errorf("headers should win, the name fills in detail: %+v", got)
	}
	if strings.Join(got.HDR, " ") != "DV HDR10 HDR10+" {
		t.Errorf("HDR = %v", got.HDR)
	}

	// Name only: Dolby Digital Plus with Atmos, in Radarr's words.
	got = fileFacts(MediaFile{ReleaseInfo: `{"audio":["DD+","Atmos"],"channels":"5.1"}`})
	if got.AudioCodec != "EAC3 Atmos" || got.AudioChannels != "5.1" {
		t.Errorf("name only: %+v", got)
	}
	// The file disagrees with the name: trust the file.
	info.Audio = []mediainfo.Audio{{Codec: "TrueHD", Channels: 8}}
	got = fileFacts(MediaFile{ReleaseInfo: `{"audio":["DTS","Atmos"]}`, MediaInfo: mustJSON(t, info)})
	if got.AudioCodec != "TrueHD Atmos" {
		t.Errorf("file wins: %+v", got)
	}
}

// A renamed file scores by what it really is: its name, then what its
// headers say and the release group it came from.
func TestScoringName(t *testing.T) {
	info := mediainfo.Info{
		Video: &mediainfo.Video{Codec: "H.265", Width: 3840, Height: 2160, BitDepth: 10, HDR: []string{"DV", "HDR10"}, DVProfile: 7},
		Audio: []mediainfo.Audio{{Codec: "TrueHD", Channels: 8}},
	}
	f := MediaFile{Path: "Tron Ares (2025)/Tron Ares (2025) (2160p) REMUX.mkv", Quality: "Remux-2160p", MediaInfo: mustJSON(t, info)}
	if got, want := f.ScoringName(), "Tron Ares (2025) (2160p) REMUX DV HDR10 H.265 10bit TrueHD 7.1"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// The group it was grabbed as, unless the name already says it; Atmos
	// from the track's name.
	info.Audio[0].Title = "TrueHD Atmos 7.1"
	f.MediaInfo, f.ReleaseGroup = mustJSON(t, info), "FraMeSToR"
	if got, want := f.ScoringName(), "Tron Ares (2025) (2160p) REMUX DV HDR10 H.265 10bit TrueHD Atmos 7.1-FraMeSToR"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	f.Path = "Tron.Ares.2025.2160p.UHD.BluRay.REMUX.DV.HDR10.HEVC.TrueHD.7.1.Atmos-FraMeSToR.mkv"
	if got := f.ScoringName(); strings.Count(got, "FraMeSToR") != 1 {
		t.Errorf("group twice: %q", got)
	}

	// Never read (or unreadable): just the name.
	if got := (MediaFile{Path: "Heat (1995)/Heat (1995).mkv"}).ScoringName(); got != "Heat (1995)" {
		t.Errorf("unprobed = %q", got)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRenameNoReplace(t *testing.T) {
	for name, rename := range map[string]func(from, to string) error{
		"renameNoReplace": renameNoReplace,
		"linkThenUnlink":  linkThenUnlink, // the fallback for NFS and the like
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			touch(t, dir, "a.mkv", []byte("a"))
			touch(t, dir, "b.mkv", []byte("b"))
			a, b, c := filepath.Join(dir, "a.mkv"), filepath.Join(dir, "b.mkv"), filepath.Join(dir, "c.mkv")
			if err := rename(a, b); !errors.Is(err, fs.ErrExist) {
				t.Errorf("onto an existing file: %v, want fs.ErrExist", err)
			}
			for p, want := range map[string]string{a: "a", b: "b"} {
				if got, _ := os.ReadFile(p); string(got) != want {
					t.Errorf("%s holds %q, want %q", filepath.Base(p), got, want)
				}
			}
			if err := rename(a, c); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(c); exists(a) || string(got) != "a" {
				t.Errorf("after renaming: a there = %v, c = %q", exists(a), got)
			}
		})
	}
	// A change of case only works whether or not the filesystem ignores case.
	dir := t.TempDir()
	touch(t, dir, "movie.mkv", []byte("m"))
	if err := renameNoReplace(filepath.Join(dir, "movie.mkv"), filepath.Join(dir, "Movie.mkv")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "Movie.mkv" {
		t.Errorf("after a case-only rename the folder holds %v", entries)
	}
}

// renameRace scans the fixture library with subtitles next to 28 Days
// Later and lets Reelkeeper manage its files, ready for a rename.
func renameRace(t *testing.T, db *store.Store) (*fixture, *Title) {
	t.Helper()
	f := newFixture(t, db)
	touch(t, f.dir, raceSubs, []byte("subs"))
	if _, err := f.svc.Scan(context.Background(), f.hd.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpdateRootFolder(context.Background(), f.hd.ID, 1, true, nil); err != nil {
		t.Fatal(err)
	}
	return f, f.titleByTMDB(t, 170)
}

const (
	raceVideo    = "28 Days Later (2002)/28.Days.Later.2002.1080p.BluRay.DDP5.1.x265.10bit-GRP.mkv"
	raceSubs     = "28 Days Later (2002)/28.Days.Later.2002.1080p.BluRay.DDP5.1.x265.10bit-GRP.en.srt"
	raceNewVideo = "28 Days Later (2002)/28 Days Later (2002) [Bluray-1080p]-GRP.mkv"
	raceNewSubs  = "28 Days Later (2002)/28 Days Later (2002) [Bluray-1080p]-GRP.en.srt"
)

func TestRenameNeverOverwritesAFileThatAppearsMeanwhile(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, movie := renameRace(t, db)
		// Another program creates a file at the new name after the plan
		// (and any check) but before the rename.
		f.svc.beforeRename = func(from, to string) {
			if to == filepath.Join(f.dir, raceNewVideo) && !exists(to) {
				touch(t, f.dir, raceNewVideo, []byte("someone else's"))
			}
		}
		done, err := f.svc.Rename(ctx, movie.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, fr := onlyFile(t, done); fr.Done || !strings.Contains(fr.Problem, "already there") {
			t.Errorf("outcome = %+v", fr)
		}
		if got, _ := os.ReadFile(filepath.Join(f.dir, raceNewVideo)); string(got) != "someone else's" {
			t.Errorf("the file that appeared now holds %q", got)
		}
		if !exists(filepath.Join(f.dir, raceVideo)) || !exists(filepath.Join(f.dir, raceSubs)) {
			t.Error("the video or its subtitles moved anyway")
		}
		if files := f.filesOf(t, 170); len(files) != 1 || files[0].Path != raceVideo {
			t.Errorf("library has %+v", files)
		}
	})
}

func TestRenameNeverOverwritesASidecarNameTakenMeanwhile(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, movie := renameRace(t, db)
		f.svc.beforeRename = func(from, to string) {
			if to == filepath.Join(f.dir, raceNewSubs) && !exists(to) {
				touch(t, f.dir, raceNewSubs, []byte("someone else's"))
			}
		}
		done, err := f.svc.Rename(ctx, movie.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, fr := onlyFile(t, done); !fr.Done || !strings.Contains(fr.Problem, "the new name is taken") {
			t.Errorf("outcome = %+v", fr)
		}
		if got, _ := os.ReadFile(filepath.Join(f.dir, raceNewSubs)); string(got) != "someone else's" {
			t.Errorf("the file that appeared now holds %q", got)
		}
		if got, _ := os.ReadFile(filepath.Join(f.dir, raceSubs)); string(got) != "subs" {
			t.Errorf("the subtitles now hold %q", got)
		}
		if !exists(filepath.Join(f.dir, raceNewVideo)) {
			t.Error("the video wasn't renamed")
		}
	})
}

func TestRenameUndoNeverOverwrites(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f, movie := renameRace(t, db)
		// Recording the new name fails (the request is cancelled after the
		// rename), so the rename is undone...
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.svc.beforeRename = func(from, to string) {
			if to == filepath.Join(f.dir, raceNewVideo) {
				cancel()
			}
		}
		done, err := f.svc.Rename(ctx, movie.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, fr := onlyFile(t, done); fr.Done || !strings.Contains(fr.Problem, "Couldn't record") {
			t.Errorf("outcome = %+v", fr)
		}
		if !exists(filepath.Join(f.dir, raceVideo)) || exists(filepath.Join(f.dir, raceNewVideo)) {
			t.Error("the failed rename wasn't undone")
		}

		// ...unless another program has taken the old name meanwhile: then
		// the video stays at its new name and the newcomer is left alone.
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
		f.svc.beforeRename = func(from, to string) {
			switch to {
			case filepath.Join(f.dir, raceNewVideo):
				cancel()
			case filepath.Join(f.dir, raceVideo):
				touch(t, f.dir, raceVideo, []byte("someone else's"))
			}
		}
		if _, err := f.svc.Rename(ctx, movie.ID); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(filepath.Join(f.dir, raceVideo)); string(got) != "someone else's" {
			t.Errorf("undoing overwrote the newcomer: it holds %q", got)
		}
		if !exists(filepath.Join(f.dir, raceNewVideo)) {
			t.Error("the video is gone")
		}
	})
}

// ignoresCase reports whether dir's filesystem treats names differing only
// in case as the same name (macOS and Windows by default; not Linux).
func ignoresCase(t *testing.T, dir string) bool {
	t.Helper()
	p := filepath.Join(dir, "case-probe")
	touch(t, dir, "case-probe", nil)
	defer os.Remove(p)
	return exists(filepath.Join(dir, "CASE-PROBE"))
}

// listNames lists a folder's entries by their exact names.
func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// createNew writes a new file, never replacing one.
func createNew(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return
		}
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", filepath.Base(path), err)
	}
	return string(b)
}

func TestRenameCaseOnlyRefusesAnotherHardLink(t *testing.T) {
	dir := t.TempDir()
	lower, upper := filepath.Join(dir, "movie.mkv"), filepath.Join(dir, "Movie.mkv")
	createNew(t, lower, "the movie")
	if ignoresCase(t, dir) {
		t.Skip("this filesystem ignores case, so the two names can't both exist")
	}
	// Two names, one file: os.Rename "succeeds" here and changes nothing.
	if err := os.Link(lower, upper); err != nil {
		t.Skip("no hard links here:", err)
	}
	if err := renameNoReplace(lower, upper); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("renameNoReplace = %v, want fs.ErrExist", err)
	}
	if got := listNames(t, dir); !slices.Equal(got, []string{"Movie.mkv", "movie.mkv"}) {
		t.Errorf("folder holds %v, want both names and nothing else", got)
	}
	if readFile(t, lower) != "the movie" || readFile(t, upper) != "the movie" {
		t.Error("a name lost its contents")
	}
}

func TestRenameCaseOnly(t *testing.T) {
	setup := func(t *testing.T) (dir, from, to string) {
		dir = t.TempDir()
		from, to = filepath.Join(dir, "movie.mkv"), filepath.Join(dir, "Movie.mkv")
		createNew(t, from, "the movie")
		t.Cleanup(func() { caseRenameStep = nil })
		return dir, from, to
	}
	// tmpOf finds the temporary name in a folder, or "".
	tmpOf := func(t *testing.T, dir string) string {
		for _, n := range listNames(t, dir) {
			if strings.HasPrefix(n, ".reelkeeper-rename-") {
				return filepath.Join(dir, n)
			}
		}
		return ""
	}

	t.Run("succeeds, and the name really changes", func(t *testing.T) {
		dir, from, to := setup(t)
		if err := renameCaseOnly(from, to); err != nil {
			t.Fatal(err)
		}
		if got := listNames(t, dir); !slices.Equal(got, []string{"Movie.mkv"}) {
			t.Errorf("folder holds %v, want only Movie.mkv", got)
		}
		if readFile(t, to) != "the movie" {
			t.Error("contents changed")
		}
	})

	t.Run("the new name appears between the steps", func(t *testing.T) {
		dir, from, to := setup(t)
		caseRenameStep = func(string) { createNew(t, to, "someone else's") }
		err := renameCaseOnly(from, to)
		if readFile(t, to) != "someone else's" {
			t.Error("the file that appeared was overwritten")
		}
		if ignoresCase(t, dir) {
			// The newcomer also holds the old name: the file stays at the
			// temporary name, and the error says where.
			var se *strandedError
			tmp := tmpOf(t, dir)
			if !errors.As(err, &se) || tmp == "" || se.At != tmp || !strings.Contains(err.Error(), tmp) {
				t.Fatalf("err = %v, temporary file = %q", err, tmp)
			}
			if readFile(t, tmp) != "the movie" {
				t.Error("the movie lost its contents")
			}
			return
		}
		// The file goes back to its old name.
		if !errors.Is(err, fs.ErrExist) {
			t.Fatalf("err = %v, want fs.ErrExist", err)
		}
		if readFile(t, from) != "the movie" {
			t.Error("the movie isn't back at its old name")
		}
		if got := listNames(t, dir); !slices.Equal(got, []string{"Movie.mkv", "movie.mkv"}) {
			t.Errorf("folder holds %v, want the newcomer and the movie only", got)
		}
	})

	t.Run("both names are taken between the steps", func(t *testing.T) {
		dir, from, to := setup(t)
		caseRenameStep = func(string) {
			createNew(t, to, "someone else's")
			createNew(t, from, "someone else's") // the same name where case is ignored
		}
		err := renameCaseOnly(from, to)
		var se *strandedError
		tmp := tmpOf(t, dir)
		if !errors.As(err, &se) || tmp == "" || !strings.Contains(err.Error(), tmp) {
			t.Fatalf("err = %v, temporary file = %q", err, tmp)
		}
		if errors.Is(err, fs.ErrExist) {
			t.Error("a stranded file must not read as a plain \"name taken\", which hides where it is")
		}
		if readFile(t, tmp) != "the movie" || readFile(t, to) != "someone else's" || readFile(t, from) != "someone else's" {
			t.Error("something was overwritten")
		}
	})
}
