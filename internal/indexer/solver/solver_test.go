package solver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeSolver(t *testing.T, got *command) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"msg":"TRAWL is ready!","version":"1.7.0"}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(got)
		if strings.Contains(got.URL, "blocked") {
			fmt.Fprint(w, `{"status":"error","message":"Challenge not solved"}`)
			return
		}
		page := `<html><body><table><tr><td>row</td></tr></table></body></html>`
		if strings.Contains(got.URL, "api") {
			page = `<html><head></head><body><pre style="word-wrap: break-word;">{"data":[1,2]}</pre></body></html>`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "solution": map[string]any{
			"url": got.URL + "#final", "status": 200, "headers": map[string]string{"Content-Type": "text/html"},
			"response": page, "cookies": []map[string]string{{"name": "cf_clearance", "value": "x", "domain": ".site"}},
		}})
	}))
}

func TestSolver(t *testing.T) {
	var got command
	srv := fakeSolver(t, &got)
	defer srv.Close()
	c := &Client{URL: srv.URL}
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodPost, "https://site/login.php", strings.NewReader("user=dan&pass=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, cookies, err := c.Do(req, []*http.Cookie{{Name: "sess", Value: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if got.Cmd != "request.post" || got.PostData != "user=dan&pass=x" || len(got.Cookies) != 1 || got.MaxTimeout != 60000 {
		t.Errorf("command = %+v", got)
	}
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<td>row</td>") || resp.Request.URL.Fragment != "final" ||
		len(cookies) != 1 || cookies[0].Name != "cf_clearance" {
		t.Errorf("response = %d %s %v %v", resp.StatusCode, body, resp.Request.URL, cookies)
	}

	// A JSON API comes back without the browser's <pre> wrapper.
	req, _ = http.NewRequest(http.MethodGet, "https://site/api/search", nil)
	resp, _, err = c.Do(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(resp.Body); string(body) != `{"data":[1,2]}` {
		t.Errorf("json = %s", body)
	}

	// The solver's own failures, and requests it can't make.
	req, _ = http.NewRequest(http.MethodGet, "https://site/blocked", nil)
	if _, _, err := c.Do(req, nil); err == nil || !strings.Contains(err.Error(), "Challenge not solved") {
		t.Errorf("blocked: %v", err)
	}
	req, _ = http.NewRequest(http.MethodPost, "https://site/api", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if _, _, err := c.Do(req, nil); err == nil {
		t.Error("a JSON POST should be refused")
	}
	if err := (&Client{URL: "http://127.0.0.1:1"}).Check(context.Background()); err == nil {
		t.Error("an unreachable solver should fail the check")
	}
}
