package indexer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// A Prowlarr answering like a real one: a definition-based tracker behind the
// FlareSolverr proxy, Knaben, a Torznab feed, and something Reelkeeper lacks.
func fakeProwlarr(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "pk" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/indexer":
			fmt.Fprint(w, `[
{"id":1,"name":"Fake","implementation":"Cardigann","definitionName":"faketracker","enable":true,"priority":10,"protocol":"torrent","tags":[1],
 "fields":[{"name":"definitionFile","value":"faketracker"},{"name":"baseUrl","value":"https://fake.example/"},
  {"name":"username","value":"dan"},{"name":"password","value":"********","privacy":"password"},
  {"name":"sort","value":1},{"name":"freeleech","value":true},{"name":"info_x","value":"text"},
  {"name":"torrentBaseSettings.seedRatio","value":1.5},{"name":"torrentBaseSettings.seedTime","value":2880},
  {"name":"baseSettings.queryLimit","value":10},{"name":"baseSettings.limitsUnit","value":1}]},
{"id":2,"name":"Knaben","implementation":"Knaben","enable":true,"priority":25,"protocol":"torrent","tags":[1],"fields":[{"name":"baseUrl","value":"https://knaben.org/"}]},
{"id":3,"name":"Feed","implementation":"Torznab","enable":false,"priority":30,"protocol":"torrent","tags":[],
 "fields":[{"name":"baseUrl","value":"https://feed.example"},{"name":"apiPath","value":"/api"},{"name":"apiKey","value":"secretkey","privacy":"apiKey"}]},
{"id":4,"name":"Special","implementation":"PassThePopcorn","enable":true,"priority":25,"protocol":"torrent","tags":[],"fields":[]}
]`)
		case "/api/v1/indexerProxy":
			fmt.Fprint(w, `[{"implementation":"FlareSolverr","tags":[1],"fields":[{"name":"host","value":"http://trawl:8191/"},{"name":"requestTimeout","value":60}]}]`)
		}
	}))
}

func TestProwlarrImport(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		p := fakeProwlarr(t)
		defer p.Close()

		if _, err := f.svc.PreviewProwlarr(ctx, p.URL, "wrong"); err == nil || !strings.Contains(err.Error(), "refused the API key") {
			t.Errorf("wrong key: %v", err)
		}
		prev, err := f.svc.PreviewProwlarr(ctx, p.URL, "pk")
		if err != nil {
			t.Fatal(err)
		}
		if prev.SolverURL != "http://trawl:8191" || prev.SolverTimeout != 60 || len(prev.Indexers) != 4 {
			t.Fatalf("preview = %+v", prev)
		}
		fake := prev.Indexers[0]
		in := fake.Input
		// The select's position (1 in sorted keys added/size) is "size".
		if in.Kind != KindCardigann || !in.UseSolver || in.Settings["sort"] != "size" || in.Settings["freeleech"] != "true" ||
			in.Settings["username"] != "dan" || in.BaseURL != "" || *in.SeedRatio != 1.5 || *in.SeedTimeMinutes != 2880 || *in.QueryLimit != 240 {
			t.Errorf("fake = %+v", in)
		}
		if len(fake.MissingSecrets) != 1 || fake.MissingSecrets[0] != "Password" || len(fake.Notes) != 1 {
			t.Errorf("fake notes = %v %v", fake.MissingSecrets, fake.Notes)
		}
		if k := prev.Indexers[1].Input; k.Kind != KindKnaben || !k.UseSolver || k.BaseURL != "" {
			t.Errorf("knaben = %+v", k)
		}
		if fd := prev.Indexers[2].Input; fd.Kind != KindTorznab || fd.BaseURL != "https://feed.example/api" || fd.Settings["apiKey"] != "secretkey" || fd.Enabled {
			t.Errorf("feed = %+v", fd)
		}
		if sp := prev.Indexers[3]; sp.Supported || !strings.Contains(sp.Reason, "PassThePopcorn") {
			t.Errorf("special = %+v", sp)
		}

		created, skipped, err := f.svc.ImportProwlarr(ctx, p.URL, "pk", []int{1, 2, 3, 4})
		if err != nil {
			t.Fatal(err)
		}
		if len(created) != 3 || skipped[4] == "" {
			t.Errorf("created %d, skipped %v", len(created), skipped)
		}
		// Importing again skips what's there.
		created, skipped, _ = f.svc.ImportProwlarr(ctx, p.URL, "pk", []int{1})
		if len(created) != 0 || !strings.Contains(skipped[1], "already") {
			t.Errorf("again: %d created, %v", len(created), skipped)
		}

		var verrs validation.Errors
		if _, err := f.svc.PreviewProwlarr(ctx, "prowlarr", ""); !errors.As(err, &verrs) || verrs["url"] == "" || verrs["apiKey"] == "" {
			t.Errorf("bad input: %v", err)
		}
	})
}
