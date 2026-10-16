package download

import (
	"slices"
	"strings"
	"testing"

	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// Moving to and from Deluge goes through the same steps as between the other
// clients. These tests use a pretend client that behaves the way Deluge does
// where it matters for a move (checked against a real Deluge 2.2):
//
//   - unfinished files have their plain names, full length, in the one folder
//     (no ".part" or ".!qB", no incomplete folder), and a check never renames
//     or moves anything;
//   - it has no tags, so a Seerr request is known from Reelkeeper's own record;
//   - the state lags after a pause or a check (the client's checkLag and
//     stopLag).
//
// The real-client versions of these are in the integration tests.

// qBittorrent to Deluge with a half-done torrent in qBittorrent's temporary
// folder: Reelkeeper renames the ".!qB" file to its plain name, Deluge is
// pointed at the temporary folder, checks it, and is then told to move the
// files to the final folder, where it checks again.
func TestMoveToDelugeHalfDownloadedFromTemporaryFolder(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientDeluge)
		m.nw.checkLag, m.old.stopLag = 2, 1
		h := m.seed(seed{have: 600, onDisk: "unfinishedTemp"})
		name := m.cat[h][0].Name
		m.switchClient(false)
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		add := m.nw.added[0]
		if add.SavePath != finalDir || add.FilesIn != tempDir || !add.Stopped {
			t.Errorf("add = %+v", add)
		}
		wantCalls(t, "new", callsOn(m.nw, h), "add", "recheck", "setfolder", "recheck", "limits")
		// Deluge never renames: the plain name that Reelkeeper gave the file is
		// the one in the final folder.
		if got := m.disk.names(); !slices.Equal(got, []string{finalDir + "/" + name}) {
			t.Errorf("disk = %v", got)
		}
		d := m.download(h)
		if d.Client != settings.ClientDeluge || d.MoveState != "" || d.Status != StatusDownloading || d.MoveOldBytes != 0 {
			t.Errorf("record = %+v", d)
		}
		if ev := m.moveEvents(); len(ev) != 1 || ev[0].Event != grab.EventMoved || !strings.Contains(ev[0].Message, "Moved from qBittorrent to Deluge") {
			t.Errorf("history = %+v", ev)
		}
	})
}

// Transmission to Deluge: ".part" files in the download folder get their plain
// names, which are the only ones Deluge knows. A Seerr request goes to the top
// of Deluge's queue although the Transmission torrent's tags are all that
// the old client knew; here Reelkeeper's own record says so.
func TestMoveToDelugeFromTransmissionRenamesPartFilesAndKeepsRequests(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientTransmission, settings.ClientDeluge)
		h := m.seed(seed{have: 500, onDisk: "part", requested: true, tags: []string{"reelkeeper"}})
		name := m.cat[h][0].Name
		m.switchClient(false)
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, "new", callsOn(m.nw, h), "add", "recheck", "limits", "top")
		if got := m.disk.names(); !slices.Equal(got, []string{finalDir + "/" + name}) {
			t.Errorf("disk = %v", got)
		}
		if tr := m.nw.tors[h]; tr.Verified != 500 {
			t.Errorf("Deluge found %d bytes, want the 500 Transmission had", tr.Verified)
		}
		if d := m.download(h); d.Client != settings.ClientDeluge || !d.Requested {
			t.Errorf("record = %+v", d)
		}
	})
}

// Out of Deluge to either other client: Deluge's partial file has its plain
// name already and nothing is renamed; the new client gets Reelkeeper's tags
// (Deluge has none to read), plus "requested" for a Seerr request, and a
// request goes to the top of the queue.
func TestMoveFromDeluge(t *testing.T) {
	for _, to := range []string{settings.ClientQBittorrent, settings.ClientTransmission} {
		for _, requested := range []bool{true, false} {
			name := settings.ClientDisplayName(to)
			if requested {
				name += " requested"
			}
			t.Run(name, func(t *testing.T) {
				storetest.Each(t, func(t *testing.T, db *store.Store) {
					m := newMover(t, db, settings.ClientDeluge, to)
					m.old.stopLag, m.old.tors = 2, map[string]*simTorrent{}
					h := m.seed(seed{have: 400, onDisk: "plain", requested: requested, grows: 0})
					file := m.cat[h][0].Name
					m.switchClient(false)
					if _, err := m.run(); err != nil {
						t.Fatal(err)
					}
					// Reelkeeper renames nothing (Transmission, as the new client,
					// names the unfinished file ".part" itself while it checks).
					for _, r := range m.disk.renames {
						if !strings.HasSuffix(r[1], ".part") {
							t.Errorf("renamed %v: Deluge's files already have their plain names", r)
						}
					}
					add := m.nw.added[0]
					wantTags := []string{"reelkeeper", "Tracker"}
					if requested {
						wantTags = append(wantTags, "requested")
					}
					if !slices.Equal(add.Tags, wantTags) || add.SavePath != finalDir || add.FilesIn != finalDir || len(add.Torrent) == 0 {
						t.Errorf("add = %+v, want tags %v", add, wantTags)
					}
					got := callsOn(m.nw, h)
					if requested != slices.Contains(got, "top") {
						t.Errorf("new calls = %v (the top of the queue is for requests)", got)
					}
					wantCalls(t, "old", callsOn(m.old, h), "stop", "layout", "remove")
					d := m.download(h)
					if d.Client != to || d.MoveState != "" || d.Requested != requested {
						t.Errorf("record = %+v", d)
					}
					_ = file
				})
			})
		}
	}
}

// A request on Reelkeeper's record still goes to the top when moving between
// the clients that have tags; so does one only the old client's tag knows (a
// download from before Reelkeeper noted it itself).
func TestMoveRequestedFromRecordOrFromTag(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientQBittorrent, settings.ClientTransmission)
		byRecord := m.seed(seed{have: 100, onDisk: "plain", requested: true, tags: []string{"reelkeeper"}})
		byTag := m.seed(seed{have: 100, onDisk: "plain", tags: []string{"reelkeeper", "requested"}})
		neither := m.seed(seed{have: 100, onDisk: "plain", tags: []string{"reelkeeper"}})
		m.switchClient(false)
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		for h, want := range map[string]bool{byRecord: true, byTag: true, neither: false} {
			if got := slices.Contains(callsOn(m.nw, h), "top"); got != want {
				t.Errorf("%s: top = %v, want %v (%v)", h[:4], got, want, callsOn(m.nw, h))
			}
		}
		// The tag is not duplicated when the record and the tag agree.
		if add := m.nw.added; len(add) != 3 {
			t.Fatalf("added %d", len(add))
		}
		for _, a := range m.nw.added {
			n := 0
			for _, tag := range a.Tags {
				if tag == "requested" {
					n++
				}
			}
			if n > 1 {
				t.Errorf("tags = %v", a.Tags)
			}
		}
	})
}

// Deluge says why a torrent is in error; the move says so when it won't touch it.
func TestMoveFromDelugeInErrorSaysWhy(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientDeluge, settings.ClientQBittorrent)
		h := m.seed(seed{have: 300, onDisk: "plain"})
		m.old.tors[h].State = torrent.Error
		m.old.tors[h].Message = "No space left on device"
		m.switchClient(false)
		if _, err := m.run(); err != nil {
			t.Fatal(err)
		}
		d := m.download(h)
		if d.MoveState != "" || !strings.Contains(d.Message, "Deluge reports an error or missing files for it (No space left on device)") {
			t.Errorf("record = %+v", d)
		}
		// Nothing was added anywhere, and nothing removed from Deluge. (A
		// download in the Queue is left stopped when its move is undone.)
		wantCalls(t, "new", callsOn(m.nw, h))
		if calls := callsOn(m.old, h); slices.Contains(calls, "remove") {
			t.Errorf("old calls = %v", calls)
		}
	})
}

// A move into Deluge whose check finds less than the old client had is undone:
// Deluge lets go (without its files), Reelkeeper's renames are put back.
func TestMoveToDelugeCheckFallsShort(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMover(t, db, settings.ClientTransmission, settings.ClientDeluge)
		h := m.seed(seed{status: StatusImported, have: 1000, onDisk: "part"})
		before := m.disk.names()
		m.switchClient(false)
		m.nw.checkLag = 1
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
		if d.Status != StatusSeeded || d.Client != settings.ClientTransmission || !strings.Contains(d.Message, "Couldn't move to Deluge: ") {
			t.Errorf("record = %+v", d)
		}
		if m.nw.tors[h] != nil {
			t.Error("Deluge still has it")
		}
	})
}
