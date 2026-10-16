package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/saltglass/reelkeeper/internal/api"
	"github.com/saltglass/reelkeeper/internal/auth"
	"github.com/saltglass/reelkeeper/internal/backup"
	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/events"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/indexer/catalog"
	"github.com/saltglass/reelkeeper/internal/jobs"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/metadata"
	"github.com/saltglass/reelkeeper/internal/migrate"
	"github.com/saltglass/reelkeeper/internal/naming"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/server"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// app is a running Reelkeeper (API only) on a test server.
type app struct {
	t         *testing.T
	srv       *httptest.Server
	settings  *settings.Service
	library   *library.Service
	jobs      *jobs.Manager
	history   *grab.History
	downloads *download.Service
	backups   *backup.Service
	tmdb      *httptest.Server
	base      string // URL base
	client    *http.Client
	dataDir   string
	// tracker is the made-up tracker the test indexers point at;
	// trackerDown makes it fail.
	tracker     *httptest.Server
	trackerDown *atomic.Bool
}

// fakeTMDB serves the recorded responses in testdata/tmdb, and a tiny
// "poster" for any image path.
func fakeTMDB(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var name string
		switch {
		case strings.HasPrefix(r.URL.Path, "/img/"):
			_, _ = w.Write([]byte("fake-jpeg"))
			return
		case r.URL.Path == "/configuration":
			_, _ = w.Write([]byte(`{}`))
			return
		case r.URL.Path == "/search/movie" && q.Get("query") == "28 Days Later":
			name = "search-28-days-later-2002.json"
		case r.URL.Path == "/search/movie" && q.Get("query") == "Dune":
			name = "search-dune.json"
		case r.URL.Path == "/movie/170":
			name = "movie-170.json"
		case r.URL.Path == "/movie/438631":
			name = "movie-438631.json"
		case r.URL.Path == "/search/movie" || (r.URL.Path == "/search/tv" && q.Get("query") != "Firefly"):
			_, _ = w.Write([]byte(`{"results":[]}`))
			return
		case r.URL.Path == "/search/tv":
			name = "search-tv-firefly.json"
		case r.URL.Path == "/find/362829" && q.Get("external_source") == "tvdb_id":
			name = "find-tvdb-362829.json"
		case r.URL.Path == "/tv/1437":
			name = "tv-1437.json"
		case strings.HasPrefix(r.URL.Path, "/tv/episode_group/"):
			name = "episode-group-" + strings.TrimPrefix(r.URL.Path, "/tv/episode_group/") + ".json"
			if _, err := os.Stat("../../testdata/tmdb/" + name); err != nil {
				http.Error(w, `{"status_code":34}`, http.StatusNotFound)
				return
			}
		default:
			http.Error(w, `{}`, http.StatusNotFound)
			return
		}
		b, err := os.ReadFile("../../testdata/tmdb/" + name)
		if err != nil {
			t.Error(err)
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newApp(t *testing.T, db *store.Store, urlBase string) *app {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	box, err := secrets.New("test-secret-key-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	st, err := settings.New(context.Background(), db, box, logger)
	if err != nil {
		t.Fatal(err)
	}
	a := auth.New(db, st, logger)
	tm := fakeTMDB(t)
	key := "0123456789abcdef0123456789abcdef"
	if _, err := st.SaveMetadata(context.Background(), settings.MetadataUpdate{TMDBAPIKey: &key, Language: "en-US"}); err != nil {
		t.Fatal(err)
	}
	client := metadata.New(func() string { return st.Metadata().TMDBAPIKey }, func() string { return "en-US" })
	client.SetBaseURL(tm.URL)
	lib := library.New(db, client, logger)
	lib.SetNaming(st.Naming)
	mgr := jobs.New(db, logger)
	lib.RegisterTasks(mgr)
	dataDir := t.TempDir()
	tracker, trackerDown := fakeTracker(t)
	writeTestDefinition(t, dataDir)
	cat, err := catalog.New(dataDir, logger)
	if err != nil {
		t.Fatal(err)
	}
	idx := indexer.New(db, box, cat, st.Solver, logger)
	hist := grab.NewHistory(db)
	gs := grab.New(lib, idx, st.Grabbing, hist, nil, logger)
	gs.RegisterTasks(mgr)
	// The download service talks to qBittorrent; tests that need it swap
	// in a fake client through app.downloads.NewClient.
	dl := download.NewService(db, st.DownloadClient, idx, lib, hist, logger)
	dl.SetSecrets(box)
	dl.SetMoves(st)
	dl.RegisterTasks(mgr)
	gs.SetDownloads(dl, dl)
	dl.SetWantedTitles(gs.WantedTitleIDs)
	// Backups go in the test's data folder. Restart is nil, so a restore is
	// recorded but nothing restarts.
	backups := backup.New(backup.Config{
		DB: db, Settings: st.Backups, DataDir: dataDir, App: "test", Logger: logger,
		KeyCheck: func(ctx context.Context) (string, error) { return settings.ReadKeyCheck(ctx, db) },
		KeyOpens: func(sealed string) bool { return settings.KeyOpens(box, sealed) },
	})
	h := server.New(server.Options{
		URLBase:  urlBase,
		Logger:   logger,
		Identify: a.Identify,
		API: api.Router(api.Deps{
			Store: db, Settings: st, Auth: a, Events: events.NewBroker(), Logger: logger,
			// Workers aren't started: queued runs stay queued, so tests are
			// deterministic.
			Jobs: mgr, Library: lib, Indexers: idx, Grab: gs, History: hist, Downloads: dl, Migrate: migrate.New(db, lib, hist, dl.AddToBlocklist), Backups: backups,
			Notify:      notify.New(st.Notifications, logger),
			TMDBBaseURL: tm.URL, ImageBaseURL: tm.URL + "/img",
			URLBase: urlBase, DataDir: dataDir, StartedAt: time.Now(),
		}),
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &app{
		t: t, srv: srv, settings: st, library: lib, jobs: mgr, history: hist, downloads: dl, backups: backups, tmdb: tm, base: urlBase, dataDir: dataDir, tracker: tracker, trackerDown: trackerDown,
		client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

type response struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func (a *app) do(method, path, body string, headers ...string) response {
	a.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, a.srv.URL+a.base+path, r)
	if err != nil {
		a.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := a.client.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := response{status: resp.StatusCode, header: resp.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (a *app) setup() {
	a.t.Helper()
	if r := a.do("POST", "/api/v1/auth/setup", `{"username":"dan","password":"a-good-password"}`); r.status != 204 {
		a.t.Fatalf("setup: %d %s", r.status, r.raw)
	}
}

// signOut forgets the browser's cookies.
func (a *app) signOut() {
	jar, _ := cookiejar.New(nil)
	a.client.Jar = jar
}

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	spec, err := openapi3.NewLoader().LoadFromFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

// The Go list of public endpoints must match the spec's `security: []`.
func TestPublicOperationsMatchSpec(t *testing.T) {
	spec := loadSpec(t)
	var inSpec []string
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			if op.Security != nil && len(*op.Security) == 0 {
				inSpec = append(inSpec, method+" "+path)
			}
		}
	}
	sort.Strings(inSpec)
	inGo := api.PublicOperationsForTest()
	if strings.Join(inSpec, "\n") != strings.Join(inGo, "\n") {
		t.Errorf("public endpoints differ.\nspec:\n  %s\ncode:\n  %s",
			strings.Join(inSpec, "\n  "), strings.Join(inGo, "\n  "))
	}
}

// Every protected endpoint in the spec refuses anonymous requests.
func TestEveryProtectedEndpointNeedsAuth(t *testing.T) {
	spec := loadSpec(t)
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	a.signOut()

	public := map[string]bool{}
	for _, p := range api.PublicOperationsForTest() {
		public[p] = true
	}
	checked := 0
	for path, item := range spec.Paths.Map() {
		for method := range item.Operations() {
			if public[method+" "+path] {
				continue
			}
			r := a.do(method, "/api/v1"+path, "{}")
			if r.status != http.StatusUnauthorized || r.body["code"] != "unauthenticated" {
				t.Errorf("%s %s without auth: %d %s", method, path, r.status, r.raw)
			}
			checked++
		}
	}
	if checked < 8 {
		t.Errorf("only %d endpoints checked; is the spec loading?", checked)
	}
}

func TestSetupLoginLogout(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")

		r := a.do("GET", "/api/v1/auth/state", "")
		if r.status != 200 || r.body["setupRequired"] != true || r.body["authenticated"] != false {
			t.Fatalf("initial state: %s", r.raw)
		}

		r = a.do("POST", "/api/v1/auth/setup", `{"username":"dan","password":"short"}`)
		if r.status != 422 || r.body["fields"].(map[string]any)["password"] == nil {
			t.Errorf("weak password: %d %s", r.status, r.raw)
		}
		a.setup()
		if r := a.do("POST", "/api/v1/auth/setup", `{"username":"eve","password":"another-password"}`); r.status != 409 {
			t.Errorf("second setup: %d", r.status)
		}

		r = a.do("GET", "/api/v1/auth/state", "")
		if r.body["authenticated"] != true || r.body["via"] != "session" || r.body["user"].(map[string]any)["username"] != "dan" {
			t.Errorf("after setup: %s", r.raw)
		}
		if r := a.do("GET", "/api/v1/settings/general", ""); r.status != 200 || r.body["instanceName"] != "Reelkeeper" {
			t.Errorf("settings: %d %s", r.status, r.raw)
		}

		if r := a.do("POST", "/api/v1/auth/logout", ""); r.status != 204 {
			t.Errorf("logout: %d", r.status)
		}
		if r := a.do("GET", "/api/v1/settings/general", ""); r.status != 401 {
			t.Errorf("after logout: %d", r.status)
		}

		if r := a.do("POST", "/api/v1/auth/login", `{"username":"dan","password":"nope-nope"}`); r.status != 401 || r.body["code"] != "invalid_credentials" {
			t.Errorf("wrong password: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", "/api/v1/auth/login", `{"username":"dan","password":"a-good-password"}`); r.status != 204 {
			t.Errorf("login: %d %s", r.status, r.raw)
		}
		if r := a.do("GET", "/api/v1/system/status", ""); r.status != 200 || r.body["database"].(map[string]any)["engine"] != string(db.Engine) {
			t.Errorf("status: %d %s", r.status, r.raw)
		}
	})
}

func TestLoginRateLimitedResponse(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	a.signOut()
	var r response
	for i := 0; i < 11; i++ {
		r = a.do("POST", "/api/v1/auth/login", `{"username":"dan","password":"wrong-password"}`)
	}
	if r.status != 429 || r.header.Get("Retry-After") == "" || r.body["code"] != "rate_limited" {
		t.Errorf("got %d %v %s", r.status, r.header.Get("Retry-After"), r.raw)
	}
}

func TestAPIKey(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		key := a.settings.Security().APIKey
		a.signOut()

		if r := a.do("GET", "/api/v1/system/status", "", "X-Api-Key", key); r.status != 200 {
			t.Errorf("header key: %d", r.status)
		}
		if r := a.do("GET", "/api/v1/system/status?apikey="+key, ""); r.status != 200 {
			t.Errorf("query key: %d", r.status)
		}
		r := a.do("GET", "/api/v1/system/status", "", "X-Api-Key", "wrong")
		if r.status != 401 || r.body["code"] != "invalid_api_key" {
			t.Errorf("wrong key: %d %s", r.status, r.raw)
		}

		// Regenerating invalidates the old key.
		r = a.do("POST", "/api/v1/settings/security/api-key", "", "X-Api-Key", key)
		newKey, _ := r.body["apiKey"].(string)
		if r.status != 200 || newKey == "" || newKey == key {
			t.Fatalf("regenerate: %d %s", r.status, r.raw)
		}
		if r := a.do("GET", "/api/v1/system/status", "", "X-Api-Key", key); r.status != 401 {
			t.Errorf("old key still works: %d", r.status)
		}
		if r := a.do("GET", "/api/v1/system/status", "", "X-Api-Key", newKey); r.status != 200 {
			t.Errorf("new key: %d", r.status)
		}
	})
}

// A website the user visits can't make the browser change Reelkeeper settings.
func TestCrossSiteRequestsRefused(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()

	r := a.do("PUT", "/api/v1/settings/general", `{"instanceName":"pwned","logLevel":"info"}`,
		"Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site")
	if r.status != http.StatusForbidden {
		t.Errorf("cross-site PUT: %d %s", r.status, r.raw)
	}
	if a.settings.General().InstanceName == "pwned" {
		t.Error("cross-site request changed settings")
	}
	r = a.do("PUT", "/api/v1/settings/general", `{"instanceName":"Home","logLevel":"info"}`,
		"Sec-Fetch-Site", "same-origin")
	if r.status != 200 {
		t.Errorf("same-origin PUT: %d %s", r.status, r.raw)
	}
}

func TestSecuritySettingsAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()

		r := a.do("GET", "/api/v1/settings/security", "")
		oidc := r.body["oidc"].(map[string]any)
		if r.status != 200 || r.body["apiKey"] == "" || oidc["clientSecretSet"] != false ||
			!strings.HasSuffix(oidc["redirectUrl"].(string), "/api/v1/auth/oidc/callback") {
			t.Fatalf("get: %d %s", r.status, r.raw)
		}

		r = a.do("PUT", "/api/v1/settings/security", `{
			"localLoginEnabled": false,
			"bypassNetworks": ["172.16.0.0/24"],
			"trustedProxies": ["172.16.0.2"],
			"sessionDays": 30,
			"oidc": {"enabled": false, "issuerUrl": "", "clientId": "", "scopes": [], "groupsClaim": "", "requiredGroup": "", "buttonLabel": ""}
		}`)
		fields, _ := r.body["fields"].(map[string]any)
		if r.status != 422 || fields["localLoginEnabled"] == nil || fields["bypassNetworks.0"] == nil {
			t.Errorf("invalid save: %d %s", r.status, r.raw)
		}

		r = a.do("PUT", "/api/v1/settings/security", `{
			"localLoginEnabled": true,
			"bypassNetworks": ["192.168.1.0/24"],
			"trustedProxies": ["172.16.0.2"],
			"sessionDays": 14,
			"oidc": {"enabled": true, "issuerUrl": "https://auth.example.com/application/o/reelkeeper/", "clientId": "reelkeeper",
			         "clientSecret": "s3cret", "scopes": ["openid","profile","email"], "groupsClaim": "groups",
			         "requiredGroup": "media", "buttonLabel": "Sign in with Authentik"}
		}`)
		if r.status != 200 {
			t.Fatalf("valid save: %d %s", r.status, r.raw)
		}
		if strings.Contains(r.raw, "s3cret") {
			t.Error("the client secret must never be returned")
		}
		if r.body["oidc"].(map[string]any)["clientSecretSet"] != true {
			t.Errorf("clientSecretSet should be true: %s", r.raw)
		}
		if a.settings.Security().OIDC.ClientSecret != "s3cret" {
			t.Error("secret not stored")
		}

		r = a.do("GET", "/api/v1/auth/state", "")
		if r.body["oidcEnabled"] != true || r.body["oidcButtonLabel"] != "Sign in with Authentik" {
			t.Errorf("auth state: %s", r.raw)
		}
	})
}

func TestBypassHasNoAccount(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	r := a.do("PUT", "/api/v1/settings/security", `{"localLoginEnabled":true,"bypassNetworks":["127.0.0.1"],"trustedProxies":[],"sessionDays":30,
		"oidc":{"enabled":false,"issuerUrl":"","clientId":"","scopes":[],"groupsClaim":"","requiredGroup":"","buttonLabel":""}}`)
	if r.status != 200 {
		t.Fatalf("save bypass: %d %s", r.status, r.raw)
	}
	a.signOut()

	r = a.do("GET", "/api/v1/auth/state", "")
	if r.body["authenticated"] != true || r.body["via"] != "bypass" {
		t.Errorf("bypass state: %s", r.raw)
	}
	r = a.do("PUT", "/api/v1/auth/password", `{"currentPassword":"a-good-password","newPassword":"something-else"}`)
	if r.status != 403 || r.body["code"] != "no_account" {
		t.Errorf("change password via bypass: %d %s", r.status, r.raw)
	}
}

func TestOIDCLoginWhenNotSetUp(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	if r := a.do("GET", "/api/v1/auth/oidc/login", ""); r.status != 404 || r.body["code"] != "sso_disabled" {
		t.Errorf("got %d %s", r.status, r.raw)
	}
	r := a.do("GET", "/api/v1/auth/oidc/callback?state=x&code=y", "")
	if loc := r.header.Get("Location"); r.status != 302 || !strings.HasPrefix(loc, "/login?") || !strings.Contains(loc, "error=sso_expired") {
		t.Errorf("callback without a sign-in in progress: %d %s", r.status, loc)
	}
	// The provider's own explanation is passed on to the login page.
	r = a.do("GET", "/api/v1/auth/oidc/callback?error=invalid_request&error_description=The+request+is+otherwise+malformed", "")
	loc := r.header.Get("Location")
	if r.status != 302 || !strings.Contains(loc, "error=sso_failed") || !strings.Contains(loc, "otherwise+malformed") {
		t.Errorf("provider refused: %d %s", r.status, loc)
	}
	r = a.do("GET", "/api/v1/auth/oidc/callback?error=access_denied", "")
	if r.status != 302 || !strings.Contains(r.header.Get("Location"), "error=sso_denied") {
		t.Errorf("provider denied: %d %s", r.status, r.header.Get("Location"))
	}
	// Linking needs a password session.
	if r := a.do("GET", "/api/v1/auth/oidc/link", ""); r.status != 401 {
		t.Errorf("link while signed out: %d %s", r.status, r.raw)
	}
}

func TestURLBase(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "/reelkeeper")
	r := a.do("POST", "/api/v1/auth/setup", `{"username":"dan","password":"a-good-password"}`)
	if r.status != 204 {
		t.Fatalf("setup under URL base: %d %s", r.status, r.raw)
	}
	cookie := r.header.Get("Set-Cookie")
	if !strings.Contains(cookie, "Path=/reelkeeper/") || !strings.Contains(cookie, "HttpOnly") {
		t.Errorf("cookie = %s", cookie)
	}
	if r := a.do("GET", "/api/v1/settings/general", ""); r.status != 200 {
		t.Errorf("settings under URL base: %d", r.status)
	}
	if r := a.do("GET", "/api/v1/nope", ""); r.status != 404 || r.body["code"] != "not_found" {
		t.Errorf("unknown endpoint: %d %s", r.status, r.raw)
	}
}

func TestTasksAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()

		r := a.do("GET", "/api/v1/system/tasks", "")
		if r.status != 200 || !strings.Contains(r.raw, `"kind":"jobs.prune"`) || !strings.Contains(r.raw, `"everySeconds":86400`) {
			t.Fatalf("list: %d %s", r.status, r.raw)
		}

		r = a.do("POST", "/api/v1/system/tasks/jobs.prune/run", "")
		job, _ := r.body["job"].(map[string]any)
		if r.status != 200 || r.body["alreadyActive"] != false || job["status"] != "queued" || job["name"] != "Tidy task history" {
			t.Fatalf("run: %d %s", r.status, r.raw)
		}
		r = a.do("POST", "/api/v1/system/tasks/jobs.prune/run", "")
		if r.body["alreadyActive"] != true {
			t.Errorf("second run should report the active one: %s", r.raw)
		}
		if r := a.do("POST", "/api/v1/system/tasks/no.such.task/run", ""); r.status != 404 {
			t.Errorf("unknown task: %d", r.status)
		}

		id := int64(job["id"].(float64))
		path := "/api/v1/system/jobs/" + strconv.FormatInt(id, 10) + "/cancel"
		if r := a.do("POST", path, ""); r.status != 204 {
			t.Errorf("cancel: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", path, ""); r.status != 409 {
			t.Errorf("cancel twice: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", "/api/v1/system/jobs/999999/cancel", ""); r.status != 404 {
			t.Errorf("cancel unknown: %d", r.status)
		}
		r = a.do("GET", "/api/v1/system/tasks", "")
		if !strings.Contains(r.raw, `"status":"cancelled"`) {
			t.Errorf("history should show the cancelled run: %s", r.raw)
		}
	})
}

func TestProfilesAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()

		r := a.do("GET", "/api/v1/profiles", "")
		if r.status != 200 || !strings.Contains(r.raw, `"mediaFolders":0`) || !strings.Contains(r.raw, `"movies":0`) {
			t.Fatalf("list: %d %s", r.status, r.raw)
		}
		r = a.do("POST", "/api/v1/profiles", `{"name":"4K","appliesTo":"movie"}`)
		if r.status != 200 || r.body["slug"] != "4k" || r.body["appliesTo"] != "movie" {
			t.Fatalf("add: %d %s", r.status, r.raw)
		}
		id := int64(r.body["id"].(float64))
		path := fmt.Sprintf("/api/v1/profiles/%d", id)

		r = a.do("POST", "/api/v1/profiles", `{"name":"hd","appliesTo":"both"}`)
		if r.status != 422 || r.body["fields"].(map[string]any)["name"] == nil {
			t.Errorf("duplicate name: %d %s", r.status, r.raw)
		}
		r = a.do("PUT", path, `{"name":"Ultra HD","appliesTo":"both"}`)
		if r.status != 200 || r.body["name"] != "Ultra HD" || r.body["slug"] != "4k" {
			t.Errorf("rename: %d %s", r.status, r.raw)
		}
		if r := a.do("PUT", "/api/v1/profiles/9999", `{"name":"x","appliesTo":"both"}`); r.status != 404 {
			t.Errorf("unknown: %d %s", r.status, r.raw)
		}

		// HD (id 1) is used by a media folder: narrowing and deleting are refused.
		dir := t.TempDir()
		if r := a.do("POST", "/api/v1/library/roots", `{"path":"`+dir+`","mediaType":"movie","defaultProfileId":1}`); r.status != 200 {
			t.Fatalf("add root: %d %s", r.status, r.raw)
		}
		r = a.do("DELETE", "/api/v1/profiles/1", "")
		if r.status != 422 || !strings.Contains(r.raw, "HD is used by 1 media folder") {
			t.Errorf("delete in use: %d %s", r.status, r.raw)
		}
		r = a.do("PUT", "/api/v1/profiles/1", `{"name":"HD","appliesTo":"series"}`)
		if r.status != 422 || r.body["fields"].(map[string]any)["appliesTo"] == nil {
			t.Errorf("narrow in use: %d %s", r.status, r.raw)
		}
		if r := a.do("GET", "/api/v1/profiles", ""); !strings.Contains(r.raw, `"mediaFolders":1`) {
			t.Errorf("usage: %s", r.raw)
		}

		if r := a.do("DELETE", path, ""); r.status != 204 {
			t.Errorf("delete: %d %s", r.status, r.raw)
		}
		if r := a.do("DELETE", path, ""); r.status != 404 {
			t.Errorf("delete twice: %d %s", r.status, r.raw)
		}
	})
}

func TestLibraryAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		a := newApp(t, db, "")
		a.setup()

		// A media folder with two movies: one clear, one ambiguous.
		dir := t.TempDir()
		for _, f := range []string{"28 Days Later (2002)/28.Days.Later.2002.1080p.BluRay.x264-GRP.mkv", "Dune/Dune.mkv"} {
			p := filepath.Join(dir, f)
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		if r := a.do("GET", "/api/v1/profiles", ""); r.status != 200 || !strings.Contains(r.raw, `"slug":"remux"`) {
			t.Fatalf("profiles: %d %s", r.status, r.raw)
		}
		r := a.do("POST", "/api/v1/library/roots", `{"path":"relative/path","mediaType":"movie","defaultProfileId":1}`)
		if r.status != 422 {
			t.Errorf("bad path: %d %s", r.status, r.raw)
		}
		r = a.do("POST", "/api/v1/library/roots", `{"path":"`+dir+`","mediaType":"movie","defaultProfileId":1}`)
		if r.status != 200 || r.body["manageFiles"] != false {
			t.Fatalf("add root: %d %s", r.status, r.raw)
		}
		rootID := int64(r.body["id"].(float64))
		// Adding a folder queues its first scan.
		if r := a.do("GET", "/api/v1/system/tasks", ""); !strings.Contains(r.raw, `"kind":"library.scan"`) || !strings.Contains(r.raw, `"status":"queued"`) {
			t.Errorf("a scan should be queued: %s", r.raw)
		}

		// Run the scan directly (the test app's workers aren't running).
		res, err := a.library.Scan(ctx, rootID, nil)
		if err != nil || res.Added != 1 || res.Review != 1 {
			t.Fatalf("scan = %+v, %v", res, err)
		}

		r = a.do("GET", "/api/v1/movies", "")
		var movies []map[string]any
		_ = json.Unmarshal([]byte(r.raw), &movies)
		if r.status != 200 || len(movies) != 1 || movies[0]["title"] != "28 Days Later" {
			t.Fatalf("movies: %d %s", r.status, r.raw)
		}
		// The list carries what the labels can show about the best file.
		if !strings.Contains(r.raw, `"releaseGroup":"GRP"`) || !strings.Contains(r.raw, `"videoCodec":`) {
			t.Errorf("label facts missing: %s", r.raw)
		}
		id := int64(movies[0]["id"].(float64))
		r = a.do("GET", "/api/v1/movies/"+strconv.FormatInt(id, 10), "")
		if r.status != 200 || !strings.Contains(r.raw, `"quality":"Bluray-1080p"`) || !strings.Contains(r.raw, "28.Days.Later.2002") {
			t.Errorf("movie detail: %d %s", r.status, r.raw)
		}

		r = a.do("GET", "/api/v1/library/review", "")
		var items []map[string]any
		_ = json.Unmarshal([]byte(r.raw), &items)
		if len(items) != 1 || items[0]["path"] != "Dune" || items[0]["reason"] == "" {
			t.Fatalf("review: %s", r.raw)
		}
		itemID := strconv.FormatInt(int64(items[0]["id"].(float64)), 10)
		r = a.do("POST", "/api/v1/library/review/"+itemID+"/resolve", `{"tmdbId":438631}`)
		if r.status != 200 || r.body["title"] != "Dune" {
			t.Errorf("resolve: %d %s", r.status, r.raw)
		}

		// Lookup marks movies already in the library.
		r = a.do("GET", "/api/v1/movies/lookup?q=Dune", "")
		if r.status != 200 || !strings.Contains(r.raw, `"inLibrary":true`) {
			t.Errorf("lookup: %d %s", r.status, r.raw)
		}

		// Root folder list includes counts.
		r = a.do("GET", "/api/v1/library/roots", "")
		if !strings.Contains(r.raw, `"titles":2`) || !strings.Contains(r.raw, `"review":0`) {
			t.Errorf("roots: %s", r.raw)
		}

		// Posters come through Reelkeeper and are cached on disk.
		r = a.do("GET", "/api/v1/images/tmdb/w342/abc123.jpg", "")
		if r.status != 200 || !strings.Contains(r.raw, "fake-jpeg") {
			t.Errorf("image: %d %q", r.status, r.raw)
		}
		if _, err := os.Stat(filepath.Join(a.dataDir, "cache", "tmdb", "w342", "abc123.jpg")); err != nil {
			t.Errorf("image should be cached: %v", err)
		}
		if r := a.do("GET", "/api/v1/images/tmdb/w342/..%2Fsecret.jpg", ""); r.status != 404 {
			t.Errorf("path tricks must be refused: %d", r.status)
		}

		// Removing the folder forgets the movies but not the files.
		if r := a.do("DELETE", "/api/v1/library/roots/"+strconv.FormatInt(rootID, 10), ""); r.status != 204 {
			t.Errorf("remove root: %d %s", r.status, r.raw)
		}
		if _, err := os.Stat(filepath.Join(dir, "Dune", "Dune.mkv")); err != nil {
			t.Error("files must stay")
		}
	})
}

func TestMetadataSettingsAPI(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	r := a.do("GET", "/api/v1/settings/metadata", "")
	if r.status != 200 || r.body["tmdbApiKeySet"] != true || strings.Contains(r.raw, "0123456789abcdef") {
		t.Errorf("get: %d %s", r.status, r.raw)
	}
	if r := a.do("POST", "/api/v1/settings/metadata/check", ""); r.body["ok"] != true {
		t.Errorf("check: %s", r.raw)
	}
	r = a.do("PUT", "/api/v1/settings/metadata", `{"tmdbApiKey":"bad","language":"en-US"}`)
	if r.status != 422 {
		t.Errorf("bad key: %d %s", r.status, r.raw)
	}
}

func TestLabelSettingsAPI(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	if r := a.do("GET", "/api/v1/settings/labels", ""); r.status != 200 || !strings.Contains(r.raw, `"parts":["profile","quality","hdr"]`) {
		t.Errorf("defaults: %d %s", r.status, r.raw)
	}
	if r := a.do("PUT", "/api/v1/settings/labels", `{"parts":["size","audio"]}`); r.status != 200 || !strings.Contains(r.raw, `"parts":["audio","size"]`) {
		t.Errorf("save: %d %s", r.status, r.raw)
	}
	if r := a.do("PUT", "/api/v1/settings/labels", `{"parts":["bitrate"]}`); r.status != 422 {
		t.Errorf("unknown part: %d %s", r.status, r.raw)
	}
}

// namingBody is a naming request: the given fields (JSON, without braces)
// override the default TV templates, so tests only spell out what they test.
func namingBody(fields string) string {
	d := naming.Defaults()
	tv := map[string]string{
		"seriesFolder": d.SeriesFolder, "seasonFolder": d.SeasonFolder, "episodeFile": d.EpisodeFile,
		"dailyEpisodeFile": d.DailyEpisodeFile, "multiEpisodeStyle": d.MultiEpisodeStyle,
	}
	var parts []string
	for k, v := range tv {
		if !strings.Contains(fields, `"`+k+`"`) {
			b, _ := json.Marshal(v)
			parts = append(parts, `"`+k+`":`+string(b))
		}
	}
	return "{" + fields + "," + strings.Join(parts, ",") + "}"
}

func TestNamingAndRenameAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		a := newApp(t, db, "")
		a.setup()

		r := a.do("GET", "/api/v1/settings/naming", "")
		if r.status != 200 || r.body["movieFolder"] != "{Movie Title} ({Release Year})" {
			t.Fatalf("defaults: %d %s", r.status, r.raw)
		}
		// The preview works on unsaved templates and explains problems.
		r = a.do("POST", "/api/v1/settings/naming/preview", namingBody(`"movieFolder":"{Movie CleanTitle} {tmdb-{TmdbId}}","movieFile":"{Movie Titel}"`))
		if r.status != 200 || !strings.Contains(r.raw, `"folder":"The Dark Knight {tmdb-155}"`) ||
			!strings.Contains(r.raw, `"movieFile":"Reelkeeper doesn't know {Movie Titel}`) {
			t.Errorf("preview: %d %s", r.status, r.raw)
		}
		if !strings.Contains(r.raw, `"episodeExamples":[{`) || !strings.Contains(r.raw, `"folder":"The Expanse (2015) {tvdb-280619}/Season 4"`) ||
			!strings.Contains(r.raw, `"folder":"The Expanse (2015) {tvdb-280619}/Specials"`) {
			t.Errorf("episode examples: %s", r.raw)
		}
		if r := a.do("POST", "/api/v1/settings/naming/preview", namingBody(`"movieFolder":"{Movie Title}","movieFile":"{Movie Title}","episodeFile":"{Episode Titel}"`)); !strings.Contains(r.raw, `"episodeFile":"Reelkeeper doesn't know`) {
			t.Errorf("episode template problem: %s", r.raw)
		}
		if r := a.do("PUT", "/api/v1/settings/naming", namingBody(`"movieFolder":"{Release Year}","movieFile":"{Movie Title}"`)); r.status != 422 {
			t.Errorf("bad template saved: %d %s", r.status, r.raw)
		}
		r = a.do("PUT", "/api/v1/settings/naming", namingBody(`"movieFolder":"{Movie Title} ({Release Year})","movieFile":"{Movie Title} ({Release Year}) {[Quality Full]}"`))
		if r.status != 200 {
			t.Fatalf("save: %d %s", r.status, r.raw)
		}

		dir := t.TempDir()
		const old = "28 Days Later (2002)/28.Days.Later.2002.1080p.BluRay.x264-GRP.mkv"
		if err := os.MkdirAll(filepath.Join(dir, "28 Days Later (2002)"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, old), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		r = a.do("POST", "/api/v1/library/roots", `{"path":"`+dir+`","mediaType":"movie","defaultProfileId":1}`)
		rootID := strconv.FormatInt(int64(r.body["id"].(float64)), 10)
		if _, err := a.library.Scan(ctx, int64(r.body["id"].(float64)), nil); err != nil {
			t.Fatal(err)
		}
		var movies []map[string]any
		_ = json.Unmarshal([]byte(a.do("GET", "/api/v1/movies", "").raw), &movies)
		id := strconv.FormatInt(int64(movies[0]["id"].(float64)), 10)

		const renamed = "28 Days Later (2002)/28 Days Later (2002) [Bluray-1080p].mkv"
		r = a.do("GET", "/api/v1/movies/"+id+"/rename", "")
		if r.status != 200 || !strings.Contains(r.raw, `"managed":false`) || !strings.Contains(r.raw, `"to":"`+renamed+`"`) {
			t.Errorf("plan: %d %s", r.status, r.raw)
		}
		a.do("PUT", "/api/v1/library/roots/"+rootID, `{"defaultProfileId":1,"manageFiles":true}`)
		r = a.do("POST", "/api/v1/movies/"+id+"/rename", "")
		if r.status != 200 || !strings.Contains(r.raw, `"done":true`) {
			t.Errorf("rename: %d %s", r.status, r.raw)
		}
		if _, err := os.Stat(filepath.Join(dir, renamed)); err != nil {
			t.Errorf("file not renamed: %v", err)
		}
		if r := a.do("GET", "/api/v1/movies/999999/rename", ""); r.status != 404 {
			t.Errorf("unknown movie: %d", r.status)
		}
	})
}

// get fetches a path and decodes the JSON into v.
func (a *app) get(path string, v any) response {
	a.t.Helper()
	r := a.do("GET", path, "")
	if r.status != 200 {
		a.t.Fatalf("GET %s: %d %s", path, r.status, r.raw)
	}
	if err := json.Unmarshal([]byte(r.raw), v); err != nil {
		a.t.Fatalf("GET %s: %v", path, err)
	}
	return r
}

func TestSeriesAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		a := newApp(t, db, "")
		a.setup()

		// A TV folder with Firefly's episode 14 as the intended order
		// numbers it (TMDB's aired order has only 11).
		dir := t.TempDir()
		files := []string{
			"Firefly (2002)/Season 01/Firefly (2002) - S01E14 - Objects in Space.mkv",
			"Mystery Show/Mystery.Show.S01E01.mkv", // TMDB knows nothing of it: goes to review
		}
		for _, f := range files {
			p := filepath.Join(dir, f)
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		r := a.do("POST", "/api/v1/library/roots", `{"path":"`+dir+`","mediaType":"series","defaultProfileId":1}`)
		if r.status != 200 {
			t.Fatalf("add TV root: %d %s", r.status, r.raw)
		}
		rootID := int64(r.body["id"].(float64))
		if _, err := a.library.Scan(ctx, rootID, nil); err != nil {
			t.Fatal(err)
		}

		var list []map[string]any
		a.get("/api/v1/series", &list)
		if len(list) != 1 || list[0]["title"] != "Firefly" {
			t.Fatalf("series list: %v", list)
		}
		if list[0]["seasons"].(float64) != 1 || list[0]["airedEpisodes"].(float64) != 11 || list[0]["problems"].(float64) != 1 {
			t.Errorf("counts: %v", list[0])
		}
		vs := list[0]["versions"].([]any)[0].(map[string]any)
		if _, ok := vs["episodesWithFiles"].(float64); !ok {
			t.Errorf("episodesWithFiles missing: %v", vs)
		}
		id := strconv.FormatInt(int64(list[0]["id"].(float64)), 10)
		base := "/api/v1/series/" + id

		// Detail: seasons with episodes, files linked, and a note on the
		// episode-14 file because TMDB's aired order has only 11.
		var d struct {
			Title      string `json:"title"`
			FirstAir   string `json:"firstAirDate"`
			EpisodeOrd string `json:"episodeOrder"`
			Problems   int    `json:"problems"`
			Unordered  []any  `json:"unordered"`
			SeasonList []struct {
				Number   int `json:"number"`
				Episodes []struct {
					Title   string  `json:"title"`
					Number  *int    `json:"number"`
					FileIds []int64 `json:"fileIds"`
				} `json:"episodes"`
			} `json:"seasonList"`
			Versions []struct {
				Id         int64 `json:"id"`
				MediaFiles []struct {
					Path      string `json:"path"`
					MatchNote string `json:"matchNote"`
				} `json:"mediaFiles"`
			} `json:"versions"`
		}
		a.get(base, &d)
		if d.Title != "Firefly" || d.FirstAir != "2002-09-20" || d.EpisodeOrd != "" || d.Unordered == nil {
			t.Errorf("detail: %+v", d)
		}
		for _, se := range d.SeasonList {
			for _, e := range se.Episodes {
				if e.FileIds == nil {
					t.Fatalf("fileIds must be an array, never null: %s", e.Title)
				}
			}
		}
		if len(d.SeasonList) < 2 || d.SeasonList[len(d.SeasonList)-1].Number != 0 {
			t.Errorf("seasons: %+v", d.SeasonList)
		}
		notes := 0
		for _, f := range d.Versions[0].MediaFiles {
			if strings.Contains(f.Path, "S01E14") && f.MatchNote != "" {
				notes++
			}
		}
		if notes != 1 || d.Problems != 1 {
			t.Errorf("match notes: %d, problems %d", notes, d.Problems)
		}
		versionID := strconv.FormatInt(d.Versions[0].Id, 10)

		// Orders: aired is current; the intended order fits both files.
		var orders []struct {
			Id       string `json:"id"`
			Name     string `json:"name"`
			Fits     int    `json:"fits"`
			Problems int    `json:"problems"`
			Current  bool   `json:"current"`
		}
		a.get(base+"/orders", &orders)
		const intended = "5acfb484c3a368738a004068"
		var sawIntended bool
		for _, o := range orders {
			if o.Id == "" && (!o.Current || o.Problems != 1) {
				t.Errorf("aired order: %+v", o)
			}
			if o.Id == intended {
				sawIntended = true
				if o.Current || o.Fits != 1 || o.Problems != 0 {
					t.Errorf("intended order: %+v", o)
				}
			}
		}
		if !sawIntended {
			t.Fatalf("intended order missing: %+v", orders)
		}

		// Switching orders re-links the files and clears the note.
		r = a.do("PUT", base+"/order", `{"episodeOrder":"`+intended+`"}`)
		if r.status != 200 || r.body["episodeOrder"] != intended || r.body["problems"].(float64) != 0 || strings.Contains(r.raw, `"matchNote"`) {
			t.Errorf("set order: %d %s", r.status, r.raw)
		}
		if r.body["episodeOrderName"] != "Intended Order" {
			t.Errorf("order name: %v", r.body["episodeOrderName"])
		}
		// "Objects in Space" is now episode 14 and holds the file.
		if !strings.Contains(r.raw, `"fileIds":[1],"id":`) || !strings.Contains(r.raw, `"number":14`) {
			t.Errorf("the file should sit on episode 14: %s", r.raw)
		}
		var withOrder []map[string]any
		a.get("/api/v1/series", &withOrder)
		if got := withOrder[0]["versions"].([]any)[0].(map[string]any)["episodesWithFiles"]; got != float64(1) {
			t.Errorf("episodesWithFiles = %v, want 1", got)
		}
		if r := a.do("PUT", base+"/order", `{"episodeOrder":"nonsense"}`); r.status != 422 {
			t.Errorf("unknown order: %d %s", r.status, r.raw)
		}
		if r := a.do("PUT", "/api/v1/series/999999/order", `{"episodeOrder":""}`); r.status != 404 {
			t.Errorf("order of unknown series: %d", r.status)
		}
		// And back to TMDB's own order.
		if r := a.do("PUT", base+"/order", `{"episodeOrder":""}`); r.status != 200 || r.body["problems"].(float64) != 1 {
			t.Errorf("back to aired: %d %s", r.status, r.raw)
		}

		// Series type.
		if r := a.do("PATCH", base, `{"seriesType":"daily"}`); r.status != 200 || r.body["seriesType"] != "daily" {
			t.Errorf("series type: %d %s", r.status, r.raw)
		}
		if r := a.do("PATCH", base, `{"seriesType":"weekly"}`); r.status != 400 && r.status != 422 {
			t.Errorf("bad series type: %d %s", r.status, r.raw)
		}
		a.do("PATCH", base, `{"seriesType":"standard"}`)

		// Rename plan: not managed, so only a plan; the file with a note is
		// listed with a problem and never renamed.
		r = a.do("GET", base+"/rename", "")
		if r.status != 200 || !strings.Contains(r.raw, `"managed":false`) || !strings.Contains(r.raw, `"problem":"Reelkeeper isn't sure`) {
			t.Errorf("rename plan: %d %s", r.status, r.raw)
		}
		// Movie endpoints don't accept a series, and series endpoints don't accept a movie.
		for _, p := range []string{"/movies/" + id, "/movies/" + id + "/rename"} {
			if r := a.do("GET", "/api/v1"+p, ""); r.status != 404 {
				t.Errorf("GET %s: %d", p, r.status)
			}
		}
		if r := a.do("POST", "/api/v1/movies/"+id+"/rename", ""); r.status != 404 {
			t.Errorf("movie rename of a series: %d", r.status)
		}
		if r := a.do("PATCH", "/api/v1/movies/"+id+"/versions/"+versionID, `{"monitored":false}`); r.status != 404 {
			t.Errorf("movie version patch of a series: %d", r.status)
		}
		if r := a.do("DELETE", "/api/v1/movies/"+id+"/versions/"+versionID, ""); r.status != 404 {
			t.Errorf("movie version delete of a series: %d", r.status)
		}
		if r := a.do("GET", "/api/v1/series/999999/rename", ""); r.status != 404 {
			t.Errorf("unknown series rename: %d", r.status)
		}
		if r := a.do("GET", "/api/v1/series/999999", ""); r.status != 404 {
			t.Errorf("unknown series: %d", r.status)
		}

		// Versions: monitoring changes, and one with files can't be removed.
		if r := a.do("PATCH", base+"/versions/"+versionID, `{"monitored":false}`); r.status != 204 {
			t.Errorf("unmonitor: %d %s", r.status, r.raw)
		}
		if r := a.do("PATCH", base+"/versions/999999", `{"monitored":false}`); r.status != 404 {
			t.Errorf("unknown version: %d", r.status)
		}
		if r := a.do("DELETE", base+"/versions/"+versionID, ""); r.status != 409 {
			t.Errorf("remove version with files: %d %s", r.status, r.raw)
		}

		// Lookup marks the series already in the library.
		var found []map[string]any
		a.get("/api/v1/series/lookup?q=Firefly", &found)
		if len(found) == 0 || found[0]["inLibrary"] != true || found[0]["seriesId"] == nil {
			t.Errorf("lookup: %v", found)
		}
		var none []any
		a.get("/api/v1/series/lookup?q=Nothing+Like+It", &none)
		if none == nil || len(none) != 0 {
			t.Errorf("empty lookup must be []: %v", none)
		}

		// Adding the same version again conflicts; a movie profile or a movie
		// folder is refused; a second profile adds a second version.
		add := func(profile int64, root int64) response {
			return a.do("POST", "/api/v1/series", fmt.Sprintf(`{"tmdbId":1437,"profileId":%d,"rootFolderId":%d}`, profile, root))
		}
		if r := add(1, rootID); r.status != 409 {
			t.Errorf("duplicate add: %d %s", r.status, r.raw)
		}
		if r := add(1, 999999); r.status != 422 {
			t.Errorf("add to unknown folder: %d %s", r.status, r.raw)
		}
		mr := a.do("POST", "/api/v1/library/roots", `{"path":"`+t.TempDir()+`","mediaType":"movie","defaultProfileId":1}`)
		if r := add(1, int64(mr.body["id"].(float64))); r.status != 422 {
			t.Errorf("add to a movie folder: %d %s", r.status, r.raw)
		}
		pr := a.do("POST", "/api/v1/profiles", `{"name":"TV 4K","appliesTo":"series"}`)
		if pr.status != 200 {
			t.Fatalf("profile: %d %s", pr.status, pr.raw)
		}
		profile2 := int64(pr.body["id"].(float64))
		r2 := a.do("POST", "/api/v1/library/roots", fmt.Sprintf(`{"path":%q,"mediaType":"series","defaultProfileId":%d}`, t.TempDir(), profile2))
		if r := add(profile2, int64(r2.body["id"].(float64))); r.status != 200 || len(r.body["versions"].([]any)) != 2 {
			t.Errorf("second version: %d %s", r.status, r.raw)
		}
		// The new version has no files, so it can be removed.
		var d2 struct {
			Versions []struct {
				Id    int64 `json:"id"`
				Files int   `json:"files"`
			} `json:"versions"`
		}
		a.get(base, &d2)
		for _, v := range d2.Versions {
			if v.Files == 0 {
				if r := a.do("DELETE", base+"/versions/"+strconv.FormatInt(v.Id, 10), ""); r.status != 204 {
					t.Errorf("remove empty version: %d %s", r.status, r.raw)
				}
			}
		}

		// Refresh asks TMDB again; the review list says what kind of folder.
		if r := a.do("POST", base+"/refresh", ""); r.status != 200 || r.body["title"] != "Firefly" {
			t.Errorf("refresh: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", "/api/v1/series/999999/refresh", ""); r.status != 404 {
			t.Errorf("refresh unknown: %d", r.status)
		}
		var review []map[string]any
		a.get("/api/v1/library/review", &review)
		if len(review) != 1 || review[0]["mediaType"] != "series" || review[0]["path"] != "Mystery Show" {
			t.Fatalf("review: %v", review)
		}
		// Resolving picks a TMDB series; the folder's files join that
		// series' version in the same media folder.
		item := strconv.FormatInt(int64(review[0]["id"].(float64)), 10)
		r = a.do("POST", "/api/v1/library/review/"+item+"/resolve", `{"tmdbId":1437}`)
		if r.status != 200 || r.body["title"] != "Firefly" || r.body["mediaType"] != "series" {
			t.Fatalf("resolve to a series: %d %s", r.status, r.raw)
		}
		resolved := strconv.FormatInt(int64(r.body["id"].(float64)), 10)
		if r := a.do("GET", "/api/v1/series/"+resolved, ""); !strings.Contains(r.raw, `"files":2`) {
			t.Errorf("after resolving: %s", r.raw)
		}
		if r := a.do("POST", "/api/v1/library/review/999999/resolve", `{"tmdbId":1437}`); r.status != 404 {
			t.Errorf("resolve unknown: %d", r.status)
		}
	})
}

// A made-up tracker definition (see internal/indexer's tests) with a
// password, a select and a checkbox. requestDelay is tiny so tests don't wait.
const testDefinition = `---
id: faketracker
name: Fake Tracker
type: private
links: [https://fake.example/]
requestDelay: 0.001
caps:
  categorymappings:
    - {id: 2, cat: TV/HD}
    - {id: 1, cat: Movies/HD}
    - {id: 3, cat: Movies/UHD}
settings:
  - {name: username, type: text, label: Username}
  - {name: password, type: password, label: Password}
  - {name: sort, type: select, label: Sort, default: added, options: {added: Added, size: Size}}
  - {name: freeleech, type: checkbox, label: Freeleech only}
  # Real definitions mark standard help texts by type, as 1337x does.
  - {name: info_flaresolverr, type: info_flaresolverr}
search:
  path: "browse/{{ .Keywords }}"
  rows: {selector: tr.t}
  fields:
    title: {selector: a}
    download: {selector: a, attribute: href}
    size: {selector: td.size}
    seeders: {text: 5}
    leechers: {text: 1}
    category: {text: 1}
`

func writeTestDefinition(t *testing.T, dataDir string) {
	t.Helper()
	dir := filepath.Join(dataDir, "definitions", "custom")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fake.yml"), []byte(testDefinition), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeTracker answers searches with one release; while down is set it fails.
func fakeTracker(t *testing.T) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	down := &atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `<table><tr class="t"><td><a href="/dl/1">%s 1080p</a></td><td class="size">1 GB</td></tr></table>`,
			strings.TrimPrefix(r.URL.Path, "/browse/"))
	}))
	t.Cleanup(srv.Close)
	return srv, down
}

// fakeSolver answers like TRAWL does on GET /.
func fakeSolver(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"msg":"TRAWL is ready!","version":"1"}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeProwlarr answers like Prowlarr: a tracker behind the solver proxy,
// Knaben, and something Reelkeeper can't run.
func fakeProwlarr(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "pk" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/indexer":
			fmt.Fprint(w, `[
{"id":1,"name":"Fake","implementation":"Cardigann","definitionName":"faketracker","enable":true,"priority":10,"protocol":"torrent","tags":[1],
 "fields":[{"name":"definitionFile","value":"faketracker"},{"name":"baseUrl","value":"https://fake.example/"},
  {"name":"username","value":"dan"},{"name":"password","value":"********","privacy":"password"}]},
{"id":2,"name":"Knaben","implementation":"Knaben","enable":true,"priority":25,"protocol":"torrent","tags":[1],"fields":[{"name":"baseUrl","value":"https://knaben.org/"}]},
{"id":4,"name":"Special","implementation":"PassThePopcorn","enable":true,"priority":25,"protocol":"torrent","tags":[],"fields":[]}
]`)
		case "/api/v1/indexerProxy":
			fmt.Fprint(w, `[{"implementation":"FlareSolverr","tags":[1],"fields":[{"name":"host","value":"http://trawl:8191/"},{"name":"requestTimeout","value":90}]}]`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestIndexersAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()

		// The catalogue has the made-up tracker; its detail drives the form.
		var defs map[string]any
		a.get("/api/v1/indexers/definitions", &defs)
		list := defs["definitions"].([]any)
		if len(list) != 1 || list[0].(map[string]any)["id"] != "faketracker" || list[0].(map[string]any)["custom"] != true || defs["count"].(float64) != 1 {
			t.Errorf("definitions: %v", defs)
		}
		var def struct {
			Login      string
			Categories []struct {
				ID   int
				Name string
			}
			Settings []struct {
				Name, Type, Default string
				Secret              bool
				Options             []struct{ Value, Label string }
			}
		}
		a.get("/api/v1/indexers/definitions/faketracker", &def)
		if def.Login != "" || len(def.Categories) != 3 || def.Categories[0].ID != 2040 || def.Categories[2].ID != 5040 {
			t.Errorf("definition: %+v", def)
		}
		if len(def.Settings) != 5 || !def.Settings[1].Secret || def.Settings[0].Secret ||
			def.Settings[3].Type != "checkbox" || def.Settings[3].Default != "false" ||
			def.Settings[2].Default != "added" || len(def.Settings[2].Options) != 2 ||
			def.Settings[2].Options[0].Value != "added" || def.Settings[2].Options[1].Label != "Size" ||
			// info_* types reach the UI as plain "info"; the name says which.
			def.Settings[4].Type != "info" || def.Settings[4].Name != "info_flaresolverr" {
			t.Errorf("settings: %+v", def.Settings)
		}
		if r := a.do("GET", "/api/v1/indexers/definitions/nope", ""); r.status != 404 {
			t.Errorf("unknown definition: %d", r.status)
		}

		// Nothing yet: an empty list, not null.
		if r := a.do("GET", "/api/v1/indexers", ""); r.status != 200 || strings.TrimSpace(r.raw) != "[]" {
			t.Errorf("empty list: %d %s", r.status, r.raw)
		}

		// Adding never echoes the password; secretsSet says it's stored.
		body := `{"name":"Fake","kind":"cardigann","definition":"faketracker","baseUrl":"` + a.tracker.URL + `/","enabled":true,` +
			`"settings":{"username":"dan","password":"hunter2","sort":"size"},"seedRatio":1.5,"seedTimeMinutes":2880}`
		r := a.do("POST", "/api/v1/indexers", body)
		if r.status != 200 || strings.Contains(r.raw, "hunter2") {
			t.Fatalf("add: %d %s", r.status, r.raw)
		}
		if r.body["definitionName"] != "Fake Tracker" || r.body["priority"].(float64) != 25 ||
			r.body["seedRatio"].(float64) != 1.5 || r.body["seedTimeMinutes"].(float64) != 2880 {
			t.Errorf("added: %s", r.raw)
		}
		if got := r.body["secretsSet"].([]any); len(got) != 1 || got[0] != "password" {
			t.Errorf("secretsSet = %v", got)
		}
		settingsOut := r.body["settings"].(map[string]any)
		if _, leaked := settingsOut["password"]; leaked || settingsOut["username"] != "dan" || settingsOut["sort"] != "size" {
			t.Errorf("settings = %v", settingsOut)
		}
		id := strconv.FormatInt(int64(r.body["id"].(float64)), 10)
		path := "/api/v1/indexers/" + id

		// Health is unknown until it's used, and ok after a test.
		var ix map[string]any
		a.get(path, &ix)
		if h := ix["health"].(map[string]any); h["status"] != "unknown" || h["message"] == "" {
			t.Errorf("health before test: %v", h)
		}
		r = a.do("POST", path+"/test", "")
		if r.status != 200 || r.body["ok"] != true || r.body["message"] == "" {
			t.Fatalf("test: %d %s", r.status, r.raw)
		}
		var all []map[string]any
		a.get("/api/v1/indexers", &all)
		if len(all) != 1 || all[0]["health"].(map[string]any)["status"] != "ok" ||
			all[0]["health"].(map[string]any)["message"] != "" || all[0]["stats"].(map[string]any)["queriesToday"].(float64) != 1 {
			t.Errorf("list after test: %v", all)
		}

		// Editing without the password keeps the stored one: the test still logs in with it.
		r = a.do("PUT", path, `{"name":"Fake 2","kind":"cardigann","definition":"faketracker","baseUrl":"`+a.tracker.URL+`/","enabled":true,`+
			`"priority":10,"settings":{"username":"dan2","sort":"added"}}`)
		if r.status != 200 || r.body["name"] != "Fake 2" || r.body["priority"].(float64) != 10 ||
			len(r.body["secretsSet"].([]any)) != 1 || r.body["settings"].(map[string]any)["username"] != "dan2" {
			t.Errorf("update: %d %s", r.status, r.raw)
		}

		// Testing unsaved input, with and without the saved indexer's id.
		unsaved := `{"name":"Another","kind":"cardigann","definition":"faketracker","baseUrl":"` + a.tracker.URL + `/","enabled":true,` +
			`"settings":{"username":"x","password":"y"}}`
		if r := a.do("POST", "/api/v1/indexers/test", unsaved); r.status != 200 || r.body["ok"] != true {
			t.Errorf("test input: %d %s", r.status, r.raw)
		}
		withID := strings.Replace(unsaved, `{"name"`, `{"id":`+id+`,"name"`, 1)
		if r := a.do("POST", "/api/v1/indexers/test", withID); r.status != 200 || r.body["ok"] != true {
			t.Errorf("test input with id: %d %s", r.status, r.raw)
		}
		if r := a.do("GET", "/api/v1/indexers", ""); strings.Contains(r.raw, "Another") {
			t.Errorf("testing must not save: %s", r.raw)
		}

		// A diagnostic search finds the release; a failing tracker is a 502.
		r = a.do("POST", path+"/search", `{"type":"search","query":"Dune"}`)
		if r.status != 200 {
			t.Fatalf("search: %d %s", r.status, r.raw)
		}
		var found []map[string]any
		if err := json.Unmarshal([]byte(r.raw), &found); err != nil || len(found) != 1 ||
			found[0]["title"] != "Dune 1080p" || found[0]["indexer"] != "Fake 2" || found[0]["size"].(float64) != 1<<30 {
			t.Errorf("releases: %s", r.raw)
		}
		a.trackerDown.Store(true)
		if r := a.do("POST", path+"/search", `{"query":"Dune"}`); r.status != 502 || r.body["message"] == "" {
			t.Errorf("failing search: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", path+"/test", ""); r.status != 200 || r.body["ok"] != false || r.body["message"] == "" {
			t.Errorf("failing test: %d %s", r.status, r.raw)
		}
		a.get(path, &ix)
		if ix["health"].(map[string]any)["status"] != "failing" {
			t.Errorf("health after failure: %v", ix["health"])
		}
		a.trackerDown.Store(false)

		// Bad input is 422 with the fields named; unknown ids are 404.
		if r := a.do("POST", "/api/v1/indexers", `{"name":"","kind":"cardigann","definition":"faketracker","enabled":true,"settings":{}}`); r.status != 422 ||
			r.body["fields"].(map[string]any)["name"] == nil {
			t.Errorf("bad add: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", "/api/v1/indexers/test", `{"name":"Bad","kind":"cardigann","definition":"faketracker","enabled":true,"priority":99,"settings":{}}`); r.status != 422 {
			t.Errorf("bad test input: %d %s", r.status, r.raw)
		}
		for _, c := range [][2]string{
			{"GET", "/api/v1/indexers/9999"}, {"PUT", "/api/v1/indexers/9999"}, {"DELETE", "/api/v1/indexers/9999"},
			{"POST", "/api/v1/indexers/9999/test"}, {"POST", "/api/v1/indexers/9999/search"},
		} {
			if r := a.do(c[0], c[1], `{"name":"x","kind":"knaben","enabled":true,"settings":{}}`); r.status != 404 {
				t.Errorf("%s %s: %d %s", c[0], c[1], r.status, r.raw)
			}
		}

		if r := a.do("DELETE", path, ""); r.status != 204 {
			t.Errorf("delete: %d %s", r.status, r.raw)
		}
		if r := a.do("GET", path, ""); r.status != 404 {
			t.Errorf("after delete: %d", r.status)
		}
	})
}

func TestSolverSettingsAPI(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	a := newApp(t, db, "")
	a.setup()
	if r := a.do("GET", "/api/v1/settings/solver", ""); r.status != 200 || r.body["url"] != "" || r.body["timeoutSeconds"].(float64) != 60 {
		t.Errorf("defaults: %d %s", r.status, r.raw)
	}
	// With no address anywhere there's nothing to check.
	if r := a.do("POST", "/api/v1/settings/solver/check", ""); r.status != 200 || r.body["ok"] != false || r.body["message"] != "No solver address is set." {
		t.Errorf("check with none: %d %s", r.status, r.raw)
	}
	sol := fakeSolver(t)
	if r := a.do("PUT", "/api/v1/settings/solver", `{"url":"`+sol.URL+`/","timeoutSeconds":90}`); r.status != 200 || r.body["url"] != sol.URL || r.body["timeoutSeconds"].(float64) != 90 {
		t.Errorf("save: %d %s", r.status, r.raw)
	}
	if r := a.do("GET", "/api/v1/settings/solver", ""); r.body["url"] != sol.URL {
		t.Errorf("reload: %s", r.raw)
	}
	if r := a.do("PUT", "/api/v1/settings/solver", `{"url":"trawl","timeoutSeconds":5}`); r.status != 422 {
		t.Errorf("bad save: %d %s", r.status, r.raw)
	}
	// The saved address, then an address given in the request.
	if r := a.do("POST", "/api/v1/settings/solver/check", "{}"); r.status != 200 || r.body["ok"] != true {
		t.Errorf("check saved: %d %s", r.status, r.raw)
	}
	if r := a.do("POST", "/api/v1/settings/solver/check", `{"url":"http://127.0.0.1:1"}`); r.status != 200 || r.body["ok"] != false || r.body["message"] == "" {
		t.Errorf("check unreachable: %d %s", r.status, r.raw)
	}
}

func TestProwlarrImportAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		p := fakeProwlarr(t)
		conn := `"url":"` + p.URL + `","apiKey":"pk"`

		r := a.do("POST", "/api/v1/indexers/import/prowlarr/preview", `{`+conn+`}`)
		if r.status != 200 || r.body["solverUrl"] != "http://trawl:8191" || r.body["solverTimeoutSeconds"].(float64) != 90 {
			t.Fatalf("preview: %d %s", r.status, r.raw)
		}
		cands := r.body["indexers"].([]any)
		if len(cands) != 3 {
			t.Fatalf("candidates: %s", r.raw)
		}
		fake, special := cands[0].(map[string]any), cands[2].(map[string]any)
		if fake["kind"] != "cardigann" || fake["definition"] != "faketracker" || fake["useSolver"] != true || fake["supported"] != true ||
			len(fake["missingSecrets"].([]any)) != 1 {
			t.Errorf("fake candidate: %v", fake)
		}
		if special["kind"] != "" || special["supported"] != false || special["reason"] == "" {
			t.Errorf("unsupported candidate: %v", special)
		}

		// A refused key and missing input are 422; an unreachable Prowlarr is 502.
		if r := a.do("POST", "/api/v1/indexers/import/prowlarr/preview", `{"url":"`+p.URL+`","apiKey":"nope"}`); r.status != 422 || r.body["fields"].(map[string]any)["apiKey"] == nil {
			t.Errorf("wrong key: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", "/api/v1/indexers/import/prowlarr/preview", `{"url":"http://127.0.0.1:1","apiKey":"pk"}`); r.status != 502 {
			t.Errorf("unreachable: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", "/api/v1/indexers/import/prowlarr/preview", `{"url":"","apiKey":""}`); r.status != 422 {
			t.Errorf("empty: %d %s", r.status, r.raw)
		}

		// Importing without the solver option leaves the solver alone.
		r = a.do("POST", "/api/v1/indexers/import/prowlarr", `{`+conn+`,"ids":[1,4],"useSolverSettings":false}`)
		if r.status != 200 || len(r.body["created"].([]any)) != 1 || r.body["created"].([]any)[0].(map[string]any)["name"] != "Fake" {
			t.Fatalf("import: %d %s", r.status, r.raw)
		}
		skipped := r.body["skipped"].([]any)
		if len(skipped) != 1 || skipped[0].(map[string]any)["prowlarrId"].(float64) != 4 {
			t.Errorf("skipped: %v", skipped)
		}
		if got := a.settings.Solver(); got.URL != "" {
			t.Errorf("solver changed: %+v", got)
		}

		// With it, the solver address and timeout follow Prowlarr's.
		r = a.do("POST", "/api/v1/indexers/import/prowlarr", `{`+conn+`,"ids":[1,2],"useSolverSettings":true}`)
		if r.status != 200 || len(r.body["created"].([]any)) != 1 || len(r.body["skipped"].([]any)) != 1 {
			t.Fatalf("second import: %d %s", r.status, r.raw)
		}
		if got := a.settings.Solver(); got.URL != "http://trawl:8191" || got.TimeoutSeconds != 90 {
			t.Errorf("solver = %+v", got)
		}
	})
}

func TestProfileSettingsAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()

		r := a.do("GET", "/api/v1/profiles/options", "")
		if r.status != 200 {
			t.Fatalf("options: %d %s", r.status, r.raw)
		}
		for _, k := range []string{"presets", "features", "tiers", "resolutions", "languages", "qualities", "choices"} {
			if l, _ := r.body[k].([]any); len(l) == 0 {
				t.Errorf("options.%s is empty: %s", k, r.raw)
			}
		}
		if langs := r.body["languages"].([]any); langs[0] != "English" {
			t.Errorf("English should come first, got %v", langs[0])
		}
		if tiers := r.body["tiers"].([]any); tiers[0].(map[string]any)["id"] != "hdtv" || tiers[len(tiers)-1].(map[string]any)["id"] != "remux" {
			t.Errorf("tiers should run worst to best: %v", tiers)
		}

		r = a.do("POST", "/api/v1/profiles", `{"name":"Four K","appliesTo":"movie","preset":"4k"}`)
		if r.status != 200 {
			t.Fatalf("add with preset: %d %s", r.status, r.raw)
		}
		id := int64(r.body["id"].(float64))
		base := fmt.Sprintf("/api/v1/profiles/%d", id)
		r = a.do("GET", base+"/settings", "")
		simple := r.body["simple"].(map[string]any)
		if r.status != 200 || r.body["mode"] != "simple" || fmt.Sprint(simple["resolutions"]) != "[2160]" {
			t.Fatalf("preset not applied: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", "/api/v1/profiles", `{"name":"Nope","appliesTo":"movie","preset":"bogus"}`); r.status != 422 || r.body["fields"].(map[string]any)["preset"] == nil {
			t.Errorf("unknown preset: %d %s", r.status, r.raw)
		}
		if r := a.do("GET", "/api/v1/profiles/9999/settings", ""); r.status != 404 {
			t.Errorf("unknown profile: %d", r.status)
		}

		// Simple: save, then a validation error.
		simple["resolutions"] = []int{1080}
		simple["upgradeUntil"] = map[string]any{"resolution": 1080, "source": "bluray"}
		simple["highestSource"] = "bluray"
		b, _ := json.Marshal(simple)
		r = a.do("PUT", base+"/settings/simple", string(b))
		if r.status != 200 || fmt.Sprint(r.body["simple"].(map[string]any)["resolutions"]) != "[1080]" {
			t.Fatalf("put simple: %d %s", r.status, r.raw)
		}
		simple["resolutions"] = []int{}
		b, _ = json.Marshal(simple)
		r = a.do("PUT", base+"/settings/simple", string(b))
		if r.status != 422 {
			t.Fatalf("empty resolutions: %d %s", r.status, r.raw)
		}
		for k := range r.body["fields"].(map[string]any) {
			if !strings.HasPrefix(k, "simple.") {
				t.Errorf("field %q should start with simple.", k)
			}
		}

		// Test a release: accepted, then a 720p one rejected.
		r = a.do("POST", base+"/test-release", `{"name":"Some.Movie.2021.1080p.BluRay.x264-GRP"}`)
		if r.status != 200 || r.body["accepted"] != true {
			t.Fatalf("accepted: %d %s", r.status, r.raw)
		}
		parsed := r.body["parsed"].(map[string]any)
		if parsed["title"] != "Some Movie" || parsed["group"] != "GRP" || parsed["resolution"] != float64(1080) {
			t.Errorf("parsed: %s", r.raw)
		}
		r = a.do("POST", base+"/test-release", `{"name":"Some.Movie.2021.720p.BluRay.x264-GRP"}`)
		if r.status != 200 || r.body["accepted"] != false || !strings.Contains(r.raw, "720p") {
			t.Errorf("rejected: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", base+"/test-release", `{"name":"  "}`); r.status != 422 {
			t.Errorf("blank name: %d %s", r.status, r.raw)
		}

		// Advanced: switch, edit, an invalid save, and back.
		r = a.do("POST", base+"/settings/switch-to-advanced", "")
		if r.status != 200 || r.body["mode"] != "advanced" {
			t.Fatalf("switch: %d %s", r.status, r.raw)
		}
		compiled := r.body["compiled"].(map[string]any)
		compiled["minScore"] = -5
		b, _ = json.Marshal(compiled)
		r = a.do("PUT", base+"/settings/advanced", string(b))
		if r.status != 200 || r.body["mode"] != "advanced" || r.body["compiled"].(map[string]any)["minScore"] != float64(-5) {
			t.Fatalf("put advanced: %d %s", r.status, r.raw)
		}
		compiled["upgradesAllowed"] = true
		compiled["upgradeUntil"] = "No Such Quality"
		b, _ = json.Marshal(compiled)
		r = a.do("PUT", base+"/settings/advanced", string(b))
		if r.status != 422 || r.body["fields"].(map[string]any)["advanced"] == nil {
			t.Errorf("invalid advanced: %d %s", r.status, r.raw)
		}
		simple["resolutions"] = []int{1080}
		b, _ = json.Marshal(simple)
		r = a.do("PUT", base+"/settings/simple", string(b))
		if r.status != 200 || r.body["mode"] != "simple" {
			t.Errorf("back to simple: %d %s", r.status, r.raw)
		}
	})
}

func TestDeleteMovieAndSkippedFoldersAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		a := newApp(t, db, "")
		a.setup()

		// A media folder Reelkeeper manages, with a movie and a subtitle file.
		managed := t.TempDir()
		for _, f := range []string{
			filepath.Join(managed, "28 Days Later (2002)", "28.Days.Later.2002.1080p.BluRay.x264-GRP.mkv"),
			filepath.Join(managed, "28 Days Later (2002)", "28.Days.Later.2002.1080p.BluRay.x264-GRP.en.srt"),
		} {
			if err := os.MkdirAll(filepath.Dir(f), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		addRoot := func(dir string, manage bool) int64 {
			r := a.do("POST", "/api/v1/library/roots", `{"path":"`+dir+`","mediaType":"movie","defaultProfileId":1}`)
			if r.status != 200 {
				t.Fatalf("add root: %d %s", r.status, r.raw)
			}
			id := int64(r.body["id"].(float64))
			if manage {
				body := `{"defaultProfileId":1,"manageFiles":true}`
				if r := a.do("PUT", "/api/v1/library/roots/"+strconv.FormatInt(id, 10), body); r.status != 200 {
					t.Fatalf("manage: %d %s", r.status, r.raw)
				}
			}
			if _, err := a.library.Scan(ctx, id, nil); err != nil {
				t.Fatal(err)
			}
			return id
		}
		movieIDs := func() []string {
			var movies []struct {
				ID int64 `json:"id"`
			}
			r := a.do("GET", "/api/v1/movies", "")
			_ = json.Unmarshal([]byte(r.raw), &movies)
			var out []string
			for _, m := range movies {
				out = append(out, strconv.FormatInt(m.ID, 10))
			}
			return out
		}
		managedRoot := addRoot(managed, true)

		if r := a.do("DELETE", "/api/v1/movies/9999", ""); r.status != 404 {
			t.Errorf("unknown movie: %d %s", r.status, r.raw)
		}

		// Keep the files: the movie goes, the folder is skipped, the disk is untouched.
		ids := movieIDs()
		if len(ids) != 1 {
			t.Fatalf("want one movie, got %v", ids)
		}
		id := ids[0]
		r := a.do("DELETE", "/api/v1/movies/"+id, "")
		if r.status != 200 || !strings.Contains(r.raw, `"deleted":[]`) || !strings.Contains(r.raw, "28 Days Later (2002)") {
			t.Fatalf("delete keeping files: %d %s", r.status, r.raw)
		}
		if r := a.do("GET", "/api/v1/movies/"+id, ""); r.status != 404 {
			t.Errorf("movie should be gone: %d", r.status)
		}
		if _, err := os.Stat(filepath.Join(managed, "28 Days Later (2002)")); err != nil {
			t.Errorf("files must stay: %v", err)
		}
		r = a.do("GET", "/api/v1/skipped-folders", "")
		var skipped []map[string]any
		_ = json.Unmarshal([]byte(r.raw), &skipped)
		if r.status != 200 || len(skipped) != 1 || skipped[0]["title"] != "28 Days Later (2002)" ||
			skipped[0]["rootPath"] != managed || skipped[0]["path"] != "28 Days Later (2002)" {
			t.Fatalf("skipped: %d %s", r.status, r.raw)
		}
		// A scan leaves the skipped folder alone.
		if _, err := a.library.Scan(ctx, managedRoot, nil); err != nil {
			t.Fatal(err)
		}
		if got := movieIDs(); len(got) != 0 {
			t.Errorf("skipped folder was added back: %v", got)
		}
		// Stop skipping, and the next scan finds it again.
		sid := strconv.FormatInt(int64(skipped[0]["id"].(float64)), 10)
		if r := a.do("DELETE", "/api/v1/skipped-folders/"+sid, ""); r.status != 204 {
			t.Errorf("unskip: %d %s", r.status, r.raw)
		}
		if r := a.do("DELETE", "/api/v1/skipped-folders/"+sid, ""); r.status != 404 {
			t.Errorf("unskip twice: %d", r.status)
		}
		if r := a.do("GET", "/api/v1/skipped-folders", ""); strings.TrimSpace(r.raw) != "[]" {
			t.Errorf("list should be empty: %s", r.raw)
		}
		if _, err := a.library.Scan(ctx, managedRoot, nil); err != nil {
			t.Fatal(err)
		}

		// Once Reelkeeper only reads the folder, deleting files is refused and nothing changes.
		off := `{"defaultProfileId":1,"manageFiles":false}`
		if r := a.do("PUT", "/api/v1/library/roots/"+strconv.FormatInt(managedRoot, 10), off); r.status != 200 {
			t.Fatalf("unmanage: %d %s", r.status, r.raw)
		}
		ids = movieIDs()
		if len(ids) != 1 {
			t.Fatalf("want the movie back, got %v", ids)
		}
		id = ids[0]
		r = a.do("DELETE", "/api/v1/movies/"+id+"?deleteFiles=true", "")
		if r.status != 422 || !strings.Contains(r.raw, managed) {
			t.Fatalf("unmanaged folder: %d %s", r.status, r.raw)
		}
		if r := a.do("GET", "/api/v1/movies/"+id, ""); r.status != 200 {
			t.Errorf("movie must still exist: %d", r.status)
		}
		if _, err := os.Stat(filepath.Join(managed, "28 Days Later (2002)", "28.Days.Later.2002.1080p.BluRay.x264-GRP.mkv")); err != nil {
			t.Errorf("nothing may be deleted: %v", err)
		}
	})
}

func TestDeleteMovieWithFilesAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		a := newApp(t, db, "")
		a.setup()
		dir := t.TempDir()
		folder := filepath.Join(dir, "28 Days Later (2002)")
		if err := os.MkdirAll(folder, 0o750); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"28.Days.Later.2002.1080p.BluRay.x264-GRP.mkv", "28.Days.Later.2002.1080p.BluRay.x264-GRP.en.srt"} {
			if err := os.WriteFile(filepath.Join(folder, f), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		r := a.do("POST", "/api/v1/library/roots", `{"path":"`+dir+`","mediaType":"movie","defaultProfileId":1}`)
		rootID := int64(r.body["id"].(float64))
		if r := a.do("PUT", "/api/v1/library/roots/"+strconv.FormatInt(rootID, 10), `{"defaultProfileId":1,"manageFiles":true}`); r.status != 200 {
			t.Fatalf("manage: %d %s", r.status, r.raw)
		}
		if _, err := a.library.Scan(ctx, rootID, nil); err != nil {
			t.Fatal(err)
		}
		var movies []struct {
			ID int64 `json:"id"`
		}
		r = a.do("GET", "/api/v1/movies", "")
		_ = json.Unmarshal([]byte(r.raw), &movies)
		if len(movies) != 1 {
			t.Fatalf("movies: %s", r.raw)
		}
		r = a.do("DELETE", "/api/v1/movies/"+strconv.FormatInt(movies[0].ID, 10)+"?deleteFiles=true", "")
		if r.status != 200 || !strings.Contains(r.raw, `"skipped":[]`) || !strings.Contains(r.raw, ".mkv") {
			t.Fatalf("delete with files: %d %s", r.status, r.raw)
		}
		if _, err := os.Stat(folder); !os.IsNotExist(err) {
			t.Errorf("the movie's folder should be gone: %v", err)
		}
		if r := a.do("GET", "/api/v1/skipped-folders", ""); strings.TrimSpace(r.raw) != "[]" {
			t.Errorf("nothing should be skipped: %s", r.raw)
		}
	})
}

// Routine runs that found nothing to do are hidden from System → Tasks
// unless ?routine=true, and the response says how many were left out.
func TestTasksAPIHidesRoutineRuns(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		now := time.Now().UTC()
		for _, quiet := range []bool{false, true, true} {
			j := &jobs.Job{Kind: "jobs.prune", Args: "{}", Status: jobs.Succeeded, Trigger: jobs.Schedule,
				Message: "Removed 0 old runs", Quiet: quiet, CreatedAt: now}
			if _, err := db.DB.NewInsert().Model(j).Exec(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		count := func(r response) (listed int, hidden float64) {
			recent, _ := r.body["recent"].([]any)
			hidden, _ = r.body["hiddenRoutine"].(float64)
			return len(recent), hidden
		}
		// Ids rise, so the two newest runs are quiet and the oldest is not.
		r := a.do("GET", "/api/v1/system/tasks", "")
		if listed, hidden := count(r); r.status != 200 || listed != 1 || hidden != 2 {
			t.Errorf("default: listed %d, hidden %v: %s", listed, hidden, r.raw)
		}
		r = a.do("GET", "/api/v1/system/tasks?routine=true", "")
		if listed, hidden := count(r); r.status != 200 || listed != 3 || hidden != 0 {
			t.Errorf("routine=true: listed %d, hidden %v: %s", listed, hidden, r.raw)
		}
		if !strings.Contains(r.raw, `"quiet":true`) || !strings.Contains(r.raw, `"quiet":false`) {
			t.Errorf("jobs should report quiet: %s", r.raw)
		}
	})
}
