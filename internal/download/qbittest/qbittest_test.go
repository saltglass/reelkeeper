package qbittest

import (
	"testing"

	"github.com/saltglass/reelkeeper/internal/torrent"
)

// Every name in AllStates is one the client recognises (qBittorrent's own
// "unknown" is the only one that maps to Unknown).
func TestAllStatesAreRecognised(t *testing.T) {
	got := Translate(t, AllStates)
	if len(got) != len(AllStates) {
		t.Fatalf("translated %d of %d names", len(got), len(AllStates))
	}
	for _, name := range AllStates {
		if (got[name] == torrent.Unknown) != (name == "unknown") {
			t.Errorf("%s -> %q", name, got[name])
		}
	}
}
