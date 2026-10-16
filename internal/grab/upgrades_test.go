package grab

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// A movie with a file and upgrades off is left alone everywhere: the
// scheduled pass and RSS don't load it, a search rejects every release
// with the reason (History shows it like any other rejection), and Wanted
// doesn't list it. A movie with no file is still wanted, whatever the
// switch says.
func TestUpgradesOff(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db) // Heat has a WEB-DL file, Ronin has none
		heat, ronin := f.lib.versions[0].Version.ID, f.lib.versions[1].Version.ID
		f.lib.versions[0].Version.Upgrades = false
		f.lib.versions[1].Version.Upgrades = false

		// load(onlyWanted) skips file + upgrades off, and keeps no file + upgrades off.
		ws, err := f.svc.load(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(ws) != 1 || ws[0].v.Version.ID != ronin {
			t.Errorf("load(onlyWanted) = %d versions, want only Ronin", len(ws))
		}
		// A search for one version loads it whatever the switch says.
		if ws, err = f.svc.load(ctx, false, heat); err != nil || len(ws) != 1 {
			t.Fatalf("load(false, heat) = %d, %v", len(ws), err)
		}

		// decide rejects every candidate, even a clear upgrade.
		ds, err := f.svc.decide(ctx, ws[0], []indexer.Release{
			rel("Heat.1995.1080p.BluRay.x264-AMIABLE", 40),
			rel("Heat.1995.2160p.BluRay.REMUX.HEVC-GRP", 40),
			rel("Heat.1995.720p.HDTV.x264-GRP", 40),
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range ds {
			if d.d.Accepted || !slices.Contains(d.d.Rejections, upgradesOffReason) {
				t.Errorf("%s: accepted = %v, rejections = %v", d.rel.Title, d.d.Accepted, d.d.Rejections)
			}
		}

		// "Search now" on Heat: nothing acceptable, History carries the reason.
		f.idx.releases = []indexer.Release{rel("Heat.1995.1080p.BluRay.x264-AMIABLE", 40)}
		msg, err := f.svc.SearchVersion(ctx, heat, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(msg, "0 acceptable") || !strings.Contains(msg, upgradesOffReason) {
			t.Errorf("message = %q", msg)
		}
		if len(f.events(t, EventWouldGrab, EventGrabbed)) != 0 {
			t.Error("grabbed or would have grabbed a movie with upgrades off")
		}
		searched := f.events(t, EventSearched)
		if len(searched) != 1 {
			t.Fatalf("searched events = %d", len(searched))
		}
		var data EventData
		_ = json.Unmarshal([]byte(searched[0].Data), &data)
		if len(data.Releases) != 1 || data.Releases[0].Accepted || !slices.Contains(data.Releases[0].Rejections, upgradesOffReason) {
			t.Errorf("recorded releases = %+v", data.Releases)
		}

		// Ronin has no file: unaffected, searched and grabbed as usual.
		f.idx.releases = []indexer.Release{rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40)}
		if msg, err = f.svc.SearchVersion(ctx, ronin, TriggerManual); err != nil || !strings.Contains(msg, "Would grab") {
			t.Errorf("Ronin: %q, %v", msg, err)
		}

		// Wanted lists Ronin as missing and leaves Heat out of both lists.
		l := wantedOf(t, f)
		if got := titles(l.Missing); len(got) != 1 || got[0] != "Ronin" {
			t.Errorf("missing = %v", got)
		}
		if l.has("Heat") || len(l.Upgrades) != 0 {
			t.Errorf("Heat with upgrades off is listed: %v", titles(l.Upgrades))
		}
		ids, err := f.svc.WantedTitleIDs(ctx)
		if err != nil || ids[f.lib.versions[0].Title.ID] || !ids[f.lib.versions[1].Title.ID] {
			t.Errorf("WantedTitleIDs = %v, %v", ids, err)
		}

		// Turned back on, Heat is an upgrade candidate again.
		f.lib.versions[0].Version.Upgrades = true
		if l := wantedOf(t, f); !l.has("Heat") {
			t.Error("Heat with upgrades on should be wanted")
		}
	})
}
