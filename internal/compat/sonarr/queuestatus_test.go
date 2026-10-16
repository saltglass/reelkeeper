package sonarr

import (
	"testing"

	"github.com/saltglass/reelkeeper/internal/download/qbittest"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// oldQueueStatus is how the status was worked out when this package read
// qBittorrent's own state names: a copy of the old code, kept as the
// yardstick for the new one.
func oldQueueStatus(state string, done bool) string {
	switch {
	case done:
		return "completed"
	case state == "queuedDL":
		return "queued"
	case state == "pausedDL" || state == "stoppedDL":
		return "paused"
	case state == "error" || state == "missingFiles":
		return "warning"
	}
	return "downloading"
}

// For every state qBittorrent can report, finished or not, Sonarr's status is
// what it was before Reelkeeper had its own state names.
func TestQueueStatusKeepsTheOldBehaviour(t *testing.T) {
	states := qbittest.Translate(t, qbittest.AllStates)
	for _, name := range qbittest.AllStates {
		for _, done := range []bool{false, true} {
			if got, want := queueStatus(states[name], done), oldQueueStatus(name, done); got != want {
				t.Errorf("%s (done %v): status = %q, was %q", name, done, got, want)
			}
		}
	}
	// And the states of every client map as they should.
	for state, want := range map[torrent.State]string{
		torrent.Queued: "queued", torrent.Paused: "paused", torrent.Error: "warning", torrent.MissingFiles: "warning",
		torrent.Downloading: "downloading", torrent.Stalled: "downloading", torrent.Seeding: "downloading", torrent.Unknown: "downloading",
	} {
		if got := queueStatus(state, false); got != want {
			t.Errorf("%s: status = %q, want %q", state, got, want)
		}
	}
}

// The queue's downloadClient is the client in use, not always qBittorrent.
func TestQueueNamesTheDownloadClient(t *testing.T) {
	for _, name := range []string{"qBittorrent", "Transmission"} {
		g := newRig(t)
		g.dl.client = name
		g.addDownloads()
		_, b := g.do(t, "GET", "hd/api/v3/queue", "")
		recs := decode[map[string]any](t, b)["records"].([]any)
		if len(recs) == 0 || recs[0].(map[string]any)["downloadClient"] != name {
			t.Errorf("downloadClient = %v, want %s", recs, name)
		}
	}
}
