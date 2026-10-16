package api_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// Episode monitoring per series version: presets, seasons and episodes,
// shown as each episode's state.
func TestSeriesMonitoringAPI(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		a := newApp(t, db, "")
		a.setup()
		dir := t.TempDir()
		p := filepath.Join(dir, "Firefly (2002)/Season 01/Firefly (2002) - S01E01 - Serenity.mkv")
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := a.do("POST", "/api/v1/library/roots", `{"path":"`+dir+`","mediaType":"series","defaultProfileId":1}`)
		if r.status != 200 {
			t.Fatalf("add TV root: %d %s", r.status, r.raw)
		}
		if _, err := a.library.Scan(ctx, int64(r.body["id"].(float64)), nil); err != nil {
			t.Fatal(err)
		}
		var list []map[string]any
		a.get("/api/v1/series", &list)
		if len(list) != 1 {
			t.Fatalf("series: %v", list)
		}
		id := strconv.FormatInt(int64(list[0]["id"].(float64)), 10)
		base := "/api/v1/series/" + id

		type detail struct {
			SeasonList []struct {
				Number   int `json:"number"`
				Episodes []struct {
					Id     int64 `json:"id"`
					Number *int  `json:"number"`
				} `json:"episodes"`
			} `json:"seasonList"`
			Versions []struct {
				Id          int64  `json:"id"`
				Monitor     string `json:"monitor"`
				MonitorFrom string `json:"monitorFrom"`
				Episodes    []struct {
					EpisodeId int64  `json:"episodeId"`
					Monitored bool   `json:"monitored"`
					State     string `json:"state"`
				} `json:"episodes"`
			} `json:"versions"`
		}
		load := func() (detail, map[string]int) {
			t.Helper()
			var d detail
			a.get(base, &d)
			counts := map[string]int{}
			for _, e := range d.Versions[0].Episodes {
				counts[e.State]++
			}
			return d, counts
		}

		// Found by a scan: only episodes from today on are wanted, so
		// Firefly's (all aired in 2002) aren't.
		d, counts := load()
		v := d.Versions[0]
		today := time.Now().Format("2006-01-02")
		if v.Monitor != "future" || v.MonitorFrom != today {
			t.Errorf("preset = %q from %q", v.Monitor, v.MonitorFrom)
		}
		if counts["has-file"] != 1 || counts["wanted"] != 0 || counts["unmonitored"] == 0 {
			t.Errorf("states = %v", counts)
		}
		vbase := base + "/versions/" + strconv.FormatInt(v.Id, 10)

		// All: every episode without a file is wanted, except Specials,
		// which are only ever turned on by hand.
		if r := a.do("PATCH", vbase, `{"monitor":"all"}`); r.status != 204 {
			t.Fatalf("preset: %d %s", r.status, r.raw)
		}
		d, counts = load()
		specials := map[int64]bool{}
		for _, s := range d.SeasonList {
			for _, e := range s.Episodes {
				specials[e.Id] = s.Number == 0
			}
		}
		if d.Versions[0].Monitor != "all" || counts["wanted"] == 0 {
			t.Errorf("after all: %s, %v", d.Versions[0].Monitor, counts)
		}
		for _, e := range d.Versions[0].Episodes {
			if (e.State == "unmonitored") != specials[e.EpisodeId] {
				t.Errorf("episode %d (special %v) is %s", e.EpisodeId, specials[e.EpisodeId], e.State)
			}
		}

		// Season 1 off, then one episode back on.
		if r := a.do("POST", vbase+"/monitor", `{"seasons":[1],"monitored":false}`); r.status != 204 {
			t.Fatalf("season off: %d %s", r.status, r.raw)
		}
		_, counts = load()
		if counts["wanted"] != 0 {
			t.Errorf("after season off: %v", counts)
		}
		var ep int64
		for _, s := range d.SeasonList {
			if s.Number == 1 {
				ep = s.Episodes[2].Id
			}
		}
		if r := a.do("POST", vbase+"/monitor", `{"episodeIds":[`+strconv.FormatInt(ep, 10)+`],"monitored":true}`); r.status != 204 {
			t.Fatalf("episode on: %d %s", r.status, r.raw)
		}
		d, counts = load()
		if counts["wanted"] != 1 {
			t.Errorf("after one on: %v", counts)
		}
		for _, e := range d.Versions[0].Episodes {
			if e.EpisodeId == ep && (!e.Monitored || e.State != "wanted") {
				t.Errorf("episode = %+v", e)
			}
		}

		// A preset replaces those choices.
		if r := a.do("PATCH", vbase, `{"monitor":"none"}`); r.status != 204 {
			t.Fatalf("none: %d", r.status)
		}
		if d, counts = load(); d.Versions[0].Monitor != "none" || counts["wanted"] != 0 || counts["not-aired"] != 0 {
			t.Errorf("after none: %v", counts)
		}

		// Search now queues a task.
		if r := a.do("POST", vbase+"/search", ""); r.status != 200 || r.body["kind"] != "grab.search" {
			t.Errorf("search: %d %s", r.status, r.raw)
		}

		// Mistakes.
		for _, c := range []struct{ method, path, body string }{
			{"PATCH", vbase, `{}`},
			{"PATCH", vbase, `{"monitor":"some"}`},
			{"POST", vbase + "/monitor", `{"monitored":true}`},
			{"POST", vbase + "/monitor", `{"episodeIds":[999999],"monitored":true}`},
		} {
			if r := a.do(c.method, c.path, c.body); r.status != 422 {
				t.Errorf("%s %s %s: %d %s", c.method, c.path, c.body, r.status, r.raw)
			}
		}
		if r := a.do("POST", base+"/versions/999999/monitor", `{"seasons":[1],"monitored":true}`); r.status != 404 {
			t.Errorf("unknown version: %d", r.status)
		}
	})
}
