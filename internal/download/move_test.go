package download

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
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

// ---- a pretend disk, shared by Reelkeeper and both clients

type fakeDisk struct {
	files     map[string]int64
	renames   [][2]string
	renameErr map[string]error // by the path being renamed
}

func newFakeDisk() *fakeDisk {
	return &fakeDisk{files: map[string]int64{}, renameErr: map[string]error{}}
}

func (d *fakeDisk) exists(p string) bool     { _, ok := d.files[p]; return ok }
func (d *fakeDisk) put(p string, size int64) { d.files[p] = size }
func (d *fakeDisk) rename(from, to string) error {
	if err := d.renameErr[from]; err != nil {
		return err
	}
	if _, ok := d.files[from]; !ok {
		return fmt.Errorf("rename %s: no such file", from)
	}
	if _, ok := d.files[to]; ok {
		return fmt.Errorf("rename %s: %s exists", from, to)
	}
	d.files[to] = d.files[from]
	delete(d.files, from)
	d.renames = append(d.renames, [2]string{from, to})
	return nil
}
func (d *fakeDisk) names() []string {
	var out []string
	for p := range d.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ---- a pretend download client that records every call

type simTorrent struct {
	Torrent
	files    []File
	folder   string
	look     []string
	filesIn  string // where this client looks for the files
	src      Source
	srcErr   error
	paused   bool // the state it goes back to when not checking
	pendStop int  // list calls until a stop shows
	pendChk  int  // list calls until a check is over
	grows    int64
	noPeers  bool // its details never arrive
}

// simClient is a download client with a memory of what it was asked. Its
// checks look at the shared disk the way a real client's do: a file counts
// when it is there under a name this client knows.
type simClient struct {
	t     *testing.T
	name  string
	disk  *fakeDisk
	cat   map[string][]File // every torrent's files, by hash (the torrent file's content)
	tors  map[string]*simTorrent
	calls []string
	// ownSuffix is the ending this client gives unfinished files ("" counts
	// plain names only); renamesOnCheck makes it rename unfinished files to
	// that ending as it checks them, as Transmission does.
	ownSuffix, renamesOnCheck string
	stopLag, checkLag         int
	staleVerified             int64 // what a new torrent claims before it is checked
	failOn                    map[string]error
	deleted                   []string
	added                     []Add
}

func newSim(t *testing.T, name string, disk *fakeDisk, cat map[string][]File, ownSuffix string) *simClient {
	return &simClient{t: t, name: name, disk: disk, cat: cat, tors: map[string]*simTorrent{}, ownSuffix: ownSuffix, failOn: map[string]error{}}
}

func (c *simClient) log(what string, hashes ...string) {
	for _, h := range hashes {
		c.calls = append(c.calls, what+" "+h[:4])
	}
}

func (c *simClient) fail(op string) error { return c.failOn[op] }

func (c *simClient) Name() string                                         { return c.name }
func (c *simClient) Version(context.Context) (string, error)              { return "sim", c.fail("version") }
func (c *simClient) EnsureCategory(context.Context, string, string) error { return nil }
func (c *simClient) MoveCategory(context.Context, string, string, []string) error {
	return errors.New("not used by a move")
}
func (c *simClient) TrackerMessages(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func (c *simClient) Add(_ context.Context, a Add) (string, error) {
	if err := c.fail("add"); err != nil {
		return "", err
	}
	var h string
	var err error
	if len(a.Torrent) > 0 {
		h, err = InfoHash(a.Torrent)
	} else {
		h, err = MagnetHash(a.Magnet)
	}
	if err != nil {
		return "", err
	}
	c.log("add", h)
	c.added = append(c.added, a)
	files, ok := c.cat[h]
	if !ok && len(a.Magnet) == 0 {
		c.t.Fatalf("%s: added a torrent nobody made", c.name)
	}
	in := a.SavePath
	if a.FilesIn != "" {
		in = a.FilesIn
	}
	t := &simTorrent{files: files, filesIn: in, paused: a.Stopped, folder: a.SavePath}
	t.Hash, t.Category, t.SavePath, t.Tags = h, a.Category, in, a.Tags
	// A magnet link has no details until the client has fetched them.
	if len(a.Torrent) > 0 {
		t.Size = sizeOf(files)
		t.Verified = c.staleVerified // a number from before any check
		if t.Verified > 0 {
			t.Progress = float64(t.Verified) / float64(t.Size)
		}
	}
	c.settle(t)
	c.tors[h] = t
	return h, nil
}

func sizeOf(fs []File) int64 {
	var n int64
	for _, f := range fs {
		n += f.Size
	}
	return n
}

// tick moves time on one list call: stops take effect, checks finish.
func (c *simClient) tick(t *simTorrent) {
	if t.pendStop > 0 {
		t.Verified += t.grows // still writing pieces until it has stopped
		t.pendStop--
		if t.pendStop == 0 {
			c.settle(t)
		}
	}
	if t.pendChk > 0 {
		t.pendChk--
		if t.pendChk == 0 {
			c.finishCheck(t)
		}
	}
}

// settle puts a torrent in its resting state.
func (c *simClient) settle(t *simTorrent) {
	switch {
	case t.pendChk > 0:
		t.State = torrent.Checking
	case t.paused && t.Size > 0 && t.Progress >= 1:
		t.State = torrent.SeedingPaused
	case t.paused:
		t.State = torrent.Paused
	case t.Size == 0:
		t.State = torrent.Metadata // no details yet
	case t.Progress >= 1:
		t.State = torrent.Seeding
	default:
		t.State = torrent.Downloading
	}
}

func (c *simClient) finishCheck(t *simTorrent) {
	// What counts is the data in the file, which the fake disk keeps as its
	// size: a file under a name this client knows is checked, any other isn't.
	var got int64
	for _, f := range t.files {
		p := t.filesIn + "/" + f.Name
		switch {
		case c.disk.exists(p):
			got += c.disk.files[p]
		case c.ownSuffix != "" && c.disk.exists(p+c.ownSuffix):
			got += c.disk.files[p+c.ownSuffix]
		}
	}
	t.Verified = got
	t.Left = t.Size - got
	t.Progress = 0
	if t.Size > 0 {
		t.Progress = float64(got) / float64(t.Size)
	}
	if c.renamesOnCheck != "" && got < t.Size {
		// Transmission names a file it has not finished ".part".
		for _, f := range t.files {
			p := t.filesIn + "/" + f.Name
			if c.disk.exists(p) {
				_ = c.disk.rename(p, p+c.renamesOnCheck)
			}
		}
	}
	t.pendChk = 0
	c.settle(t)
}

func (c *simClient) Torrents(_ context.Context, cat string) ([]Torrent, error) {
	if err := c.fail("list"); err != nil {
		return nil, err
	}
	var out []Torrent
	for _, t := range c.tors {
		if t.Category != cat {
			continue
		}
		c.tick(t)
		out = append(out, t.Torrent)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hash < out[j].Hash })
	return out, nil
}

func (c *simClient) Remove(_ context.Context, _ string, hashes []string, deleteFiles bool) error {
	for _, h := range hashes {
		c.log("remove", h)
		if deleteFiles {
			c.deleted = append(c.deleted, h)
		}
		delete(c.tors, h)
	}
	return c.fail("remove")
}

func (c *simClient) NoShareLimits(_ context.Context, _ string, hashes []string) error {
	if err := c.fail("limits"); err != nil {
		return err
	}
	for _, h := range hashes {
		c.log("limits", h)
		if t := c.tors[h]; t != nil {
			t.paused = false
			c.settle(t)
		}
	}
	return nil
}

func (c *simClient) TopPriority(_ context.Context, _ string, hashes []string) error {
	c.log("top", hashes...)
	return nil
}

func (c *simClient) Stop(_ context.Context, _ string, hashes []string) error {
	if err := c.fail("stop"); err != nil {
		return err
	}
	for _, h := range hashes {
		c.log("stop", h)
		if t := c.tors[h]; t != nil {
			t.paused = true
			t.pendStop = c.stopLag
			if c.stopLag == 0 {
				c.settle(t)
			}
		}
	}
	return nil
}

func (c *simClient) Start(_ context.Context, _ string, hashes []string) error {
	if err := c.fail("start"); err != nil {
		return err
	}
	for _, h := range hashes {
		c.log("start", h)
		if t := c.tors[h]; t != nil {
			t.paused = false
			if t.Size == 0 && !t.noPeers {
				// Details arrive as soon as it runs.
				t.files = c.cat[h]
				t.Size = sizeOf(t.files)
			}
			c.settle(t)
		}
	}
	return nil
}

func (c *simClient) Source(_ context.Context, _, hash string) (Source, error) {
	c.log("source", hash)
	t := c.tors[hash]
	if t == nil {
		return Source{}, errors.New("no such torrent")
	}
	return t.src, t.srcErr
}

func (c *simClient) Layout(_ context.Context, _, hash string) (Layout, error) {
	c.log("layout", hash)
	t := c.tors[hash]
	if t == nil {
		return Layout{}, errors.New("no such torrent")
	}
	return Layout{Files: t.files, Folder: t.folder, Look: t.look}, nil
}

func (c *simClient) Recheck(_ context.Context, _, hash string) error {
	if err := c.fail("recheck"); err != nil {
		return err
	}
	c.log("recheck", hash)
	t := c.tors[hash]
	if t == nil {
		return errors.New("no such torrent")
	}
	if !t.Stopped() {
		c.t.Errorf("%s: asked to check %s while it was running", c.name, hash[:4])
	}
	t.pendChk = c.checkLag + 1
	t.State = torrent.Checking
	if c.checkLag == 0 {
		t.pendChk = 0
		c.finishCheck(t)
	}
	return nil
}

// SetFolder moves the files, as Transmission's set-location does.
func (c *simClient) SetFolder(_ context.Context, _, hash, folder string) error {
	c.log("setfolder", hash)
	t := c.tors[hash]
	if t == nil {
		return errors.New("no such torrent")
	}
	for _, f := range t.files {
		for _, suffix := range []string{"", c.ownSuffix, ".part"} {
			from := t.filesIn + "/" + f.Name + suffix
			if c.disk.exists(from) {
				_ = c.disk.rename(from, folder+"/"+f.Name+suffix)
				break
			}
		}
	}
	t.filesIn, t.SavePath = folder, folder
	return nil
}

// ---- the world a move test lives in

// memMoves is MoveStore in memory.
type memMoves struct{ m settings.DownloadMove }

func (s *memMoves) DownloadMove() settings.DownloadMove { return s.m }
func (s *memMoves) SaveDownloadMove(_ context.Context, m settings.DownloadMove) error {
	s.m = m
	return nil
}
func (s *memMoves) ClearDownloadMove(context.Context) error {
	s.m = settings.DownloadMove{}
	return nil
}

type mover struct {
	t       *testing.T
	db      *store.Store
	f       *dfix
	disk    *fakeDisk
	cat     map[string][]File
	old, nw *simClient
	moves   *memMoves
	oldCfg  settings.DownloadClient
	newCfg  settings.DownloadClient
	box     *secrets.Box
	n       int
}

const (
	finalDir = "/data/final"
	tempDir  = "/data/temp"
)

// newMover sets up qBittorrent (old) and Transmission (new), or the other way round.
func newMover(t *testing.T, db *store.Store, oldType, newType string) *mover {
	t.Helper()
	m := &mover{t: t, db: db, disk: newFakeDisk(), cat: map[string][]File{}, moves: &memMoves{}}
	box, err := secrets.New("a-test-key-of-sixteen-or-more")
	if err != nil {
		t.Fatal(err)
	}
	m.box = box
	// Deluge keeps unfinished files under their plain names.
	suffix := map[string]string{settings.ClientQBittorrent: ".!qB", settings.ClientTransmission: ".part", settings.ClientDeluge: ""}
	m.old = newSim(t, settings.ClientDisplayName(oldType), m.disk, m.cat, suffix[oldType])
	m.nw = newSim(t, settings.ClientDisplayName(newType), m.disk, m.cat, suffix[newType])
	if newType == settings.ClientTransmission {
		m.nw.renamesOnCheck = ".part"
	}
	m.oldCfg = settings.DownloadClient{Type: oldType, URL: "http://old", Username: "u", Password: "p"}
	m.newCfg = settings.DownloadClient{Type: newType, URL: "http://new", Username: "u2", Password: "p2"}
	m.f = newDFix(t, db)
	m.f.cfg = m.oldCfg
	m.wire(m.f.svc)
	t.Cleanup(func() {
		for _, c := range []*simClient{m.old, m.nw} {
			if len(c.deleted) > 0 {
				t.Errorf("%s was asked to delete the data of %v", c.name, c.deleted)
			}
		}
	})
	return m
}

func (m *mover) wire(s *Service) {
	s.NewClient = func(c settings.DownloadClient) Client {
		if c.URL == "http://old" {
			return m.old
		}
		return m.nw
	}
	s.exists, s.rename = m.disk.exists, m.disk.rename
	s.moveTiming = moveTiming{poll: time.Microsecond, begin: 5 * time.Millisecond, stop: 50 * time.Millisecond, check: 200 * time.Millisecond,
		metadata: 50 * time.Millisecond, files: 20 * time.Millisecond, giveUp: moveGiveUp}
	s.SetSecrets(m.box)
	s.SetMoves(m.moves)
	s.freeSpace = func(string) int64 { return -1 }
}

// restart replaces the service by a new one over the same database and
// clients, as Reelkeeper starting again would.
func (m *mover) restart() {
	f := m.f
	f.svc = NewService(m.db, func() settings.DownloadClient { return f.cfg }, fakeFetcher{ratio: 1.5}, f.imp, f.hist, slog.New(slog.DiscardHandler))
	m.wire(f.svc)
}

// seed describes a torrent in the old client.
type seed struct {
	status  string // the download's status (default: downloading)
	file    string // the torrent's one file (default: Movie.mkv)
	size    int64  // default 1000
	have    int64  // bytes the old client has verified
	paused  bool
	onDisk  string   // "" (nothing), "plain", "unfinished" (the old client's ending), "part", "plainTemp", "unfinishedTemp"
	magnet  bool     // keep a magnet link as the source (default: a .torrent)
	noSrc   string   // the old client can't hand the torrent back, and nothing was kept: its reason
	details bool     // the old client is still fetching details
	tags    []string // the old client's tags (default: reelkeeper and the indexer; Deluge has none)
	// requested: a Seerr request, noted on Reelkeeper's own record.
	requested bool
	seeded    time.Duration
	upload    int64
	grows     int64
}

// seed adds a torrent to the old client, its files to the disk, and its
// record to the database. It returns the hash.
func (m *mover) seed(s seed) string {
	t := m.t
	t.Helper()
	m.n++
	if s.file == "" {
		s.file = fmt.Sprintf("Movie%d.mkv", m.n)
	}
	if s.size == 0 {
		s.size = 1000
	}
	if s.status == "" {
		s.status = StatusDownloading
	}
	data := someData(s.file, int(s.size))
	raw := testTorrentFile(t, s.file, data)
	hash, err := InfoHash(raw)
	if err != nil {
		t.Fatal(err)
	}
	files := []File{{Name: s.file, Size: s.size}}
	m.cat[hash] = files
	done := s.have >= s.size
	st := torrent.Downloading
	switch {
	case s.details:
		st = torrent.Metadata
	case s.paused && done:
		st = torrent.SeedingPaused
	case s.paused:
		st = torrent.Paused
	case done:
		st = torrent.Seeding
	}
	tags := s.tags
	if tags == nil && m.oldCfg.Type != settings.ClientDeluge {
		tags = []string{"reelkeeper", "Tracker"}
	}
	tor := &simTorrent{files: files, folder: finalDir, look: []string{finalDir, tempDir}, filesIn: finalDir, paused: s.paused, grows: s.grows}
	tor.Hash, tor.Category, tor.State, tor.Size, tor.Verified, tor.Tags = hash, settings.MovieCategory, st, s.size, s.have, tags
	tor.SavePath = finalDir
	tor.SeedingTime, tor.Uploaded = s.seeded, s.upload
	tor.Progress = float64(s.have) / float64(s.size)
	tor.Left = s.size - s.have
	if s.details {
		tor.files, tor.Size, tor.Progress, tor.Left = nil, 0, 0, 0
	}
	switch {
	case s.noSrc != "":
		tor.srcErr = &NoSourceError{s.noSrc}
	case s.magnet:
		tor.src = Source{Magnet: "magnet:?xt=urn:btih:" + hash}
	default:
		tor.src = Source{Torrent: raw}
	}
	m.old.tors[hash] = tor
	// The data on disk is as much as the client has verified (a file's size
	// on the fake disk is the bytes of real data in it).
	switch s.onDisk {
	case "plain":
		m.disk.put(finalDir+"/"+s.file, s.have)
	case "unfinished":
		m.disk.put(finalDir+"/"+s.file+m.old.ownSuffix, s.have)
	case "part":
		m.disk.put(finalDir+"/"+s.file+".part", s.have)
	case "plainTemp":
		m.disk.put(tempDir+"/"+s.file, s.have)
		tor.filesIn = tempDir
	case "unfinishedTemp":
		m.disk.put(tempDir+"/"+s.file+m.old.ownSuffix, s.have)
		tor.filesIn = tempDir
	}
	d := &Download{
		CreatedAt: time.Now().UTC(), Hash: hash, Category: settings.MovieCategory, ReleaseTitle: "Release " + s.file, Indexer: "Tracker", Quality: "WEBDL-1080p",
		Size: s.size, Status: s.status, CleanupTrack: "{}", Client: m.oldCfg.Type,
		TitleID: &m.f.mv.Title.ID, VersionID: &m.f.mv.Version.ID, Requested: s.requested,
	}
	if _, err := m.db.DB.NewInsert().Model(d).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	// What the grab would have kept.
	if s.noSrc == "" {
		src := tor.src
		if err := m.f.svc.storeSource(context.Background(), d.ID, src); err != nil {
			t.Fatal(err)
		}
	}
	return hash
}

// runReport collects what a task would report.
type runReport struct{ lines []string }

func (r *runReport) Progress(_ int, msg string) { r.lines = append(r.lines, msg) }
func (r *runReport) Quiet()                     {}

// switchClient does what saving a different download client does: plan,
// begin, switch the settings, and let the move start.
func (m *mover) switchClient(leaveSeeding bool) int {
	m.t.Helper()
	ctx := context.Background()
	if err := m.f.svc.PlanMove(ctx, m.oldCfg, m.newCfg, leaveSeeding); err != nil {
		m.t.Fatalf("PlanMove: %v", err)
	}
	n, err := m.f.svc.BeginMove(ctx, m.oldCfg, m.newCfg, leaveSeeding)
	if err != nil {
		m.t.Fatalf("BeginMove: %v", err)
	}
	m.f.cfg = m.newCfg
	if _, err := m.f.svc.ClientChanged(ctx, m.oldCfg.ClientName(), m.newCfg.ClientName()); err != nil {
		m.t.Fatalf("ClientChanged: %v", err)
	}
	return n
}

// run runs the move task once.
func (m *mover) run() (*runReport, error) {
	r := &runReport{}
	err := m.f.svc.RunMove(context.Background(), r)
	return r, err
}

func (m *mover) download(hash string) *Download {
	m.t.Helper()
	return m.f.download(m.t, m.db, hash)
}

// history is the messages of the move events for a download, newest first.
func (m *mover) moveEvents() []grab.Event {
	m.t.Helper()
	evs, err := m.f.hist.List(context.Background(), grab.ListOptions{})
	if err != nil {
		m.t.Fatal(err)
	}
	var out []grab.Event
	for _, e := range evs {
		if e.Event == grab.EventMoved || e.Event == grab.EventMoveFailed {
			out = append(out, e)
		}
	}
	return out
}

func (m *mover) sourceRows() int {
	n, err := m.db.DB.NewSelect().Model((*downloadSource)(nil)).Count(context.Background())
	if err != nil {
		m.t.Fatal(err)
	}
	return int(n)
}

// callsOn are the calls a sim client got for a hash, in order.
func callsOn(c *simClient, hash string) []string {
	var out []string
	for _, call := range c.calls {
		if strings.HasSuffix(call, " "+hash[:4]) {
			out = append(out, strings.TrimSuffix(call, " "+hash[:4]))
		}
	}
	return out
}

func wantCalls(t *testing.T, who string, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s calls:\n got %v\nwant %v", who, got, want)
	}
}

// ---- tests

// A finished torrent seeding in qBittorrent moves to Transmission: stopped
// first, checked in place by the new client, then removed from the old one
// without its files, with the old client's seeding time and upload carried
// over and the kept .torrent still there.
func TestMoveSeedingTorrent(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		m.old.stopLag = 2
		h := m.seed(seed{status: StatusImported, have: 1000, onDisk: "plain", seeded: 90 * time.Minute, upload: 2500, grows: 7})
		if n := m.switchClient(false); n != 1 {
			t.Fatalf("moving %d", n)
		}
		if !m.f.svc.Moving() {
			t.Fatal("not moving")
		}
		if d := m.download(h); d.MoveState != moveQueued || d.MoveTo != settings.ClientTransmission || d.Client != settings.ClientQBittorrent {
			t.Fatalf("marked %+v", d)
		}
		rep, err := m.run()
		if err != nil {
			t.Fatal(err)
		}
		if m.f.svc.Moving() {
			t.Error("still moving")
		}
		if got := rep.lines[len(rep.lines)-1]; got != "Moved 1 to Transmission." {
			t.Errorf("summary = %q", got)
		}
		// The order: old stopped (and waited for), then the new client added
		// it stopped, checked it, and only then the old one let go.
		wantCalls(t, "old", callsOn(m.old, h), "stop", "layout", "remove")
		wantCalls(t, "new", callsOn(m.nw, h), "add", "recheck", "limits")
		add := m.nw.added[0]
		if !add.Stopped || add.Category != settings.MovieCategory || add.SavePath != finalDir || add.FilesIn != finalDir ||
			!slices.Equal(add.Tags, []string{"reelkeeper", "Tracker"}) || len(add.Torrent) == 0 {
			t.Errorf("add = %+v", add)
		}
		d := m.download(h)
		if d.Client != settings.ClientTransmission || d.MoveState != "" || d.MoveLayout != "" || d.MovedAt == nil || d.Status != StatusImported || !d.LimitsSet {
			t.Errorf("record = %+v", d)
		}
		// What the old client had once it had stopped, not before.
		if d.MoveOldBytes != 0 || d.SeedSecondsOffset != 90*60 || d.UploadedOffset != 2500 {
			t.Errorf("offsets: old bytes %d, seed %d, uploaded %d", d.MoveOldBytes, d.SeedSecondsOffset, d.UploadedOffset)
		}
		if m.old.tors[h] != nil || m.nw.tors[h] == nil || m.nw.tors[h].Verified != 1000 {
			t.Errorf("old has it: %v; new: %+v", m.old.tors[h] != nil, m.nw.tors[h])
		}
		if got := m.disk.names(); !slices.Equal(got, []string{finalDir + "/" + m.cat[h][0].Name}) {
			t.Errorf("disk = %v", got)
		}
		if ev := m.moveEvents(); len(ev) != 1 || ev[0].Event != grab.EventMoved || !strings.Contains(ev[0].Message, "Moved from qBittorrent to Transmission") {
			t.Errorf("history = %+v", ev)
		}
		if m.sourceRows() != 1 {
			t.Errorf("the kept torrent should stay while it is active: %d rows", m.sourceRows())
		}
		if m.moves.m.Active || m.moves.m.Old.URL != "" {
			t.Errorf("the old connection was kept: %+v", m.moves.m)
		}
		// Nothing else to do next time.
		rep, err = m.run()
		if err != nil || !strings.Contains(rep.lines[0], "No torrents are moving") {
			t.Errorf("again: %v %v", rep.lines, err)
		}
		_ = ctx
	})
}

// The old client keeps writing pieces until it has stopped: Reelkeeper
// waits for "stopped" before reading what it verified.
func TestMoveReadsVerifiedBytesOnlyAfterTheOldClientStopped(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		m.old.stopLag = 3
		// 400 bytes now, growing 100 on each look until it has stopped: 700.
		h := m.seed(seed{have: 400, onDisk: "unfinished", grows: 100})
		m.switchClient(false)
		// Stop the run right after the old client stopped, to read the record.
		m.f.svc.moveHook = func(state string) error {
			if state == moveStopped {
				return errors.New("stop here")
			}
			return nil
		}
		_, _ = m.run()
		d := m.download(h)
		if d.MoveState != moveStopped || d.MoveOldBytes != 700 {
			t.Errorf("state %q, old bytes %d (want 700, read after the stop)", d.MoveState, d.MoveOldBytes)
		}
	})
}

// A half-downloaded torrent with an unfinished file (qBittorrent's .!qB) in its
// temporary folder: Reelkeeper renames it to the plain name, Transmission is
// pointed at the temporary folder, checks it, finds as much as qBittorrent
// had, and is then told to put the files in the final folder.
func TestMoveHalfDownloadedFromTemporaryFolder(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		h := m.seed(seed{have: 600, onDisk: "unfinishedTemp"})
		name := m.cat[h][0].Name
		m.switchClient(false)
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		add := m.nw.added[0]
		if add.SavePath != finalDir || add.FilesIn != tempDir {
			t.Errorf("add paths = %+v", add)
		}
		wantCalls(t, "new", callsOn(m.nw, h), "add", "recheck", "setfolder", "recheck", "limits")
		// The file was renamed to its plain name for the check, then Transmission
		// moved it to the final folder (and, unfinished, named it .part).
		if got := m.disk.names(); !slices.Equal(got, []string{finalDir + "/" + name + ".part"}) {
			t.Errorf("disk = %v", got)
		}
		if fmt.Sprint(m.disk.renames[0]) != fmt.Sprint([2]string{tempDir + "/" + name + ".!qB", tempDir + "/" + name}) {
			t.Errorf("first rename = %v", m.disk.renames[0])
		}
		d := m.download(h)
		if d.Client != settings.ClientTransmission || d.MoveState != "" || d.Status != StatusDownloading {
			t.Errorf("record = %+v", d)
		}
		if tr := m.nw.tors[h]; tr.Verified != 1000 || tr.SavePath != finalDir {
			// The sim counts a present file as whole; what matters is the order above.
			t.Logf("new: %+v", tr.Torrent)
		}
	})
}

// Transmission to qBittorrent: ".part" files in the download folder get their
// plain names; the new torrent is told the files are in the final folder, so
// qBittorrent isn't given a temporary folder to move them to.
func TestMoveFromTransmissionRenamesPartFiles(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientTransmission, settings.ClientQBittorrent)
		h := m.seed(seed{have: 500, onDisk: "part", magnet: true, tags: []string{"reelkeeper", "Tracker", "requested"}})
		name := m.cat[h][0].Name
		m.switchClient(false)
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		add := m.nw.added[0]
		if add.SavePath != finalDir || add.FilesIn != finalDir || add.Magnet == "" || !slices.Contains(add.Tags, "requested") {
			t.Errorf("add = %+v", add)
		}
		// A magnet link has no details: the new client starts to fetch them,
		// is stopped again, and only then checks.
		wantCalls(t, "new", callsOn(m.nw, h), "add", "start", "stop", "recheck", "limits", "top")
		if got := m.disk.names(); !slices.Equal(got, []string{finalDir + "/" + name}) {
			t.Errorf("disk = %v", got)
		}
	})
}

// A torrent the user had paused is paused again in the new client; one that
// had not been is running.
func TestMoveKeepsPaused(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		paused := m.seed(seed{have: 300, onDisk: "plain", paused: true})
		running := m.seed(seed{have: 300, onDisk: "plain"})
		m.switchClient(false)
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		if st := m.nw.tors[paused].State; st != torrent.Paused {
			t.Errorf("the paused one is %q", st)
		}
		if st := m.nw.tors[running].State; st != torrent.Downloading {
			t.Errorf("the running one is %q", st)
		}
		// The old client was only stopped for the one that was running.
		wantCalls(t, "old paused", callsOn(m.old, paused), "layout", "remove")
		wantCalls(t, "old running", callsOn(m.old, running), "stop", "layout", "remove")
		if last := callsOn(m.nw, paused); last[len(last)-1] != "stop" {
			t.Errorf("new calls for the paused one: %v", last)
		}
	})
}

// A torrent still fetching its details moves as the magnet link, with no check.
func TestMoveTorrentStillFetchingDetails(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		h := m.seed(seed{details: true, magnet: true})
		m.switchClient(false)
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, "new", callsOn(m.nw, h), "add", "limits")
		if m.nw.added[0].Magnet == "" || len(m.nw.added[0].Torrent) != 0 {
			t.Errorf("add = %+v", m.nw.added[0])
		}
		if d := m.download(h); d.Client != settings.ClientTransmission || d.MoveState != "" {
			t.Errorf("record = %+v", d)
		}
	})
}

// A torrent grabbed before 1.3 has nothing kept: the old client gives its
// file back when it can.
func TestMoveAsksTheOldClientWhenNothingWasKept(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		h := m.seed(seed{have: 1000, onDisk: "plain", status: StatusImported})
		m.old.tors[h].src = Source{Torrent: testTorrentFile(t, m.cat[h][0].Name, someData(m.cat[h][0].Name, 1000))}
		if _, err := m.db.DB.NewDelete().Model((*downloadSource)(nil)).Where("1 = 1").Exec(context.Background()); err != nil {
			t.Fatal(err)
		}
		m.switchClient(false)
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		if d := m.download(h); d.Client != settings.ClientTransmission {
			t.Errorf("record = %+v", d)
		}
		// What the old client gave is kept, like what a grab keeps.
		if m.sourceRows() != 1 {
			t.Errorf("rows = %d", m.sourceRows())
		}
	})
}

// A check that falls short puts everything back: the new client lets go
// without deleting, every file gets its name and folder back (Transmission
// renamed it while it checked), and the old client checks what it has and
// starts seeding again.
func TestMoveCheckFallsShortOfASeedingTorrent(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		h := m.seed(seed{status: StatusImported, have: 1000, onDisk: "unfinishedTemp", paused: false, seeded: time.Hour, upload: 10})
		name := m.cat[h][0].Name
		// The new client looks somewhere else: it sees nothing.
		m.nw.tors = map[string]*simTorrent{}
		before := m.disk.names()
		m.switchClient(false)
		m.nw.checkLag = 1
		// Make Transmission's Add land in a different folder.
		m.f.svc.NewClient = func(c settings.DownloadClient) Client {
			if c.URL == "http://old" {
				return m.old
			}
			return &blindClient{m.nw}
		}
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		if got := m.disk.names(); !slices.Equal(got, before) {
			t.Errorf("files not put back:\n got %v\nwant %v", got, before)
		}
		d := m.download(h)
		if d.Status != StatusSeeded || d.MoveState != "" || d.Client != settings.ClientQBittorrent {
			t.Errorf("record = %+v", d)
		}
		if !strings.Contains(d.Message, "Couldn't move to Transmission: ") || !strings.Contains(d.Message, "the check found 0 bytes") ||
			!strings.Contains(d.Message, "It stays in qBittorrent") {
			t.Errorf("message = %q", d.Message)
		}
		if m.nw.tors[h] != nil {
			t.Error("the new client still has it")
		}
		wantCalls(t, "new", callsOn(m.nw, h), "add", "recheck", "remove")
		// Old: stopped, then checked, then started again.
		wantCalls(t, "old", callsOn(m.old, h), "stop", "layout", "recheck", "start")
		if tr := m.old.tors[h]; tr.State != torrent.Seeding || tr.Verified != 1000 {
			t.Errorf("old = %+v", tr.Torrent)
		}
		ev := m.moveEvents()
		if len(ev) != 1 || ev[0].Event != grab.EventMoveFailed {
			t.Errorf("history = %+v", ev)
		}
		if m.sourceRows() != 0 {
			t.Errorf("an untracked torrent keeps its torrent: %d rows", m.sourceRows())
		}
		if m.f.svc.Moving() {
			t.Error("still moving")
		}
		_ = name
	})
}

// blindClient is a client whose check never finds the files (it was pointed at
// the wrong folder).
type blindClient struct{ *simClient }

func (b *blindClient) Add(ctx context.Context, a Add) (string, error) {
	a.FilesIn, a.SavePath = "/nowhere", "/nowhere"
	return b.simClient.Add(ctx, a)
}

// A download in the Queue that can't move stays STOPPED in the old client and
// the title is wanted again.
func TestMoveFailureOfADownloadLeavesItStopped(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		h := m.seed(seed{have: 400, onDisk: "unfinished"})
		before := m.disk.names()
		m.switchClient(false)
		m.f.svc.NewClient = func(c settings.DownloadClient) Client {
			if c.URL == "http://old" {
				return m.old
			}
			return &blindClient{m.nw}
		}
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		d := m.download(h)
		if d.Status != StatusRemoved || !strings.Contains(d.Message, "stopped in qBittorrent") || !strings.Contains(d.Message, "search for it again") {
			t.Errorf("record = %q %q", d.Status, d.Message)
		}
		if tr := m.old.tors[h]; !tr.Stopped() {
			t.Errorf("the old client's torrent runs: %v", tr.State)
		}
		wantCalls(t, "old", callsOn(m.old, h), "stop", "layout", "recheck")
		if got := m.disk.names(); !slices.Equal(got, before) {
			t.Errorf("files not put back: %v want %v", got, before)
		}
		// Not in the Queue any more, so the version is wanted again.
		active, _ := m.f.svc.ActiveVersions(context.Background())
		if active[m.f.mv.Version.ID] {
			t.Error("the version still counts as downloading")
		}
	})
}

// Torrents that can't be handed to a new client at all (a private Transmission
// torrent from before 1.3) fail before anything is touched. A seeding one
// keeps running; one in the Queue is stopped.
func TestMoveWithoutATorrentFile(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientTransmission, settings.ClientQBittorrent)
		const why = "It is a private torrent, Transmission can't give back its .torrent file, and Reelkeeper didn't keep a copy."
		seeding := m.seed(seed{status: StatusImported, have: 1000, onDisk: "plain", noSrc: why})
		queued := m.seed(seed{have: 100, onDisk: "part", noSrc: why})
		// The preview says so before anything is saved.
		p, err := m.f.svc.MovePreview(context.Background())
		if err != nil || p.Total != 2 || p.Seeding != 1 || p.Downloading != 1 || len(p.CannotMove) != 2 || p.CannotMove[0].Reason != why {
			t.Errorf("preview = %+v, %v", p, err)
		}
		m.switchClient(false)
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		// (Once for the preview, once when the move began.)
		wantCalls(t, "old seeding", callsOn(m.old, seeding), "source", "source")
		if tr := m.old.tors[seeding]; tr.State != torrent.Seeding {
			t.Errorf("the seeding one was touched: %v", tr.State)
		}
		if d := m.download(seeding); d.Status != StatusSeeded || !strings.Contains(d.Message, why) {
			t.Errorf("seeding = %q %q", d.Status, d.Message)
		}
		if tr := m.old.tors[queued]; !tr.Stopped() {
			t.Errorf("the download in the Queue still runs: %v", tr.State)
		}
		if d := m.download(queued); d.Status != StatusRemoved || !strings.Contains(d.Message, why) {
			t.Errorf("queued = %q %q", d.Status, d.Message)
		}
		if len(m.nw.calls) != 0 {
			t.Errorf("the new client was asked: %v", m.nw.calls)
		}
		if len(m.disk.renames) != 0 {
			t.Errorf("files were renamed: %v", m.disk.renames)
		}
	})
}

// Files Reelkeeper can't find or can't tell apart keep the torrent where it is.
func TestMoveFilesNotFound(t *testing.T) {
	for name, tc := range map[string]struct {
		s     seed
		extra func(m *mover, name string)
		want  string
	}{
		"nothing there":    {s: seed{have: 400}, want: "can't find its files"},
		"finished, absent": {s: seed{have: 1000, status: StatusImported}, want: "can't find its files"},
		"two folders": {s: seed{have: 400, onDisk: "plain"}, want: "is in more than one folder",
			extra: func(m *mover, name string) { m.disk.put(tempDir+"/"+name, 400) }},
		"plain and unfinished": {s: seed{have: 400, onDisk: "unfinished"}, want: "and Reelkeeper won't choose between them",
			extra: func(m *mover, name string) { m.disk.put(finalDir+"/"+name, 400) }},
	} {
		t.Run(name, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, db *store.Store) {
				m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
				h := m.seed(tc.s)
				if tc.extra != nil {
					tc.extra(m, m.cat[h][0].Name)
				}
				before := m.disk.names()
				m.switchClient(false)
				if _, err := m.run(); err != nil {
					t.Fatal(err)
				}
				d := m.download(h)
				if d.Status != StatusRemoved && d.Status != StatusSeeded || !strings.Contains(d.Message, tc.want) {
					t.Errorf("%q %q", d.Status, d.Message)
				}
				if len(m.nw.calls) != 0 || !slices.Equal(m.disk.names(), before) || len(m.disk.renames) != 0 {
					t.Errorf("something was touched: new %v, disk %v", m.nw.calls, m.disk.names())
				}
			})
		})
	}
}

// A client that refuses the torrent is a failure; one that can't be reached
// is not: the move waits and tries again, and finishes the first time it can.
func TestMoveWaitsForAnUnreachableClient(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		h := m.seed(seed{have: 600, onDisk: "plain"})
		m.switchClient(false)
		m.nw.failOn["add"] = &unreachableError{msg: "can't reach Transmission: connection refused", err: errors.New("refused")}
		if _, err := m.run(); err == nil || !strings.Contains(err.Error(), "haven't finished moving") {
			t.Fatalf("run = %v", err)
		}
		d := m.download(h)
		if d.MoveState != moveRenamed || d.Status != StatusDownloading || !m.f.svc.Moving() {
			t.Fatalf("state %q status %q moving %v", d.MoveState, d.Status, m.f.svc.Moving())
		}
		if !m.old.tors[h].Stopped() || m.old.tors[h] == nil {
			t.Error("the old client's torrent should wait, stopped")
		}
		// Back up: carries on from "renamed".
		delete(m.nw.failOn, "add")
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		if d := m.download(h); d.Client != settings.ClientTransmission || d.MoveState != "" {
			t.Errorf("record = %+v", d)
		}
		if d := m.download(h); d.SeedSecondsOffset != 0 || d.UploadedOffset != 0 {
			t.Errorf("counters added twice: %+v", d)
		}
	})
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		h := m.seed(seed{have: 600, onDisk: "plain"})
		m.switchClient(false)
		m.nw.failOn["add"] = errors.New("Transmission refused the torrent (it may be invalid)")
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		if d := m.download(h); d.Status != StatusRemoved || !strings.Contains(d.Message, "refused the torrent") {
			t.Errorf("refused: %q %q", d.Status, d.Message)
		}
	})
}

// A new client that can't stop an old torrent in time is a failure that starts
// it again.
func TestMoveOldClientDoesNotStop(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		m.old.stopLag = 1 << 30
		h := m.seed(seed{status: StatusImported, have: 1000, onDisk: "plain"})
		m.switchClient(false)
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		d := m.download(h)
		if d.Status != StatusSeeded || !strings.Contains(d.Message, "didn't stop it in time") {
			t.Errorf("record = %q %q", d.Status, d.Message)
		}
		if len(m.nw.calls) != 0 {
			t.Errorf("new client: %v", m.nw.calls)
		}
	})
}

// A new client can show a false 100% before any check; Reelkeeper trusts
// only a number that comes after its own explicit check.
func TestMoveDoesNotTrustNumbersFromBeforeTheCheck(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		m.nw.staleVerified = 1000
		h := m.seed(seed{status: StatusImported, have: 1000, onDisk: "plain"})
		m.switchClient(false)
		m.f.svc.NewClient = func(c settings.DownloadClient) Client {
			if c.URL == "http://old" {
				return m.old
			}
			return &blindClient{m.nw}
		}
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		if d := m.download(h); d.Status != StatusSeeded || !strings.Contains(d.Message, "the check found 0 bytes") {
			t.Errorf("record = %q %q", d.Status, d.Message)
		}
	})
}

// Each step is saved before the next, so a restart anywhere carries on and
// ends where an uninterrupted move does, without doing anything twice.
func TestMoveResumesAfterARestartAtEveryStep(t *testing.T) {
	for _, state := range []string{moveQueued, moveStopped, moveRenaming, moveRenamed, moveAdded, moveChecking, moveFinishing} {
		t.Run(state, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, db *store.Store) {
				m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
				m.old.stopLag = 1
				h := m.seed(seed{have: 600, onDisk: "unfinishedTemp", seeded: time.Hour, upload: 50})
				name := m.cat[h][0].Name
				m.switchClient(false)
				crash := errors.New("simulated crash")
				m.f.svc.moveHook = func(s string) error {
					if s == state {
						return crash
					}
					return nil
				}
				if state == moveChecking {
					// "checking" is only ever seen by a task that starts again
					// in the middle of a check: the client went away during it.
					m.f.svc.moveHook = nil
					m.nw.failOn["recheck"] = &unreachableError{msg: "can't reach Transmission: gone", err: crash}
				}
				if _, err := m.run(); err == nil || !strings.Contains(err.Error(), "simulated crash") && !strings.Contains(err.Error(), "gone") {
					t.Fatalf("run = %v", err)
				}
				delete(m.nw.failOn, "recheck")
				if d := m.download(h); d.MoveState != state {
					t.Fatalf("saved step = %q, want %q", d.MoveState, state)
				}
				m.restart()
				if !m.f.svc.Moving() {
					t.Fatal("a restart forgot the move")
				}
				if _, err := m.run(); err != nil {
					t.Fatal(err)
				}
				d := m.download(h)
				if d.Client != settings.ClientTransmission || d.MoveState != "" || d.Status != StatusDownloading {
					t.Fatalf("record = %+v", d)
				}
				if d.SeedSecondsOffset != 3600 || d.UploadedOffset != 50 {
					t.Errorf("counters: %d s, %d bytes", d.SeedSecondsOffset, d.UploadedOffset)
				}
				if got := m.disk.names(); !slices.Equal(got, []string{finalDir + "/" + name + ".part"}) {
					t.Errorf("disk = %v", got)
				}
				// Once each.
				count := func(c *simClient, op string) int {
					n := 0
					for _, call := range callsOn(c, h) {
						if call == op {
							n++
						}
					}
					return n
				}
				for _, op := range []string{"stop", "remove"} {
					if n := count(m.old, op); n != 1 {
						t.Errorf("old %s %d times", op, n)
					}
				}
				if n := count(m.nw, "add"); n != 1 {
					t.Errorf("new add %d times", n)
				}
				if m.old.tors[h] != nil || m.nw.tors[h] == nil || m.f.svc.Moving() {
					t.Error("the move didn't finish")
				}
				if ev := m.moveEvents(); len(ev) != 1 {
					t.Errorf("history has %d move entries", len(ev))
				}
			})
		})
	}
}

// A restart in the middle of undoing a failed move carries on undoing it.
func TestMoveResumesARollbackAfterARestart(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(fmt.Sprint("early=", early), func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, db *store.Store) {
				m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
				s := seed{have: 400, onDisk: "unfinished", status: StatusImported}
				if early {
					s.noSrc = "no copy"
				}
				h := m.seed(s)
				before := m.disk.names()
				m.switchClient(false)
				m.f.svc.NewClient = func(c settings.DownloadClient) Client {
					if c.URL == "http://old" {
						return m.old
					}
					return &blindClient{m.nw}
				}
				want := moveRollback
				if early {
					want = moveRollbackEarly
				}
				m.f.svc.moveHook = func(s string) error {
					if s == want {
						return errors.New("simulated crash")
					}
					return nil
				}
				_, _ = m.run()
				if d := m.download(h); d.MoveState != want {
					t.Fatalf("saved step = %q, want %q", d.MoveState, want)
				}
				m.restart()
				m.f.svc.NewClient = func(c settings.DownloadClient) Client {
					if c.URL == "http://old" {
						return m.old
					}
					return &blindClient{m.nw}
				}
				if _, err := m.run(); err != nil {
					t.Fatal(err)
				}
				if d := m.download(h); d.Status != StatusSeeded || d.MoveState != "" {
					t.Errorf("record = %q %q %q", d.Status, d.MoveState, d.Message)
				}
				if got := m.disk.names(); !slices.Equal(got, before) {
					t.Errorf("disk = %v want %v", got, before)
				}
				if m.f.svc.Moving() {
					t.Error("still moving")
				}
			})
		})
	}
}

// Putting files back works wherever the clients left them: renamed in place,
// or moved to the final folder.
func TestRestoreNames(t *testing.T) {
	disk := newFakeDisk()
	s := &Service{exists: disk.exists, rename: disk.rename}
	ml := moveLayout{Dir: tempDir, Final: finalDir, Look: []string{finalDir, tempDir}, Files: []movedFile{
		{Name: "a/one.mkv", Was: "a/one.mkv.!qB"}, {Name: "a/two.mkv", Was: "a/two.mkv"}, {Name: "a/three.mkv"},
	}}
	disk.put(finalDir+"/a/one.mkv.part", 5) // Transmission moved and renamed it
	disk.put(tempDir+"/a/two.mkv", 5)       // untouched
	if err := s.restoreNames(ml); err != nil {
		t.Fatal(err)
	}
	if got := disk.names(); !slices.Equal(got, []string{tempDir + "/a/one.mkv.!qB", tempDir + "/a/two.mkv"}) {
		t.Errorf("disk = %v", got)
	}
	// A file that is nowhere is reported, not made up.
	delete(disk.files, tempDir+"/a/two.mkv")
	if err := s.restoreNames(ml); err == nil || !strings.Contains(err.Error(), "couldn't find a/two.mkv") {
		t.Errorf("missing file: %v", err)
	}
}

// While torrents move, grabbing, imports, cleanup and the Queue's buttons wait.
func TestMovingPausesGrabbingImportsAndCleanup(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		withCleanup(m.f, settings.CleanupOn)
		h := m.seed(seed{have: 1000, onDisk: "plain"})
		m.switchClient(false)

		err := m.f.svc.Grab(ctx, grab.Grabbed{Version: m.f.mv})
		if !errors.Is(err, grab.ErrMoving) {
			t.Errorf("Grab = %v", err)
		}
		if msg, quiet, err := m.f.svc.Check(ctx); err != nil || !quiet || !strings.Contains(msg, "Paused while torrents move") {
			t.Errorf("Check = %q %v %v", msg, quiet, err)
		}
		if msg, quiet, err := m.f.svc.Cleanup(ctx); err != nil || !quiet || !strings.Contains(msg, "Paused while torrents move") {
			t.Errorf("Cleanup = %q %v %v", msg, quiet, err)
		}
		d := m.download(h)
		if err := m.f.svc.RetryImport(ctx, d.ID); err == nil || !strings.Contains(err.Error(), "imports wait") {
			t.Errorf("RetryImport = %v", err)
		}
		for name, act := range map[string]func() error{
			"Pause":     func() error { return m.f.svc.Pause(ctx, d.ID) },
			"Resume":    func() error { return m.f.svc.Resume(ctx, d.ID) },
			"MoveToTop": func() error { return m.f.svc.MoveToTop(ctx, d.ID) },
			"Remove":    func() error { _, err := m.f.svc.Remove(ctx, d.ID, RemoveOptions{}); return err },
		} {
			if err := act(); err == nil || !strings.Contains(err.Error(), "moving to Transmission") {
				t.Errorf("%s = %v", name, err)
			}
		}
		// The Queue shows it, asking the old client for the torrent.
		items, cerr, err := m.f.svc.Queue(ctx)
		if err != nil || cerr != nil || len(items) != 1 || items[0].Download.MovingTo() != "Transmission" || items[0].Torrent == nil || items[0].Torrent.Hash != h {
			t.Errorf("queue = %+v %v %v", items, cerr, err)
		}
		// Once it is done everything works again.
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		if _, quiet, err := m.f.svc.Check(ctx); err != nil || quiet {
			t.Errorf("Check after = %v %v", quiet, err)
		}
	})
}

// The moving flag also covers a half-done switch: if the settings were never
// saved, nothing moves after all.
func TestMoveCancelsWhenTheClientWasNotChanged(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		h := m.seed(seed{have: 600, onDisk: "plain"})
		if n, err := m.f.svc.BeginMove(context.Background(), m.oldCfg, m.newCfg, false); err != nil || n != 1 {
			t.Fatalf("BeginMove = %d %v", n, err)
		}
		// (The settings still say qBittorrent.)
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		if d := m.download(h); d.MoveState != "" || d.Status != StatusDownloading || m.f.svc.Moving() {
			t.Errorf("record = %+v moving %v", d, m.f.svc.Moving())
		}
		if len(m.old.calls) != 0 || len(m.nw.calls) != 0 {
			t.Errorf("clients were asked: %v %v", m.old.calls, m.nw.calls)
		}
		// And CancelMove, for when saving the settings fails.
		if _, err := m.f.svc.BeginMove(context.Background(), m.oldCfg, m.newCfg, false); err != nil {
			t.Fatal(err)
		}
		m.f.svc.CancelMove(context.Background())
		if d := m.download(h); d.MoveState != "" || d.MoveStartedAt != nil || m.f.svc.Moving() {
			t.Errorf("after cancel: %+v", d)
		}
	})
}

// PlanMove refuses a change while a move runs, and when a client can't be
// reached; with nothing to move it asks nothing.
func TestPlanMove(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		// Nothing to move: no client is asked.
		m.old.failOn["version"], m.nw.failOn["version"] = errors.New("down"), errors.New("down")
		if err := m.f.svc.PlanMove(ctx, m.oldCfg, m.newCfg, false); err != nil {
			t.Errorf("empty: %v", err)
		}
		m.seed(seed{have: 600, onDisk: "plain"})
		if err := m.f.svc.PlanMove(ctx, m.oldCfg, m.newCfg, false); err == nil || !strings.Contains(err.Error(), "qBittorrent needs to be reachable") {
			t.Errorf("old down: %v", err)
		}
		delete(m.old.failOn, "version")
		if err := m.f.svc.PlanMove(ctx, m.oldCfg, m.newCfg, false); err == nil || !strings.Contains(err.Error(), "Transmission can't be used yet") {
			t.Errorf("new down: %v", err)
		}
		delete(m.nw.failOn, "version")
		if err := m.f.svc.PlanMove(ctx, m.oldCfg, m.newCfg, false); err != nil {
			t.Errorf("ok: %v", err)
		}
		m.switchClient(false)
		if err := m.f.svc.PlanMove(ctx, m.newCfg, m.oldCfg, false); !errors.Is(err, ErrMoveRunning) {
			t.Errorf("while moving: %v", err)
		}
		if _, err := m.f.svc.BeginMove(ctx, m.newCfg, m.oldCfg, false); !errors.Is(err, ErrMoveRunning) {
			t.Errorf("begin while moving: %v", err)
		}
	})
}

// The seeding ones can be left behind (as in 1.2): the Queue's downloads
// still move, and the ones left are marked, with History saying so.
func TestMoveLeavesSeedingOnRequest(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		seeding := m.seed(seed{status: StatusImported, have: 1000, onDisk: "plain"})
		queued := m.seed(seed{have: 400, onDisk: "plain"})
		if n := m.switchClient(true); n != 1 {
			t.Fatalf("moving %d", n)
		}
		if d := m.download(seeding); d.Status != StatusSeeded || d.MoveState != "" ||
			d.Message != "Left seeding in qBittorrent when the download client changed to Transmission; Reelkeeper no longer tracks it." {
			t.Errorf("seeding = %q %q %q", d.Status, d.MoveState, d.Message)
		}
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		if d := m.download(queued); d.Client != settings.ClientTransmission {
			t.Errorf("queued = %+v", d)
		}
		if m.old.tors[seeding] == nil || len(callsOn(m.old, seeding)) != 0 || len(callsOn(m.nw, seeding)) != 0 {
			t.Errorf("the left torrent was touched: %v %v", callsOn(m.old, seeding), callsOn(m.nw, seeding))
		}
	})
}

// The preview counts what would move.
func TestMovePreview(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		m.seed(seed{status: StatusImported, have: 1000, onDisk: "plain"})
		m.seed(seed{status: StatusImported, have: 1000, onDisk: "plain"})
		m.seed(seed{have: 400, onDisk: "plain"})
		m.seed(seed{status: StatusImportFailed, have: 1000, onDisk: "plain"})
		p, err := m.f.svc.MovePreview(ctx)
		if err != nil || p.Total != 4 || p.Seeding != 2 || p.Downloading != 2 || len(p.CannotMove) != 0 || p.ClientError != "" {
			t.Errorf("preview = %+v, %v", p, err)
		}
		// Not in the client any more: nothing to move.
		for h := range m.old.tors {
			delete(m.old.tors, h)
			break
		}
		if p, _ := m.f.svc.MovePreview(ctx); p.Total != 3 {
			t.Errorf("after one vanished: %+v", p)
		}
		// The client can't be asked: the counts still come from the records.
		m.old.failOn["list"] = &unreachableError{msg: "can't reach qBittorrent: down", err: errors.New("down")}
		p, err = m.f.svc.MovePreview(ctx)
		if err != nil || p.Total != 4 || !strings.Contains(p.ClientError, "Can't reach qBittorrent") {
			t.Errorf("unreachable: %+v, %v", p, err)
		}
	})
}

// The seeding goal and the tracker's minimum add what the old client had
// already seeded and uploaded to what the new client reports.
func TestCleanupCountsWhatTheOldClientHad(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		cfg, _ := withCleanup(f, settings.CleanupOn)
		cfg.SeedRatio, cfg.SeedHours = 1.0, 0 // ratio only
		f.grab(t, hashA)                      // the tracker asks for ratio 2 or 60 minutes
		f.mem.torrents[hashA].Progress = 1
		if _, _, err := f.svc.Check(ctx); err != nil {
			t.Fatal(err)
		}
		tr := f.mem.torrents[hashA]
		tr.Size, tr.Uploaded, tr.Ratio, tr.SeedingTime = 1000, 100, 9, 5*time.Minute // (the client's own ratio is ignored once moved)
		now := time.Now().UTC()
		if _, err := db.DB.NewUpdate().Model((*Download)(nil)).Set("moved_at = ?", now).Where("hash = ?", hashA).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		// New client: 5 minutes and 100 bytes. Not enough for the tracker
		// (60 minutes or ratio 2) on its own.
		if _, _, err := f.svc.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		if d := f.download(t, db, hashA); d.Status != StatusImported {
			t.Fatalf("removed too early: %q", d.Status)
		}
		items, _ := f.svc.Seeding(ctx)
		if len(items) != 1 || items[0].Torrent.SeedingTime != 5*time.Minute || items[0].Torrent.Ratio != 0.1 {
			t.Errorf("seeding = %+v", items[0].Torrent)
		}
		// The old client had seeded 56 minutes: 61 in all, the tracker's
		// minimum is met in time, but the user's goal is a ratio of 1.0 and
		// 100 bytes is 0.1 of 1000: kept.
		if _, err := db.DB.NewUpdate().Model((*Download)(nil)).Set("seed_seconds_offset = ?", 56*60).Where("hash = ?", hashA).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.svc.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		if d := f.download(t, db, hashA); d.Status != StatusImported {
			t.Fatalf("the goal wasn't met, but it was removed: %q", d.Status)
		}
		// With the old client's upload counted the goal (ratio 1.0) is met too.
		if _, err := db.DB.NewUpdate().Model((*Download)(nil)).Set("uploaded_offset = ?", 900).Where("hash = ?", hashA).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.svc.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		d := f.download(t, db, hashA)
		if d.Status != StatusSeeded || !strings.Contains(d.Message, "Seeded to ratio 1.00") {
			t.Errorf("record = %q %q", d.Status, d.Message)
		}
	})
}

// counted leaves a torrent that never moved exactly as the client reports it.
func TestCountedLeavesUnmovedTorrentsAlone(t *testing.T) {
	tr := Torrent{Size: 100, Ratio: 3, SeedingTime: time.Hour, Uploaded: 10}
	if got := counted(&Download{}, tr); got.Ratio != tr.Ratio || got.SeedingTime != tr.SeedingTime {
		t.Errorf("counted changed it: %+v", got)
	}
	got := counted(&Download{SeedSecondsOffset: 60, UploadedOffset: 90}, tr)
	if got.SeedingTime != time.Hour+time.Minute || got.Ratio != 1.0 {
		t.Errorf("counted = %+v", got)
	}
}

// What is sent at grab time is kept, encrypted, only while the torrent is
// active, and deleted every way a torrent leaves the client.
func TestKeptTorrentIsEncryptedAndDeletedWithTheTorrent(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newDFix(t, db)
		box, _ := secrets.New("a-test-key-of-sixteen-or-more")
		f.svc.SetSecrets(box)
		withCleanup(f, settings.CleanupOn)
		hashB := strings.Repeat("b", 40)
		hashC := strings.Repeat("c", 40)
		hashD := strings.Repeat("d", 40)
		for _, h := range []string{hashA, hashB, hashC, hashD} {
			f.grab(t, h)
		}
		var rows []downloadSource
		if err := db.DB.NewSelect().Model(&rows).Scan(ctx); err != nil || len(rows) != 4 {
			t.Fatalf("rows = %d, %v", len(rows), err)
		}
		for _, r := range rows {
			if r.Kind != SourceMagnet || strings.Contains(r.Sealed, "btih") || !strings.HasPrefix(r.Sealed, "v1:") {
				t.Errorf("not sealed: %+v", r)
			}
		}
		d := f.download(t, db, hashA)
		if src, ok, err := f.svc.storedSource(ctx, d.ID); err != nil || !ok || src.Magnet != "magnet:?xt=urn:btih:"+hashA {
			t.Errorf("stored = %+v %v %v", src, ok, err)
		}
		// Removed from the Queue by the user.
		if _, err := f.svc.Remove(ctx, d.ID, RemoveOptions{}); err != nil {
			t.Fatal(err)
		}
		// Gone from the client by hand.
		delete(f.mem.torrents, hashB)
		_, _, _ = f.svc.Check(ctx)
		// Imported, then removed by cleanup.
		f.mem.torrents[hashC].Progress = 1
		_, _, _ = f.svc.Check(ctx)
		if n, _ := db.DB.NewSelect().Model((*downloadSource)(nil)).Count(ctx); n != 2 {
			t.Errorf("after remove and gone: %d rows, want C (imported, still seeding) and D (downloading)", n)
		}
		f.mem.torrents[hashC].Ratio = 5
		f.mem.torrents[hashC].SeedingTime = 10 * time.Hour
		if _, _, err := f.svc.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
		if n, _ := db.DB.NewSelect().Model((*downloadSource)(nil)).Count(ctx); n != 1 {
			t.Errorf("after cleanup: %d rows, want only D", n)
		}
		// Left behind by a change of client.
		f.mem.torrents[hashD].Progress = 1
		_, _, _ = f.svc.Check(ctx)
		f.cfg = settings.DownloadClient{Type: settings.ClientTransmission, URL: "http://tm:9091"}
		if _, err := f.svc.ClientChanged(ctx, "qBittorrent", "Transmission"); err != nil {
			t.Fatal(err)
		}
		if n, _ := db.DB.NewSelect().Model((*downloadSource)(nil)).Count(ctx); n != 0 {
			t.Errorf("after the change: %d rows", n)
		}
		// A key that changed makes the kept copy unreadable, not an error.
		f.grab(t, hashA)
		d = f.download(t, db, hashA)
		other, _ := secrets.New("a-different-key-of-16-plus")
		f.svc.SetSecrets(other)
		if _, ok, err := f.svc.storedSource(ctx, d.ID); ok || err != nil {
			t.Errorf("with the wrong key: %v %v", ok, err)
		}
	})
}

// PieceLength reads the piece size from a .torrent file.
func TestPieceLength(t *testing.T) {
	raw := testTorrentFile(t, "x.mkv", someData("x", 40000))
	if got := PieceLength(raw); got != 16384 {
		t.Errorf("PieceLength = %d", got)
	}
	for _, bad := range [][]byte{nil, []byte("hello"), []byte("d4:infod4:name1:xee")} {
		if got := PieceLength(bad); got != 0 {
			t.Errorf("PieceLength(%q) = %d", bad, got)
		}
	}
}

// ---- giving up after an hour

// backdate makes a download's move look as if it began some time ago.
func (m *mover) backdate(hash string, ago time.Duration) {
	m.t.Helper()
	_, err := m.db.DB.NewUpdate().Model((*Download)(nil)).Set("move_started_at = ?", time.Now().UTC().Add(-ago)).
		Where("hash = ?", hash).Exec(context.Background())
	if err != nil {
		m.t.Fatal(err)
	}
}

func gone() error {
	return &unreachableError{msg: "can't reach qBittorrent: connection refused", err: errors.New("refused")}
}

// The time a move began is noted when it is marked, and gone when it ends.
func TestMoveNotesWhenItBegan(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		h := m.seed(seed{have: 600, onDisk: "plain"})
		m.switchClient(false)
		// Its hour starts when its turn comes, not at the switch.
		if d := m.download(h); d.MoveStartedAt != nil {
			t.Fatalf("move_started_at before its turn = %v", d.MoveStartedAt)
		}
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		if d := m.download(h); d.MoveStartedAt != nil || d.MoveState != "" || d.Client != settings.ClientTransmission {
			t.Errorf("after the move: %+v", d)
		}
	})
}

// An old client that is gone for good can't hold everything up forever: inside
// the hour the move waits and tries again, after it the torrent ends as a
// failure that says where it was left, and nothing was deleted or touched.
func TestMoveGivesUpOnAnOldClientThatIsGone(t *testing.T) {
	for _, seeding := range []bool{false, true} {
		t.Run(fmt.Sprint("seeding=", seeding), func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, db *store.Store) {
				m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
				s := seed{have: 600, onDisk: "plain"}
				if seeding {
					s = seed{have: 1000, onDisk: "plain", status: StatusImported}
				}
				h := m.seed(s)
				before := m.disk.names()
				m.switchClient(false)
				m.old.failOn["list"] = gone()

				// Inside the hour: tried again, nothing ends.
				m.backdate(h, 59*time.Minute)
				for range 2 {
					if _, err := m.run(); err == nil || !strings.Contains(err.Error(), "haven't finished moving") {
						t.Fatalf("run = %v", err)
					}
					if d := m.download(h); d.MoveState != moveQueued || d.MoveStartedAt == nil || !m.f.svc.Moving() {
						t.Fatalf("inside the hour: %+v moving %v", d, m.f.svc.Moving())
					}
				}

				// Past it: ends.
				m.backdate(h, 61*time.Minute)
				r, err := m.run()
				if err != nil {
					t.Fatalf("run after the hour = %v", err)
				}
				d := m.download(h)
				wantStatus := StatusRemoved
				if seeding {
					wantStatus = StatusSeeded
				}
				if d.Status != wantStatus || d.MoveState != "" || d.MoveTo != "" || d.MoveLayout != "" || d.MoveStartedAt != nil {
					t.Errorf("record = %+v", d)
				}
				if m.f.svc.Moving() || m.moves.m.Active {
					t.Error("the move settings are still set")
				}
				ev := m.moveEvents()
				if len(ev) != 1 || ev[0].Event != grab.EventMoveFailed {
					t.Fatalf("history = %+v", ev)
				}
				for _, want := range []string{"couldn't be moved within an hour", "connection refused", "gave up", "may still be in qBittorrent", "Nothing was deleted"} {
					if !strings.Contains(ev[0].Message, want) {
						t.Errorf("history message lacks %q: %s", want, ev[0].Message)
					}
				}
				if !strings.Contains(strings.Join(r.lines, "|"), "1 couldn't move") {
					t.Errorf("report = %v", r.lines)
				}
				// Nothing was touched: the new client heard nothing, the disk is as it was.
				if len(m.nw.calls) != 0 || len(m.disk.renames) != 0 || !slices.Equal(m.disk.names(), before) {
					t.Errorf("something was touched: new %v renames %v", m.nw.calls, m.disk.renames)
				}
				// Grabbing, imports and cleanup are free again.
				if msg, _, err := m.f.svc.Check(context.Background()); err != nil || strings.Contains(msg, "Paused while torrents move") {
					t.Errorf("Check after = %q %v", msg, err)
				}
			})
		})
	}
}

// A step that keeps failing for an hour becomes a failure like any other, and
// the rollback that follows puts everything back.
func TestMoveStuckStepRollsBackAfterAnHour(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		h := m.seed(seed{have: 400, onDisk: "unfinished"})
		before := m.disk.names()
		m.switchClient(false)
		m.nw.failOn["add"] = &unreachableError{msg: "can't reach Transmission: connection refused", err: errors.New("refused")}
		if _, err := m.run(); err == nil {
			t.Fatal("expected the move to wait")
		}
		if d := m.download(h); d.MoveState != moveRenamed {
			t.Fatalf("state = %q", d.MoveState)
		}
		m.backdate(h, 2*time.Hour)
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		d := m.download(h)
		if d.Status != StatusRemoved || d.MoveState != "" || d.MoveStartedAt != nil ||
			!strings.Contains(d.Message, "couldn't be moved within an hour") || !strings.Contains(d.Message, "connection refused") {
			t.Errorf("record = %q %q %q", d.Status, d.MoveState, d.Message)
		}
		if !slices.Equal(m.disk.names(), before) {
			t.Errorf("disk = %v want %v", m.disk.names(), before)
		}
		if !m.old.tors[h].Stopped() || m.f.svc.Moving() {
			t.Error("the torrent should wait stopped in the old client, and the move be over")
		}
	})
}

// A rollback that can't put a file back (it can't be found) is tried again
// within the hour; after it, the download ends and History says which folder
// and which original names to look for.
func TestMoveGivesUpOnARollbackThatCannotFinish(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		h := m.seed(seed{have: 400, onDisk: "unfinished", file: "Lost.mkv"})
		m.switchClient(false)
		m.nw.failOn["add"] = gone()
		if _, err := m.run(); err == nil {
			t.Fatal("expected the move to wait")
		}
		// Reelkeeper had renamed Lost.mkv.!qB to Lost.mkv; now it has vanished
		// and the new client refuses the torrent, so the move must be undone.
		delete(m.disk.files, finalDir+"/Lost.mkv")
		m.nw.failOn["add"] = errors.New("Transmission refused the torrent (it may be invalid)")

		m.backdate(h, 10*time.Minute)
		if _, err := m.run(); err == nil || !strings.Contains(err.Error(), "couldn't find Lost.mkv") {
			t.Fatalf("run inside the hour = %v", err)
		}
		if d := m.download(h); d.MoveState != moveRollback || !m.f.svc.Moving() {
			t.Fatalf("inside the hour: %q moving %v", d.MoveState, m.f.svc.Moving())
		}

		m.backdate(h, 3*time.Hour)
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		d := m.download(h)
		if d.Status != StatusRemoved || d.MoveState != "" || d.MoveLayout != "" || d.MoveStartedAt != nil || m.f.svc.Moving() {
			t.Errorf("record = %+v moving %v", d, m.f.svc.Moving())
		}
		ev := m.moveEvents()
		if len(ev) != 1 || ev[0].Event != grab.EventMoveFailed {
			t.Fatalf("history = %+v", ev)
		}
		for _, want := range []string{"Transmission refused the torrent", "couldn't find Lost.mkv", "gave up", finalDir, "Lost.mkv.!qB", "may still be in qBittorrent", "in Transmission", "Nothing was deleted"} {
			if !strings.Contains(ev[0].Message, want) {
				t.Errorf("history message lacks %q: %s", want, ev[0].Message)
			}
		}
	})
}
