package cleanup

import (
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func f64(v float64) *float64 { return &v }
func intp(v int) *int        { return &v }

func TestSeeding(t *testing.T) {
	cfg := settings.DefaultCleanup() // ratio 1.0 or 24 hours
	imported := Download{Imported: true}
	cases := []struct {
		name string
		d    Download
		t    Torrent
		want Action
		why  string
	}{
		{"not yet", imported, Torrent{Done: true, Ratio: 0.4, SeedingTime: 3 * time.Hour}, Keep, ""},
		{"ratio goal", imported, Torrent{Done: true, Ratio: 1.02, SeedingTime: 3 * time.Hour}, RemoveSeeded, "ratio 1.02"},
		{"time goal", imported, Torrent{Done: true, Ratio: 0.2, SeedingTime: 25 * time.Hour}, RemoveSeeded, "25 hours"},
		// Private tracker: 72 hours minimum (and no ratio rule).
		{"tracker minimum first", Download{Imported: true, SeedTimeMinutes: intp(72 * 60)},
			Torrent{Done: true, Ratio: 3, SeedingTime: 30 * time.Hour}, Keep, ""},
		{"tracker minimum met", Download{Imported: true, SeedTimeMinutes: intp(72 * 60)},
			Torrent{Done: true, Ratio: 0.5, SeedingTime: 73 * time.Hour}, RemoveSeeded, "asks for 3 days"},
		// The tracker's ratio rule is enough on its own.
		{"tracker ratio", Download{Imported: true, SeedRatio: f64(1), SeedTimeMinutes: intp(72 * 60)},
			Torrent{Done: true, Ratio: 1.1, SeedingTime: 2 * time.Hour}, RemoveSeeded, "ratio 1 or 3 days"},
		{"deleted on the tracker", imported, Torrent{Done: true, Unregistered: true}, RemoveSeeded, "no longer has"},
	}
	for _, c := range cases {
		v, _ := Evaluate(c.d, c.t, Track{}, cfg, t0)
		if v.Action != c.want || !strings.Contains(v.Reason, c.why) {
			t.Errorf("%s: %+v", c.name, v)
		}
	}
	// No goal: as soon as the tracker's minimum is met.
	cfg.SeedRatio, cfg.SeedHours = 0, 0
	if v, _ := Evaluate(imported, Torrent{Done: true}, Track{}, cfg, t0); v.Action != RemoveSeeded {
		t.Errorf("no goal: %+v", v)
	}
}

// run feeds a download's checks through Evaluate, one per step.
func run(t *testing.T, cfg settings.Cleanup, steps []Torrent, every time.Duration) (Verdict, Track) {
	t.Helper()
	var tr Track
	var v Verdict
	now := t0
	for _, s := range steps {
		v, tr = Evaluate(Download{}, s, tr, cfg, now)
		if v.Action != Keep {
			return v, tr
		}
		now = now.Add(every)
	}
	return v, tr
}

func repeat(t Torrent, n int) []Torrent {
	out := make([]Torrent, n)
	for i := range out {
		out[i] = t
	}
	return out
}

func TestStalled(t *testing.T) {
	cfg := settings.DefaultCleanup() // 6 hours
	cfg.SlowKBps, cfg.SlowHours = 0, 0
	stalled := Torrent{State: torrent.Stalled, Left: 500}
	if v, _ := run(t, cfg, repeat(stalled, 6*12), 5*time.Minute); v.Action != Keep {
		t.Errorf("removed before 6 hours: %+v", v)
	}
	v, _ := run(t, cfg, repeat(stalled, 7*12+1), 5*time.Minute)
	if v.Rule != "stalled" || v.Action != RemoveStuck {
		t.Errorf("after 7 hours: %+v", v)
	}
	// Any progress resets the clock.
	var steps []Torrent
	for i := range 9 * 12 {
		steps = append(steps, Torrent{State: torrent.Downloading, Left: int64(10000 - i), Speed: 1 << 20})
	}
	if v, _ := run(t, cfg, steps, 5*time.Minute); v.Action != Keep {
		t.Errorf("a progressing download was removed: %+v", v)
	}
	// Queued or paused downloads aren't stuck.
	if v, _ := run(t, cfg, repeat(Torrent{State: torrent.Queued, Left: 500}, 12*12), 5*time.Minute); v.Action != Keep {
		t.Errorf("a queued download was removed: %+v", v)
	}
}

// A download paused from Activity → Queue makes no progress at no speed for
// as long as it stays paused. That must never count as stalled or slow,
// however long it lasts, with both rules switched on. The same goes for
// the other states where the client isn't trying to download: queued,
// checking its files and moving them.
func TestPausedIsNeverStalledOrSlow(t *testing.T) {
	cfg := settings.DefaultCleanup()
	if cfg.StalledHours == 0 || cfg.SlowHours == 0 || cfg.SlowKBps == 0 {
		t.Fatal("the test needs the stalled and slow rules on by default")
	}
	for _, state := range []torrent.State{torrent.Paused, torrent.Queued, torrent.Checking, torrent.Moving} {
		paused := Torrent{State: state, Left: 500}
		// Two weeks of checks every 5 minutes, far past 6 hours and 12 hours.
		if v, _ := run(t, cfg, repeat(paused, 14*24*12), 5*time.Minute); v.Action != Keep {
			t.Errorf("%s: a paused download was removed: %+v", state, v)
		}
		// It was going nowhere before being paused; pausing restarts the
		// clocks, so it gets the full time again after resuming.
		steps := repeat(Torrent{State: torrent.Stalled, Left: 500}, 5*12)
		steps = append(steps, repeat(paused, 24*12)...)
		steps = append(steps, repeat(Torrent{State: torrent.Downloading, Left: 500, Speed: 1 << 20}, 5*12)...)
		if v, _ := run(t, cfg, steps, 5*time.Minute); v.Action != Keep {
			t.Errorf("%s: a resumed download was removed: %+v", state, v)
		}
	}
}

func TestSlow(t *testing.T) {
	cfg := settings.DefaultCleanup() // under 50 KB/s for 12 hours
	cfg.StalledHours = 0
	var slow []Torrent
	for i := range 13 * 12 {
		slow = append(slow, Torrent{State: torrent.Downloading, Left: int64(1e9 - i), Speed: 10 * 1024})
	}
	v, _ := run(t, cfg, slow, 5*time.Minute)
	if v.Rule != "slow" || !strings.Contains(v.Reason, "Slower than 50 KB/s") {
		t.Errorf("slow: %+v", v)
	}
	// A burst of speed resets it.
	slow[100].Speed = 1 << 20
	if v, _ := run(t, cfg, slow, 5*time.Minute); v.Action != Keep {
		t.Errorf("reset by a fast check: %+v", v)
	}
}

func TestMetadataAndStrikes(t *testing.T) {
	cfg := settings.DefaultCleanup()
	v, _ := run(t, cfg, repeat(Torrent{State: torrent.Metadata}, 3*12), 5*time.Minute)
	if v.Rule != "metadata" {
		t.Errorf("metadata: %+v", v)
	}
	// Unregistered needs three checks in a row...
	gone := Torrent{State: torrent.Stalled, Left: 9, Unregistered: true}
	v, tr := run(t, cfg, repeat(gone, 2), time.Minute)
	if v.Action != Keep || tr.Strikes != 2 {
		t.Errorf("after 2 checks: %+v %+v", v, tr)
	}
	v, _ = run(t, cfg, repeat(gone, 3), time.Minute)
	if v.Rule != "unregistered" {
		t.Errorf("after 3 checks: %+v", v)
	}
	// ...and a clean check in between starts the count again.
	steps := []Torrent{gone, gone, {State: torrent.Stalled, Left: 9}, gone, gone}
	if v, _ := run(t, cfg, steps, time.Minute); v.Action != Keep {
		t.Errorf("strikes should reset: %+v", v)
	}
	// Turned off: never.
	cfg.Unregistered = false
	if v, _ := run(t, cfg, repeat(gone, 10), time.Minute); v.Action != Keep {
		t.Errorf("rule off: %+v", v)
	}
}

// Files the download client lost, or an error it reports, are removed after
// three checks in a row, and the reason names the client.
func TestMissingFilesNamesTheClient(t *testing.T) {
	cfg := settings.DefaultCleanup()
	cfg.MissingFiles = true
	for _, state := range []torrent.State{torrent.MissingFiles, torrent.Error} {
		for name, want := range map[string]string{
			"Transmission": "Transmission reports an error or missing files for it.",
			"qBittorrent":  "qBittorrent reports an error or missing files for it.",
			// The built-in client's name isn't a proper name: it needs its capital here.
			"the built-in client": "The built-in client reports an error or missing files for it.",
			"":                    "The download client reports an error or missing files for it.",
		} {
			v, _ := run(t, cfg, repeat(Torrent{State: state, ClientName: name, Left: 9}, 3), time.Minute)
			if v.Rule != "missing" || !strings.HasPrefix(v.Reason, want) {
				t.Errorf("%s, client %q: %+v", state, name, v)
			}
		}
	}
}

func TestLooksStuck(t *testing.T) {
	for state, want := range map[torrent.State]bool{
		torrent.Stalled: true, torrent.Metadata: true, torrent.Error: true, torrent.MissingFiles: true, torrent.SeedingStalled: true,
		torrent.Downloading: false, torrent.Queued: false, torrent.Paused: false, torrent.Checking: false, torrent.Moving: false,
		torrent.Seeding: false, torrent.SeedingPaused: false, torrent.Unknown: false, "": false,
	} {
		if got := LooksStuck(Torrent{State: state}); got != want {
			t.Errorf("LooksStuck(%q) = %v, want %v", state, got, want)
		}
	}
}

func TestImportFailedAndWaiting(t *testing.T) {
	cfg := settings.DefaultCleanup() // 3 days
	done := t0.Add(-4 * 24 * time.Hour)
	if v, _ := Evaluate(Download{ImportFail: true, CompletedAt: &done}, Torrent{Done: true}, Track{}, cfg, t0); v.Rule != "import-failed" {
		t.Errorf("old failed import: %+v", v)
	}
	recent := t0.Add(-time.Hour)
	if v, _ := Evaluate(Download{ImportFail: true, CompletedAt: &recent}, Torrent{Done: true}, Track{}, cfg, t0); v.Action != Keep {
		t.Errorf("recent failed import: %+v", v)
	}
	// Finished and waiting to import: left alone.
	if v, _ := Evaluate(Download{}, Torrent{Done: true, State: torrent.SeedingStalled}, Track{}, cfg, t0); v.Action != Keep {
		t.Errorf("finished download: %+v", v)
	}
}

func TestUnregisteredMessages(t *testing.T) {
	for msg, want := range map[string]bool{
		"Unregistered torrent":                     true,
		"Torrent not registered with this tracker": true,
		"torrent not found":                        true,
		"Trumped by a better release":              true,
		// What Transmission shows for a tracker's answer.
		"Tracker gave an error: Unregistered torrent":               true,
		"info_hash not found":                                       true,
		"Info hash not found in the database":                       true,
		"Connection timed out":                                      false,
		"Could not connect to tracker":                              false,
		"Tracker did not respond":                                   false,
		"Tracker gave HTTP response code 503 (Service Unavailable)": false,
		"Tracker is down for maintenance, try later":                false,
	} {
		if Unregistered([]string{msg}) != want {
			t.Errorf("%q: want %v", msg, want)
		}
	}
}

func TestWaiting(t *testing.T) {
	cfg := settings.DefaultCleanup()
	d := Download{Imported: true, SeedTimeMinutes: intp(72 * 60)}
	if w := Waiting(d, Torrent{Ratio: 2, SeedingTime: time.Hour}, cfg, time.Now()); w != "The tracker's minimum: 3 days." {
		t.Errorf("tracker: %q", w)
	}
	if w := Waiting(Download{Imported: true}, Torrent{Ratio: 0.1, SeedingTime: time.Hour}, cfg, time.Now()); w != "Your seeding goal: ratio 1 or 24 hours." {
		t.Errorf("goal: %q", w)
	}
	if w := Waiting(Download{Imported: true}, Torrent{Ratio: 1.5}, cfg, time.Now()); w != "" {
		t.Errorf("done: %q", w)
	}
}

// qBittorrent's queue can keep a finished torrent waiting to seed with its
// seeding clock stopped. The user's goal counts from when it finished;
// the tracker's minimum still counts real seeding time.
func TestSeedGoalCountsFromCompletion(t *testing.T) {
	cfg := settings.DefaultCleanup()
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	finished := now.Add(-25 * time.Hour)
	queued := Torrent{Done: true, State: torrent.Seeding, Ratio: 0, SeedingTime: 0}

	v, _ := Evaluate(Download{Imported: true, CompletedAt: &finished}, queued, Track{}, cfg, now)
	if v.Action != RemoveSeeded || !strings.Contains(v.Reason, "finished 25 hours ago (0 minutes of it seeding)") {
		t.Errorf("queued for 25 hours: %+v", v)
	}
	recent := now.Add(-2 * time.Hour)
	if v, _ := Evaluate(Download{Imported: true, CompletedAt: &recent}, queued, Track{}, cfg, now); v.Action != Keep {
		t.Errorf("finished 2 hours ago: %+v", v)
	}
	// A private tracker's 3 days are 3 days of seeding, however long ago
	// it finished.
	owed := Download{Imported: true, CompletedAt: &finished, SeedTimeMinutes: intp(72 * 60)}
	if v, _ := Evaluate(owed, Torrent{Done: true, Ratio: 0.1, SeedingTime: time.Hour}, Track{}, cfg, now.Add(100*time.Hour)); v.Action != Keep {
		t.Errorf("tracker minimum not met: %+v", v)
	}
}

func TestMakeRoom(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	at := func(h int) *time.Time { x := now.Add(-time.Duration(h) * time.Hour); return &x }
	gb := int64(1 << 30)
	items := []Seeding{
		{D: Download{Imported: true, CompletedAt: at(5)}, T: Torrent{Done: true, Ratio: 0.2}, Size: 30 * gb},
		{D: Download{Imported: true, CompletedAt: at(20)}, T: Torrent{Done: true, Ratio: 0.5}, Size: 10 * gb},
		// Owed to its tracker: never picked.
		{D: Download{Imported: true, CompletedAt: at(50), SeedTimeMinutes: intp(72 * 60)}, T: Torrent{Done: true, SeedingTime: time.Hour}, Size: 80 * gb},
		{D: Download{Imported: true, CompletedAt: at(10)}, T: Torrent{Done: true, Ratio: 0.1}, Size: 20 * gb},
	}
	if p, _ := MakeRoom(items, 150*gb, 100*gb, now); p != nil {
		t.Errorf("with room: picked %v", p)
	}
	// 75 GB free, 100 kept free: the longest finished first (20 h, then
	// 10 h) until 25 GB more is free.
	p, why := MakeRoom(items, 75*gb, 100*gb, now)
	if len(p) != 2 || p[0] != 1 || p[1] != 3 {
		t.Fatalf("picked %v", p)
	}
	if why[0].Rule != "space" || !strings.Contains(why[0].Reason, "had 75.0 GB free, below the 100.0 GB you keep free") {
		t.Errorf("reason = %+v", why[0])
	}
	// Even all of them can't make enough room: every one that may go, goes.
	if p, _ := MakeRoom(items, 0, 100*gb, now); len(p) != 3 {
		t.Errorf("out of space: picked %v", p)
	}
	if p, _ := MakeRoom(items, 0, 0, now); p != nil {
		t.Errorf("turned off: picked %v", p)
	}
}

func TestEvaluateUsenet(t *testing.T) {
	cfg := settings.DefaultCleanup()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	done := now.Add(-time.Hour)
	sab := func(state torrent.State, msg string) Torrent {
		return Torrent{State: state, ClientName: "SABnzbd", Message: msg}
	}
	for _, c := range []struct {
		name   string
		d      Download
		t      Torrent
		action Action
		rule   string
		reason string
	}{
		{"imported goes at once", Download{Imported: true, CompletedAt: &done}, sab(torrent.Completed, ""), RemoveSeeded, "usenet-imported", "nothing to seed"},
		{"failed in SABnzbd", Download{}, sab(torrent.Failed, "Aborted, cannot be completed - https://sabnzbd.org/not-complete"),
			RemoveStuck, "usenet-failed", "SABnzbd couldn't download it: Aborted, cannot be completed"},
		{"a duplicate isn't the release's fault", Download{}, sab(torrent.Failed, "Duplicate NZB"), Keep, "", ""},
		{"still downloading", Download{}, sab(torrent.Downloading, ""), Keep, "", ""},
		{"repairing", Download{}, sab(torrent.Repairing, ""), Keep, "", ""},
		{"finished, waiting to import", Download{}, sab(torrent.Completed, ""), Keep, "", ""},
		{"a fresh failed import waits", Download{ImportFail: true, CompletedAt: &done}, sab(torrent.Completed, ""), Keep, "", ""},
	} {
		v := EvaluateUsenet(c.d, c.t, cfg, now)
		if v.Action != c.action || v.Rule != c.rule || !strings.Contains(v.Reason, c.reason) {
			t.Errorf("%s: %+v", c.name, v)
		}
	}
	old := now.Add(-time.Duration(cfg.ImportFailedDays+1) * 24 * time.Hour)
	if cfg.ImportFailedDays > 0 {
		if v := EvaluateUsenet(Download{ImportFail: true, CompletedAt: &old}, sab(torrent.Completed, ""), cfg, now); v.Rule != "import-failed" {
			t.Errorf("an old failed import: %+v", v)
		}
	}
}
