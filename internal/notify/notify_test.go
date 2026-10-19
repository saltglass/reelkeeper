package notify_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/settings"
)

type captured struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
}

func (c *captured) handler(status int, reply string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.reqs = append(c.reqs, r)
		c.body = append(c.body, string(b))
		c.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	})
}

func newSvc(cfg settings.Notifications) *notify.Service {
	return notify.New(func() settings.Notifications { return cfg }, slog.New(slog.DiscardHandler))
}

func TestEmbyGetsTheTranslatedFolder(t *testing.T) {
	var c captured
	srv := httptest.NewServer(c.handler(204, ""))
	defer srv.Close()
	svc := newSvc(settings.Notifications{Emby: settings.MediaServerNotify{
		Enabled: true, URL: srv.URL, APIKey: "tok", PathFrom: "/media", PathTo: "/Volumes/Media",
	}})

	cases := []struct{ kind, path, wantPath, wantType string }{
		{notify.KindImported, "/media/movies/Dune (2021)/Dune (2021).mkv", "/Volumes/Media/movies/Dune (2021)", "Created"},
		{notify.KindDeleted, "/media/movies/Dr. Strangelove (1964)", "/Volumes/Media/movies/Dr. Strangelove (1964)", "Deleted"},
		{notify.KindRenamed, "/other/Movie (2000)/Movie (2000).mp4", "/other/Movie (2000)", "Modified"},
	}
	for _, tc := range cases {
		svc.Notify(context.Background(), notify.Event{Kind: tc.kind, Title: "x", Path: tc.path})
	}
	// Problems and path-less events never reach Emby.
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindImportFailed, Path: "/media/a"})
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindImported})
	svc.Wait()

	if len(c.reqs) != len(cases) {
		t.Fatalf("Emby got %d requests, want %d", len(c.reqs), len(cases))
	}
	got := map[string]string{}
	for i, r := range c.reqs {
		if r.URL.Path != "/emby/Library/Media/Updated" || r.Method != "POST" {
			t.Errorf("request %d: %s %s (never a full refresh)", i, r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Emby-Token") != "tok" {
			t.Errorf("token header = %q", r.Header.Get("X-Emby-Token"))
		}
		var b struct {
			Updates []struct{ Path, UpdateType string }
		}
		if err := json.Unmarshal([]byte(c.body[i]), &b); err != nil || len(b.Updates) != 1 {
			t.Fatalf("body = %s", c.body[i])
		}
		got[b.Updates[0].Path] = b.Updates[0].UpdateType
	}
	for _, tc := range cases {
		if got[tc.wantPath] != tc.wantType {
			t.Errorf("%s: got %v", tc.wantPath, got)
		}
	}
}

func TestEmbyTest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != "good" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ServerName":"Emby-server","Version":"4.9.3"}`))
	}))
	defer srv.Close()
	svc := newSvc(settings.Notifications{})
	msg, err := svc.Test(context.Background(), "emby", settings.Notifications{Emby: settings.MediaServerNotify{URL: srv.URL, APIKey: "good"}})
	if err != nil || msg != "Connected to Emby 4.9.3 (Emby-server)" {
		t.Errorf("= %q, %v", msg, err)
	}
	_, err = svc.Test(context.Background(), "emby", settings.Notifications{Emby: settings.MediaServerNotify{URL: srv.URL, APIKey: "bad"}})
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("bad key: %v", err)
	}
}

// Jellyfin gets Emby's call without the /emby prefix, with the key in the
// standard Authorization header: Jellyfin 10.11 can refuse Emby's X-Emby-Token,
// and newer versions do by default.
func TestJellyfinGetsTheTranslatedFolder(t *testing.T) {
	var c captured
	srv := httptest.NewServer(c.handler(204, ""))
	defer srv.Close()
	svc := newSvc(settings.Notifications{Jellyfin: settings.MediaServerNotify{
		Enabled: true, URL: srv.URL + "/jellyfin", APIKey: "tok", PathFrom: "/media", PathTo: "/data/media",
	}})
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindImported, Path: "/media/movies/Dune (2021)/Dune (2021).mkv"})
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindDeleted, Path: "/media/tv/Severance"})
	// Problems and path-less events never reach it.
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindImportFailed, Path: "/media/a"})
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindImported})
	svc.Wait()

	if len(c.reqs) != 2 {
		t.Fatalf("Jellyfin got %d requests, want 2", len(c.reqs))
	}
	got := map[string]string{}
	for i, r := range c.reqs {
		// A base path in the address (a reverse proxy's /jellyfin) is kept.
		if r.URL.Path != "/jellyfin/Library/Media/Updated" || r.Method != "POST" {
			t.Errorf("request %d: %s %s", i, r.Method, r.URL.Path)
		}
		if a := r.Header.Get("Authorization"); a != `MediaBrowser Token="tok"` {
			t.Errorf("Authorization = %q", a)
		}
		if r.Header.Get("X-Emby-Token") != "" {
			t.Error("Jellyfin was sent Emby's legacy token header")
		}
		var b struct {
			Updates []struct{ Path, UpdateType string }
		}
		if err := json.Unmarshal([]byte(c.body[i]), &b); err != nil || len(b.Updates) != 1 {
			t.Fatalf("body = %s", c.body[i])
		}
		got[b.Updates[0].Path] = b.Updates[0].UpdateType
	}
	if got["/data/media/movies/Dune (2021)"] != "Created" || got["/data/media/tv/Severance"] != "Deleted" {
		t.Errorf("updates = %v", got)
	}
}

// Emby and Jellyfin can both be on (one library in each); each gets the event.
func TestEmbyAndJellyfinBothGetTheEvent(t *testing.T) {
	var e, j captured
	es := httptest.NewServer(e.handler(204, ""))
	defer es.Close()
	js := httptest.NewServer(j.handler(204, ""))
	defer js.Close()
	svc := newSvc(settings.Notifications{
		Emby:     settings.MediaServerNotify{Enabled: true, URL: es.URL, APIKey: "e"},
		Jellyfin: settings.MediaServerNotify{Enabled: true, URL: js.URL, APIKey: "j"},
	})
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindRenamed, Path: "/media/movies/Heat (1995)"})
	svc.Wait()
	if len(e.reqs) != 1 || len(j.reqs) != 1 {
		t.Errorf("emby got %d, jellyfin got %d", len(e.reqs), len(j.reqs))
	}
}

// Jellyfin answers Emby's /emby paths too, with a Jellyfin key, so the Emby
// card's Test must not report a Jellyfin server as Emby (2026-10-19).
func TestEmbyTestRejectsAJellyfinServer(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("X-Emby-Token") != "jf-key" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ServerName":"den","Version":"10.11.0","ProductName":"Jellyfin Server"}`))
	}))
	defer srv.Close()
	svc := newSvc(settings.Notifications{})
	msg, err := svc.Test(context.Background(), "emby", settings.Notifications{Emby: settings.MediaServerNotify{URL: srv.URL, APIKey: "jf-key"}})
	if err == nil || err.Error() != "that address answered, but it is a Jellyfin server; use the Jellyfin card" {
		t.Errorf("= %q, %v", msg, err)
	}
	if len(paths) != 1 || paths[0] != "/emby/System/Info" {
		t.Errorf("asked %v", paths)
	}
}

func TestJellyfinTest(t *testing.T) {
	product := "Jellyfin Server"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/System/Info" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != `MediaBrowser Token="good"` {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ServerName":"den","Version":"10.11.2","ProductName":"` + product + `"}`))
	}))
	defer srv.Close()
	svc := newSvc(settings.Notifications{})
	test := func(key string) (string, error) {
		return svc.Test(context.Background(), "jellyfin", settings.Notifications{Jellyfin: settings.MediaServerNotify{URL: srv.URL, APIKey: key}})
	}
	if msg, err := test("good"); err != nil || msg != "Connected to Jellyfin 10.11.2 (den)" {
		t.Errorf("= %q, %v", msg, err)
	}
	if _, err := test("bad"); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("bad key: %v", err)
	}
	// An Emby server at the Jellyfin address is caught, not reported as Jellyfin.
	product = "Emby Server"
	if _, err := test("good"); err == nil || !strings.Contains(err.Error(), "doesn't look like Jellyfin") {
		t.Errorf("Emby at the address: %v", err)
	}
	if _, err := svc.Test(context.Background(), "jellyfin", settings.Notifications{}); err == nil || !strings.Contains(err.Error(), "Jellyfin's address") {
		t.Errorf("no address: %v", err)
	}
}

func TestWebhookGetsEveryEventWithSecret(t *testing.T) {
	var c captured
	srv := httptest.NewServer(c.handler(200, ""))
	defer srv.Close()
	svc := newSvc(settings.Notifications{Webhook: settings.WebhookNotify{Enabled: true, URL: srv.URL + "/hook", Secret: "s3"}})
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindImported, Title: "Dune", Message: "m", TitleID: 7, Path: "/media/x", At: at})
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindGrabFailed, Title: "Dune"})
	svc.Wait()
	if len(c.reqs) != 2 {
		t.Fatalf("got %d requests", len(c.reqs))
	}
	for _, r := range c.reqs {
		if r.Header.Get("X-Reelkeeper-Secret") != "s3" {
			t.Errorf("secret header = %q", r.Header.Get("X-Reelkeeper-Secret"))
		}
	}
	var p map[string]any
	for _, b := range c.body {
		_ = json.Unmarshal([]byte(b), &p)
		if p["event"] == "imported" {
			if p["title"] != "Dune" || p["message"] != "m" || p["titleId"] != float64(7) || p["path"] != "/media/x" || p["at"] != "2026-10-09T12:00:00Z" {
				t.Errorf("payload = %v", p)
			}
		}
	}
}

func TestFailingChannelDoesNotAffectOthers(t *testing.T) {
	var c captured
	ok := httptest.NewServer(c.handler(200, ""))
	defer ok.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) }))
	defer broken.Close()
	svc := newSvc(settings.Notifications{
		Emby:    settings.MediaServerNotify{Enabled: true, URL: broken.URL, APIKey: "k"},
		Webhook: settings.WebhookNotify{Enabled: true, URL: ok.URL},
	})
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindImported, Path: "/a/b.mkv"})
	svc.Wait()
	if len(c.reqs) != 1 {
		t.Errorf("webhook got %d requests", len(c.reqs))
	}
}

// fakeSMTP accepts one plain connection and records the message.
func fakeSMTP(t *testing.T) (port int, got func() string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var mu sync.Mutex
	var msg strings.Builder
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				rd := bufio.NewReader(conn)
				write := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
				write("220 fake")
				data := false
				for {
					line, err := rd.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimRight(line, "\r\n")
					if data {
						if line == "." {
							data = false
							write("250 queued")
							continue
						}
						mu.Lock()
						msg.WriteString(line + "\n")
						mu.Unlock()
						continue
					}
					switch cmd := strings.ToUpper(line); {
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
						write("250 fake")
					case strings.HasPrefix(cmd, "DATA"):
						data = true
						write("354 go")
					case strings.HasPrefix(cmd, "QUIT"):
						write("221 bye")
						return
					default:
						write("250 ok")
					}
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() string { mu.Lock(); defer mu.Unlock(); return msg.String() }
}

func TestEmailOnlyForProblemsAndNotRepeated(t *testing.T) {
	port, got := fakeSMTP(t)
	svc := newSvc(settings.Notifications{Email: settings.EmailNotify{
		Enabled: true, Host: "127.0.0.1", Port: port, From: "reelkeeper@example.com", To: "dan@example.com",
	}})
	now := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	notify.SetClock(svc, func() time.Time { return now })

	svc.Notify(context.Background(), notify.Event{Kind: notify.KindImported, Title: "Fine", Message: "all good"})
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindImportFailed, Title: "Dune (2021)", Message: "The file was damaged."})
	svc.Wait()
	m := got()
	if strings.Contains(m, "all good") {
		t.Errorf("a non-problem was emailed:\n%s", m)
	}
	for _, want := range []string{"To: dan@example.com", "Subject: Reelkeeper: Dune (2021)", "The file was damaged.", "Sent by Reelkeeper"} {
		if !strings.Contains(m, want) {
			t.Errorf("email lacks %q:\n%s", want, m)
		}
	}

	// The same problem within six hours is not sent again; after, it is.
	count := func() int { return strings.Count(got(), "The file was damaged.") }
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindImportFailed, Title: "Dune (2021)", Message: "The file was damaged."})
	svc.Wait()
	if count() != 1 {
		t.Errorf("repeat emailed: %d", count())
	}
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindImportFailed, Title: "Other", Message: "The file was damaged."})
	svc.Wait()
	if count() != 2 {
		t.Errorf("a different title should be emailed: %d", count())
	}
	now = now.Add(7 * time.Hour)
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindImportFailed, Title: "Dune (2021)", Message: "The file was damaged."})
	svc.Wait()
	if count() != 3 {
		t.Errorf("after 6h it should be emailed again: %d", count())
	}
}

func TestEmailLoginOnLocalServer(t *testing.T) {
	port, _ := fakeSMTP(t)
	svc := newSvc(settings.Notifications{})
	// 127.0.0.1 counts as local, so the login is allowed there...
	_, err := svc.Test(context.Background(), "email", settings.Notifications{Email: settings.EmailNotify{
		Host: "127.0.0.1", Port: port, Username: "u", Password: "p", From: "a@example.com", To: "b@example.com",
	}})
	// ...the fake has no AUTH so it fails at login, not before.
	if err == nil || !strings.Contains(err.Error(), "login") {
		t.Errorf("local login attempt: %v", err)
	}
}

func TestMessageBuilding(t *testing.T) {
	m := string(notify.BuildMessageForTest("a@x.com", "b@x.com", "Reelkeeper: Bad\r\nBcc: evil@x.com", "line1\nline2", ""))
	if strings.Contains(m, "\r\nBcc:") {
		t.Errorf("header injection:\n%s", m)
	}
	if !strings.Contains(m, "line1\r\nline2") {
		t.Errorf("body lines:\n%s", m)
	}
}

func TestProblems(t *testing.T) {
	for _, k := range []string{notify.KindImportFailed, notify.KindDownloadRemoved, notify.KindIndexerFailing, notify.KindClientUnreachable, notify.KindGrabFailed} {
		if !notify.IsProblem(k) {
			t.Errorf("%s should be a problem", k)
		}
	}
	for _, k := range []string{notify.KindImported, notify.KindDeleted, notify.KindRenamed} {
		if notify.IsProblem(k) {
			t.Errorf("%s is not a problem", k)
		}
	}
}

func TestLiveHookSeesEveryEventEvenWithNoChannels(t *testing.T) {
	svc := newSvc(settings.Notifications{})
	var got []notify.Event
	svc.SetLive(func(ev notify.Event) { got = append(got, ev) })
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindImported, Title: "Dune", Message: "Imported Dune"})
	svc.Notify(context.Background(), notify.Event{Kind: notify.KindImportFailed, Title: "Dune"})
	svc.Wait()
	if len(got) != 2 || got[0].Message != "Imported Dune" || notify.IsProblem(got[0].Kind) || !notify.IsProblem(got[1].Kind) {
		t.Errorf("live hook got %+v", got)
	}
}
