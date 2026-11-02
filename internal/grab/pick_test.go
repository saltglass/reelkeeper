package grab

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// "Choose a release" for a movie (pick.go). The fixture's profile is the HD
// preset (1080p, the movie's original language); Ronin (1998, English) has
// no file, Heat has a WEB-DL.

// roninReleases are what the fake indexers find for Ronin.
func roninReleases() []indexer.Release {
	return []indexer.Release{
		rel("Ronin.1998.GERMAN.720p.HDTV.x264-WORSE", 300), // two rules broken
		rel("Ronin.1998.GERMAN.1080p.BluRay.x264-GER", 50), // one rule: the language
		rel("Ronin.1998.1080p.WEB-DL.H.264-GRP", 400),      // acceptable
		rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40),    // acceptable, and the best
		rel("Ronin.2023.1080p.WEB-DL.H.264-REMAKE", 900),   // another movie: hidden
		rel("Ronin.1998.1080p.BluRay.x264-BLOCKED", 500),   // blocklisted: hidden
		rel("Ronin.1998.GERMAN.1080p.BluRay.x264-DEAD", 0), // nobody seeds it: hidden
	}
}

// pickOn turns movie grabbing on with a fake download client and queue.
func pickOn(f *fixture) (*fakeGrabber, *fakeQueue) {
	f.mode = settings.Grabbing{Mode: settings.GrabOn, TVMode: settings.GrabOn, MinSeeders: 1, QueueLimit: 50}
	g, q := &fakeGrabber{}, &fakeQueue{active: map[int64]bool{}, blocked: map[string]bool{}}
	f.setDownloads(g, q)
	return g, q
}

// group is the release group at the end of a release name.
func group(name string) string { return name[strings.LastIndex(name, "-")+1:] }

// choices lists Ronin's releases and returns them with their tokens by
// release group.
func choices(t *testing.T, f *fixture) (*MovieChoiceList, map[string]string) {
	t.Helper()
	ronin := f.lib.versions[1]
	l, err := f.svc.MovieChoices(context.Background(), ronin.Title.ID, ronin.Version.ID)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, c := range l.Releases {
		tokens[group(c.Title)] = c.Token
	}
	return l, tokens
}

func TestMovieChoicesListAcceptedThenClosestMisses(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		_, q := pickOn(f)
		q.blocked["Ronin.1998.1080p.BluRay.x264-BLOCKED"] = true
		f.idx.releases = roninReleases()
		f.idx.failures = []indexer.IndexerError{{Indexer: "Slowtracker", Message: "timed out"}}

		l, _ := choices(t, f)
		var order []string
		for _, c := range l.Releases {
			order = append(order, group(c.Title))
		}
		// The two the profile accepts, best first; then the misses, fewest
		// broken rules first (the German Blu-ray breaks only the language
		// rule).
		if got := strings.Join(order, ","); got != "AMIABLE,GRP,GER,WORSE" {
			t.Errorf("order = %s", got)
		}
		if l.Hidden != 3 || l.More != 0 {
			t.Errorf("hidden = %d, more = %d; want 3 (remake, blocklisted, dead) and 0", l.Hidden, l.More)
		}
		if len(l.IndexerErrors) != 1 || l.IndexerErrors[0] != "Slowtracker: timed out" {
			t.Errorf("indexer errors = %v", l.IndexerErrors)
		}
		best, ger, worse := l.Releases[0], l.Releases[2], l.Releases[3]
		if !best.Accepted || len(best.Reasons) != 0 || best.Token == "" || best.Quality != "Bluray-1080p" ||
			best.Seeders != 40 || best.Indexer != "Knaben" || best.Size != 8<<30 {
			t.Errorf("best = %+v", best)
		}
		if ger.Accepted || len(ger.Reasons) != 1 || !strings.Contains(ger.Reasons[0], "original language") {
			t.Errorf("German Blu-ray = %+v", ger)
		}
		if worse.Accepted || len(worse.Reasons) < 2 {
			t.Errorf("worse = %+v", worse)
		}
		// Listing grabs nothing and records nothing.
		if evs := f.events(t); len(evs) != 0 {
			t.Errorf("History = %+v", evs)
		}
	})
}

// The list ignores the Upgrades switch: a movie with a file and upgrades
// off still shows what the profile would take as an upgrade.
func TestMovieChoicesIgnoreUpgradesSwitch(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		pickOn(f)
		heat := &f.lib.versions[0]
		heat.Version.Upgrades = false
		f.idx.releases = []indexer.Release{rel("Heat.1995.1080p.BluRay.x264-AMIABLE", 40)}
		l, err := f.svc.MovieChoices(context.Background(), heat.Title.ID, heat.Version.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(l.Releases) != 1 || !l.Releases[0].Accepted {
			t.Errorf("releases = %+v", l.Releases)
		}
	})
}

func TestMovieChoicesCapAtFifty(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		pickOn(f)
		f.idx.releases = nil
		for i := range maxChoices + 7 {
			f.idx.releases = append(f.idx.releases, rel(fmt.Sprintf("Ronin.1998.1080p.WEB-DL.H.264-G%d", i), 10+i))
		}
		l, _ := choices(t, f)
		if len(l.Releases) != maxChoices || l.More != 7 {
			t.Errorf("listed %d, more %d", len(l.Releases), l.More)
		}
	})
}

// A release outside the profile is grabbed, with History saying so, and
// turns Upgrades off so the next search keeps the file.
func TestGrabMovieChoiceOutsideProfile(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		g, q := pickOn(f)
		f.idx.releases = roninReleases()
		ronin := &f.lib.versions[1]
		_, tokens := choices(t, f)

		// The queue limit doesn't stop a release the user picked.
		f.mode.QueueLimit = 1
		q.size = 1
		res, err := f.svc.GrabMovieChoice(ctx, ronin.Title.ID, ronin.Version.ID, tokens["GER"])
		if err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 1 || g.grabs[0].Release.Title != "Ronin.1998.GERMAN.1080p.BluRay.x264-GER" ||
			g.grabs[0].Version.Version.ID != ronin.Version.ID || !g.grabs[0].Requested || g.grabs[0].Upgrade {
			t.Fatalf("grabs = %+v", g.grabs)
		}
		if res.DryRun || !res.UpgradesOff || ronin.Version.Upgrades {
			t.Errorf("result = %+v, upgrades = %v; want upgrades turned off", res, ronin.Version.Upgrades)
		}
		for _, want := range []string{"Grabbed Ronin.1998.GERMAN.1080p.BluRay.x264-GER", "Picked by you outside the profile: ",
			"original language", "Upgrades are now off"} {
			if !strings.Contains(res.Message, want) {
				t.Errorf("message %q lacks %q", res.Message, want)
			}
		}
		evs := f.events(t, EventGrabbed)
		if len(evs) != 1 || evs[0].Message != res.Message || evs[0].SourceTitle != "Ronin.1998.GERMAN.1080p.BluRay.x264-GER" ||
			!strings.Contains(evs[0].Data, `"trigger":"picked"`) {
			t.Errorf("History = %+v", evs)
		}
		// One use only.
		if _, err := f.svc.GrabMovieChoice(ctx, ronin.Title.ID, ronin.Version.ID, tokens["GER"]); !errors.Is(err, ErrOptionGone) {
			t.Errorf("reused token: %v", err)
		}

		// Imported: the next search finds the English Blu-ray the profile
		// likes, and keeps the German file.
		ronin.Files = []library.MediaFile{{VersionID: ronin.Version.ID, Path: "Ronin/Ronin.1998.GERMAN.1080p.BluRay.x264-GER.mkv",
			Size: 8 << 30, Resolution: 1080, Source: "BLURAY"}}
		q.size = 0
		f.idx.releases = []indexer.Release{rel("Ronin.1998.1080p.BluRay.x264-AMIABLE", 40)}
		if _, err := f.svc.SearchVersion(ctx, ronin.Version.ID, TriggerManual); err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 1 {
			t.Errorf("the next search replaced the picked file: %v", g.got)
		}
	})
}

// A release the profile accepts is an ordinary grab: Upgrades stay on.
func TestGrabMovieChoiceInsideProfile(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		g, _ := pickOn(f)
		f.idx.releases = roninReleases()
		ronin := &f.lib.versions[1]
		_, tokens := choices(t, f)
		res, err := f.svc.GrabMovieChoice(context.Background(), ronin.Title.ID, ronin.Version.ID, tokens["GRP"])
		if err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 1 || res.UpgradesOff || !ronin.Version.Upgrades {
			t.Errorf("result = %+v, upgrades = %v", res, ronin.Version.Upgrades)
		}
		if !strings.HasSuffix(res.Message, "Picked by you.") || strings.Contains(res.Message, "outside") {
			t.Errorf("message = %q", res.Message)
		}
	})
}

// Picking a replacement for a movie that has a file is an upgrade: the old
// file is set aside on import, as for any upgrade.
func TestGrabMovieChoiceReplacesAFile(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		g, _ := pickOn(f)
		heat := f.lib.versions[0]
		f.idx.releases = []indexer.Release{rel("Heat.1995.1080p.BluRay.x264-AMIABLE", 40)}
		l, err := f.svc.MovieChoices(context.Background(), heat.Title.ID, heat.Version.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.GrabMovieChoice(context.Background(), heat.Title.ID, heat.Version.ID, l.Releases[0].Token); err != nil {
			t.Fatal(err)
		}
		if len(g.grabs) != 1 || !g.grabs[0].Upgrade {
			t.Errorf("grabs = %+v", g.grabs)
		}
	})
}

// With movie grabbing in dry run (or off), a pick is recorded as "would
// grab" and nothing is downloaded or changed, even with no download client.
func TestGrabMovieChoiceDryRun(t *testing.T) {
	for _, mode := range []string{settings.GrabDryRun, settings.GrabOff} {
		t.Run(mode, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, db *store.Store) {
				f := newFixture(t, db)
				f.mode = settings.Grabbing{Mode: mode, TVMode: settings.GrabOn, MinSeeders: 1}
				f.idx.releases = roninReleases()
				ronin := &f.lib.versions[1]
				_, tokens := choices(t, f)
				res, err := f.svc.GrabMovieChoice(context.Background(), ronin.Title.ID, ronin.Version.ID, tokens["GER"])
				if err != nil {
					t.Fatal(err)
				}
				if !res.DryRun || res.UpgradesOff || !ronin.Version.Upgrades ||
					!strings.HasPrefix(res.Message, "Would grab Ronin.1998.GERMAN.1080p.BluRay.x264-GER") ||
					!strings.Contains(res.Message, "Dry run: nothing was downloaded.") {
					t.Errorf("result = %+v, upgrades = %v", res, ronin.Version.Upgrades)
				}
				would := f.events(t, EventWouldGrab)
				if len(would) != 1 || would[0].Message != res.Message {
					t.Errorf("History = %+v", would)
				}
				if len(f.events(t, EventGrabbed)) != 0 {
					t.Error("recorded a grab")
				}
			})
		})
	}
}

func TestGrabMovieChoiceRefusals(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		g, q := pickOn(f)
		f.idx.releases = roninReleases()
		ronin := f.lib.versions[1]
		heat := f.lib.versions[0]
		_, tokens := choices(t, f)
		grab := func(token string) error {
			_, err := f.svc.GrabMovieChoice(ctx, ronin.Title.ID, ronin.Version.ID, token)
			return err
		}

		q.blocked["Ronin.1998.GERMAN.1080p.BluRay.x264-GER"] = true
		if err := grab(tokens["GER"]); !refusedWith(err, "blocklist") {
			t.Errorf("blocklisted since: %v", err)
		}
		q.blocked = map[string]bool{}
		f.lib.unmanaged = map[int64]bool{ronin.Version.RootFolderID: true}
		if err := grab(tokens["GER"]); !refusedWith(err, "Manage files") {
			t.Errorf("unmanaged: %v", err)
		}
		f.lib.unmanaged = nil
		g.err = fmt.Errorf("%w: the download disk has 50.0 GB free", ErrNotEnoughSpace)
		if err := grab(tokens["GER"]); !refusedWith(err, "50.0 GB free") {
			t.Errorf("no space: %v", err)
		}
		g.err = ErrMoving
		if err := grab(tokens["GER"]); !refusedWith(err, "moving to another download client") {
			t.Errorf("moving: %v", err)
		}
		g.err = nil
		q.active[ronin.Version.ID] = true
		if err := grab(tokens["GER"]); !refusedWith(err, "already in progress") {
			t.Errorf("downloading: %v", err)
		}
		q.active = map[int64]bool{}
		f.svc.SetDownloads(nil, q)
		if err := grab(tokens["GER"]); !refusedWith(err, "No download client") {
			t.Errorf("no client: %v", err)
		}
		f.svc.SetDownloads(g, q)
		if len(g.got) != 0 {
			t.Fatalf("grabbed %v despite refusals", g.got)
		}
		// Refusals leave the token usable, once the problem is fixed.
		if err := grab(tokens["GER"]); err != nil {
			t.Errorf("after fixing: %v", err)
		}

		// A token is for its own version only, and a version for its own
		// movie only.
		if _, err := f.svc.GrabMovieChoice(ctx, heat.Title.ID, heat.Version.ID, tokens["GRP"]); !errors.Is(err, ErrOptionGone) {
			t.Errorf("another version's token: %v", err)
		}
		if _, err := f.svc.GrabMovieChoice(ctx, heat.Title.ID, ronin.Version.ID, tokens["GRP"]); !errors.Is(err, library.ErrNotFound) {
			t.Errorf("another movie's version: %v", err)
		}
		if _, err := f.svc.MovieChoices(ctx, heat.Title.ID, ronin.Version.ID); !errors.Is(err, library.ErrNotFound) {
			t.Errorf("listing another movie's version: %v", err)
		}
		// Neither picker takes the other's tokens.
		if _, err := f.svc.BackfillGrabOption(ctx, 0, tokens["GRP"]); !errors.Is(err, ErrOptionGone) {
			t.Errorf("movie token taken by backfill: %v", err)
		}
		for _, token := range []string{"", "0000", "https://evil.example/x.torrent", "magnet:?xt=urn:btih:abc"} {
			if err := grab(token); !errors.Is(err, ErrOptionGone) {
				t.Errorf("forged token %q: %v", token, err)
			}
		}
		// Offers expire.
		f.now = f.now.Add(optionsKept + 1)
		if err := grab(tokens["WORSE"]); !errors.Is(err, ErrOptionGone) {
			t.Errorf("expired token: %v", err)
		}
		if len(g.got) != 1 {
			t.Errorf("grabbed %v", g.got)
		}
	})
}
