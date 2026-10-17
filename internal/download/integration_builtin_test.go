package download

// Moving a half-finished torrent between a real Transmission and the built-in
// client, with real files. Run by `just test-clients` (and skipped without the
// dev stack): it needs REELKEEPER_TEST_TRANSMISSION_URL set, as the other
// TestReal* tests do, and docker.
//
// The dev stack's Transmission keeps its files in a docker volume, which the
// built-in client (running in this test process, on the host) can't see, so
// the test starts a Transmission of its own from the same image, with a
// folder of this repository's ignored tmp/ directory mounted at the same path
// inside and outside the container. Nothing here reaches the internet or a
// real tracker or peer:
//
//   - The container has NO network at all (--network none). Its RPC port is
//     reached through `docker exec ... nc`, from a listener on 127.0.0.1.
//   - Its settings.json is written before it first starts, with DHT, peer
//     exchange, local peer discovery, uTP, port forwarding and the blocklist
//     off, so nothing runs even for a moment.
//   - The torrent is made up, private and has no tracker; the built-in
//     engine runs on a Network that refuses anything but 127.0.0.1.
//
// The container is removed afterwards, and so is its folder.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// isolatedTransmission is a throwaway Transmission with no network.
type isolatedTransmission struct {
	t         *testing.T
	container string
	folder    string // where its downloads go, the same path inside and out
	client    *Transmission
	cfg       settings.DownloadClient
}

func startIsolatedTransmission(t *testing.T) *isolatedTransmission {
	t.Helper()
	if os.Getenv("REELKEEPER_TEST_TRANSMISSION_URL") == "" {
		t.Skip("set REELKEEPER_TEST_TRANSMISSION_URL (the dev stack) to move torrents between Transmission and the built-in client")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker isn't installed")
	}
	// The same image as the dev stack's Transmission, which is already here:
	// nothing is pulled.
	dev := os.Getenv("REELKEEPER_TEST_TRANSMISSION_CONTAINER")
	if dev == "" {
		dev = "reelkeeper-dev-transmission"
	}
	img, err := exec.Command("docker", "inspect", "-f", "{{.Config.Image}}", dev).Output()
	if err != nil {
		t.Skipf("can't find the dev stack's Transmission container (%s): %v", dev, err)
	}
	image := strings.TrimSpace(string(img))

	// The folder is inside this repository (tmp/ is git-ignored), because the
	// docker VM can see the home folder, and the temporary folders of the
	// operating system aren't there.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoTmp := filepath.Join(wd, "..", "..", "tmp")
	if err := os.MkdirAll(repoTmp, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(repoTmp, "builtin-it-")
	if err != nil {
		t.Fatal(err)
	}
	if root, err = filepath.Abs(root); err != nil {
		t.Fatal(err)
	}
	conf, dl := filepath.Join(root, "config"), filepath.Join(root, "dl")
	for _, d := range []string{conf, dl} {
		if err := os.MkdirAll(d, 0o777); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(d, 0o777)
	}
	settingsJSON, _ := json.Marshal(map[string]any{
		"dht-enabled": false, "pex-enabled": false, "lpd-enabled": false, "utp-enabled": false,
		"port-forwarding-enabled": false, "blocklist-enabled": false, "peer-port-random-on-start": false,
		"download-dir": dl, "incomplete-dir-enabled": false, "rename-partial-files": true,
		"rpc-authentication-required": false, "rpc-whitelist-enabled": false, "rpc-host-whitelist-enabled": false,
		"umask": "000", "download-queue-enabled": false, "seed-queue-enabled": false, "start-added-torrents": true,
		"speed-limit-down-enabled": false, "script-torrent-done-enabled": false,
	})
	if err := os.WriteFile(filepath.Join(conf, "settings.json"), settingsJSON, 0o666); err != nil {
		t.Fatal(err)
	}

	name := fmt.Sprintf("reelkeeper-test-builtin-%d", time.Now().UnixNano())
	out, err := exec.Command("docker", "run", "-d", "--pull", "never", "--name", name, "--network", "none",
		"-e", fmt.Sprintf("PUID=%d", os.Getuid()), "-e", fmt.Sprintf("PGID=%d", os.Getgid()),
		"-v", conf+":/config", "-v", dl+":"+dl, image).CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(root)
		t.Fatalf("starting the throwaway Transmission: %v\n%s", err, out)
	}
	it := &isolatedTransmission{t: t, container: name, folder: dl}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
		_ = os.RemoveAll(root)
		_ = os.Remove(repoTmp) // only if nothing else is in it
	})

	// Its RPC port, from a listener on 127.0.0.1 that hands each connection to
	// `nc` inside the container.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go it.bridge(l)
	it.client = &Transmission{URL: "http://" + l.Addr().String()}
	it.cfg = settings.DownloadClient{Type: settings.ClientTransmission, URL: it.client.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for {
		if _, err := it.client.Version(ctx); err == nil {
			break
		}
		if ctx.Err() != nil {
			logs, _ := exec.Command("docker", "logs", "--tail", "20", name).CombinedOutput()
			t.Fatalf("the throwaway Transmission never answered:\n%s", logs)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return it
}

// bridge connects each connection to Transmission's RPC port in the container.
func (it *isolatedTransmission) bridge(l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			cmd := exec.Command("docker", "exec", "-i", it.container, "nc", "127.0.0.1", "9091")
			in, err := cmd.StdinPipe()
			if err != nil {
				return
			}
			outp, err := cmd.StdoutPipe()
			if err != nil {
				return
			}
			if err := cmd.Start(); err != nil {
				return
			}
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = io.Copy(conn, outp)
				_ = conn.Close()
			}()
			_, _ = io.Copy(in, conn)
			_ = in.Close()
			_ = cmd.Process.Kill()
			wg.Wait()
			_ = cmd.Wait()
		}()
	}
}

// bytesOfFile reads a file under either name it may have: Transmission names
// a file it hasn't finished with ".part".
func bytesOfFile(t *testing.T, path string) ([]byte, string) {
	t.Helper()
	for _, p := range []string{path, path + ".part"} {
		if b, err := os.ReadFile(p); err == nil {
			return b, p
		}
	}
	t.Fatalf("neither %s nor %s.part exists", path, path)
	return nil, ""
}

func realWorld2(t *testing.T, db *store.Store, it *isolatedTransmission) *bworld {
	t.Helper()
	w := newBWorld(t, db, it.folder)
	w.ext = it.client
	w.xcfg = it.cfg
	w.f.svc.moveTiming = moveTiming{poll: 150 * time.Millisecond, begin: 5 * time.Second, stop: 30 * time.Second, check: 90 * time.Second,
		metadata: 20 * time.Second, files: 30 * time.Second, giveUp: moveGiveUp}
	return w
}

// A half-finished download in a real Transmission moves to the built-in
// client: Transmission is stopped, its ".part" file gets its plain name, the
// built-in client finds the same pieces, nothing is downloaded and nothing is
// deleted.
func TestRealMoveTransmissionToBuiltin(t *testing.T) {
	it := startIsolatedTransmission(t)
	{
		db := storetest.Open(t, store.SQLite) // one database: the container is shared
		ctx := context.Background()
		h := newHalfDoneIn(t, it.folder, "Half.Done.mkv", 8)
		w := realWorld2(t, db, it)
		w.download(h, StatusDownloading, settings.ClientTransmission)

		// Transmission takes it on, checks the data, and renames the file
		// to the ending it gives unfinished ones.
		if _, err := it.client.Add(ctx, Add{Torrent: h.raw, Category: settings.MovieCategory, SavePath: it.folder, Tags: []string{"reelkeeper", "Tracker"}}); err != nil {
			t.Fatal(err)
		}
		waitReal(t, "Transmission to check the data", func() bool {
			ts, _ := it.client.Torrents(ctx, settings.MovieCategory)
			return len(ts) == 1 && ts[0].Verified == h.haveBytes && ts[0].State != torrent.Checking
		})
		before, was := bytesOfFile(t, h.file)
		t.Logf("Transmission keeps the file as %s", filepath.Base(was))

		w.f.cfg = w.xcfg
		w.switchClient(w.xcfg, w.bcfg, false)
		w.run()

		d := w.f.download(t, db, h.hash)
		if d.MoveState != "" || d.Client != settings.ClientBuiltin {
			t.Fatalf("download after the move = %+v", d)
		}
		if evs := moveFailures(t, w.f); len(evs) != 0 {
			t.Fatalf("the move failed: %s", evs[0].Message)
		}
		if ts, _ := it.client.Torrents(ctx, settings.MovieCategory); len(ts) != 0 {
			t.Errorf("Transmission still has %d torrents", len(ts))
		}
		tr := w.builtinTorrent(h.hash)
		if tr == nil || tr.Verified != h.haveBytes || tr.Done() {
			t.Fatalf("the built-in client's torrent = %+v (want %d bytes verified)", tr, h.haveBytes)
		}
		after, now := bytesOfFile(t, h.file)
		if now != h.file {
			t.Errorf("the file is still called %s", filepath.Base(now))
		}
		if !bytes.Equal(before, after) {
			t.Error("the file's content changed")
		}
		if entries, _ := os.ReadDir(it.folder); len(entries) != 1 {
			t.Errorf("the download folder holds %d things, want just the file", len(entries))
		}
		if w.nw.dials.Load() != 0 {
			t.Errorf("the built-in client opened %d connections", w.nw.dials.Load())
		}
		if tr.Uploaded != 0 {
			t.Errorf("uploaded %d", tr.Uploaded)
		}
	}
}

// And the other way: the built-in client's running torrent goes to a real
// Transmission, which checks the files and finds what the built-in one had.
func TestRealMoveBuiltinToTransmission(t *testing.T) {
	it := startIsolatedTransmission(t)
	{
		db := storetest.Open(t, store.SQLite)
		ctx := context.Background()
		h := newHalfDoneIn(t, it.folder, "Half.Done.mkv", 8)
		w := realWorld2(t, db, it)
		before := h.bytesOnDisk(t)
		w.f.cfg = w.bcfg
		w.host.Apply(w.bcfg, settings.DownloadMove{})
		b := &Builtin{cfg: w.bcfg, host: w.host}
		if _, err := b.Add(ctx, Add{Torrent: h.raw, Category: settings.MovieCategory, SavePath: it.folder, Tags: []string{"reelkeeper", "Tracker"}}); err != nil {
			t.Fatal(err)
		}
		w.download(h, StatusDownloading, settings.ClientBuiltin)
		waitReal(t, "the built-in client to find what is on disk", func() bool {
			tr := w.builtinTorrent(h.hash)
			return tr != nil && tr.Verified == h.haveBytes
		})

		w.switchClient(w.bcfg, w.xcfg, false)
		w.run()

		d := w.f.download(t, db, h.hash)
		if d.MoveState != "" || d.Client != settings.ClientTransmission {
			t.Fatalf("download after the move = %+v", d)
		}
		if evs := moveFailures(t, w.f); len(evs) != 0 {
			t.Fatalf("the move failed: %s", evs[0].Message)
		}
		var tm *Torrent
		waitReal(t, "Transmission to settle", func() bool {
			ts, _ := it.client.Torrents(ctx, settings.MovieCategory)
			if len(ts) == 1 {
				tm = &ts[0]
			}
			return tm != nil && tm.State != torrent.Checking
		})
		if tm.Verified != h.haveBytes || tm.Paused() || tm.Done() {
			t.Errorf("Transmission's torrent = verified %d of %d, state %s", tm.Verified, tm.Size, tm.State)
		}
		after, _ := bytesOfFile(t, h.file)
		if !bytes.Equal(before, after) {
			t.Error("the file's content changed")
		}
		if e, _, _ := w.host.running(); e != nil {
			t.Error("the built-in engine is still running after the move")
		}
	}
}

func waitReal(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("gave up waiting for %s", what)
}
