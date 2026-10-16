package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// Moving torrents between the dev stack's real qBittorrent, Transmission and
// Deluge (every pair, both directions). Needs REELKEEPER_TEST_QBIT_URL (plus
// _USER, _PASS) and REELKEEPER_TEST_TRANSMISSION_URL (plus _USER, _PASS), and
// docker, like the other TestReal* tests; with REELKEEPER_TEST_DELUGE_URL (plus
// _PASS) too, the pairs with Deluge run as well (they are skipped without it).
// `just test-clients` runs them.
//
// Nothing here may reach the internet or a real tracker, or touch any
// torrent but its own:
//
//   - Torrents are made up, private, and have no tracker (a magnet link's
//     tracker is at 127.0.0.1). The clients find each other because the
//     test tells one the other's address and port on the compose network.
//   - DHT, peer exchange and local peer discovery are switched OFF in all
//     clients for the length of the tests (and Transmission's blocklist
//     updates and port forwarding, Deluge's UPnP, NAT-PMP and release check
//     too), through their own settings APIs, and left off: the dev clients
//     never go on the internet. The incomplete-folder settings a test changes
//     are put back as they were.
//   - All the clients are left empty: a test removes what it added, with its
//     data (they are the dev stack's clients, nobody else's), and the labels
//     it made in Deluge.
//
// Reelkeeper looks at the files on disk, and on the Mac the clients' volumes
// aren't there, so the service is given a file system that works through
// `docker exec` in one of the containers (they see the same /mnt/downloads).

// ---- the real clients, isolated

type realWorld struct {
	t          *testing.T
	q          *QBittorrent
	tr         *Transmission
	d          *Deluge // nil when REELKEEPER_TEST_DELUGE_URL isn't set
	qc, tc, dc string  // container names
	fsC        string  // the container Reelkeeper "sees the disk" through
	qIP, tIP   string
	dIP        string
	qPort      int // the peer ports
	dPort      int
	qCfg, tCfg settings.DownloadClient
	dCfg       settings.DownloadClient
	// labels0 are the Deluge labels there were at the start.
	labels0 []string
}

const (
	tPeerPort = "51413"
	mvRoot    = "/mnt/downloads/mv"
)

func realClients(t *testing.T) *realWorld {
	t.Helper()
	qurl, turl := os.Getenv("REELKEEPER_TEST_QBIT_URL"), os.Getenv("REELKEEPER_TEST_TRANSMISSION_URL")
	if qurl == "" || turl == "" {
		t.Skip("set REELKEEPER_TEST_QBIT_URL and REELKEEPER_TEST_TRANSMISSION_URL (plus _USER and _PASS) to move torrents between real clients")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker isn't installed; these tests put files in the dev stack's containers")
	}
	w := &realWorld{t: t, qc: "reelkeeper-dev-qbittorrent", tc: "reelkeeper-dev-transmission"}
	if c := os.Getenv("REELKEEPER_TEST_TRANSMISSION_CONTAINER"); c != "" {
		w.tc = c
	}
	if c := os.Getenv("REELKEEPER_TEST_QBIT_CONTAINER"); c != "" {
		w.qc = c
	}
	w.fsC = w.tc
	w.dc = "reelkeeper-dev-deluge"
	if c := os.Getenv("REELKEEPER_TEST_DELUGE_CONTAINER"); c != "" {
		w.dc = c
	}
	w.qCfg = settings.DownloadClient{Type: settings.ClientQBittorrent, URL: qurl, Username: os.Getenv("REELKEEPER_TEST_QBIT_USER"), Password: os.Getenv("REELKEEPER_TEST_QBIT_PASS")}
	w.tCfg = settings.DownloadClient{Type: settings.ClientTransmission, URL: turl, Username: os.Getenv("REELKEEPER_TEST_TRANSMISSION_USER"), Password: os.Getenv("REELKEEPER_TEST_TRANSMISSION_PASS")}
	w.q = &QBittorrent{URL: qurl, Username: w.qCfg.Username, Password: w.qCfg.Password}
	w.tr = &Transmission{URL: turl, Username: w.tCfg.Username, Password: w.tCfg.Password}
	ctx := context.Background()
	if _, err := w.q.Version(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := w.tr.Version(ctx); err != nil {
		t.Fatal(err)
	}
	w.qIP = strings.Fields(dockerExec(t, w.qc, nil, "hostname -i"))[0]
	w.tIP = strings.Fields(dockerExec(t, w.tc, nil, "hostname -i"))[0]
	if durl := os.Getenv("REELKEEPER_TEST_DELUGE_URL"); durl != "" {
		w.dCfg = settings.DownloadClient{Type: settings.ClientDeluge, URL: durl, Password: os.Getenv("REELKEEPER_TEST_DELUGE_PASS")}
		w.d = &Deluge{URL: durl, Password: w.dCfg.Password}
		if _, err := w.d.Version(ctx); err != nil {
			t.Fatal(err)
		}
		w.dIP = strings.Fields(dockerExec(t, w.dc, nil, "hostname -i"))[0]
		if err := w.d.call(ctx, "list its labels", "label.get_labels", &w.labels0); err != nil {
			t.Fatal(err)
		}
		if err := w.d.call(ctx, "read its peer port", "core.get_listen_port", &w.dPort); err != nil || w.dPort == 0 {
			t.Fatalf("Deluge's peer port: %d %v", w.dPort, err)
		}
	}
	w.qPort = int(w.qPrefs()["listen_port"].(float64))
	w.isolate()
	// Both clients start empty, and end empty.
	w.assertEmpty("at the start")
	t.Cleanup(func() {
		w.clear()
		w.assertEmpty("at the end")
		w.dropLabels()
	})
	return w
}

// dropLabels takes the labels the tests made out of Deluge's Label plugin (the
// ones it had at the start stay).
func (w *realWorld) dropLabels() {
	if w.d == nil {
		return
	}
	var now []string
	if err := w.d.call(context.Background(), "list its labels", "label.get_labels", &now); err != nil {
		w.t.Errorf("Deluge's labels: %v", err)
		return
	}
	for _, l := range now {
		if !slices.Contains(w.labels0, l) {
			removeLabels(w.d, l)
		}
	}
}

// clients are the ones this run has.
func (w *realWorld) clients() []Client {
	cs := []Client{w.q, w.tr}
	if w.d != nil {
		cs = append(cs, w.d)
	}
	return cs
}

// qPrefs and the setters below are the clients' own settings APIs.
func (w *realWorld) qPrefs() map[string]any {
	w.t.Helper()
	b, status, err := w.q.do(context.Background(), "GET", "/api/v2/app/preferences", nil)
	if err != nil || status != 200 {
		w.t.Fatalf("qBittorrent preferences: %v %d", err, status)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *realWorld) qSet(kv map[string]any) {
	w.t.Helper()
	b, err := json.Marshal(kv)
	if err != nil {
		w.t.Fatal(err)
	}
	body, status, err := w.q.do(context.Background(), "POST", "/api/v2/app/setPreferences", form(url.Values{"json": {string(b)}}))
	if err != nil || status != 200 {
		w.t.Fatalf("set qBittorrent preferences %v: %v %d %s", kv, err, status, body)
	}
}

func (w *realWorld) tSession() map[string]any {
	w.t.Helper()
	var out map[string]any
	if err := w.tr.call(context.Background(), "read settings", "session-get", nil, &out); err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *realWorld) tSet(kv map[string]any) {
	w.t.Helper()
	if err := w.tr.call(context.Background(), "change settings", "session-set", kv, nil); err != nil {
		w.t.Fatalf("set Transmission settings %v: %v", kv, err)
	}
}

// changeQ and changeT set client settings for the rest of the test and put
// them back as they were when it ends.
func (w *realWorld) changeQ(kv map[string]any) {
	w.t.Helper()
	was := w.qPrefs()
	old := map[string]any{}
	for k := range kv {
		old[k] = was[k]
	}
	w.qSet(kv)
	w.t.Cleanup(func() { w.qSet(old) })
}

func (w *realWorld) changeT(kv map[string]any) {
	w.t.Helper()
	was := w.tSession()
	old := map[string]any{}
	for k := range kv {
		old[k] = was[k]
	}
	w.tSet(kv)
	w.t.Cleanup(func() { w.tSet(old) })
}

// isolate keeps the clients off the internet: no DHT, no peer exchange, no
// local peer discovery, no blocklist download, no port forwarding.
func (w *realWorld) isolate() {
	w.t.Helper()
	// Not put back afterwards: the dev clients stay off the internet.
	w.qSet(map[string]any{"dht": false, "pex": false, "lsd": false, "upnp": false})
	w.tSet(map[string]any{"dht-enabled": false, "pex-enabled": false, "lpd-enabled": false, "blocklist-enabled": false, "port-forwarding-enabled": false})
	if w.d != nil {
		isolateDeluge(w.t, w.d) // (checks that it took)
	}
	// Prove it, from what the clients say now.
	p, s := w.qPrefs(), w.tSession()
	for _, k := range []string{"dht", "pex", "lsd"} {
		if p[k] != false {
			w.t.Fatalf("qBittorrent %s is still on", k)
		}
	}
	for _, k := range []string{"dht-enabled", "pex-enabled", "lpd-enabled", "blocklist-enabled", "port-forwarding-enabled"} {
		if s[k] != false {
			w.t.Fatalf("Transmission %s is still on", k)
		}
	}
}

// clear removes everything the test added to either client, with its data.
func (w *realWorld) clear() {
	ctx := context.Background()
	for _, cat := range []string{settings.MovieCategory, settings.TVCategory, "reelkeeper-seeder"} {
		for _, c := range w.clients() {
			ts, err := c.Torrents(ctx, cat)
			if err != nil {
				continue
			}
			for _, tt := range ts {
				_ = c.Remove(ctx, cat, []string{tt.Hash}, true)
			}
		}
	}
	dockerExecQuiet(w.tc, "rm -rf "+mvRoot)
}

func (w *realWorld) assertEmpty(when string) {
	w.t.Helper()
	ctx := context.Background()
	var left []string
	for _, cat := range []string{settings.MovieCategory, settings.TVCategory, "reelkeeper-seeder"} {
		for _, c := range w.clients() {
			ts, err := c.Torrents(ctx, cat)
			if err != nil {
				w.t.Errorf("%s: %v", c.Name(), err)
				continue
			}
			for _, tt := range ts {
				left = append(left, c.Name()+" "+tt.Hash[:8])
			}
		}
	}
	if len(left) > 0 {
		w.t.Errorf("a client wasn't empty %s: %v", when, left)
	}
}

// dockerExecQuiet runs a command and ignores how it went.
func dockerExecQuiet(container, script string) {
	_ = exec.Command("docker", "exec", "-u", "root", container, "sh", "-c", script).Run()
}

// ---- Reelkeeper's view of the disk, through a container

type dockerFS struct{ container string }

func (d dockerFS) exists(p string) bool {
	return exec.Command("docker", "exec", "-u", "root", d.container, "test", "-e", p).Run() == nil
}

func (d dockerFS) rename(from, to string) error {
	out, err := exec.Command("docker", "exec", "-u", "root", d.container, "sh", "-c", `[ ! -e "$2" ] && mv "$1" "$2"`, "sh", from, to).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mv %s %s: %w %s", from, to, err, out)
	}
	return nil
}

// ---- a case: two clients, one pair of folders, one service

// side is one of the clients, with what a test needs to do with it.
type side struct {
	w         *realWorld
	name      string
	typ       string
	c         Client
	cfg       settings.DownloadClient
	container string
	ip        string
	port      int // its peer port
}

// pair is an old client and the new one a torrent moves to.
type pair struct{ old, nw string }

func (p pair) String() string { return p.old + " to " + p.nw }

func (w *realWorld) side(typ string) *side {
	switch typ {
	case settings.ClientQBittorrent:
		return &side{w: w, name: "qBittorrent", typ: typ, c: w.q, cfg: w.qCfg, container: w.qc, ip: w.qIP, port: w.qPort}
	case settings.ClientTransmission:
		port, _ := strconv.Atoi(tPeerPort)
		return &side{w: w, name: "Transmission", typ: typ, c: w.tr, cfg: w.tCfg, container: w.tc, ip: w.tIP, port: port}
	case settings.ClientDeluge:
		return &side{w: w, name: "Deluge", typ: typ, c: w.d, cfg: w.dCfg, container: w.dc, ip: w.dIP, port: w.dPort}
	}
	w.t.Fatalf("no client %q", typ)
	return nil
}

func (w *realWorld) sides(p pair) (old, nw *side) { return w.side(p.old), w.side(p.nw) }

// add puts a torrent in this client the way Reelkeeper does (a client may need
// the category made first).
func (s *side) add(a Add) (string, error) {
	ctx := context.Background()
	if err := s.c.EnsureCategory(ctx, a.Category, ""); err != nil {
		return "", err
	}
	return s.c.Add(ctx, a)
}

func (s *side) list(cat string) []Torrent {
	s.w.t.Helper()
	ts, err := s.c.Torrents(context.Background(), cat)
	if err != nil {
		s.w.t.Fatal(err)
	}
	return ts
}

func (s *side) find(cat, hash string) *Torrent {
	for _, tt := range s.list(cat) {
		if tt.Hash == hash {
			return &tt
		}
	}
	return nil
}

// waitFor polls this client's torrent until ok, or fails the test.
func (s *side) waitFor(cat, hash, what string, within time.Duration, ok func(Torrent) bool) Torrent {
	s.w.t.Helper()
	var last *Torrent
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if last = s.find(cat, hash); last != nil && ok(*last) {
			return *last
		}
	}
	if last == nil {
		s.w.t.Fatalf("%s: %s: torrent isn't listed", s.name, what)
	}
	s.w.t.Fatalf("%s: %s: state %q, progress %.2f, verified %d", s.name, what, last.State, last.Progress, last.Verified)
	return Torrent{}
}

// throttle limits how fast the torrent downloads, so a test can stop it part way.
func (s *side) throttle(hash string, bytesPerSecond int) {
	s.w.t.Helper()
	ctx := context.Background()
	if s.typ == settings.ClientQBittorrent {
		b, status, err := s.w.q.do(ctx, "POST", "/api/v2/torrents/setDownloadLimit", form(url.Values{"hashes": {hash}, "limit": {fmt.Sprint(bytesPerSecond)}}))
		if err != nil || status != 200 {
			s.w.t.Fatalf("limit: %v %d %s", err, status, b)
		}
		return
	}
	if s.typ == settings.ClientDeluge {
		// KiB/s; -1 is no limit. (Asked after the torrent has gone, it is
		// refused: nothing to undo then.)
		limit := -1.0
		if bytesPerSecond > 0 {
			limit = float64(bytesPerSecond) / 1024
		}
		err := s.w.d.call(ctx, "limit", "core.set_torrent_options", nil, []string{hash}, map[string]any{"max_download_speed": limit})
		if err != nil && bytesPerSecond > 0 {
			s.w.t.Fatal(err)
		}
		return
	}
	err := s.w.tr.call(ctx, "limit", "torrent-set", map[string]any{"ids": []string{hash}, "downloadLimited": bytesPerSecond > 0, "downloadLimit": bytesPerSecond / 1024}, nil)
	if err != nil {
		s.w.t.Fatal(err)
	}
}

// connect introduces two clients that both have a torrent, for the one
// downloading it to find the one seeding it. Whoever can ask for a connection
// does: Deluge (core.connect_peer) or qBittorrent (addPeers), the downloader
// first; Transmission has no such call, so when it is the downloader the
// seeder reaches out to it. Offered again and again by the callers: a try
// while the other is still checking, or before it is ready, gets no answer.
func (w *realWorld) connect(hash string, down, seed *side) {
	w.t.Helper()
	ctx := context.Background()
	from, to := down, seed
	if down.typ == settings.ClientTransmission {
		from, to = seed, down
	}
	switch from.typ {
	case settings.ClientQBittorrent:
		b, status, err := w.q.do(ctx, "POST", "/api/v2/torrents/addPeers", form(url.Values{"hashes": {hash}, "peers": {fmt.Sprintf("%s:%d", to.ip, to.port)}}))
		if err != nil || status != 200 {
			w.t.Fatalf("addPeers: %v %d %s", err, status, b)
		}
	case settings.ClientDeluge:
		// (A torrent Deluge doesn't have yet is refused; the caller tries again.)
		_ = w.d.call(ctx, "connect to a peer", "core.connect_peer", nil, hash, to.ip, to.port)
	default:
		w.t.Fatalf("%s can't connect to %s", from.name, to.name)
	}
}

func sha(t *testing.T, container, path string) string {
	t.Helper()
	return strings.Fields(dockerExec(t, container, nil, "sha256sum '"+path+"'"))[0]
}

func shaOf(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// ---- a torrent in the old client, in the state a test wants

type realCase struct {
	w        *realWorld
	t        *testing.T
	db       *store.Store
	f        *dfix
	old, nw  *side
	moves    *memMoves
	box      *secrets.Box
	name     string
	data     []byte // everything the torrent holds, in order
	files    []tfile
	raw      []byte
	hash     string
	finalDir string
	tempDir  string
	seedDir  string
	// oldDir is where a half-done download in the old client is kept
	// (finalDir, unless a test needs it somewhere else).
	oldDir string
	// oldBytes is what the old client had verified once it had stopped
	// (the record forgets it when the move is over).
	oldBytes int64
}

// tfile is one file of a torrent: its path inside the torrent's folder (for a
// single file, just its name) and what is in it.
type tfile struct {
	path string
	data []byte
}

// newRealCase prepares folders and a service that moves from old to new, for a
// torrent of one file.
func newRealCase(t *testing.T, db *store.Store, w *realWorld, p pair, name string, size int) *realCase {
	t.Helper()
	data := someData("move "+name, size)
	return newRealCaseOf(t, db, w, p, name, []tfile{{name, data}}, testTorrentFile(t, name, data))
}

// newRealCaseOf is newRealCase for any torrent: name is its (top folder's)
// name and files are in it; raw is the .torrent for them.
func newRealCaseOf(t *testing.T, db *store.Store, w *realWorld, p pair, name string, files []tfile, raw []byte) *realCase {
	t.Helper()
	c := &realCase{w: w, t: t, db: db, name: name, moves: &memMoves{}, files: files, raw: raw}
	c.old, c.nw = w.sides(p)
	// A folder name from the test's name: letters and digits only.
	slug := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, strings.ToLower(t.Name()))
	if len(slug) > 60 {
		slug = slug[len(slug)-60:]
	}
	c.finalDir, c.tempDir, c.seedDir = mvRoot+"/"+slug+"/final", mvRoot+"/"+slug+"/temp", mvRoot+"/"+slug+"/seed"
	dockerExec(t, w.tc, nil, fmt.Sprintf("mkdir -p '%s' '%s' '%s' && chown -R 1000:1000 '%s'", c.finalDir, c.tempDir, c.seedDir, mvRoot))
	for _, f := range files {
		c.data = append(c.data, f.data...)
	}
	c.hash, _ = InfoHash(c.raw)
	c.oldDir = c.finalDir

	box, err := secrets.New("a-test-key-of-sixteen-or-more")
	if err != nil {
		t.Fatal(err)
	}
	c.box = box
	c.f = newDFix(t, db)
	c.f.cfg = c.old.cfg
	c.wire(c.f.svc)
	return c
}

func (c *realCase) wire(s *Service) {
	fs := dockerFS{c.w.fsC}
	s.NewClient = newClient
	s.exists, s.rename = fs.exists, fs.rename
	s.moveTiming = moveTiming{poll: 200 * time.Millisecond, begin: 5 * time.Second, stop: 30 * time.Second, check: 2 * time.Minute, metadata: 20 * time.Second, files: 30 * time.Second, giveUp: time.Hour}
	s.SetSecrets(c.box)
	s.SetMoves(c.moves)
	s.freeSpace = func(string) int64 { return -1 }
}

// restart replaces the service by a new one, as Reelkeeper starting again.
func (c *realCase) restart() {
	f := c.f
	f.svc = NewService(c.db, func() settings.DownloadClient { return f.cfg }, fakeFetcher{ratio: 1.5}, f.imp, f.hist, slog.New(slog.DiscardHandler))
	c.wire(f.svc)
}

// track records the torrent as a download of the old client's. keep keeps
// the .torrent as a grab would.
func (c *realCase) track(status string, keep bool) {
	c.t.Helper()
	d := &Download{
		CreatedAt: time.Now().UTC(), Hash: c.hash, Category: settings.MovieCategory, ReleaseTitle: "Release " + c.name, Indexer: "Tracker", Quality: "WEBDL-1080p",
		Size: int64(len(c.data)), Status: status, CleanupTrack: "{}", Client: c.old.typ, TitleID: &c.f.mv.Title.ID, VersionID: &c.f.mv.Version.ID,
	}
	if _, err := c.db.DB.NewInsert().Model(d).Exec(context.Background()); err != nil {
		c.t.Fatal(err)
	}
	if keep {
		if err := c.f.svc.storeSource(context.Background(), d.ID, Source{Torrent: c.raw}); err != nil {
			c.t.Fatal(err)
		}
	}
}

// seedOld puts the whole file on disk and has the old client seed it.
func (c *realCase) seedOld(dir string, tags ...string) {
	c.t.Helper()
	c.put(dir)
	c.addTo(c.old, dir, false, tags...)
	c.old.waitFor(settings.MovieCategory, c.hash, "seeding", 60*time.Second, func(x Torrent) bool {
		return x.Done() && x.State != torrent.Checking && x.Verified == int64(len(c.data))
	})
	if err := c.old.c.NoShareLimits(context.Background(), settings.MovieCategory, []string{c.hash}); err != nil {
		c.t.Fatal(err)
	}
}

func (c *realCase) addTo(s *side, dir string, stopped bool, tags ...string) {
	c.t.Helper()
	if len(tags) == 0 {
		tags = []string{"reelkeeper", "Tracker"}
	}
	if _, err := s.add(Add{Torrent: c.raw, Category: settings.MovieCategory, Tags: tags, SavePath: dir, Stopped: stopped}); err != nil {
		c.t.Fatal(err)
	}
}

// partialOld has the old client download part of the file from a seeder in
// the new client, slowly, and returns once about frac of it has arrived. The
// seeder is then taken away (its file stays in the seed folder), so the new
// client has no copy of the torrent when the move begins.
func (c *realCase) partialOld(frac float64) {
	c.t.Helper()
	ctx := context.Background()
	c.put(c.seedDir)
	if _, err := c.nw.add(Add{Torrent: c.raw, Category: settings.MovieCategory, Tags: []string{"seeder"}, SavePath: c.seedDir}); err != nil {
		c.t.Fatal(err)
	}
	c.nw.waitFor(settings.MovieCategory, c.hash, "seeding", 60*time.Second, func(x Torrent) bool {
		return x.Done() && x.State != torrent.Checking
	})
	if err := c.nw.c.NoShareLimits(ctx, settings.MovieCategory, []string{c.hash}); err != nil {
		c.t.Fatal(err)
	}
	c.addTo(c.old, c.oldDir, false)
	c.old.throttle(c.hash, len(c.data)/12) // about 12 seconds for all of it
	for i := 0; ; i++ {
		// (Offered again every few seconds, not constantly: a connection
		// that is being made can be knocked over by another offer.)
		if i%16 == 0 {
			c.w.connect(c.hash, c.old, c.nw)
		}
		time.Sleep(500 * time.Millisecond)
		if x := c.old.find(settings.MovieCategory, c.hash); x != nil && x.Progress >= frac {
			break
		}
		if i > 160 {
			c.t.Fatalf("the old client never got to %.0f%%: %+v; the seeder: %+v", frac*100, c.old.find(settings.MovieCategory, c.hash), c.nw.find(settings.MovieCategory, c.hash))
		}
	}
	// The seeder goes (its data stays).
	if err := c.nw.c.Remove(ctx, settings.MovieCategory, []string{c.hash}, false); err != nil {
		c.t.Fatal(err)
	}
}

// move does what saving the other client does, and runs the task to the end.
func (c *realCase) move(leave bool) (*runReport, error) {
	c.t.Helper()
	ctx := context.Background()
	if err := c.f.svc.PlanMove(ctx, c.old.cfg, c.nw.cfg, leave); err != nil {
		c.t.Fatalf("PlanMove: %v", err)
	}
	if _, err := c.f.svc.BeginMove(ctx, c.old.cfg, c.nw.cfg, leave); err != nil {
		c.t.Fatalf("BeginMove: %v", err)
	}
	c.f.cfg = c.nw.cfg
	if _, err := c.f.svc.ClientChanged(ctx, c.old.name, c.nw.name); err != nil {
		c.t.Fatal(err)
	}
	return c.run()
}

func (c *realCase) run() (*runReport, error) {
	r := &runReport{}
	prev := c.f.svc.moveHook
	c.f.svc.moveHook = func(state string) error {
		if state == moveRenaming {
			c.oldBytes = c.download().MoveOldBytes
		}
		if prev != nil {
			return prev(state)
		}
		return nil
	}
	err := c.f.svc.RunMove(context.Background(), r)
	return r, err
}

func (c *realCase) download() *Download {
	c.t.Helper()
	d := new(Download)
	if err := c.db.DB.NewSelect().Model(d).Where("hash = ?", c.hash).Scan(context.Background()); err != nil {
		c.t.Fatal(err)
	}
	return d
}

func (c *realCase) history() []grab.Event {
	c.t.Helper()
	evs, err := c.f.hist.List(context.Background(), grab.ListOptions{})
	if err != nil {
		c.t.Fatal(err)
	}
	var out []grab.Event
	for _, e := range evs {
		if e.Event == grab.EventMoved || e.Event == grab.EventMoveFailed {
			out = append(out, e)
		}
	}
	return out
}

// assertMoved checks the common outcome of a good move: the new client has
// the torrent with at least what the old one had, the old one has let go,
// the record and History say so, and no data was lost.
func (c *realCase) assertMoved(oldVerified int64) Torrent {
	c.t.Helper()
	if c.f.svc.Moving() {
		c.t.Error("the move is still marked as running")
	}
	d := c.download()
	if d.Client != c.nw.typ || d.MoveState != "" || d.MoveLayout != "" || d.MovedAt == nil {
		c.t.Errorf("record = client %q state %q moved %v", d.Client, d.MoveState, d.MovedAt)
	}
	if x := c.old.find(settings.MovieCategory, c.hash); x != nil {
		c.t.Errorf("the old client (%s) still has the torrent: %+v", c.old.name, x)
	}
	x := c.nw.find(settings.MovieCategory, c.hash)
	if x == nil {
		c.t.Fatalf("the new client (%s) doesn't have the torrent", c.nw.name)
	}
	if x.Verified < oldVerified {
		c.t.Errorf("the new client verified %d, the old one had %d", x.Verified, oldVerified)
	}
	if ev := c.history(); len(ev) != 1 || ev[0].Event != grab.EventMoved {
		c.t.Errorf("history = %+v", ev)
	}
	return *x
}

// diskHas lists a folder's files (names only).
func (c *realCase) diskHas(dir string) string {
	c.t.Helper()
	return strings.Join(strings.Fields(dockerExec(c.t, c.w.tc, nil, "ls -A '"+dir+"' 2>/dev/null | sort")), " ")
}

// put writes the torrent's files, whole, into dir.
func (c *realCase) put(dir string) {
	c.t.Helper()
	for _, f := range c.files {
		sub, file := path.Split(f.path)
		putFile(c.t, c.w.tc, strings.TrimRight(dir+"/"+sub, "/"), file, f.data)
	}
	// (Every client runs as the same user, and the top folder is theirs too.)
	dockerExec(c.t, c.w.tc, nil, "chown -R 1000:1000 '"+dir+"'")
}

// differs says which of the torrent's files in dir isn't whole and as it should
// be ("" if all are).
func (c *realCase) differs(dir string) string {
	c.t.Helper()
	for _, f := range c.files {
		if got := sha(c.t, c.w.tc, dir+"/"+f.path); got != shaOf(f.data) {
			return f.path + " has checksum " + got
		}
	}
	return ""
}

var errCrash = errors.New("simulated crash")

// directions runs a test once for each way a torrent can move (a pair of the
// clients there are, either way round).
func directions(t *testing.T, fn func(t *testing.T, w *realWorld, db *store.Store, p pair)) {
	for _, p := range allPairs() {
		t.Run(p.String(), func(t *testing.T) {
			fn(t, realClients(t), storetest.Open(t, store.SQLite), p)
		})
	}
}

// allPairs are the pairs of clients the environment has, found by a quick
// look at which are set up (the clients themselves are made in each test).
func allPairs() []pair {
	types := []string{settings.ClientQBittorrent, settings.ClientTransmission}
	if os.Getenv("REELKEEPER_TEST_DELUGE_URL") != "" {
		types = append(types, settings.ClientDeluge)
	}
	var out []pair
	for _, o := range types {
		for _, n := range types {
			if o != n {
				out = append(out, pair{o, n})
			}
		}
	}
	return out
}
