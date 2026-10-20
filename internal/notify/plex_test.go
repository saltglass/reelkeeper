package notify_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/settings"
)

// fakePlex answers like Plex Media Server: its details at /, its libraries
// at /library/sections (three: Movies, TV Shows over two folders, and Remux
// inside the Movies folder), and folder scans. It records the scans.
type fakePlex struct {
	mu       sync.Mutex
	scans    []string // "section path"
	tokens   []string
	inURL    bool // a token was sent in the address
	sections string
}

const plexSections = `{"MediaContainer":{"size":3,"Directory":[
	{"key":"1","title":"Movies","type":"movie","Location":[{"id":1,"path":"/data/movies"}]},
	{"key":"2","title":"TV Shows","type":"show","Location":[{"id":2,"path":"/data/tv"},{"id":3,"path":"/data/tv-kids/"}]},
	{"key":"3","title":"Remux","type":"movie","Location":[{"id":4,"path":"/data/movies/remux"}]}]}}`

func (p *fakePlex) server(t *testing.T) *httptest.Server {
	if p.sections == "" {
		p.sections = plexSections
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.tokens = append(p.tokens, r.Header.Get("X-Plex-Token"))
		if r.URL.Query().Has("X-Plex-Token") {
			p.inURL = true
		}
		if r.Header.Get("X-Plex-Token") != "plex-token" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Accept") != "application/json" {
			http.Error(w, "this stub only speaks JSON", http.StatusNotAcceptable)
			return
		}
		switch {
		case r.URL.Path == "/":
			_, _ = w.Write([]byte(`{"MediaContainer":{"friendlyName":"den","machineIdentifier":"0123abcd","version":"1.41.2.9200-c6bbc1b53"}}`))
		case r.URL.Path == "/library/sections":
			_, _ = w.Write([]byte(p.sections))
		case strings.HasPrefix(r.URL.Path, "/library/sections/") && strings.HasSuffix(r.URL.Path, "/refresh"):
			key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/library/sections/"), "/refresh")
			p.scans = append(p.scans, key+" "+r.URL.Query().Get("path"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func plexCfg(url string) settings.MediaServerNotify {
	return settings.MediaServerNotify{Enabled: true, URL: url, APIKey: "plex-token", PathFrom: "/media", PathTo: "/data"}
}

// Each change scans just the folder, in the library that holds it (the most
// specific one when libraries nest), with Plex's path, and the token only in
// the header.
func TestPlexScansTheFolderInItsLibrary(t *testing.T) {
	var p fakePlex
	srv := p.server(t)
	var logs bytes.Buffer
	svc := notify.New(func() settings.Notifications { return settings.Notifications{Plex: plexCfg(srv.URL)} },
		slog.New(slog.NewTextHandler(&logs, nil)))
	for _, ev := range []notify.Event{
		{Kind: notify.KindImported, Path: "/media/movies/Dune (2021)/Dune (2021).mkv"},
		{Kind: notify.KindRenamed, Path: "/media/movies/remux/Heat (1995)/Heat (1995) - Remux.mkv"},
		{Kind: notify.KindDeleted, Path: "/media/tv-kids/Bluey"},
		{Kind: notify.KindImported, Path: "/media/tv/Severance"},
		// Not in any Plex library: nothing is scanned, and the log says why.
		{Kind: notify.KindImported, Path: "/media/other/Film (2000)/Film (2000).mkv"},
		// Problems and path-less events never reach Plex.
		{Kind: notify.KindImportFailed, Path: "/media/movies/x"},
		{Kind: notify.KindImported},
	} {
		svc.Notify(context.Background(), ev)
		svc.Wait()
	}
	want := []string{
		"1 /data/movies/Dune (2021)",
		"3 /data/movies/remux/Heat (1995)",
		"2 /data/tv-kids/Bluey",
		"2 /data/tv/Severance",
	}
	if strings.Join(p.scans, "|") != strings.Join(want, "|") {
		t.Errorf("scans:\n got %q\nwant %q", p.scans, want)
	}
	if p.inURL {
		t.Error("the token was sent in the address")
	}
	if !strings.Contains(logs.String(), "no Plex library holds /data/other/Film (2000)") {
		t.Errorf("log = %s", logs.String())
	}
}

func TestPlexTest(t *testing.T) {
	var p fakePlex
	srv := p.server(t)
	test := func(cfg settings.MediaServerNotify, folders ...string) (string, error) {
		svc := newSvc(settings.Notifications{})
		if folders != nil {
			svc.SetMediaFolders(func(context.Context) ([]string, error) { return folders, nil })
		}
		return svc.Test(context.Background(), "plex", settings.Notifications{Plex: cfg})
	}

	msg, err := test(plexCfg(srv.URL), "/media/movies", "/media/tv")
	if err != nil || msg != "Connected to Plex 1.41.2.9200-c6bbc1b53 (den): Movies, TV Shows, Remux." {
		t.Errorf("all held: %q, %v", msg, err)
	}
	// A folder no library holds is pointed out, but the test still passes:
	// a folder can be left out of Plex on purpose.
	msg, err = test(plexCfg(srv.URL), "/media/movies", "/media/anime")
	if err != nil || !strings.HasSuffix(msg, "No Plex library holds /data/anime, so Plex won't hear about changes there.") {
		t.Errorf("one missing: %q, %v", msg, err)
	}
	// None held: the paths are wrong.
	wrong := plexCfg(srv.URL)
	wrong.PathTo = "/mnt/media"
	if _, err := test(wrong, "/media/movies", "/media/tv"); err == nil || !strings.Contains(err.Error(), "none of its libraries holds /mnt/media/movies, /mnt/media/tv") {
		t.Errorf("none held: %v", err)
	}
	bad := plexCfg(srv.URL)
	bad.APIKey = "wrong"
	if _, err := test(bad); err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("bad token: %v", err)
	}
	if _, err := test(settings.MediaServerNotify{}); err == nil || !strings.Contains(err.Error(), "Plex's address") {
		t.Errorf("no address: %v", err)
	}
	if p.inURL {
		t.Error("the token was sent in the address")
	}

	var empty fakePlex
	empty.sections = `{"MediaContainer":{"size":0}}`
	if _, err := test(plexCfg(empty.server(t).URL)); err == nil || !strings.Contains(err.Error(), "no libraries yet") {
		t.Errorf("no libraries: %v", err)
	}

	// Something that isn't Plex at the address (Jellyfin answers / with its web page).
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>Jellyfin</html>"))
	}))
	defer other.Close()
	if _, err := test(plexCfg(other.URL)); err == nil || err.Error() != "that address answered, but it doesn't look like Plex" {
		t.Errorf("not Plex: %v", err)
	}
}
