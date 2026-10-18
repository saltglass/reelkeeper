//go:build loadtest && linux

package builtin

// The load harness (built-in client, stage 3): how much memory the engine
// needs with thousands of seeding torrents, and what each one costs. It is
// test-only and behind the loadtest build tag, so it is neither in the shipped
// binary nor in `just test`. Run it with `just load-builtin` (about 1.5 hours);
// the numbers go to tmp/load-builtin/.
//
// Nothing here leaves the computer. The engine's Network is loopbackNet
// (harness_test.go), which refuses anything that isn't 127.0.0.1; the tracker
// is an httptest stub; the DHT is off (with it on the engine would try to look
// up the public bootstrap nodes, which loopbackNet refuses, and private
// torrents never use it anyway); the uploading peers are plain anacrolix
// clients on 127.0.0.1 with trackers and the DHT off.
//
// Each measurement runs in a fresh process (the test binary run again with
// TestLoadOne), so one run's memory never shows up in the next, and the peers
// that download from the engine run in yet another one (TestLoadPeer), so
// their memory isn't counted as the engine's.
//
// The torrents are made up: private, on the stub tracker, 256 KiB pieces, and
// a mix of single files (1-400 MB) and episode folders (3-12 files); with
// LOAD_REAL=1 they are 100 times bigger, with real torrents' 1-16 MiB pieces.
// Their files are sparse (all zeros, so they take no disk space and every
// piece has the same hash), and the engine's piece records are filled in
// beforehand, so every torrent is complete and seeding without hashing
// anything. Every torrent starts out running and is then parked when the
// seeding limit applies, as a finished download is in real use.
//
// Measured 2026-10-18 (docs/install.md, built-in client): memory goes with
// the pieces the client holds, about 0.6 KB each, so about 1 MB per
// real-sized torrent, parked or seeding, almost all of it anacrolix's
// per-piece records. Dan accepted that: about 100 torrents is the realistic
// top end, and an external client suits more.

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // BitTorrent v1 pieces are hashed with SHA-1 by definition
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"go.etcd.io/bbolt"
	"golang.org/x/time/rate"

	rktorrent "github.com/saltglass/reelkeeper/internal/torrent"
)

// loadRun is one measurement.
type loadRun struct {
	N     int `json:"n"`     // torrents
	Seeds int `json:"seeds"` // MaxActiveSeeds; 0 means no limit
	// Peers download from the engine during the settle time, each from its
	// own torrent (one of the first, which are 400 MB and in a seeding slot),
	// at PeerKBs each.
	Peers   int           `json:"peers"`
	PeerKBs int           `json:"peerKBs"`
	Settle  time.Duration `json:"settle"`
	// Interval is the announce interval the stub tracker asks for.
	Interval time.Duration `json:"interval"`
	// Real makes the torrents the size real ones are (100 times bigger: 5-40
	// GB movies and season folders) with the piece sizes real torrent makers
	// pick (1-16 MiB, for about 1,000-2,000 pieces). Memory goes with the
	// number of pieces, so this is the case to compare with a real library.
	Real bool   `json:"real"`
	Out  string `json:"out"`
}

// pieceLengthFor is the piece size for a torrent of total bytes: 256 KiB, or
// for real-sized torrents the smallest power of two from 256 KiB to 16 MiB
// that keeps it to about 2,000 pieces, as torrent makers do.
func pieceLengthFor(total int64, real bool) int64 {
	pl := int64(loadPiece)
	for real && total/pl > 2000 && pl < 16<<20 {
		pl *= 2
	}
	return pl
}

// loadResult is what a run measured. Memory is in bytes.
type loadResult struct {
	loadRun
	// The fresh test process before anything was made.
	BaseRSS  int64 `json:"baseRSS"`
	BaseAnon int64 `json:"baseAnon"`
	// AvgPieces is the torrents' average piece count.
	AvgPieces float64 `json:"avgPieces"`
	// After the settle time: Go's heap in use, the process's resident memory,
	// and the anonymous part of it (what a container's memory.stat "anon"
	// counts: not the page cache of files read), and the most anon seen
	// while settling.
	HeapInuse int64 `json:"heapInuse"`
	// HeapLive is the heap in use right after a garbage collection: what the
	// engine really holds, without garbage not yet collected.
	HeapLive   int64   `json:"heapLive"`
	RSS        int64   `json:"rss"`
	Anon       int64   `json:"anon"`
	MaxAnon    int64   `json:"maxAnon"`
	Goroutines int     `json:"goroutines"`
	FDs        int     `json:"fds"`
	CPUPercent float64 `json:"cpuPercent"` // of one core, over the settle time
	Running    int     `json:"running"`
	Parked     int     `json:"parked"`
	// Announces the stub tracker got per minute over the settle time, and
	// in all (adding included).
	AnnouncesPerMin float64 `json:"announcesPerMin"`
	Announces       int64   `json:"announces"`
	AddSeconds      float64 `json:"addSeconds"` // to add all N
	// <data>/builtin: the engine's records, the piece records and the kept
	// .torrent files.
	StateDB     int64 `json:"stateDB"`
	PieceDB     int64 `json:"pieceDB"`
	TorrentsDir int64 `json:"torrentsDir"`
	// Opening the engine again from the same folder.
	CloseSeconds  float64 `json:"closeSeconds"`
	ReopenSeconds float64 `json:"reopenSeconds"`
	ReopenBack    int     `json:"reopenBack"` // torrents listed again, complete
	// After the reopen: torrents running, and the heap after a garbage
	// collection. A parked torrent isn't parked again after a restart (only
	// the ones in a slot are handed to the client), so this is lower.
	ReopenRunning  int   `json:"reopenRunning"`
	ReopenHeapLive int64 `json:"reopenHeapLive"`
	// ReopenReadMB is what the reopen read through system calls: the kept
	// .torrent files and the records. Hashing the files again would read
	// gigabytes.
	ReopenReadMB float64 `json:"reopenReadMB"`
	UploadMBs    float64 `json:"uploadMBs"` // to the peers, over the settle time
	Refused      int64   `json:"refused"`   // non-loopback attempts (must be 0)
}

const (
	loadPiece    = 256 << 10
	loadResultAt = "LOADRESULT "
)

// TestLoadBuiltin runs the whole set of measurements, each in its own process,
// and writes the table. LOAD_RUNS can pick runs, as "n:seeds[:peers]" separated
// by commas; LOAD_SETTLE shortens the settle time.
func TestLoadBuiltin(t *testing.T) {
	runs := os.Getenv("LOAD_RUNS")
	if runs == "" {
		runs = "0:25,100:25,100:0,1000:25,1000:0,3000:25,3000:0,5000:25,5000:0,3000:25:4"
	}
	settle := 5 * time.Minute
	if s := os.Getenv("LOAD_SETTLE"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			t.Fatal(err)
		}
		settle = d
	}
	out := os.Getenv("LOAD_OUT")
	if out == "" {
		out = filepath.Join(os.TempDir(), "load-builtin")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	var results []loadResult
	for _, spec := range strings.Split(runs, ",") {
		parts := strings.Split(strings.TrimSpace(spec), ":")
		r := loadRun{Settle: settle, Interval: 5 * time.Minute, PeerKBs: 10 << 10, Out: out, Real: os.Getenv("LOAD_REAL") != ""}
		var err error
		if r.N, err = strconv.Atoi(parts[0]); err == nil && len(parts) > 1 {
			r.Seeds, err = strconv.Atoi(parts[1])
		}
		if err == nil && len(parts) > 2 {
			r.Peers, err = strconv.Atoi(parts[2])
		}
		if err != nil || len(parts) < 2 {
			t.Fatalf("LOAD_RUNS: %q isn't n:seeds[:peers]", spec)
		}
		t.Logf("run: %d torrents, seeds %d, peers %d", r.N, r.Seeds, r.Peers)
		res, err := runChild[loadResult](t, "TestLoadOne", "LOAD_ONE", r)
		if err != nil {
			t.Fatalf("run %s: %v", spec, err)
		}
		results = append(results, res)
		if res.Refused != 0 {
			t.Errorf("run %s: the engine tried to reach %d non-loopback addresses", spec, res.Refused)
		}
		writeResults(t, out, results) // after each run, so a long run keeps what it has
	}
	b, _ := os.ReadFile(filepath.Join(out, "results.md"))
	t.Logf("\n%s", b)
}

// runChild runs this test binary again for one test, with the job as JSON in
// env, and returns the JSON it prints after LOADRESULT.
func runChild[T any](t *testing.T, test, env string, job any) (T, error) {
	var res T
	exe, err := os.Executable()
	if err != nil {
		return res, err
	}
	j, _ := json.Marshal(job)
	cmd := exec.Command(exe, "-test.run=^"+test+"$", "-test.v", "-test.timeout=0", "-test.count=1") //nolint:gosec // our own test binary
	cmd.Env = append(os.Environ(), env+"="+string(j))
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return res, err
	}
	if err := cmd.Start(); err != nil {
		return res, err
	}
	found := false
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, loadResultAt); i >= 0 {
			found = json.Unmarshal([]byte(line[i+len(loadResultAt):]), &res) == nil
			continue
		}
		t.Log(line)
	}
	if err := cmd.Wait(); err != nil {
		return res, err
	}
	if !found {
		return res, fmt.Errorf("%s printed no result", test)
	}
	return res, nil
}

func mb(b int64) float64 { return float64(b) / (1 << 20) }

func writeResults(t *testing.T, out string, rs []loadResult) {
	j, _ := json.MarshalIndent(rs, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "results.json"), j, 0o644); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("| torrents | pieces each | seed slots | peers | running | parked | heap MB | live heap MB | RSS MB | anon MB | anon − base MB | max anon MB | goroutines | fds | CPU % | announces/min | announces | add s | state.db MB | piece db MB | .torrents MB | reopen s | back | reopen read MB | running after reopen | live heap after reopen MB | upload MB/s |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range rs {
		seeds := strconv.Itoa(r.Seeds)
		if r.Seeds == 0 {
			seeds = "no limit"
		}
		fmt.Fprintf(&b, "| %d | %.0f | %s | %d | %d | %d | %.0f | %.0f | %.0f | %.0f | %.0f | %.0f | %d | %d | %.1f | %.0f | %d | %.1f | %.1f | %.1f | %.1f | %.1f | %d | %.0f | %d | %.0f | %.1f |\n",
			r.N, r.AvgPieces, seeds, r.Peers, r.Running, r.Parked, mb(r.HeapInuse), mb(r.HeapLive), mb(r.RSS), mb(r.Anon), mb(r.Anon-r.BaseAnon), mb(r.MaxAnon),
			r.Goroutines, r.FDs, r.CPUPercent, r.AnnouncesPerMin, r.Announces, r.AddSeconds, mb(r.StateDB), mb(r.PieceDB), mb(r.TorrentsDir),
			r.ReopenSeconds, r.ReopenBack, r.ReopenReadMB, r.ReopenRunning, mb(r.ReopenHeapLive), r.UploadMBs)
	}
	if err := os.WriteFile(filepath.Join(out, "results.md"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadOne is one measurement, in its own process (see TestLoadBuiltin).
func TestLoadOne(t *testing.T) {
	job := os.Getenv("LOAD_ONE")
	if job == "" {
		t.Skip("run by TestLoadBuiltin")
	}
	var r loadRun
	if err := json.Unmarshal([]byte(job), &r); err != nil {
		t.Fatal(err)
	}
	res := loadResult{loadRun: r}
	res.BaseRSS, res.BaseAnon = procMemory(t)

	stub := newAnnounceStub(r.Interval)
	defer stub.Close()
	data, folder, staging := t.TempDir(), t.TempDir(), t.TempDir()
	hashes, pieces := makeLoadTorrents(t, staging, folder, stub.URL+"/announce", r.N, r.Peers, r.Real)
	for _, p := range pieces {
		res.AvgPieces += float64(p) / float64(max(r.N, 1))
	}
	fillPieceRecords(t, filepath.Join(data, "builtin"), hashes, pieces)

	nw := &loopbackNet{}
	// Every torrent starts out running, as if the engine had downloaded it:
	// in real use a download that finishes with no seeding slot free is
	// parked, not dropped. One that never ran (only added complete) isn't in
	// the client at all and costs next to nothing, so starting them all in
	// slots and then setting the limit measures the real case.
	cfg := Config{
		DataDir: data, Folder: folder, ListenHost: "127.0.0.1",
		MaxActiveDownloads: 3, MaxActiveSeeds: 0,
		DHT: false, PEX: true, Network: nw,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	e, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := range r.N {
		b, err := os.ReadFile(filepath.Join(staging, fmt.Sprintf("%05d.torrent", i)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Add(AddRequest{Torrent: b, Category: cat, SavePath: folder}); err != nil {
			t.Fatal(err)
		}
	}
	res.AddSeconds = time.Since(start).Seconds()
	waitAllSeeding(t, e, r.N)
	waitRunning(t, e, r.N)
	if r.Seeds > 0 {
		cfg.MaxActiveSeeds = r.Seeds
		e.Configure(cfg)
		waitRunning(t, e, min(r.Seeds, r.N))
	}
	t.Logf("%d torrents added in %.1fs, all seeding, %d running", r.N, res.AddSeconds, running(e))

	// Settle (with the peers downloading, for that run).
	var peerDone chan error
	if r.Peers > 0 {
		var ts []string
		for i := range r.Peers {
			ts = append(ts, filepath.Join(staging, fmt.Sprintf("%05d.torrent", i)))
		}
		peerDone = make(chan error, 1)
		job := loadPeerJob{Addr: fmt.Sprintf("127.0.0.1:%d", e.Port()), Torrents: ts, KBs: r.PeerKBs, For: r.Settle}
		go func() {
			_, err := runChild[struct{}](t, "TestLoadPeer", "LOAD_PEER", job)
			peerDone <- err
		}()
	}
	up0 := uploaded(t, e, r.Peers)
	cpu0, ann0, t0 := cpuSeconds(), stub.count.Load(), time.Now()
	for deadline := t0.Add(r.Settle); time.Now().Before(deadline); time.Sleep(10 * time.Second) {
		_, anon := procMemory(t)
		res.MaxAnon = max(res.MaxAnon, anon)
	}
	elapsed := time.Since(t0)
	res.CPUPercent = (cpuSeconds() - cpu0) / elapsed.Seconds() * 100
	res.AnnouncesPerMin = float64(stub.count.Load()-ann0) / elapsed.Minutes()
	res.UploadMBs = mb(uploaded(t, e, r.Peers)-up0) / elapsed.Seconds()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	res.HeapInuse = int64(ms.HeapInuse)
	res.RSS, res.Anon = procMemory(t)
	res.MaxAnon = max(res.MaxAnon, res.Anon)
	res.HeapLive = heapLive()
	res.Goroutines = runtime.NumGoroutine()
	res.FDs = fdCount(t)
	e.mu.Lock()
	for _, en := range e.ents {
		switch {
		case en.running():
			res.Running++
		case en.parked != nil:
			res.Parked++
		}
	}
	e.mu.Unlock()
	if r.Peers > 0 {
		if err := <-peerDone; err != nil {
			t.Errorf("the peers: %v", err)
		}
	}
	if f, err := os.Create(filepath.Join(r.Out, fmt.Sprintf("heap-%d-seeds%d-peers%d.pprof", r.N, r.Seeds, r.Peers))); err == nil {
		_ = pprof.WriteHeapProfile(f)
		_ = f.Close()
	}

	// Close, and open again from the same folder.
	start = time.Now()
	closeEngine(t, e)
	res.CloseSeconds = time.Since(start).Seconds()
	read0 := ioChars(t)
	start = time.Now()
	e2, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	res.ReopenSeconds = time.Since(start).Seconds()
	all, _ := e2.List(cat)
	for _, s := range all {
		if s.Progress >= 1 {
			res.ReopenBack++
		}
	}
	res.ReopenReadMB = mb(ioChars(t) - read0)
	want := r.N // with no limit, all run again
	if r.Seeds > 0 {
		want = min(r.Seeds, r.N)
	}
	waitRunning(t, e2, want)
	res.ReopenRunning, res.ReopenHeapLive = running(e2), heapLive()
	closeEngine(t, e2)
	dir := filepath.Join(data, "builtin")
	res.StateDB, res.PieceDB = fileSize(filepath.Join(dir, "state.db")), fileSize(filepath.Join(dir, ".torrent.bolt.db"))
	res.TorrentsDir = dirSize(t, filepath.Join(dir, torrentsDir))
	res.Refused = nw.refused.Load()
	res.Announces = stub.count.Load()
	j, _ := json.Marshal(res)
	fmt.Println(loadResultAt + string(j))
}

// waitAllSeeding waits until every torrent is complete and none is being
// checked.
func waitAllSeeding(t *testing.T, e *Engine, n int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Minute)
	for {
		all, err := e.List(cat)
		if err != nil {
			t.Fatal(err)
		}
		done := 0
		for _, s := range all {
			if s.Progress >= 1 && s.State != rktorrent.Checking {
				done++
			}
		}
		if done == n && len(all) == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d torrents are complete", done, n)
		}
		time.Sleep(time.Second)
	}
}

// waitRunning waits until n torrents are running.
func waitRunning(t *testing.T, e *Engine, n int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Minute); running(e) != n; time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatalf("%d torrents are running; waited for %d", running(e), n)
		}
	}
}

// uploaded is what the first n torrents have uploaded.
func uploaded(t *testing.T, e *Engine, n int) int64 {
	if n == 0 {
		return 0
	}
	all, err := e.List(cat)
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	for _, s := range all[:min(n, len(all))] { // List is in queue order
		sum += s.Uploaded
	}
	return sum
}

// ---- made-up torrents

// makeLoadTorrents writes n torrents to staging (00000.torrent, ...) and their
// files, sparse, under folder. The first big ones are 400 MB single files,
// for the peers. It returns each torrent's info hash and piece count.
func makeLoadTorrents(t *testing.T, staging, folder, announce string, n, big int, real bool) ([]metainfo.Hash, []int) {
	t.Helper()
	zero := map[int64][]byte{}
	hashOfZeros := func(n int64) []byte {
		if h, ok := zero[n]; ok {
			return h
		}
		sum := sha1.Sum(make([]byte, n)) //nolint:gosec // see import
		zero[n] = sum[:]
		return sum[:]
	}
	hashes, pieces := make([]metainfo.Hash, n), make([]int, n)
	priv := true
	for i := range n {
		r := rand.New(rand.NewPCG(uint64(i), 7)) //nolint:gosec // made-up sizes, not security
		name := fmt.Sprintf("Load.Title.%05d.2020.1080p.WEB-DL-LOAD", i)
		type file struct {
			path []string
			size int64
		}
		var files []file
		single := false
		switch k := r.IntN(100); {
		case i < big:
			single, files = true, []file{{[]string{name + ".mkv"}, 400 << 20}}
		case k < 50: // a movie
			single, files = true, []file{{[]string{name + ".mkv"}, int64(50+r.IntN(350)) << 20}}
		case k < 85: // a season folder
			for ep := range 3 + r.IntN(10) {
				files = append(files, file{[]string{fmt.Sprintf("%s.S01E%02d.mkv", name, ep+1)}, int64(10+r.IntN(30)) << 20})
			}
			files = append(files, file{[]string{name + ".nfo"}, 2 << 10})
		default: // something small
			single, files = true, []file{{[]string{name + ".mkv"}, int64(1+r.IntN(20)) << 20}}
		}
		if real && i >= big {
			for k := range files {
				if strings.HasSuffix(files[k].path[0], ".mkv") {
					files[k].size *= 100
				}
			}
		}
		var total int64
		for _, f := range files {
			total += f.size
			p := filepath.Join(append([]string{folder}, f.path...)...)
			if !single {
				p = filepath.Join(append([]string{folder, name}, f.path...)...)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(p, f.size); err != nil { // sparse: no disk space
				t.Fatal(err)
			}
		}
		pl := pieceLengthFor(total, real)
		info := metainfo.Info{PieceLength: pl, Private: &priv}
		if single {
			info.Name, info.Length = files[0].path[0], files[0].size
		} else {
			info.Name = name
			for _, f := range files {
				info.Files = append(info.Files, metainfo.FileInfo{Length: f.size, Path: f.path})
			}
		}
		np := int((total + pl - 1) / pl)
		info.Pieces = make([]byte, 0, np*sha1.Size)
		for p := range np {
			info.Pieces = append(info.Pieces, hashOfZeros(min(pl, total-int64(p)*pl))...)
		}
		mi := metainfo.MetaInfo{InfoBytes: bencode.MustMarshal(info), Announce: announce}
		b, err := bencode.Marshal(mi)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(staging, fmt.Sprintf("%05d.torrent", i)), b, 0o600); err != nil {
			t.Fatal(err)
		}
		hashes[i], pieces[i] = mi.HashInfoBytes(), np
	}
	return hashes, pieces
}

// fillPieceRecords marks every piece of these torrents good in the piece
// records the engine will open (anacrolix's bolt layout: bucket "completion",
// a bucket per info hash, the piece index big-endian, "c" for complete), so
// nothing has to be hashed.
func fillPieceRecords(t *testing.T, dir string, hashes []metainfo.Hash, pieces []int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	db, err := bbolt.Open(filepath.Join(dir, ".torrent.bolt.db"), 0o660, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for from := 0; from < len(hashes); from += 50 {
		err := db.Update(func(tx *bbolt.Tx) error {
			c, err := tx.CreateBucketIfNotExists([]byte("completion"))
			if err != nil {
				return err
			}
			for i := from; i < min(from+50, len(hashes)); i++ {
				b, err := c.CreateBucketIfNotExists(hashes[i][:])
				if err != nil {
					return err
				}
				var key [4]byte
				for p := range pieces[i] {
					binary.BigEndian.PutUint32(key[:], uint32(p)) //nolint:gosec // piece counts fit
					if err := b.Put(key[:], []byte("c")); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// ---- the stub tracker

type announceStub struct {
	*httptest.Server
	count atomic.Int64
}

// newAnnounceStub answers every announce with no peers and the interval.
func newAnnounceStub(interval time.Duration) *announceStub {
	s := &announceStub{}
	body := bencode.MustMarshal(map[string]any{"interval": int(interval.Seconds()), "peers": ""})
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.count.Add(1)
		_, _ = w.Write(body)
	}))
	return s
}

// ---- the peers

type loadPeerJob struct {
	Addr     string        `json:"addr"`
	Torrents []string      `json:"torrents"`
	KBs      int           `json:"kbs"`
	For      time.Duration `json:"for"`
}

// TestLoadPeer downloads each torrent from the engine, over and over, for the
// job's time, each as its own client (a peer) limited to KBs. Its storage keeps
// nothing: the made-up files are all zeros, so reading zeros back passes the
// hash check. Run by TestLoadOne, in its own process.
func TestLoadPeer(t *testing.T) {
	job := os.Getenv("LOAD_PEER")
	if job == "" {
		t.Skip("run by TestLoadOne")
	}
	var j loadPeerJob
	if err := json.Unmarshal([]byte(job), &j); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var rounds atomic.Int64
	deadline := time.Now().Add(j.For)
	for _, path := range j.Torrents {
		mi, err := metainfo.LoadFromFile(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg := newSeederConfig(false) // loopback only; no trackers, DHT or PEX
		cfg.Seed = false
		cfg.DataDir = t.TempDir()
		cfg.DownloadRateLimiter = rate.NewLimiter(rate.Limit(j.KBs<<10), 256<<10)
		cl, err := torrent.NewClient(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer cl.Close()
		wg.Go(func() {
			for time.Now().Before(deadline) {
				spec := torrent.TorrentSpecFromMetaInfo(mi)
				spec.Storage, spec.Trackers = zeroStorage{}, nil
				tt, _, err := cl.AddTorrentSpec(spec)
				if err != nil {
					t.Error(err)
					return
				}
				tt.AddPeers([]torrent.PeerInfo{{Addr: torrent.StringAddr(j.Addr), Trusted: true}})
				tt.DownloadAll()
				select {
				case <-tt.Complete().On():
					rounds.Add(1)
				case <-time.After(time.Until(deadline)):
				}
				tt.Drop()
			}
		})
	}
	wg.Wait()
	t.Logf("the peers finished %d downloads", rounds.Load())
	fmt.Println(loadResultAt + "{}")
}

// zeroStorage stores nothing and reads zeros; it only remembers which pieces
// are done.
type zeroStorage struct{}

func (zeroStorage) OpenTorrent(_ context.Context, info *metainfo.Info, _ metainfo.Hash) (storage.TorrentImpl, error) {
	done := make([]atomic.Bool, info.NumPieces())
	return storage.TorrentImpl{
		Piece: func(p metainfo.Piece) storage.PieceImpl { return zeroPiece{&done[p.Index()]} },
		Close: func() error { return nil },
	}, nil
}

type zeroPiece struct{ done *atomic.Bool }

func (zeroPiece) ReadAt(b []byte, _ int64) (int, error)  { clear(b); return len(b), nil }
func (zeroPiece) WriteAt(b []byte, _ int64) (int, error) { return len(b), nil }
func (p zeroPiece) MarkComplete() error                  { p.done.Store(true); return nil }
func (p zeroPiece) MarkNotComplete() error               { p.done.Store(false); return nil }
func (p zeroPiece) Completion() storage.Completion {
	return storage.Completion{Ok: true, Complete: p.done.Load()}
}

// ---- measuring the process (Linux)

// procMemory is the process's resident memory and the anonymous part of it,
// from /proc/self/status.
func procMemory(t *testing.T) (rss, anon int64) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		kb, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "VmRSS:":
			rss = kb << 10
		case "RssAnon:":
			anon = kb << 10
		}
	}
	return rss, anon
}

// heapLive is the heap in use after a garbage collection.
func heapLive() int64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return int64(ms.HeapInuse)
}

func cpuSeconds() float64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano()).Seconds()
}

// ioChars is the bytes the process has read through system calls (rchar in
// /proc/self/io), whether from disk or the page cache.
func ioChars(t *testing.T) int64 {
	b, err := os.ReadFile("/proc/self/io")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "rchar: "); ok {
			n, _ := strconv.ParseInt(v, 10, 64)
			return n
		}
	}
	return 0
}

func fdCount(t *testing.T) int {
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(es)
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

func dirSize(t *testing.T, dir string) int64 {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if info, err := d.Info(); err == nil && !d.IsDir() {
			n += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
