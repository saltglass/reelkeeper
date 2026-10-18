package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	pp "github.com/anacrolix/torrent/peer_protocol"

	rktorrent "github.com/saltglass/reelkeeper/internal/torrent"
)

// The tracker's own words must reach TrackerMessages, because cleanup's
// "unregistered" rule reads them.
func TestTrackerMessages(t *testing.T) {
	tr := startTracker(t)
	c := makeContent(t, "Tracked.2020", contentOpts{announce: tr.url(), big: 100 << 10})
	tr.set(c.hash, "Unregistered torrent")
	nw := &loopbackNet{}
	e, _ := openEngine(t, nw, engineOpts{})
	hash, err := e.Add(AddRequest{Torrent: c.torrent, Category: cat})
	if err != nil {
		t.Fatal(err)
	}
	var msgs []string
	eventually(t, "the tracker's answer", func() bool {
		msgs, _ = e.TrackerMessages(cat, hash)
		return len(msgs) > 0
	})
	if len(msgs) != 1 || msgs[0] != "Unregistered torrent" {
		t.Fatalf("messages = %q", msgs)
	}
	if tr.called(c.hash) == 0 {
		t.Error("the stub tracker was never asked")
	}
	// A tracker that answers well gives no message, and the seed count it
	// reports is kept. (A tracker that failed is asked again only after a
	// while, so this is a second torrent; clearing is checked below.)
	c2 := makeContent(t, "Tracked.Fine.2020", contentOpts{announce: tr.url(), big: 100 << 10})
	s := startSeeder(t, seederOpts{}, c2)
	tr.set(c2.hash, "", s.addr())
	h2, _ := e.Add(AddRequest{Torrent: c2.torrent, Category: cat})
	unstick(t, engineTorrent(e, h2), nil)
	waitFor(t, e, h2, "the good tracker's peers to arrive", 20*time.Second, func(s Status) bool { return s.Progress >= 1 && s.SwarmSeeds == 7 })
	if msgs, _ := e.TrackerMessages(cat, h2); len(msgs) != 0 {
		t.Errorf("a tracker that answered well left a message: %q", msgs)
	}
	if nw.refused.Load() != 0 {
		t.Errorf("%d refused network uses", nw.refused.Load())
	}
}

func TestTrackerLogForgetsAFailureAfterAGoodAnnounce(t *testing.T) {
	l := newTrackerLog()
	l.record("ab", "http://t/announce", nil, fmt.Errorf(`tracker gave failure reason: "Torrent not found"`))
	l.record("ab", "http://u/announce", nil, fmt.Errorf("i/o timeout"))
	if got := l.messages("ab"); !reflect.DeepEqual(got, []string{"Torrent not found", "i/o timeout"}) {
		t.Fatalf("messages = %q", got)
	}
	l.record("ab", "http://t/announce", nil, nil)
	if got := l.messages("ab"); !reflect.DeepEqual(got, []string{"i/o timeout"}) {
		t.Errorf("after a good announce: %q", got)
	}
	if got := l.messages("cd"); got != nil {
		t.Errorf("another torrent has messages: %q", got)
	}
}

func TestFailureTextUnwrapsTheTrackersWords(t *testing.T) {
	for in, want := range map[string]string{
		`tracker gave failure reason: "Unregistered torrent"`: "Unregistered torrent",
		`tracker gave failure reason: "a \"quoted\" word"`:    `a "quoted" word`,
		"connection refused": "connection refused",
	} {
		if got := failureText(fmt.Errorf("%s", in)); got != want {
			t.Errorf("failureText(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every connection, listener, UDP socket and name lookup goes through the
// Network the engine was given.
func TestEverythingGoesThroughTheNetwork(t *testing.T) {
	tr := startTracker(t)
	c := makeContent(t, "Seam.2020", contentOpts{announce: tr.url()})
	s := startSeeder(t, seederOpts{}, c)
	tr.set(c.hash, "", s.addr())
	nw := &loopbackNet{}
	e, cfg := openEngine(t, nw, engineOpts{})
	// One TCP listener and one UDP socket, all from the Network (a second try
	// only if another program had the port picked).
	if nw.listens.Load() < 1 || nw.listens.Load() != nw.packets.Load() {
		t.Fatalf("starting listened %d times and opened %d UDP sockets, want the same number, at least one", nw.listens.Load(), nw.packets.Load())
	}
	hash, _ := e.Add(AddRequest{Torrent: c.torrent, Category: cat})
	unstick(t, engineTorrent(e, hash), nil)
	waitFor(t, e, hash, "finishing", 30*time.Second, func(s Status) bool { return s.Progress >= 1 })
	sameFiles(t, cfg.Folder, c)
	// The tracker (HTTP, by an address) and the peer were both dialled through it.
	if nw.dials.Load() < 2 {
		t.Errorf("the engine dialled through the Network %d times", nw.dials.Load())
	}
	nw.mu.Lock()
	dialed := strings.Join(nw.dialed, " ")
	nw.mu.Unlock()
	trackerHost := strings.TrimPrefix(tr.srv.URL, "http://")
	for _, want := range []string{trackerHost, s.addr()} {
		if !strings.Contains(dialed, want) {
			t.Errorf("%s wasn't dialled through the Network (%s)", want, dialed)
		}
	}
	// Names are looked up through it as well: a tracker named "localhost" is
	// resolved by the Network, and anacrolix is handed the address.
	before := nw.lookups.Load()
	c2 := makeContent(t, "Names.2020", contentOpts{announce: "http://localhost:" + strings.Split(trackerHost, ":")[1] + "/announce"})
	tr.set(c2.hash, "Not found")
	h2, _ := e.Add(AddRequest{Torrent: c2.torrent, Category: cat})
	eventually(t, "the tracker's name to be looked up and the tracker asked", func() bool {
		m, _ := e.TrackerMessages(cat, h2)
		return nw.lookups.Load() > before && len(m) == 1
	})
	if nw.refused.Load() != 0 {
		t.Errorf("%d times the engine tried to leave loopback", nw.refused.Load())
	}
}

// udp trackers are given as addresses; their names are looked up through the
// Network, never by anacrolix's own resolver.
func TestUDPTrackerNamesAreResolvedThroughTheNetwork(t *testing.T) {
	known, names := splitTrackers([][]string{
		{"http://tracker.example/announce", "udp://203.0.113.9:6969/announce", "udp://open.tracker.example:1337/announce", "wss://ws.example/", "ftp://nope/"},
		{"udp://[2001:db8::1]:80/announce"},
		{"https://tls.example/announce"},
	})
	want := [][]string{
		{"http://tracker.example/announce", "udp://203.0.113.9:6969/announce"},
		{"udp://[2001:db8::1]:80/announce"},
		{"https://tls.example/announce"},
	}
	if !reflect.DeepEqual(known, want) || !reflect.DeepEqual(names, []string{"udp://open.tracker.example:1337/announce"}) {
		t.Fatalf("known = %v, names = %v", known, names)
	}

	c := makeContent(t, "Udp.2020", contentOpts{announce: "udp://localhost:9/announce"})
	nw := &loopbackNet{}
	e, _ := openEngine(t, nw, engineOpts{})
	hash, _ := e.Add(AddRequest{Torrent: c.torrent, Category: cat})
	eventually(t, "the name to be looked up", func() bool { return nw.lookups.Load() > 0 })
	eventually(t, "the address to be known", func() bool {
		e.trk.mu.Lock()
		defer e.trk.mu.Unlock()
		return e.trk.names["udp://127.0.0.1:9/announce"] == "udp://localhost:9/announce"
	})
	_ = hash
}

// The ways the engine could reach out without the user knowing are off, and
// stay off.
func TestEngineConfigurationNeverOpensThingsItShouldNot(t *testing.T) {
	e, _ := openEngine(t, &loopbackNet{}, engineOpts{})
	cfg := e.clientConfig()
	for name, ok := range map[string]bool{
		"UPnP and NAT-PMP port mapping are off":      cfg.NoDefaultPortForwarding,
		"WebTorrent is off":                          cfg.DisableWebtorrent,
		"web seeds are off":                          cfg.DisableWebseeds,
		"anacrolix's own TCP socket is off":          cfg.DisableTCP,
		"anacrolix's own uTP socket is off":          cfg.DisableUTP,
		"anacrolix's own DHT is off":                 cfg.NoDHT,
		"anacrolix's own DHT announcing is off":      !cfg.PeriodicallyAnnounceTorrentsToDht,
		"no proxy from the environment":              cfg.HTTPProxy == nil,
		"trackers are dialled through the Network":   cfg.TrackerDialContext != nil && cfg.TrackerListenPacket != nil,
		"web requests are dialled through Network":   cfg.HTTPDialContext != nil,
		"the DHT's entry points are looked up there": cfg.DhtStartingNodes != nil,
		"there is no folder anything could go to":    cfg.DefaultStorage != nil,
		"peer exchange follows the setting (off)":    cfg.DisablePEX,
	} {
		if !ok {
			t.Errorf("not so: %s", name)
		}
	}
	// There is no local service discovery in anacrolix to switch off, and no
	// default tracker list: pin both, so an upgrade that adds either is noticed.
	for _, f := range reflect.VisibleFields(reflect.TypeOf(*cfg)) {
		n := strings.ToLower(f.Name)
		if strings.Contains(n, "lsd") || strings.Contains(n, "localservice") || strings.Contains(n, "defaulttracker") || strings.Contains(n, "publictracker") {
			t.Errorf("anacrolix's configuration gained %s: decide whether the engine must turn it off", f.Name)
		}
	}
	// And nothing was listening on a port nobody asked for.
	if e.Port() == 0 {
		t.Error("no port")
	}
}

// A private torrent never uses the DHT or peer exchange; a public one does.
func TestPrivateTorrentsNeverUseTheDHTOrPeerExchange(t *testing.T) {
	pub := makeContent(t, "Public.2020", contentOpts{})
	priv := makeContent(t, "Private.2020", contentOpts{private: true})
	// The seeder offers peer exchange to everyone who speaks it.
	s := startSeeder(t, seederOpts{pex: true}, pub, priv)
	nw := &loopbackNet{}
	e, _ := openEngine(t, nw, engineOpts{cfg: func(c *Config) { c.DHT, c.PEX = true, true }})
	var announced = map[string]*atomic.Int32{pub.hash: {}, priv.hash: {}}
	e.mu.Lock()
	e.announceDHT = func(tt *torrent.Torrent) { announced[tt.InfoHash().HexString()].Add(1) }
	e.mu.Unlock()
	if e.dht == nil {
		t.Fatal("the DHT should be on")
	}

	hPub, _ := e.Add(AddRequest{Torrent: pub.torrent, Category: cat})
	hPriv, _ := e.Add(AddRequest{Torrent: priv.torrent, Category: cat})
	feed(t, e, hPub, s)
	feed(t, e, hPriv, s)
	waitFor(t, e, hPub, "the public one to finish", 30*time.Second, func(s Status) bool { return s.Progress >= 1 })
	waitFor(t, e, hPriv, "the private one to finish", 30*time.Second, func(s Status) bool { return s.Progress >= 1 })

	eventually(t, "the public torrent to be announced to the DHT", func() bool { return announced[pub.hash].Load() > 0 })
	time.Sleep(500 * time.Millisecond)
	if n := announced[priv.hash].Load(); n != 0 {
		t.Errorf("the private torrent was announced to the DHT %d times", n)
	}

	// Peer exchange: what the seeder learned about us in each torrent's
	// extended handshake.
	ext := func(c *content) map[pp.ExtensionName]pp.ExtensionNumber { return s.extensions(c.hash) }
	if ext(pub) == nil || ext(priv) == nil {
		t.Fatalf("the seeder saw no extended handshake: public %v, private %v", ext(pub), ext(priv))
	}
	if _, ok := ext(pub)[pp.ExtensionNamePex]; !ok {
		t.Errorf("the public torrent should speak peer exchange: %v", ext(pub))
	}
	if _, ok := ext(priv)[pp.ExtensionNamePex]; ok {
		t.Errorf("the private torrent offered peer exchange: %v", ext(priv))
	}
}

func TestFilesGoneMeansMissingFilesAndComingBackClearsIt(t *testing.T) {
	c := makeContent(t, "Gone.2020", contentOpts{})
	e, cfg := openEngine(t, &loopbackNet{}, engineOpts{})
	hash, _ := e.Add(AddRequest{Torrent: c.torrent, Category: cat, SavePath: c.parent})
	waitFor(t, e, hash, "being finished", 20*time.Second, func(s Status) bool { return s.Progress >= 1 })
	_ = cfg
	file := filepath.Join(c.root, "movie.mkv")
	saved, _ := os.ReadFile(file)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	e.sweepFiles()
	st := status(t, e, hash)
	if st.State != rktorrent.MissingFiles || isRunning(e, hash) {
		t.Fatalf("with a file gone: %+v", st)
	}
	if err := os.WriteFile(file, saved, 0o644); err != nil {
		t.Fatal(err)
	}
	e.sweepFiles()
	if st := status(t, e, hash); !sharing(st.State) {
		t.Fatalf("with the file back: %s", st.State)
	}
}

// The sweep looks at files without the engine's lock, so a torrent can be
// moved, checked or removed meanwhile. An answer about where its files used to
// be must then be dropped, not mark it missing.
func TestAFileSweepThatRacesAChangeIsDropped(t *testing.T) {
	c := makeContent(t, "Raced.2020", contentOpts{})
	e, _ := openEngine(t, &loopbackNet{}, engineOpts{})
	hash, _ := e.Add(AddRequest{Torrent: c.torrent, Category: cat, SavePath: c.parent})
	waitFor(t, e, hash, "being finished", 20*time.Second, func(s Status) bool { return s.Progress >= 1 })
	file := filepath.Join(c.root, "movie.mkv")
	saved, _ := os.ReadFile(file)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(en *entry) (undo func()){
		"moved": func(en *entry) func() {
			was := en.rec.SavePath
			en.rec.SavePath = t.TempDir()
			return func() { en.rec.SavePath = was }
		},
		"being checked": func(en *entry) func() {
			en.verifying = true
			return func() { en.verifying = false }
		},
		"being moved": func(en *entry) func() {
			en.busy = "moving"
			return func() { en.busy = "" }
		},
		"removed and added again": func(en *entry) func() {
			again := *en
			e.ents[hash] = &again
			return func() { e.ents[hash] = en }
		},
	}
	for name, change := range changes {
		e.mu.Lock()
		checks := e.fileChecksLocked()
		e.mu.Unlock()
		lookForFiles(checks)
		e.mu.Lock()
		en := e.ents[hash]
		undo := change(en)
		e.applyFileChecksLocked(checks)
		missing := e.ents[hash].missing
		undo()
		e.mu.Unlock()
		if missing {
			t.Errorf("%s while the files were looked at: marked missing anyway", name)
		}
	}
	// With nothing changed, the same answer counts.
	e.sweepFiles()
	if st := status(t, e, hash); st.State != rktorrent.MissingFiles {
		t.Errorf("nothing changed: %s", st.State)
	}
	_ = os.WriteFile(file, saved, 0o644)
}

func TestRecheckFindsDamageAndStaysOffTheNetwork(t *testing.T) {
	c := makeContent(t, "Damage.2020", contentOpts{})
	nw := &loopbackNet{}
	e, _ := openEngine(t, nw, engineOpts{})
	hash, _ := e.Add(AddRequest{Torrent: c.torrent, Category: cat, SavePath: c.parent, Stopped: true})
	check := func() Status {
		t.Helper()
		if err := e.Recheck(cat, hash); err != nil {
			t.Fatal(err)
		}
		if st := status(t, e, hash); st.State != rktorrent.Checking {
			t.Fatalf("right after Recheck: %s", st.State)
		}
		return waitFor(t, e, hash, "the check", 20*time.Second, func(s Status) bool { return s.State != rktorrent.Checking })
	}
	// Whole: everything verifies, the torrent stays stopped.
	st := check()
	if st.Have != c.totalLen || st.State != rktorrent.SeedingPaused {
		t.Errorf("whole: %+v", st)
	}
	// Damaged in the middle of the big file.
	f := filepath.Join(c.root, "movie.mkv")
	b, _ := os.ReadFile(f)
	b[300<<10] ^= 0xff
	if err := os.WriteFile(f, b, 0o644); err != nil {
		t.Fatal(err)
	}
	st = check()
	if st.Have >= c.totalLen || st.Have < c.totalLen-64<<10 || st.State != rktorrent.Paused {
		t.Errorf("damaged: have %d of %d, state %s", st.Have, c.totalLen, st.State)
	}
	if nw.dials.Load() != 0 {
		t.Error("checking used the network")
	}
}

func TestSetFolderMovesTheFilesAndKeepsTheRest(t *testing.T) {
	c := makeContent(t, "Move.Me.2020", contentOpts{})
	e, _ := openEngine(t, &loopbackNet{}, engineOpts{})
	hash, _ := e.Add(AddRequest{Torrent: c.torrent, Category: cat, SavePath: c.parent, Stopped: true})
	dest := t.TempDir()
	mustWrite(t, filepath.Join(c.root, "not-in-torrent.txt"), "stays")
	if err := e.SetFolder(cat, hash, dest); err != nil {
		t.Fatal(err)
	}
	sameFiles(t, dest, c)
	if st := status(t, e, hash); st.SavePath != dest {
		t.Errorf("save path = %s", st.SavePath)
	}
	// Only the torrent's files left; the other file is still where it was.
	if b, err := os.ReadFile(filepath.Join(c.root, "not-in-torrent.txt")); err != nil || string(b) != "stays" {
		t.Errorf("the other file: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(c.root, "movie.mkv")); err == nil {
		t.Error("the file is in both places")
	}
	// It checks out in its new place.
	if err := e.Recheck(cat, hash); err != nil {
		t.Fatal(err)
	}
	st := waitFor(t, e, hash, "the check", 20*time.Second, func(s Status) bool { return s.State != rktorrent.Checking })
	if st.Have != c.totalLen {
		t.Errorf("after the move the check found %d of %d", st.Have, c.totalLen)
	}
	// Nothing is overwritten: with a file in the way, moving back is refused
	// and neither file is touched.
	mustWrite(t, filepath.Join(c.root, "movie.mkv"), "in the way")
	if err := e.SetFolder(cat, hash, c.parent); err == nil {
		t.Error("a file in the way was overwritten")
	}
	if b, _ := os.ReadFile(filepath.Join(c.root, "movie.mkv")); string(b) != "in the way" {
		t.Error("the file in the way was changed")
	}
	if st := status(t, e, hash); st.SavePath != dest {
		t.Errorf("a refused move changed the save path to %s", st.SavePath)
	}
	sameFiles(t, dest, c)
}

func TestVersion(t *testing.T) {
	if v := Version(); !strings.HasPrefix(v, "Built-in (anacrolix/torrent v") {
		t.Errorf("Version() = %q", v)
	}
}
