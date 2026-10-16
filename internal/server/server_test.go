package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(t *testing.T, urlBase string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(Options{
		URLBase: urlBase,
		Logger:  slog.New(slog.DiscardHandler),
	}))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestPing(t *testing.T) {
	srv := newTestServer(t, "")
	code, body := get(t, srv.URL+"/ping")
	if code != http.StatusOK || body != `{"status":"OK"}` {
		t.Errorf("got %d %s", code, body)
	}
}

func TestPingUnderURLBase(t *testing.T) {
	srv := newTestServer(t, "/reelkeeper")
	for _, path := range []string{"/ping", "/reelkeeper/ping"} {
		if code, _ := get(t, srv.URL+path); code != http.StatusOK {
			t.Errorf("%s: status %d, want 200", path, code)
		}
	}
}

func TestUnknownPathIs404(t *testing.T) {
	srv := newTestServer(t, "")
	if code, _ := get(t, srv.URL+"/nope"); code != http.StatusNotFound {
		t.Errorf("status %d, want 404", code)
	}
}
