package builtin

// Tracker-only torrents (private ones) get every peer from their tracker, so
// they must still be told about them after being paused, queued behind others,
// rotated out of the seeding slots, checked again, or held by the kill switch.
// In every test here DHT and peer exchange are off and the seeder is only ever
// found through the stub tracker (no feed()), so a torrent the tracker can no
// longer reach never finishes.

import (
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/anacrolix/torrent"

	rktorrent "github.com/saltglass/reelkeeper/internal/torrent"
)

// unstickByClosing is the harness's answer to the loopback stall (see unstick)
// without the part that hands the torrent its peer: a torrent that makes no
// progress for a few seconds has its connections closed, and it must find its
// way back through what its tracker (or its memory of earlier peers) tells it.
func unstickByClosing(t *testing.T, e *Engine, hash string) {
	t.Helper()
	unstick(t, engineTorrent(e, hash), nil)
}

func trackerContent(t *testing.T, tr *stubTracker, name string, big int) *content {
	t.Helper()
	return makeContent(t, name, contentOpts{announce: tr.url(), big: big})
}

// The upstream bug, pinned: with anacrolix/torrent 1.61, a torrent dropped from
// a client and added to it again is never given the peers its tracker returns
// (the tracker's answers go to the dropped torrent). The engine works around it
// (stopRunning, parkRunning, reconcile). If this test starts to fail, upstream
// fixed it, and the workaround can be reconsidered.
func TestAnacrolixTorrentReAddedAfterDropIsTrackerBlind(t *testing.T) {
	// The seeder and the tracker are made first: the tracker has to know the
	// content's info hash, and the content has to name the tracker.
	tr := startTracker(t)
	c := makeContent(t, "Blind.2020", contentOpts{announce: tr.url(), big: 1 << 20})
	s := startSeeder(t, seederOpts{}, c)
	tr.set(c.hash, "", s.addr())

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = t.TempDir()
	cfg.ListenHost = torrent.LoopbackListenHost
	cfg.ListenPort = 0
	cfg.NoDHT, cfg.DisablePEX, cfg.NoDefaultPortForwarding = true, true, true
	cfg.DisableIPv6, cfg.DisableWebtorrent, cfg.DisableWebseeds = true, true, true
	cfg.Slogger = slog.New(slog.NewTextHandler(io.Discard, nil))
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	peersAfter := func(wait time.Duration) int {
		spec := torrent.TorrentSpecFromMetaInfo(c.mi)
		spec.Storage = fileStorageAt(t.TempDir())
		tt, _, err := cl.AddTorrentSpec(spec)
		if err != nil {
			t.Fatal(err)
		}
		defer tt.Drop()
		deadline := time.Now().Add(wait)
		for time.Now().Before(deadline) {
			if n := tt.Stats().TotalPeers; n > 0 {
				return n
			}
			time.Sleep(50 * time.Millisecond)
		}
		return 0
	}
	if n := peersAfter(10 * time.Second); n == 0 {
		t.Fatal("control: the first add got no peers from the tracker either")
	}
	if n := peersAfter(4 * time.Second); n != 0 {
		t.Skipf("anacrolix/torrent now gives a re-added torrent its tracker's peers (%d): the workaround in stopRunning can be reviewed", n)
	}
}

func TestPauseAndResumeStillGetsTrackerPeers(t *testing.T) {
	tr := startTracker(t)
	c := trackerContent(t, tr, "Pause.Tracker.2021", 3<<20)
	s := startSeeder(t, seederOpts{}, c)
	tr.set(c.hash, "", s.addr())
	e, cfg := openEngine(t, &loopbackNet{}, engineOpts{cfg: func(c *Config) { c.DownloadLimitKBs = 500 }})
	hash, _ := e.Add(AddRequest{Torrent: c.torrent, Category: cat})
	unstickByClosing(t, e, hash)
	waitFor(t, e, hash, "a start through the tracker", 30*time.Second, func(s Status) bool { return s.Have > 300<<10 })
	for round := 1; round <= 2; round++ {
		if err := e.Stop(cat, []string{hash}); err != nil {
			t.Fatal(err)
		}
		if isRunning(e, hash) {
			t.Fatal("still running after Stop")
		}
		had := status(t, e, hash).Have
		time.Sleep(700 * time.Millisecond)
		if got := status(t, e, hash).Have; got != had {
			t.Fatalf("round %d: data moved while paused: %d -> %d", round, had, got)
		}
		if st := status(t, e, hash); st.State != rktorrent.Paused || st.Peers != 0 {
			t.Fatalf("round %d: paused torrent: %+v", round, st)
		}
		if err := e.Start(cat, []string{hash}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, e, hash, fmt.Sprintf("progress after resume %d", round), 30*time.Second, func(s Status) bool { return s.Have > had+200<<10 || s.Progress >= 1 })
	}
	waitFor(t, e, hash, "finishing through the tracker", 60*time.Second, func(s Status) bool { return s.Progress >= 1 })
	sameFiles(t, cfg.Folder, c)
	// The totals are not counted twice by a torrent that carries on.
	st := status(t, e, hash)
	if st.Ratio > 0.05 {
		t.Errorf("uploaded count looks wrong: %+v", st)
	}
}

// A running torrent pushed back into the queue by Move to top, and run again
// when the other one is done, still hears from its tracker.
func TestTorrentPushedOutByMoveToTopStillGetsTrackerPeers(t *testing.T) {
	tr := startTracker(t)
	a := trackerContent(t, tr, "Pushed.Out.2021", 3<<20)
	b := trackerContent(t, tr, "Pushes.In.2021", 1<<20)
	s := startSeeder(t, seederOpts{}, a, b)
	tr.set(a.hash, "", s.addr())
	tr.set(b.hash, "", s.addr())
	e, cfg := openEngine(t, &loopbackNet{}, engineOpts{cfg: func(c *Config) { c.MaxActiveDownloads = 1; c.DownloadLimitKBs = 500 }})
	ha, _ := e.Add(AddRequest{Torrent: a.torrent, Category: cat})
	unstickByClosing(t, e, ha)
	waitFor(t, e, ha, "a start", 30*time.Second, func(s Status) bool { return s.Have > 300<<10 })
	hb, _ := e.Add(AddRequest{Torrent: b.torrent, Category: cat})
	unstickByClosing(t, e, hb)
	if st := status(t, e, hb); st.State != rktorrent.Queued {
		t.Fatalf("the second torrent is %s, not queued", st.State)
	}
	if err := e.Top(cat, []string{hb}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, hb, "the one moved to the top finishing", 40*time.Second, func(s Status) bool { return s.Progress >= 1 })
	if st := status(t, e, ha); st.State != rktorrent.Queued && st.Progress < 1 {
		// (It may already have restarted by now.)
		t.Logf("the pushed-out torrent is %s", st.State)
	}
	waitFor(t, e, ha, "the pushed-out one finishing through the tracker", 60*time.Second, func(s Status) bool { return s.Progress >= 1 })
	sameFiles(t, cfg.Folder, a)
	sameFiles(t, cfg.Folder, b)
}

// When the number of torrents allowed to seed goes down, one is rotated out
// (parked); when it comes back it uploads again and is still announced.
func TestSeedingRotationKeepsTorrentsReachable(t *testing.T) {
	tr := startTracker(t)
	a := trackerContent(t, tr, "Seed.A.2021", 600<<10)
	b := trackerContent(t, tr, "Seed.B.2021", 600<<10)
	s := startSeeder(t, seederOpts{}, a, b)
	tr.set(a.hash, "", s.addr())
	tr.set(b.hash, "", s.addr())
	e, _ := openEngine(t, &loopbackNet{}, engineOpts{})
	ha, _ := e.Add(AddRequest{Torrent: a.torrent, Category: cat})
	unstickByClosing(t, e, ha)
	hb, _ := e.Add(AddRequest{Torrent: b.torrent, Category: cat})
	unstickByClosing(t, e, hb)
	for _, h := range []string{ha, hb} {
		waitFor(t, e, h, "finishing through the tracker", 40*time.Second, func(s Status) bool { return s.Progress >= 1 })
	}
	// One slot: the later one is rotated out.
	cfg := e.Config()
	cfg.MaxActiveSeeds = 1
	e.Configure(cfg)
	eventuallyWithin(t, 5*time.Second, "one seed to be rotated out", func() bool { return running(e) == 1 })
	out, in := hb, ha
	if !isRunning(e, ha) {
		out, in = ha, hb
	}
	if err := e.Top(cat, []string{out}); err != nil { // now the other way round
		t.Fatal(err)
	}
	eventuallyWithin(t, 5*time.Second, "the rotation", func() bool { return isRunning(e, out) && !isRunning(e, in) })
	// The one that came back uploads to somebody who asks.
	c := a
	if out == hb {
		c = b
	}
	lt := leech(t, c, e.Port())
	select {
	case <-lt.Complete().On():
	case <-time.After(40 * time.Second):
		t.Fatal("the rotated-in seed never uploaded to a leecher")
	}
	before := tr.called(c.hash)
	eventuallyWithin(t, 10*time.Second, "the tracker to hear from the seed again", func() bool { return tr.called(c.hash) > before })
}

// Checking a torrent again takes it out of the client for good (its files are
// being read), and it comes back in a new client, which hears from its tracker.
func TestRecheckedTorrentStillGetsTrackerPeers(t *testing.T) {
	tr := startTracker(t)
	c := trackerContent(t, tr, "Recheck.Tracker.2021", 3<<20)
	s := startSeeder(t, seederOpts{}, c)
	tr.set(c.hash, "", s.addr())
	e, cfg := openEngine(t, &loopbackNet{}, engineOpts{cfg: func(c *Config) { c.DownloadLimitKBs = 500 }})
	hash, _ := e.Add(AddRequest{Torrent: c.torrent, Category: cat})
	unstickByClosing(t, e, hash)
	waitFor(t, e, hash, "a start", 30*time.Second, func(s Status) bool { return s.Have > 300<<10 })
	if err := e.Recheck(cat, hash); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, hash, "finishing after the check", 60*time.Second, func(s Status) bool { return s.Progress >= 1 })
	sameFiles(t, cfg.Folder, c)
}

// Holding the engine and letting it go (what the kill switch does) leaves
// tracker-only torrents able to finish.
func TestHoldAndReleaseStillGetsTrackerPeers(t *testing.T) {
	tr := startTracker(t)
	c := trackerContent(t, tr, "Hold.Tracker.2021", 3<<20)
	s := startSeeder(t, seederOpts{}, c)
	tr.set(c.hash, "", s.addr())
	e, cfg := openEngine(t, &loopbackNet{}, engineOpts{cfg: func(c *Config) { c.DownloadLimitKBs = 500 }})
	hash, _ := e.Add(AddRequest{Torrent: c.torrent, Category: cat})
	unstickByClosing(t, e, hash)
	waitFor(t, e, hash, "a start", 30*time.Second, func(s Status) bool { return s.Have > 300<<10 })
	e.SetHold(true)
	eventuallyWithin(t, 5*time.Second, "the hold", func() bool { return running(e) == 0 })
	had := status(t, e, hash).Have
	time.Sleep(500 * time.Millisecond)
	if got := status(t, e, hash).Have; got != had {
		t.Fatalf("data moved while held: %d -> %d", had, got)
	}
	e.SetHold(false)
	waitFor(t, e, hash, "finishing after the hold", 60*time.Second, func(s Status) bool { return s.Progress >= 1 })
	sameFiles(t, cfg.Folder, c)
}
