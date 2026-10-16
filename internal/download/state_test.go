package download

import (
	"context"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/cleanup"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// Every state name qBittorrent 4.x and 5.x can report, and what Reelkeeper
// makes of it. A name that isn't here (a future version's) is Unknown.
var qbitStates = []struct {
	name string
	want torrent.State
}{
	{"error", torrent.Error},
	{"missingFiles", torrent.MissingFiles},
	{"uploading", torrent.Seeding},
	{"forcedUP", torrent.Seeding},
	{"queuedUP", torrent.Seeding},
	{"stalledUP", torrent.SeedingStalled},
	{"pausedUP", torrent.SeedingPaused},  // 4.x
	{"stoppedUP", torrent.SeedingPaused}, // 5
	{"checkingUP", torrent.Checking},
	{"allocating", torrent.Checking},
	{"downloading", torrent.Downloading},
	{"forcedDL", torrent.Downloading},
	{"stalledDL", torrent.Stalled},
	{"metaDL", torrent.Metadata},
	{"forcedMetaDL", torrent.Metadata},
	{"queuedDL", torrent.Queued},
	{"pausedDL", torrent.Paused},  // 4.x
	{"stoppedDL", torrent.Paused}, // 5
	{"checkingDL", torrent.Checking},
	{"checkingResumeData", torrent.Checking},
	{"moving", torrent.Moving},
	{"unknown", torrent.Unknown},
}

func TestQbitStateTranslation(t *testing.T) {
	for _, c := range qbitStates {
		if got := qbitState(c.name); got != c.want {
			t.Errorf("qbitState(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	// Names nobody has heard of, and no name at all, are Unknown.
	for _, name := range []string{"", "somethingNew", "Downloading", "STALLEDDL"} {
		if got := qbitState(name); got != torrent.Unknown {
			t.Errorf("qbitState(%q) = %q, want unknown", name, got)
		}
	}
}

// The state comes through the real parsing of /torrents/info.
func TestQbitTorrentsCarryNeutralStates(t *testing.T) {
	f := newFakeQbit(t)
	f.modern = true
	f.torrents = nil
	for i, c := range qbitStates {
		f.torrents = append(f.torrents, map[string]any{
			"hash": string(rune('a'+i)) + "0", "name": c.name, "category": "cat", "state": c.name, "progress": 0.5,
		})
	}
	q := &QBittorrent{URL: f.srv.URL, Username: "admin", Password: "secret"}
	ts, err := q.Torrents(context.Background(), "cat")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != len(qbitStates) {
		t.Fatalf("got %d torrents, want %d", len(ts), len(qbitStates))
	}
	for i, c := range qbitStates {
		if ts[i].Name != c.name || ts[i].State != c.want {
			t.Errorf("%s: state = %q, want %q", c.name, ts[i].State, c.want)
		}
	}
}

// behaviour is everything outside the client that used to depend on a
// qBittorrent state name.
type behaviour struct {
	waiting    bool // cleanup: queued, paused, checking or moving; not stuck, clocks reset
	missing    bool // cleanup: the "files missing or error" rule applies
	metadata   bool // cleanup: the "stuck getting details" rule applies
	looksStuck bool // cleanup: worth asking the trackers about
	paused     bool // Torrent.Paused (Queue's Resume button, Pause refused)
}

// oldBehaviour is how the code worked before Reelkeeper had its own state
// names: a copy of the old checks, fed qBittorrent's raw name. Kept as the
// yardstick the new behaviour is measured against.
func oldBehaviour(raw string) behaviour {
	oldWaiting := map[string]bool{
		"queuedDL": true, "pausedDL": true, "stoppedDL": true, "checkingDL": true, "checkingResumeData": true, "moving": true,
	}
	return behaviour{
		waiting:  oldWaiting[raw],
		missing:  raw == "missingFiles" || raw == "error",
		metadata: raw == "metaDL",
		looksStuck: func() bool {
			switch raw {
			case "stalledDL", "metaDL", "error", "missingFiles", "stalledUP":
				return true
			}
			return false
		}(),
		paused: raw == "pausedDL" || raw == "stoppedDL",
	}
}

// newBehaviour asks the real code, with the state qBittorrent's name
// becomes. Cleanup is driven through Evaluate, as the app does, with a
// download that is mid-way and has been seen with each problem long enough
// that one more look acts on it.
func newBehaviour(raw string) behaviour {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	cfg := settings.DefaultCleanup()
	state := qbitState(raw)
	tor := cleanup.Torrent{State: state, Left: 500}

	// Not stuck: a waiting torrent resets the progress clock to now.
	earlier := now.Add(-time.Minute)
	_, next := cleanup.Evaluate(cleanup.Download{}, tor, cleanup.Track{Left: 500, ProgressAt: &earlier}, cfg, now)
	waiting := next.ProgressAt != nil && next.ProgressAt.Equal(now)

	// Two strikes of "missing" already: a third look removes it.
	v, _ := cleanup.Evaluate(cleanup.Download{}, tor, cleanup.Track{Left: 500, ProgressAt: &earlier, Problem: "missing", Strikes: 2}, cfg, now)
	missing := v.Rule == "missing"

	// Waiting for details for longer than the limit.
	long := now.Add(-time.Duration(cfg.MetadataHours+1) * time.Hour)
	v, _ = cleanup.Evaluate(cleanup.Download{}, tor, cleanup.Track{MetaSince: &long}, cfg, now)
	metadata := v.Rule == "metadata"

	return behaviour{
		waiting: waiting, missing: missing, metadata: metadata,
		looksStuck: cleanup.LooksStuck(tor),
		paused:     Torrent{State: state}.Paused(),
	}
}

// The new code behaves as the old did for every qBittorrent state, except
// in the few places listed here. Each is a place where the old code was
// inconsistent with itself, and each is listed so a change can't slip in
// unnoticed: the test fails if an entry stops being a difference too.
var behaviourChanges = map[string]struct {
	why    string
	adjust func(*behaviour)
}{
	"allocating": {
		"qBittorrent reserving disk space for a new download makes no progress either; it is waiting, like checking files",
		func(b *behaviour) { b.waiting = true },
	},
	"checkingUP": {
		"checking files is one state now, and checkingDL, which was already waiting, is the same thing",
		func(b *behaviour) { b.waiting = true },
	},
	"forcedMetaDL": {
		"a forced start still waits for details; it was missing from the metadata rule and from LooksStuck while every other place treated it as metaDL",
		func(b *behaviour) { b.metadata, b.looksStuck = true, true },
	},
}

func TestNeutralStatesKeepTheOldBehaviour(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range qbitStates {
		want := oldBehaviour(c.name)
		if change, ok := behaviourChanges[c.name]; ok {
			seen[c.name] = true
			before := want
			change.adjust(&want)
			if before == want {
				t.Errorf("%s: listed as a change, but it changes nothing (%s)", c.name, change.why)
			}
		}
		if got := newBehaviour(c.name); got != want {
			t.Errorf("%s: behaviour = %+v, want %+v", c.name, got, want)
		}
	}
	for name := range behaviourChanges {
		if !seen[name] {
			t.Errorf("%s is listed as a change, but isn't a qBittorrent state", name)
		}
	}
}
