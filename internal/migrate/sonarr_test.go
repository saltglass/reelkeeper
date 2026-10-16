package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/metadata"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// seriesTMDB is a TMDB with a few made-up series. It answers only the TV
// calls the import makes.
type seriesTMDB struct {
	series map[int]*metadata.Series
	groups map[string]*metadata.EpisodeGroup
}

func (f *seriesTMDB) SearchMovies(context.Context, string, int) ([]metadata.MovieResult, error) {
	return nil, errors.New("not used")
}
func (f *seriesTMDB) Movie(context.Context, int) (*metadata.Movie, error) {
	return nil, errors.New("not used")
}
func (f *seriesTMDB) FindByIMDb(context.Context, string) (*metadata.MovieResult, error) {
	return nil, errors.New("not used")
}
func (f *seriesTMDB) SearchTV(context.Context, string, int) ([]metadata.SeriesResult, error) {
	return nil, errors.New("not used")
}
func (f *seriesTMDB) Series(_ context.Context, id int) (*metadata.Series, error) {
	if s, ok := f.series[id]; ok {
		return s, nil
	}
	return nil, metadata.ErrNotFound
}
func (f *seriesTMDB) FindSeriesByTVDB(context.Context, int) (*metadata.SeriesResult, error) {
	return nil, metadata.ErrNotFound
}
func (f *seriesTMDB) FindSeriesByIMDb(context.Context, string) (*metadata.SeriesResult, error) {
	return nil, metadata.ErrNotFound
}
func (f *seriesTMDB) EpisodeGroup(_ context.Context, id string) (*metadata.EpisodeGroup, error) {
	if g, ok := f.groups[id]; ok {
		return g, nil
	}
	return nil, metadata.ErrNotFound
}

// tmdbEp is one TMDB episode: season, number, air date.
type tmdbEp struct {
	season, number int
	date           string
}

func newSeriesTMDB() *seriesTMDB {
	f := &seriesTMDB{series: map[int]*metadata.Series{}, groups: map[string]*metadata.EpisodeGroup{}}
	add := func(id, tvdb int, name string, eps ...tmdbEp) {
		s := &metadata.Series{ID: id, TVDBID: tvdb, Name: name, OriginalName: name, FirstAirDate: eps[0].date, Type: "Scripted"}
		seasons := map[int]*metadata.Season{}
		for _, e := range eps {
			if seasons[e.season] == nil {
				seasons[e.season] = &metadata.Season{Number: e.season, Name: fmt.Sprintf("Season %d", e.season)}
			}
			seasons[e.season].Episodes = append(seasons[e.season].Episodes, metadata.Episode{
				ID: id*100 + e.season*10 + e.number, Season: e.season, Number: e.number,
				Name: fmt.Sprintf("Episode %d-%d", e.season, e.number), AirDate: e.date,
			})
		}
		for n := 0; n < 10; n++ {
			if seasons[n] != nil {
				s.Seasons = append(s.Seasons, *seasons[n])
			}
		}
		f.series[id] = s
	}
	// Harbor Lights: aired order, with a special and an episode that hasn't aired.
	add(7001, 8001, "Harbor Lights",
		tmdbEp{0, 1, "2014-12-25"},
		tmdbEp{1, 1, "2015-01-01"}, tmdbEp{1, 2, "2015-01-08"}, tmdbEp{1, 3, "2015-01-15"}, tmdbEp{1, 4, "2015-01-22"},
		tmdbEp{2, 1, "2016-01-01"}, tmdbEp{2, 2, "2016-01-08"}, tmdbEp{2, 3, "2016-01-15"},
		tmdbEp{3, 1, "2099-01-01"})
	// Numbers Differ: aired order, but Reelkeeper follows a group that swaps the first two.
	add(7002, 8002, "Numbers Differ",
		tmdbEp{1, 1, "2014-03-01"}, tmdbEp{1, 2, "2014-03-08"}, tmdbEp{1, 3, "2014-03-15"},
		tmdbEp{2, 1, "2099-02-01"}, tmdbEp{2, 2, "2099-02-08"})
	f.series[7002].EpisodeGroups = []metadata.EpisodeGroupInfo{{ID: "swapped", Name: "Swapped", Type: 3}}
	f.groups["swapped"] = &metadata.EpisodeGroup{ID: "swapped", Name: "Swapped", Type: 3, Episodes: []metadata.GroupEpisode{
		{TMDBID: 700211, Season: 1, Number: 2}, {TMDBID: 700212, Season: 1, Number: 1}, {TMDBID: 700213, Season: 1, Number: 3},
		{TMDBID: 700221, Season: 2, Number: 1}, {TMDBID: 700222, Season: 2, Number: 2}}}
	// Ambiguous: two episodes share a date.
	add(7003, 8003, "Ambiguous", tmdbEp{1, 1, "2012-05-01"}, tmdbEp{1, 2, "2012-05-05"}, tmdbEp{1, 3, "2012-05-05"})
	// Sonarr Only: Reelkeeper's scan never found it.
	add(7004, 8004, "Sonarr Only", tmdbEp{1, 1, "2013-09-01"}, tmdbEp{1, 2, "2013-09-08"}, tmdbEp{1, 3, "2013-09-15"})
	add(7006, 8006, "Only Here", tmdbEp{1, 1, "2011-01-01"})
	return f
}

// sonarrEp is one Sonarr episode: series, season, number, air date,
// monitored, has a file. Its ID is made from the first three.
type sonarrEp struct {
	series, season, number int
	date                   string
	monitored, hasFile     bool
}

func (e sonarrEp) json() map[string]any {
	m := map[string]any{"id": e.series*1000 + e.season*100 + e.number, "seasonNumber": e.season, "episodeNumber": e.number,
		"title": "x", "monitored": e.monitored, "hasFile": e.hasFile}
	if e.date != "" {
		m["airDate"] = e.date
	}
	return m
}

// fakeSonarr serves a small Sonarr (series 1 to 5, see the test for what
// each is) and counts requests that aren't reads.
type fakeSonarr struct {
	*httptest.Server
	writes int
}

func newFakeSonarr(t *testing.T, tvRoot string) *fakeSonarr {
	t.Helper()
	series := []map[string]any{
		{"id": 1, "tvdbId": 8001, "tmdbId": 7001, "title": "Harbor Lights", "year": 2015, "monitored": true, "monitorNewItems": "all",
			"path": tvRoot + "/Harbor Lights", "rootFolderPath": tvRoot, "tags": []int{1}},
		{"id": 2, "tvdbId": 8002, "tmdbId": 7002, "title": "Numbers Differ", "year": 2014, "monitored": true, "monitorNewItems": "none",
			"path": tvRoot + "/Numbers Differ", "rootFolderPath": tvRoot, "tags": []int{}},
		// No TMDB ID: matched by TVDB ID.
		{"id": 3, "tvdbId": 8003, "tmdbId": 0, "title": "Ambiguous", "year": 2012, "monitored": false, "monitorNewItems": "all",
			"path": tvRoot + "/Ambiguous", "rootFolderPath": tvRoot, "tags": []int{2}},
		{"id": 4, "tvdbId": 8004, "tmdbId": 7004, "title": "Sonarr Only", "year": 2013, "monitored": true, "monitorNewItems": "all",
			"path": tvRoot + "/Sonarr Only", "rootFolderPath": tvRoot, "tags": []int{}},
		{"id": 5, "tvdbId": 8005, "tmdbId": 7005, "title": "Elsewhere", "year": 2010, "monitored": true, "monitorNewItems": "all",
			"path": "/data/tv/Elsewhere", "rootFolderPath": "/data/tv", "tags": []int{}},
	}
	eps := map[string][]sonarrEp{
		"1": {
			{1, 0, 1, "2014-12-25", false, false},
			{1, 1, 1, "2015-01-01", true, true}, {1, 1, 2, "2015-01-08", true, false}, {1, 1, 3, "2015-01-15", false, false},
			{1, 1, 4, "2015-01-22", true, false},
			{1, 2, 1, "2016-01-01", true, false}, {1, 2, 2, "2016-01-08", true, false}, {1, 2, 3, "2016-01-15", false, true},
			{1, 3, 1, "2099-01-01", true, false},
		},
		// Sonarr numbers these in aired order; Reelkeeper follows the group.
		"2": {
			{2, 1, 1, "2014-03-01", true, false}, {2, 1, 2, "2014-03-08", true, false}, {2, 1, 3, "2014-03-15", false, false},
			{2, 2, 1, "2099-02-01", false, false}, {2, 2, 2, "2099-02-08", true, false},
		},
		"3": {
			{3, 1, 1, "2012-05-01", true, false}, {3, 1, 2, "2012-05-05", true, false},
			{3, 1, 7, "2012-05-05", true, false}, // two Reelkeeper episodes share this date
		},
		"4": {{4, 1, 1, "2013-09-01", true, true}, {4, 1, 2, "2013-09-08", true, false}, {4, 1, 3, "2013-09-15", true, false}},
		"5": {{5, 1, 1, "2010-01-01", true, false}},
	}
	// SECRET stands in for Prowlarr's API key inside the download URL.
	history := `{"totalRecords":4,"records":[
	 {"id":10,"seriesId":1,"episodeId":1102,"sourceTitle":"Harbor.Lights.S01E02.1080p.WEB-GRP","eventType":"grabbed","date":"2026-01-01T10:00:00Z",
	  "quality":{"quality":{"name":"WEBDL-1080p"}},"data":{"indexer":"1337x (Prowlarr)","downloadUrl":"https://prowlarr/1/download?apikey=SECRET"}},
	 {"id":11,"seriesId":1,"episodeId":1101,"sourceTitle":"Harbor.Lights.S01E01.1080p.WEB-GRP","eventType":"downloadFolderImported","date":"2026-01-01T11:00:00Z",
	  "quality":{"quality":{"name":"WEBDL-1080p"}},"data":{"importedPath":"/media/shows/Harbor Lights/S01E01.mkv"}},
	 {"id":12,"seriesId":4,"episodeId":4102,"sourceTitle":"Sonarr.Only.S01E02.720p-GRP","eventType":"grabbed","date":"2026-01-02T10:00:00Z",
	  "quality":{"quality":{"name":"HDTV-720p"}},"data":{"indexer":"Knaben","downloadUrl":"https://prowlarr/2/download?apikey=SECRET"}},
	 {"id":13,"seriesId":5,"episodeId":5101,"sourceTitle":"Elsewhere.S01E01-GRP","eventType":"grabbed","date":"2026-01-03T10:00:00Z",
	  "quality":{"quality":{"name":"HDTV-720p"}},"data":{"indexer":"Knaben"}}]}`
	blocklist := `{"totalRecords":2,"records":[
	 {"id":5,"seriesId":1,"episodeIds":[1104],"sourceTitle":"Harbor.Lights.S01E04.CAM-BAD","indexer":"Knaben","message":"Fake","torrentInfoHash":"ABCDEF",
	  "quality":{"quality":{"name":"HDTV-720p"}},"date":"2026-01-02T00:00:00Z"},
	 {"id":6,"seriesId":5,"episodeIds":[5101],"sourceTitle":"Elsewhere.CAM","indexer":"Knaben","message":"","torrentInfoHash":"123456",
	  "quality":{"quality":{"name":"HDTV-720p"}},"date":"2026-01-02T00:00:00Z"}]}`
	f := &fakeSonarr{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			f.writes++
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Api-Key") != "key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/api/v3/system/status":
			_, _ = w.Write([]byte(`{"appName":"Sonarr","instanceName":"Sonarr","version":"4.0.20"}`))
		case "/api/v3/series":
			write(series)
		case "/api/v3/episode":
			out := []map[string]any{}
			for _, e := range eps[r.URL.Query().Get("seriesId")] {
				out = append(out, e.json())
			}
			write(out)
		case "/api/v3/tag":
			_, _ = w.Write([]byte(`[{"id":1,"label":"1 - dan"},{"id":2,"label":"2 - beth"}]`))
		case "/api/v3/history":
			_, _ = w.Write([]byte(history))
		case "/api/v3/blocklist":
			_, _ = w.Write([]byte(blocklist))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// sonarrFixture is Reelkeeper's side: the real library, with Harbor Lights,
// Numbers Differ, Ambiguous and Only Here found by the scan.
type sonarrFixture struct {
	db     *store.Store
	lib    *library.Service
	svc    *Service
	hist   *grab.History
	root   *library.RootFolder
	blocks []download.BlockedRelease
	ids    map[int]int64 // TMDB ID → version ID
	titles map[int]int64 // TMDB ID → title ID
}

func newSonarrFixture(t *testing.T, db *store.Store) *sonarrFixture {
	t.Helper()
	ctx := context.Background()
	f := &sonarrFixture{db: db, ids: map[int]int64{}, titles: map[int]int64{}}
	f.lib = library.New(db, newSeriesTMDB(), slog.New(slog.DiscardHandler))
	f.hist = grab.NewHistory(db)
	f.svc = New(db, f.lib, f.hist, func(_ context.Context, b download.BlockedRelease) error {
		f.blocks = append(f.blocks, b)
		return nil
	})
	var err error
	if f.root, err = f.lib.AddRootFolder(ctx, library.RootInput{Path: t.TempDir(), MediaType: library.Series, DefaultProfileID: 1}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{7001, 7002, 7003, 7006} {
		d, err := f.lib.AddSeries(ctx, id, 1, f.root.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		f.ids[id], f.titles[id] = d.Versions[0].ID, d.ID
		// Like a fresh scan: only new episodes wanted.
		if err := f.lib.ApplyMonitorPreset(ctx, d.Versions[0].ID, library.MonitorFuture); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.lib.SetEpisodeOrder(ctx, f.titles[7002], "swapped"); err != nil {
		t.Fatal(err)
	}
	if err := f.lib.ApplyMonitorPreset(ctx, f.ids[7002], library.MonitorFuture); err != nil {
		t.Fatal(err)
	}
	// The scan found Harbor Lights S01E01.
	f.addFile(t, 7001, "S01E01")
	return f
}

// episode finds a Reelkeeper episode of a series by its season and number
// in the series' chosen order.
func (f *sonarrFixture) episode(t *testing.T, tmdb, season, number int) library.Episode {
	t.Helper()
	ps, err := f.lib.ProfileSeries(context.Background(), 1, library.ProfileSeriesFilter{TMDBID: tmdb})
	if err != nil || len(ps) != 1 {
		t.Fatalf("series %d: %v (%d found)", tmdb, err, len(ps))
	}
	for _, e := range ps[0].Episodes {
		if *e.Season == season && *e.Number == number {
			return e
		}
	}
	t.Fatalf("series %d has no S%02dE%02d", tmdb, season, number)
	return library.Episode{}
}

func (f *sonarrFixture) addFile(t *testing.T, tmdb int, label string) {
	t.Helper()
	var season, number int
	_, _ = fmt.Sscanf(label, "S%02dE%02d", &season, &number)
	e := f.episode(t, tmdb, season, number)
	now := time.Now().UTC()
	mf := &library.MediaFile{VersionID: f.ids[tmdb], RootFolderID: f.root.ID, Path: label + ".mkv", Size: 1, ModifiedAt: now, AddedAt: now,
		ReleaseInfo: "{}", MediaInfo: "{}", EpisodeClaim: "{}"}
	ctx := context.Background()
	if _, err := f.db.DB.NewInsert().Model(mf).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.DB.NewInsert().Model(&library.EpisodeFile{EpisodeID: e.ID, MediaFileID: mf.ID}).Exec(ctx); err != nil {
		t.Fatal(err)
	}
}

// state is what Reelkeeper wants of a series: its version's monitored flag,
// and each wanted episode's number in the chosen order.
func (f *sonarrFixture) state(t *testing.T, tmdb int) (bool, map[string]bool) {
	t.Helper()
	ps, err := f.lib.ProfileSeries(context.Background(), 1, library.ProfileSeriesFilter{TMDBID: tmdb})
	if err != nil || len(ps) != 1 {
		t.Fatalf("series %d: %v (%d found)", tmdb, err, len(ps))
	}
	wanted := map[string]bool{}
	for _, e := range ps[0].Episodes {
		if ps[0].Monitored[e.ID] {
			wanted[fmt.Sprintf("S%02dE%02d", *e.Season, *e.Number)] = true
		}
	}
	return ps[0].Version.Monitored, wanted
}

func wantSet(t *testing.T, name string, got map[string]bool, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: wanted %v, want %v", name, got, want)
		return
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("%s: wanted %v, want %v", name, got, want)
			return
		}
	}
}

func TestSonarrPreviewAndImport(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newSonarrFixture(t, db)
		sonarr := newFakeSonarr(t, f.root.Path)
		o := Options{URL: sonarr.URL, APIKey: "key", ProfileID: 1}

		// Reelkeeper starts out wanting only what hasn't aired.
		_, harbor := f.state(t, 7001)
		wantSet(t, "Harbor Lights before", harbor, "S03E01")

		rep, err := f.svc.PreviewSonarr(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Instance != "Sonarr" || rep.Version != "4.0.20" || rep.Series != 5 || rep.ToAdd != 2 || rep.MonitoredSeries != 4 ||
			rep.Episodes != 9+5+3+3+1 || rep.Matched != 9+3+2 || rep.MatchedByDate != 2 || rep.Unmatched != 1 ||
			rep.NewlyWanted != 6 || rep.AlreadyWanted != 0 || rep.TurnedOff != 1 || rep.History != 4 || rep.Blocklist != 2 || rep.Imported {
			t.Errorf("counts = %+v", rep)
		}
		wantList := []WantedSeries{
			{Title: "Harbor Lights", Year: 2015, Seasons: []WantedSeason{{1, 2}, {2, 2}}},
			{Title: "Numbers Differ", Year: 2014, Seasons: []WantedSeason{{1, 2}}},
		}
		if got, _ := json.Marshal(rep.Wanted); string(got) != mustJSON(wantList) {
			t.Errorf("wanted = %s", got)
		}
		details := map[string]string{}
		for _, is := range rep.Issues {
			details[is.Kind+"/"+is.Title] = is.Detail
		}
		for _, k := range []string{"files-differ/Harbor Lights", "numbered-differently/Numbers Differ", "unmatched-episodes/Ambiguous",
			"missing-in-reelkeeper/Sonarr Only", "no-media-folder/Elsewhere", "only-in-reelkeeper/Only Here"} {
			if details[k] == "" {
				t.Errorf("no issue %s in %v", k, details)
			}
		}
		if len(rep.Issues) != 6 {
			t.Errorf("issues = %+v", rep.Issues)
		}
		if d := details["numbered-differently/Numbers Differ"]; !strings.Contains(d, "2 episodes matched by air date") {
			t.Errorf("numbering detail = %q", d)
		}
		if d := details["unmatched-episodes/Ambiguous"]; !strings.Contains(d, "1 Sonarr episode couldn't be matched") || !strings.Contains(d, "S01E07") {
			t.Errorf("unmatched detail = %q", d)
		}
		if d := details["files-differ/Harbor Lights"]; !strings.Contains(d, "S02E03") || strings.Contains(d, "S01E01") {
			t.Errorf("files detail = %q", d)
		}

		// The preview changed nothing.
		_, harbor = f.state(t, 7001)
		wantSet(t, "Harbor Lights after preview", harbor, "S03E01")
		if evs, _ := f.hist.List(ctx, grab.ListOptions{Events: []string{grab.EventFromSonarr}}); len(evs) != 0 || len(f.blocks) != 0 {
			t.Error("the preview wrote history or the blocklist")
		}
		if ps, _ := f.lib.ProfileSeries(ctx, 1, library.ProfileSeriesFilter{TMDBID: 7004}); len(ps) != 0 {
			t.Error("the preview added a series")
		}

		rep, err = f.svc.ImportSonarr(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		// Sonarr Only was added: its three aired episodes join the wanted ones.
		if !rep.Imported || rep.Added != 1 || rep.MonitorChanged != 1 || rep.NewlyWanted != 9 || rep.Matched != 9+3+2+3 ||
			rep.EpisodesTurnedOn != 5+2+2+3 || rep.EpisodesTurnedOff != 1 || rep.HistoryAdded != 3 || rep.BlocklistAdded != 1 {
			t.Errorf("import = %+v", rep)
		}
		if len(rep.Wanted) != 3 || rep.Wanted[2].Title != "Sonarr Only" || rep.Wanted[2].Seasons[0].Episodes != 3 {
			t.Errorf("wanted after import = %+v", rep.Wanted)
		}
		if sonarr.writes != 0 {
			t.Error("Sonarr was written to")
		}

		// Harbor Lights: Sonarr's choices, old episodes included. Episodes
		// Sonarr doesn't monitor stay off.
		mon, harbor := f.state(t, 7001)
		wantSet(t, "Harbor Lights", harbor, "S01E01", "S01E02", "S01E04", "S02E01", "S02E02", "S03E01")
		if !mon {
			t.Error("Harbor Lights should stay monitored")
		}
		// Numbers Differ: matched by air date, so the swapped numbers are
		// handled; "new items: none" turned the date rule off, but the
		// future episode Sonarr monitors stays wanted.
		_, numbers := f.state(t, 7002)
		wantSet(t, "Numbers Differ", numbers, "S01E01", "S01E02", "S02E02")
		ps, _ := f.lib.ProfileSeries(ctx, 1, library.ProfileSeriesFilter{TMDBID: 7002})
		if ps[0].Version.MonitorFrom != nil {
			t.Errorf("Numbers Differ still has the new-episode rule: %v", *ps[0].Version.MonitorFrom)
		}
		// Ambiguous: Sonarr has it unmonitored; the unmatched S01E07 changed
		// nothing, so the episode sharing its date is not wanted.
		mon, ambiguous := f.state(t, 7003)
		if mon {
			t.Error("Ambiguous should become unmonitored, as in Sonarr")
		}
		wantSet(t, "Ambiguous", ambiguous, "S01E01", "S01E02")
		// Sonarr Only was added with Sonarr's choices.
		mon, only := f.state(t, 7004)
		if !mon {
			t.Error("Sonarr Only should be monitored")
		}
		wantSet(t, "Sonarr Only", only, "S01E01", "S01E02", "S01E03")
		// Only Here isn't Sonarr's: untouched.
		_, here := f.state(t, 7006)
		wantSet(t, "Only Here", here)

		// Tags came across by label.
		tags, _ := f.lib.VersionTags(ctx, []int64{f.ids[7001], f.ids[7003]})
		if len(tags[f.ids[7001]]) != 1 || len(tags[f.ids[7003]]) != 1 {
			t.Errorf("tags = %v", tags)
		}

		// History: three records, the two for Harbor Lights pointing at the
		// episodes they were about, none for the folder Reelkeeper lacks.
		evs, _ := f.hist.List(ctx, grab.ListOptions{Events: []string{grab.EventFromSonarr}})
		if len(evs) != 3 {
			t.Fatalf("history = %+v", evs)
		}
		sawGrab := false
		for _, ev := range evs {
			b, _ := json.Marshal(ev)
			if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "apikey") || strings.Contains(string(b), "prowlarr") {
				t.Fatal("a download URL with a key was stored")
			}
			if strings.HasPrefix(ev.Message, "Sonarr grabbed Harbor.Lights.S01E02") {
				sawGrab = true
				want := f.episode(t, 7001, 1, 2).ID
				var data grab.EventData
				_ = json.Unmarshal([]byte(ev.Data), &data) // Postgres re-spaces the JSON, so read it
				if ev.Indexer != "1337x (Prowlarr)" || len(data.EpisodeIDs) != 1 || data.EpisodeIDs[0] != want {
					t.Errorf("grab event = %+v (want episode %d)", ev, want)
				}
			}
		}
		if !sawGrab {
			t.Errorf("no grab event in %+v", evs)
		}
		if b, _ := json.Marshal(rep); strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "apikey") {
			t.Fatal("the report holds a download URL")
		}
		if len(f.blocks) != 1 || f.blocks[0].Hash != "abcdef" || f.blocks[0].TitleID != f.titles[7001] || !strings.Contains(f.blocks[0].Reason, "Fake") ||
			f.blocks[0].Rule != download.BlockSonarr || f.blocks[0].Temporary {
			t.Errorf("blocklist = %+v", f.blocks)
		}

		// Again: nothing is added or written twice.
		rep, err = f.svc.ImportSonarr(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Added != 0 || rep.MonitorChanged != 0 || rep.EpisodesTurnedOn != 0 || rep.EpisodesTurnedOff != 0 ||
			rep.HistoryAdded != 0 || rep.BlocklistAdded != 0 || rep.NewlyWanted != 0 || rep.AlreadyWanted != 9 {
			t.Errorf("second import = %+v", rep)
		}
		_, harbor2 := f.state(t, 7001)
		wantSet(t, "Harbor Lights again", harbor2, "S01E01", "S01E02", "S01E04", "S02E01", "S02E02", "S03E01")
		if evs, _ := f.hist.List(ctx, grab.ListOptions{Events: []string{grab.EventFromSonarr}}); len(evs) != 3 || len(f.blocks) != 1 {
			t.Error("history or blocklist doubled")
		}
	})
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestSonarrErrors(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newSonarrFixture(t, db)
		sonarr := newFakeSonarr(t, f.root.Path)

		_, err := f.svc.PreviewSonarr(ctx, Options{URL: sonarr.URL, APIKey: "wrong", ProfileID: 1})
		if !errors.Is(err, ErrSonarr) || !errors.Is(err, ErrArr) || errors.Is(err, ErrRadarr) || !strings.Contains(err.Error(), "Sonarr refused the API key") {
			t.Errorf("wrong key: %v", err)
		}
		_, err = f.svc.ImportSonarr(ctx, Options{URL: "http://127.0.0.1:1", APIKey: "key", ProfileID: 1})
		if !errors.Is(err, ErrSonarr) || !strings.Contains(err.Error(), "can't reach Sonarr") {
			t.Errorf("unreachable: %v", err)
		}
		// A Radarr answering where a Sonarr should be.
		radarr := fakeRadarr(t)
		_, err = f.svc.PreviewSonarr(ctx, Options{URL: radarr.URL, APIKey: "key", ProfileID: 1})
		if !errors.Is(err, ErrSonarr) || err.Error() != "That address is Radarr, not Sonarr" {
			t.Errorf("wrong app: %v", err)
		}
		// And the other way round, with its own message.
		_, err = f.svc.Preview(ctx, Options{URL: sonarr.URL, APIKey: "key", ProfileID: 1})
		if !errors.Is(err, ErrRadarr) || errors.Is(err, ErrSonarr) || err.Error() != "That address is Sonarr, not Radarr" {
			t.Errorf("Radarr import pointed at Sonarr: %v", err)
		}
	})
}

// The matching rules, without a database.
func TestPlanSeriesMatching(t *testing.T) {
	ep := func(id int64, season, number int, date string) library.Episode {
		return library.Episode{ID: id, Season: &season, Number: &number, AirDate: date}
	}
	ps := library.ProfileSeries{
		SeriesVersion: library.SeriesVersion{
			Version: library.Version{Monitored: true, MonitorFrom: ptrTo("2026-10-11")},
			Episodes: []library.Episode{
				ep(1, 1, 1, "2020-01-10"), ep(2, 1, 2, "2020-01-17"), ep(3, 1, 3, "2020-01-24"),
				ep(4, 2, 1, "2021-03-01"), ep(5, 2, 2, "2021-03-01"), // same date twice
				ep(6, 3, 1, "2023-01-01"), ep(7, 3, 2, ""), // the last has no date
			},
			HasFile: map[int64]bool{},
		},
	}
	se := func(id int64, season, number int, date string) sEpisode {
		return sEpisode{ID: id, SeasonNumber: season, EpisodeNumber: number, AirDate: date, Monitored: true}
	}
	eps := []sEpisode{
		se(101, 1, 1, "2020-01-10"), // same number, same date
		se(102, 1, 2, "2020-01-18"), // a day off still matches by number
		se(103, 1, 9, "2020-01-24"), // other number, unique date: by date
		se(104, 2, 7, "2021-03-01"), // date shared by two: unmatched
		se(105, 3, 1, "2022-02-02"), // number matches but the dates are years apart: unmatched
		se(106, 4, 1, ""),           // nothing to go on: unmatched
		se(107, 3, 2, "2022-02-02"), // Reelkeeper has no date for it: the number is enough
	}
	p := planSeries(sSeries{Monitored: true, MonitorNewItems: "all"}, ps, map[int64]bool{}, eps, "2026-10-11")
	if p.byNumber != 3 || p.byDate != 1 || len(p.unmatched) != 3 {
		t.Fatalf("plan = %+v", p)
	}
	if p.matches[103] != 3 || p.matches[101] != 1 || p.matches[102] != 2 || p.matches[107] != 7 {
		t.Errorf("matches = %v", p.matches)
	}
	labels := []string{}
	for _, e := range p.unmatched {
		labels = append(labels, e.label())
	}
	if strings.Join(labels, " ") != "S02E07 S03E01 S04E01" {
		t.Errorf("unmatched = %v", labels)
	}
	if len(p.writeOn) != 4 || len(p.writeOff) != 0 {
		t.Errorf("writes: on %v off %v", p.writeOn, p.writeOff)
	}

	// Two Sonarr episodes that could only be the same Reelkeeper episode
	// are both left unmatched.
	twice := []sEpisode{se(201, 1, 8, "2020-01-24"), se(202, 1, 9, "2020-01-24")}
	p = planSeries(sSeries{Monitored: true}, ps, map[int64]bool{}, twice, "2026-10-11")
	if p.byDate != 0 || len(p.unmatched) != 2 {
		t.Errorf("two for one: %+v", p)
	}
}

func ptrTo[T any](v T) *T { return &v }

func TestDatesAgree(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"2020-01-10", "2020-01-10", true}, {"2020-01-10", "2020-01-11", true}, {"2020-01-11", "2020-01-10", true},
		{"2020-01-10", "2020-01-12", false}, {"", "2020-01-12", true}, {"2020-01-12", "", true}, {"2020-12-31", "2021-01-01", true},
		{"nonsense", "2020-01-12", false},
	} {
		if got := datesAgree(c.a, c.b); got != c.want {
			t.Errorf("datesAgree(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
