package download

import (
	"context"
	"fmt"
	"os/exec"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// See integration_move_test.go for what these need and promise.

// completeFromSeeder finishes a download that moved part-way: the old client
// (which has let go of the torrent) is given the whole file as a seeder, the
// clients are introduced, and the new client downloads the rest. It proves
// the new client really carried on from the files it was given.
func (c *realCase) completeFromSeeder() Torrent {
	c.t.Helper()
	ctx := context.Background()
	c.old.throttle(c.hash, 0)
	if _, err := c.old.add(Add{Torrent: c.raw, Category: settings.MovieCategory, Tags: []string{"seeder"}, SavePath: c.seedDir}); err != nil {
		c.t.Fatal(err)
	}
	c.old.waitFor(settings.MovieCategory, c.hash, "seeding", 60*time.Second, func(x Torrent) bool { return x.Done() && x.State != torrent.Checking })
	if err := c.old.c.NoShareLimits(ctx, settings.MovieCategory, []string{c.hash}); err != nil {
		c.t.Fatal(err)
	}
	var done Torrent
	for i, deadline := 0, time.Now().Add(90*time.Second); ; i++ {
		if i%6 == 0 {
			c.w.connect(c.hash, c.nw, c.old)
		}
		time.Sleep(500 * time.Millisecond)
		if x := c.nw.find(settings.MovieCategory, c.hash); x != nil && x.Done() && x.State != torrent.Checking {
			done = *x
			break
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("%s never finished the download after the move: %+v", c.nw.name, c.nw.find(settings.MovieCategory, c.hash))
		}
	}
	return done
}

// A finished torrent seeding in one client moves to another, with the
// .torrent Reelkeeper kept at grab time, or (qBittorrent only) the one the old
// client exports. The files stay where they are and are checked in place.
func TestRealMoveFinishedTorrent(t *testing.T) {
	for _, keep := range []bool{true, false} {
		t.Run(fmt.Sprint("kept=", keep), func(t *testing.T) {
			directions(t, func(t *testing.T, w *realWorld, db *store.Store, p pair) {
				c := newRealCase(t, db, w, p, "Finished.Film.2024.mkv", 3_000_000)
				c.seedOld(c.finalDir)
				c.track(StatusImported, keep)
				if !keep && p.old != settings.ClientQBittorrent {
					// Nothing kept, and Transmission and Deluge can't give a private torrent back:
					// it stays where it is, untouched, and History says why.
					if _, err := c.move(false); err != nil {
						t.Fatal(err)
					}
					d := c.download()
					if d.Status != StatusSeeded || !strings.Contains(d.Message, "private torrent") {
						t.Errorf("record = %q %q", d.Status, d.Message)
					}
					if x := c.old.find(settings.MovieCategory, c.hash); x == nil || x.State != torrent.Seeding && x.State != torrent.SeedingStalled {
						t.Errorf("the old torrent was disturbed: %+v", x)
					}
					if c.nw.find(settings.MovieCategory, c.hash) != nil {
						t.Error("the new client has it")
					}
					return
				}
				before := c.old.find(settings.MovieCategory, c.hash)
				if _, err := c.move(false); err != nil {
					t.Fatal(err)
				}
				c.assertMoved(before.Verified)
				// (A client takes a moment to show that it started.)
				x := c.nw.waitFor(settings.MovieCategory, c.hash, "seeding", 20*time.Second, func(x Torrent) bool {
					return x.State == torrent.Seeding || x.State == torrent.SeedingStalled
				})
				if !x.Done() || x.Verified != int64(len(c.data)) || (x.State != torrent.Seeding && x.State != torrent.SeedingStalled) {
					t.Errorf("new torrent = %+v", x)
				}
				if bad := c.differs(c.finalDir); bad != "" {
					t.Errorf("the file changed: %s", bad)
				}
				if got := c.diskHas(c.finalDir); got != c.name {
					t.Errorf("final folder = %q", got)
				}
				// The counters carried over: the old client's seeding time is in the record.
				if d := c.download(); d.SeedSecondsOffset < 0 || d.LimitsSet != true {
					t.Errorf("record = %+v", d)
				}
			})
		})
	}
}

// A torrent the user paused is paused again in the new client.
func TestRealMovePausedTorrent(t *testing.T) {
	directions(t, func(t *testing.T, w *realWorld, db *store.Store, p pair) {
		c := newRealCase(t, db, w, p, "Paused.Film.2024.mkv", 1_000_000)
		c.seedOld(c.finalDir)
		c.track(StatusImported, true)
		if err := c.old.c.Stop(context.Background(), settings.MovieCategory, []string{c.hash}); err != nil {
			t.Fatal(err)
		}
		c.old.waitFor(settings.MovieCategory, c.hash, "paused", 20*time.Second, Torrent.Stopped)
		if _, err := c.move(false); err != nil {
			t.Fatal(err)
		}
		x := c.assertMoved(1_000_000)
		c.nw.waitFor(settings.MovieCategory, c.hash, "paused again", 20*time.Second, func(x Torrent) bool { return x.State == torrent.SeedingPaused })
		if x.Verified != 1_000_000 {
			t.Errorf("verified %d", x.Verified)
		}
	})
}

// variant is one way an old client may keep a half-done download.
type variant struct {
	name string
	// outside: the unfinished files are not in the folder the torrent belongs
	// in, so the new client has to move them there (Transmission's
	// set-location, Deluge's move_storage; qBittorrent does it itself).
	outside bool
	// setup changes the old client's folders; it says where the unfinished
	// file is, and what it is called.
	setup func(c *realCase) (dir, file string)
}

// halfVariants are the ways each client keeps unfinished files: qBittorrent
// with its temporary folder on or off and the .!qB ending on or off;
// Transmission with its incomplete folder on or off and .part names on or off;
// Deluge under plain names where it was told to put them (it has no other).
func halfVariants(oldType string) []variant {
	switch oldType {
	case settings.ClientQBittorrent:
		return []variant{
			{"temp folder off", false, func(c *realCase) (string, string) {
				c.w.changeQ(map[string]any{"temp_path_enabled": false, "incomplete_files_ext": false})
				return c.finalDir, c.name
			}},
			{"temp folder on", true, func(c *realCase) (string, string) {
				c.w.changeQ(map[string]any{"temp_path_enabled": true, "temp_path": c.tempDir, "incomplete_files_ext": false})
				return c.tempDir, c.name
			}},
			{"temp folder on, .!qB names", true, func(c *realCase) (string, string) {
				c.w.changeQ(map[string]any{"temp_path_enabled": true, "temp_path": c.tempDir, "incomplete_files_ext": true})
				return c.tempDir, c.name + ".!qB"
			}},
			{"temp folder off, .!qB names", false, func(c *realCase) (string, string) {
				c.w.changeQ(map[string]any{"temp_path_enabled": false, "incomplete_files_ext": true})
				return c.finalDir, c.name + ".!qB"
			}},
		}
	case settings.ClientTransmission:
		return []variant{
			{"incomplete folder off, .part names", false, func(c *realCase) (string, string) {
				c.w.changeT(map[string]any{"incomplete-dir-enabled": false, "rename-partial-files": true})
				return c.finalDir, c.name + ".part"
			}},
			{"incomplete folder on, .part names", true, func(c *realCase) (string, string) {
				c.w.changeT(map[string]any{"incomplete-dir-enabled": true, "incomplete-dir": c.tempDir, "rename-partial-files": true})
				return c.tempDir, c.name + ".part"
			}},
			{"incomplete folder on, plain names", true, func(c *realCase) (string, string) {
				c.w.changeT(map[string]any{"incomplete-dir-enabled": true, "incomplete-dir": c.tempDir, "rename-partial-files": false})
				return c.tempDir, c.name
			}},
			{"incomplete folder off, plain names", false, func(c *realCase) (string, string) {
				c.w.changeT(map[string]any{"incomplete-dir-enabled": false, "rename-partial-files": false})
				return c.finalDir, c.name
			}},
		}
	}
	return []variant{{"plain names", false, func(c *realCase) (string, string) { return c.finalDir, c.name }}}
}

// variantFor picks the first variant of an old client that keeps its files
// outside the final folder (or, if asked for the other kind, inside it); when
// the client has none of that kind (Deluge keeps nothing outside) it is the
// first variant it has.
func variantFor(oldType string, outside bool) variant {
	vs := halfVariants(oldType)
	for _, v := range vs {
		if v.outside == outside {
			return v
		}
	}
	return vs[0]
}

// filesIn lists the files under a folder (one path a line, empty if none).
func (c *realCase) filesIn(dir string) string {
	c.t.Helper()
	return dockerExec(c.t, c.w.tc, nil, "find '"+dir+"' -type f 2>/dev/null | sort")
}

// A half-downloaded torrent moves with the unfinished files it has, whatever
// the old client calls them and wherever it keeps them. The new client checks
// them, finds what the old one had, and carries on: given a seeder it finishes
// the download and the finished file ends up in the final folder.
func TestRealMoveHalfDownloaded(t *testing.T) {
	for _, p := range allPairs() {
		for _, v := range halfVariants(p.old) {
			t.Run(p.String()+"/"+v.name, func(t *testing.T) {
				w := realClients(t)
				c := newRealCase(t, storetest.Open(t, store.SQLite), w, p, "Half.Film.2024.mkv", 3_000_000)
				dir, file := v.setup(c)
				c.partialOld(0.4)
				c.track(StatusDownloading, true)
				old := c.old.find(settings.MovieCategory, c.hash)
				if old == nil || old.Progress >= 1 || old.Verified == 0 {
					t.Fatalf("the old client is not part-way: %+v", old)
				}
				// The unfinished file is where and as the old client names it.
				// (Transmission keeps pieces in its write cache and writes no
				// file until it is stopped, so for it the move itself, which
				// stops it first, is what brings the file to disk.)
				if p.old != settings.ClientTransmission {
					if got := c.diskHas(dir); !strings.Contains(" "+got+" ", " "+file+" ") {
						t.Fatalf("expected %s in %s, found %q", file, dir, got)
					}
				}
				if _, err := c.move(false); err != nil {
					t.Fatal(err)
				}
				x := c.assertMoved(c.oldBytes)
				if x.Done() {
					t.Errorf("it can't be done: %+v", x)
				}
				t.Logf("old client had %d bytes verified (%.0f%%); new client has %d; state %s", c.oldBytes, old.Progress*100, x.Verified, x.State)
				if c.oldBytes == 0 || x.Verified < c.oldBytes {
					t.Errorf("verified %d of the %d the old client had", x.Verified, c.oldBytes)
				}
				// The new client carries on, and finishes with the file in the final folder.
				done := c.completeFromSeeder()
				if done.Verified != int64(len(c.data)) {
					t.Errorf("finished with %d verified", done.Verified)
				}
				if bad := c.differs(c.finalDir); bad != "" {
					t.Errorf("the finished file is wrong: %s", bad)
				}
				if v.outside {
					if left := c.filesIn(c.tempDir); left != "" {
						t.Errorf("files were left behind in %s: %s", c.tempDir, left)
					}
				}
			})
		}
	}
}

// A torrent of several files, in folders, moves with its unfinished files
// wherever the old client keeps them. Into Deluge (and Transmission) that
// means the files are checked where they are and then moved to the final
// folder while only partly there (core.move_storage, set-location); they
// must all arrive, in their folders, and finish from a seeder.
func TestRealMoveMultiFileTorrent(t *testing.T) {
	const top = "Multi.Film.2024"
	files := []tfile{
		{top + "/" + top + ".mkv", someData("multi video", 1_900_000)},
		{top + "/Subs/en.srt", someData("multi subs", 150_000)},
		{top + "/Subs/fr.srt", someData("multi subs fr", 120_000)},
		{top + "/Extras/making.of.mkv", someData("multi extras", 700_000)},
		{top + "/info.nfo", someData("multi info", 20_000)},
	}
	for _, outside := range []bool{true, false} {
		for _, p := range allPairs() {
			v := variantFor(p.old, outside)
			if v.outside != outside {
				continue // (Deluge keeps nothing outside the final folder)
			}
			t.Run(p.String()+"/"+v.name, func(t *testing.T) {
				w := realClients(t)
				c := newRealCaseOf(t, storetest.Open(t, store.SQLite), w, p, top, files, testMultiTorrentFile(t, top, files))
				dir, _ := v.setup(c)
				c.partialOld(0.45)
				c.track(StatusDownloading, true)
				old := c.old.find(settings.MovieCategory, c.hash)
				if old == nil || old.Progress >= 1 || old.Verified == 0 {
					t.Fatalf("the old client is not part-way: %+v", old)
				}
				if p.old != settings.ClientTransmission { // (see TestRealMoveHalfDownloaded)
					if got := c.filesIn(dir); !strings.Contains(got, dir+"/"+top+"/"+top+".mkv") {
						t.Fatalf("expected the video in %s, found:\n%s", dir, got)
					}
				}
				// The files the old client has written so far, without any ending.
				var had []string
				for _, l := range strings.Fields(c.filesIn(dir)) {
					rel := strings.TrimPrefix(l, dir+"/")
					had = append(had, strings.TrimSuffix(strings.TrimSuffix(rel, ".part"), ".!qB"))
				}
				if _, err := c.move(false); err != nil {
					t.Fatal(err)
				}
				x := c.assertMoved(c.oldBytes)
				if x.Done() || c.oldBytes == 0 || x.Verified < c.oldBytes {
					t.Errorf("new = %+v after the old one had %d", x, c.oldBytes)
				}
				t.Logf("old client had %d bytes verified; new client has %d (of %d); state %s", c.oldBytes, x.Verified, len(c.data), x.State)
				if outside && p.nw != settings.ClientQBittorrent {
					// Transmission and Deluge have moved what was in the temporary
					// folder to the final one by now (qBittorrent keeps an unfinished
					// torrent in its own temporary folder until it is done).
					if left := c.filesIn(c.tempDir); left != "" {
						t.Errorf("files were left behind in %s:\n%s", c.tempDir, left)
					}
					// ...and every file the old client had (it makes a file when it
					// first writes to it) is there.
					for _, f := range had {
						if got := c.filesIn(c.finalDir); !strings.Contains(got, c.finalDir+"/"+f) {
							t.Errorf("%s didn't arrive in %s:\n%s", f, c.finalDir, got)
						}
					}
				}
				done := c.completeFromSeeder()
				if done.Verified != int64(len(c.data)) {
					t.Errorf("finished with %d verified", done.Verified)
				}
				if outside {
					if left := c.filesIn(c.tempDir); left != "" {
						t.Errorf("files were left behind in %s:\n%s", c.tempDir, left)
					}
				}
				if bad := c.differs(c.finalDir); bad != "" {
					t.Errorf("the finished download is wrong: %s", bad)
				}
				if got := c.diskHas(c.finalDir); got != top {
					t.Errorf("final folder = %q", got)
				}
				if got := c.filesIn(c.finalDir); strings.Count(got, "\n")+1 != len(files) {
					t.Errorf("final folder holds:\n%s", got)
				}
			})
		}
	}
}

// A torrent still fetching its details (a magnet link nobody answers) moves
// as the magnet link: the new client has it and goes on fetching, and there
// is nothing to check.
func TestRealMoveTorrentFetchingDetails(t *testing.T) {
	directions(t, func(t *testing.T, w *realWorld, db *store.Store, p pair) {
		c := newRealCase(t, db, w, p, "Fetching.Film.2024.mkv", 100_000)
		// A made-up hash, and a tracker at 127.0.0.1 that isn't listening.
		hash := strings.Repeat("ae", 20)
		c.hash = hash
		magnet := "magnet:?xt=urn:btih:" + hash + "&dn=Fetching&tr=http%3A%2F%2F127.0.0.1%3A9%2Fannounce"
		if _, err := c.old.add(Add{Magnet: magnet, Category: settings.MovieCategory, Tags: []string{"reelkeeper", "Tracker"}, SavePath: c.finalDir}); err != nil {
			t.Fatal(err)
		}
		c.old.waitFor(settings.MovieCategory, hash, "fetching details", 20*time.Second, func(x Torrent) bool { return x.State == torrent.Metadata })
		c.track(StatusDownloading, false)
		if err := c.f.svc.storeSource(context.Background(), c.download().ID, Source{Magnet: magnet}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.move(false); err != nil {
			t.Fatal(err)
		}
		c.assertMoved(0)
		c.nw.waitFor(settings.MovieCategory, hash, "still fetching", 20*time.Second, func(x Torrent) bool { return x.State == torrent.Metadata })
	})
}

// Files in a folder the other client can't see: the new client's check finds
// nothing, so nothing moves. The torrent is removed from the new client
// without touching any file, every file is renamed back, and the old client
// then verifies exactly what it had. The download in the Queue stays
// stopped, and Reelkeeper stops tracking it so it is searched for again.
func TestRealMoveFilesTheOtherClientCannotSee(t *testing.T) {
	directions(t, func(t *testing.T, w *realWorld, db *store.Store, p pair) {
		c := newRealCase(t, db, w, p, "Hidden.Film.2024.mkv", 3_000_000)
		// Each container has a /downloads of its own, on a different volume
		// (Deluge's has none until a folder is made in it).
		hidden := "/downloads/incomplete"
		var file string
		switch p.old {
		case settings.ClientQBittorrent:
			w.fsC = w.qc // Reelkeeper sees qBittorrent's folder, the others can't
			c.restart()
			w.changeQ(map[string]any{"temp_path_enabled": true, "temp_path": hidden, "incomplete_files_ext": true})
			file = c.name + ".!qB"
		case settings.ClientTransmission:
			w.fsC = w.tc // Reelkeeper sees Transmission's folder, the others can't
			c.restart()
			w.changeT(map[string]any{"incomplete-dir-enabled": true, "incomplete-dir": hidden, "rename-partial-files": true})
			file = c.name + ".part"
		default:
			// Deluge keeps a download where it was told to: a folder of the
			// test's own, in its own container.
			hidden = "/downloads/hidden-" + path.Base(path.Dir(c.finalDir))
			w.fsC = w.dc
			c.restart()
			c.oldDir = hidden
			file = c.name
		}
		dockerExec(t, w.fsC, nil, "mkdir -p "+hidden+" && chown 1000:1000 "+hidden)
		t.Cleanup(func() {
			// Torrents (and their files) first, then only folders this test made.
			w.clear()
			if p.old == settings.ClientDeluge {
				dockerExecQuiet(w.dc, "rmdir '"+hidden+"'")
			}
			if p.nw == settings.ClientDeluge {
				// (Deluge makes the folder of a torrent it is given.)
				dockerExecQuiet(w.dc, "rmdir '"+hidden+"'")
				dockerExecQuiet(w.dc, "rmdir /downloads/incomplete")
			}
			if p.old == settings.ClientDeluge || p.nw == settings.ClientDeluge {
				dockerExecQuiet(w.dc, "rmdir /downloads")
			}
		})
		c.partialOld(0.4)
		c.track(StatusDownloading, true)
		before := ""
		if p.old != settings.ClientTransmission { // (Transmission writes its file only once stopped)
			if !strings.Contains(dockerExec(t, w.fsC, nil, "ls -A "+hidden), file) {
				t.Fatalf("expected %s in %s of the container Reelkeeper looks at", file, hidden)
			}
			before = dockerExec(t, w.fsC, nil, "ls -lA "+hidden+" | tail -n +2")
		}
		if _, err := c.move(false); err != nil {
			t.Fatal(err)
		}
		d := c.download()
		if d.Status != StatusRemoved || d.MoveState != "" || !strings.Contains(d.Message, "checked the files where they are") ||
			!strings.Contains(d.Message, "stopped in "+c.old.name) {
			t.Fatalf("record = %q %q %q", d.Status, d.MoveState, d.Message)
		}
		if c.nw.find(settings.MovieCategory, c.hash) != nil {
			t.Error("the new client still has the torrent")
		}
		// The old client has it, stopped, and after its own check it has what it had.
		x := c.old.waitFor(settings.MovieCategory, c.hash, "stopped, verified again", 60*time.Second, func(x Torrent) bool { return x.Stopped() && x.State != torrent.Checking })
		if !x.Stopped() || x.Verified < c.oldBytes || x.Verified == 0 {
			t.Errorf("old client: %+v (it had %d)", x, c.oldBytes)
		}
		t.Logf("the old client had %d bytes verified before, %d after the failed move", c.oldBytes, x.Verified)
		// Every file has its old name and folder back. (Compared without the
		// times, which the clients may have touched.)
		after := dockerExec(t, w.fsC, nil, "ls -lA "+hidden+" | tail -n +2")
		if p.old == settings.ClientTransmission {
			if !strings.Contains(after, file) {
				t.Errorf("%s isn't back in %s: %s", file, hidden, after)
			}
		} else if namesAndSizes(after) != namesAndSizes(before) {
			t.Errorf("the files in %s changed:\n before %s\n after  %s", hidden, before, after)
		}
		if ev := c.history(); len(ev) != 1 || ev[0].Event != "move-failed" {
			t.Errorf("history = %+v", ev)
		}
		if c.f.svc.Moving() {
			t.Error("still moving")
		}
		w.fsC = w.tc
		if p.old == settings.ClientQBittorrent {
			dockerExecQuiet(w.qc, "rm -rf "+hidden)
		}
	})
}

// namesAndSizes is the sizes and names in an `ls -l` listing (not the times,
// which a client may touch).
func namesAndSizes(listing string) string {
	var out []string
	for _, l := range strings.Split(listing, "\n") {
		if f := strings.Fields(l); len(f) >= 9 {
			out = append(out, f[4]+" "+f[8])
		}
	}
	return strings.Join(out, ";")
}

// A check that falls short in a place the new client CAN see: the files are in
// the shared folder, but by the time the new client checks them they are not
// what the old one had verified (the test blanks them). The new client finds
// less, the move is undone, and every file is exactly as it was: the same
// name (whatever the new client renamed it to while it checked, such as
// Transmission's ".part", is undone), and the old client verifies exactly the
// bytes it had before. (The test puts the data back as the undoing begins, so
// that the old client has something to verify.)
func TestRealMoveCheckFallsShort(t *testing.T) {
	directions(t, func(t *testing.T, w *realWorld, db *store.Store, p pair) {
		c := newRealCase(t, db, w, p, "Short.Film.2024.mkv", 3_000_000)
		dir, file := variantFor(p.old, false).setup(c)
		c.partialOld(0.4)
		c.track(StatusDownloading, true)
		backup := path.Dir(c.finalDir) + "/backup"
		var listBefore, sumBefore, renamedTo string
		// onDisk is the file as it is named now: plain, or with an ending.
		onDisk := func() string {
			for _, n := range []string{c.name, c.name + ".part", c.name + ".!qB"} {
				if exec.Command("docker", "exec", "-u", "root", w.tc, "test", "-e", dir+"/"+n).Run() == nil {
					return n
				}
			}
			return ""
		}
		c.f.svc.moveHook = func(state string) error {
			switch state {
			case moveRenaming:
				// The old client has stopped; this is the folder as it left it.
				listBefore = dockerExec(t, w.tc, nil, "ls -lA '"+dir+"' | tail -n +2")
				if n := onDisk(); n != file {
					t.Errorf("the file is %q once the old client had stopped, expected %q", n, file)
				} else {
					sumBefore = sha(t, w.tc, dir+"/"+n)
				}
			case moveAdded:
				// Added, not yet checked: blank the file (its size stays).
				dockerExec(t, w.tc, nil, fmt.Sprintf("mkdir -p '%[1]s' && cp -p '%[2]s/%[3]s' '%[1]s/data' && n=$(wc -c < '%[2]s/%[3]s') && head -c $n /dev/zero > '%[2]s/%[3]s'", backup, dir, onDisk()))
			case moveRollback:
				// The new client has checked and found nothing; what it renamed the
				// file to is what is on disk now. Put the data back in it.
				renamedTo = onDisk()
				dockerExec(t, w.tc, nil, fmt.Sprintf("cat '%[1]s/data' > '%[2]s/%[3]s'", backup, dir, renamedTo))
			}
			return nil
		}
		if _, err := c.move(false); err != nil {
			t.Fatal(err)
		}
		d := c.download()
		if d.Status != StatusRemoved || d.MoveState != "" || !strings.Contains(d.Message, "checked the files where they are") {
			t.Fatalf("record = %q %q %q", d.Status, d.MoveState, d.Message)
		}
		t.Logf("the file was %q when the move began, %q when it was undone", file, renamedTo)
		if p.nw == settings.ClientTransmission && renamedTo != c.name+".part" {
			t.Errorf("Transmission was meant to rename the file to .part while it checked, and left %q", renamedTo)
		}
		if c.nw.find(settings.MovieCategory, c.hash) != nil {
			t.Error("the new client still has the torrent")
		}
		// Back as it was: the same names and sizes, the same bytes.
		after := dockerExec(t, w.tc, nil, "ls -lA '"+dir+"' | tail -n +2")
		if namesAndSizes(after) != namesAndSizes(listBefore) {
			t.Errorf("the folder changed:\n before %s\n after  %s", listBefore, after)
		}
		if got := sha(t, w.tc, dir+"/"+file); got != sumBefore {
			t.Errorf("%s has checksum %s, was %s", file, got, sumBefore)
		}
		x := c.old.waitFor(settings.MovieCategory, c.hash, "stopped, verified again", 60*time.Second, func(x Torrent) bool { return x.Stopped() && x.State != torrent.Checking })
		if x.Verified < c.oldBytes || x.Verified == 0 {
			t.Errorf("old client: %+v (it had %d)", x, c.oldBytes)
		}
		t.Logf("the old client had %d bytes verified before, %d after the failed move", c.oldBytes, x.Verified)
		if ev := c.history(); len(ev) != 1 || ev[0].Event != "move-failed" {
			t.Errorf("history = %+v", ev)
		}
	})
}

// A restart in the middle of a move carries on from the step each torrent
// had reached, and the torrent still arrives checked and intact.
func TestRealMoveSurvivesARestart(t *testing.T) {
	for _, step := range []string{moveStopped, moveRenaming, moveRenamed, moveAdded, moveFinishing} {
		t.Run(step, func(t *testing.T) {
			directions(t, func(t *testing.T, w *realWorld, db *store.Store, p pair) {
				c := newRealCase(t, db, w, p, "Restart.Film.2024.mkv", 3_000_000)
				// Files outside the final folder, named as the old client names
				// unfinished ones (Deluge has no such thing).
				variantFor(p.old, true).setup(c)
				c.partialOld(0.4)
				c.track(StatusDownloading, true)
				c.f.svc.moveHook = func(s string) error {
					if s == step {
						return errCrash
					}
					return nil
				}
				if _, err := c.move(false); err == nil || !strings.Contains(err.Error(), "simulated crash") {
					t.Fatalf("move = %v", err)
				}
				if d := c.download(); d.MoveState != step || !c.f.svc.Moving() {
					t.Fatalf("saved step %q, moving %v", d.MoveState, c.f.svc.Moving())
				}
				saved := c.oldBytes
				c.restart()
				if _, err := c.run(); err != nil {
					t.Fatal(err)
				}
				if c.oldBytes == 0 {
					c.oldBytes = saved
				}
				x := c.assertMoved(c.oldBytes)
				if x.Verified == 0 || c.oldBytes == 0 {
					t.Errorf("new torrent = %+v, the old one had %d", x, c.oldBytes)
				}
				done := c.completeFromSeeder()
				if bad := c.differs(c.finalDir); done.Verified != int64(len(c.data)) || bad != "" {
					t.Errorf("the finished download is wrong: %+v %s", done, bad)
				}
			})
		})
	}
}
