package radarr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/auth"
	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/torrent"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// fakeLib is an in-memory library with two profiles (HD and Remux) and
// two media folders.
type fakeLib struct {
	profiles map[string]*library.Profile
	titles   map[int]*library.Title // by TMDB ID
	versions []library.ProfileMovie
	tags     []library.Tag
	deleted  []int64
	nextID   int64
}

func newFakeLib() *fakeLib {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := &fakeLib{
		profiles: map[string]*library.Profile{
			"hd":    {ID: 1, Name: "HD", Slug: "hd", AppliesTo: "both"},
			"remux": {ID: 2, Name: "Remux", Slug: "remux", AppliesTo: "movie"},
			"tv":    {ID: 3, Name: "TV", Slug: "tv", AppliesTo: "series"},
		},
		titles: map[int]*library.Title{
			603: {ID: 10, MediaType: library.Movie, TMDBID: 603, IMDbID: "tt0133093", Title: "The Matrix", OriginalTitle: "The Matrix", SortTitle: "matrix", Year: 1999, Runtime: 136, Status: "Released", OriginalLanguage: "en", PosterPath: "/p.jpg", Genres: `["Action"]`, AltTitles: `["Matrix"]`, CreatedAt: now,
				TheatricalRelease: "1999-03-31", DigitalRelease: "1999-09-21", PhysicalRelease: "1999-09-21", Collection: "The Matrix Collection"},
			918001: {ID: 11, MediaType: library.Movie, TMDBID: 918001, Title: "Zendral Quorn: Part Two", SortTitle: "zendral quorn part two", Year: 2024, Status: "Released", DigitalRelease: "2026-01-10", Genres: `[]`, AltTitles: `[]`, CreatedAt: now},
		},
		nextID: 100,
	}
	hdRoot := library.RootFolder{ID: 1, Path: "/media/movies", MediaType: library.Movie, DefaultProfileID: 1}
	f.versions = []library.ProfileMovie{{
		Title: *f.titles[603], Root: hdRoot,
		Version: library.Version{ID: 50, TitleID: 10, ProfileID: 1, RootFolderID: 1, Folder: "The Matrix (1999)", Monitored: true, CreatedAt: now, LastSearchAt: &now},
		Files: []library.MediaFile{{ID: 7, VersionID: 50, RootFolderID: 1, Path: "The Matrix (1999)/The Matrix (1999) [Bluray-1080p]-GRP.mkv",
			Size: 8 << 30, Resolution: 1080, Source: "BluRay", Quality: "Bluray-1080p", ReleaseGroup: "GRP", ReleaseInfo: "{}", MediaInfo: "{}", AddedAt: now}},
	}}
	return f
}

func (f *fakeLib) ProfileBySlug(_ context.Context, slug string) (*library.Profile, error) {
	if p, ok := f.profiles[slug]; ok {
		return p, nil
	}
	return nil, library.ErrNotFound
}

func (f *fakeLib) CompiledProfile(context.Context, int64) (*decision.Profile, error) {
	pr, _ := decision.PresetByID("hd")
	p, err := pr.Settings.Compile()
	return &p, err
}

func (f *fakeLib) ProfileMovies(_ context.Context, profileID int64, flt library.ProfileMovieFilter) ([]library.ProfileMovie, error) {
	var out []library.ProfileMovie
	for _, v := range f.versions {
		if v.Version.ProfileID != profileID || (flt.TMDBID != 0 && v.Title.TMDBID != flt.TMDBID) ||
			(flt.VersionID != 0 && v.Version.ID != flt.VersionID) {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

func (f *fakeLib) RootFolders(context.Context) ([]library.RootFolder, error) {
	return []library.RootFolder{
		{ID: 1, Path: "/media/movies", MediaType: library.Movie, DefaultProfileID: 1},
		{ID: 2, Path: "/media/remux", MediaType: library.Movie, DefaultProfileID: 2},
		{ID: 3, Path: "/media/tv", MediaType: library.Series, DefaultProfileID: 3},
	}, nil
}

func (f *fakeLib) Tags(context.Context) ([]library.Tag, error) { return f.tags, nil }

func (f *fakeLib) CreateTag(_ context.Context, label string) (*library.Tag, error) {
	l := strings.ToLower(strings.TrimSpace(label))
	for _, t := range f.tags {
		if t.Label == l {
			return &t, nil
		}
	}
	t := library.Tag{ID: int64(len(f.tags) + 1), Label: l}
	f.tags = append(f.tags, t)
	return &t, nil
}

func (f *fakeLib) RenameTag(_ context.Context, id int64, label string) (*library.Tag, error) {
	for i := range f.tags {
		if f.tags[i].ID == id {
			f.tags[i].Label = label
			return &f.tags[i], nil
		}
	}
	return nil, library.ErrNotFound
}

func (f *fakeLib) SetVersionTags(_ context.Context, versionID int64, tagIDs []int64) error {
	for _, id := range tagIDs {
		if int(id) > len(f.tags) || id <= 0 {
			return validation.Errors{"tags": "One of the tags doesn't exist."}
		}
	}
	for i := range f.versions {
		if f.versions[i].Version.ID == versionID {
			f.versions[i].Tags = tagIDs
		}
	}
	return nil
}

func (f *fakeLib) TitleByTMDB(_ context.Context, id int) (*library.Title, error) {
	for _, v := range f.versions {
		if v.Title.TMDBID == id {
			t := v.Title
			return &t, nil
		}
	}
	return nil, library.ErrNotFound
}

func (f *fakeLib) TMDBMovie(_ context.Context, id int) (*library.Title, error) {
	if t, ok := f.titles[id]; ok {
		c := *t
		c.ID = 0
		return &c, nil
	}
	return nil, library.ErrNotFound
}

func (f *fakeLib) Lookup(_ context.Context, q string) ([]library.LookupResult, error) {
	var out []library.LookupResult
	for _, t := range f.titles {
		if strings.Contains(strings.ToLower(t.Title), strings.ToLower(q)) || t.IMDbID == q {
			lr := library.LookupResult{}
			lr.ID = t.TMDBID
			out = append(out, lr)
		}
	}
	return out, nil
}

func (f *fakeLib) AddMovie(_ context.Context, tmdbID int, profileID, rootID int64, monitored bool) (*library.MovieView, error) {
	t := f.titles[tmdbID]
	f.nextID++
	roots, _ := f.RootFolders(context.Background())
	var root library.RootFolder
	for _, r := range roots {
		if r.ID == rootID {
			root = r
		}
	}
	v := library.Version{ID: f.nextID, TitleID: t.ID, ProfileID: profileID, RootFolderID: rootID, Folder: t.Title + fmt.Sprintf(" (%d)", t.Year), Monitored: monitored}
	f.versions = append(f.versions, library.ProfileMovie{Title: *t, Version: v, Root: root})
	return &library.MovieView{Title: *t, Versions: []library.VersionView{{Version: v}}}, nil
}

func (f *fakeLib) SetMonitored(_ context.Context, _, versionID int64, monitored bool) error {
	for i := range f.versions {
		if f.versions[i].Version.ID == versionID {
			f.versions[i].Version.Monitored = monitored
		}
	}
	return nil
}

func (f *fakeLib) DeleteVersion(_ context.Context, _, versionID int64, deleteFiles bool) (library.DeleteResult, error) {
	if deleteFiles {
		panic("the compat API must never delete files")
	}
	f.deleted = append(f.deleted, versionID)
	f.versions = slices.DeleteFunc(f.versions, func(v library.ProfileMovie) bool { return v.Version.ID == versionID })
	return library.DeleteResult{}, nil
}

type fakeDownloads struct {
	client  string // the download client's name; "qBittorrent" when empty
	items   []download.Item
	removed []int64
}

func (d *fakeDownloads) ClientName() string {
	if d.client == "" {
		return "qBittorrent"
	}
	return d.client
}
func (d *fakeDownloads) Queue(context.Context) ([]download.Item, error, error) {
	return d.items, nil, nil
}
func (d *fakeDownloads) RemoveForVersion(_ context.Context, v int64) error {
	d.removed = append(d.removed, v)
	return nil
}

type rig struct {
	srv      *httptest.Server
	lib      *fakeLib
	dl       *fakeDownloads
	searched []int64
}

func newRig(t *testing.T) *rig {
	t.Helper()
	g := &rig{lib: newFakeLib(), dl: &fakeDownloads{}}
	h := Router(Deps{
		Library: g.lib, Downloads: g.dl, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Search: func(_ context.Context, v int64) error { g.searched = append(g.searched, v); return nil },
	})
	// Stand-in for auth.Identify: the API key "key" signs in.
	withAuth := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") == "key" {
			r = r.WithContext(auth.WithIdentity(r.Context(), &auth.Identity{Via: auth.ViaAPIKey}))
		}
		http.StripPrefix("/compat", h).ServeHTTP(w, r)
	})
	g.srv = httptest.NewServer(withAuth)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *rig) do(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, g.srv.URL+"/compat/radarr/"+path, rd)
	req.Header.Set("X-Api-Key", "key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestAuthAndScope(t *testing.T) {
	g := newRig(t)
	req, _ := http.NewRequest(http.MethodGet, g.srv.URL+"/compat/radarr/hd/api/v3/system/status", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no key: %d", resp.StatusCode)
	}
	for path, want := range map[string]int{
		"hd/api/v3/system/status": 200, "nope/api/v3/system/status": 404, "tv/api/v3/system/status": 404,
		"hd/api/v3/history": 404,
	} {
		if code, _ := g.do(t, "GET", path, ""); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
}

// Every field Radarr sends must be in Reelkeeper's answer, with the same JSON
// type (extra fields are fine). Compared with answers recorded from a real
// Radarr 6 (testdata/compat/radarr), whose titles, IDs and paths were
// replaced with invented ones, keeping every key and value type. The movie
// in movie_lookup_term_tmdb_900001.json is a made-up one; the test looks up
// the rig's own movie, since only the shape counts.
func TestShapesMatchRecordedRadarr(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "testdata", "compat", "radarr")
	g := newRig(t)
	g.dl.items = []download.Item{{Download: download.Download{ID: 1, VersionID: ptr(int64(50)), Hash: "abc", ReleaseTitle: "x", Quality: "Bluray-1080p"},
		Torrent: &download.Torrent{Hash: "abc", Progress: 0.5, Size: 100, Left: 50, ETA: time.Minute}}}
	for file, path := range map[string]string{
		"system_status.json":                 "hd/api/v3/system/status",
		"qualityProfile.json":                "hd/api/v3/qualityProfile",
		"rootfolder.json":                    "hd/api/v3/rootfolder",
		"movie.json":                         "hd/api/v3/movie",
		"movie_lookup_term_tmdb_900001.json": "hd/api/v3/movie/lookup?term=tmdb:603",
		"queue_pageSize_1.json":              "hd/api/v3/queue?pageSize=1",
	} {
		raw, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		var want any
		_ = json.Unmarshal(raw, &want)
		code, body := g.do(t, "GET", path, "")
		if code != 200 {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		var got any
		_ = json.Unmarshal(body, &got)
		for _, problem := range sameShape(want, got, file) {
			t.Error(problem)
		}
	}
}

// sameShape lists fields of want missing from got, or of another JSON
// type. Lists compare their first elements; an empty list on either side
// matches anything.
func sameShape(want, got any, at string) []string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: want an object, got %T", at, got)}
		}
		var out []string
		for k, wv := range w {
			gv, present := g[k]
			if !present {
				if wv != nil {
					out = append(out, fmt.Sprintf("%s.%s: missing", at, k))
				}
				continue
			}
			if wv == nil || gv == nil {
				continue
			}
			out = append(out, sameShape(wv, gv, at+"."+k)...)
		}
		return out
	case []any:
		g, ok := got.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: want a list, got %T", at, got)}
		}
		if len(w) == 0 || len(g) == 0 {
			return nil
		}
		return sameShape(w[0], g[0], at+"[0]")
	case float64:
		if _, ok := got.(float64); !ok {
			return []string{fmt.Sprintf("%s: want a number, got %T", at, got)}
		}
	case string:
		if _, ok := got.(string); !ok {
			return []string{fmt.Sprintf("%s: want a string, got %T", at, got)}
		}
	case bool:
		if _, ok := got.(bool); !ok {
			return []string{fmt.Sprintf("%s: want a bool, got %T", at, got)}
		}
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return v
}

// Seerr's request flow: look up by TMDB ID, then add (or re-monitor), with
// tags, and search.
func TestSeerrAddFlow(t *testing.T) {
	g := newRig(t)

	// Zendral Quorn isn't in the Remux profile: the lookup has no id.
	_, b := g.do(t, "GET", "remux/api/v3/movie/lookup?term=tmdb:918001", "")
	look := decode[[]map[string]any](t, b)
	if len(look) != 1 || look[0]["id"] != nil || look[0]["hasFile"] != false || look[0]["title"] != "Zendral Quorn: Part Two" {
		t.Fatalf("lookup = %s", b)
	}
	// Seerr creates its user tag, then adds the movie.
	_, b = g.do(t, "POST", "remux/api/v3/tag", `{"label":"1 - Dan"}`)
	tag := decode[map[string]any](t, b)
	code, b := g.do(t, "POST", "remux/api/v3/movie", `{"title":"Zendral Quorn: Part Two","qualityProfileId":2,"tmdbId":918001,"year":2024,
		"rootFolderPath":"/media/remux","monitored":true,"tags":[1],"minimumAvailability":"released","addOptions":{"searchForMovie":true}}`)
	if code != 201 {
		t.Fatalf("add: %d %s", code, b)
	}
	added := decode[map[string]any](t, b)
	id := int64(added["id"].(float64))
	if added["rootFolderPath"] != "/media/remux" || added["qualityProfileId"].(float64) != 2 || len(added["tags"].([]any)) != 1 ||
		tag["label"] != "1 - dan" {
		t.Errorf("added = %s", b)
	}
	if len(g.searched) != 1 || g.searched[0] != id {
		t.Errorf("searched %v", g.searched)
	}
	// Adding it again is refused the way Radarr refuses it.
	code, b = g.do(t, "POST", "remux/api/v3/movie", `{"tmdbId":918001,"rootFolderPath":"/media/remux"}`)
	if code != 400 || !strings.Contains(string(b), "already been added") {
		t.Errorf("second add: %d %s", code, b)
	}
	// A root folder that isn't this profile's.
	code, _ = g.do(t, "POST", "hd/api/v3/movie", `{"tmdbId":918001,"rootFolderPath":"/somewhere"}`)
	if code != 400 {
		t.Errorf("bad root: %d", code)
	}
	// The HD "Radarr" doesn't see the Remux version.
	_, b = g.do(t, "GET", "hd/api/v3/movie?tmdbId=918001", "")
	if n := len(decode[[]any](t, b)); n != 0 {
		t.Errorf("HD sees %d Remux versions", n)
	}
	// Re-monitoring through PUT, as Seerr does for an unmonitored movie.
	g.lib.versions[len(g.lib.versions)-1].Version.Monitored = false
	code, b = g.do(t, "PUT", "remux/api/v3/movie", fmt.Sprintf(`{"id":%d,"monitored":true,"tags":[1],"addOptions":{"searchForMovie":true}}`, id))
	if code != 202 || decode[map[string]any](t, b)["monitored"] != true || len(g.searched) != 2 {
		t.Errorf("re-monitor: %d %s, searched %v", code, b, g.searched)
	}
	// Seerr's search command.
	g.do(t, "POST", "remux/api/v3/command", fmt.Sprintf(`{"name":"MoviesSearch","movieIds":[%d]}`, id))
	if len(g.searched) != 3 {
		t.Errorf("command search: %v", g.searched)
	}
}

func TestLibraryMovieShape(t *testing.T) {
	g := newRig(t)
	_, b := g.do(t, "GET", "hd/api/v3/movie/50", "")
	m := decode[map[string]any](t, b)
	mf := m["movieFile"].(map[string]any)
	q := mf["quality"].(map[string]any)["quality"].(map[string]any)
	if m["hasFile"] != true || m["path"] != "/media/movies/The Matrix (1999)" || m["titleSlug"] != "603" ||
		q["name"] != "Bluray-1080p" || q["source"] != "bluray" || q["id"].(float64) != 7 ||
		mf["relativePath"] != "The Matrix (1999) [Bluray-1080p]-GRP.mkv" || m["sizeOnDisk"].(float64) != float64(8<<30) {
		t.Errorf("movie = %s", b)
	}
	// The lookup of a movie this profile has returns the library entry.
	_, b = g.do(t, "GET", "hd/api/v3/movie/lookup?term=tmdb:603", "")
	if l := decode[[]map[string]any](t, b); len(l) != 1 || l[0]["id"].(float64) != 50 {
		t.Errorf("lookup of a library movie = %s", b)
	}
	if code, _ := g.do(t, "GET", "remux/api/v3/movie/50", ""); code != 404 {
		t.Errorf("another profile's version: %d", code)
	}
}

func TestDeleteKeepsFiles(t *testing.T) {
	g := newRig(t)
	code, _ := g.do(t, "DELETE", "hd/api/v3/movie/50?deleteFiles=true&addImportExclusion=false", "")
	if code != 200 || len(g.lib.deleted) != 1 || len(g.dl.removed) != 1 {
		t.Errorf("delete: %d, deleted %v, downloads removed %v", code, g.lib.deleted, g.dl.removed)
	}
	if code, _ := g.do(t, "DELETE", "hd/api/v3/movie/50", ""); code != 404 {
		t.Errorf("second delete: %d", code)
	}
}

func TestQueueAndCalendar(t *testing.T) {
	g := newRig(t)
	for i := range 12 {
		g.dl.items = append(g.dl.items, download.Item{
			Download: download.Download{ID: int64(i + 1), VersionID: ptr(int64(50)), Hash: fmt.Sprint(i), Status: download.StatusDownloading},
			Torrent:  &download.Torrent{State: torrent.Downloading, Size: 100, Left: 40, ETA: 90 * time.Second},
		})
	}
	g.dl.items = append(g.dl.items, download.Item{Download: download.Download{ID: 99, VersionID: ptr(int64(999))}}) // another profile's
	_, b := g.do(t, "GET", "hd/api/v3/queue", "")
	q := decode[map[string]any](t, b)
	recs := q["records"].([]any)
	if q["totalRecords"].(float64) != 12 || len(recs) != 10 { // Radarr's default page size
		t.Errorf("queue = total %v, %d records", q["totalRecords"], len(recs))
	}
	r0 := recs[0].(map[string]any)
	if r0["movieId"].(float64) != 50 || r0["sizeleft"].(float64) != 40 || r0["timeleft"] != "00:01:30" || r0["status"] != "downloading" {
		t.Errorf("record = %v", r0)
	}
	_, b = g.do(t, "GET", "hd/api/v3/queue?pageSize=1", "")
	if q := decode[map[string]any](t, b); q["totalRecords"].(float64) != 12 || len(q["records"].([]any)) != 1 {
		t.Errorf("pageSize=1: %s", b)
	}

	// The calendar: a movie with a digital release in range.
	g.lib.versions[0].Title.DigitalRelease = "2026-03-15"
	_, b = g.do(t, "GET", "hd/api/v3/calendar?start=2026-03-01&end=2026-03-31", "")
	if l := decode[[]map[string]any](t, b); len(l) != 1 || l[0]["digitalRelease"] != "2026-03-15T00:00:00Z" {
		t.Errorf("calendar = %s", b)
	}
	_, b = g.do(t, "GET", "hd/api/v3/calendar?start=2026-04-01&end=2026-04-30", "")
	if l := decode[[]any](t, b); len(l) != 0 {
		t.Errorf("calendar out of range = %s", b)
	}
}
