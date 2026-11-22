package library

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/jobs"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// moveFix is the scan fixture plus a second, empty movie folder ("movies",
// where the kids' film turns out to belong), both managed, with 28 Days
// Later (2002) found in the first.
type moveFix struct {
	*fixture
	movies *RootFolder
	dir2   string
	title  *Title
	v      *Version
}

const daysFolder = "28 Days Later (2002)"

func newMoveFix(t *testing.T, db *store.Store) *moveFix {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t, db)
	if _, err := f.svc.Scan(ctx, f.hd.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpdateRootFolder(ctx, f.hd.ID, 1, true, nil); err != nil {
		t.Fatal(err)
	}
	dir2 := t.TempDir()
	movies, err := f.svc.AddRootFolder(ctx, RootInput{Path: dir2, MediaType: Movie, DefaultProfileID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpdateRootFolder(ctx, movies.ID, 1, true, nil); err != nil {
		t.Fatal(err)
	}
	title := f.titleByTMDB(t, 170)
	v := new(Version)
	if err := db.DB.NewSelect().Model(v).Where("title_id = ?", title.ID).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	return &moveFix{fixture: f, movies: movies, dir2: dir2, title: title, v: v}
}

func (m *moveFix) plan(t *testing.T) *MovePlan {
	t.Helper()
	p, err := m.svc.PlanMove(context.Background(), m.title.ID, m.v.ID, m.movies.ID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (m *moveFix) move(t *testing.T) (MoveResult, error) {
	t.Helper()
	return m.svc.MoveVersion(context.Background(), MoveArgs{VersionID: m.v.ID, FromRootID: m.hd.ID, ToRootID: m.movies.ID}, nil)
}

// versionRoot is the media folder the library has the version in now.
func (m *moveFix) versionRoot(t *testing.T) int64 {
	t.Helper()
	v := new(Version)
	if err := m.db.DB.NewSelect().Model(v).Where("id = ?", m.v.ID).Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, f := range m.filesOf(t, 170) {
		if f.RootFolderID != v.RootFolderID {
			t.Errorf("file %s is recorded in media folder %d, its version in %d", f.Path, f.RootFolderID, v.RootFolderID)
		}
	}
	return v.RootFolderID
}

// rescanBoth scans both folders and fails if either sees anything new,
// changed or gone, or a new doubtful folder: after a move the library and
// the disks agree. (The fixture's library has two doubtful folders of its
// own, which stay.)
func (m *moveFix) rescanBoth(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	before, _ := m.svc.ReviewItems(ctx)
	for _, r := range []*RootFolder{m.hd, m.movies} {
		res, err := m.svc.Scan(ctx, r.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Added+res.Updated+res.Missing != 0 {
			t.Errorf("rescan of %s after the move: %+v", r.Path, res)
		}
	}
	if after, _ := m.svc.ReviewItems(ctx); len(after) != len(before) {
		t.Errorf("review items: %d before the rescan, %d after", len(before), len(after))
	}
}

// The whole folder moves: the video and everything with it (trailer,
// featurettes, artwork, subtitles), on the same disk by renaming.
func TestMoveWholeFolderOnTheSameDisk(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMoveFix(t, db)
		touch(t, m.dir, daysFolder+"/28.Days.Later.2002.1080p.BluRay.DDP5.1.x265.10bit-GRP.en.srt", []byte("subs"))
		var notified []MoveResult
		m.svc.OnMoved(func(_ context.Context, r MoveResult) { notified = append(notified, r) })

		p := m.plan(t)
		if len(p.Problems) != 0 || !p.WholeFolder || p.Copy {
			t.Fatalf("plan = %+v", p)
		}
		if len(p.Files) != 6 { // video, subtitles, trailer, featurette, sample, thumb
			t.Errorf("plan has %d files: %+v", len(p.Files), p.Files)
		}

		res, err := m.move(t)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range p.Files {
			if exists(filepath.Join(m.dir, f.Path)) || !exists(filepath.Join(m.dir2, f.Path)) {
				t.Errorf("%s didn't move", f.Path)
			}
		}
		if exists(filepath.Join(m.dir, daysFolder)) {
			t.Error("the empty old folder was left behind")
		}
		if m.versionRoot(t) != m.movies.ID {
			t.Error("the library still has the movie in the old folder")
		}
		if res.Moved != 1 || len(res.Left) != 0 || !strings.Contains(res.Message, m.dir2) {
			t.Errorf("result = %+v", res)
		}
		if len(notified) != 1 || notified[0].FromDir != filepath.Join(m.dir, daysFolder) || notified[0].ToDir != filepath.Join(m.dir2, daysFolder) {
			t.Errorf("told the media servers %+v", notified)
		}
		m.rescanBoth(t)
	})
}

// Across disks the files are copied, checked, then the old copies deleted.
// Each copy keeps the original's time, so a scan sees nothing changed.
func TestMoveAcrossDisksCopiesThenDeletes(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMoveFix(t, db)
		m.svc.onSameDisk = func(string, string) bool { return false }
		video := filepath.Join(daysFolder, "28.Days.Later.2002.1080p.BluRay.DDP5.1.x265.10bit-GRP.mkv")
		old := time.Date(2020, 5, 1, 12, 0, 0, 0, time.UTC)
		if err := os.Chtimes(filepath.Join(m.dir, video), old, old); err != nil {
			t.Fatal(err)
		}
		if _, err := m.svc.Scan(context.Background(), m.hd.ID, nil); err != nil {
			t.Fatal(err)
		}

		p := m.plan(t)
		if !p.Copy || len(p.Problems) != 0 {
			t.Fatalf("plan = %+v", p)
		}
		var steps []string
		res, err := m.svc.MoveVersion(context.Background(), MoveArgs{VersionID: m.v.ID, FromRootID: m.hd.ID, ToRootID: m.movies.ID},
			func(_ int, msg string) { steps = append(steps, msg) })
		if err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(filepath.Join(m.dir2, video))
		if err != nil || !st.ModTime().Equal(old) {
			t.Errorf("the copy: %v, %v (want the original's time %v)", st, err, old)
		}
		if exists(filepath.Join(m.dir, daysFolder)) {
			t.Error("the old copy wasn't deleted")
		}
		if exists(filepath.Join(m.dir2, movingDir)) {
			t.Error("the hidden staging folder was left behind")
		}
		if len(res.Left) != 0 || !strings.Contains(strings.Join(steps, "|"), "Copying") {
			t.Errorf("result = %+v, steps %q", res, steps)
		}
		if m.versionRoot(t) != m.movies.ID {
			t.Error("not recorded in the new folder")
		}
		m.rescanBoth(t)
	})
}

// A version sharing its folder with another version (multi-version naming)
// takes only its own video and the files named after it.
func TestMoveFromASharedFolderTakesOnlyItsFiles(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newMoveFix(t, db)
		const remux = daysFolder + "/28 Days Later (2002) - Remux.mkv"
		const subs = daysFolder + "/28.Days.Later.2002.1080p.BluRay.DDP5.1.x265.10bit-GRP.en.srt"
		touch(t, m.dir, remux, []byte("remux"))
		touch(t, m.dir, subs, []byte("subs"))
		other := &Version{TitleID: m.title.ID, ProfileID: 2, RootFolderID: m.hd.ID, Folder: daysFolder, Monitored: true, Upgrades: true, CreatedAt: time.Now()}
		if _, err := db.DB.NewInsert().Model(other).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		rf := &MediaFile{VersionID: other.ID, RootFolderID: m.hd.ID, Path: remux, Size: 5, AddedAt: time.Now(), ReleaseInfo: "{}", MediaInfo: "{}", EpisodeClaim: "null"}
		if _, err := db.DB.NewInsert().Model(rf).Exec(ctx); err != nil {
			t.Fatal(err)
		}

		p := m.plan(t)
		if p.WholeFolder || len(p.Files) != 2 || len(p.Problems) != 0 {
			t.Fatalf("plan = %+v", p)
		}
		if _, err := m.move(t); err != nil {
			t.Fatal(err)
		}
		if !exists(filepath.Join(m.dir2, subs)) || exists(filepath.Join(m.dir, subs)) {
			t.Error("the subtitles didn't go with the video")
		}
		for _, stays := range []string{remux, daysFolder + "/28 Days Later (2002)-trailer.mkv", daysFolder + "/Featurettes/Making Of.mkv"} {
			if !exists(filepath.Join(m.dir, stays)) || exists(filepath.Join(m.dir2, stays)) {
				t.Errorf("%s should have stayed: it isn't this version's", stays)
			}
		}
	})
}

// A file already at the destination stops the move before anything
// changes; nothing is ever overwritten.
func TestMoveNeverOverwrites(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newMoveFix(t, db)
		const thumb = daysFolder + "/28 Days Later (2002)-thumb.jpg"
		touch(t, m.dir2, thumb, []byte("someone else's"))

		p := m.plan(t)
		if len(p.Problems) != 1 || !strings.Contains(p.Problems[0], "never overwrites") || !strings.Contains(p.Problems[0], "thumb.jpg") {
			t.Fatalf("problems = %q", p.Problems)
		}
		jm := jobs.New(db, slog.New(slog.DiscardHandler))
		m.svc.RegisterTasks(jm)
		var refused *MoveRefused
		if _, err := m.svc.QueueMove(ctx, jm, m.title.ID, m.v.ID, m.movies.ID); !errors.As(err, &refused) {
			t.Fatalf("queue = %v, want a refusal", err)
		}
		if _, err := m.move(t); err == nil || !strings.Contains(err.Error(), "nothing was moved") {
			t.Errorf("move = %v", err)
		}
		if b, _ := os.ReadFile(filepath.Join(m.dir2, thumb)); string(b) != "someone else's" {
			t.Error("the file at the destination was changed")
		}
		if !exists(filepath.Join(m.dir, thumb)) || m.versionRoot(t) != m.hd.ID {
			t.Error("something moved")
		}
	})
}

// If a rename is refused partway (another program took the name after the
// check), the files already moved go back and the library is unchanged.
func TestMoveFailingHalfwayPutsEverythingBack(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		m := newMoveFix(t, db)
		p := m.plan(t)
		before := map[string]bool{}
		for _, f := range p.Files {
			before[f.Path] = true
		}
		n := 0
		m.svc.beforeRename = func(_, to string) {
			if n++; n == 3 {
				touch(t, m.dir2, mustRel(t, m.dir2, to), []byte("intruder"))
			}
		}
		if _, err := m.move(t); err == nil || !strings.Contains(err.Error(), "never overwrites") {
			t.Fatalf("move = %v", err)
		}
		m.svc.beforeRename = nil
		for f := range before {
			if !exists(filepath.Join(m.dir, f)) {
				t.Errorf("%s wasn't put back", f)
			}
		}
		var left []string
		_ = filepath.Walk(m.dir2, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				left = append(left, p)
			}
			return nil
		})
		if len(left) != 1 { // only the intruder
			t.Errorf("left at the destination: %q", left)
		}
		if m.versionRoot(t) != m.hd.ID {
			t.Error("the library recorded a move that didn't happen")
		}
	})
}

func mustRel(t *testing.T, base, p string) string {
	t.Helper()
	r, err := filepath.Rel(base, p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A move cut short by a restart after the library recorded it finishes
// when the task runs again: the old copy is deleted, but only files whose
// copy is in the new folder.
func TestMoveResumesAfterARestart(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newMoveFix(t, db)
		p := m.plan(t)
		// What the earlier attempt did: copies in place and the move
		// recorded, all but the thumbnail copied.
		for _, f := range p.Files {
			if strings.HasSuffix(f.Path, "thumb.jpg") {
				continue
			}
			b, _ := os.ReadFile(filepath.Join(m.dir, f.Path))
			touch(t, m.dir2, f.Path, b)
		}
		if _, err := db.DB.NewUpdate().Model((*Version)(nil)).Set("root_folder_id = ?", m.movies.ID).Where("id = ?", m.v.ID).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.NewUpdate().Model((*MediaFile)(nil)).Set("root_folder_id = ?", m.movies.ID).Where("version_id = ?", m.v.ID).Exec(ctx); err != nil {
			t.Fatal(err)
		}

		res, err := m.move(t)
		if err != nil {
			t.Fatal(err)
		}
		const thumb = daysFolder + "/28 Days Later (2002)-thumb.jpg"
		if !exists(filepath.Join(m.dir, thumb)) {
			t.Error("deleted a file that has no copy in the new folder")
		}
		if len(res.Left) != 1 || !strings.Contains(res.Left[0], "thumb.jpg") || !strings.Contains(res.Message, "Left in the old folder") {
			t.Errorf("result = %+v", res)
		}
		if exists(filepath.Join(m.dir, daysFolder, "28.Days.Later.2002.1080p.BluRay.DDP5.1.x265.10bit-GRP.mkv")) {
			t.Error("the old copy of the video wasn't deleted")
		}
	})
}

// While a movie moves, imports into it wait, renames and deletes refuse,
// and scans leave its folder alone (halfway it would look gone or new).
func TestWhileMovingOthersKeepOff(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newMoveFix(t, db)
		m.svc.startMoving(m.v.ID, movingVersion{from: m.hd.ID, to: m.movies.ID, paths: []string{daysFolder}})

		if _, err := m.svc.ImportMovie(ctx, ImportRequest{VersionID: m.v.ID, Source: "/nowhere"}); !errors.Is(err, ErrMoving) {
			t.Errorf("import = %v, want ErrMoving", err)
		}
		if _, err := m.svc.DeleteMovie(ctx, m.title.ID, true); !errors.Is(err, ErrMoving) {
			t.Errorf("delete = %v, want ErrMoving", err)
		}
		plan, err := m.svc.RenamePlan(ctx, m.title.ID)
		if err != nil || len(plan) != 1 || len(plan[0].Files) != 1 || !strings.Contains(plan[0].Files[0].Problem, "being moved") {
			t.Errorf("rename plan = %+v, %v", plan, err)
		}
		if p := m.plan(t); len(p.Problems) == 0 || !strings.Contains(p.Problems[0], "already being moved") {
			t.Errorf("a second move: %q", p.Problems)
		}
		// Halfway: the folder is gone from the old media folder.
		if err := os.Rename(filepath.Join(m.dir, daysFolder), filepath.Join(m.dir2, daysFolder)); err != nil {
			t.Fatal(err)
		}
		for _, r := range []*RootFolder{m.hd, m.movies} {
			res, err := m.svc.Scan(ctx, r.ID, nil)
			if err != nil || res.Missing+res.Added != 0 {
				t.Errorf("scan of %s during the move = %+v, %v", r.Path, res, err)
			}
		}
		if items, _ := m.svc.ReviewItems(ctx); len(items) != 2 { // the fixture's own two
			t.Errorf("a scan during the move made review items: %+v", items)
		}
		m.svc.stopMoving(m.v.ID)
		if res, _ := m.svc.Scan(ctx, m.hd.ID, nil); res.Missing == 0 {
			t.Error("after the move a scan should look at the folder again")
		}
	})
}

// The plan says why a move can't be done: a folder Reelkeeper only reads,
// a TV folder, the folder it's already in.
func TestMoveProblems(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newMoveFix(t, db)
		if _, err := m.svc.UpdateRootFolder(ctx, m.movies.ID, 1, false, nil); err != nil {
			t.Fatal(err)
		}
		if p := m.plan(t); len(p.Problems) != 1 || !strings.Contains(p.Problems[0], "only reads "+m.dir2) {
			t.Errorf("read-only destination: %q", p.Problems)
		}
		tv, err := m.svc.AddRootFolder(ctx, RootInput{Path: t.TempDir(), MediaType: Series, DefaultProfileID: 1})
		if err != nil {
			t.Fatal(err)
		}
		if p, _ := m.svc.PlanMove(ctx, m.title.ID, m.v.ID, tv.ID); len(p.Problems) != 1 || !strings.Contains(p.Problems[0], "TV media folder") {
			t.Errorf("TV folder: %q", p.Problems)
		}
		if p, _ := m.svc.PlanMove(ctx, m.title.ID, m.v.ID, m.hd.ID); len(p.Problems) != 1 || !strings.Contains(p.Problems[0], "already in") {
			t.Errorf("same folder: %q", p.Problems)
		}
		if _, err := m.svc.PlanMove(ctx, m.title.ID+999, m.v.ID, m.movies.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("another title's version: %v", err)
		}
		// Another film's video already in a folder of that name at the
		// destination: the media server would show two movies.
		if _, err := m.svc.UpdateRootFolder(ctx, m.movies.ID, 1, true, nil); err != nil {
			t.Fatal(err)
		}
		touch(t, m.dir2, daysFolder+"/Something Else (1999).mkv", []byte("x"))
		if p := m.plan(t); len(p.Problems) != 1 || !strings.Contains(p.Problems[0], "Something Else (1999).mkv") {
			t.Errorf("stray video: %q", p.Problems)
		}
	})
}

// A version with nothing on disk yet just changes folder: its downloads
// will go to the new one.
func TestMoveWithNothingOnDisk(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		m := newMoveFix(t, db)
		if err := os.RemoveAll(filepath.Join(m.dir, daysFolder)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB.NewDelete().Model((*MediaFile)(nil)).Where("version_id = ?", m.v.ID).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if p := m.plan(t); len(p.Files) != 0 || len(p.Problems) != 0 {
			t.Fatalf("plan = %+v", p)
		}
		if _, err := m.move(t); err != nil {
			t.Fatal(err)
		}
		if m.versionRoot(t) != m.movies.ID {
			t.Error("not recorded in the new folder")
		}
	})
}
