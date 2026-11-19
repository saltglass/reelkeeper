package grab

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// badGrabber refuses the releases named in bad for their files, as the
// download service does after reading a .torrent, and grabs the rest.
type badGrabber struct {
	fakeGrabber
	bad   map[string]bool
	tried []string
}

func (b *badGrabber) Grab(ctx context.Context, g Grabbed) error {
	b.tried = append(b.tried, g.Release.Title)
	if b.bad[g.Release.Title] {
		return fmt.Errorf("%w: it contains Setup.exe, a Windows program, not a video", ErrBadFiles)
	}
	return b.fakeGrabber.Grab(ctx, g)
}

// useWith connects it to the fixture as its grabber (setDownloads, for a
// badGrabber).
func (b *badGrabber) useWith(f *fixture) {
	q := &fakeQueue{}
	b.q = q
	f.svc.SetDownloads(b, q)
}

func refusing(titles ...string) *badGrabber {
	b := &badGrabber{bad: map[string]bool{}}
	for _, t := range titles {
		b.bad[t] = true
	}
	return b
}

// A search whose best release is refused for its files grabs the next
// acceptable one instead, and History says why the first wasn't taken.
// No "couldn't send it" email: nothing is wrong that anyone must fix.
func TestSearchMovesOnFromAReleaseWithBadFiles(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode = settings.Grabbing{Mode: settings.GrabOn, MinSeeders: 1}
		var sent []notify.Event
		f.svc.SetNotifier(func(_ context.Context, ev notify.Event) { sent = append(sent, ev) })
		g := refusing("Ronin.1998.1080p.BluRay.x264-FAKE")
		g.useWith(f)
		f.idx.releases = []indexer.Release{
			rel("Ronin.1998.1080p.BluRay.x264-FAKE", 90),
			rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40),
		}
		msg, err := f.svc.SearchVersion(ctx, f.lib.versions[1].Version.ID, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(g.tried, []string{"Ronin.1998.1080p.BluRay.x264-FAKE", "Ronin.1998.1080p.BluRay.x264-AMIABLE"}) ||
			!slices.Equal(g.got, []string{"Ronin.1998.1080p.BluRay.x264-AMIABLE"}) {
			t.Fatalf("tried %v, grabbed %v (%s)", g.tried, g.got, msg)
		}
		skipped := f.events(t, EventGrabSkipped)
		if len(skipped) != 1 || !strings.Contains(skipped[0].Message, "read its file list first and it contains Setup.exe, a Windows program") ||
			!strings.Contains(skipped[0].Message, "on the blocklist now") {
			t.Errorf("skipped = %+v", skipped)
		}
		if len(f.events(t, EventGrabbed)) != 1 || !strings.Contains(msg, "Grabbed Ronin.1998.1080p.BluRay.x264-AMIABLE") {
			t.Errorf("message = %q", msg)
		}
		if len(sent) != 0 {
			t.Errorf("emails = %+v, want none", sent)
		}
		if _, marked := f.lib.searched[f.lib.versions[1].Version.ID]; !marked {
			t.Error("the search didn't count")
		}
	})
}

// Each refusal costs one of the indexer's grabs (the .torrent was fetched),
// so one search gives up after maxBadFileTries of them.
func TestSearchStopsAfterThreeReleasesWithBadFiles(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode = settings.Grabbing{Mode: settings.GrabOn, MinSeeders: 1}
		names := []string{"Ronin.1998.1080p.BluRay.x264-A", "Ronin.1998.1080p.BluRay.x264-B",
			"Ronin.1998.1080p.BluRay.x264-C", "Ronin.1998.1080p.BluRay.x264-D"}
		g := refusing(names...)
		g.useWith(f)
		for i, n := range names {
			f.idx.releases = append(f.idx.releases, rel(n, 90-10*i))
		}
		if _, err := f.svc.SearchVersion(ctx, f.lib.versions[1].Version.ID, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.tried) != maxBadFileTries || len(g.got) != 0 {
			t.Errorf("tried %v, grabbed %v", g.tried, g.got)
		}
	})
}

// Any other refusal still stops the search at once, as before.
func TestSearchDoesNotMoveOnFromOtherRefusals(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode = settings.Grabbing{Mode: settings.GrabOn, MinSeeders: 1}
		g := &badGrabber{}
		g.err = errors.New("qBittorrent said no")
		g.useWith(f)
		f.idx.releases = []indexer.Release{rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 90), rel("Ronin.1998.1080p.BluRay.x264-GRP", 40)}
		if _, err := f.svc.SearchVersion(ctx, f.lib.versions[1].Version.ID, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.tried) != 1 {
			t.Errorf("tried %v, want only the best", g.tried)
		}
	})
}

// A season pack refused for its files gives its episodes back: they're
// picked again from the other releases (here, singles).
func TestTVRepicksEpisodesOfAPackWithBadFiles(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode = settings.Grabbing{Mode: settings.GrabOn, TVMode: settings.GrabOn, MinSeeders: 1}
		sv := addSeries(t, db, f, "The Expanse", "standard", map[string]string{
			"S01E01": "2015-12-14", "S01E02": "2015-12-15",
		}, "S01E01", "S01E02")
		g := refusing("The.Expanse.S01.1080p.BluRay.x264-FAKE")
		g.useWith(f)
		f.idx.releases = []indexer.Release{
			tvRel("The.Expanse.S01.1080p.BluRay.x264-FAKE", 9<<30, 90),
			tvRel("The.Expanse.S01E01.1080p.WEB-DL.H.264-NTb", 2<<30, 50),
			tvRel("The.Expanse.S01E02.1080p.WEB-DL.H.264-NTb", 2<<30, 40),
		}
		msg, err := f.svc.SearchVersion(ctx, sv.Version.ID, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(g.got, []string{"The.Expanse.S01E01.1080p.WEB-DL.H.264-NTb", "The.Expanse.S01E02.1080p.WEB-DL.H.264-NTb"}) {
			t.Fatalf("tried %v, grabbed %v (%s)", g.tried, g.got, msg)
		}
		if len(f.lib.epSearched[sv.Version.ID]) != 2 {
			t.Errorf("search times = %v", f.lib.epSearched)
		}
	})
}

// When a TV search runs out of tries with releases still untried, those
// episodes don't count as searched, so the next search tries again soon.
func TestTVKeepsEpisodesFirstInLineAfterThreeBadReleases(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.mode = settings.Grabbing{Mode: settings.GrabOn, TVMode: settings.GrabOn, MinSeeders: 1}
		sv := addSeries(t, db, f, "The Expanse", "standard", map[string]string{
			"S04E01": "2019-12-13", "S04E02": "2019-12-13", "S04E03": "2019-12-13", "S04E04": "2019-12-13",
		}, "S04E03")
		var names []string
		for i, grp := range []string{"A", "B", "C", "D"} {
			n := "The.Expanse.S04E03.1080p.WEB-DL.H.264-" + grp
			names = append(names, n)
			f.idx.releases = append(f.idx.releases, tvRel(n, 2<<30, 90-10*i))
		}
		g := refusing(names...)
		g.useWith(f)
		msg, err := f.svc.SearchVersion(ctx, sv.Version.ID, TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		if len(g.tried) != maxBadFileTries || len(g.got) != 0 {
			t.Errorf("tried %v, grabbed %v", g.tried, g.got)
		}
		if !strings.Contains(msg, "Stopped after 3 releases with bad files") {
			t.Errorf("message = %q", msg)
		}
		if got := f.lib.epSearched[sv.Version.ID]; len(got) != 0 {
			t.Errorf("search times = %v, want the episode still first in line", got)
		}
	})
}

// A release the user chose by hand is refused with the reason, too.
func TestPickRefusalForBadFiles(t *testing.T) {
	err := grabRefusal(fmt.Errorf("%w: it contains Setup.exe, a Windows program, not a video", ErrBadFiles))
	var r Refusal
	if !errors.As(err, &r) || !strings.HasPrefix(string(r), "Not downloaded: Reelkeeper read its file list first and it contains Setup.exe") {
		t.Errorf("refusal = %v", err)
	}
}
