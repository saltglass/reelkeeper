package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// fakeSABnzbd answers the calls the connection test makes, like SABnzbd 5.1.
func fakeSABnzbd(t *testing.T, misc string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("apikey") != "sabkey" {
			_, _ = io.WriteString(w, "API Key Incorrect")
			return
		}
		switch q.Get("mode") {
		case "version":
			_, _ = io.WriteString(w, `{"version":"5.1.3"}`)
		case "get_config":
			_, _ = io.WriteString(w, `{"config":{"misc":`+misc+`}}`)
		case "queue":
			_, _ = io.WriteString(w, `{"queue":{"slots":[]}}`)
		case "history":
			_, _ = io.WriteString(w, `{"history":{"slots":[]}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestUsenetClientAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		a := newApp(t, db, "")
		a.setup()
		if r := a.do("GET", "/api/v1/settings/usenet-client", ""); r.status != 200 || r.body["url"] != "" || r.body["apiKeySet"] != false || r.body["type"] != "sabnzbd" {
			t.Errorf("empty: %d %s", r.status, r.raw)
		}
		if r := a.do("PUT", "/api/v1/settings/usenet-client", `{"url":"sab:8080"}`); r.status != 422 || !strings.Contains(r.raw, `"url"`) || !strings.Contains(r.raw, `"apiKey"`) {
			t.Errorf("validation: %d %s", r.status, r.raw)
		}
		sab := fakeSABnzbd(t, `{"no_dupes":0,"history_retention_option":"all"}`)
		r := a.do("PUT", "/api/v1/settings/usenet-client", `{"url":"`+sab.URL+`","apiKey":"sabkey"}`)
		if r.status != 200 || r.body["apiKeySet"] != true || strings.Contains(r.raw, "sabkey") {
			t.Fatalf("save: %d %s", r.status, r.raw)
		}
		if !a.downloads.UsenetOn() {
			t.Error("the download service doesn't see the Usenet client")
		}
		// The torrent client is a separate setting, still empty.
		if r := a.do("GET", "/api/v1/settings/download-client", ""); r.body["url"] != "" {
			t.Errorf("torrent client: %s", r.raw)
		}
		// Testing without the key uses the stored one.
		if r := a.do("POST", "/api/v1/settings/usenet-client/test", `{"url":"`+sab.URL+`"}`); r.status != 200 || r.body["version"] != "5.1.3" {
			t.Errorf("test: %d %s", r.status, r.raw)
		}
		if r := a.do("POST", "/api/v1/settings/usenet-client/test", `{"url":"`+sab.URL+`","apiKey":"wrong"}`); r.status != 422 || !strings.Contains(r.raw, "API key") {
			t.Errorf("wrong key: %d %s", r.status, r.raw)
		}
		// Settings that only hurt are warnings; ones that break imports fail
		// the test, saying what to change.
		dupes := fakeSABnzbd(t, `{"no_dupes":3,"history_retention_option":"all"}`)
		r = a.do("POST", "/api/v1/settings/usenet-client/test", `{"url":"`+dupes.URL+`","apiKey":"sabkey"}`)
		if w, _ := r.body["warnings"].([]any); r.status != 200 || len(w) != 1 || !strings.Contains(r.raw, "duplicate detection") {
			t.Errorf("duplicates: %d %s", r.status, r.raw)
		}
		forgets := fakeSABnzbd(t, `{"no_dupes":0,"history_retention_option":"all-delete"}`)
		if r := a.do("POST", "/api/v1/settings/usenet-client/test", `{"url":"`+forgets.URL+`","apiKey":"sabkey"}`); r.status != 422 || !strings.Contains(r.raw, "History Retention") {
			t.Errorf("history retention: %d %s", r.status, r.raw)
		}
		// Grabbing can be On with only a Usenet client.
		if r := a.do("PUT", "/api/v1/settings/grabbing", `{"mode":"on","tvMode":"on","queueLimit":0,"minSeeders":1}`); r.status != 200 {
			t.Errorf("grabbing on with only SABnzbd: %d %s", r.status, r.raw)
		}
		// Clearing the address removes it.
		if r := a.do("PUT", "/api/v1/settings/usenet-client", `{"url":""}`); r.status != 200 || r.body["apiKeySet"] != false || a.downloads.UsenetOn() {
			t.Errorf("clear: %d %s", r.status, r.raw)
		}
	})
}
