package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInjectBase(t *testing.T) {
	in := []byte(`<head><!--reelkeeper:base--><title>x</title>`)
	if got := string(injectBase(in, "")); !strings.Contains(got, `<base href="/">`) {
		t.Errorf("root: %s", got)
	}
	if got := string(injectBase(in, "/reelkeeper")); !strings.Contains(got, `<base href="/reelkeeper/">`) {
		t.Errorf("url base: %s", got)
	}
	if got := string(injectBase(in, `/"><script>`)); strings.Contains(got, "<script>") {
		t.Errorf("URL base must be escaped: %s", got)
	}
}

// Unknown API addresses must not get the web page (Seerr, testing a
// Radarr connection, read the page as a broken answer).
func TestAPIAddressesAreNotPages(t *testing.T) {
	h := Handler("/reelkeeper")
	for path, want := range map[string]int{
		"/reelkeeper/api/v3/system/status":          http.StatusNotFound,
		"/reelkeeper/compat/radarr/hd/api/v3/movie": http.StatusNotFound,
		"/reelkeeper/api":                           http.StatusNotFound,
		"/reelkeeper/movies":                        http.StatusOK,
		"/reelkeeper/apiary":                        http.StatusOK, // a page whose name starts with "api"
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", path, rec.Code, want)
		}
		if want == http.StatusNotFound && !strings.Contains(rec.Body.String(), "Not Found") {
			t.Errorf("%s: body %q", path, rec.Body.String())
		}
	}
}
