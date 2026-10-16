package download

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

// fakeDeluge mimics the parts of Deluge's web UI JSON-RPC that Reelkeeper uses,
// including its rules, as seen against Deluge 2.2.0:
//
//   - every answer is HTTP 200; a problem is an "error" object in the body, and
//     "Not authenticated" (code 1) is what a call without a live session gets;
//   - the web UI must be connected to a daemon: until it is, every daemon call
//     (core, label, daemon) answers "Unknown method"; web.connect with an id it
//     doesn't know does nothing and says nothing;
//   - label calls answer "Unknown method" while the Label plugin is off;
//   - a new torrent copies the user's global settings (globals), and setting a
//     label applies the label's own options (labelOpts);
//   - set_torrent_options silently ignores names it doesn't know;
//   - a status read of a torrent Deluge doesn't have is empty, not an error;
//   - force_recheck on a paused torrent resumes it for the length of the check
//     and pauses it again; the state lags (see recheckSteps).
type fakeDeluge struct {
	mu  sync.Mutex
	srv *httptest.Server

	password string
	sessions map[string]bool
	logins   int
	// version answers daemon.get_version.
	version string
	plugins []string
	// connected is whether the web UI is connected to a daemon.
	connected bool
	hosts     [][]any
	// badConnect makes web.connect do nothing, as it does for an id it doesn't know.
	badConnect bool

	labels    []string
	labelOpts map[string]map[string]any
	// globals are the options a new torrent gets.
	globals map[string]any
	// ignoreOptions makes set_torrent_options change nothing, as it does for
	// names it doesn't know.
	ignoreOptions bool

	torrents map[string]map[string]any
	calls    []delugeCall
	// failures answer a method with this message as the error (code 4).
	failures map[string]string
	// lateDuplicate is a torrent that appears when one is added: someone else
	// added it a moment after Reelkeeper looked.
	lateDuplicate map[string]any
	// recheckSteps are the (state, paused) pairs a torrent shows on its next
	// reads after force_recheck; the last one stays. quickRecheck skips to the
	// last at once.
	recheckSteps [][2]any
	// movingReads is how many reads show a torrent as "Moving" after move_storage.
	movingReads int
	// moveFails makes move_storage not change the folder.
	moveFails bool
	// magnets are get_magnet_uri's answers, by hash.
	magnets map[string]string
}

// delugeCall is a call the fake got.
type delugeCall struct {
	Method string
	Params []any
}

func newFakeDeluge(t *testing.T) *fakeDeluge {
	t.Helper()
	f := &fakeDeluge{
		password: "reelkeeper", sessions: map[string]bool{}, version: "2.2.0", plugins: []string{"Label"},
		connected: true, hosts: [][]any{{"host-1", "127.0.0.1", 58846.0, "localclient"}},
		labelOpts: map[string]map[string]any{}, globals: map[string]any{"stop_at_ratio": false, "remove_at_ratio": false, "move_completed": false},
		torrents: map[string]map[string]any{}, failures: map[string]string{}, magnets: map[string]string{},
		recheckSteps: [][2]any{{"Paused", false}, {"Checking", false}, {"Seeding", false}, {"Paused", true}},
	}
	f.torrents[strings.Repeat("f", 40)] = f.row("Someone else's", "radarr", "Seeding", 100)
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// row is a torrent as Deluge reports it.
func (f *fakeDeluge) row(name, label, state string, progress float64) map[string]any {
	done := progress >= 100
	r := map[string]any{
		"name": name, "label": label, "state": state, "paused": state == "Paused", "message": "OK", "is_finished": done,
		"private": false, "progress": progress, "total_wanted": 1000, "total_done": int(progress * 10), "total_remaining": 1000 - int(progress*10),
		"download_payload_rate": 0, "eta": 0, "save_path": "/mnt/downloads/reelkeeper", "ratio": -1.0, "seeding_time": 0, "time_added": 1700000000,
		"completed_time": 0, "num_seeds": 0, "num_peers": 0, "total_seeds": -1, "total_uploaded": 0,
		"trackers": []any{}, "tracker_status": "", "files": []any{},
	}
	for k, v := range f.globals {
		r[k] = v
	}
	return r
}

// client is a Reelkeeper client for the fake, with the right password.
func (f *fakeDeluge) client() *Deluge { return &Deluge{URL: f.srv.URL, Password: "reelkeeper"} }

// add puts a torrent in the fake, with its label.
func (f *fakeDeluge) add(hash, label string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.torrents[hash] = f.row("n-"+hash[:4], label, "Downloading", 50)
	f.torrents[hash]["download_payload_rate"] = 100
}

func (f *fakeDeluge) labelOf(hash string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.torrents[hash]; ok {
		return t["label"].(string)
	}
	return "<none>"
}

// called lists the methods asked for, in order.
func (f *fakeDeluge) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.Method)
	}
	return out
}

// calledWith returns the parameters of every call to a method.
func (f *fakeDeluge) calledWith(method string) [][]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]any
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c.Params)
		}
	}
	return out
}

func (f *fakeDeluge) forget() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

// failure is an error object the way Deluge words one, traceback and all.
func failure(class, msg string, code int) map[string]any {
	return map[string]any{
		"message": "Failure: [Failure instance: Traceback (failure with no frames): <class '" + class + "'>: " + msg +
			"\nTraceback (most recent call last):\n  File \"/lsiopy/lib/python3.12/site-packages/deluge/core/rpcserver.py\", line 342, in dispatch\n]",
		"code": code,
	}
}

func wrapped(msg string) map[string]any { return failure("deluge.error.WrappedException", msg, 4) }

func (f *fakeDeluge) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/json" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	b, _ := io.ReadAll(r.Body)
	var req struct {
		Method string `json:"method"`
		Params []any  `json:"params"`
		ID     int    `json:"id"`
	}
	if err := json.Unmarshal(b, &req); err != nil {
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	f.calls = append(f.calls, delugeCall{Method: req.Method, Params: req.Params})
	answer := func(result, errObj any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": errObj, "id": req.ID})
	}
	if req.Method == "auth.login" {
		ok := len(req.Params) == 1 && req.Params[0] == f.password
		if ok {
			f.logins++
			id := fmt.Sprintf("session-%d", f.logins)
			f.sessions[id] = true
			http.SetCookie(w, &http.Cookie{Name: "_session_id", Value: id, Path: "/"})
		}
		answer(ok, nil)
		return
	}
	if c, err := r.Cookie("_session_id"); err != nil || !f.sessions[c.Value] {
		answer(nil, map[string]any{"message": "Not authenticated", "code": 1})
		return
	}
	if msg, ok := f.failures[req.Method]; ok {
		answer(nil, wrapped(msg))
		return
	}
	switch {
	case req.Method == "web.connected":
		answer(f.connected, nil)
		return
	case req.Method == "web.get_hosts":
		answer(f.hosts, nil)
		return
	case req.Method == "web.connect":
		if !f.badConnect && len(req.Params) == 1 {
			for _, h := range f.hosts {
				if h[0] == req.Params[0] {
					f.connected = true
				}
			}
		}
		answer(nil, nil)
		return
	case !f.connected, strings.HasPrefix(req.Method, "label.") && !slices.Contains(f.plugins, "Label"):
		answer(nil, map[string]any{"message": "Unknown method", "code": 2})
		return
	}
	res, errObj := f.daemon(req.Method, req.Params)
	answer(res, errObj)
}

// daemon answers the calls a connected web UI passes on to the daemon.
func (f *fakeDeluge) daemon(method string, p []any) (any, any) {
	switch method {
	case "daemon.get_version":
		return f.version, nil
	case "core.get_enabled_plugins":
		return f.plugins, nil
	case "label.get_labels":
		return f.labels, nil
	case "label.add":
		name := p[0].(string)
		if slices.Contains(f.labels, name) {
			return nil, wrapped("Label already exists")
		}
		f.labels = append(f.labels, name)
		return nil, nil
	case "label.set_torrent":
		t, ok := f.torrents[p[0].(string)]
		switch {
		case !ok:
			return nil, wrapped("Unknown Torrent")
		case p[1] != "" && !slices.Contains(f.labels, p[1].(string)):
			return nil, wrapped("Unknown Label")
		}
		t["label"] = p[1]
		for k, v := range f.labelOpts[p[1].(string)] {
			t[k] = v
		}
		return nil, nil
	case "core.get_torrents_status":
		return f.status(p[0].(map[string]any), stringsOf(p[1])), nil
	case "core.get_magnet_uri":
		if m, ok := f.magnets[p[0].(string)]; ok {
			return m, nil
		}
		return "magnet:?xt=urn:btih:" + p[0].(string), nil
	case "core.add_torrent_file", "core.add_torrent_magnet":
		return f.addTorrent(method, p)
	case "core.set_torrent_options":
		for _, id := range stringsOf(p[0]) {
			t, ok := f.torrents[id]
			if !ok {
				return nil, wrapped("'" + id + "'")
			}
			if f.ignoreOptions {
				continue
			}
			for k, v := range p[1].(map[string]any) {
				if _, known := f.globals[k]; known {
					t[k] = v
				}
			}
		}
		return nil, nil
	case "core.remove_torrent":
		id := p[0].(string)
		if _, ok := f.torrents[id]; !ok {
			return nil, failure("deluge.error.InvalidTorrentError", "torrent_id "+id+" not in session.", 4)
		}
		delete(f.torrents, id)
		return true, nil
	case "core.pause_torrents", "core.resume_torrents", "core.queue_top", "core.force_recheck", "core.move_storage":
		for _, id := range stringsOf(p[0]) {
			t, ok := f.torrents[id]
			if !ok {
				return nil, wrapped("'" + id + "'")
			}
			f.act(method, t, p)
		}
		return nil, nil
	}
	return nil, map[string]any{"message": "Unknown method", "code": 2}
}

// act applies pause, resume, recheck and move to a torrent.
func (f *fakeDeluge) act(method string, t map[string]any, p []any) {
	switch method {
	case "core.pause_torrents":
		t["paused"], t["state"] = true, "Paused"
	case "core.resume_torrents":
		t["paused"] = false
		t["state"] = "Downloading"
		if t["is_finished"] == true {
			t["state"] = "Seeding"
		}
	case "core.force_recheck":
		t["_steps"] = slices.Clone(f.recheckSteps)
	case "core.move_storage":
		if !f.moveFails {
			t["save_path"] = p[1]
		}
		t["_moving"] = f.movingReads
	}
}

// status answers core.get_torrents_status: torrents matching the filter, with
// only the fields asked for (none: all). Reading advances a torrent's check
// or move by one step.
func (f *fakeDeluge) status(filter map[string]any, fields []string) map[string]any {
	out := map[string]any{}
	for hash, t := range f.torrents {
		if ids, ok := filter["id"]; ok && !slices.Contains(stringsOf(ids), hash) {
			continue
		}
		if l, ok := filter["label"]; ok && t["label"] != l {
			continue
		}
		f.tick(t)
		row := map[string]any{}
		for _, name := range fields {
			if v, ok := t[name]; ok {
				row[name] = v
			}
		}
		out[hash] = row
	}
	return out
}

func (f *fakeDeluge) tick(t map[string]any) {
	if steps, ok := t["_steps"].([][2]any); ok && len(steps) > 0 {
		t["state"], t["paused"] = steps[0][0], steps[0][1]
		if len(steps) > 1 {
			t["_steps"] = steps[1:]
		} else {
			delete(t, "_steps")
		}
	}
	if n, ok := t["_moving"].(int); ok {
		if n > 0 {
			t["state"] = "Moving"
			t["_moving"] = n - 1
		} else {
			t["state"] = "Paused"
			delete(t, "_moving")
		}
	}
}

func (f *fakeDeluge) addTorrent(method string, p []any) (any, any) {
	if f.lateDuplicate != nil {
		f.torrents[f.lateDuplicate["hash"].(string)] = f.lateDuplicate
		dup := f.lateDuplicate["hash"].(string)
		f.lateDuplicate = nil
		return nil, failure("deluge.error.AddTorrentError", "Torrent already in session ("+dup+").", 4)
	}
	var hash string
	var opts map[string]any
	if method == "core.add_torrent_magnet" {
		hash, _ = MagnetHash(p[0].(string))
		opts = p[1].(map[string]any)
	} else {
		raw, _ := base64.StdEncoding.DecodeString(p[1].(string))
		hash, _ = InfoHash(raw)
		opts = p[2].(map[string]any)
	}
	if _, ok := f.torrents[hash]; ok {
		return nil, failure("deluge.error.AddTorrentError", "Torrent already in session ("+hash+").", 4)
	}
	t := f.row("new", "", "Downloading", 0)
	t["total_wanted"], t["total_remaining"] = 0, 0
	if len(opts) > 0 {
		// Options the add names win over the global settings.
		for _, k := range []string{"stop_at_ratio", "remove_at_ratio", "move_completed"} {
			if v, ok := opts[k]; ok && !f.ignoreOptions {
				t[k] = v
			}
		}
		if dir, ok := opts["download_location"]; ok {
			t["save_path"] = dir
		}
		if opts["add_paused"] == true {
			t["paused"], t["state"] = true, "Paused"
		}
	}
	f.torrents[hash] = t
	return hash, nil
}

// ---- tests

func TestDelugeLoginConnectAndSession(t *testing.T) {
	ctx := context.Background()

	t.Run("logs in, connects a web UI that has no daemon, and keeps the session", func(t *testing.T) {
		f := newFakeDeluge(t)
		f.connected = false
		tr := f.client()
		v, err := tr.Version(ctx)
		if err != nil || v != "2.2.0" {
			t.Fatalf("Version = %q, %v", v, err)
		}
		want := []string{"auth.login", "web.connected", "web.get_hosts", "web.connect", "web.connected", "daemon.get_version", "core.get_enabled_plugins"}
		if got := f.called(); !slices.Equal(got, want) {
			t.Errorf("calls:\n got %v\nwant %v", got, want)
		}
		if hosts := f.calledWith("web.connect"); len(hosts) != 1 || hosts[0][0] != "host-1" {
			t.Errorf("connected to %v", hosts)
		}
		// The session is kept: no second login, no second connect.
		f.forget()
		if _, err := tr.Version(ctx); err != nil {
			t.Fatal(err)
		}
		if got := f.called(); !slices.Equal(got, []string{"daemon.get_version", "core.get_enabled_plugins"}) {
			t.Errorf("second Version made calls %v", got)
		}
		if f.logins != 1 {
			t.Errorf("logins = %d", f.logins)
		}
	})

	t.Run("a web UI that is connected already isn't connected again", func(t *testing.T) {
		f := newFakeDeluge(t)
		if _, err := f.client().Version(ctx); err != nil {
			t.Fatal(err)
		}
		if got := f.called(); slices.Contains(got, "web.get_hosts") || slices.Contains(got, "web.connect") {
			t.Errorf("calls = %v", got)
		}
	})

	t.Run("an expired session logs in again and repeats the call", func(t *testing.T) {
		f := newFakeDeluge(t)
		f.add(tmA, "reelkeeper-movies")
		tr := f.client()
		if _, err := tr.Torrents(ctx, "reelkeeper-movies"); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		f.sessions = map[string]bool{} // Deluge restarted, or the session timed out
		f.mu.Unlock()
		ts, err := tr.Torrents(ctx, "reelkeeper-movies")
		if err != nil || len(ts) != 1 {
			t.Fatalf("after the session ended: %v, %v", ts, err)
		}
		if f.logins != 2 {
			t.Errorf("logins = %d, want 2", f.logins)
		}
	})

	t.Run("a daemon that went away is connected again", func(t *testing.T) {
		f := newFakeDeluge(t)
		tr := f.client()
		if _, err := tr.Version(ctx); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		f.connected = false // the web UI lost its daemon: daemon calls say "Unknown method"
		f.mu.Unlock()
		if _, err := tr.Version(ctx); err != nil {
			t.Fatalf("after the daemon went: %v", err)
		}
		if len(f.calledWith("web.connect")) != 1 {
			t.Errorf("connects: %v", f.calledWith("web.connect"))
		}
	})

	t.Run("a wrong password", func(t *testing.T) {
		f := newFakeDeluge(t)
		_, err := (&Deluge{URL: f.srv.URL, Password: "nope"}).Version(ctx)
		if !errors.Is(err, ErrLogin) || err.Error() != "Deluge refused the password" {
			t.Errorf("error = %v", err)
		}
		// Nothing but the login was tried.
		if got := f.called(); !slices.Equal(got, []string{"auth.login"}) {
			t.Errorf("calls = %v", got)
		}
	})

	t.Run("no daemon to connect to, or more than one, is said plainly", func(t *testing.T) {
		const plain = "Deluge's web UI isn't connected to a daemon: open it and connect once"
		for name, hosts := range map[string][][]any{
			"none": {},
			"two":  {{"host-1", "127.0.0.1", 58846.0, "a"}, {"host-2", "10.1.1.1", 58846.0, "b"}},
		} {
			f := newFakeDeluge(t)
			f.connected, f.hosts = false, hosts
			_, err := f.client().Version(ctx)
			if err == nil || err.Error() != plain {
				t.Errorf("%s: error = %v", name, err)
			}
			if len(f.calledWith("web.connect")) != 0 {
				t.Errorf("%s: chose a daemon on its own", name)
			}
		}
	})

	t.Run("a host id that fails silently is caught by asking again", func(t *testing.T) {
		f := newFakeDeluge(t)
		f.connected, f.badConnect = false, true
		_, err := f.client().Version(ctx)
		if err == nil || !strings.Contains(err.Error(), "isn't connected to a daemon: open it and connect once") {
			t.Errorf("error = %v", err)
		}
		if got := f.called(); !slices.Equal(got, []string{"auth.login", "web.connected", "web.get_hosts", "web.connect", "web.connected"}) {
			t.Errorf("calls = %v", got)
		}
	})
}

func TestDelugeVersionAndLabelPluginGate(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		version string
		err     string // empty: accepted
	}{
		{"1.3.15", "Deluge 1.3.15 is too old: Reelkeeper needs 2.1 or newer"},
		{"2.0.5", "Deluge 2.0.5 is too old: Reelkeeper needs 2.1 or newer"},
		{"2.1.0", ""},
		{"2.1.1.dev0", ""},
		{"2.2.0", ""},
		{"3.0.0", ""},
	} {
		f := newFakeDeluge(t)
		f.version = c.version
		v, err := f.client().Version(ctx)
		switch {
		case c.err == "" && (err != nil || v != c.version):
			t.Errorf("%s: %q, %v", c.version, v, err)
		case c.err != "" && (err == nil || err.Error() != c.err):
			t.Errorf("%s: error = %v, want %q", c.version, err, c.err)
		}
	}

	// The Label plugin has to be on, and the connection test says so.
	f := newFakeDeluge(t)
	f.plugins = []string{"Scheduler"}
	_, err := f.client().Version(ctx)
	if err == nil || !strings.HasPrefix(err.Error(), "Deluge's Label plugin is turned off") || !strings.Contains(err.Error(), "Plugins") {
		t.Errorf("plugin off: %v", err)
	}
	// Using a label call anyway (the plugin was turned off later) says the same.
	f.plugins = nil
	err = f.client().EnsureCategory(ctx, "reelkeeper-movies", "")
	if err == nil || !strings.Contains(err.Error(), "Label plugin seems to be off") {
		t.Errorf("label call without the plugin: %v", err)
	}
}

func TestDelugeErrors(t *testing.T) {
	ctx := context.Background()
	f := newFakeDeluge(t)
	f.add(tmA, "reelkeeper-movies")
	tr := f.client()

	// Deluge's own words, without its traceback, in a sentence naming it. The
	// HTTP answer is 200 all the same.
	f.failures["core.remove_torrent"] = "Unknown Torrent"
	err := tr.Remove(ctx, "reelkeeper-movies", []string{tmA}, true)
	if err == nil || err.Error() != "Deluge couldn't remove the torrent: Unknown Torrent" {
		t.Errorf("failed call: %v", err)
	}
	var de *delugeError
	if !errors.As(err, &de) || de.code != 4 {
		t.Errorf("error type: %#v", err)
	}
	if got := (&rpcError{Message: failure("deluge.error.InvalidTorrentError", "torrent_id x not in session.", 4)["message"].(string)}).text(); got != "torrent_id x not in session." {
		t.Errorf("text() = %q", got)
	}
	if got := (&rpcError{Code: 7}).text(); got != "error code 7" {
		t.Errorf("empty message: %q", got)
	}

	// Not Deluge's address at all.
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "<html>hello</html>") }))
	defer web.Close()
	if _, err := (&Deluge{URL: web.URL}).Version(ctx); err == nil || !strings.Contains(err.Error(), "Deluge's answer didn't make sense") {
		t.Errorf("not JSON-RPC: %v", err)
	}
	// An address that isn't the web UI's: a 404 or 405 is not an answer.
	nope := httptest.NewServer(http.NotFoundHandler())
	defer nope.Close()
	if _, err := (&Deluge{URL: nope.URL}).Version(ctx); err == nil || !strings.Contains(err.Error(), "Deluge answered 404") {
		t.Errorf("404: %v", err)
	}
	// Nothing listening.
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	_, err = (&Deluge{URL: gone.URL}).Version(ctx)
	if err == nil || !strings.HasPrefix(err.Error(), "can't reach Deluge: ") || !errors.Is(err, ErrUnreachable) {
		t.Errorf("unreachable: %v", err)
	}
	// A server that never accepts the session doesn't loop forever.
	never := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Method string }
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		if req.Method == "auth.login" {
			_, _ = io.WriteString(w, `{"result": true, "error": null, "id": 1}`)
			return
		}
		_, _ = io.WriteString(w, `{"result": null, "error": {"message": "Not authenticated", "code": 1}, "id": 1}`)
	}))
	defer never.Close()
	_, err = (&Deluge{URL: never.URL}).Version(ctx)
	if err == nil || !strings.Contains(err.Error(), "Not authenticated") {
		t.Errorf("endless code 1: %v", err)
	}
	// A version that isn't one.
	odd := newFakeDeluge(t)
	odd.version = "x"
	if _, err := odd.client().Version(ctx); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Errorf("odd version: %v", err)
	}
}

func TestDelugeEndpoint(t *testing.T) {
	for url, want := range map[string]string{
		"http://192.168.1.20:8112":        "http://192.168.1.20:8112/json",
		"http://192.168.1.20:8112/":       "http://192.168.1.20:8112/json",
		"http://192.168.1.20:8112/json":   "http://192.168.1.20:8112/json",
		"https://box.example/deluge":      "https://box.example/deluge/json",
		"https://box.example/deluge/json": "https://box.example/deluge/json",
	} {
		if got := (&Deluge{URL: url}).endpoint(); got != want {
			t.Errorf("%s: endpoint = %s, want %s", url, got, want)
		}
	}
}

func TestDelugeTorrentsMapping(t *testing.T) {
	ctx := context.Background()
	f := newFakeDeluge(t)
	a := f.row("Film.2024.1080p", "reelkeeper-movies", "Downloading", 25)
	for k, v := range map[string]any{
		"total_wanted": 4000, "total_done": 1000, "total_remaining": 3000, "download_payload_rate": 1234, "eta": 90,
		"save_path": "/mnt/downloads/reelkeeper", "ratio": 0.5, "seeding_time": 7200, "time_added": 1700000000,
		"num_seeds": 3, "num_peers": 8, "total_seeds": 40, "total_uploaded": 2000,
	} {
		a[k] = v
	}
	b := f.row("Done.Film", "reelkeeper-movies", "Seeding", 100)
	b["completed_time"] = 1700000500
	b["save_path"] = "/mnt/downloads/reelkeeper"
	f.torrents[tmA] = a
	f.torrents[tmB] = b
	f.torrents[tmC] = f.row("A show", "reelkeeper-tv", "Downloading", 10)

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
		// What a move needs: bytes verified and uploaded. Deluge has no tags.
		Verified: 1000, Uploaded: 2000,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mapping:\n got %+v\nwant %+v", got, want)
	}
	done := ts[1]
	if done.ETA != 0 || done.Ratio != 0 || done.SwarmSeeds != 0 || done.CompletedOn == nil || !done.CompletedOn.Equal(time.Unix(1700000500, 0)) || !done.Done() {
		t.Errorf("finished torrent with unknown values: %+v", done)
	}
	if got.CompletedOn != nil || got.Done() {
		t.Errorf("unfinished torrent: %+v", got)
	}
	// Only the fields it uses are asked for, and only for Reelkeeper's label.
	params := f.calledWith("core.get_torrents_status")[0]
	if !reflect.DeepEqual(params[0], map[string]any{"label": "reelkeeper-movies"}) {
		t.Errorf("filter = %v", params[0])
	}
	asked := stringsOf(params[1])
	for _, need := range []string{"label", "state", "paused", "progress", "total_done", "download_payload_rate", "is_finished"} {
		if !slices.Contains(asked, need) {
			t.Errorf("didn't ask for %s", need)
		}
	}
	if slices.Contains(asked, "files") || slices.Contains(asked, "peers") || slices.Contains(asked, "trackers") {
		t.Errorf("asked for more than it uses: %v", asked)
	}
	if _, err := f.client().Torrents(ctx, ""); err == nil {
		t.Error("an empty category listed everything")
	}
}

// Deluge reports progress from 0 to 100, and a torrent in error never counts
// as finished.
func TestDelugeProgressAndError(t *testing.T) {
	tr := delugeTorrent{State: "Seeding", Progress: 100, IsFinished: true, TotalWanted: 10}.torrent(tmA, "c")
	if tr.Progress != 1 || !tr.Done() {
		t.Errorf("seeding: %+v", tr)
	}
	tr = delugeTorrent{State: "Downloading", Progress: 99.99, TotalWanted: 10, DownRate: 1}.torrent(tmA, "c")
	if tr.Done() || tr.Progress < 0.99 || tr.Progress >= 1 {
		t.Errorf("nearly done: %+v", tr)
	}
	// A magnet link without details isn't finished although nothing is wanted.
	tr = delugeTorrent{State: "Paused", Progress: 0, IsFinished: true, TotalWanted: 0}.torrent(tmA, "c")
	if tr.Done() {
		t.Errorf("no details yet: %+v", tr)
	}
	tr = delugeTorrent{State: "Error", Progress: 100, IsFinished: true, TotalWanted: 10, Message: "No space left on device"}.torrent(tmA, "c")
	if tr.State != torrent.Error || tr.Done() || tr.Message != "No space left on device" {
		t.Errorf("in error: %+v", tr)
	}
	tr = delugeTorrent{State: "Paused", Progress: 100, IsFinished: true, TotalWanted: 10, TimeAdded: 5}.torrent(tmA, "c")
	if tr.State != torrent.SeedingPaused || !tr.Done() || !tr.Stopped() {
		t.Errorf("paused and finished: %+v", tr)
	}
}

func TestDelugeState(t *testing.T) {
	// Each row is one Deluge torrent. The "like" column is the qBittorrent
	// state it stands for.
	for _, c := range []struct {
		name string
		raw  delugeTorrent
		want torrent.State
		like string
	}{
		{"downloading", delugeTorrent{State: "Downloading", TotalWanted: 100, Progress: 30, DownRate: 500}, torrent.Downloading, "downloading"},
		{"downloading, nothing arriving", delugeTorrent{State: "Downloading", TotalWanted: 100, Progress: 30}, torrent.Stalled, "stalledDL"},
		{"getting details", delugeTorrent{State: "Downloading"}, torrent.Metadata, "metaDL"},
		{"waiting to download", delugeTorrent{State: "Queued", TotalWanted: 100, Progress: 10}, torrent.Queued, "queuedDL"},
		{"waiting to seed", delugeTorrent{State: "Queued", TotalWanted: 100, Progress: 100, IsFinished: true}, torrent.Seeding, "queuedUP"},
		{"checking", delugeTorrent{State: "Checking", TotalWanted: 100, Progress: 50}, torrent.Checking, "checkingDL"},
		{"checking a finished torrent", delugeTorrent{State: "Checking", TotalWanted: 100, Progress: 100, IsFinished: true}, torrent.Checking, "checkingUP"},
		{"allocating", delugeTorrent{State: "Allocating"}, torrent.Checking, "allocating"},
		{"moving files", delugeTorrent{State: "Moving", TotalWanted: 100, Progress: 100, IsFinished: true}, torrent.Moving, "moving"},
		{"seeding", delugeTorrent{State: "Seeding", TotalWanted: 100, Progress: 100, IsFinished: true}, torrent.Seeding, "uploading"},
		{"paused before finishing", delugeTorrent{State: "Paused", TotalWanted: 100, Progress: 40, Paused: true}, torrent.Paused, "pausedDL"},
		{"paused, nothing downloaded", delugeTorrent{State: "Paused", TotalWanted: 100, Paused: true}, torrent.Paused, "pausedDL"},
		{"paused after finishing", delugeTorrent{State: "Paused", TotalWanted: 100, Progress: 100, IsFinished: true, Paused: true}, torrent.SeedingPaused, "pausedUP"},
		{"error", delugeTorrent{State: "Error", TotalWanted: 100, Progress: 20, Message: "Permission denied"}, torrent.Error, "error"},
		{"error after finishing", delugeTorrent{State: "Error", TotalWanted: 100, Progress: 100, IsFinished: true}, torrent.Error, "error"},
		{"a state from the future", delugeTorrent{State: "Hibernating"}, torrent.Unknown, "unknown"},
	} {
		if got := delugeState(c.raw); got != c.want {
			t.Errorf("%s: %q, want %q (qBittorrent's %s)", c.name, got, c.want, c.like)
		}
		// The qBittorrent state it is said to stand for translates to the same.
		if like := qbitState(c.like); like != c.want {
			t.Errorf("%s: qBittorrent's %s is %q, not %q", c.name, c.like, like, c.want)
		}
	}
}

func TestDelugeAdd(t *testing.T) {
	ctx := context.Background()
	magnet := "magnet:?xt=urn:btih:" + tmA + "&dn=film"
	const cat = "reelkeeper-movies"
	wantOptions := map[string]any{"add_paused": true, "stop_at_ratio": false, "remove_at_ratio": false, "move_completed": false}

	t.Run("a magnet link: label first, then the options, then read back, then start", func(t *testing.T) {
		f := newFakeDeluge(t)
		tr := f.client()
		if err := tr.EnsureCategory(ctx, cat, ""); err != nil {
			t.Fatal(err)
		}
		f.forget()
		h, err := tr.Add(ctx, Add{Magnet: magnet, Category: cat, Tags: []string{"reelkeeper", "Tracker"}, SavePath: "/mnt/downloads/reelkeeper"})
		if err != nil || h != tmA {
			t.Fatalf("Add = %q, %v", h, err)
		}
		want := []string{"core.get_torrents_status", "core.add_torrent_magnet", "label.set_torrent", "core.set_torrent_options", "core.get_torrents_status", "core.resume_torrents"}
		if got := f.called(); !slices.Equal(got, want) {
			t.Errorf("calls:\n got %v\nwant %v", got, want)
		}
		add := f.calledWith("core.add_torrent_magnet")[0]
		opts := map[string]any{"download_location": "/mnt/downloads/reelkeeper"}
		for k, v := range wantOptions {
			opts[k] = v
		}
		if add[0] != magnet || !reflect.DeepEqual(add[1], opts) {
			t.Errorf("add params: %v", add)
		}
		if set := f.calledWith("label.set_torrent")[0]; !reflect.DeepEqual(set, []any{tmA, cat}) {
			t.Errorf("label.set_torrent: %v", set)
		}
		if o := f.calledWith("core.set_torrent_options")[0]; !reflect.DeepEqual(o[0], []any{tmA}) ||
			!reflect.DeepEqual(o[1], map[string]any{"stop_at_ratio": false, "remove_at_ratio": false, "move_completed": false}) {
			t.Errorf("options: %v", o)
		}
		if got := f.labelOf(tmA); got != cat {
			t.Errorf("label = %q", got)
		}
		if f.torrents[tmA]["paused"] != false {
			t.Error("the torrent wasn't started")
		}
	})

	t.Run("a torrent file", func(t *testing.T) {
		f := newFakeDeluge(t)
		tr := f.client()
		_ = tr.EnsureCategory(ctx, "reelkeeper-tv", "")
		file := testTorrentFile(t, "film.mkv", []byte("some film data"))
		wantHash, _ := InfoHash(file)
		h, err := tr.Add(ctx, Add{Torrent: file, Category: "reelkeeper-tv"})
		if err != nil || h != wantHash {
			t.Fatalf("Add = %q, %v (want %s)", h, err, wantHash)
		}
		args := f.calledWith("core.add_torrent_file")[0]
		if args[1] != base64.StdEncoding.EncodeToString(file) || len(f.calledWith("core.add_torrent_magnet")) != 0 {
			t.Errorf("arguments: %v", args)
		}
		if _, has := args[2].(map[string]any)["download_location"]; has {
			t.Error("sent a folder although none was set")
		}
	})

	t.Run("Deluge's global settings are overridden: a global remove-at-ratio can't remove it", func(t *testing.T) {
		f := newFakeDeluge(t)
		// The user's Deluge removes torrents at their ratio, stops them, and
		// moves finished ones; the label applies its own move-completed too.
		f.globals = map[string]any{"stop_at_ratio": true, "remove_at_ratio": true, "move_completed": true}
		f.labels = []string{cat}
		f.labelOpts[cat] = map[string]any{"move_completed": true, "remove_at_ratio": true}
		if _, err := f.client().Add(ctx, Add{Magnet: magnet, Category: cat}); err != nil {
			t.Fatal(err)
		}
		tor := f.torrents[tmA]
		if tor["stop_at_ratio"] != false || tor["remove_at_ratio"] != false || tor["move_completed"] != false {
			t.Errorf("options after the add: %v %v %v", tor["stop_at_ratio"], tor["remove_at_ratio"], tor["move_completed"])
		}
	})

	t.Run("options Deluge ignores are an error, and the torrent just added is taken out again", func(t *testing.T) {
		f := newFakeDeluge(t)
		f.globals["remove_at_ratio"] = true
		f.labels = []string{cat}
		f.ignoreOptions = true
		_, err := f.client().Add(ctx, Add{Magnet: magnet, Category: cat})
		if err == nil || !strings.Contains(err.Error(), "didn't turn off its seeding limits") {
			t.Fatalf("error = %v", err)
		}
		if _, there := f.torrents[tmA]; there {
			t.Error("a torrent with the user's limits was left in Deluge")
		}
		rm := f.calledWith("core.remove_torrent")
		if len(rm) != 1 || rm[0][1] != false {
			t.Errorf("remove calls: %v (files must be kept)", rm)
		}
		if slices.Contains(f.called(), "core.resume_torrents") {
			t.Error("started it")
		}
	})

	t.Run("a label that can't be set takes the torrent out again", func(t *testing.T) {
		f := newFakeDeluge(t)
		f.failures["label.set_torrent"] = "Unknown Label"
		_, err := f.client().Add(ctx, Add{Magnet: magnet, Category: cat})
		if err == nil || !strings.Contains(err.Error(), "Deluge couldn't label the torrent reelkeeper-movies: Unknown Label") {
			t.Fatalf("error = %v", err)
		}
		if _, there := f.torrents[tmA]; there {
			t.Error("an unlabelled torrent was left in Deluge")
		}
	})

	t.Run("stopped adds stay stopped, and the folder for a move is the one the files are in", func(t *testing.T) {
		f := newFakeDeluge(t)
		tr := f.client()
		_ = tr.EnsureCategory(ctx, cat, "")
		if _, err := tr.Add(ctx, Add{Magnet: magnet, Category: cat, Stopped: true, SavePath: "/data/final", FilesIn: "/data/temp"}); err != nil {
			t.Fatal(err)
		}
		add := f.calledWith("core.add_torrent_magnet")[0][1].(map[string]any)
		if add["download_location"] != "/data/temp" || add["add_paused"] != true {
			t.Errorf("add options = %v", add)
		}
		if slices.Contains(f.called(), "core.resume_torrents") || f.torrents[tmA]["paused"] != true {
			t.Error("a stopped add was started")
		}
	})

	t.Run("the folder given to EnsureCategory is used when the add has none", func(t *testing.T) {
		f := newFakeDeluge(t)
		tr := f.client()
		if err := tr.EnsureCategory(ctx, cat, "/data/movies"); err != nil {
			t.Fatal(err)
		}
		// A second call doesn't replace it, or add the label again.
		if err := tr.EnsureCategory(ctx, cat, "/elsewhere"); err != nil {
			t.Fatal(err)
		}
		if len(f.calledWith("label.add")) != 1 {
			t.Errorf("label.add calls: %v", f.calledWith("label.add"))
		}
		if _, err := tr.Add(ctx, Add{Magnet: magnet, Category: cat}); err != nil {
			t.Fatal(err)
		}
		if got := f.calledWith("core.add_torrent_magnet")[0][1].(map[string]any)["download_location"]; got != "/data/movies" {
			t.Errorf("download_location = %v", got)
		}
		// The add's own folder wins.
		f2 := newFakeDeluge(t)
		tr2 := f2.client()
		_ = tr2.EnsureCategory(ctx, cat, "/data/movies")
		if _, err := tr2.Add(ctx, Add{Magnet: magnet, Category: cat, SavePath: "/mine"}); err != nil {
			t.Fatal(err)
		}
		if got := f2.calledWith("core.add_torrent_magnet")[0][1].(map[string]any)["download_location"]; got != "/mine" {
			t.Errorf("download_location = %v", got)
		}
	})

	t.Run("a label that exists already, or appears in between, is fine", func(t *testing.T) {
		f := newFakeDeluge(t)
		f.labels = []string{cat}
		if err := f.client().EnsureCategory(ctx, cat, ""); err != nil || len(f.calledWith("label.add")) != 0 {
			t.Errorf("existing label: %v %v", err, f.calledWith("label.add"))
		}
		// Added by someone else between the list and the add.
		g := newFakeDeluge(t)
		g.failures["label.add"] = "Label already exists"
		if err := g.client().EnsureCategory(ctx, cat, ""); err != nil {
			t.Errorf("label added in between: %v", err)
		}
	})

	t.Run("already there with the label counts as added", func(t *testing.T) {
		f := newFakeDeluge(t)
		f.add(tmA, cat)
		h, err := f.client().Add(ctx, Add{Magnet: magnet, Category: cat})
		if err != nil || h != tmA {
			t.Fatalf("Add = %q, %v", h, err)
		}
		if len(f.calledWith("core.add_torrent_magnet")) != 0 {
			t.Error("added a torrent Deluge already has")
		}
	})

	t.Run("already there without the label is left alone", func(t *testing.T) {
		f := newFakeDeluge(t)
		f.add(tmA, "radarr")
		_, err := f.client().Add(ctx, Add{Magnet: magnet, Category: cat})
		if err == nil || !strings.Contains(err.Error(), "Deluge already has this torrent") {
			t.Fatalf("error = %v", err)
		}
		for _, m := range f.called() {
			if m != "core.get_torrents_status" && !strings.HasPrefix(m, "auth.") && !strings.HasPrefix(m, "web.") {
				t.Errorf("sent %s", m)
			}
		}
		if got := f.labelOf(tmA); got != "radarr" {
			t.Errorf("label = %q", got)
		}
	})

	t.Run("another app adds it in between", func(t *testing.T) {
		// Deluge says "already in session" for one Reelkeeper didn't see.
		f := newFakeDeluge(t)
		other := f.row("x", "sonarr", "Seeding", 100)
		other["hash"] = tmA
		f.lateDuplicate = other
		if _, err := f.client().Add(ctx, Add{Magnet: magnet, Category: cat}); err == nil {
			t.Error("took over another app's torrent")
		}
		if got := f.labelOf(tmA); got != "sonarr" {
			t.Errorf("label = %q", got)
		}
		g := newFakeDeluge(t)
		mine := g.row("x", cat, "Downloading", 10)
		mine["hash"] = tmA
		g.lateDuplicate = mine
		if h, err := g.client().Add(ctx, Add{Magnet: magnet, Category: cat}); err != nil || h != tmA {
			t.Errorf("a duplicate that is ours: %q, %v", h, err)
		}
	})

	t.Run("nothing to add, or a bad link", func(t *testing.T) {
		f := newFakeDeluge(t)
		if _, err := f.client().Add(ctx, Add{Category: cat}); err == nil {
			t.Error("added nothing")
		}
		if _, err := f.client().Add(ctx, Add{Magnet: "not a link", Category: cat}); err == nil {
			t.Error("added a bad link")
		}
		if len(f.called()) != 0 {
			t.Errorf("called Deluge: %v", f.called())
		}
	})

	t.Run("Deluge refuses it", func(t *testing.T) {
		f := newFakeDeluge(t)
		f.failures["core.add_torrent_magnet"] = "Unable to add torrent, decoding filedump failed"
		_, err := f.client().Add(ctx, Add{Magnet: magnet, Category: cat})
		if err == nil || err.Error() != "Deluge couldn't add the torrent: Unable to add torrent, decoding filedump failed" {
			t.Errorf("error = %v", err)
		}
	})
}

func TestDelugeCalls(t *testing.T) {
	ctx := context.Background()
	const cat = "reelkeeper-movies"
	newOne := func(t *testing.T) (*fakeDeluge, *Deluge) {
		f := newFakeDeluge(t)
		f.labels = []string{cat, "reelkeeper-tv"}
		f.add(tmA, cat)
		f.add(tmB, cat)
		return f, f.client()
	}
	ids := func(f *fakeDeluge, method string) any {
		c := f.calledWith(method)
		if len(c) == 0 {
			return nil
		}
		return c[len(c)-1][0]
	}

	t.Run("Stop and Start take lists", func(t *testing.T) {
		f, tr := newOne(t)
		if err := tr.Stop(ctx, cat, []string{tmA, tmB}); err != nil {
			t.Fatal(err)
		}
		if err := tr.Start(ctx, cat, []string{tmA}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ids(f, "core.pause_torrents"), []any{tmA, tmB}) || !reflect.DeepEqual(ids(f, "core.resume_torrents"), []any{tmA}) {
			t.Errorf("calls: %v", f.calls)
		}
		if f.torrents[tmB]["paused"] != true || f.torrents[tmA]["paused"] != false {
			t.Error("states")
		}
	})

	t.Run("TopPriority", func(t *testing.T) {
		f, tr := newOne(t)
		if err := tr.TopPriority(ctx, cat, []string{tmB}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ids(f, "core.queue_top"), []any{tmB}) {
			t.Errorf("calls: %v", f.calls)
		}
	})

	t.Run("NoShareLimits turns the limits off and reads it back, then starts", func(t *testing.T) {
		f, tr := newOne(t)
		f.torrents[tmA]["remove_at_ratio"] = true
		f.torrents[tmA]["move_completed"] = true
		f.torrents[tmB]["paused"], f.torrents[tmB]["state"] = true, "Paused"
		if err := tr.NoShareLimits(ctx, cat, []string{tmA, tmB}); err != nil {
			t.Fatal(err)
		}
		if f.torrents[tmA]["remove_at_ratio"] != false || f.torrents[tmA]["move_completed"] != false {
			t.Errorf("options: %v", f.torrents[tmA])
		}
		calls := f.called()
		if i, j := slices.Index(calls, "core.set_torrent_options"), slices.Index(calls, "core.resume_torrents"); i < 0 || j < i {
			t.Errorf("order: %v (limits are lifted first, then the torrents start)", calls)
		}
		if !reflect.DeepEqual(ids(f, "core.resume_torrents"), []any{tmA, tmB}) || f.torrents[tmB]["paused"] != false {
			t.Errorf("start: %v", ids(f, "core.resume_torrents"))
		}
		// Options that didn't take are an error.
		f.ignoreOptions = true
		f.torrents[tmA]["stop_at_ratio"] = true
		if err := tr.NoShareLimits(ctx, cat, []string{tmA}); err == nil {
			t.Error("limits that didn't turn off went unnoticed")
		}
	})

	t.Run("Remove passes remove_data only as asked", func(t *testing.T) {
		f, tr := newOne(t)
		if err := tr.Remove(ctx, cat, []string{tmA}, true); err != nil {
			t.Fatal(err)
		}
		if err := tr.Remove(ctx, cat, []string{tmB}, false); err != nil {
			t.Fatal(err)
		}
		rm := f.calledWith("core.remove_torrent")
		if len(rm) != 2 || !reflect.DeepEqual(rm[0], []any{tmA, true}) || !reflect.DeepEqual(rm[1], []any{tmB, false}) {
			t.Errorf("remove_torrent: %v", rm)
		}
		if _, there := f.torrents[tmA]; there {
			t.Error("not removed")
		}
	})

	t.Run("MoveCategory relabels and keeps the options off", func(t *testing.T) {
		f, tr := newOne(t)
		f.labelOpts["reelkeeper-tv"] = map[string]any{"move_completed": true}
		if err := tr.MoveCategory(ctx, cat, "reelkeeper-tv", []string{tmA, tmB}); err != nil {
			t.Fatal(err)
		}
		for _, h := range []string{tmA, tmB} {
			if f.labelOf(h) != "reelkeeper-tv" || f.torrents[h]["move_completed"] != false {
				t.Errorf("%s: %v", h[:4], f.torrents[h])
			}
		}
		// A label Deluge doesn't have yet is added.
		g, tr2 := newOne(t)
		g.labels = []string{cat}
		if err := tr2.MoveCategory(ctx, cat, "reelkeeper-tv", []string{tmA}); err != nil || !slices.Contains(g.labels, "reelkeeper-tv") {
			t.Errorf("new label: %v %v", err, g.labels)
		}
	})

	t.Run("no hashes, no calls", func(t *testing.T) {
		f, tr := newOne(t)
		f.forget()
		for _, err := range []error{
			tr.Stop(ctx, cat, nil), tr.Start(ctx, cat, nil), tr.TopPriority(ctx, cat, nil), tr.NoShareLimits(ctx, cat, nil),
			tr.Remove(ctx, cat, nil, true), tr.MoveCategory(ctx, cat, "x", nil),
		} {
			if err != nil {
				t.Error(err)
			}
		}
		if len(f.called()) != 0 {
			t.Errorf("called Deluge: %v", f.called())
		}
	})

	t.Run("hashes in capitals are Deluge's lower case", func(t *testing.T) {
		f, tr := newOne(t)
		if err := tr.Stop(ctx, cat, []string{strings.ToUpper(tmA)}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ids(f, "core.pause_torrents"), []any{tmA}) {
			t.Errorf("ids = %v", ids(f, "core.pause_torrents"))
		}
	})

	t.Run("TrackerMessages", func(t *testing.T) {
		f, tr := newOne(t)
		f.torrents[tmB]["tracker_status"] = "Error: Unregistered torrent"
		f.torrents[tmB]["trackers"] = []any{
			map[string]any{"message": "", "endpoints": []any{}},
			map[string]any{"message": "Could not connect to tracker", "endpoints": []any{map[string]any{"message": "Unregistered torrent"}, map[string]any{"message": ""}}},
		}
		f.torrents[tmA]["tracker_status"] = "Announce OK"
		msgs, err := tr.TrackerMessages(ctx, cat, tmA)
		if err != nil || len(msgs) != 0 {
			t.Errorf("a tracker that is fine: %v, %v", msgs, err)
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
func TestDelugeLabelGuard(t *testing.T) {
	ctx := context.Background()
	const cat = "reelkeeper-movies"
	foreign := strings.Repeat("f", 40) // "radarr"'s, from the fake
	f := newFakeDeluge(t)
	f.labels = []string{cat, "reelkeeper-tv"}
	f.add(tmA, cat)
	f.add(tmB, "reelkeeper-tv") // Reelkeeper's, but another category
	tr := f.client()

	calls := map[string]func(hashes ...string) error{
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
		"Source":    func(h ...string) error { _, err := tr.Source(ctx, cat, h[0]); return err },
		"Layout":    func(h ...string) error { _, err := tr.Layout(ctx, cat, h[0]); return err },
		"Recheck":   func(h ...string) error { return tr.Recheck(ctx, cat, h[0]) },
		"SetFolder": func(h ...string) error { return tr.SetFolder(ctx, cat, h[0], "/x") },
	}
	single := map[string]bool{"TrackerMessages": true, "Source": true, "Layout": true, "Recheck": true, "SetFolder": true}
	for name, call := range calls {
		for what, hashes := range map[string][]string{
			"another app's torrent":            {foreign},
			"another Reelkeeper category":      {tmB},
			"a torrent that isn't there":       {tmC},
			"ours together with another app's": {tmA, foreign},
		} {
			if single[name] && len(hashes) > 1 {
				continue // asks about one torrent
			}
			f.forget()
			err := call(hashes...)
			if err == nil || (!strings.Contains(err.Error(), "won't change it") && !strings.Contains(err.Error(), "doesn't list torrent")) {
				t.Errorf("%s with %s: error = %v", name, what, err)
			}
			// Only reads: nothing was sent that changes a torrent.
			for _, m := range f.called() {
				if m != "core.get_torrents_status" && m != "label.get_labels" && !strings.HasPrefix(m, "auth.") && !strings.HasPrefix(m, "web.") {
					t.Errorf("%s with %s: sent %s", name, what, m)
				}
			}
		}
	}
	// Nothing was removed, relabelled or started.
	if got := f.labelOf(foreign); got != "radarr" {
		t.Errorf("foreign torrent's label = %q", got)
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
	// Listing never shows another app's torrents, even asked by a made-up name.
	ts, err := tr.Torrents(ctx, cat)
	if err != nil || len(ts) != 1 || ts[0].Hash != tmA {
		t.Errorf("listing: %+v, %v", ts, err)
	}
}

// The parts of the client that moving a torrent to or from another client uses.
func TestDelugeReadsWhatAMoveNeeds(t *testing.T) {
	ctx := context.Background()
	const cat = "reelkeeper-movies"
	f := newFakeDeluge(t)
	f.labels = []string{cat}
	f.add(tmA, cat)
	row := f.torrents[tmA]
	row["state"], row["paused"], row["progress"], row["total_done"], row["total_uploaded"] = "Paused", true, 48.0, 480, 77
	row["save_path"] = "/data/final/"
	row["files"] = []any{
		map[string]any{"index": 0, "path": "Film/a.mkv", "size": 600, "offset": 0},
		map[string]any{"index": 1, "path": "Film/b.srt", "size": 400, "offset": 600},
	}
	row["private"] = true
	f.magnets[tmA] = "magnet:?xt=urn:btih:" + tmA + "&tr=http%3A%2F%2Ftracker%2Fannounce%3Fpasskey%3Dsecret"
	tr := f.client()

	ts, err := tr.Torrents(ctx, cat)
	if err != nil || len(ts) != 1 || ts[0].Verified != 480 || ts[0].Uploaded != 77 || !ts[0].Stopped() || ts[0].State != torrent.Paused {
		t.Fatalf("torrents = %+v, %v", ts, err)
	}
	lay, err := tr.Layout(ctx, cat, tmA)
	want := Layout{Folder: "/data/final", Look: []string{"/data/final"}, Files: []File{{"Film/a.mkv", 600}, {"Film/b.srt", 400}}}
	if err != nil || !reflect.DeepEqual(lay, want) {
		t.Errorf("layout:\n got %+v (%v)\nwant %+v", lay, err, want)
	}

	// A private torrent can't be handed over; a public one gives its magnet link.
	_, err = tr.Source(ctx, cat, tmA)
	var ns *NoSourceError
	if !errors.As(err, &ns) || !strings.Contains(ns.Reason, "private torrent") || !strings.Contains(ns.Reason, "no copy") {
		t.Errorf("private: %v", err)
	}
	row["private"] = false
	src, err := tr.Source(ctx, cat, tmA)
	if err != nil || src.Magnet != f.magnets[tmA] || len(src.Torrent) != 0 {
		t.Errorf("public: %+v %v", src, err)
	}

	// The files are put in their folder only when they aren't there already.
	if err := tr.SetFolder(ctx, cat, tmA, "/data/final"); err != nil || len(f.calledWith("core.move_storage")) != 0 {
		t.Errorf("already there: %v %v", err, f.calledWith("core.move_storage"))
	}
	if err := tr.SetFolder(ctx, cat, tmA, "/data/other"); err != nil {
		t.Fatal(err)
	}
	mv := f.calledWith("core.move_storage")
	if len(mv) != 1 || !reflect.DeepEqual(mv[0], []any{[]any{tmA}, "/data/other"}) {
		t.Errorf("move_storage = %v", mv)
	}
	if f.torrents[tmA]["save_path"] != "/data/other" {
		t.Errorf("save_path = %v", f.torrents[tmA]["save_path"])
	}
}

func TestDelugeSetFolder(t *testing.T) {
	ctx := context.Background()
	const cat = "reelkeeper-movies"

	// It returns when Deluge has stopped saying "Moving", and not before.
	f := newFakeDeluge(t)
	f.add(tmA, cat)
	f.movingReads = 3
	tr := f.client()
	if err := tr.SetFolder(ctx, cat, tmA, "/data/other"); err != nil {
		t.Fatal(err)
	}
	if f.torrents[tmA]["state"] != "Paused" {
		t.Errorf("returned while moving: %v", f.torrents[tmA]["state"])
	}
	if n := len(f.calledWith("core.get_torrents_status")); n < 5 {
		t.Errorf("looked %d times; it should have waited for the move", n)
	}

	// A move that didn't change the folder is an error, with Deluge's message.
	g := newFakeDeluge(t)
	g.add(tmA, cat)
	g.moveFails = true
	g.torrents[tmA]["message"] = "Permission denied"
	err := g.client().SetFolder(ctx, cat, tmA, "/data/other")
	if err == nil || !strings.Contains(err.Error(), "didn't move the torrent's files to /data/other") || !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("failed move: %v", err)
	}
}

// Deluge resumes a paused torrent for the length of a check and pauses it
// again, and its state lags. Recheck's torrent is Checking until the state is
// "Paused" and paused is true; only then are total_done and the state read as
// usual.
func TestDelugeRecheck(t *testing.T) {
	ctx := context.Background()
	const cat = "reelkeeper-movies"
	states := func(tr *Deluge) []torrent.State {
		var out []torrent.State
		for range 8 {
			ts, err := tr.Torrents(ctx, cat)
			if err != nil || len(ts) != 1 {
				t.Fatalf("%v %v", ts, err)
			}
			out = append(out, ts[0].State)
		}
		return out
	}
	paused := func(f *fakeDeluge) {
		f.add(tmA, cat)
		r := f.torrents[tmA]
		r["state"], r["paused"], r["progress"], r["is_finished"], r["total_done"], r["total_remaining"] = "Paused", true, 100.0, true, 1000, 0
	}

	t.Run("the state lags and goes through Seeding: Checking until paused again", func(t *testing.T) {
		f := newFakeDeluge(t)
		paused(f)
		tr := f.client()
		if err := tr.Recheck(ctx, cat, tmA); err != nil {
			t.Fatal(err)
		}
		if got := f.calledWith("core.force_recheck"); len(got) != 1 || !reflect.DeepEqual(got[0], []any{[]any{tmA}}) {
			t.Errorf("force_recheck = %v", got)
		}
		want := []torrent.State{torrent.Checking, torrent.Checking, torrent.Checking, torrent.SeedingPaused, torrent.SeedingPaused, torrent.SeedingPaused, torrent.SeedingPaused, torrent.SeedingPaused}
		if got := states(tr); !slices.Equal(got, want) {
			t.Errorf("states:\n got %v\nwant %v", got, want)
		}
		ts, _ := tr.Torrents(ctx, cat)
		if ts[0].Verified != 1000 || !ts[0].Stopped() {
			t.Errorf("after the check: %+v", ts[0])
		}
	})

	t.Run("a check that is over before it was seen", func(t *testing.T) {
		f := newFakeDeluge(t)
		paused(f)
		f.recheckSteps = [][2]any{{"Paused", true}} // already paused again at the first look
		now := time.Unix(1700000000, 0)
		tr := f.client()
		tr.now = func() time.Time { return now }
		if err := tr.Recheck(ctx, cat, tmA); err != nil {
			t.Fatal(err)
		}
		// Not trusted straight away: Checking, until the grace has passed.
		if ts, _ := tr.Torrents(ctx, cat); ts[0].State != torrent.Checking {
			t.Errorf("at once: %v", ts[0].State)
		}
		now = now.Add(recheckGrace + time.Second)
		if ts, _ := tr.Torrents(ctx, cat); ts[0].State != torrent.SeedingPaused {
			t.Errorf("after the grace: %v", ts[0].State)
		}
		if ts, _ := tr.Torrents(ctx, cat); ts[0].State != torrent.SeedingPaused {
			t.Errorf("and it stays: %v", ts[0].State)
		}
	})

	t.Run("an unfinished torrent ends Paused, with the bytes that checked out", func(t *testing.T) {
		f := newFakeDeluge(t)
		f.add(tmA, cat)
		r := f.torrents[tmA]
		r["state"], r["paused"], r["progress"], r["total_done"], r["total_remaining"] = "Paused", true, 40.0, 400, 600
		tr := f.client()
		if err := tr.Recheck(ctx, cat, tmA); err != nil {
			t.Fatal(err)
		}
		_ = states(tr)
		ts, _ := tr.Torrents(ctx, cat)
		if ts[0].State != torrent.Paused || ts[0].Verified != 400 {
			t.Errorf("after: %+v", ts[0])
		}
	})

	t.Run("a torrent that goes into error is not held as Checking", func(t *testing.T) {
		f := newFakeDeluge(t)
		paused(f)
		f.recheckSteps = [][2]any{{"Error", true}}
		tr := f.client()
		if err := tr.Recheck(ctx, cat, tmA); err != nil {
			t.Fatal(err)
		}
		if ts, _ := tr.Torrents(ctx, cat); ts[0].State != torrent.Error {
			t.Errorf("state = %v", ts[0].State)
		}
	})

	t.Run("only a paused torrent is checked", func(t *testing.T) {
		f := newFakeDeluge(t)
		f.add(tmA, cat)
		err := f.client().Recheck(ctx, cat, tmA)
		if err == nil || !strings.Contains(err.Error(), "only check a paused torrent") {
			t.Errorf("error = %v", err)
		}
		if len(f.calledWith("core.force_recheck")) != 0 {
			t.Error("checked a running torrent")
		}
	})

	t.Run("a refused check isn't tracked", func(t *testing.T) {
		f := newFakeDeluge(t)
		paused(f)
		f.failures["core.force_recheck"] = "boom"
		tr := f.client()
		if err := tr.Recheck(ctx, cat, tmA); err == nil {
			t.Fatal("no error")
		}
		if ts, _ := tr.Torrents(ctx, cat); ts[0].State != torrent.SeedingPaused {
			t.Errorf("state = %v", ts[0].State)
		}
	})
}

func TestDelugeUnreachableMatchesErrUnreachable(t *testing.T) {
	if _, err := (&Deluge{URL: "http://127.0.0.1:1", Password: "x"}).Version(context.Background()); !errors.Is(err, ErrUnreachable) {
		t.Errorf("Deluge: %v", err)
	}
	if !unreachable(passwordError{}) {
		t.Error("a wrong password should be retried like an unreachable client")
	}
}
