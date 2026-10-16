package sonarr

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

// The fake library has profiles HD (movies and TV), Remux (movies only)
// and TV (TV only), and one series in HD: Velmora Quill, two seasons and a
// special, with files for all of season 1 (a double episode among them)
// and a season 2 still airing. TMDB also knows The Dravnik Reach, which isn't
// in the library.

var (
	aired  = "2020-02-07"
	future = "2099-01-01"
)

func ep(id int64, season, number int, air string) library.Episode {
	return library.Episode{ID: id, TitleID: 10, TMDBID: int(id) * 7, Season: &season, Number: &number, AiredSeason: season,
		AiredNumber: number, Name: fmt.Sprintf("Episode %d-%d", season, number), Overview: "Things happen.", AirDate: air, Runtime: 30}
}

type fakeLib struct {
	profiles map[string]*library.Profile
	series   []library.ProfileSeries
	previews map[int]*library.SeriesPreview // by TMDB ID
	tvdb     map[int]int                    // TVDB → TMDB
	tags     []library.Tag
	deleted  []int64
	nextID   int64
}

func newFakeLib() *fakeLib {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	from := "2020-01-01"
	mq := library.Title{ID: 10, MediaType: library.Series, TMDBID: 912345, TVDBID: 912346, IMDbID: "tt9123457", Title: "Velmora Quill",
		SortTitle: "velmora quill", Year: 2020, Status: "Returning Series", OriginalLanguage: "en", Overview: "A game studio.",
		Network: "Apple TV+", Runtime: 30, ReleaseDate: "2020-02-07", LastAirDate: aired, PosterPath: "/p.jpg", BackdropPath: "/b.jpg",
		Genres: `["Comedy"]`, AltTitles: `["MQ"]`, SeriesType: "standard", VoteAverage: 7.8, CreatedAt: now}
	eps := []library.Episode{ep(100, 0, 1, aired), ep(101, 1, 1, aired), ep(102, 1, 2, aired), ep(103, 1, 3, aired),
		ep(201, 2, 1, aired), ep(202, 2, 2, future)}
	v := library.Version{ID: 50, TitleID: 10, ProfileID: 1, RootFolderID: 3, Folder: "Velmora Quill (2020)", Monitored: true,
		MonitorFrom: &from, CreatedAt: now}
	ps := library.ProfileSeries{
		SeriesVersion: library.SeriesVersion{Version: v, Title: mq, Episodes: eps,
			HasFile:   map[int64]bool{101: true, 102: true, 103: true},
			Monitored: map[int64]bool{101: true, 102: true, 103: true, 201: true, 202: true}},
		Root: library.RootFolder{ID: 3, Path: "/media/tv", MediaType: library.Series, DefaultProfileID: 1},
		Files: []library.MediaFile{
			{ID: 7, VersionID: 50, RootFolderID: 3, Path: "Velmora Quill (2020)/Season 01/Velmora Quill (2020) - S01E01 - Pilot [WEBDL-1080p]-GRP.mkv",
				Size: 1 << 30, Resolution: 1080, Source: "WEBDL", Quality: "WEBDL-1080p", ReleaseGroup: "GRP", ReleaseInfo: "{}", MediaInfo: "{}", AddedAt: now},
			{ID: 8, VersionID: 50, RootFolderID: 3, Path: "Velmora Quill (2020)/Season 01/Velmora Quill (2020) - S01E02-E03 [WEBDL-1080p]-GRP.mkv",
				Size: 2 << 30, Resolution: 1080, Source: "WEBDL", Quality: "WEBDL-1080p", ReleaseGroup: "GRP", ReleaseInfo: "{}", MediaInfo: "{}", AddedAt: now},
		},
		FileEpisodes: map[int64][]int64{7: {101}, 8: {102, 103}},
	}
	dravnik := library.Title{MediaType: library.Series, TMDBID: 917001, TVDBID: 917002, IMDbID: "tt9170030", Title: "The Dravnik Reach",
		SortTitle: "dravnik reach", Year: 2015, Status: "Ended", OriginalLanguage: "en", Overview: "Space.", ReleaseDate: "2015-12-14",
		LastAirDate: "2022-01-14", PosterPath: "/e.jpg", Genres: `["Drama"]`, AltTitles: `[]`, SeriesType: "standard", CreatedAt: now}
	var expEps []library.Episode
	for s := 0; s <= 2; s++ {
		for n := 1; n <= 3; n++ {
			e := ep(0, s, n, "2016-01-01")
			e.TitleID = 0
			expEps = append(expEps, e)
		}
	}
	return &fakeLib{
		profiles: map[string]*library.Profile{
			"hd":    {ID: 1, Name: "HD", Slug: "hd", AppliesTo: "both"},
			"remux": {ID: 2, Name: "Remux", Slug: "remux", AppliesTo: "movie"},
			"tv":    {ID: 4, Name: "TV", Slug: "tv", AppliesTo: "series"},
		},
		series: []library.ProfileSeries{ps},
		previews: map[int]*library.SeriesPreview{
			912345: {Title: mq, Episodes: eps},
			917001: {Title: dravnik, Episodes: expEps},
		},
		tvdb:   map[int]int{912346: 912345, 917002: 917001},
		nextID: 100,
	}
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

func (f *fakeLib) ProfileSeries(_ context.Context, profileID int64, flt library.ProfileSeriesFilter) ([]library.ProfileSeries, error) {
	var out []library.ProfileSeries
	for _, s := range f.series {
		if s.Version.ProfileID != profileID || (flt.TVDBID != 0 && s.Title.TVDBID != flt.TVDBID) ||
			(flt.TMDBID != 0 && s.Title.TMDBID != flt.TMDBID) || (flt.VersionID != 0 && s.Version.ID != flt.VersionID) {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeLib) RootFolders(context.Context) ([]library.RootFolder, error) {
	return []library.RootFolder{
		{ID: 1, Path: "/media/movies", MediaType: library.Movie, DefaultProfileID: 1},
		{ID: 3, Path: "/media/tv", MediaType: library.Series, DefaultProfileID: 1},
		{ID: 4, Path: "/media/tv-b", MediaType: library.Series, DefaultProfileID: 1},
	}, nil
}

func (f *fakeLib) Tags(context.Context) ([]library.Tag, error) { return f.tags, nil }

func (f *fakeLib) CreateTag(_ context.Context, label string) (*library.Tag, error) {
	t := library.Tag{ID: int64(len(f.tags) + 1), Label: strings.ToLower(strings.TrimSpace(label))}
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
	for i := range f.series {
		if f.series[i].Version.ID == versionID {
			f.series[i].Tags = tagIDs
		}
	}
	return nil
}

func (f *fakeLib) SeriesTMDBID(_ context.Context, source, id string) (int, error) {
	var n int
	_, _ = fmt.Sscan(id, &n)
	if tmdb, ok := f.tvdb[n]; ok && source == "tvdb" {
		return tmdb, nil
	}
	return 0, library.ErrNotFound
}

func (f *fakeLib) SeriesPreview(_ context.Context, tmdbID int) (*library.SeriesPreview, error) {
	if p, ok := f.previews[tmdbID]; ok {
		return p, nil
	}
	return nil, library.ErrNotFound
}

func (f *fakeLib) LookupSeries(_ context.Context, q string) ([]library.SeriesLookupResult, error) {
	var out []library.SeriesLookupResult
	for id, p := range f.previews {
		if strings.Contains(strings.ToLower(p.Title.Title), strings.ToLower(q)) {
			r := library.SeriesLookupResult{}
			r.ID = id
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeLib) AddSeries(_ context.Context, tmdbID int, profileID, rootID int64, monitored bool) (*library.SeriesDetail, error) {
	p := f.previews[tmdbID]
	f.nextID++
	t := p.Title
	t.ID = f.nextID + 1000
	var eps []library.Episode
	for i, e := range p.Episodes {
		e.ID, e.TitleID = f.nextID*100+int64(i), t.ID
		eps = append(eps, e)
	}
	roots, _ := f.RootFolders(context.Background())
	var root library.RootFolder
	for _, r := range roots {
		if r.ID == rootID {
			root = r
		}
	}
	v := library.Version{ID: f.nextID, TitleID: t.ID, ProfileID: profileID, RootFolderID: rootID, Folder: "The Dravnik Reach (2015)", Monitored: monitored}
	f.series = append(f.series, library.ProfileSeries{
		SeriesVersion: library.SeriesVersion{Version: v, Title: t, Episodes: eps, HasFile: map[int64]bool{}, Monitored: map[int64]bool{}},
		Root:          root, FileEpisodes: map[int64][]int64{},
	})
	d := &library.SeriesDetail{SeriesView: library.SeriesView{Title: t, Versions: []library.VersionView{{Version: v}}}}
	for _, n := range []int{0, 1, 2} {
		d.SeasonList = append(d.SeasonList, library.SeasonView{Number: n})
	}
	return d, nil
}

func (f *fakeLib) find(versionID int64) *library.ProfileSeries {
	for i := range f.series {
		if f.series[i].Version.ID == versionID {
			return &f.series[i]
		}
	}
	return nil
}

func (f *fakeLib) ApplySeriesMonitoring(_ context.Context, versionID int64, m library.SeriesMonitoring) error {
	ps := f.find(versionID)
	if ps == nil {
		return library.ErrNotFound
	}
	if m.Monitored != nil {
		ps.Version.Monitored = *m.Monitored
	}
	if m.NewEpisodes != nil {
		ps.Version.MonitorFrom = nil
		if *m.NewEpisodes {
			ps.Version.MonitorFrom = &future
		}
	}
	for _, e := range ps.Episodes {
		if on, ok := m.Seasons[*e.Season]; ok {
			if on {
				ps.Monitored[e.ID] = true
			} else {
				delete(ps.Monitored, e.ID)
			}
		}
	}
	return nil
}

func (f *fakeLib) SetEpisodesMonitored(_ context.Context, versionID int64, ids []int64, monitored bool) error {
	ps := f.find(versionID)
	for _, id := range ids {
		if monitored {
			ps.Monitored[id] = true
		} else {
			delete(ps.Monitored, id)
		}
	}
	return nil
}

func (f *fakeLib) DeleteSeriesVersion(_ context.Context, _, versionID int64) (library.DeleteResult, error) {
	f.deleted = append(f.deleted, versionID)
	f.series = slices.DeleteFunc(f.series, func(s library.ProfileSeries) bool { return s.Version.ID == versionID })
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
	checked  int
}

func newRig(t *testing.T) *rig {
	t.Helper()
	g := &rig{lib: newFakeLib(), dl: &fakeDownloads{}}
	h := Router(Deps{
		Library: g.lib, Downloads: g.dl, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Search:         func(_ context.Context, v int64) error { g.searched = append(g.searched, v); return nil },
		CheckDownloads: func(context.Context) error { g.checked++; return nil },
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
	req, _ := http.NewRequest(method, g.srv.URL+"/compat/sonarr/"+path, rd)
	req.Header.Set("X-Api-Key", "key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return v
}

func ptr[T any](v T) *T { return &v }

// a season pack of season 1 and a single episode of season 2, both in
// progress, and a download for another profile.
func (g *rig) addDownloads() {
	g.dl.items = []download.Item{
		{Download: download.Download{ID: 1, VersionID: ptr(int64(50)), Hash: "abc", ReleaseTitle: "Velmora.Quill.S01.1080p.WEB-DL-GRP",
			Quality: "WEBDL-1080p", Status: download.StatusDownloading},
			Torrent: &download.Torrent{Hash: "abc", State: torrent.Downloading, Size: 300, Left: 100, ETA: 90 * time.Second}, EpisodeIDs: []int64{101, 102, 103}},
		{Download: download.Download{ID: 2, VersionID: ptr(int64(50)), Hash: "def", ReleaseTitle: "Velmora.Quill.S02E01.1080p.WEB-DL-GRP",
			Quality: "WEBDL-1080p", Status: download.StatusDownloading},
			Torrent: &download.Torrent{Hash: "def", State: torrent.Downloading, Size: 100, Left: 100, ETA: time.Hour}, EpisodeIDs: []int64{201}},
		{Download: download.Download{ID: 3, VersionID: ptr(int64(999)), Hash: "ghi"}, EpisodeIDs: []int64{5}},
	}
}

func TestAuthAndScope(t *testing.T) {
	g := newRig(t)
	req, _ := http.NewRequest(http.MethodGet, g.srv.URL+"/compat/sonarr/hd/api/v3/system/status", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no key: %d", resp.StatusCode)
	}
	for path, want := range map[string]int{
		"hd/api/v3/system/status": 200, "tv/api/v3/system/status": 200, "remux/api/v3/system/status": 404,
		"nope/api/v3/system/status": 404, "hd/api/v3/history": 404,
	} {
		if code, _ := g.do(t, "GET", path, ""); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
	_, b := g.do(t, "GET", "hd/api/v3/system/status", "")
	if s := decode[map[string]any](t, b); s["appName"] != "Sonarr" || !strings.HasPrefix(s["version"].(string), "4.") {
		t.Errorf("status = %s", b)
	}
}

// Every field Sonarr sends must be in Reelkeeper's answer, with the same
// JSON type (extra fields are fine). Compared with answers recorded from a
// real Sonarr 4 (testdata/compat/sonarr), whose titles, IDs and paths were
// replaced with invented ones, keeping every key and value type. The IDs in
// the lookup file names are made up and differ from the ones the test asks
// the rig for; only the shapes are compared.
func TestShapesMatchRecordedSonarr(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "testdata", "compat", "sonarr")
	g := newRig(t)
	g.addDownloads()
	g.lib.tags = []library.Tag{{ID: 1, Label: "1-dan"}}
	for file, path := range map[string]string{
		"system_status.json":             "hd/api/v3/system/status",
		"qualityProfile.json":            "hd/api/v3/qualityprofile",
		"rootfolder.json":                "hd/api/v3/rootfolder",
		"languageprofile.json":           "hd/api/v3/languageprofile",
		"tag.json":                       "hd/api/v3/tag",
		"series.json":                    "hd/api/v3/series",
		"series_49.json":                 "hd/api/v3/series/50",
		"episode_seriesId_49.json":       "hd/api/v3/episode?seriesId=50",
		"series_lookup_tvdb_900001.json": "hd/api/v3/series/lookup?term=tvdb:912346",
		"series_lookup_tvdb_900002.json": "hd/api/v3/series/lookup?term=tvdb:917002",
		"queue_includeEpisode_true.json": "hd/api/v3/queue?includeEpisode=true&includeSeries=true",
		"queue_pageSize_1.json":          "hd/api/v3/queue?pageSize=1",
		"calendar.json":                  "hd/api/v3/calendar?start=2020-01-01&end=2020-12-31",
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

// The numbers Seerr reads to mark seasons available, counted as Sonarr
// counts them.
func TestSeriesShape(t *testing.T) {
	g := newRig(t)
	_, b := g.do(t, "GET", "hd/api/v3/series/50", "")
	s := decode[map[string]any](t, b)
	if s["tvdbId"].(float64) != 912346 || s["path"] != "/media/tv/Velmora Quill (2020)" || s["status"] != "continuing" ||
		s["monitorNewItems"] != "all" || s["titleSlug"] != "912345" || s["qualityProfileId"].(float64) != 1 {
		t.Errorf("series = %s", b)
	}
	seasons := s["seasons"].([]any)
	stats := func(i int) map[string]any { return seasons[i].(map[string]any)["statistics"].(map[string]any) }
	if len(seasons) != 3 || seasons[0].(map[string]any)["monitored"] != false || seasons[1].(map[string]any)["monitored"] != true {
		t.Fatalf("seasons = %v", seasons)
	}
	// Season 1: three episodes, all with files (one file holds two).
	if s1 := stats(1); s1["episodeFileCount"].(float64) != 3 || s1["totalEpisodeCount"].(float64) != 3 ||
		s1["episodeCount"].(float64) != 3 || s1["percentOfEpisodes"].(float64) != 100 || s1["sizeOnDisk"].(float64) != 3<<30 {
		t.Errorf("season 1 = %v", s1)
	}
	// Season 2: one aired and wanted, one still to come.
	if s2 := stats(2); s2["episodeFileCount"].(float64) != 0 || s2["episodeCount"].(float64) != 1 ||
		s2["totalEpisodeCount"].(float64) != 2 || s2["nextAiring"] != future+"T00:00:00Z" {
		t.Errorf("season 2 = %v", s2)
	}
	st := s["statistics"].(map[string]any)
	if st["seasonCount"].(float64) != 2 || st["episodeFileCount"].(float64) != 3 || st["episodeCount"].(float64) != 4 ||
		st["totalEpisodeCount"].(float64) != 6 || st["percentOfEpisodes"].(float64) != 75 {
		t.Errorf("statistics = %v", st)
	}
	// Episodes: the double episode's file id on both.
	_, b = g.do(t, "GET", "hd/api/v3/episode?seriesId=50&seasonNumber=1", "")
	eps := decode[[]map[string]any](t, b)
	if len(eps) != 3 || eps[1]["episodeFileId"].(float64) != 8 || eps[2]["episodeFileId"].(float64) != 8 || eps[0]["hasFile"] != true {
		t.Errorf("episodes = %s", b)
	}
	if code, _ := g.do(t, "GET", "tv/api/v3/series/50", ""); code != 404 {
		t.Errorf("another profile's version: %d", code)
	}
	// No date rule: new items aren't monitored.
	g.lib.series[0].Version.MonitorFrom = nil
	_, b = g.do(t, "GET", "hd/api/v3/series?tvdbId=912346", "")
	if l := decode[[]map[string]any](t, b); len(l) != 1 || l[0]["monitorNewItems"] != "none" {
		t.Errorf("by tvdb = %s", b)
	}
}

// Seerr's TV request: look the show up by TVDB ID, add it with the
// requested seasons, search; later another season is requested on the
// series that's now there.
func TestSeerrAddFlow(t *testing.T) {
	g := newRig(t)
	_, b := g.do(t, "GET", "tv/api/v3/series/lookup?term=tvdb:917002", "")
	look := decode[[]map[string]any](t, b)
	if len(look) != 1 || look[0]["id"] != nil || look[0]["title"] != "The Dravnik Reach" || len(look[0]["seasons"].([]any)) != 3 ||
		look[0]["status"] != "ended" || look[0]["folder"] != "The Dravnik Reach (2015)" {
		t.Fatalf("lookup = %s", b)
	}
	if _, b = g.do(t, "POST", "tv/api/v3/tag", `{"label":"1-dan"}`); decode[map[string]any](t, b)["id"].(float64) != 1 {
		t.Fatalf("tag = %s", b)
	}
	code, b := g.do(t, "POST", "tv/api/v3/series", `{"tvdbId":917002,"title":"The Dravnik Reach","qualityProfileId":4,"languageProfileId":1,
		"seasons":[{"seasonNumber":0,"monitored":false},{"seasonNumber":1,"monitored":true},{"seasonNumber":2,"monitored":false}],
		"tags":[1],"seasonFolder":true,"monitored":true,"monitorNewItems":"all","rootFolderPath":"/media/tv","seriesType":"standard",
		"addOptions":{"ignoreEpisodesWithFiles":true,"searchForMissingEpisodes":true}}`)
	if code != 201 {
		t.Fatalf("add: %d %s", code, b)
	}
	added := decode[map[string]any](t, b)
	id := int64(added["id"].(float64))
	seasonOn := func(s map[string]any) []bool {
		var out []bool
		for _, x := range s["seasons"].([]any) {
			out = append(out, x.(map[string]any)["monitored"].(bool))
		}
		return out
	}
	if !slices.Equal(seasonOn(added), []bool{false, true, false}) || added["monitored"] != true ||
		added["monitorNewItems"] != "all" || len(added["tags"].([]any)) != 1 || added["rootFolderPath"] != "/media/tv" {
		t.Errorf("added = %s", b)
	}
	if !slices.Equal(g.searched, []int64{id}) {
		t.Errorf("searched %v", g.searched)
	}
	// Adding it again is refused the way Sonarr refuses it.
	code, b = g.do(t, "POST", "tv/api/v3/series", `{"tvdbId":917002,"rootFolderPath":"/media/tv"}`)
	if code != 400 || !strings.Contains(string(b), "already been added") {
		t.Errorf("second add: %d %s", code, b)
	}
	// A folder that isn't a TV folder, and a show TMDB doesn't know.
	if code, _ = g.do(t, "POST", "hd/api/v3/series", `{"tvdbId":917002,"rootFolderPath":"/media/movies"}`); code != 400 {
		t.Errorf("movie folder: %d", code)
	}
	if code, _ = g.do(t, "POST", "hd/api/v3/series", `{"tvdbId":1,"rootFolderPath":"/media/tv"}`); code != 400 {
		t.Errorf("unknown show: %d", code)
	}

	// Season 2 requested later: Seerr finds the series, ticks the season
	// and PUTs the whole thing back, then re-monitors any stragglers and
	// searches.
	_, b = g.do(t, "GET", "tv/api/v3/series/lookup?term=tvdb:917002", "")
	look = decode[[]map[string]any](t, b)
	if look[0]["id"].(float64) != float64(id) {
		t.Fatalf("lookup after adding = %s", b)
	}
	look[0]["seasons"].([]any)[2].(map[string]any)["monitored"] = true
	body, _ := json.Marshal(look[0])
	code, b = g.do(t, "PUT", "tv/api/v3/series", string(body))
	if code != 202 || !slices.Equal(seasonOn(decode[map[string]any](t, b)), []bool{false, true, true}) {
		t.Errorf("update: %d %s", code, b)
	}
	_, b = g.do(t, "GET", fmt.Sprintf("tv/api/v3/episode?seriesId=%d", id), "")
	eps := decode[[]map[string]any](t, b)
	special := int64(eps[0]["id"].(float64))
	if code, _ := g.do(t, "PUT", "tv/api/v3/episode/monitor", fmt.Sprintf(`{"episodeIds":[%d],"monitored":true}`, special)); code != 202 {
		t.Errorf("monitor episodes: %d", code)
	}
	if !g.lib.find(id).Monitored[special] {
		t.Error("the special wasn't monitored")
	}
	g.do(t, "POST", "tv/api/v3/command", fmt.Sprintf(`{"name":"MissingEpisodeSearch","seriesId":%d}`, id))
	g.do(t, "POST", "tv/api/v3/command", `{"name":"RefreshMonitoredDownloads"}`)
	if len(g.searched) != 2 || g.checked != 1 {
		t.Errorf("searched %v, checked %d", g.searched, g.checked)
	}
	// A title search finds both shows; the HD endpoint doesn't see TV's.
	_, b = g.do(t, "GET", "hd/api/v3/series/lookup?term=the", "")
	if l := decode[[]map[string]any](t, b); len(l) != 1 || l[0]["id"] != nil {
		t.Errorf("title lookup = %s", b)
	}
}

func TestDeleteKeepsFiles(t *testing.T) {
	g := newRig(t)
	code, _ := g.do(t, "DELETE", "hd/api/v3/series/50?deleteFiles=true&addImportExclusion=false", "")
	if code != 200 || !slices.Equal(g.lib.deleted, []int64{50}) || !slices.Equal(g.dl.removed, []int64{50}) {
		t.Errorf("delete: %d, deleted %v, downloads removed %v", code, g.lib.deleted, g.dl.removed)
	}
	if code, _ := g.do(t, "DELETE", "hd/api/v3/series/50", ""); code != 404 {
		t.Errorf("second delete: %d", code)
	}
}

func TestQueueAndCalendar(t *testing.T) {
	g := newRig(t)
	g.addDownloads()
	_, b := g.do(t, "GET", "hd/api/v3/queue?includeEpisode=true", "")
	q := decode[map[string]any](t, b)
	recs := q["records"].([]any)
	// The pack is a record per episode; the other profile's download isn't here.
	if q["totalRecords"].(float64) != 4 || len(recs) != 4 {
		t.Fatalf("queue = %s", b)
	}
	r0 := recs[0].(map[string]any)
	if r0["seriesId"].(float64) != 50 || r0["episodeId"].(float64) != 101 || r0["seasonNumber"].(float64) != 1 ||
		r0["sizeleft"].(float64) != 100 || r0["timeleft"] != "00:01:30" || r0["downloadId"] != "ABC" ||
		r0["episode"].(map[string]any)["episodeNumber"].(float64) != 1 || r0["quality"].(map[string]any)["quality"].(map[string]any)["id"].(float64) != 3 {
		t.Errorf("record = %v", r0)
	}
	if recs[0].(map[string]any)["id"] == recs[1].(map[string]any)["id"] {
		t.Error("records share an id")
	}
	_, b = g.do(t, "GET", "hd/api/v3/queue?pageSize=1&page=4", "")
	if q := decode[map[string]any](t, b); q["totalRecords"].(float64) != 4 || len(q["records"].([]any)) != 1 {
		t.Errorf("page 4 of 1: %s", b)
	}

	// The calendar: monitored episodes of monitored series in range.
	_, b = g.do(t, "GET", "hd/api/v3/calendar?start=2020-01-01&end=2020-12-31", "")
	if l := decode[[]map[string]any](t, b); len(l) != 4 || l[0]["series"] != nil {
		t.Errorf("calendar = %s", b)
	}
	_, b = g.do(t, "GET", "hd/api/v3/calendar?start=2020-01-01&end=2020-12-31&unmonitored=true&includeSeries=true", "")
	if l := decode[[]map[string]any](t, b); len(l) != 5 || l[0]["series"].(map[string]any)["title"] != "Velmora Quill" {
		t.Errorf("calendar with unmonitored = %s", b)
	}
	g.lib.series[0].Version.Monitored = false
	_, b = g.do(t, "GET", "hd/api/v3/calendar?start=2020-01-01&end=2099-12-31", "")
	if l := decode[[]any](t, b); len(l) != 0 {
		t.Errorf("calendar of an unmonitored series = %s", b)
	}
}

// Sonarr numbers qualities its own way; the profile and files use its
// numbers, and qualities it doesn't have are left out.
func TestSonarrQualities(t *testing.T) {
	for name, want := range map[string]int{"WEBDL-1080p": 3, "HDTV-720p": 4, "Bluray-1080p": 7, "Remux-2160p": 21, "Bluray-576p": 22} {
		if q, ok := qualityOf(name); !ok || q.ID != want {
			t.Errorf("%s = %+v", name, q)
		}
	}
	if q, ok := qualityOf("CAM"); ok || q.ID != 0 {
		t.Errorf("CAM = %+v, %v", q, ok)
	}
	g := newRig(t)
	_, b := g.do(t, "GET", "hd/api/v3/qualityProfile", "")
	profiles := decode[[]map[string]any](t, b)
	if len(profiles) != 1 || profiles[0]["name"] != "HD" || strings.Contains(string(b), `"CAM"`) || strings.Contains(string(b), `"Remux-1080p"`) {
		t.Errorf("profiles = %s", b)
	}
}
