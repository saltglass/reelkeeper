package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/torrent"
)

// The tests below run against a real Transmission when
// REELKEEPER_TEST_TRANSMISSION_URL (plus _USER and _PASS) is set, such as
// the dev stack's (http://localhost:9092, reelkeeper/reelkeeper). Nothing here touches
// the internet: torrents are made up, private (no DHT) and have no tracker.
//
// The ones that need files on Transmission's disk put them there with
// `docker exec` into the dev stack's container, named by
// REELKEEPER_TEST_TRANSMISSION_CONTAINER (reelkeeper-dev-transmission by default),
// and skip when docker isn't there.

func realTransmission(t *testing.T) *Transmission {
	t.Helper()
	u := os.Getenv("REELKEEPER_TEST_TRANSMISSION_URL")
	if u == "" {
		t.Skip("set REELKEEPER_TEST_TRANSMISSION_URL, _USER and _PASS to test against a real Transmission")
	}
	return &Transmission{URL: u, Username: os.Getenv("REELKEEPER_TEST_TRANSMISSION_USER"), Password: os.Getenv("REELKEEPER_TEST_TRANSMISSION_PASS")}
}

// dockerExec runs a shell command as root in a dev stack container, with
// stdin as its input.
func dockerExec(t *testing.T, container string, stdin []byte, script string) string {
	t.Helper()
	cmd := exec.Command("docker", "exec", "-i", "-u", "root", container, "sh", "-c", script)
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker exec %s: %v\n%s", container, err, out)
	}
	return strings.TrimSpace(string(out))
}

func transmissionContainer(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker isn't installed; this test puts files in the dev stack's containers")
	}
	c := os.Getenv("REELKEEPER_TEST_TRANSMISSION_CONTAINER")
	if c == "" {
		c = "reelkeeper-dev-transmission"
	}
	return c
}

// putFile writes a file into the dev stack's shared downloads volume, owned by
// the user the containers run as.
func putFile(t *testing.T, container, dir, name string, data []byte) {
	t.Helper()
	dockerExec(t, container, data, fmt.Sprintf(
		"mkdir -p '%[1]s' && cat > '%[1]s/%[2]s' && chown -R 1000:1000 '%[1]s'", dir, name))
}

// someData is deterministic made-up content of the given size.
func someData(seed string, size int) []byte {
	out := make([]byte, 0, size)
	for i := 0; len(out) < size; i++ {
		sum := sha256.Sum256(fmt.Appendf(nil, "%s-%d", seed, i))
		out = append(out, sum[:]...)
	}
	return out[:size]
}

// findTorrent returns the torrent with this hash in the category, or nil.
func findTorrent(t *testing.T, tr *Transmission, cat, hash string) *Torrent {
	t.Helper()
	ts, err := tr.Torrents(context.Background(), cat)
	if err != nil {
		t.Fatal(err)
	}
	for i := range ts {
		if ts[i].Hash == hash {
			return &ts[i]
		}
	}
	return nil
}

// waitForTorrent polls until the torrent satisfies ok, and returns it.
func waitForTorrent(t *testing.T, tr *Transmission, cat, hash, what string, timeout time.Duration, ok func(Torrent) bool) Torrent {
	t.Helper()
	var last *Torrent
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		if last = findTorrent(t, tr, cat, hash); last != nil && ok(*last) {
			return *last
		}
	}
	if last == nil {
		t.Fatalf("%s: torrent not listed", what)
	}
	t.Fatalf("%s: state %q, progress %.2f", what, last.State, last.Progress)
	return Torrent{}
}

func TestRealTransmission(t *testing.T) {
	tr := realTransmission(t)
	ctx := context.Background()
	v, err := tr.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Transmission %s", v)

	const cat, cat2 = "reelkeeper-test", "reelkeeper-test-moved"
	data := someData("reelkeeper-transmission-test", 40000)
	file := testTorrentFile(t, "reelkeeper-test.bin", data)
	hash, _ := InfoHash(file)
	// Another app's torrent in the same Transmission, to prove Reelkeeper
	// leaves it alone.
	otherFile := testTorrentFile(t, "someone-elses.bin", someData("someone else", 20000))
	otherHash, _ := InfoHash(otherFile)
	t.Cleanup(func() {
		// Whatever the test left behind, with its data. The other app's torrent
		// isn't Reelkeeper's to remove, so it goes by raw RPC.
		for _, c := range []string{cat, cat2} {
			_ = tr.Remove(ctx, c, []string{hash}, true)
		}
		_ = tr.call(ctx, "clean up", "torrent-remove", map[string]any{"ids": []string{otherHash}, "delete-local-data": true}, nil)
	})
	if err := tr.EnsureCategory(ctx, cat, ""); err != nil {
		t.Fatal(err)
	}

	// Another app's torrent: added without Reelkeeper's label.
	if err := tr.call(ctx, "add", "torrent-add", map[string]any{
		"metainfo": base64.StdEncoding.EncodeToString(otherFile), "paused": true, "labels": []string{"radarr"}, "download-dir": "/mnt/downloads/reelkeeper-test",
	}, nil); err != nil {
		t.Fatal(err)
	}

	// Added stopped, in a folder of its own, with the category and a tag.
	got, err := tr.Add(ctx, Add{Torrent: file, Category: cat, Tags: []string{"reelkeeper", "test-indexer"}, Stopped: true, SavePath: "/mnt/downloads/reelkeeper-test"})
	if err != nil || got != hash {
		t.Fatalf("Add = %q, %v (want %s)", got, err, hash)
	}
	// Adding it again is fine (it is Reelkeeper's); another category's is not.
	if again, err := tr.Add(ctx, Add{Torrent: file, Category: cat, Stopped: true}); err != nil || again != hash {
		t.Errorf("Add again = %q, %v", again, err)
	}
	if _, err := tr.Add(ctx, Add{Torrent: file, Category: cat2, Stopped: true}); err == nil {
		t.Error("Add into another category took over a torrent")
	}
	if _, err := tr.Add(ctx, Add{Torrent: otherFile, Category: cat, Stopped: true}); err == nil {
		t.Error("Add took over another app's torrent")
	}

	// Listed with the right category, folder, size and a state Reelkeeper knows.
	tor := waitForTorrent(t, tr, cat, hash, "listed", 10*time.Second, func(Torrent) bool { return true })
	if tor.Category != cat || tor.Name != "reelkeeper-test.bin" || tor.Size != int64(len(data)) || tor.SavePath != "/mnt/downloads/reelkeeper-test" ||
		tor.ContentPath != "/mnt/downloads/reelkeeper-test/reelkeeper-test.bin" || tor.Left != int64(len(data)) {
		t.Errorf("torrent = %+v", tor)
	}
	// (Transmission may check the torrent's files first, which shows as checking.)
	tor = waitForTorrent(t, tr, cat, hash, "added stopped", 10*time.Second, func(x Torrent) bool { return x.State == torrent.Paused })
	if !tor.Paused() {
		t.Errorf("added stopped, but state is %q", tor.State)
	}
	if ts, _ := tr.Torrents(ctx, cat); len(ts) != 1 {
		t.Errorf("category lists %d torrents, want only ours: %+v", len(ts), ts)
	}
	if findTorrent(t, tr, cat, otherHash) != nil {
		t.Error("another app's torrent is listed in Reelkeeper's category")
	}
	// The labels as Transmission has them.
	rows, err := tr.get(ctx, []string{hash}, []string{"hashString", "labels"})
	if err != nil || len(rows) != 1 || strings.Join(rows[0].Labels, ",") != cat+",reelkeeper,test-indexer" {
		t.Errorf("labels = %+v, %v", rows, err)
	}

	// Start: not paused any more (nobody has the data, so it downloads, or
	// stalls). Stop: paused again.
	if err := tr.Start(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	tor = waitForTorrent(t, tr, cat, hash, "after Start", 10*time.Second, func(x Torrent) bool { return x.State != torrent.Paused })
	if tor.State == torrent.Unknown {
		t.Errorf("Transmission reported a state Reelkeeper doesn't recognise")
	}
	if err := tr.TopPriority(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	if err := tr.NoShareLimits(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	var lim struct {
		Torrents []struct {
			SeedRatioMode int `json:"seedRatioMode"`
			SeedIdleMode  int `json:"seedIdleMode"`
		} `json:"torrents"`
	}
	if err := tr.call(ctx, "read the limits", "torrent-get", map[string]any{"ids": []string{hash}, "fields": []string{"seedRatioMode", "seedIdleMode"}}, &lim); err != nil {
		t.Fatal(err)
	}
	if len(lim.Torrents) != 1 || lim.Torrents[0].SeedRatioMode != 2 || lim.Torrents[0].SeedIdleMode != 2 {
		t.Errorf("seeding limits = %+v, want both unlimited (2)", lim.Torrents)
	}
	if _, err := tr.TrackerMessages(ctx, cat, hash); err != nil {
		t.Fatal(err)
	}
	if err := tr.Stop(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	waitForTorrent(t, tr, cat, hash, "after Stop", 10*time.Second, func(x Torrent) bool { return x.State == torrent.Paused })

	// The guard, against the real thing: Reelkeeper's calls refuse the other
	// app's torrent, which stays as it was.
	if err := tr.Start(ctx, cat, []string{otherHash}); err == nil {
		t.Error("Start touched another app's torrent")
	}
	if err := tr.Remove(ctx, cat, []string{otherHash}, true); err == nil {
		t.Error("Remove touched another app's torrent")
	}
	var other struct {
		Torrents []rawTorrent `json:"torrents"`
	}
	if err := tr.call(ctx, "read", "torrent-get", map[string]any{"ids": []string{otherHash}, "fields": []string{"labels", "status"}}, &other); err != nil ||
		len(other.Torrents) != 1 || strings.Join(other.Torrents[0].Labels, ",") != "radarr" || other.Torrents[0].Status != 0 {
		t.Errorf("the other app's torrent changed: %+v, %v", other.Torrents, err)
	}

	// MoveCategory: the category label changes, the other labels stay.
	if err := tr.MoveCategory(ctx, cat, cat2, []string{hash}); err != nil {
		t.Fatal(err)
	}
	if findTorrent(t, tr, cat, hash) != nil {
		t.Error("still in the old category")
	}
	if findTorrent(t, tr, cat2, hash) == nil {
		t.Fatal("not in the new category")
	}
	rows, _ = tr.get(ctx, []string{hash}, []string{"hashString", "labels"})
	if len(rows) != 1 || strings.Join(rows[0].Labels, ",") != cat2+",reelkeeper,test-indexer" {
		t.Errorf("labels after the move = %+v", rows)
	}

	// Remove with its data.
	if err := tr.Remove(ctx, cat2, []string{hash}, true); err != nil {
		t.Fatal(err)
	}
	if findTorrent(t, tr, cat2, hash) != nil {
		t.Error("still there after Remove")
	}
}

// A finished download, without any network: the file is already in the
// folder, so Transmission's check finds it complete and goes straight to
// seeding.
func TestRealTransmissionCompleted(t *testing.T) {
	tr := realTransmission(t)
	container := transmissionContainer(t)
	ctx := context.Background()
	if _, err := tr.Version(ctx); err != nil {
		t.Fatal(err)
	}
	const cat, dir, name = "reelkeeper-test-done", "/mnt/downloads/reelkeeper-test-done", "Finished.Film.2024.mkv"
	data := someData("finished film", 300000)
	file := testTorrentFile(t, name, data)
	hash, _ := InfoHash(file)
	putFile(t, container, dir, name, data)
	t.Cleanup(func() {
		_ = tr.Remove(ctx, cat, []string{hash}, true)
		dockerExec(t, container, nil, "rm -rf '"+dir+"'")
	})

	if _, err := tr.Add(ctx, Add{Torrent: file, Category: cat, Tags: []string{"reelkeeper"}, SavePath: dir}); err != nil {
		t.Fatal(err)
	}
	tor := waitForTorrent(t, tr, cat, hash, "finished", 30*time.Second, func(x Torrent) bool { return x.Done() && x.State != torrent.Checking })
	if tor.Progress != 1 || tor.Left != 0 || tor.Size != int64(len(data)) {
		t.Errorf("progress %.2f, left %d, size %d", tor.Progress, tor.Left, tor.Size)
	}
	if tor.State != torrent.Seeding && tor.State != torrent.SeedingStalled {
		t.Errorf("state = %q, want seeding", tor.State)
	}
	// (No completion time is asserted: Transmission 4.0 leaves it empty for a
	// torrent that was already complete when added; Reelkeeper then uses the
	// time it noticed.)
	if tor.ETA != 0 || tor.Ratio != 0 {
		t.Errorf("eta %v, ratio %v", tor.ETA, tor.Ratio)
	}
	if tor.ContentPath != dir+"/"+name {
		t.Errorf("content path = %s", tor.ContentPath)
	}
	// Stopped after it finished: seeding paused, not paused.
	if err := tr.Stop(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	waitForTorrent(t, tr, cat, hash, "stopped", 10*time.Second, func(x Torrent) bool { return x.State == torrent.SeedingPaused })
	if err := tr.NoShareLimits(ctx, cat, []string{hash}); err != nil { // also starts it again
		t.Fatal(err)
	}
	waitForTorrent(t, tr, cat, hash, "started", 10*time.Second, func(x Torrent) bool {
		return x.State == torrent.Seeding || x.State == torrent.SeedingStalled
	})

	// Remove with data: the file is gone from the disk too.
	if err := tr.Remove(ctx, cat, []string{hash}, true); err != nil {
		t.Fatal(err)
	}
	if left := dockerExec(t, container, nil, "ls '"+dir+"' 2>&1 | wc -l"); left != "0" {
		t.Errorf("the downloaded file is still on disk: %s entries", left)
	}
}

// A real transfer between the dev stack's two clients, with no outside
// network: qBittorrent seeds a made-up torrent and is told where Transmission
// is on the compose network; Transmission downloads it from there. Needs both
// REELKEEPER_TEST_TRANSMISSION_URL and REELKEEPER_TEST_QBIT_URL.
func TestRealTransmissionDownloadsFromQbittorrent(t *testing.T) {
	tr := realTransmission(t)
	container := transmissionContainer(t)
	qurl := os.Getenv("REELKEEPER_TEST_QBIT_URL")
	if qurl == "" {
		t.Skip("set REELKEEPER_TEST_QBIT_URL, _USER and _PASS too, to download from the dev qBittorrent")
	}
	ctx := context.Background()
	q := &QBittorrent{URL: qurl, Username: os.Getenv("REELKEEPER_TEST_QBIT_USER"), Password: os.Getenv("REELKEEPER_TEST_QBIT_PASS")}
	if _, err := q.Version(ctx); err != nil {
		t.Fatal(err)
	}
	const cat, name = "reelkeeper-test-transfer", "Transfer.Test.2024.mkv"
	const seedDir, leechDir = "/mnt/downloads/transfer-seed", "/mnt/downloads/transfer-leech"
	// 3 MB: enough for several pieces and a visible progress.
	data := someData("transfer test", 3_000_000)
	file := testTorrentFile(t, name, data)
	hash, _ := InfoHash(file)
	putFile(t, container, seedDir, name, data)
	// Transmission's own folder, made ahead as it would be on a real server
	// (the volume's top folder belongs to root, so Transmission can't).
	dockerExec(t, container, nil, "mkdir -p '"+leechDir+"' && chown 1000:1000 '"+leechDir+"'")
	t.Cleanup(func() {
		_ = q.Remove(ctx, cat, []string{hash}, false)
		_ = tr.Remove(ctx, cat, []string{hash}, true)
		dockerExec(t, container, nil, "rm -rf '"+seedDir+"' '"+leechDir+"'")
	})

	if err := q.EnsureCategory(ctx, cat, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Add(ctx, Add{Torrent: file, Category: cat, SavePath: seedDir}); err != nil {
		t.Fatal(err)
	}
	// qBittorrent's own share limits would stop its seeding part way (the
	// dev one's do at ratio 1), as they would on a real server; Reelkeeper
	// lifts them on its own torrents the same way.
	if err := q.NoShareLimits(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	// qBittorrent checks the file it was pointed at and starts seeding.
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(300 * time.Millisecond) {
		ts, err := q.Torrents(ctx, cat)
		if err != nil {
			t.Fatal(err)
		}
		if len(ts) == 1 && (ts[0].State == torrent.Seeding || ts[0].State == torrent.SeedingStalled) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("qBittorrent didn't start seeding: %+v", ts)
		}
	}

	if _, err := tr.Add(ctx, Add{Torrent: file, Category: cat, Tags: []string{"reelkeeper"}, SavePath: leechDir}); err != nil {
		t.Fatal(err)
	}
	// Tell qBittorrent where Transmission is (its address on the compose
	// network, and Transmission's peer port).
	ip := strings.Fields(dockerExec(t, container, nil, "hostname -i"))[0]
	peer := ip + ":51413"
	var tor Torrent
	for i, deadline := 0, time.Now().Add(120*time.Second); ; i++ {
		time.Sleep(time.Second)
		// Offered again every 10 seconds, in case a try came before Transmission was ready.
		if i%10 == 0 {
			b, status, err := q.do(ctx, "POST", "/api/v2/torrents/addPeers", form(url.Values{"hashes": {hash}, "peers": {peer}}))
			if err != nil || status != 200 {
				t.Fatalf("addPeers: %v %d %s", err, status, b)
			}
		}
		if x := findTorrent(t, tr, cat, hash); x != nil {
			tor = *x
			if tor.Done() && tor.State != torrent.Checking {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Transmission didn't finish downloading from qBittorrent at %s: %+v", peer, tor)
		}
	}
	t.Logf("downloaded %d bytes from qBittorrent at %s; state %s", tor.Size, peer, tor.State)
	if tor.State != torrent.Seeding && tor.State != torrent.SeedingStalled {
		t.Errorf("state after the download = %q", tor.State)
	}
	if tor.CompletedOn == nil || tor.Left != 0 {
		t.Errorf("completed %v, left %d", tor.CompletedOn, tor.Left)
	}
	// The same bytes arrived.
	got := dockerExec(t, container, nil, "sha256sum '"+leechDir+"/"+name+"' | cut -d' ' -f1")
	sum := sha256.Sum256(data)
	if got != hex.EncodeToString(sum[:]) {
		t.Errorf("downloaded file's checksum = %s", got)
	}
}
