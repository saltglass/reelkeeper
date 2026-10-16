package download

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/torrent"
)

// TestRealQbittorrent runs against a real qBittorrent when
// REELKEEPER_TEST_QBIT_URL (plus _USER and _PASS) is set, such as the dev
// stack's. It adds a made-up magnet link stopped, so nothing downloads,
// and removes it again.
func TestRealQbittorrent(t *testing.T) {
	u := os.Getenv("REELKEEPER_TEST_QBIT_URL")
	if u == "" {
		t.Skip("set REELKEEPER_TEST_QBIT_URL, _USER and _PASS to test against a real qBittorrent")
	}
	ctx := context.Background()
	q := &QBittorrent{URL: u, Username: os.Getenv("REELKEEPER_TEST_QBIT_USER"), Password: os.Getenv("REELKEEPER_TEST_QBIT_PASS")}
	v, err := q.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("qBittorrent %s", v)
	const cat = "reelkeeper-test"
	if err := q.EnsureCategory(ctx, cat, ""); err != nil {
		t.Fatal(err)
	}
	magnet := "magnet:?xt=urn:btih:" + strings.Repeat("ae", 20) + "&dn=reelkeeper-test"
	hash, err := q.Add(ctx, Add{Magnet: magnet, Category: cat, Tags: []string{"reelkeeper"}, Stopped: true, SavePath: "/downloads/reelkeeper-test"})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for range 20 { // magnets appear a moment after adding
		ts, err := q.Torrents(ctx, cat)
		if err != nil {
			t.Fatal(err)
		}
		for _, tr := range ts {
			found = found || tr.Hash == hash
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatal("added torrent not listed")
	}
	ts, _ := q.Torrents(ctx, cat)
	for _, tr := range ts {
		if tr.Hash == hash && !strings.HasPrefix(tr.SavePath, "/downloads/reelkeeper-test") {
			t.Errorf("save path = %q", tr.SavePath)
		}
	}
	if err := q.NoShareLimits(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.TrackerMessages(ctx, cat, hash); err != nil {
		t.Fatal(err)
	}

	// The torrent was added stopped, so Start must make it not stopped, and
	// Stop must stop it again. TopPriority is accepted either way (with
	// queueing off it does nothing).
	stateOf := func() torrent.State {
		t.Helper()
		ts, err := q.Torrents(ctx, cat)
		if err != nil {
			t.Fatal(err)
		}
		for _, tr := range ts {
			if tr.Hash == hash {
				// A state qBittorrent really reports must be one that
				// qbitState knows.
				if tr.State == torrent.Unknown {
					t.Errorf("qBittorrent reported a state Reelkeeper doesn't recognise")
				}
				return tr.State
			}
		}
		t.Fatal("torrent not listed")
		return ""
	}
	waitFor := func(what string, ok func(state torrent.State) bool) {
		t.Helper()
		var state torrent.State
		for range 40 { // qBittorrent takes a moment to change state
			if state = stateOf(); ok(state) {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatalf("%s: state is %q", what, state)
	}
	isStopped := func(state torrent.State) bool { return state == torrent.Paused }
	if err := q.Start(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	waitFor("after Start", func(state torrent.State) bool { return !isStopped(state) })
	if err := q.TopPriority(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	if err := q.Stop(ctx, cat, []string{hash}); err != nil {
		t.Fatal(err)
	}
	waitFor("after Stop", isStopped)
	if tr := (Torrent{State: stateOf()}); !tr.Paused() {
		t.Errorf("Paused() is false for state %q", tr.State)
	}
	if err := q.Remove(ctx, cat, []string{hash}, true); err != nil {
		t.Fatal(err)
	}
}
