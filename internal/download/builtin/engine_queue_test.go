package builtin

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	rktorrent "github.com/saltglass/reelkeeper/internal/torrent"
)

// Restarting the engine must bring everything back as it was: finished
// torrents stay finished without being checked again, stopped ones stay
// stopped, and the totals carry on.
func TestRestartBringsTorrentsBack(t *testing.T) {
	done := makeContent(t, "Finished.Movie.2019", contentOpts{})
	half := makeContent(t, "Half.Way.2020", contentOpts{big: 3 << 20})
	waiting := makeContent(t, "Never.Started.2021", contentOpts{})
	s := startSeeder(t, seederOpts{}, done, half, waiting)
	dataDir, folder := t.TempDir(), t.TempDir()
	o := engineOpts{dir: dataDir, down: folder, cfg: func(c *Config) { c.DownloadLimitKBs = 700 }}
	e, cfg := openEngine(t, &loopbackNet{}, o)

	hDone, _ := e.Add(AddRequest{Torrent: done.torrent, Category: cat, Tags: []string{"reelkeeper", "an-indexer"}})
	feed(t, e, hDone, s)
	waitFor(t, e, hDone, "the first to finish", 30*time.Second, func(s Status) bool { return s.Progress >= 1 })
	// Somebody downloads it from us, so there is an upload total to keep.
	lt := leech(t, done, e.Port())
	select {
	case <-lt.Complete().On():
	case <-time.After(30 * time.Second):
		t.Fatal("the leecher never finished")
	}
	before := waitFor(t, e, hDone, "an upload to be counted", 10*time.Second, func(s Status) bool { return s.Uploaded > 0 })

	hHalf, _ := e.Add(AddRequest{Torrent: half.torrent, Category: cat})
	feed(t, e, hHalf, s)
	waitFor(t, e, hHalf, "half a download", 30*time.Second, func(s Status) bool { return s.Have > 500<<10 })
	hWait, _ := e.Add(AddRequest{Magnet: waiting.magnet(), Category: cat, Stopped: true})
	if err := e.Stop(cat, []string{hHalf}); err != nil {
		t.Fatal(err)
	}
	halfBefore := status(t, e, hHalf)
	order, _ := e.List(cat)

	closeEngine(t, e)
	// While it is closed the finished files are altered in place, same size:
	// a restart that trusted the piece records would not notice, one that
	// checked everything again would.
	doneFile := filepath.Join(folder, done.name, "movie.mkv")
	b, _ := os.ReadFile(doneFile)
	b[0] ^= 0xff
	if err := os.WriteFile(doneFile, b, 0o644); err != nil {
		t.Fatal(err)
	}

	nw := &loopbackNet{}
	e2, err := Open(testConfig(t, nw, engineOpts{dir: dataDir, down: folder}))
	if err != nil {
		t.Fatal(err)
	}
	defer closeEngine(t, e2)
	_ = cfg

	after := status(t, e2, hDone)
	if !sharing(after.State) || after.Progress < 1 || after.Have != done.totalLen {
		t.Errorf("the finished torrent after a restart: %+v", after)
	}
	if after.Uploaded < before.Uploaded || after.Completed == nil || !after.Completed.Equal(*before.Completed) {
		t.Errorf("totals didn't carry over: uploaded %d -> %d, completed %v -> %v", before.Uploaded, after.Uploaded, before.Completed, after.Completed)
	}
	if got := after.Tags; len(got) != 2 || got[1] != "an-indexer" {
		t.Errorf("tags = %v", got)
	}
	if after.SeedingTime < before.SeedingTime {
		t.Errorf("seeding time went back: %v -> %v", before.SeedingTime, after.SeedingTime)
	}
	hs := status(t, e2, hHalf)
	// (A piece still being checked when the torrent was stopped is counted
	// when it is read back, so it may have a little more than it showed.)
	if hs.State != rktorrent.Paused || hs.Have < halfBefore.Have || hs.Have > halfBefore.Have+256<<10 || isRunning(e2, hHalf) {
		t.Errorf("the stopped download after a restart: %+v (had %d)", hs, halfBefore.Have)
	}
	if ws := status(t, e2, hWait); ws.State != rktorrent.Paused {
		t.Errorf("the never-started magnet after a restart: %+v", ws)
	}
	again, _ := e2.List(cat)
	for i := range order {
		if again[i].Hash != order[i].Hash {
			t.Errorf("the queue order changed: %s is now at %d", again[i].Name, i)
		}
	}
	if _, err := os.Stat(e2.torrentPath(hHalf)); err != nil {
		t.Errorf("the .torrent file is gone: %v", err)
	}
	// A restart doesn't ask anything of the network for stopped torrents, and
	// the engine isn't looking for the finished one's data again.
	if nw.dials.Load() != 0 {
		t.Errorf("%d connections were opened with nothing to download", nw.dials.Load())
	}
}

func TestMaxActiveDownloadsQueuesInOrderAndTopPriorityJumps(t *testing.T) {
	var hashes []string
	e, _ := openEngine(t, &loopbackNet{}, engineOpts{cfg: func(c *Config) { c.MaxActiveDownloads = 2 }})
	for _, n := range []string{"First.2020", "Second.2020", "Third.2020", "Fourth.2020"} {
		h, err := e.Add(AddRequest{Torrent: makeContent(t, n, contentOpts{}).torrent, Category: cat})
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, h)
	}
	states := func() (out []rktorrent.State) {
		for _, h := range hashes {
			out = append(out, status(t, e, h).State)
		}
		return out
	}
	active := func(s rktorrent.State) bool { return s == rktorrent.Downloading || s == rktorrent.Stalled }
	eventually(t, "two to run", func() bool { return running(e) == 2 })
	for i, s := range states() {
		if want := i < 2; active(s) != want || (!want && s != rktorrent.Queued) {
			t.Errorf("torrent %d is %s", i+1, s)
		}
	}
	// Move to top: the fourth takes the front.
	if err := e.Top(cat, []string{hashes[3]}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the fourth to run", func() bool { return isRunning(e, hashes[3]) })
	got := states()
	if !active(got[3]) || !active(got[0]) || got[1] != rktorrent.Queued || got[2] != rktorrent.Queued {
		t.Errorf("after moving the fourth to the top: %v", got)
	}
	if all, _ := e.List(cat); all[0].Hash != hashes[3] {
		t.Error("the list doesn't show the moved torrent first")
	}
	// Two moved at once keep their order among themselves.
	if err := e.Top(cat, []string{hashes[2], hashes[1]}); err != nil {
		t.Fatal(err)
	}
	all, _ := e.List(cat)
	if all[0].Hash != hashes[2] || all[1].Hash != hashes[1] {
		t.Errorf("order after moving two: %s %s", all[0].Name, all[1].Name)
	}
	// A paused torrent doesn't use a slot.
	if err := e.Stop(cat, []string{hashes[2]}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the next one to take the slot", func() bool { return running(e) == 2 && isRunning(e, hashes[1]) && isRunning(e, hashes[3]) })
	// The limit changes live.
	cfg := e.Config()
	cfg.MaxActiveDownloads = 4
	e.Configure(cfg)
	eventually(t, "three to run", func() bool { return running(e) == 3 })
	if st := status(t, e, hashes[2]); st.State != rktorrent.Paused {
		t.Errorf("the paused one is %s", st.State)
	}
}

func TestMaxActiveSeedsStopsTheRestUploading(t *testing.T) {
	var hashes []string
	e, _ := openEngine(t, &loopbackNet{}, engineOpts{cfg: func(c *Config) { c.MaxActiveSeeds = 2 }})
	for _, n := range []string{"Seed.One.2020", "Seed.Two.2020", "Seed.Three.2020"} {
		c := makeContent(t, n, contentOpts{})
		// Finished already: the files are where the torrent will look.
		h, err := e.Add(AddRequest{Torrent: c.torrent, Category: cat, SavePath: c.parent})
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, h)
	}
	for _, h := range hashes {
		waitFor(t, e, h, "being finished", 20*time.Second, func(s Status) bool { return s.Progress >= 1 })
	}
	eventually(t, "two seeds to run", func() bool { return running(e) == 2 })
	if isRunning(e, hashes[2]) || !isRunning(e, hashes[0]) || !isRunning(e, hashes[1]) {
		t.Error("the oldest two should seed")
	}
	// Waiting for its turn still reads as seeding.
	if st := status(t, e, hashes[2]); st.State != rktorrent.Seeding || st.Progress < 1 {
		t.Errorf("the third: %+v", st)
	}
	// Move to top applies to seeding order too.
	if err := e.Top(cat, []string{hashes[2]}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the third to seed", func() bool { return isRunning(e, hashes[2]) && !isRunning(e, hashes[1]) })
	// 0 means no limit.
	cfg := e.Config()
	cfg.MaxActiveSeeds = 0
	e.Configure(cfg)
	eventually(t, "all three to seed", func() bool { return running(e) == 3 })
}

func TestDownloadSpeedLimit(t *testing.T) {
	c := makeContent(t, "Slow.Down.2020", contentOpts{big: 3 << 20})
	s := startSeeder(t, seederOpts{}, c)
	e, _ := openEngine(t, &loopbackNet{}, engineOpts{cfg: func(c *Config) { c.DownloadLimitKBs = 500 }})
	hash, _ := e.Add(AddRequest{Torrent: c.torrent, Category: cat})
	start := time.Now()
	feed(t, e, hash, s)
	waitFor(t, e, hash, "half of it", 30*time.Second, func(s Status) bool { return s.Have > c.totalLen/2 })
	// 3 MB at 500 KB/s takes about six seconds; half of it, about three, less
	// the burst a limiter allows. Without a limit this is over in well under
	// a second, so this tells the limit works without being fussy about it.
	if took := time.Since(start); took < 1500*time.Millisecond {
		t.Errorf("half the torrent arrived in %v despite a 500 KB/s limit", took)
	}
	// Taking the limit off applies at once, without restarting anything.
	cfg := e.Config()
	cfg.DownloadLimitKBs = 0
	e.Configure(cfg)
	rest := time.Now()
	waitFor(t, e, hash, "the rest", 20*time.Second, func(s Status) bool { return s.Progress >= 1 })
	if took := time.Since(rest); took > 3*time.Second {
		t.Errorf("the rest took %v after the limit was lifted", took)
	}
	if !isRunning(e, hash) {
		t.Error("lifting the limit restarted the torrent")
	}
}

func TestUploadSpeedLimit(t *testing.T) {
	c := makeContent(t, "Slow.Up.2020", contentOpts{big: 2 << 20})
	e, _ := openEngine(t, &loopbackNet{}, engineOpts{cfg: func(c *Config) { c.UploadLimitKBs = 400 }})
	hash, _ := e.Add(AddRequest{Torrent: c.torrent, Category: cat, SavePath: c.parent})
	waitFor(t, e, hash, "being finished", 20*time.Second, func(s Status) bool { return s.Progress >= 1 })
	start := time.Now()
	lt := leech(t, c, e.Port())
	select {
	case <-lt.Complete().On():
	case <-time.After(30 * time.Second):
		t.Fatal("the leecher never finished")
	}
	// 2 MB at 400 KB/s is about five seconds.
	if took := time.Since(start); took < 2*time.Second {
		t.Errorf("2 MB went out in %v despite a 400 KB/s limit", took)
	}
}

// The piece database is only a cache of what hashing the files finds out, so a
// damaged one is put aside and the files are checked again.
func TestDamagedPieceRecordsAreStartedAgain(t *testing.T) {
	c := makeContent(t, "Cache.2020", contentOpts{})
	dataDir := t.TempDir()
	e, _ := openEngine(t, &loopbackNet{}, engineOpts{dir: dataDir})
	hash, _ := e.Add(AddRequest{Torrent: c.torrent, Category: cat, SavePath: c.parent})
	waitFor(t, e, hash, "being finished", 20*time.Second, func(s Status) bool { return s.Progress >= 1 })
	closeEngine(t, e)

	db := filepath.Join(dataDir, "builtin", ".torrent.bolt.db")
	if err := os.WriteFile(db, []byte("this is not a database, at all, no matter what"), 0o600); err != nil {
		t.Fatal(err)
	}
	e2, err := Open(testConfig(t, &loopbackNet{}, engineOpts{dir: dataDir}))
	if err != nil {
		t.Fatal(err)
	}
	defer closeEngine(t, e2)
	waitFor(t, e2, hash, "the files to be checked again", 20*time.Second, func(s Status) bool { return s.Progress >= 1 })
	if m, _ := filepath.Glob(db + ".damaged-*"); len(m) != 1 {
		t.Errorf("the damaged database wasn't kept aside: %v", m)
	}
	// Not the torrent records: those, if damaged, are for a person to look at.
	closeEngine(t, e2)
	if err := os.WriteFile(filepath.Join(dataDir, "builtin", "state.db"), []byte("not a database either, not at all, nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(testConfig(t, &loopbackNet{}, engineOpts{dir: dataDir})); err == nil {
		t.Error("an engine opened with unreadable torrent records")
	}
}
