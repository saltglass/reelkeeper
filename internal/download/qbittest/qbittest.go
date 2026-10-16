// Package qbittest lets other packages' tests find out what Reelkeeper makes of
// each state name qBittorrent can send, by running the names through the real
// qBittorrent client. It exists for the tests that compare today's behaviour
// with what it was when the code read qBittorrent's names directly.
package qbittest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// AllStates is every state name qBittorrent 4.x and 5.x can report.
var AllStates = []string{
	"error", "missingFiles", "uploading", "pausedUP", "stoppedUP", "queuedUP", "stalledUP", "checkingUP", "forcedUP",
	"allocating", "downloading", "metaDL", "forcedMetaDL", "pausedDL", "stoppedDL", "queuedDL", "stalledDL", "checkingDL",
	"forcedDL", "checkingResumeData", "moving", "unknown",
}

// Translate returns, for each name, the state the qBittorrent client
// reports when qBittorrent sends that name.
func Translate(t *testing.T, names []string) map[string]torrent.State {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var list []map[string]any
		for i, name := range names {
			list = append(list, map[string]any{"hash": fmt.Sprintf("%040d", i), "name": name, "state": name, "category": "test"})
		}
		_ = json.NewEncoder(w).Encode(list)
	}))
	defer srv.Close()
	ts, err := (&download.QBittorrent{URL: srv.URL}).Torrents(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]torrent.State{}
	for _, tr := range ts {
		out[tr.Name] = tr.State
	}
	return out
}
