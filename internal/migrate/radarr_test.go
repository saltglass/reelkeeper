package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// fakeRadarr serves a small Radarr: four movies, two tags, history and a
// blocklist entry.
func fakeRadarr(t *testing.T) *httptest.Server {
	t.Helper()
	movies := `[
	 {"id":1,"tmdbId":603,"title":"The Matrix","year":1999,"monitored":true,"hasFile":true,"path":"/media/movies/The Matrix (1999)","rootFolderPath":"/media/movies","tags":[1],
	  "movieFile":{"relativePath":"The Matrix (1999) Bluray-1080p.mkv","quality":{"quality":{"name":"Bluray-1080p"}}}},
	 {"id":2,"tmdbId":604,"title":"The Matrix Reloaded","year":2003,"monitored":false,"hasFile":true,"path":"/media/movies/The Matrix Reloaded (2003)","rootFolderPath":"/media/movies","tags":[],
	  "movieFile":{"relativePath":"Reloaded.mkv","quality":{"quality":{"name":"WEBDL-1080p"}}}},
	 {"id":3,"tmdbId":605,"title":"The Matrix Revolutions","year":2003,"monitored":true,"hasFile":false,"path":"/media/movies/The Matrix Revolutions (2003)","rootFolderPath":"/media/movies","tags":[2]},
	 {"id":4,"tmdbId":606,"title":"Elsewhere","year":2010,"monitored":true,"hasFile":false,"path":"/data/Elsewhere (2010)","rootFolderPath":"/data","tags":[]}]`
	history := `{"totalRecords":2,"records":[
	 {"id":10,"movieId":1,"sourceTitle":"The.Matrix.1999.1080p.BluRay.x264-GRP","eventType":"grabbed","date":"2026-01-01T10:00:00Z",
	  "quality":{"quality":{"name":"Bluray-1080p"}},"data":{"indexer":"1337x (Prowlarr)","downloadUrl":"https://prowlarr/1/download?apikey=SECRET"}},
	 {"id":11,"movieId":1,"sourceTitle":"The.Matrix.1999.1080p.BluRay.x264-GRP","eventType":"downloadFolderImported","date":"2026-01-01T11:00:00Z",
	  "quality":{"quality":{"name":"Bluray-1080p"}},"data":{"importedPath":"/media/movies/The Matrix (1999)/The Matrix (1999) Bluray-1080p.mkv"}}]}`
	blocklist := `{"totalRecords":1,"records":[{"id":5,"movieId":3,"sourceTitle":"The.Matrix.Revolutions.2003.CAM-BAD","indexer":"Knaben","message":"Fake",
	  "torrentInfoHash":"ABCDEF","quality":{"quality":{"name":"CAM"}},"date":"2026-01-02T00:00:00Z"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v3/system/status":
			_, _ = w.Write([]byte(`{"appName":"Radarr","instanceName":"Radarr","version":"6.4.4"}`))
		case "/api/v3/movie":
			_, _ = w.Write([]byte(movies))
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
	t.Cleanup(srv.Close)
	return srv
}

// fakeLib holds Reelkeeper's side: The Matrix found by the scan (as
// WEBDL-1080p, by its headers), an unmonitored copy of Reloaded with no
// file, and a movie Radarr doesn't have.
type fakeLib struct {
	Library // the series half, which Radarr's import never calls
	pms     []library.ProfileMovie
	ensured []int
	monitor map[int64]bool
	tags    map[int64][]int64
	labels  []string
}

func newFakeLib() *fakeLib {
	root := library.RootFolder{ID: 1, Path: "/media/movies", MediaType: library.Movie, DefaultProfileID: 1}
	return &fakeLib{monitor: map[int64]bool{}, tags: map[int64][]int64{}, pms: []library.ProfileMovie{
		{Title: library.Title{ID: 1, TMDBID: 603, Title: "The Matrix", Year: 1999}, Root: root,
			Version: library.Version{ID: 1, TitleID: 1, ProfileID: 1, RootFolderID: 1, Folder: "The Matrix (1999)", Monitored: true},
			Files:   []library.MediaFile{{Path: "The Matrix (1999)/The Matrix (1999) Bluray-1080p.mkv", Quality: "WEBDL-1080p"}}},
		{Title: library.Title{ID: 2, TMDBID: 604, Title: "The Matrix Reloaded", Year: 2003}, Root: root,
			Version: library.Version{ID: 2, TitleID: 2, ProfileID: 1, RootFolderID: 1, Monitored: true}},
		{Title: library.Title{ID: 3, TMDBID: 999, Title: "Only Here", Year: 2020}, Root: root,
			Version: library.Version{ID: 3, TitleID: 3, ProfileID: 1, RootFolderID: 1, Monitored: true}},
	}}
}

func (f *fakeLib) RootFolders(context.Context) ([]library.RootFolder, error) {
	return []library.RootFolder{{ID: 1, Path: "/media/movies", MediaType: library.Movie}}, nil
}
func (f *fakeLib) ProfileMovies(context.Context, int64, library.ProfileMovieFilter) ([]library.ProfileMovie, error) {
	return f.pms, nil
}
func (f *fakeLib) EnsureVersion(_ context.Context, tmdbID int, profileID, rootID int64, folder string, monitored bool) (*library.Version, bool, error) {
	for _, pm := range f.pms {
		if pm.Title.TMDBID == tmdbID {
			v := pm.Version
			return &v, false, nil
		}
	}
	f.ensured = append(f.ensured, tmdbID)
	id := int64(3 + len(f.ensured))
	f.pms = append(f.pms, library.ProfileMovie{Title: library.Title{ID: id, TMDBID: tmdbID},
		Version: library.Version{ID: id, TitleID: id, ProfileID: profileID, RootFolderID: rootID, Folder: folder, Monitored: monitored}})
	return &library.Version{ID: id, TitleID: id, Monitored: monitored, Folder: folder}, true, nil
}
func (f *fakeLib) SetMonitored(_ context.Context, _, versionID int64, m bool) error {
	f.monitor[versionID] = m
	return nil
}
func (f *fakeLib) CreateTag(_ context.Context, label string) (*library.Tag, error) {
	for i, l := range f.labels {
		if l == label {
			return &library.Tag{ID: int64(i + 1), Label: l}, nil
		}
	}
	f.labels = append(f.labels, label)
	return &library.Tag{ID: int64(len(f.labels)), Label: label}, nil
}
func (f *fakeLib) VersionTags(_ context.Context, ids []int64) (map[int64][]int64, error) {
	return map[int64][]int64{ids[0]: f.tags[ids[0]]}, nil
}
func (f *fakeLib) SetVersionTags(_ context.Context, v int64, tags []int64) error {
	f.tags[v] = tags
	return nil
}

// titles inserts the titles and versions history rows point at.
func titles(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	root := &library.RootFolder{Path: "/media/movies", MediaType: library.Movie, DefaultProfileID: 1, CreatedAt: now}
	if _, err := db.DB.NewInsert().Model(root).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	// Four movies, so titles and versions 1 to 4 exist (the fake library
	// below uses those IDs; a fresh database numbers from 1).
	for i := range 4 {
		ti := &library.Title{MediaType: library.Movie, TMDBID: 9000 + i, Title: "x", CreatedAt: now, Genres: "[]", AltTitles: "[]"}
		if _, err := db.DB.NewInsert().Model(ti).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		v := &library.Version{TitleID: ti.ID, ProfileID: 1, RootFolderID: root.ID, CreatedAt: now}
		if _, err := db.DB.NewInsert().Model(v).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if ti.ID != int64(i+1) || v.ID != int64(i+1) {
			t.Fatalf("expected IDs %d, got title %d version %d", i+1, ti.ID, v.ID)
		}
	}
}

func TestPreviewAndImport(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		titles(t, db)
		radarr := fakeRadarr(t)
		lib := newFakeLib()
		hist := grab.NewHistory(db)
		var blocked []download.BlockedRelease
		svc := New(db, lib, hist, func(_ context.Context, b download.BlockedRelease) error { blocked = append(blocked, b); return nil })
		o := Options{URL: radarr.URL, APIKey: "key", ProfileID: 1}

		rep, err := svc.Preview(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		kinds := map[string][]string{}
		for _, is := range rep.Issues {
			kinds[is.Kind] = append(kinds[is.Kind], is.Title)
		}
		if rep.Movies != 4 || rep.WithFile != 2 || rep.Instance != "Radarr" || rep.Version != "6.4.4" || rep.Monitored != 3 || rep.Wanted != 2 || rep.ToAdd != 2 || rep.History != 2 || rep.Blocklist != 1 {
			t.Errorf("counts = %+v", rep)
		}
		if len(kinds["quality-differs"]) != 1 || len(kinds["missing-in-reelkeeper"]) != 1 || len(kinds["only-in-reelkeeper"]) != 1 ||
			len(kinds["no-media-folder"]) != 1 {
			t.Errorf("issues = %v", kinds)
		}
		if len(lib.ensured) != 0 || len(blocked) != 0 {
			t.Fatal("the preview changed something")
		}

		rep, err = svc.Import(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		if !rep.Imported || rep.Added != 1 || rep.MonitorChanged != 1 || rep.HistoryAdded != 2 || rep.BlocklistAdded != 1 {
			t.Errorf("import = %+v", rep)
		}
		// Revolutions (wanted) was added; Elsewhere (not one of Reelkeeper's
		// folders) wasn't.
		if len(lib.ensured) != 1 || lib.ensured[0] != 605 {
			t.Errorf("added %v", lib.ensured)
		}
		if lib.monitor[2] != false {
			t.Error("Reloaded should become unmonitored, as in Radarr")
		}
		if len(lib.labels) != 2 || len(lib.tags[1]) != 1 {
			t.Errorf("tags: labels %v, matrix %v", lib.labels, lib.tags[1])
		}
		evs, _ := hist.List(ctx, grab.ListOptions{Events: []string{grab.EventFromRadarr}})
		if len(evs) != 2 || !strings.Contains(evs[1].Message, "Radarr grabbed The.Matrix") || evs[1].Indexer != "1337x (Prowlarr)" {
			t.Errorf("history = %+v", evs)
		}
		for _, ev := range evs {
			b, _ := json.Marshal(ev)
			if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "apikey") {
				t.Fatal("a download URL with a key was stored")
			}
		}
		if len(blocked) != 1 || blocked[0].Hash != "abcdef" || !strings.Contains(blocked[0].Reason, "Fake") ||
			blocked[0].Rule != download.BlockRadarr || blocked[0].Temporary {
			t.Errorf("blocklist = %+v", blocked)
		}

		// Importing again adds no history or blocklist twice.
		rep, err = svc.Import(ctx, o)
		if err != nil || rep.HistoryAdded != 0 || rep.BlocklistAdded != 0 || rep.Added != 0 {
			t.Errorf("second import = %+v, %v", rep, err)
		}
	})
}

func TestBadKeyAndNotRadarr(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		radarr := fakeRadarr(t)
		svc := New(db, newFakeLib(), grab.NewHistory(db), nil)
		if _, err := svc.Preview(context.Background(), Options{URL: radarr.URL, APIKey: "wrong"}); !errors.Is(err, ErrRadarr) ||
			!strings.Contains(err.Error(), "refused the API key") {
			t.Errorf("wrong key: %v", err)
		}
		if _, err := svc.Preview(context.Background(), Options{URL: "http://127.0.0.1:1", APIKey: "key"}); err == nil ||
			!strings.Contains(err.Error(), "can't reach Radarr") {
			t.Errorf("unreachable: %v", err)
		}
	})
}
