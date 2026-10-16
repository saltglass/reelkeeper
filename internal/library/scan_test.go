package library

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/metadata"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// fakeTMDB answers from a small in-memory catalogue and counts calls.
type fakeTMDB struct {
	mu      sync.Mutex
	movies  map[int]*metadata.Movie
	imdb    map[string]int
	series  map[int]*metadata.Series
	tvdb    map[int]int
	groups  map[string]*metadata.EpisodeGroup
	calls   int
	failKey bool
}

func newFakeTMDB() *fakeTMDB {
	f := &fakeTMDB{movies: map[int]*metadata.Movie{}, imdb: map[string]int{}}
	add := func(id int, title, date string, runtime int, alts ...string) {
		f.movies[id] = &metadata.Movie{ID: id, Title: title, OriginalTitle: title, ReleaseDate: date, Runtime: runtime, AlternativeTitles: alts, PosterPath: "/p.jpg"}
	}
	add(170, "28 Days Later", "2002-10-31", 113)
	add(1562, "28 Weeks Later", "2007-04-26", 100)
	add(520763, "A Quiet Place Part II", "2021-05-21", 97)
	add(438631, "Dune", "2021-09-15", 155)
	add(841, "Dune", "1984-12-14", 137)
	add(693134, "Dune: Part Two", "2024-02-27", 167)
	add(272, "Batman Begins", "2005-06-10", 140)
	add(155, "The Dark Knight", "2008-07-16", 152)
	add(49026, "The Dark Knight Rises", "2012-07-17", 165)
	add(365177, "Borderlands", "2024-08-07", 101)
	add(530915, "1917", "2019-12-25", 119)
	add(1100, "Underworld", "2003-09-19", 121)
	f.imdb["tt0289043"] = 170
	addTV(f)
	return f
}

func (f *fakeTMDB) SearchMovies(_ context.Context, q string, year int) ([]metadata.MovieResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failKey {
		return nil, metadata.ErrBadKey
	}
	var out []metadata.MovieResult
	for _, m := range f.movies {
		if !strings.Contains(normalize(m.Title), normalize(q)) && !strings.Contains(normalize(q), normalize(m.Title)) {
			continue
		}
		if year != 0 && m.Year() != year {
			continue
		}
		out = append(out, metadata.MovieResult{ID: m.ID, Title: m.Title, OriginalTitle: m.Title, ReleaseDate: m.ReleaseDate})
	}
	return out, nil
}

func (f *fakeTMDB) Movie(_ context.Context, id int) (*metadata.Movie, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failKey {
		return nil, metadata.ErrBadKey
	}
	m, ok := f.movies[id]
	if !ok {
		return nil, metadata.ErrNotFound
	}
	return m, nil
}

func (f *fakeTMDB) FindByIMDb(_ context.Context, id string) (*metadata.MovieResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if tid, ok := f.imdb[id]; ok {
		m := f.movies[tid]
		return &metadata.MovieResult{ID: m.ID, Title: m.Title, ReleaseDate: m.ReleaseDate}, nil
	}
	return nil, metadata.ErrNotFound
}

func (f *fakeTMDB) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

// touch creates a file (and its folders) under dir.
func touch(t *testing.T, dir, rel string, content []byte) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

// tinyMKV is a minimal Matroska file with one 3840x2160 HEVC video track
// and a 2-hour duration, so scans can read real header facts.
func tinyMKV() []byte {
	el := func(id []byte, body ...[]byte) []byte {
		b := append([]byte{}, id...)
		size := make([]byte, 8)
		n := 0
		for _, x := range body {
			n += len(x)
		}
		binary.BigEndian.PutUint64(size, uint64(n))
		size[0] = 0x01
		b = append(b, size...)
		for _, x := range body {
			b = append(b, x...)
		}
		return b
	}
	u := func(v uint16) []byte { return []byte{byte(v >> 8), byte(v)} }
	dur := make([]byte, 8)
	binary.BigEndian.PutUint64(dur, 0x415B774000000000) // 7.2e6 ms as float64
	return append(
		el([]byte{0x1A, 0x45, 0xDF, 0xA3}, el([]byte{0x42, 0x82}, []byte("matroska"))),
		el([]byte{0x18, 0x53, 0x80, 0x67},
			el([]byte{0x15, 0x49, 0xA9, 0x66}, el([]byte{0x44, 0x89}, dur)),
			el([]byte{0x16, 0x54, 0xAE, 0x6B}, el([]byte{0xAE},
				el([]byte{0x83}, []byte{1}),
				el([]byte{0x86}, []byte("V_MPEGH/ISO/HEVC")),
				el([]byte{0xE0}, el([]byte{0xB0}, u(3840)), el([]byte{0xBA}, u(2160))))),
		)...)
}

// buildLibrary creates a small movie library in the styles found in real
// ones.
func buildLibrary(t *testing.T, dir string) {
	t.Helper()
	files := []string{
		"28 Days Later (2002)/28.Days.Later.2002.1080p.BluRay.DDP5.1.x265.10bit-GRP.mkv",
		"28 Weeks Later (2007)/28 Weeks Later (2007) (1080p BluRay x265 HEVC 10bit AAC 5.1 Grp).mkv",
		"A Quiet Place Part II (2021) {tmdb-520763}/A Quiet Place Part II (2021) - [WEBDL-2160p]-GRP.mkv",
		"Dune (2021)/Dune (2021).mkv",
		"Dune/Dune.mkv", // no year anywhere: two films fit
		"Borderlands (2024)/Borderlands (2024) [2160p]/Borderlands (2024) [2160p].mkv",
		"Borderlands (2024)/Borderlands (2024) [1080p].mp4",
		"Batman (REMUX)/Batman Begins (2005) (REMUX)/Batman Begins (2005) (REMUX).mkv",
		"Batman (REMUX)/The Dark Knight (2008) (REMUX)/The Dark Knight (2008) (REMUX).mkv",
		"Batman (REMUX)/The Dark Knight Rises (2012) (REMUX)/The Dark Knight Rises (2012) (REMUX).mkv",
		"Underworld (2003).mkv", // loose in the root
		"Some Home Video (1999)/Some Home Video (1999).mkv",
		// Ignored: extras, Plex copies, samples, junk.
		"28 Days Later (2002)/28 Days Later (2002)-trailer.mkv",
		"28 Days Later (2002)/Featurettes/Making Of.mkv",
		"28 Days Later (2002)/sample.mkv",
		"Plex Versions/Optimized for Mobile/Dune (2021).mp4",
		"28 Days Later (2002)/28 Days Later (2002)-thumb.jpg",
		".hidden/Thing (2001).mkv",
	}
	for _, f := range files {
		touch(t, dir, f, []byte("x"))
	}
	// One real header: the name says nothing about resolution, the file
	// says 2160p.
	touch(t, dir, "1917 (2019)/1917 (2019).mkv", tinyMKV())
}

type fixture struct {
	db   *store.Store
	svc  *Service
	tmdb *fakeTMDB
	hd   *RootFolder
	dir  string
}

func newFixture(t *testing.T, db *store.Store) *fixture {
	t.Helper()
	tm := newFakeTMDB()
	svc := New(db, tm, slog.New(slog.DiscardHandler))
	dir := t.TempDir()
	buildLibrary(t, dir)
	root, err := svc.AddRootFolder(context.Background(), RootInput{Path: dir, MediaType: Movie, DefaultProfileID: 1})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{db: db, svc: svc, tmdb: tm, hd: root, dir: dir}
}

func (f *fixture) titleByTMDB(t *testing.T, id int) *Title {
	t.Helper()
	tt := new(Title)
	if err := f.db.DB.NewSelect().Model(tt).Where("tmdb_id = ?", id).Scan(context.Background()); err != nil {
		t.Fatalf("title %d not in library: %v", id, err)
	}
	return tt
}

func (f *fixture) filesOf(t *testing.T, id int) []MediaFile {
	t.Helper()
	var files []MediaFile
	err := f.db.DB.NewSelect().Model(&files).
		Join("JOIN versions v ON v.id = media_file.version_id").
		Join("JOIN titles ti ON ti.id = v.title_id").
		Where("ti.tmdb_id = ?", id).Order("media_file.path").Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	buildLibrary(t, dir)
	units, err := discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, u := range units {
		got[u.Path] = len(u.Files)
	}
	want := map[string]int{
		"1917 (2019)": 1, "28 Days Later (2002)": 1, "28 Weeks Later (2007)": 1,
		"A Quiet Place Part II (2021) {tmdb-520763}": 1, "Dune (2021)": 1, "Dune": 1,
		"Borderlands (2024)":                          2,
		"Batman (REMUX)/Batman Begins (2005) (REMUX)": 1, "Batman (REMUX)/The Dark Knight (2008) (REMUX)": 1,
		"Batman (REMUX)/The Dark Knight Rises (2012) (REMUX)": 1,
		"Underworld (2003).mkv":                               1, "Some Home Video (1999)": 1,
	}
	if len(got) != len(want) {
		t.Errorf("units = %v", got)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s: %d files, want %d (all: %v)", k, got[k], n, got)
		}
	}
}

func TestScanIdentifiesLibrary(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		var last string
		res, err := f.svc.Scan(ctx, f.hd.ID, func(_ int, msg string) { last = msg })
		if err != nil {
			t.Fatal(err)
		}
		if res.Units != 12 || res.Added != 10 || res.Review != 2 {
			t.Errorf("result = %+v", res)
		}
		if !strings.Contains(last, "10 added") {
			t.Errorf("final message = %q", last)
		}

		// Raw scene name; quality from the name since the file is empty.
		files := f.filesOf(t, 170)
		if len(files) != 1 || files[0].Quality != "Bluray-1080p" || files[0].ReleaseGroup != "GRP" || files[0].ProbeError == "" {
			t.Errorf("28 Days Later files = %+v", files)
		}
		// Real header beats the name: 1917 is 2160p.
		files = f.filesOf(t, 530915)
		if len(files) != 1 || files[0].Resolution != 2160 || files[0].Media() == nil || files[0].Media().Duration != 2*time.Hour {
			t.Errorf("1917 files = %+v", files)
		}
		// Quality sub-folders belong to the same movie.
		if files := f.filesOf(t, 365177); len(files) != 2 {
			t.Errorf("Borderlands should have 2 files, got %d", len(files))
		}
		// Collection folder: three movies.
		for _, id := range []int{272, 155, 49026} {
			if files := f.filesOf(t, id); len(files) != 1 || files[0].Source != "Remux" {
				t.Errorf("collection movie %d: %+v", id, files)
			}
		}
		// The year picks the right Dune; the loose file is found.
		f.titleByTMDB(t, 438631)
		u := f.titleByTMDB(t, 1100)
		var v Version
		if err := db.DB.NewSelect().Model(&v).Where("title_id = ?", u.ID).Scan(ctx); err != nil || v.Folder != "" {
			t.Errorf("loose file version = %+v, %v", v, err)
		}

		// Doubtful folders wait for review, with reasons and candidates.
		items, err := f.svc.ReviewItems(ctx)
		if err != nil {
			t.Fatal(err)
		}
		reasons := map[string]ScanItem{}
		for _, it := range items {
			reasons[it.Path] = it
		}
		dune := reasons["Dune"]
		c := dune.CandidateList()
		if dune.Reason == "" || len(c) < 2 || c[0].Title != "Dune" || c[1].Title != "Dune" || c[0].Year == c[1].Year {
			t.Errorf("Dune review should offer both films named Dune first: %+v", dune)
		}
		if home := reasons["Some Home Video (1999)"]; !strings.Contains(home.Reason, "nothing") {
			t.Errorf("home video review = %+v", home)
		}
	})
}

func TestRescanIsCheapAndTracksMissingFiles(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		calls := f.tmdb.callCount()

		res, err := f.svc.Scan(ctx, f.hd.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Unchanged != 10 || res.Review != 2 || res.Added != 0 || f.tmdb.callCount() != calls {
			t.Errorf("second scan = %+v, TMDB calls %d → %d (should not ask again)", res, calls, f.tmdb.callCount())
		}

		// A file disappears (say, the NAS share hiccups): marked missing,
		// not forgotten.
		p := filepath.Join(f.dir, "Dune (2021)", "Dune (2021).mkv")
		data, _ := os.ReadFile(p)
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		res, _ = f.svc.Scan(ctx, f.hd.ID, nil)
		if res.Missing != 1 {
			t.Errorf("missing = %d", res.Missing)
		}
		files := f.filesOf(t, 438631)
		if len(files) != 1 || files[0].MissingSince == nil {
			t.Errorf("missing file should be kept and marked: %+v", files)
		}
		// It comes back.
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if res, _ := f.svc.Scan(ctx, f.hd.ID, nil); res.Missing != 0 {
			t.Errorf("after restore missing = %d", res.Missing)
		}
		if files := f.filesOf(t, 438631); files[0].MissingSince != nil {
			t.Error("restored file should no longer be missing")
		}

		// A new file in a known movie's folder joins that movie.
		touch(t, f.dir, "28 Days Later (2002)/28 Days Later (2002) Bluray-2160p.mkv", []byte("y"))
		res, _ = f.svc.Scan(ctx, f.hd.ID, nil)
		if res.Updated != 1 || len(f.filesOf(t, 170)) != 2 {
			t.Errorf("new file in known folder: %+v", res)
		}
	})
}

func TestVersionsAcrossRootFolders(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}

		// A Remux folder with a movie that's also in the HD folder: one
		// title, two versions.
		remuxDir := t.TempDir()
		touch(t, remuxDir, "Dune - Part Two (2024)/Dune - Part Two (2024) Remux-2160p.mkv", []byte("z"))
		touch(t, remuxDir, "1917 (2019)/1917 (2019) Remux-2160p.mkv", []byte("z"))
		remux, err := f.svc.AddRootFolder(ctx, RootInput{Path: remuxDir, MediaType: Movie, DefaultProfileID: 2})
		if err != nil {
			t.Fatal(err)
		}
		if res, err := f.svc.Scan(ctx, remux.ID, nil); err != nil || res.Added != 2 {
			t.Fatalf("remux scan = %+v, %v", res, err)
		}
		t1917 := f.titleByTMDB(t, 530915)
		n, _ := db.DB.NewSelect().Model((*Version)(nil)).Where("title_id = ?", t1917.ID).Count(ctx)
		if n != 2 {
			t.Errorf("1917 should have 2 versions (HD and Remux), has %d", n)
		}

		// A second HD folder with the same movie: that's a conflict for
		// review, not a silent merge.
		kidsDir := t.TempDir()
		touch(t, kidsDir, "Dune (2021)/Dune (2021).mkv", []byte("k"))
		kids, err := f.svc.AddRootFolder(ctx, RootInput{Path: kidsDir, MediaType: Movie, DefaultProfileID: 1})
		if err != nil {
			t.Fatal(err)
		}
		res, err := f.svc.Scan(ctx, kids.ID, nil)
		if err != nil || res.Review != 1 {
			t.Fatalf("duplicate scan = %+v, %v", res, err)
		}
		items, _ := f.svc.ReviewItems(ctx)
		found := false
		for _, it := range items {
			if it.RootFolderID == kids.ID && strings.Contains(it.Reason, "already in") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected an 'already in' review item, got %+v", items)
		}
	})
}

func TestScanStopsWithoutAWorkingKey(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	f := newFixture(t, db)
	f.tmdb.failKey = true
	_, err := f.svc.Scan(context.Background(), f.hd.ID, nil)
	if !errors.Is(err, metadata.ErrBadKey) {
		t.Errorf("err = %v", err)
	}
	if f.tmdb.callCount() > 2 {
		t.Errorf("should stop at the first key error, made %d calls", f.tmdb.callCount())
	}
}

func TestRootFolderValidation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		file := filepath.Join(f.dir, "Underworld (2003).mkv")
		cases := map[string]RootInput{
			"empty":       {Path: "", MediaType: Movie, DefaultProfileID: 1},
			"relative":    {Path: "movies", MediaType: Movie, DefaultProfileID: 1},
			"missing":     {Path: filepath.Join(f.dir, "nope"), MediaType: Movie, DefaultProfileID: 1},
			"file":        {Path: file, MediaType: Movie, DefaultProfileID: 1},
			"duplicate":   {Path: f.dir, MediaType: Movie, DefaultProfileID: 1},
			"nested":      {Path: filepath.Join(f.dir, "Dune (2021)"), MediaType: Movie, DefaultProfileID: 1},
			"bad profile": {Path: t.TempDir(), MediaType: Movie, DefaultProfileID: 99},
			"music":       {Path: t.TempDir(), MediaType: "music", DefaultProfileID: 1},
			"movie-only":  {Path: t.TempDir(), MediaType: Series, DefaultProfileID: 2}, // Remux is for movies
		}
		for name, in := range cases {
			if _, err := f.svc.AddRootFolder(ctx, in); err == nil {
				t.Errorf("%s: expected a validation error", name)
			}
		}
		// Parent of an existing root also overlaps.
		if _, err := f.svc.AddRootFolder(ctx, RootInput{Path: filepath.Dir(f.dir), MediaType: Movie, DefaultProfileID: 1}); err == nil {
			t.Error("parent folder should overlap")
		}
	})
}

func TestRemoveRootFolderForgetsButKeepsFiles(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.RemoveRootFolder(ctx, f.hd.ID); err != nil {
			t.Fatal(err)
		}
		for _, m := range []any{(*Title)(nil), (*Version)(nil), (*MediaFile)(nil), (*ScanItem)(nil)} {
			if n, _ := db.DB.NewSelect().Model(m).Count(ctx); n != 0 {
				t.Errorf("%T: %d rows left", m, n)
			}
		}
		if _, err := os.Stat(filepath.Join(f.dir, "Dune (2021)", "Dune (2021).mkv")); err != nil {
			t.Error("files on disk must never be touched")
		}
	})
}

func TestResolveAndIgnoreReview(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		items, _ := f.svc.ReviewItems(ctx)
		byPath := map[string]int64{}
		for _, it := range items {
			byPath[it.Path] = it.ID
		}

		// "Dune" is the 1984 film.
		title, err := f.svc.ResolveReview(ctx, byPath["Dune"], 841)
		if err != nil || title.Year != 1984 {
			t.Fatalf("resolve: %+v, %v", title, err)
		}
		if files := f.filesOf(t, 841); len(files) != 1 || files[0].Path != "Dune/Dune.mkv" {
			t.Errorf("files = %+v", files)
		}
		// The home video is ignored, and stays ignored after a rescan.
		if err := f.svc.IgnoreReview(ctx, byPath["Some Home Video (1999)"]); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
			t.Fatal(err)
		}
		if items, _ := f.svc.ReviewItems(ctx); len(items) != 0 {
			t.Errorf("nothing should be left to review: %+v", items)
		}
	})
}
