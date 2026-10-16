package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/saltglass/reelkeeper/internal/jobs"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
)

// Ext is a backup file's extension.
const Ext = ".backup"

// safetyKept is how many safety backups (made before a restore) are kept.
const safetyKept = 3

// File names in the data folder that carry a restore across a restart.
// setupFile is a backup uploaded on the first-run screen; each upload
// replaces the last, so the open setup screen can't fill the disk.
const (
	pendingFile = "restore-pending.json"
	resultFile  = "restore-result.json"
	setupFile   = "setup-restore.backup"
	// checksFile remembers what List found out about each backup file, so
	// the first Backups page after a restart doesn't read every file again.
	checksFile = "backup-checks.json"
)

// TaskKind is the scheduled backup's task.
const TaskKind = "backup.scheduled"

// Service makes, lists and restores backups in the configured folder.
type Service struct {
	db       *store.Store
	settings func() settings.Backups
	// KeyCheck returns the database's sealed key check (settings).
	keyCheck func(ctx context.Context) (string, error)
	// KeyOpens reports whether a sealed key check opens with the running key.
	keyOpens func(sealed string) bool
	dataDir  string
	app      string
	logger   *slog.Logger
	now      func() time.Time
	// restart asks Reelkeeper to shut down cleanly so it starts again (Docker
	// restarts it) and restores before anything else runs.
	restart func()

	mu sync.Mutex // one backup or restore at a time

	// checked remembers what List found out about each file, so a file is
	// read in full once, not every time the page asks (or after a restart:
	// it is saved in the data folder, see checksFile). A file is the same
	// file while its name, size and modification time are. inspect reads a
	// file in full (a field so tests can count the reads). A nil checked
	// means nothing is remembered.
	checkedMu sync.Mutex
	checked   map[fileKey]checkedFile
	inspect   func(path string) (*Summary, error)
}

// fileKey identifies a backup file as it was when it was checked.
type fileKey struct {
	name    string
	size    int64
	modTime int64 // UnixNano, so it survives being saved to a file
}

// checkedFile is the result of reading a backup file in full.
type checkedFile struct {
	header  *Header
	problem string
}

// Config is what New needs.
type Config struct {
	DB       *store.Store
	Settings func() settings.Backups
	KeyCheck func(ctx context.Context) (string, error)
	KeyOpens func(sealed string) bool
	DataDir  string
	App      string
	Logger   *slog.Logger
	Restart  func()
}

// New returns the backup service.
func New(c Config) *Service {
	s := &Service{db: c.DB, settings: c.Settings, keyCheck: c.KeyCheck, keyOpens: c.KeyOpens, dataDir: c.DataDir,
		app: c.App, logger: c.Logger, now: time.Now, restart: c.Restart,
		checked: map[fileKey]checkedFile{}, inspect: inspectFile}
	s.loadChecks()
	return s
}

// checkedRecord is one remembered file as saved in checksFile.
type checkedRecord struct {
	Name    string  `json:"name"`
	Size    int64   `json:"size"`
	ModTime int64   `json:"modTime"`
	Header  *Header `json:"header,omitempty"`
	Problem string  `json:"problem,omitempty"`
}

// loadChecks reads the remembered results saved by an earlier run. A missing
// or unreadable file just means starting empty: the files get read again.
func (s *Service) loadChecks() {
	b, err := os.ReadFile(filepath.Join(s.dataDir, checksFile)) // #nosec G304 -- a fixed name in the data folder
	if err != nil {
		return
	}
	var records []checkedRecord
	if json.Unmarshal(b, &records) != nil {
		return
	}
	for _, r := range records {
		if r.Header == nil && r.Problem == "" {
			continue // neither a result nor a problem: not something List saved
		}
		s.checked[fileKey{r.Name, r.Size, r.ModTime}] = checkedFile{header: r.Header, problem: r.Problem}
	}
}

// saveChecks writes the remembered results to the data folder, through a
// temporary file so a crash never leaves half a file. The caller holds
// checkedMu. A failure only costs a re-read after the next restart.
func (s *Service) saveChecks() {
	records := make([]checkedRecord, 0, len(s.checked))
	for k, c := range s.checked {
		records = append(records, checkedRecord{k.name, k.size, k.modTime, c.header, c.problem})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	b, err := json.Marshal(records)
	if err == nil {
		err = os.MkdirAll(s.dataDir, 0o750)
	}
	var tmp *os.File
	if err == nil {
		tmp, err = os.CreateTemp(s.dataDir, checksTempPrefix+"*")
	}
	if err == nil {
		_, err = tmp.Write(b)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmp.Name(), filepath.Join(s.dataDir, checksFile))
		}
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}
	if err != nil {
		s.logger.Warn("couldn't save which backups were checked; they will be read again after a restart", "error", err)
	}
}

// remember records a file we just wrote and so already know is fine, so the
// next List doesn't read it back.
func (s *Service) remember(name string, info os.FileInfo, h *Header) {
	if s.checked == nil || info == nil {
		return
	}
	s.checkedMu.Lock()
	defer s.checkedMu.Unlock()
	s.checked[fileKey{name, info.Size(), info.ModTime().UnixNano()}] = checkedFile{header: h}
	s.saveChecks()
}

// freeName is the name for a new backup made at stamp: the plain name, or if
// a file already has it (two backups in the same second) the same with _2,
// _3 and so on after the time. "_" sorts after "-", so the later one still
// sorts as newer, and the kind and ending that pruning and the page look at
// stay as they were.
func freeName(dir, stamp, kind string) string {
	name := fmt.Sprintf("reelkeeper-%s-%s%s", stamp, kind, Ext)
	for n := 2; ; n++ {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			return name // free (or unreadable, which the rename will report)
		}
		name = fmt.Sprintf("reelkeeper-%s_%d-%s%s", stamp, n, kind, Ext)
	}
}

// Leftovers are temporary files and folders a killed Reelkeeper can leave
// behind. Only these names are ever removed by RemoveLeftovers.
const (
	writingPrefix     = ".writing-"      // a backup being made
	uploadPrefix      = ".upload-"       // a backup being uploaded
	setupUploadPrefix = ".setup-upload-" // a first-run upload in the data folder
	checksTempPrefix  = ".backup-checks-"
	scratchPrefix     = "restore-" // folders only: restore-pending.json and restore-result.json are files
)

// RemoveLeftovers deletes the temporary files a killed backup, upload or
// restore left behind. Call it once at startup, before anything can be
// writing. Each one removed is logged.
func (s *Service) RemoveLeftovers() {
	remove := func(path string, dir bool) {
		var err error
		if dir {
			err = os.RemoveAll(path)
		} else {
			err = os.Remove(path)
		}
		if err != nil {
			s.logger.Warn("couldn't remove a leftover from an interrupted backup", "path", path, "error", err)
			return
		}
		s.logger.Info("removed a leftover from an interrupted backup", "path", path)
	}
	// In the backup folders: half-written backups and uploads.
	for _, dir := range []string{s.Folder(), filepath.Join(s.dataDir, "backups")} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !e.IsDir() && (strings.HasPrefix(e.Name(), writingPrefix) || strings.HasPrefix(e.Name(), uploadPrefix)) {
				remove(filepath.Join(dir, e.Name()), false)
			}
		}
	}
	// In the data folder: first-run uploads, the saved-results temp file and
	// the scratch folders of restoring an older backup.
	entries, _ := os.ReadDir(s.dataDir)
	for _, e := range entries {
		n := e.Name()
		switch {
		case !e.IsDir() && (strings.HasPrefix(n, setupUploadPrefix) || strings.HasPrefix(n, checksTempPrefix)):
			remove(filepath.Join(s.dataDir, n), false)
		case e.IsDir() && strings.HasPrefix(n, scratchPrefix) && onlyScratch(filepath.Join(s.dataDir, n)):
			remove(filepath.Join(s.dataDir, n), true)
		}
	}
}

// onlyScratch reports whether a restore-* folder holds nothing but an
// upgrade's scratch database (scratch.db and its -wal and -shm files), so a
// folder that merely shares the name, with anything else in it, is never
// deleted.
func onlyScratch(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "scratch.db") {
			return false
		}
	}
	return true
}

// Folder is where backups go: the setting, or "backups" in the data folder.
func (s *Service) Folder() string {
	if f := s.settings().Folder; f != "" {
		return f
	}
	return filepath.Join(s.dataDir, "backups")
}

// Entry is one backup in the folder.
type Entry struct {
	Name   string  `json:"name"`
	Size   int64   `json:"size"`
	Header *Header `json:"header,omitempty"`
	// KeyMatches: the running secret key opens this backup's credentials.
	KeyMatches bool `json:"keyMatches"`
	// Problem says why the file can't be used ("" when it looks fine).
	Problem string `json:"problem,omitempty"`
}

// List returns the folder's backups, newest first. Each file is read in
// full (format, rows and checksum), so a damaged or cut-off file shows its
// problem here and not only when someone tries to restore it. The result
// is remembered per file, so only new or changed files are read.
func (s *Service) List() ([]Entry, error) {
	dir := s.Folder()
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the backup folder %s: %w", dir, err)
	}
	out := []Entry{}
	seen := map[fileKey]checkedFile{}
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), Ext) {
			continue
		}
		e := Entry{Name: f.Name()}
		info, err := f.Info()
		if err != nil {
			// Gone since the folder was read.
			continue
		}
		e.Size = info.Size()
		key := fileKey{f.Name(), info.Size(), info.ModTime().UnixNano()}
		c := s.checkFile(key, filepath.Join(dir, f.Name()))
		seen[key] = c
		if c.problem != "" {
			e.Problem = c.problem
		} else {
			e.Header = c.header
			e.KeyMatches = c.header.KeyCheck == "" || s.keyOpens(c.header.KeyCheck)
		}
		out = append(out, e)
	}
	// Forget files that are gone or have changed, and save what we now know.
	s.checkedMu.Lock()
	changed := len(seen) != len(s.checked)
	for k := range seen {
		if _, ok := s.checked[k]; !ok {
			changed = true
		}
	}
	s.checked = seen
	if changed {
		s.saveChecks()
	}
	s.checkedMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return created(out[i]).After(created(out[j])) })
	return out, nil
}

// checkFile reads a backup in full, unless it was already read as it is now.
func (s *Service) checkFile(key fileKey, path string) checkedFile {
	s.checkedMu.Lock()
	c, ok := s.checked[key]
	s.checkedMu.Unlock()
	if ok {
		return c
	}
	if sum, err := s.inspect(path); err != nil {
		c = checkedFile{problem: plain(err)}
	} else {
		c = checkedFile{header: &sum.Header}
	}
	return c
}

func created(e Entry) time.Time {
	if e.Header != nil {
		return e.Header.CreatedAt
	}
	return time.Time{}
}

// Create makes a backup now and prunes old ones.
func (s *Service) Create(ctx context.Context, kind string) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.create(ctx, s.Folder(), kind)
}

func (s *Service) create(ctx context.Context, dir, kind string) (*Entry, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create the backup folder %s: %w", dir, err)
	}
	sealed, err := s.keyCheck(ctx)
	if err != nil {
		return nil, err
	}
	// The caller holds s.mu (or runs alone at startup), so the name stays
	// free until the rename below.
	name := freeName(dir, s.now().UTC().Format("20060102-150405"), kind)
	final := filepath.Join(dir, name)
	// Written under a temporary name, so a half-written file never looks
	// like a backup.
	tmp, err := os.CreateTemp(dir, ".writing-*")
	if err != nil {
		return nil, fmt.Errorf("write to the backup folder %s: %w", dir, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	sum, err := Write(ctx, s.db, tmp, Meta{App: s.app, Kind: kind, KeyCheck: sealed})
	if err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return nil, err
	}
	s.prune(dir)
	info, _ := os.Stat(final)
	e := &Entry{Name: name, Header: &sum.Header, KeyMatches: true}
	if info != nil {
		e.Size = info.Size()
	}
	s.remember(name, info, &sum.Header)
	s.logger.Info("backup made", "file", final, "kind", kind, "rows", sum.Total)
	return e, nil
}

// prune keeps the newest Keep scheduled and manual backups and the newest
// few safety backups. Uploaded backups are left for the user to delete.
func (s *Service) prune(dir string) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var regular, safety []string
	for _, f := range files {
		n := f.Name()
		switch {
		case !strings.HasPrefix(n, "reelkeeper-") || !strings.HasSuffix(n, Ext):
		case strings.HasSuffix(n, "-"+KindSafety+Ext):
			safety = append(safety, n)
		case strings.HasSuffix(n, "-"+KindScheduled+Ext), strings.HasSuffix(n, "-"+KindManual+Ext):
			regular = append(regular, n)
		}
	}
	drop := func(names []string, keep int) {
		// The names start with the time they were made, so they sort by age.
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		for _, n := range names[min(keep, len(names)):] {
			if err := os.Remove(filepath.Join(dir, n)); err == nil {
				s.logger.Info("old backup deleted", "file", n)
			}
		}
	}
	drop(regular, s.settings().Keep)
	drop(safety, safetyKept)
}

// path returns the full path of a backup named in a request, refusing
// anything that isn't a plain backup file name in the folder.
func (s *Service) path(name string) (string, error) {
	if name == "" || name != filepath.Base(name) || !strings.HasSuffix(name, Ext) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	p := filepath.Join(s.Folder(), name)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	return p, nil
}

// ErrNotFound means there is no backup by that name in the folder.
var ErrNotFound = errors.New("no such backup")

// Open opens a backup for download.
func (s *Service) Open(name string) (*os.File, error) {
	p, err := s.path(name)
	if err != nil {
		return nil, err
	}
	return os.Open(p) // #nosec G304 -- path() allows only plain backup names in the folder
}

// Delete removes a backup.
func (s *Service) Delete(name string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// Upload saves a backup someone sends (for example from another
// installation), after checking it in full. Its name says it was uploaded,
// and pruning leaves it alone.
func (s *Service) Upload(r io.Reader) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.Folder()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create the backup folder %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	sum, err := Inspect(tmp)
	_ = tmp.Close()
	if err != nil {
		return nil, err
	}
	name := freeName(dir, s.now().UTC().Format("20060102-150405"), "uploaded")
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		return nil, err
	}
	info, _ := os.Stat(filepath.Join(dir, name))
	e := &Entry{Name: name, Header: &sum.Header, KeyMatches: sum.Header.KeyCheck == "" || s.keyOpens(sum.Header.KeyCheck)}
	if info != nil {
		e.Size = info.Size()
	}
	s.remember(name, info, &sum.Header)
	return e, nil
}

// Check reads a backup in full and says whether it can be restored here,
// changing nothing: the format, checksum, version and key.
func (s *Service) Check(ctx context.Context, name string) (*Summary, error) {
	p, err := s.path(name)
	if err != nil {
		return nil, err
	}
	sum, err := inspectFile(p)
	if err != nil {
		return nil, err
	}
	return sum, s.usable(ctx, sum)
}

// pending is a restore waiting for the next start.
type pending struct {
	Path        string    `json:"path"`
	RequestedAt time.Time `json:"requestedAt"`
	// SetupOnly: asked for on the first-run screen, before any account
	// existed. It only runs if there still is none, so nobody can use the
	// moments before the restart to replace an install someone has since
	// set up.
	SetupOnly bool `json:"setupOnly,omitempty"`
}

// ErrAccountExists means a first-run restore was skipped because an account exists.
var ErrAccountExists = errors.New("an account exists now, so the first-run restore was skipped; sign in and restore from System → Backups instead")

// Result is how the last restore went, shown on the Backups page.
type Result struct {
	At      time.Time `json:"at"`
	Backup  string    `json:"backup"`
	OK      bool      `json:"ok"`
	Message string    `json:"message"`
	// Safety is the backup made just before, which undoes the restore.
	Safety string `json:"safety,omitempty"`
}

// StageRestore checks a backup, records it for restoring, and restarts
// Reelkeeper, which restores it before anything else runs (RunPending).
func (s *Service) StageRestore(ctx context.Context, name string) (*Summary, error) {
	sum, err := s.Check(ctx, name)
	if err != nil {
		return nil, err
	}
	p, _ := s.path(name)
	return sum, s.stage(pending{Path: p, RequestedAt: s.now().UTC()})
}

// StageSetupRestore is the first-run screen's "Restore from a backup": it
// saves the uploaded backup in the data folder (replacing any earlier one),
// checks it like any restore, and restarts. The caller must have checked
// that no account exists; the restore checks again when it runs.
func (s *Service) StageSetupRestore(ctx context.Context, r io.Reader) (*Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dataDir, 0o750); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(s.dataDir, ".setup-upload-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	sum, err := Inspect(tmp)
	_ = tmp.Close()
	if err != nil {
		return nil, err
	}
	if err := s.usable(ctx, sum); err != nil {
		return nil, err
	}
	final := filepath.Join(s.dataDir, setupFile)
	if err := os.Rename(tmp.Name(), final); err != nil {
		return nil, err
	}
	return sum, s.stage(pending{Path: final, RequestedAt: s.now().UTC(), SetupOnly: true})
}

// usable checks a backup can be restored here: not newer, same key.
func (s *Service) usable(ctx context.Context, sum *Summary) error {
	current, err := s.db.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if sum.Header.SchemaVersion > current {
		return ErrNewer
	}
	if sum.Header.KeyCheck != "" && !s.keyOpens(sum.Header.KeyCheck) {
		return ErrWrongKey
	}
	return nil
}

// stage records a restore for the next start and restarts Reelkeeper.
func (s *Service) stage(p pending) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.dataDir, pendingFile), b, 0o600); err != nil {
		return fmt.Errorf("record the restore: %w", err)
	}
	s.logger.Warn("restore requested; restarting to restore before anything else runs", "backup", p.Path, "first_run", p.SetupOnly)
	if s.restart != nil {
		go func() {
			time.Sleep(time.Second) // let the answer reach the browser first
			s.restart()
		}()
	}
	return nil
}

// LastResult returns how the last restore went, if one ran.
func (s *Service) LastResult() *Result {
	b, err := os.ReadFile(filepath.Join(s.dataDir, resultFile))
	if err != nil {
		return nil
	}
	var r Result
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

// RunPending restores a backup recorded by StageRestore, if there is one.
// It runs at startup, after migrations and before settings load or any
// task starts. A failed restore leaves the database as it was; either way
// the outcome is saved for the Backups page and the request is cleared,
// so a bad backup can't stop Reelkeeper from starting.
func RunPending(ctx context.Context, db *store.Store, dataDir, app string,
	keyCheck func(ctx context.Context) (string, error), keyOpens func(string) bool, logger *slog.Logger) {
	marker := filepath.Join(dataDir, pendingFile)
	b, err := os.ReadFile(marker) // #nosec G304 -- a fixed name in the data folder
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	_ = os.Remove(marker)
	var p pending
	if err != nil || json.Unmarshal(b, &p) != nil || p.Path == "" {
		logger.Error("a restore was requested but its record can't be read; nothing was restored")
		return
	}
	res := Result{At: time.Now().UTC(), Backup: filepath.Base(p.Path)}
	saveResult := func() {
		if b, err := json.Marshal(res); err == nil {
			_ = os.WriteFile(filepath.Join(dataDir, resultFile), b, 0o600)
		}
	}
	defer func() { saveResult() }()
	safetyDir := filepath.Dir(p.Path)
	if p.SetupOnly {
		defer func() { _ = os.Remove(p.Path) }()
		safetyDir = filepath.Join(dataDir, "backups")
		// The users table is the auth package's; reading its count here
		// keeps this check independent of services that aren't running yet.
		var accounts int
		if err := db.DB.NewRaw("SELECT COUNT(*) FROM users").Scan(ctx, &accounts); err != nil || accounts > 0 {
			if err == nil {
				err = ErrAccountExists
			}
			res.Message = "Nothing was restored: " + plain(err)
			logger.Error("first-run restore skipped", "error", err)
			return
		}
	}

	// If Reelkeeper is killed from here on, the deferred save above never
	// runs and the page would keep showing the previous restore. So record
	// now that this one was interrupted; the real outcome replaces it. The
	// restore is a single transaction, so a kill leaves the old data.
	res.Message = "Reelkeeper stopped during the restore of " + res.Backup + ", so nothing was restored; the database is as it was before."
	saveResult()
	res.Message = ""

	// A safety backup of what's there now, next to the backup being
	// restored (on first run, in the data folder's backups), so the
	// restore can be undone.
	s := &Service{db: db, keyCheck: keyCheck, keyOpens: keyOpens, dataDir: dataDir, app: app, logger: logger,
		now: time.Now, settings: func() settings.Backups { return settings.Backups{Keep: 1 << 30} },
		checked: map[fileKey]checkedFile{}}
	// The safety backup is added to the remembered results, so the first
	// Backups page after this restart doesn't have to read it back.
	s.loadChecks()
	safety, err := s.create(ctx, safetyDir, KindSafety)
	if err != nil {
		res.Message = "Nothing was restored: the safety backup of the current database failed: " + plain(err)
		logger.Error("restore skipped: safety backup failed", "error", err)
		return
	}
	res.Safety = safety.Name

	logger.Warn("restoring a backup", "backup", p.Path)
	sum, err := Restore(ctx, db, p.Path, RestoreOptions{CheckKey: keyOpens, ScratchDir: dataDir})
	if err != nil {
		res.Message = "Nothing was restored: " + plain(err)
		logger.Error("restore failed; the database is unchanged", "backup", p.Path, "error", err)
		return
	}
	res.OK = true
	res.Message = fmt.Sprintf("Restored %s: %d rows, from Reelkeeper %s (%s).", res.Backup, sum.Total,
		firstNonEmpty(sum.Header.App, "unknown version"), sum.Header.CreatedAt.Local().Format("2006-01-02 15:04"))
	logger.Warn("backup restored", "backup", p.Path, "rows", sum.Total, "safety_backup", safety.Name)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// plain words an error for the page: the user-facing ones as they are,
// others with their detail.
func plain(err error) string {
	msg := err.Error()
	if msg == "" {
		return "unknown error"
	}
	return strings.ToUpper(msg[:1]) + msg[1:]
}

// Due reports whether the daily backup should run now: it's past the set
// time today and no scheduled backup was made today (local time).
func (s *Service) Due() bool {
	set := s.settings()
	if !set.Scheduled {
		return false
	}
	now := s.now()
	hm, err := time.Parse("15:04", set.Time)
	if err != nil {
		return false
	}
	at := time.Date(now.Year(), now.Month(), now.Day(), hm.Hour(), hm.Minute(), 0, 0, now.Location())
	if now.Before(at) {
		return false
	}
	entries, err := s.List()
	if err != nil {
		return true // a failure to read the folder shows when the backup runs
	}
	for _, e := range entries {
		if e.Header != nil && e.Header.Kind == KindScheduled && !e.Header.CreatedAt.In(now.Location()).Before(at) {
			return false
		}
	}
	return true
}

// RegisterTasks adds the scheduled backup, checked every 15 minutes.
func (s *Service) RegisterTasks(m *jobs.Manager) {
	m.Register(jobs.Task{
		Kind:        TaskKind,
		Name:        "Back up the database",
		Description: "Makes the daily backup at the time set under System → Backups, and deletes the oldest past the number kept. Run now makes one straight away.",
		Every:       15 * time.Minute,
		Run: func(ctx context.Context, r *jobs.Run) error {
			if r.Job().Trigger != jobs.Manual && !s.Due() {
				r.Quiet()
				return nil
			}
			kind := KindScheduled
			if r.Job().Trigger == jobs.Manual {
				kind = KindManual
			}
			r.Progress(0, "Backing up")
			e, err := s.Create(ctx, kind)
			if err != nil {
				return err
			}
			r.Progress(100, fmt.Sprintf("Made %s (%s)", e.Name, size(e.Size)))
			return nil
		},
	})
}

func size(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}
