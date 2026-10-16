package download

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/torrent"
)

// The tests below run against a real Deluge when REELKEEPER_TEST_DELUGE_URL
// and REELKEEPER_TEST_DELUGE_PASS are set, such as the dev stack's
// (http://localhost:8112, password reelkeeper). Nothing here touches the internet
// or anything but its own torrents: they are made up, private (no DHT) and
// have no tracker, and Deluge's peer discovery (DHT, peer exchange, local
// discovery, UPnP, NAT-PMP) and release check are switched off through its own
// settings and left off: the dev clients never go on the internet. Deluge is
// left empty.
//
// The ones that need files on Deluge's disk put them there with `docker exec`
// into the dev stack's container, named by REELKEEPER_TEST_DELUGE_CONTAINER
// (reelkeeper-dev-deluge by default), and skip when docker isn't there.

func realDeluge(t *testing.T) *Deluge {
	t.Helper()
	u := os.Getenv("REELKEEPER_TEST_DELUGE_URL")
	if u == "" {
		t.Skip("set REELKEEPER_TEST_DELUGE_URL and _PASS to test against a real Deluge")
	}
	d := &Deluge{URL: u, Password: os.Getenv("REELKEEPER_TEST_DELUGE_PASS")}
	ctx := context.Background()
	if _, err := d.Version(ctx); err != nil {
		t.Fatal(err)
	}
	isolateDeluge(t, d)
	return d
}

func delugeContainer(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker isn't installed; this test puts files in the dev stack's containers")
	}
	if c := os.Getenv("REELKEEPER_TEST_DELUGE_CONTAINER"); c != "" {
		return c
	}
	return "reelkeeper-dev-deluge"
}

// isolateDeluge switches off everything in Deluge that looks for peers or
// phones home, and checks that it took. It leaves them off.
func isolateDeluge(t *testing.T, d *Deluge) {
	t.Helper()
	ctx := context.Background()
	off := map[string]any{"dht": false, "utpex": false, "lsd": false, "upnp": false, "natpmp": false, "new_release_check": false, "send_info": false}
	if err := d.call(ctx, "turn off peer discovery", "core.set_config", nil, off); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := d.call(ctx, "read its settings", "core.get_config", &cfg); err != nil {
		t.Fatal(err)
	}
	for k := range off {
		if cfg[k] != false {
			t.Fatalf("Deluge's %s is %v, not off", k, cfg[k])
		}
	}
}

// delugeStatus reads fields of one torrent straight from Deluge, whatever its label.
func delugeStatus(t *testing.T, d *Deluge, hash string, fields ...string) map[string]any {
	t.Helper()
	var out map[string]map[string]any
	if err := d.call(context.Background(), "read", "core.get_torrents_status", &out, map[string]any{"id": []string{hash}}, fields); err != nil {
		t.Fatal(err)
	}
	return out[hash]
}

func findDeluge(t *testing.T, d *Deluge, cat, hash string) *Torrent {
	t.Helper()
	ts, err := d.Torrents(context.Background(), cat)
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

// waitForDeluge polls until the torrent satisfies ok, and returns it.
func waitForDeluge(t *testing.T, d *Deluge, cat, hash, what string, timeout time.Duration, ok func(Torrent) bool) Torrent {
	t.Helper()
	var last *Torrent
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if last = findDeluge(t, d, cat, hash); last != nil && ok(*last) {
			return *last
		}
	}
	if last == nil {
		t.Fatalf("%s: torrent not listed", what)
	}
	t.Fatalf("%s: state %q, progress %.2f, verified %d", what, last.State, last.Progress, last.Verified)
	return Torrent{}
}

// removeLabels takes labels the test made out of Deluge's Label plugin.
func removeLabels(d *Deluge, labels ...string) {
	for _, l := range labels {
		_ = d.call(context.Background(), "remove a label", "label.remove", nil, l)
	}
}

// removeRaw removes a torrent whatever its label, with its data: only for
// torrents the test itself made.
func removeRaw(d *Deluge, hash string) {
	_ = d.call(context.Background(), "remove", "core.remove_torrent", nil, hash, true)
}

// A torrent's whole life in a real Deluge without any peers: added stopped and
// labelled, kept apart from another app's torrent, listed with the right
// fields, started and stopped, queued, relabelled and removed. Deluge's own
// settings (a global "remove at ratio" and "move completed") don't reach it.
func TestRealDeluge(t *testing.T) {
	d := realDeluge(t)
	ctx := context.Background()
	v, err := d.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Deluge %s", v)

	const cat, cat2 = "reelkeeper-test", "reelkeeper-test-moved"
	data := someData("reelkeeper-deluge-test", 40000)
	file := testTorrentFile(t, "reelkeeper-test.bin", data)
	hash, _ := InfoHash(file)
	otherFile := testTorrentFile(t, "someone-elses.bin", someData("someone else", 20000))
	otherHash, _ := InfoHash(otherFile)

	// Deluge's global settings, as they are now, to put back.
	var before map[string]any
	if err := d.call(ctx, "read", "core.get_config", &before); err != nil {
		t.Fatal(err)
	}
	restore := map[string]any{}
	for _, k := range []string{"stop_seed_at_ratio", "stop_seed_ratio", "remove_seed_at_ratio", "move_completed", "move_completed_path"} {
		restore[k] = before[k]
	}
	t.Cleanup(func() {
		_ = d.call(ctx, "put the settings back", "core.set_config", nil, restore)
		_ = d.Remove(ctx, cat, []string{hash}, true)
		_ = d.Remove(ctx, cat2, []string{hash}, true)
		removeRaw(d, hash)
		removeRaw(d, otherHash)
		removeLabels(d, cat, cat2)
	})
	if err := d.EnsureCategory(ctx, cat, ""); err != nil {
		t.Fatal(err)
	}

	// Another app's torrent in the same Deluge, added without Reelkeeper's label.
	var added string
	if err := d.call(ctx, "add", "core.add_torrent_file", &added, "other.torrent", base64.StdEncoding.EncodeToString(otherFile),
		map[string]any{"add_paused": true, "download_location": "/mnt/downloads/reelkeeper-test"}); err != nil || added != otherHash {
		t.Fatalf("adding another app's torrent: %q %v", added, err)
	}

	// The user's Deluge removes torrents at their ratio and moves finished ones.
	hostile := map[string]any{"stop_seed_at_ratio": true, "stop_seed_ratio": 0.01, "remove_seed_at_ratio": true, "move_completed": true, "move_completed_path": "/mnt/downloads/reelkeeper-test-elsewhere"}
	if err := d.call(ctx, "change the settings", "core.set_config", nil, hostile); err != nil {
		t.Fatal(err)
	}
	// Added stopped, in a folder of its own, with the category.
	got, err := d.Add(ctx, Add{Torrent: file, Category: cat, Tags: []string{"reelkeeper", "test-indexer"}, Stopped: true, SavePath: "/mnt/downloads/reelkeeper-test"})
	if err != nil || got != hash {
		t.Fatalf("Add = %q, %v (want %s)", got, err, hash)
	}
	if err := d.call(ctx, "put the settings back", "core.set_config", nil, restore); err != nil {
		t.Fatal(err)
	}
	st := delugeStatus(t, d, hash, "label", "stop_at_ratio", "remove_at_ratio", "move_completed", "paused")
	if st["label"] != cat || st["stop_at_ratio"] != false || st["remove_at_ratio"] != false || st["move_completed"] != false || st["paused"] != true {
		t.Errorf("the torrent after Add (the user's global settings are hostile): %v", st)
	}
	// Adding it again is fine (it is Reelkeeper's); another category's is not.
	if again, err := d.Add(ctx, Add{Torrent: file, Category: cat, Stopped: true}); err != nil || again != hash {
		t.Errorf("Add again = %q, %v", again, err)
	}
	if _, err := d.Add(ctx, Add{Torrent: file, Category: cat2, Stopped: true}); err == nil {
		t.Error("Add into another category took over a torrent")
	}
	if _, err := d.Add(ctx, Add{Torrent: otherFile, Category: cat, Stopped: true}); err == nil {
		t.Error("Add took over another app's torrent")
	}

	// Listed with the right category, folder, size and a state Reelkeeper knows.
	tor := waitForDeluge(t, d, cat, hash, "added stopped", 10*time.Second, func(x Torrent) bool { return x.State == torrent.Paused })
	if tor.Category != cat || tor.Name != "reelkeeper-test.bin" || tor.Size != int64(len(data)) || tor.SavePath != "/mnt/downloads/reelkeeper-test" ||
		tor.ContentPath != "/mnt/downloads/reelkeeper-test/reelkeeper-test.bin" || tor.Left != int64(len(data)) || tor.Progress != 0 || tor.Verified != 0 {
		t.Errorf("torrent = %+v", tor)
	}
	if !tor.Paused() || tor.Done() || len(tor.Tags) != 0 {
		t.Errorf("added stopped, but %+v", tor)
	}
	if ts, _ := d.Torrents(ctx, cat); len(ts) != 1 {
		t.Errorf("category lists %d torrents, want only ours: %+v", len(ts), ts)
	}
	if findDeluge(t, d, cat, otherHash) != nil {
		t.Error("another app's torrent is listed in Reelkeeper's category")
	}

	// Start: not paused any more (nobody has the data, so it downloads, or
	// stalls). Stop: paused again.
	if err := d.Start(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	tor = waitForDeluge(t, d, cat, hash, "after Start", 10*time.Second, func(x Torrent) bool { return x.State != torrent.Paused })
	if tor.State == torrent.Unknown {
		t.Errorf("Deluge reported a state Reelkeeper doesn't recognise")
	}
	if err := d.TopPriority(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	if err := d.NoShareLimits(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	if msgs, err := d.TrackerMessages(ctx, cat, hash); err != nil {
		t.Fatal(err)
	} else {
		t.Logf("tracker messages: %q", msgs)
	}
	if err := d.Stop(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	waitForDeluge(t, d, cat, hash, "after Stop", 10*time.Second, func(x Torrent) bool { return x.State == torrent.Paused })

	// A private torrent can't be handed over; the folder is where it was added.
	if _, err := d.Source(ctx, cat, hash); err == nil || !strings.Contains(err.Error(), "private torrent") {
		t.Errorf("Source of a private torrent: %v", err)
	}
	lay, err := d.Layout(ctx, cat, hash)
	if err != nil || lay.Folder != "/mnt/downloads/reelkeeper-test" || len(lay.Files) != 1 || lay.Files[0] != (File{"reelkeeper-test.bin", int64(len(data))}) {
		t.Errorf("layout = %+v, %v", lay, err)
	}

	// The guard, against the real thing: Reelkeeper's calls refuse the other
	// app's torrent, which stays as it was.
	for name, call := range map[string]func() error{
		"Start":  func() error { return d.Start(ctx, cat, []string{otherHash}) },
		"Remove": func() error { return d.Remove(ctx, cat, []string{otherHash}, true) },
		"Source": func() error { _, err := d.Source(ctx, cat, otherHash); return err },
		"Layout": func() error { _, err := d.Layout(ctx, cat, otherHash); return err },
	} {
		if err := call(); err == nil {
			t.Errorf("%s touched another app's torrent", name)
		}
	}
	if st := delugeStatus(t, d, otherHash, "label", "paused", "stop_at_ratio"); st["label"] != "" || st["paused"] != true {
		t.Errorf("the other app's torrent changed: %v", st)
	}

	// MoveCategory: the label changes.
	if err := d.MoveCategory(ctx, cat, cat2, []string{hash}); err != nil {
		t.Fatal(err)
	}
	if findDeluge(t, d, cat, hash) != nil || findDeluge(t, d, cat2, hash) == nil {
		t.Error("the torrent didn't change category")
	}
	if st := delugeStatus(t, d, hash, "remove_at_ratio", "move_completed"); st["remove_at_ratio"] != false || st["move_completed"] != false {
		t.Errorf("options after relabelling: %v", st)
	}

	// Remove with its data.
	if err := d.Remove(ctx, cat2, []string{hash}, true); err != nil {
		t.Fatal(err)
	}
	if findDeluge(t, d, cat2, hash) != nil {
		t.Error("still there after Remove")
	}
}

// A finished download, without any network: the file is already in the
// folder, so Deluge's check finds it complete and goes straight to seeding.
// Then what a move uses: the verified bytes after a check, and moving the
// files to another folder (a finished torrent and a half-done one).
func TestRealDelugeCompleted(t *testing.T) {
	d := realDeluge(t)
	container := delugeContainer(t)
	ctx := context.Background()
	const cat, dir, dir2, name = "reelkeeper-test-done", "/mnt/downloads/reelkeeper-test-done", "/mnt/downloads/reelkeeper-test-done-moved", "Finished.Film.2024.mkv"
	data := someData("finished film", 300000)
	file := testTorrentFile(t, name, data)
	hash, _ := InfoHash(file)
	putFile(t, container, dir, name, data)
	// The half-done one: the whole file's length, with only its first nine
	// pieces (147456 bytes) real, as a client leaves a file it has not finished.
	const half, realBytes = "Half.Done.2024.mkv", 9 * 16384
	partial := append(someData("half done film", realBytes), make([]byte, 300000-realBytes)...)
	halfFile := testTorrentFile(t, half, someData("half done film", 300000))
	halfHash, _ := InfoHash(halfFile)
	putFile(t, container, dir, half, partial)
	t.Cleanup(func() {
		_ = d.Remove(ctx, cat, []string{hash, halfHash}, true)
		removeRaw(d, hash)
		removeRaw(d, halfHash)
		removeLabels(d, cat)
		dockerExec(t, container, nil, "rm -rf '"+dir+"' '"+dir2+"'")
	})

	if err := d.EnsureCategory(ctx, cat, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Add(ctx, Add{Torrent: file, Category: cat, Tags: []string{"reelkeeper"}, SavePath: dir}); err != nil {
		t.Fatal(err)
	}
	tor := waitForDeluge(t, d, cat, hash, "finished", 30*time.Second, func(x Torrent) bool { return x.Done() && x.State != torrent.Checking })
	if tor.Progress != 1 || tor.Left != 0 || tor.Size != int64(len(data)) || tor.Verified != int64(len(data)) {
		t.Errorf("progress %.2f, left %d, size %d, verified %d", tor.Progress, tor.Left, tor.Size, tor.Verified)
	}
	if tor.State != torrent.Seeding {
		t.Errorf("state = %q, want seeding", tor.State)
	}
	if tor.ETA != 0 || tor.ContentPath != dir+"/"+name {
		t.Errorf("eta %v, content path %s", tor.ETA, tor.ContentPath)
	}

	// Stopped after it finished: seeding paused, not paused. NoShareLimits
	// starts it again.
	if err := d.Stop(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	tor = waitForDeluge(t, d, cat, hash, "stopped", 10*time.Second, func(x Torrent) bool { return x.State == torrent.SeedingPaused })
	if !tor.Stopped() || !tor.Done() {
		t.Errorf("stopped = %+v", tor)
	}

	// A check of a stopped torrent: Checking until it is over, then paused
	// again with everything verified.
	if err := d.Recheck(ctx, cat, hash); err != nil {
		t.Fatal(err)
	}
	if x := findDeluge(t, d, cat, hash); x == nil || x.State != torrent.Checking {
		t.Errorf("right after Recheck: %+v", x)
	}
	tor = waitForDeluge(t, d, cat, hash, "checked", 30*time.Second, func(x Torrent) bool { return x.State != torrent.Checking })
	if tor.State != torrent.SeedingPaused || tor.Verified != int64(len(data)) || tor.Left != 0 {
		t.Errorf("after the check: %+v", tor)
	}
	if st := delugeStatus(t, d, hash, "paused", "state"); st["paused"] != true || st["state"] != "Paused" {
		t.Errorf("Deluge after the check: %v", st)
	}
	// Nothing was renamed or moved by the check.
	if got := dockerExec(t, container, nil, "ls '"+dir+"'"); !strings.Contains(got, name) {
		t.Errorf("files after the check: %s", got)
	}

	// Moving the files to another folder, finished: Deluge says Moving, then
	// is paused again, with the files in the new folder and nothing left behind.
	if err := d.SetFolder(ctx, cat, hash, dir2); err != nil {
		t.Fatal(err)
	}
	if lay, err := d.Layout(ctx, cat, hash); err != nil || lay.Folder != dir2 {
		t.Errorf("layout after the move: %+v %v", lay, err)
	}
	if got := dockerExec(t, container, nil, "ls '"+dir2+"'"); got != name {
		t.Errorf("new folder holds: %q", got)
	}
	if got := dockerExec(t, container, nil, "ls '"+dir+"' | grep -c '"+name+"' || true"); got != "0" {
		t.Errorf("the old folder still has the file")
	}
	if sum := dockerExec(t, container, nil, "sha256sum '"+dir2+"/"+name+"' | cut -d' ' -f1"); sum != shaOf(data) {
		t.Errorf("checksum after the move = %s", sum)
	}
	if err := d.Recheck(ctx, cat, hash); err != nil {
		t.Fatal(err)
	}
	tor = waitForDeluge(t, d, cat, hash, "checked in the new folder", 30*time.Second, func(x Torrent) bool { return x.State != torrent.Checking })
	if tor.Verified != int64(len(data)) {
		t.Errorf("after the move and a check: %+v", tor)
	}
	if err := d.NoShareLimits(ctx, cat, []string{hash}); err != nil { // also starts it again
		t.Fatal(err)
	}
	waitForDeluge(t, d, cat, hash, "started", 10*time.Second, func(x Torrent) bool { return x.State == torrent.Seeding })

	// A half-done torrent, added stopped where its (full-length) file is: the
	// check finds the real half and nothing more; the move to another folder
	// carries the partial file, and a second check finds the same.
	if _, err := d.Add(ctx, Add{Torrent: halfFile, Category: cat, Stopped: true, SavePath: dir2, FilesIn: dir}); err != nil {
		t.Fatal(err)
	}
	tor = waitForDeluge(t, d, cat, halfHash, "half added", 10*time.Second, func(x Torrent) bool { return x.State == torrent.Paused })
	if tor.Verified != 0 || tor.Progress != 0 {
		t.Errorf("a paused add shows %d bytes, %.2f (never a false number)", tor.Verified, tor.Progress)
	}
	if err := d.Recheck(ctx, cat, halfHash); err != nil {
		t.Fatal(err)
	}
	checked := waitForDeluge(t, d, cat, halfHash, "half checked", 30*time.Second, func(x Torrent) bool { return x.State != torrent.Checking })
	if checked.State != torrent.Paused || checked.Verified != realBytes || checked.Done() {
		t.Errorf("the half-done torrent after the check: %+v (want %d of 300000 bytes)", checked, realBytes)
	}
	if got := dockerExec(t, container, nil, "ls '"+dir+"'"); !strings.Contains(got, half) {
		t.Errorf("the check moved or renamed the partial file: %s", got)
	}
	if err := d.SetFolder(ctx, cat, halfHash, dir2); err != nil {
		t.Fatal(err)
	}
	if got := dockerExec(t, container, nil, "ls '"+dir2+"'"); !strings.Contains(got, half) {
		t.Errorf("the partial file didn't move: %s", got)
	}
	if got := dockerExec(t, container, nil, "ls '"+dir+"' | grep -c '"+half+"' || true"); got != "0" {
		t.Errorf("the partial file was left behind")
	}
	if err := d.Recheck(ctx, cat, halfHash); err != nil {
		t.Fatal(err)
	}
	moved := waitForDeluge(t, d, cat, halfHash, "half checked after the move", 30*time.Second, func(x Torrent) bool { return x.State != torrent.Checking })
	if moved.Verified != checked.Verified || moved.State != torrent.Paused {
		t.Errorf("after the move: %+v, before: %+v", moved, checked)
	}

	// Remove with data: the files are gone from the disk too.
	if err := d.Remove(ctx, cat, []string{hash, halfHash}, true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, dir2} {
		if left := dockerExec(t, container, nil, "ls '"+p+"' 2>/dev/null | wc -l"); left != "0" {
			t.Errorf("%s still has %s entries after Remove with data", p, left)
		}
	}
}

// A real transfer between the dev stack's two clients, with no outside
// network: qBittorrent seeds a made-up torrent, Deluge is told where
// qBittorrent is on the compose network (retrying while qBittorrent is still
// checking), and downloads it from there. Needs REELKEEPER_TEST_QBIT_URL too.
func TestRealDelugeDownloadsFromQbittorrent(t *testing.T) {
	d := realDeluge(t)
	container := delugeContainer(t)
	qurl := os.Getenv("REELKEEPER_TEST_QBIT_URL")
	if qurl == "" {
		t.Skip("set REELKEEPER_TEST_QBIT_URL, _USER and _PASS too, to download from the dev qBittorrent")
	}
	ctx := context.Background()
	q := &QBittorrent{URL: qurl, Username: os.Getenv("REELKEEPER_TEST_QBIT_USER"), Password: os.Getenv("REELKEEPER_TEST_QBIT_PASS")}
	if _, err := q.Version(ctx); err != nil {
		t.Fatal(err)
	}
	qc := "reelkeeper-dev-qbittorrent"
	if c := os.Getenv("REELKEEPER_TEST_QBIT_CONTAINER"); c != "" {
		qc = c
	}
	qIP := strings.Fields(dockerExec(t, qc, nil, "hostname -i"))[0]
	// qBittorrent's peer port, and its own peer discovery off (it stays off).
	b, status, err := q.do(ctx, "GET", "/api/v2/app/preferences", nil)
	if err != nil || status != 200 {
		t.Fatalf("qBittorrent preferences: %v %d", err, status)
	}
	var prefs struct {
		ListenPort int  `json:"listen_port"`
		DHT        bool `json:"dht"`
		PeX        bool `json:"pex"`
		LSD        bool `json:"lsd"`
	}
	if err := json.Unmarshal(b, &prefs); err != nil {
		t.Fatal(err)
	}
	if prefs.DHT || prefs.PeX || prefs.LSD {
		t.Fatalf("the dev qBittorrent has peer discovery on: %+v (the other real tests turn it off and leave it off)", prefs)
	}

	const cat, name = "reelkeeper-test-deluge-transfer", "Transfer.Test.2024.mkv"
	const seedDir, leechDir = "/mnt/downloads/deluge-transfer-seed", "/mnt/downloads/deluge-transfer-leech"
	// 3 MB: enough for several pieces and a visible progress.
	data := someData("deluge transfer test", 3_000_000)
	file := testTorrentFile(t, name, data)
	hash, _ := InfoHash(file)
	putFile(t, container, seedDir, name, data)
	// Deluge's own folder, made ahead as it would be on a real server (the
	// volume's top folder belongs to root, so Deluge can't).
	dockerExec(t, container, nil, "mkdir -p '"+leechDir+"' && chown 1000:1000 '"+leechDir+"'")
	t.Cleanup(func() {
		_ = q.Remove(ctx, cat, []string{hash}, false)
		_ = d.Remove(ctx, cat, []string{hash}, true)
		removeRaw(d, hash)
		removeLabels(d, cat)
		dockerExec(t, container, nil, "rm -rf '"+seedDir+"' '"+leechDir+"'")
	})

	if err := q.EnsureCategory(ctx, cat, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Add(ctx, Add{Torrent: file, Category: cat, SavePath: seedDir}); err != nil {
		t.Fatal(err)
	}
	// qBittorrent's own share limits would stop its seeding part way (the dev
	// one's do at ratio 1), as they would on a real server.
	if err := q.NoShareLimits(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
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

	if err := d.EnsureCategory(ctx, cat, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Add(ctx, Add{Torrent: file, Category: cat, Tags: []string{"reelkeeper"}, SavePath: leechDir}); err != nil {
		t.Fatal(err)
	}
	// Slow enough (about 600 KiB/s) to be seen part way.
	if err := d.call(ctx, "slow it down", "core.set_torrent_options", nil, []string{hash}, map[string]any{"max_download_speed": 600.0}); err != nil {
		t.Fatal(err)
	}
	if err := d.TopPriority(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	connect := func() {
		if err := d.call(ctx, "connect to qBittorrent", "core.connect_peer", nil, hash, qIP, prefs.ListenPort); err != nil {
			t.Fatal(err)
		}
	}
	// Part way: some of it, not all, with the speed and the seeds showing.
	var tor Torrent
	for i, deadline := 0, time.Now().Add(120*time.Second); ; i++ {
		time.Sleep(500 * time.Millisecond)
		// Offered again every few seconds: a try while qBittorrent is still
		// checking, or before Deluge is ready, gets no answer.
		if i%6 == 0 {
			connect()
		}
		if x := findDeluge(t, d, cat, hash); x != nil {
			tor = *x
			if tor.Progress > 0.15 && tor.Progress < 0.95 {
				break
			}
			if tor.Done() {
				t.Fatalf("it finished before it could be paused part way: %+v", tor)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Deluge didn't start downloading from qBittorrent at %s:%d: %+v", qIP, prefs.ListenPort, tor)
		}
	}
	// (Deluge's rate is bursty, so a moment with no data counts as stalled.)
	if (tor.State != torrent.Downloading && tor.State != torrent.Stalled) || tor.Done() || tor.Left <= 0 || tor.Left >= tor.Size || tor.Verified <= 0 {
		t.Errorf("part way: %+v", tor)
	}
	t.Logf("part way: state %s, %.0f%%, speed %d B/s, seeds %d, peers %d", tor.State, tor.Progress*100, tor.Speed, tor.Seeds, tor.Peers)

	// Pause: stops, and keeps what it has. Resume: carries on.
	if err := d.Stop(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	paused := waitForDeluge(t, d, cat, hash, "paused", 10*time.Second, func(x Torrent) bool { return x.State == torrent.Paused })
	time.Sleep(1500 * time.Millisecond)
	later := findDeluge(t, d, cat, hash)
	if later == nil || later.State != torrent.Paused || later.Verified != paused.Verified || later.Done() {
		t.Errorf("while paused: %+v, then %+v", paused, later)
	}
	if err := d.Start(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	waitForDeluge(t, d, cat, hash, "resumed", 10*time.Second, func(x Torrent) bool { return x.State != torrent.Paused })
	// (The peer connection was dropped by the pause.)
	for i, deadline := 0, time.Now().Add(120*time.Second); ; i++ {
		time.Sleep(500 * time.Millisecond)
		if i%6 == 0 {
			connect()
		}
		if x := findDeluge(t, d, cat, hash); x != nil {
			tor = *x
			if tor.Done() && tor.State != torrent.Checking {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Deluge didn't finish downloading from qBittorrent at %s:%d: %+v", qIP, prefs.ListenPort, tor)
		}
	}
	t.Logf("downloaded %d bytes from qBittorrent at %s:%d; state %s", tor.Size, qIP, prefs.ListenPort, tor.State)
	if tor.State != torrent.Seeding {
		t.Errorf("state after the download = %q, want seeding", tor.State)
	}
	if tor.CompletedOn == nil || tor.Left != 0 || tor.Progress != 1 || tor.Verified != tor.Size {
		t.Errorf("completed %v, left %d, progress %v, verified %d of %d", tor.CompletedOn, tor.Left, tor.Progress, tor.Verified, tor.Size)
	}
	// The same bytes arrived, at the plain name in the folder given, with
	// nothing renamed or moved.
	got := dockerExec(t, container, nil, "sha256sum '"+leechDir+"/"+name+"' | cut -d' ' -f1")
	sum := sha256.Sum256(data)
	if got != hex.EncodeToString(sum[:]) {
		t.Errorf("downloaded file's checksum = %s", got)
	}
	if files := dockerExec(t, container, nil, "ls -A '"+leechDir+"'"); files != name {
		t.Errorf("the folder holds %q", files)
	}
	// Deluge's global settings did not touch it, even at a ratio far over any limit.
	if st := delugeStatus(t, d, hash, "state", "stop_at_ratio", "remove_at_ratio", "move_completed"); st["state"] != "Seeding" || st["remove_at_ratio"] != false {
		t.Errorf("after finishing: %v", fmt.Sprint(st))
	}

	// Remove with data: gone from the disk too.
	if err := d.Remove(ctx, cat, []string{hash}, true); err != nil {
		t.Fatal(err)
	}
	if findDeluge(t, d, cat, hash) != nil {
		t.Error("still there after Remove")
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(300 * time.Millisecond) {
		if left := dockerExec(t, container, nil, "ls -A '"+leechDir+"' | wc -l"); left == "0" {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("the downloaded file is still on disk: %s entries", left)
		}
	}
}
