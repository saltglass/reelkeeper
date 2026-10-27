package grab

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// wantedOf returns the Wanted lists, failing the test on an error.
func wantedOf(t *testing.T, f *fixture) *WantedList {
	t.Helper()
	l, err := f.svc.Wanted(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// item finds the item for a title (and season, for episode groups).
func (l *WantedList) item(t *testing.T, title string, season ...int) WantedItem {
	t.Helper()
	for _, list := range [][]WantedItem{l.Missing, l.Upgrades} {
		for _, it := range list {
			if it.Title == title && (len(season) == 0 || (it.Season != nil && *it.Season == season[0])) {
				return it
			}
		}
	}
	t.Fatalf("%s %v is not wanted: missing=%v upgrades=%v", title, season, titles(l.Missing), titles(l.Upgrades))
	return WantedItem{}
}

func (l *WantedList) has(title string) bool {
	for _, list := range [][]WantedItem{l.Missing, l.Upgrades} {
		for _, it := range list {
			if it.Title == title {
				return true
			}
		}
	}
	return false
}

func titles(items []WantedItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Title)
	}
	return out
}

// With a simple profile (the fixture's is the HD preset), a file at the
// profile's "upgrade until" quality isn't wanted, even with avoided features:
// no movie is searched for, and downloaded, again and again. A file with a
// blocked format is still wanted, with no score target to show.
func TestWantedStopsAtTheQualityWithASimpleProfile(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		file := func(name string) {
			f.lib.versions[0].Files = []library.MediaFile{{VersionID: f.lib.versions[0].Version.ID, Path: "Heat/" + name,
				Size: 8 << 30, Resolution: 1080, Source: "BluRay"}}
		}
		for _, name := range []string{"Heat.1995.1080p.BluRay.x264.mkv", "Heat.1995.1080p.BluRay.x265-GRP.mkv"} {
			file(name)
			if l := wantedOf(t, f); l.has("Heat") {
				t.Errorf("%s is wanted as an upgrade: %+v", name, l.Upgrades)
			}
		}
		file("Heat.1995.3D.1080p.BluRay.x264-GRP.mkv")
		up := wantedOf(t, f).item(t, "Heat")
		if up.Score == nil || up.TargetScore != nil || !slices.ContainsFunc(up.Formats, func(m decision.FormatMatch) bool { return m.Score <= decision.BlockScore }) {
			t.Errorf("a blocked file = %+v", up)
		}
	})
}

// Missing movies, movies below their profile's target, and movies that
// need nothing: only the first two are wanted, and unreleased ones aren't.
func TestWantedMissingAndUpgrades(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)                        // Heat has a WEB-DL file, Ronin has none
		untilScore := f.lib.profile.UpgradeUntilScore // put back after each change
		l := wantedOf(t, f)
		if got := titles(l.Missing); len(got) != 1 || got[0] != "Ronin" {
			t.Errorf("missing = %v", got)
		}
		up := l.item(t, "Heat")
		if len(l.Upgrades) != 1 || up.Kind != WantedMovie || up.Quality != "WEBDL-1080p" || up.Target != "Bluray-1080p" || up.Score != nil {
			t.Errorf("upgrades = %+v", l.Upgrades)
		}
		if l.MissingMovies != 1 || l.MissingEpisodes != 0 {
			t.Errorf("counts = %d movies, %d episodes", l.MissingMovies, l.MissingEpisodes)
		}
		if m := l.item(t, "Ronin"); m.Profile != "HD" || m.Year != 1998 || m.Kind != WantedMovie || m.Date != "" {
			t.Errorf("Ronin = %+v", m)
		}

		// Meeting the cutoff leaves nothing to look for.
		f.lib.versions[0].Files = []library.MediaFile{{VersionID: f.lib.versions[0].Version.ID, Path: "Heat/Heat.1995.1080p.BluRay.x264-GRP.mkv",
			Size: 8 << 30, Resolution: 1080, Source: "BluRay"}}
		if l := wantedOf(t, f); l.has("Heat") {
			t.Errorf("Heat at its target is wanted: %+v", l.Upgrades)
		}

		// At the target quality but below the "upgrade until" score: wanted,
		// with the score gap and the scored formats the file has.
		f.lib.profile.Formats = []decision.CustomFormat{
			{Name: "GRP", Specs: []decision.Spec{{Name: "g", Kind: decision.KindReleaseTitle, Fields: decision.Fields{"value": "-GRP"}}}},
			{Name: "x264", Specs: []decision.Spec{{Name: "c", Kind: decision.KindReleaseTitle, Fields: decision.Fields{"value": "x264"}}}},
		}
		for i := range f.lib.profile.Formats {
			if err := f.lib.profile.Formats[i].Compile(); err != nil {
				t.Fatal(err)
			}
		}
		f.lib.profile.Scores = map[string]int{"GRP": 50}
		f.lib.profile.UpgradeUntilScore = 100
		up = wantedOf(t, f).item(t, "Heat")
		if up.Score == nil || *up.Score != 50 || up.TargetScore == nil || *up.TargetScore != 100 || up.Quality != "Bluray-1080p" ||
			len(up.Formats) != 1 || up.Formats[0].Name != "GRP" { // x264 scores 0, so it isn't listed
			t.Errorf("score-only upgrade = %+v", up)
		}
		f.lib.profile.UpgradeUntilScore = 50
		if l := wantedOf(t, f); l.has("Heat") {
			t.Errorf("Heat at its target score is wanted: %+v", l.Upgrades)
		}
		f.lib.profile.Formats, f.lib.profile.Scores, f.lib.profile.UpgradeUntilScore = nil, nil, untilScore

		// Better than anything the profile allows: left alone, not listed as
		// an "upgrade" to a lower quality (2026-10-13).
		f.lib.versions[0].Files = []library.MediaFile{{VersionID: f.lib.versions[0].Version.ID, Path: "Heat/Heat.1995.2160p.WEB-DL.H.265-GRP.mkv",
			Size: 15 << 30, Resolution: 2160, Source: "WEB-DL"}}
		f.lib.profile.UpgradeUntilScore = 100
		if l := wantedOf(t, f); l.has("Heat") {
			t.Errorf("a 2160p file under an HD profile is wanted: %+v", l.Upgrades)
		}
		f.lib.profile.UpgradeUntilScore = untilScore

		// So does a profile with upgrades off.
		f.lib.versions[0].Files = []library.MediaFile{{VersionID: f.lib.versions[0].Version.ID, Path: "Heat/Heat.1995.1080p.WEB-DL.H.264-GRP.mkv",
			Size: 4 << 30, Resolution: 1080, Source: "WEB-DL"}}
		if !wantedOf(t, f).has("Heat") {
			t.Fatal("Heat should be an upgrade again")
		}
		f.lib.profile.UpgradesAllowed = false
		if l := wantedOf(t, f); l.has("Heat") || len(l.Upgrades) != 0 {
			t.Errorf("upgrades off, but %+v", l.Upgrades)
		}

		// Not out yet: not listed.
		f.lib.versions[1].Title.Status = "In Production"
		if l := wantedOf(t, f); l.has("Ronin") {
			t.Error("an unreleased movie is wanted")
		}
		// A digital release that has passed makes it available, and gives the date.
		f.lib.versions[1].Title.DigitalRelease = "2026-09-01"
		f.lib.versions[1].Title.PhysicalRelease = "2026-10-01"
		if m := wantedOf(t, f).item(t, "Ronin"); m.Date != "2026-09-01" {
			t.Errorf("date = %q", m.Date)
		}
	})
}

// Episodes are grouped by series version and season, newest first; episodes
// that haven't aired aren't listed.
func TestWantedEpisodeGroups(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		addSeries(t, db, f, "Slow Horses", "standard", map[string]string{
			"S05E01": "2025-09-24", "S05E02": "2025-10-01",
			"S06E01": "2026-10-01", "S06E02": "2026-10-08", "S06E03": "2026-10-16", // S06E03 hasn't aired
		}, "S05E01", "S05E02", "S06E01", "S06E02")
		l := wantedOf(t, f)

		s6 := l.item(t, "Slow Horses", 6)
		if s6.Kind != WantedEpisodes || len(s6.Episodes) != 2 || s6.Date != "2026-10-08" || *s6.Season != 6 {
			t.Errorf("season 6 = %+v", s6)
		}
		if e := s6.Episodes[1]; e.Season != 6 || e.Number != 2 || e.AirDate != "2026-10-08" {
			t.Errorf("episode = %+v", e)
		}
		s5 := l.item(t, "Slow Horses", 5)
		if len(s5.Episodes) != 2 || s5.Date != "2025-10-01" || s5.VersionID != s6.VersionID {
			t.Errorf("season 5 = %+v", s5)
		}
		if l.MissingEpisodes != 4 || l.MissingMovies != 1 {
			t.Errorf("counts = %d episodes, %d movies", l.MissingEpisodes, l.MissingMovies)
		}
		// Newest first; Ronin has no date at all, so it comes last.
		if got := []int{*l.Missing[0].Season, *l.Missing[1].Season}; got[0] != 6 || got[1] != 5 || l.Missing[2].Title != "Ronin" {
			t.Errorf("order = %v", titles(l.Missing))
		}
	})
}

// Each state, and the note a dry run adds.
func TestWantedStates(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		ronin := f.lib.versions[1].Version.ID
		status := func() WantedItem { return wantedOf(t, f).item(t, "Ronin") }

		// Dry run is the default: never searched.
		it := status()
		if it.State != StateNever || it.Status != neverText+dryRunNote || it.NextSearchAt != nil || it.LastSearchedAt != nil {
			t.Errorf("never: %+v", it)
		}

		// Searched yesterday: the next search is due in two days.
		yesterday := f.now.Add(-24 * time.Hour)
		f.lib.searched[ronin] = yesterday
		it = status()
		if it.State != StateWaiting || it.Status != "Next search in about 2 days."+dryRunNote ||
			it.NextSearchAt == nil || !it.NextSearchAt.Equal(yesterday.Add(searchEvery)) ||
			it.LastSearchedAt == nil || !it.LastSearchedAt.Equal(yesterday) {
			t.Errorf("waiting: %+v", it)
		}
		f.lib.searched[ronin] = f.now.Add(-searchEvery + 5*time.Hour)
		if it = status(); it.Status != "Next search in about 5 hours."+dryRunNote {
			t.Errorf("hours: %q", it.Status)
		}
		f.lib.searched[ronin] = f.now.Add(-searchEvery + 10*time.Minute)
		if it = status(); it.Status != "Next search in under an hour."+dryRunNote {
			t.Errorf("minutes: %q", it.Status)
		}
		f.lib.searched[ronin] = f.now.Add(-searchEvery - time.Hour)
		if it = status(); it.State != StateWaiting || it.Status != dueNowText+dryRunNote || it.NextSearchAt.After(f.now) {
			t.Errorf("due: %+v", it)
		}

		// On has no dry-run note.
		f.mode = settings.Grabbing{Mode: settings.GrabOn, TVMode: settings.GrabOn, MinSeeders: 1}
		if it = status(); it.Status != dueNowText {
			t.Errorf("on: %q", it.Status)
		}

		// The queue full (only while On).
		queue := &fakeQueue{size: 1}
		f.svc.SetDownloads(&fakeGrabber{}, queue)
		f.mode.QueueLimit = 2
		if it = status(); it.State == StateCapped {
			t.Errorf("below the limit: %+v", it)
		}
		queue.size = 2
		if it = status(); it.State != StateCapped || it.Status != cappedText {
			t.Errorf("capped: %+v", it)
		}
		if strings.Contains(it.Status, "midnight") || !strings.Contains(it.Status, "queue is full") {
			t.Errorf("capped text = %q", it.Status)
		}
		f.mode.Mode = settings.GrabDryRun // dry run has no limit
		if it = status(); it.State != StateWaiting {
			t.Errorf("dry run is not capped: %+v", it)
		}

		// Grabbing off beats the limit.
		f.mode.Mode = settings.GrabOff
		if it = status(); it.State != StateOff || !strings.Contains(it.Status, "Grabbing is off") {
			t.Errorf("off: %+v", it)
		}

		// Downloading beats everything.
		f.svc.SetDownloads(&fakeGrabber{}, &fakeQueue{active: map[int64]bool{ronin: true}})
		if it = status(); it.State != StateDownloading || it.Status != downloadingText {
			t.Errorf("downloading: %+v", it)
		}
	})
}

func TestWantedEpisodeStates(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		f.mode.TVMode = settings.GrabOn
		sv := addSeries(t, db, f, "The Expanse", "standard", map[string]string{
			"S06E01": "2026-10-01", "S06E02": "2026-10-08", "S06E03": "2026-10-09", // E03 airs today
		}, "S06E01", "S06E02", "S06E03")
		status := func() WantedItem { return wantedOf(t, f).item(t, "The Expanse", 6) }

		if it := status(); it.State != StateNever || it.Status != neverText {
			t.Errorf("never: %+v", it)
		}
		// One episode searched two days ago, the other never: due at once.
		f.lib.epSearched = map[int64]map[int64]time.Time{sv.Version.ID: {epID(sv, "S06E01"): f.now.Add(-48 * time.Hour)}}
		if it := status(); it.State != StateWaiting || it.Status != dueNowText || !it.LastSearchedAt.Equal(f.now.Add(-48*time.Hour)) {
			t.Errorf("mixed: %+v", it)
		}
		// Both searched: due when the older one is.
		f.lib.epSearched[sv.Version.ID][epID(sv, "S06E02")] = f.now.Add(-24 * time.Hour)
		it := status()
		// The older one is 48 hours old, so it's due in a day.
		if it.State != StateWaiting || it.Status != "Next search in about a day." {
			t.Errorf("both searched: %+v", it)
		}
		if want := f.now.Add(-48 * time.Hour).Add(searchEvery); it.NextSearchAt == nil || !it.NextSearchAt.Equal(want) {
			t.Errorf("next = %v, want %v", it.NextSearchAt, want)
		}
		if !it.LastSearchedAt.Equal(f.now.Add(-24 * time.Hour)) {
			t.Errorf("last = %v", it.LastSearchedAt)
		}

		// Downloading: any episode of the group in the queue.
		f.svc.SetDownloads(&fakeGrabber{}, &fakeQueue{activeEps: map[int64]map[int64]bool{sv.Version.ID: {epID(sv, "S06E02"): true}}})
		if it := status(); it.State != StateDownloading {
			t.Errorf("downloading: %+v", it)
		}
		f.svc.SetDownloads(&fakeGrabber{}, &fakeQueue{})

		f.mode.TVMode = settings.GrabOff
		if it := status(); it.State != StateOff {
			t.Errorf("off: %+v", it)
		}
		// Movies are unaffected by TV's mode.
		if it := wantedOf(t, f).item(t, "Ronin"); it.State != StateNever {
			t.Errorf("movie: %+v", it)
		}
	})
}

// Episodes that aired today aren't searched until tomorrow, and the page
// says so instead of promising a search.
func TestWantedAiredToday(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		addSeries(t, db, f, "The Expanse", "standard", map[string]string{"S06E03": "2026-10-09"}, "S06E03")
		it := wantedOf(t, f).item(t, "The Expanse", 6)
		if it.State != StateNever || it.Status != airedTodayText+dryRunNote {
			t.Errorf("aired today: %+v", it)
		}
	})
}

// The last search is the latest "searched" event, with its own message.
func TestWantedLastSearchMovie(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		ronin := f.lib.versions[1].Version.ID
		heat := f.lib.versions[0].Version.ID
		if it := wantedOf(t, f).item(t, "Ronin"); it.LastSearchMessage != "" || it.LastSearchedAt != nil {
			t.Errorf("before any search: %+v", it)
		}

		f.idx.releases = []indexer.Release{rel("Ronin.1998.720p.BluRay.x264-GRP", 5), rel("Ronin.1998.720p.WEB-DL.x264-GRP", 5)}
		first, err := f.svc.SearchVersion(ctx, ronin, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(time.Hour)
		f.idx.releases = []indexer.Release{rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40)}
		second, err := f.svc.SearchVersion(ctx, ronin, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if first == second {
			t.Fatal("the two searches should say different things")
		}
		// A search for another version, and a non-search event, don't count.
		if _, err := f.svc.SearchVersion(ctx, heat, TriggerManual); err != nil {
			t.Fatal(err)
		}

		var latest Event
		evs, err := f.hist.List(ctx, ListOptions{Events: []string{EventSearched}, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if *e.VersionID == ronin {
				latest = e
				break
			}
		}
		it := wantedOf(t, f).item(t, "Ronin")
		if it.LastSearchMessage != latest.Message || !strings.HasPrefix(it.LastSearchMessage, "Searched for Ronin (1998), HD: 1 release, 1 acceptable") {
			t.Errorf("message = %q, History says %q", it.LastSearchMessage, latest.Message)
		}
		if it.LastSearchedAt == nil || !it.LastSearchedAt.Equal(latest.CreatedAt) {
			t.Errorf("last searched = %v, want %v", it.LastSearchedAt, latest.CreatedAt)
		}
		// Upgrades get theirs too.
		if up := wantedOf(t, f).item(t, "Heat"); !strings.HasPrefix(up.LastSearchMessage, "Searched for Heat (1995)") {
			t.Errorf("upgrade message = %q", up.LastSearchMessage)
		}
		// Without the History read (the sidebar's count), there's no message.
		counts, err := f.svc.Wanted(ctx, false)
		if err != nil || counts.item(t, "Ronin").LastSearchMessage != "" || counts.MissingMovies != 1 {
			t.Errorf("counts only: %+v, %v", counts, err)
		}
	})
}

// For TV, the latest search whose episodes overlap the group's.
func TestWantedLastSearchEpisodes(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		sv := addSeries(t, db, f, "Slow Horses", "standard", map[string]string{
			"S05E01": "2025-09-24", "S06E01": "2026-10-01", "S06E02": "2026-10-02",
		}, "S05E01", "S06E01", "S06E02")
		other := addSeries(t, db, f, "Andor", "standard", map[string]string{"S02E01": "2026-10-01"}, "S02E01")
		f.idx.releases = []indexer.Release{tvRel("Slow.Horses.S06E01.720p.WEB-DL.x264-GRP", 1<<30, 30)}

		add := func(version int64, msg string, ids ...int64) {
			t.Helper()
			vid, tid := version, int64(1)
			if err := f.hist.Add(ctx, &Event{Event: EventSearched, VersionID: &vid, TitleID: &tid, Message: msg}, EventData{EpisodeIDs: ids}); err != nil {
				t.Fatal(err)
			}
		}
		s5, s6a, s6b := epID(sv, "S05E01"), epID(sv, "S06E01"), epID(sv, "S06E02")
		add(sv.Version.ID, "season 5, old", s5)
		add(sv.Version.ID, "season 6, first", s6a)
		add(sv.Version.ID, "season 6, second episode", s6b)
		add(sv.Version.ID, "season 5, newer", s5)
		add(other.Version.ID, "another show", epID(other, "S02E01"))
		add(sv.Version.ID, "season 6, no episodes recorded") // overlaps nothing

		l := wantedOf(t, f)
		if m := l.item(t, "Slow Horses", 5).LastSearchMessage; m != "season 5, newer" {
			t.Errorf("season 5: %q", m)
		}
		if m := l.item(t, "Slow Horses", 6).LastSearchMessage; m != "season 6, second episode" {
			t.Errorf("season 6: %q", m)
		}
		if m := l.item(t, "Andor", 2).LastSearchMessage; m != "another show" {
			t.Errorf("Andor: %q", m)
		}

		// A real search writes the message the page shows.
		f.lib.epSearched = nil
		msg, err := f.svc.SearchSeriesVersion(ctx, other.Version.ID, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if m := wantedOf(t, f).item(t, "Andor", 2).LastSearchMessage; !strings.Contains(msg, m) || !strings.HasPrefix(m, "Searched for Andor") {
			t.Errorf("Andor after a search: %q (search said %q)", m, msg)
		}
	})
}

// Searches older than the window are ignored, and a pruned history just
// means no message.
func TestWantedIgnoresOldSearchesAndSurvivesPruning(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		sv := addSeries(t, db, f, "Slow Horses", "standard", map[string]string{"S06E01": "2026-09-01"}, "S06E01")
		vid := sv.Version.ID
		old := f.now.Add(-wantedTVWindow - 24*time.Hour)
		if err := f.hist.Add(ctx, &Event{Event: EventSearched, VersionID: &vid, CreatedAt: old, Message: "long ago"}, EventData{EpisodeIDs: []int64{epID(sv, "S06E01")}}); err != nil {
			t.Fatal(err)
		}
		if it := wantedOf(t, f).item(t, "Slow Horses", 6); it.LastSearchMessage != "" {
			t.Errorf("message = %q", it.LastSearchMessage)
		}
	})
}

// A fixed handful of queries however many versions are wanted.
func TestWantedQueryCountDoesNotGrow(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		var n atomic.Int64
		db.DB = db.DB.WithQueryHook(countingHook{&n}) // the History reads db.DB on every call
		count := func() int64 {
			t.Helper()
			n.Store(0)
			if _, err := f.svc.Wanted(ctx, true); err != nil {
				t.Fatal(err)
			}
			return n.Load()
		}
		addSeries(t, db, f, "First Show", "standard", map[string]string{"S01E01": "2026-10-01"}, "S01E01")
		small := count() // both History lookups (movies, TV) run

		for i := 0; i < 8; i++ {
			addMovieVersion(t, db, f, "Movie "+string(rune('A'+i)))
			addSeries(t, db, f, "Show "+string(rune('A'+i)), "standard", map[string]string{"S01E01": "2026-10-01", "S02E01": "2026-10-02"}, "S01E01", "S02E01")
		}
		// Give every version a search to find.
		for _, mv := range f.lib.versions {
			vid, tid := mv.Version.ID, mv.Title.ID
			if err := f.hist.Add(ctx, &Event{Event: EventSearched, VersionID: &vid, TitleID: &tid, Message: "x"}, EventData{}); err != nil {
				t.Fatal(err)
			}
		}
		for _, sv := range f.lib.series {
			vid := sv.Version.ID
			if err := f.hist.Add(ctx, &Event{Event: EventSearched, VersionID: &vid, Message: "y"}, EventData{EpisodeIDs: []int64{sv.Wanted[0].ID}}); err != nil {
				t.Fatal(err)
			}
		}
		big := count()
		t.Logf("queries: %d with few versions, %d with many", small, big)
		if small == 0 || big != small {
			t.Errorf("queries: %d with few versions, %d with many", small, big)
		}
		if l := wantedOf(t, f); len(l.Missing) < 19 || l.item(t, "Show A", 2).LastSearchMessage != "" {
			t.Errorf("missing = %d", len(l.Missing))
		}
	})
}

type countingHook struct{ n *atomic.Int64 }

func (countingHook) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context { return ctx }
func (h countingHook) AfterQuery(context.Context, *bun.QueryEvent)                      { h.n.Add(1) }

// addMovieVersion stores another movie without a file and adds it to the
// fake library.
func addMovieVersion(t *testing.T, db *store.Store, f *fixture, title string) {
	t.Helper()
	ctx := context.Background()
	var root library.RootFolder
	if err := db.DB.NewSelect().Model(&root).Limit(1).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	m := &library.Title{MediaType: library.Movie, TMDBID: 500 + len(f.lib.versions), Title: title, Year: 2001, Runtime: 100,
		Status: "Released", OriginalLanguage: "en", CreatedAt: f.now, Genres: "[]", AltTitles: "[]"}
	if _, err := db.DB.NewInsert().Model(m).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	v := &library.Version{TitleID: m.ID, ProfileID: 1, RootFolderID: root.ID, Monitored: true, Upgrades: true, CreatedAt: f.now}
	if _, err := db.DB.NewInsert().Model(v).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	f.lib.versions = append(f.lib.versions, library.MovieVersion{Version: *v, Title: *m})
}

// A movie whose grab the queue limit held back was searched but not marked
// searched (it stays first in line). Later it mustn't say "not
// searched yet" next to a search result.
func TestWantedHeldBackByTheLimit(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode = settings.Grabbing{Mode: settings.GrabOn, TVMode: settings.GrabOff, MinSeeders: 1, QueueLimit: 1}
		queue := &fakeQueue{}
		f.setDownloads(&fakeGrabber{}, queue)
		f.idx.releases = []indexer.Release{rel("Heat.1995.1080p.BluRay.x264-AMIABLE", 40)}
		if _, err := f.svc.SearchVersion(ctx, f.lib.versions[0].Version.ID, TriggerManual); err != nil {
			t.Fatal(err) // grabs Heat: the queue's one place is taken
		}
		f.idx.releases = []indexer.Release{rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40)}
		if _, err := f.svc.SearchVersion(ctx, f.lib.versions[1].Version.ID, TriggerManual); err != nil {
			t.Fatal(err)
		}
		// Heat's download finishes, so the queue has room again.
		queue.size = 0
		f.now = f.now.Add(24 * time.Hour)
		it := wantedOf(t, f).item(t, "Ronin")
		if it.State != StateNever || it.Status != heldBackText || !strings.HasPrefix(it.LastSearchMessage, "Searched for Ronin") {
			t.Errorf("held back: %+v", it)
		}
	})
}

// While the download disk is short (and grabbing is On), everything waiting
// says so: the hourly pass doesn't search until cleanup makes room. A dry
// run isn't held back by the disk.
func TestWantedWaitingForSpace(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		f.mode = settings.Grabbing{Mode: settings.GrabOn, TVMode: settings.GrabDryRun, MinSeeders: 1}
		why := "the download disk has 18.0 GB free."
		f.svc.SetDownloads(&fakeGrabber{}, &diskQueue{fakeQueue: &fakeQueue{}, why: why})
		addSeries(t, db, f, "The Expanse", "standard", map[string]string{"S01E01": "2015-12-14"}, "S01E01")

		l := wantedOf(t, f)
		it := l.item(t, "Ronin")
		if it.State != StateNoSpace || it.Status != "Waiting for space: "+why+" Searching resumes when cleanup makes room." {
			t.Errorf("movie with the disk short: %+v", it)
		}
		if tv := l.item(t, "The Expanse", 1); tv.State != StateNever {
			t.Errorf("dry-run TV with the disk short: %+v", tv)
		}
	})
}

// Episodes searched but not marked (the disk had no room for their grab)
// say they're first in line, not "not searched yet".
func TestWantedEpisodesHeldBackBySpace(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		g, _ := bfOn(f)
		g.err = fmt.Errorf("%w: the download disk has 30.8 GB free", ErrNotEnoughSpace)
		sv := addSeries(t, db, f, "The Expanse", "standard", map[string]string{"S01E01": "2015-12-14"}, "S01E01")
		f.idx.releases = []indexer.Release{tvRel("The.Expanse.S01E01.1080p.WEB-DL.DDP5.1.H.264-NTb", 2<<30, 90)}
		if _, err := f.svc.SearchVersion(ctx, sv.Version.ID, TriggerRequest); err != nil {
			t.Fatal(err)
		}
		it := wantedOf(t, f).item(t, "The Expanse", 1)
		if it.State != StateNever || it.Status != heldBackText || it.LastSearchMessage == "" {
			t.Errorf("held back by the disk: %+v", it)
		}
	})
}
