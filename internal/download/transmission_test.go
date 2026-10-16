package download

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/torrent"
)

// fakeTransmission mimics the parts of Transmission's RPC that Reelkeeper uses,
// including its rules: basic auth when a login is set, and a session id that
// every request must carry (a request without the right one gets 409 and the
// id to use). Its answers are shaped like Transmission 4's.
type fakeTransmission struct {
	mu  sync.Mutex
	srv *httptest.Server
	// path is where the fake serves RPC (/transmission/rpc by default).
	path string
	// user and pass are the login; empty user means RPC login is off.
	user, pass string
	session    string
	// version answers session-get.
	version    string
	rpcVersion int

	torrents   []map[string]any
	calls      []rpcCall         // every request that got past the session check
	handshakes int               // how many 409s were sent
	failures   map[string]string // method -> the "result" to answer instead of "success"
	// sessionExtra are more answers for session-get (the folders).
	sessionExtra map[string]any
	// lateDuplicate is a torrent that appears when it is added: someone
	// else added it a moment after Reelkeeper looked.
	lateDuplicate map[string]any
}

type rpcCall struct {
	Method string
	Args   map[string]any
}

func newFakeTransmission(t *testing.T) *fakeTransmission {
	t.Helper()
	f := &fakeTransmission{
		path: "/transmission/rpc", user: "admin", pass: "secret", session: "sess-1",
		version: "4.0.6 (38c164933e)", rpcVersion: 17,
		failures: map[string]string{},
	}
	f.torrents = []map[string]any{{
		"hashString": strings.Repeat("f", 40), "name": "Someone else's", "labels": []string{"radarr"},
		"status": 6, "percentDone": 1.0,
	}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// client is a Reelkeeper client for the fake, logged in as it expects.
func (f *fakeTransmission) client() *Transmission {
	return &Transmission{URL: f.srv.URL, Username: "admin", Password: "secret"}
}

// add puts a torrent in the fake, with its labels.
func (f *fakeTransmission) add(hash string, labels ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.torrents = append(f.torrents, map[string]any{"hashString": hash, "name": "n-" + hash[:4], "labels": labels, "status": 4, "percentDone": 0.5})
}

func (f *fakeTransmission) labelsOf(hash string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.torrents {
		if t["hashString"] == hash {
			return t["labels"].([]string)
		}
	}
	return nil
}

// called lists the methods asked for, in order.
func (f *fakeTransmission) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.Method)
	}
	return out
}

// calledWith returns the arguments of every call to a method.
func (f *fakeTransmission) calledWith(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c.Args)
		}
	}
	return out
}

func (f *fakeTransmission) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != f.path || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if f.user != "" {
		u, p, ok := r.BasicAuth()
		if !ok || u != f.user || p != f.pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="Transmission"`)
			http.Error(w, "Unauthorized User", http.StatusUnauthorized)
			return
		}
	}
	if r.Header.Get("X-Transmission-Session-Id") != f.session {
		f.handshakes++
		w.Header().Set("X-Transmission-Session-Id", f.session)
		http.Error(w, "409: Conflict", http.StatusConflict)
		return
	}
	var req struct {
		Method    string         `json:"method"`
		Arguments map[string]any `json:"arguments"`
	}
	b, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(b, &req); err != nil {
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	f.calls = append(f.calls, rpcCall{req.Method, req.Arguments})
	if res, ok := f.failures[req.Method]; ok {
		reply(w, res, nil)
		return
	}
	switch req.Method {
	case "session-get":
		out := map[string]any{"version": f.version, "rpc-version": f.rpcVersion}
		for k, v := range f.sessionExtra {
			out[k] = v
		}
		reply(w, "success", out)
	case "torrent-get":
		reply(w, "success", map[string]any{"torrents": f.get(req.Arguments)})
	case "torrent-add":
		reply(w, "success", f.addTorrent(req.Arguments))
	case "torrent-set":
		f.set(req.Arguments)
		reply(w, "success", nil)
	case "torrent-remove":
		ids := stringsOf(req.Arguments["ids"])
		f.torrents = slices.DeleteFunc(f.torrents, func(t map[string]any) bool { return slices.Contains(ids, t["hashString"].(string)) })
		reply(w, "success", nil)
	case "torrent-start", "torrent-stop", "queue-move-top", "torrent-verify", "torrent-set-location":
		reply(w, "success", nil)
	default:
		reply(w, "method name not recognized", nil)
	}
}

func reply(w http.ResponseWriter, result string, args map[string]any) {
	out := map[string]any{"result": result}
	if args != nil {
		out["arguments"] = args
	}
	_ = json.NewEncoder(w).Encode(out)
}

func stringsOf(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// get answers torrent-get: the torrents asked for (all when there is no ids),
// with only the fields asked for, as Transmission does.
func (f *fakeTransmission) get(args map[string]any) []map[string]any {
	fields := stringsOf(args["fields"])
	var ids []string
	if v, ok := args["ids"]; ok {
		ids = stringsOf(v)
	}
	out := []map[string]any{}
	for _, t := range f.torrents {
		if ids != nil && !slices.Contains(ids, t["hashString"].(string)) {
			continue
		}
		row := map[string]any{}
		for _, name := range fields {
			if v, ok := t[name]; ok {
				row[name] = v
			}
		}
		out = append(out, row)
	}
	return out
}

func (f *fakeTransmission) addTorrent(args map[string]any) map[string]any {
	if f.lateDuplicate != nil {
		f.torrents = append(f.torrents, f.lateDuplicate)
		dup := f.lateDuplicate
		f.lateDuplicate = nil
		return map[string]any{"torrent-duplicate": map[string]any{"hashString": dup["hashString"], "name": dup["name"]}}
	}
	hash := strings.Repeat("a", 40)
	if m, ok := args["filename"].(string); ok {
		h, _ := MagnetHash(m)
		hash = h
	}
	if mi, ok := args["metainfo"].(string); ok {
		raw, _ := base64.StdEncoding.DecodeString(mi)
		hash, _ = InfoHash(raw)
	}
	for _, t := range f.torrents {
		if t["hashString"] == hash {
			return map[string]any{"torrent-duplicate": map[string]any{"hashString": hash, "name": t["name"]}}
		}
	}
	f.torrents = append(f.torrents, map[string]any{"hashString": hash, "name": "new", "labels": stringsOf(args["labels"]), "status": 4})
	return map[string]any{"torrent-added": map[string]any{"hashString": strings.ToUpper(hash), "name": "new"}}
}

func (f *fakeTransmission) set(args map[string]any) {
	ids := stringsOf(args["ids"])
	for _, t := range f.torrents {
		if slices.Contains(ids, t["hashString"].(string)) {
			if l, ok := args["labels"]; ok {
				t["labels"] = stringsOf(l)
			}
		}
	}
}

const (
	tmA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tmB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tmC = "cccccccccccccccccccccccccccccccccccccccc"
)

func TestTransmissionSessionHandshakeAndLogin(t *testing.T) {
	ctx := context.Background()
	f := newFakeTransmission(t)
	tr := f.client()

	v, err := tr.Version(ctx)
	if err != nil || v != "4.0.6 (38c164933e)" {
		t.Fatalf("Version = %q, %v", v, err)
	}
	// The first request got a 409 with the session id and was sent again with it.
	if f.handshakes != 1 {
		t.Errorf("handshakes = %d, want 1", f.handshakes)
	}
	// The id is remembered: the next calls don't get a 409.
	if _, err := tr.Version(ctx); err != nil {
		t.Fatal(err)
	}
	if f.handshakes != 1 {
		t.Errorf("handshakes after a second call = %d, want 1", f.handshakes)
	}
	// Transmission changes its session id now and then: one more round, once.
	f.mu.Lock()
	f.session = "sess-2"
	f.mu.Unlock()
	if _, err := tr.Version(ctx); err != nil {
		t.Fatal(err)
	}
	if f.handshakes != 2 {
		t.Errorf("handshakes after the id changed = %d, want 2", f.handshakes)
	}

	// Wrong password: ErrLogin, which names Transmission.
	bad := &Transmission{URL: f.srv.URL, Username: "admin", Password: "nope"}
	_, err = bad.Version(ctx)
	if !errors.Is(err, ErrLogin) || err.Error() != "Transmission refused the username or password" {
		t.Errorf("wrong password: %v", err)
	}
	// No username at all against a Transmission that wants one.
	if _, err := (&Transmission{URL: f.srv.URL}).Version(ctx); !errors.Is(err, ErrLogin) {
		t.Errorf("no login: %v", err)
	}
}

func TestTransmissionSendsBasicAuthOnlyWhenAUsernameIsSet(t *testing.T) {
	ctx := context.Background()
	f := newFakeTransmission(t)
	f.user = "" // RPC login turned off
	var sawAuth bool
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, sawAuth = r.BasicAuth()
		f.serve(w, r)
	}))
	defer probe.Close()
	if _, err := (&Transmission{URL: probe.URL}).Version(ctx); err != nil {
		t.Fatal(err)
	}
	if sawAuth {
		t.Error("sent a login although none is set")
	}
}

func TestTransmissionEndpoint(t *testing.T) {
	for url, want := range map[string]string{
		"http://192.168.1.20:9091":                      "http://192.168.1.20:9091/transmission/rpc",
		"http://192.168.1.20:9091/":                     "http://192.168.1.20:9091/transmission/rpc",
		"http://192.168.1.20:9091/transmission/rpc":     "http://192.168.1.20:9091/transmission/rpc",
		"http://192.168.1.20:9091/transmission/rpc/":    "http://192.168.1.20:9091/transmission/rpc",
		"https://box.example/tm/transmission/rpc":       "https://box.example/tm/transmission/rpc",
		"https://box.example/transmission-behind-proxy": "https://box.example/transmission-behind-proxy",
	} {
		if got := (&Transmission{URL: url}).endpoint(); got != want {
			t.Errorf("%s: endpoint = %s, want %s", url, got, want)
		}
	}
	// A custom path is used as given.
	f := newFakeTransmission(t)
	f.path = "/custom/rpc"
	tr := &Transmission{URL: f.srv.URL + "/custom/rpc", Username: "admin", Password: "secret"}
	if _, err := tr.Version(context.Background()); err != nil {
		t.Errorf("custom path: %v", err)
	}
}

func TestTransmissionVersionGate(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		version string
		rpc     int
		err     string // empty: accepted
	}{
		{"3.00 (bb6b5a062e)", 16, "Transmission 3.00 is too old: Reelkeeper needs 4.0 or newer, for labels"},
		{"2.94 (d8e60ee44f)", 15, "Transmission 2.94 is too old: Reelkeeper needs 4.0 or newer, for labels"},
		{"4.0.0 (123abc)", 17, ""},
		{"4.0.6 (38c164933e)", 17, ""},
		{"4.1.3 (838877323f)", 19, ""},
	} {
		f := newFakeTransmission(t)
		f.version, f.rpcVersion = c.version, c.rpc
		v, err := f.client().Version(ctx)
		switch {
		case c.err == "" && (err != nil || v != c.version):
			t.Errorf("%s: %q, %v", c.version, v, err)
		case c.err != "" && (err == nil || err.Error() != c.err):
			t.Errorf("%s: error = %v, want %q", c.version, err, c.err)
		}
	}
}

func TestTransmissionErrors(t *testing.T) {
	ctx := context.Background()
	f := newFakeTransmission(t)
	f.add(tmA, "reelkeeper-movies")
	tr := f.client()

	// Transmission's own words for what went wrong, in a sentence naming it.
	f.failures["torrent-remove"] = "duplicate or corrupt torrent"
	err := tr.Remove(ctx, "reelkeeper-movies", []string{tmA}, true)
	if err == nil || err.Error() != "Transmission couldn't remove the torrent: duplicate or corrupt torrent" {
		t.Errorf("failed call: %v", err)
	}

	// Not Transmission's RPC address at all.
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>hello</html>") }))
	defer web.Close()
	if _, err := (&Transmission{URL: web.URL}).Version(ctx); err == nil || !strings.Contains(err.Error(), "Transmission's answer didn't make sense") {
		t.Errorf("not RPC: %v", err)
	}

	// The RPC whitelist.
	deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusForbidden) }))
	defer deny.Close()
	if _, err := (&Transmission{URL: deny.URL}).Version(ctx); err == nil || !strings.Contains(err.Error(), "rpc-whitelist") {
		t.Errorf("whitelist: %v", err)
	}

	// A server that never accepts the session id doesn't loop forever.
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Transmission-Session-Id", "again")
		w.WriteHeader(http.StatusConflict)
	}))
	defer loop.Close()
	if _, err := (&Transmission{URL: loop.URL}).Version(ctx); err == nil {
		t.Error("endless 409s: no error")
	}

	// Nothing listening.
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	if _, err := (&Transmission{URL: gone.URL}).Version(ctx); err == nil || !strings.Contains(err.Error(), "can't reach Transmission") {
		t.Errorf("unreachable: %v", err)
	}
}

func TestTransmissionTorrentsMapping(t *testing.T) {
	ctx := context.Background()
	f := newFakeTransmission(t)
	f.torrents = append(f.torrents,
		map[string]any{
			"hashString": strings.ToUpper(tmA), "name": "Film.2024.1080p", "labels": []string{"reelkeeper-movies", "reelkeeper", "indexer"},
			"status": 4, "error": 0, "errorString": "", "isStalled": false, "metadataPercentComplete": 1.0, "percentDone": 0.25,
			"sizeWhenDone": 4000, "leftUntilDone": 3000, "rateDownload": 1234, "eta": 90, "downloadDir": "/mnt/downloads/reelkeeper",
			"uploadRatio": 0.5, "secondsSeeding": 7200, "addedDate": 1700000000, "doneDate": 0,
			"peersSendingToUs": 3, "peersConnected": 8, "haveValid": 1000, "uploadedEver": 2000,
			"trackerStats": []map[string]any{{"seederCount": 12}, {"seederCount": 40}, {"seederCount": -1}},
		},
		map[string]any{
			"hashString": tmB, "name": "Done.Film", "labels": []string{"reelkeeper-movies"},
			"status": 6, "metadataPercentComplete": 1.0, "percentDone": 1.0, "sizeWhenDone": 500, "leftUntilDone": 0,
			"eta": -1, "downloadDir": "/mnt/downloads/reelkeeper", "uploadRatio": -1, "secondsSeeding": 0,
			"addedDate": 1700000100, "doneDate": 1700000500, "trackerStats": []map[string]any{{"seederCount": -1}},
		},
		map[string]any{"hashString": tmC, "name": "A show", "labels": []string{"reelkeeper-tv"}, "status": 4, "metadataPercentComplete": 1.0},
	)
	ts, err := f.client().Torrents(ctx, "reelkeeper-movies")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 {
		t.Fatalf("listed %d torrents, want 2 (the foreign and the TV one are left out): %+v", len(ts), ts)
	}
	got := ts[0]
	want := Torrent{
		Hash: tmA, Name: "Film.2024.1080p", Category: "reelkeeper-movies", State: torrent.Downloading,
		Progress: 0.25, Size: 4000, Left: 3000, Speed: 1234, ETA: 90 * time.Second,
		SavePath: "/mnt/downloads/reelkeeper", ContentPath: "/mnt/downloads/reelkeeper/Film.2024.1080p",
		Ratio: 0.5, SeedingTime: 2 * time.Hour, AddedOn: time.Unix(1700000000, 0).UTC(),
		Seeds: 3, SwarmSeeds: 40, Peers: 8,
		// What a move needs: bytes verified, bytes uploaded, and the labels
		// besides the category.
		Verified: 1000, Uploaded: 2000, Tags: []string{"reelkeeper", "indexer"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mapping:\n got %+v\nwant %+v", got, want)
	}
	done := ts[1]
	if done.ETA != 0 || done.Ratio != 0 || done.SwarmSeeds != 0 || done.CompletedOn == nil || !done.CompletedOn.Equal(time.Unix(1700000500, 0)) || !done.Done() {
		t.Errorf("finished torrent with unknown values: %+v", done)
	}
	// The instant its last piece is in, Transmission still says "downloading"
	// with everything downloaded: not finished until it changes to seeding.
	justAboutDone := rawTorrent{HashString: tmA, Status: 4, MetadataPercentComplete: 1, PercentDone: 1, SizeWhenDone: 100}.torrent("c")
	if justAboutDone.Done() || justAboutDone.Progress < 0.9999 {
		t.Errorf("a torrent still downloading counts as finished: %+v", justAboutDone)
	}
	if seeding := (rawTorrent{Status: 6, PercentDone: 1}).torrent("c"); !seeding.Done() {
		t.Errorf("a seeding torrent isn't finished: %+v", seeding)
	}
	if stopped := (rawTorrent{Status: 0, PercentDone: 1}).torrent("c"); !stopped.Done() {
		t.Errorf("a stopped finished torrent isn't finished: %+v", stopped)
	}
	if got.CompletedOn != nil {
		t.Errorf("unfinished torrent has a completion time: %v", got.CompletedOn)
	}
	// Only the fields it uses are asked for.
	args := f.calledWith("torrent-get")[0]
	if _, hasIDs := args["ids"]; hasIDs {
		t.Error("listing sent ids")
	}
	asked := stringsOf(args["fields"])
	for _, need := range []string{"hashString", "labels", "status", "percentDone", "trackerStats", "isStalled"} {
		if !slices.Contains(asked, need) {
			t.Errorf("didn't ask for %s", need)
		}
	}
	if slices.Contains(asked, "files") || slices.Contains(asked, "peers") {
		t.Errorf("asked for more than it uses: %v", asked)
	}
	if _, err := f.client().Torrents(ctx, ""); err == nil {
		t.Error("an empty category listed everything")
	}
}

func TestTransmissionState(t *testing.T) {
	// Each row is one Transmission torrent. The "like" column is the
	// qBittorrent state it stands for.
	for _, c := range []struct {
		name string
		raw  rawTorrent
		want torrent.State
		like string
	}{
		{"downloading", rawTorrent{Status: 4, MetadataPercentComplete: 1, PercentDone: 0.3}, torrent.Downloading, "downloading"},
		{"downloading, no activity yet flagged", rawTorrent{Status: 4, MetadataPercentComplete: 1, IsStalled: false}, torrent.Downloading, "downloading"},
		{"stalled download", rawTorrent{Status: 4, MetadataPercentComplete: 1, IsStalled: true, PercentDone: 0.3}, torrent.Stalled, "stalledDL"},
		{"getting details", rawTorrent{Status: 4, MetadataPercentComplete: 0.4}, torrent.Metadata, "metaDL"},
		{"getting details, flagged stalled", rawTorrent{Status: 4, MetadataPercentComplete: 0, IsStalled: true}, torrent.Metadata, "metaDL"},
		{"waiting to download", rawTorrent{Status: 3, MetadataPercentComplete: 1}, torrent.Queued, "queuedDL"},
		{"waiting to check", rawTorrent{Status: 1}, torrent.Checking, "checkingResumeData"},
		{"checking", rawTorrent{Status: 2, PercentDone: 0.5}, torrent.Checking, "checkingDL"},
		{"checking a finished torrent", rawTorrent{Status: 2, PercentDone: 1}, torrent.Checking, "checkingUP"},
		{"seeding", rawTorrent{Status: 6, PercentDone: 1}, torrent.Seeding, "uploading"},
		{"seeding, nobody wants it", rawTorrent{Status: 6, PercentDone: 1, IsStalled: true}, torrent.SeedingStalled, "stalledUP"},
		{"waiting to seed", rawTorrent{Status: 5, PercentDone: 1}, torrent.Seeding, "queuedUP"},
		{"stopped before finishing", rawTorrent{Status: 0, PercentDone: 0.4}, torrent.Paused, "stoppedDL"},
		{"stopped, nothing downloaded", rawTorrent{Status: 0}, torrent.Paused, "stoppedDL"},
		{"stopped after finishing", rawTorrent{Status: 0, PercentDone: 1}, torrent.SeedingPaused, "stoppedUP"},
		{"local error", rawTorrent{Status: 0, Error: 3, ErrorString: "Permission denied", PercentDone: 0.2}, torrent.Error, "error"},
		{"local error while seeding", rawTorrent{Status: 6, Error: 3, ErrorString: "No space left on device", PercentDone: 1}, torrent.Error, "error"},
		{"files gone", rawTorrent{Status: 0, Error: 3, ErrorString: "No data found! Ensure your drives are connected or use \"Set Location\". To re-download, remove the torrent and re-add it.", PercentDone: 1}, torrent.MissingFiles, "missingFiles"},
		{"files gone, other case", rawTorrent{Status: 0, Error: 3, ErrorString: "NO DATA FOUND"}, torrent.MissingFiles, "missingFiles"},
		// A tracker's complaint doesn't stop the torrent working.
		{"tracker warning", rawTorrent{Status: 4, MetadataPercentComplete: 1, Error: 1, ErrorString: "Tracker warning"}, torrent.Downloading, "downloading"},
		{"tracker error", rawTorrent{Status: 6, PercentDone: 1, Error: 2, ErrorString: "Unregistered torrent"}, torrent.Seeding, "uploading"},
		{"tracker error, stalled", rawTorrent{Status: 4, MetadataPercentComplete: 1, IsStalled: true, Error: 2, ErrorString: "Unregistered torrent"}, torrent.Stalled, "stalledDL"},
		{"a status from the future", rawTorrent{Status: 9}, torrent.Unknown, "unknown"},
	} {
		if got := transmissionState(c.raw); got != c.want {
			t.Errorf("%s: %q, want %q (qBittorrent's %s)", c.name, got, c.want, c.like)
		}
		// The qBittorrent state it is said to stand for translates to the same.
		if like := qbitState(c.like); like != c.want {
			t.Errorf("%s: qBittorrent's %s is %q, not %q", c.name, c.like, like, c.want)
		}
	}
}

func TestTransmissionAdd(t *testing.T) {
	ctx := context.Background()
	magnet := "magnet:?xt=urn:btih:" + tmA + "&dn=film"

	t.Run("a magnet link", func(t *testing.T) {
		f := newFakeTransmission(t)
		h, err := f.client().Add(ctx, Add{Magnet: magnet, Category: "reelkeeper-movies", Tags: []string{"reelkeeper", "My, Indexer", "reelkeeper", " "}, Stopped: true, SavePath: "/mnt/downloads/reelkeeper"})
		if err != nil || h != tmA {
			t.Fatalf("Add = %q, %v", h, err)
		}
		args := f.calledWith("torrent-add")
		if len(args) != 1 {
			t.Fatalf("torrent-add called %d times", len(args))
		}
		want := map[string]any{
			"filename": magnet, "paused": true, "download-dir": "/mnt/downloads/reelkeeper",
			"labels": []any{"reelkeeper-movies", "reelkeeper", "My  Indexer"},
		}
		if !reflect.DeepEqual(args[0], want) {
			t.Errorf("torrent-add arguments:\n got %v\nwant %v", args[0], want)
		}
		if got := f.labelsOf(tmA); !reflect.DeepEqual(got, []string{"reelkeeper-movies", "reelkeeper", "My  Indexer"}) {
			t.Errorf("labels = %v", got)
		}
	})

	t.Run("a torrent file", func(t *testing.T) {
		f := newFakeTransmission(t)
		file := testTorrentFile(t, "film.mkv", []byte("some film data"))
		wantHash, _ := InfoHash(file)
		h, err := f.client().Add(ctx, Add{Torrent: file, Category: "reelkeeper-tv"})
		if err != nil || h != wantHash {
			t.Fatalf("Add = %q, %v (want %s)", h, err, wantHash)
		}
		args := f.calledWith("torrent-add")[0]
		if args["metainfo"] != base64.StdEncoding.EncodeToString(file) || args["paused"] != false {
			t.Errorf("arguments: %v", args)
		}
		if _, has := args["filename"]; has {
			t.Error("sent both a file and a link")
		}
		if _, has := args["download-dir"]; has {
			t.Error("sent a folder although none was set")
		}
	})

	t.Run("the folder given to EnsureCategory is used when the add has none", func(t *testing.T) {
		f := newFakeTransmission(t)
		tr := f.client()
		if err := tr.EnsureCategory(ctx, "reelkeeper-movies", "/data/movies"); err != nil {
			t.Fatal(err)
		}
		// A second call doesn't replace it (an existing qBittorrent category is left as it is).
		if err := tr.EnsureCategory(ctx, "reelkeeper-movies", "/elsewhere"); err != nil {
			t.Fatal(err)
		}
		if _, err := tr.Add(ctx, Add{Magnet: magnet, Category: "reelkeeper-movies"}); err != nil {
			t.Fatal(err)
		}
		if got := f.calledWith("torrent-add")[0]["download-dir"]; got != "/data/movies" {
			t.Errorf("download-dir = %v", got)
		}
		// The add's own folder wins.
		f2 := newFakeTransmission(t)
		tr2 := f2.client()
		_ = tr2.EnsureCategory(ctx, "reelkeeper-movies", "/data/movies")
		if _, err := tr2.Add(ctx, Add{Magnet: magnet, Category: "reelkeeper-movies", SavePath: "/mine"}); err != nil {
			t.Fatal(err)
		}
		if got := f2.calledWith("torrent-add")[0]["download-dir"]; got != "/mine" {
			t.Errorf("download-dir = %v", got)
		}
		// EnsureCategory itself makes no call.
		if got := f.called(); slices.Contains(got, "session-get") || len(got) != 2 {
			t.Errorf("calls = %v", got)
		}
	})

	t.Run("already there with the label counts as added", func(t *testing.T) {
		f := newFakeTransmission(t)
		f.add(tmA, "reelkeeper-movies")
		h, err := f.client().Add(ctx, Add{Magnet: magnet, Category: "reelkeeper-movies", Tags: []string{"x"}})
		if err != nil || h != tmA {
			t.Fatalf("Add = %q, %v", h, err)
		}
		if len(f.calledWith("torrent-add")) != 0 {
			t.Error("added a torrent Transmission already has")
		}
	})

	t.Run("already there without the label is left alone", func(t *testing.T) {
		f := newFakeTransmission(t)
		f.add(tmA, "radarr")
		_, err := f.client().Add(ctx, Add{Magnet: magnet, Category: "reelkeeper-movies"})
		if err == nil || !strings.Contains(err.Error(), "Transmission already has this torrent") {
			t.Fatalf("error = %v", err)
		}
		if len(f.calledWith("torrent-add")) != 0 || len(f.calledWith("torrent-set")) != 0 {
			t.Errorf("touched it: %v", f.called())
		}
		if got := f.labelsOf(tmA); !reflect.DeepEqual(got, []string{"radarr"}) {
			t.Errorf("labels = %v", got)
		}
	})

	t.Run("another app adds it in between", func(t *testing.T) {
		// Transmission answers torrent-duplicate for one Reelkeeper didn't see.
		f := newFakeTransmission(t)
		f.lateDuplicate = map[string]any{"hashString": tmA, "name": "x", "labels": []string{"sonarr"}}
		if _, err := f.client().Add(ctx, Add{Magnet: magnet, Category: "reelkeeper-movies"}); err == nil {
			t.Error("took over another app's torrent")
		}
		f = newFakeTransmission(t)
		f.lateDuplicate = map[string]any{"hashString": tmA, "name": "x", "labels": []string{"reelkeeper-movies"}}
		if h, err := f.client().Add(ctx, Add{Magnet: magnet, Category: "reelkeeper-movies"}); err != nil || h != tmA {
			t.Errorf("a duplicate that is ours: %q, %v", h, err)
		}
	})

	t.Run("nothing to add, or a bad link", func(t *testing.T) {
		f := newFakeTransmission(t)
		if _, err := f.client().Add(ctx, Add{Category: "reelkeeper-movies"}); err == nil {
			t.Error("added nothing")
		}
		if _, err := f.client().Add(ctx, Add{Magnet: "not a link", Category: "reelkeeper-movies"}); err == nil {
			t.Error("added a bad link")
		}
		if len(f.called()) != 0 {
			t.Errorf("called Transmission: %v", f.called())
		}
	})

	t.Run("Transmission refuses it", func(t *testing.T) {
		f := newFakeTransmission(t)
		f.failures["torrent-add"] = "invalid or corrupt torrent file"
		_, err := f.client().Add(ctx, Add{Magnet: magnet, Category: "reelkeeper-movies"})
		if err == nil || err.Error() != "Transmission couldn't add the torrent: invalid or corrupt torrent file" {
			t.Errorf("error = %v", err)
		}
	})
}

func TestTransmissionCalls(t *testing.T) {
	ctx := context.Background()
	const cat = "reelkeeper-movies"
	newOne := func(t *testing.T) (*fakeTransmission, *Transmission) {
		f := newFakeTransmission(t)
		f.add(tmA, cat, "reelkeeper", "indexer")
		f.add(tmB, cat)
		return f, f.client()
	}
	ids := func(f *fakeTransmission, method string) any {
		c := f.calledWith(method)
		if len(c) == 0 {
			return nil
		}
		return c[len(c)-1]["ids"]
	}

	t.Run("Stop and Start", func(t *testing.T) {
		f, tr := newOne(t)
		if err := tr.Stop(ctx, cat, []string{tmA, tmB}); err != nil {
			t.Fatal(err)
		}
		if err := tr.Start(ctx, cat, []string{tmA}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ids(f, "torrent-stop"), []any{tmA, tmB}) || !reflect.DeepEqual(ids(f, "torrent-start"), []any{tmA}) {
			t.Errorf("calls: %v", f.calls)
		}
	})

	t.Run("TopPriority", func(t *testing.T) {
		f, tr := newOne(t)
		if err := tr.TopPriority(ctx, cat, []string{tmB}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ids(f, "queue-move-top"), []any{tmB}) {
			t.Errorf("calls: %v", f.calls)
		}
	})

	t.Run("NoShareLimits sets unlimited, then starts", func(t *testing.T) {
		f, tr := newOne(t)
		if err := tr.NoShareLimits(ctx, cat, []string{tmA, tmB}); err != nil {
			t.Fatal(err)
		}
		set := f.calledWith("torrent-set")
		if len(set) != 1 || !reflect.DeepEqual(set[0], map[string]any{"ids": []any{tmA, tmB}, "seedRatioMode": float64(2), "seedIdleMode": float64(2)}) {
			t.Errorf("torrent-set: %v", set)
		}
		calls := f.called()
		if i, j := slices.Index(calls, "torrent-set"), slices.Index(calls, "torrent-start"); i < 0 || j < i {
			t.Errorf("order: %v (limits are lifted first, then the torrents start)", calls)
		}
		if !reflect.DeepEqual(ids(f, "torrent-start"), []any{tmA, tmB}) {
			t.Errorf("start ids: %v", ids(f, "torrent-start"))
		}
	})

	t.Run("Remove", func(t *testing.T) {
		f, tr := newOne(t)
		if err := tr.Remove(ctx, cat, []string{tmA}, true); err != nil {
			t.Fatal(err)
		}
		if err := tr.Remove(ctx, cat, []string{tmB}, false); err != nil {
			t.Fatal(err)
		}
		rm := f.calledWith("torrent-remove")
		if len(rm) != 2 || rm[0]["delete-local-data"] != true || rm[1]["delete-local-data"] != false ||
			!reflect.DeepEqual(rm[0]["ids"], []any{tmA}) {
			t.Errorf("torrent-remove: %v", rm)
		}
	})

	t.Run("MoveCategory keeps the other labels", func(t *testing.T) {
		f, tr := newOne(t)
		if err := tr.MoveCategory(ctx, cat, "reelkeeper-tv", []string{tmA, tmB}); err != nil {
			t.Fatal(err)
		}
		if got := f.labelsOf(tmA); !reflect.DeepEqual(got, []string{"reelkeeper-tv", "reelkeeper", "indexer"}) {
			t.Errorf("A's labels = %v", got)
		}
		if got := f.labelsOf(tmB); !reflect.DeepEqual(got, []string{"reelkeeper-tv"}) {
			t.Errorf("B's labels = %v", got)
		}
		// Moved to a label it already has: no repeat.
		f.add(tmC, cat, "reelkeeper-tv")
		if err := tr.MoveCategory(ctx, cat, "reelkeeper-tv", []string{tmC}); err != nil {
			t.Fatal(err)
		}
		if got := f.labelsOf(tmC); !reflect.DeepEqual(got, []string{"reelkeeper-tv"}) {
			t.Errorf("C's labels = %v", got)
		}
	})

	t.Run("no hashes, no calls", func(t *testing.T) {
		f, tr := newOne(t)
		for _, err := range []error{
			tr.Stop(ctx, cat, nil), tr.Start(ctx, cat, nil), tr.TopPriority(ctx, cat, nil), tr.NoShareLimits(ctx, cat, nil),
			tr.Remove(ctx, cat, nil, true), tr.MoveCategory(ctx, cat, "x", nil),
		} {
			if err != nil {
				t.Error(err)
			}
		}
		if len(f.called()) != 0 {
			t.Errorf("called Transmission: %v", f.called())
		}
	})

	t.Run("TrackerMessages", func(t *testing.T) {
		f, tr := newOne(t)
		f.mu.Lock()
		f.torrents[2]["trackerStats"] = []map[string]any{
			{"lastAnnounceResult": "Success", "lastScrapeResult": "Success"},
			{"lastAnnounceResult": "Unregistered torrent", "lastScrapeResult": ""},
			{"lastAnnounceResult": "", "lastScrapeResult": "Could not connect to tracker"},
		}
		f.mu.Unlock()
		msgs, err := tr.TrackerMessages(ctx, cat, tmA)
		if err != nil || len(msgs) != 0 {
			t.Errorf("no trackers: %v, %v", msgs, err)
		}
		msgs, err = tr.TrackerMessages(ctx, cat, tmB)
		if err != nil || !reflect.DeepEqual(msgs, []string{"Unregistered torrent", "Could not connect to tracker"}) {
			t.Errorf("messages = %v, %v", msgs, err)
		}
	})
}

// Every call that reads or changes a torrent refuses one without Reelkeeper's
// label, or one that isn't there, and changes nothing. One foreign hash among
// several is enough to refuse them all.
func TestTransmissionLabelGuard(t *testing.T) {
	ctx := context.Background()
	const cat = "reelkeeper-movies"
	foreign := strings.Repeat("f", 40) // "radarr"'s, from the fake
	f := newFakeTransmission(t)
	f.add(tmA, cat)
	f.add(tmB, "reelkeeper-tv") // Reelkeeper's, but another category
	tr := f.client()

	mutating := map[string]func(hashes ...string) error{
		"Remove":        func(h ...string) error { return tr.Remove(ctx, cat, h, true) },
		"MoveCategory":  func(h ...string) error { return tr.MoveCategory(ctx, cat, "reelkeeper-tv", h) },
		"TopPriority":   func(h ...string) error { return tr.TopPriority(ctx, cat, h) },
		"NoShareLimits": func(h ...string) error { return tr.NoShareLimits(ctx, cat, h) },
		"Stop":          func(h ...string) error { return tr.Stop(ctx, cat, h) },
		"Start":         func(h ...string) error { return tr.Start(ctx, cat, h) },
		"TrackerMessages": func(h ...string) error {
			_, err := tr.TrackerMessages(ctx, cat, h[0])
			return err
		},
	}
	for name, call := range mutating {
		for what, hashes := range map[string][]string{
			"another app's torrent":            {foreign},
			"another Reelkeeper category":      {tmB},
			"a torrent that isn't there":       {tmC},
			"ours together with another app's": {tmA, foreign},
		} {
			f.mu.Lock()
			f.calls = nil
			f.mu.Unlock()
			err := call(hashes...)
			if name == "TrackerMessages" && len(hashes) > 1 {
				continue // asks about one torrent
			}
			if err == nil || !strings.Contains(err.Error(), "won't change it") {
				t.Errorf("%s with %s: error = %v", name, what, err)
			}
			for _, m := range f.called() {
				if m != "torrent-get" {
					t.Errorf("%s with %s: sent %s", name, what, m)
				}
			}
		}
	}
	// Nothing was removed, relabelled or started.
	if got := f.labelsOf(foreign); !reflect.DeepEqual(got, []string{"radarr"}) {
		t.Errorf("foreign torrent's labels = %v", got)
	}
	if len(f.torrents) != 3 {
		t.Errorf("%d torrents left, want 3", len(f.torrents))
	}
	// Ours still works, and the label is matched exactly.
	if err := tr.Stop(ctx, cat, []string{tmA}); err != nil {
		t.Errorf("our own torrent: %v", err)
	}
	f.add(tmC, "Reelkeeper-Movies")
	if err := tr.Stop(ctx, cat, []string{tmC}); err == nil {
		t.Error("a label that only differs in case was accepted")
	}
}
