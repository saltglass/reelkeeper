package download

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/cleanup"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/settings"
)

// Cleanup carries out internal/cleanup's decisions for Reelkeeper's downloads:
// in dry run it only records what it would do; on, it removes torrents
// from qBittorrent (with their downloaded copy; the library copy is
// separate) and, for stuck downloads, blocklists the release and searches
// for another. Only torrents in Reelkeeper's category are ever touched.

// Cleanup event actions (stored; never rename).
const (
	ActionRemoved     = "removed"
	ActionWouldRemove = "would-remove"
	ActionEmptied     = "emptied"
	ActionWouldEmpty  = "would-empty"
)

// CleanupEvent is one row of Activity → Cleanup.
type CleanupEvent struct {
	bun.BaseModel `bun:"table:cleanup_events"`

	ID           int64     `bun:"id,pk,autoincrement"`
	CreatedAt    time.Time `bun:"created_at"`
	DownloadID   *int64    `bun:"download_id"`
	TitleID      *int64    `bun:"title_id"`
	ReleaseTitle string    `bun:"release_title"`
	Action       string    `bun:"action"`
	Rule         string    `bun:"rule"`
	Message      string    `bun:"message"`
}

// ReplacedEmptier empties old .reelkeeper-replaced folders (library.Service).
type ReplacedEmptier interface {
	EmptyReplaced(ctx context.Context, maxAge time.Duration, dryRun bool) ([]string, error)
}

// SetCleanup connects the cleanup settings, the "search for another
// release" action and the replaced-files emptier.
func (s *Service) SetCleanup(cfg func() settings.Cleanup, searchAgain func(ctx context.Context, versionID int64) error, replaced ReplacedEmptier) {
	s.cleanupCfg, s.searchAgain, s.replaced = cfg, searchAgain, replaced
}

// Cleanup runs the rules over every download. It's the "Clean up
// downloads" task.
func (s *Service) Cleanup(ctx context.Context) (msg string, quiet bool, err error) {
	if s.cleanupCfg == nil {
		return "", false, errors.New("cleanup isn't set up")
	}
	cfg := s.cleanupCfg()
	if cfg.Mode == settings.CleanupOff {
		return "Cleanup is off (Settings → Cleanup).", true, nil
	}
	dry := cfg.Mode == settings.CleanupDryRun
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	var ds []Download
	if err := s.db.DB.NewSelect().Model(&ds).
		Where("status IN (?)", bun.List([]string{StatusDownloading, StatusImported, StatusImportFailed})).
		Order("id").Scan(ctx); err != nil {
		return "", false, err
	}
	var torrents, usenet []Download
	for _, d := range ds {
		if d.Usenet() {
			usenet = append(usenet, d)
		} else {
			torrents = append(torrents, d)
		}
	}
	moving := s.Moving()
	switch {
	case moving && len(usenet) == 0:
		return "Paused while torrents move to another download client.", true, nil
	case len(ds) == 0:
		return "Nothing to clean up.", true, nil
	}
	var parts []string
	var errs []error
	quiet = true
	if len(torrents) > 0 && moving {
		parts = append(parts, "torrents paused while they move to another download client")
	} else if len(torrents) > 0 {
		p, q, err := s.cleanupTorrents(ctx, cfg, dry, torrents)
		if err != nil {
			errs = append(errs, err)
		}
		parts, quiet = append(parts, p...), quiet && q
	}
	if len(usenet) > 0 {
		p, q, err := s.cleanupUsenet(ctx, cfg, dry, usenet)
		if err != nil {
			errs = append(errs, err)
		}
		parts, quiet = append(parts, p...), quiet && q
	}
	if len(errs) > 0 {
		return "", false, errors.Join(errs...)
	}
	return capitalizeFirst(strings.Join(parts, ", ")) + ".", quiet, nil
}

// cleanupTorrents runs the torrent rules: seeding goals, stuck downloads and
// making room on the download disk.
func (s *Service) cleanupTorrents(ctx context.Context, cfg settings.Cleanup, dry bool, ds []Download) (parts []string, quiet bool, err error) {
	c, err := s.client()
	if err != nil {
		return nil, false, err
	}
	ts, err := ourTorrents(ctx, c)
	if err != nil {
		return nil, false, err
	}
	live := map[string]Torrent{}
	for _, t := range ts {
		live[t.Hash] = t
	}
	// Torrents that moved to this client count what the old one had already
	// seeded and uploaded (see counted).
	for i := range ds {
		if t, ok := live[ds[i].Hash]; ok {
			live[ds[i].Hash] = counted(&ds[i], t)
		}
	}

	// Lift qBittorrent's own seeding limits where that hasn't happened yet
	// (downloads from before M5, or a failed attempt at grab time).
	lift := map[string][]string{} // by category
	lifted := 0
	for _, d := range ds {
		if t, ok := live[d.Hash]; ok && !d.LimitsSet && d.Status != StatusImportFailed {
			lift[t.Category] = append(lift[t.Category], d.Hash)
		}
	}
	for _, cat := range settings.Categories {
		hashes := lift[cat]
		if len(hashes) == 0 {
			continue
		}
		if err := c.NoShareLimits(ctx, cat, hashes); err != nil {
			s.logger.Warn("couldn't lift the download client's seeding limits", "client", c.Name(), "category", cat, "error", err)
		} else if _, err := s.db.DB.NewUpdate().Model((*Download)(nil)).Set("limits_set = TRUE").
			Where("hash IN (?)", bun.List(hashes)).Exec(ctx); err != nil {
			return nil, false, err
		} else {
			lifted += len(hashes)
		}
	}

	now := s.now().UTC()
	changed := lifted > 0 // lifting seeding limits counts as doing something
	acted, would, kept := 0, 0, 0
	handled := map[int64]bool{} // removed (or marked) by a rule this run
	for i := range ds {
		d := &ds[i]
		t, ok := live[d.Hash]
		if !ok {
			// A download in progress is marked removed by Check. An imported
			// one whose torrent was removed in the download client by hand
			// would otherwise sit under Seeding forever.
			if d.Status == StatusImported {
				s.finish(ctx, d, StatusRemoved, "Its torrent was removed from "+c.Name()+" outside Reelkeeper after it was imported.", grab.EventRemoved)
				changed = true
			}
			continue
		}
		obs := cleanup.Torrent{State: t.State, ClientName: c.Name(), Done: t.Done(), Left: t.Left, Speed: t.Speed, Ratio: t.Ratio, SeedingTime: t.SeedingTime}
		if cleanup.LooksStuck(obs) && (cfg.Unregistered) {
			if msgs, err := c.TrackerMessages(ctx, t.Category, d.Hash); err == nil {
				obs.Unregistered = cleanup.Unregistered(msgs)
			}
		}
		var tr cleanup.Track
		_ = json.Unmarshal([]byte(d.CleanupTrack), &tr)
		v, next := cleanup.Evaluate(cleanup.Download{
			Imported: d.Status == StatusImported, ImportFail: d.Status == StatusImportFailed, CompletedAt: d.CompletedAt,
			SeedRatio: d.SeedRatio, SeedTimeMinutes: d.SeedTimeMinutes,
		}, obs, tr, cfg, now)
		if b, err := json.Marshal(next); err == nil && string(b) != d.CleanupTrack {
			if _, err := s.db.DB.NewUpdate().Model(d).Set("cleanup_track = ?", string(b)).WherePK().Exec(ctx); err != nil {
				return nil, false, err
			}
		}
		if v.Action == cleanup.Keep {
			kept++
			continue
		}
		if dry {
			recorded, err := s.recordOnce(ctx, d, ActionWouldRemove, v)
			if err != nil {
				return nil, false, err
			}
			if recorded {
				would++
			}
			handled[d.ID] = true
			continue
		}
		if err := s.act(ctx, c, d, v); err != nil {
			s.logger.Error("cleanup couldn't remove a download", "release", d.ReleaseTitle, "error", err)
			continue
		}
		handled[d.ID] = true
		acted++
	}

	// Making room: when the download disk has less free than Settings →
	// Cleanup keeps free, imported torrents whose tracker minimum is met go
	// early, longest finished first (cleanup.MakeRoom).
	if free := s.downloadFree(); free >= 0 {
		var items []cleanup.Seeding
		var which []*Download
		for i := range ds {
			d := &ds[i]
			t, ok := live[d.Hash]
			if !ok || d.Status != StatusImported || handled[d.ID] {
				continue
			}
			items = append(items, cleanup.Seeding{
				D: cleanup.Download{Imported: true, CompletedAt: d.CompletedAt, SeedRatio: d.SeedRatio, SeedTimeMinutes: d.SeedTimeMinutes},
				T: cleanup.Torrent{Done: t.Done(), Ratio: t.Ratio, SeedingTime: t.SeedingTime}, Size: t.Size,
			})
			which = append(which, d)
		}
		picked, why := cleanup.MakeRoom(items, free, s.keepFree(), now)
		for n, i := range picked {
			d, v := which[i], why[n]
			if dry {
				recorded, err := s.recordOnce(ctx, d, ActionWouldRemove, v)
				if err != nil {
					return nil, false, err
				}
				if recorded {
					would++
				}
				continue
			}
			if err := s.act(ctx, c, d, v); err != nil {
				s.logger.Error("cleanup couldn't remove a download to free space", "release", d.ReleaseTitle, "error", err)
				continue
			}
			acted++
		}
		if len(picked) > 0 {
			s.logger.Warn("the download disk is low on space; removing finished torrents early",
				"free", cleanup.Size(free), "keep_free", cleanup.Size(s.keepFree()), "picked", len(picked), "dry_run", dry)
		}
	}

	if dry {
		parts = append(parts, fmt.Sprintf("Dry run: %d newly marked \"would remove\"", would))
	} else {
		parts = append(parts, fmt.Sprintf("%d removed", acted))
	}
	parts = append(parts, fmt.Sprintf("%d kept", kept))
	// Quiet: nothing removed, nothing newly marked, no limits lifted.
	return parts, !changed && acted == 0 && would == 0, nil
}

// cleanupUsenet runs the Usenet rules (cleanup.EvaluateUsenet): an imported
// download's copy goes from the Usenet client at once, and a job the client
// gave up on is removed, blocklisted and searched again. Dry run only
// records what it would do, as for torrents.
func (s *Service) cleanupUsenet(ctx context.Context, cfg settings.Cleanup, dry bool, ds []Download) (parts []string, quiet bool, err error) {
	c, err := s.usenetClient()
	if err != nil {
		return nil, false, err
	}
	jobs, err := ourTorrents(ctx, c)
	if err != nil {
		return nil, false, err
	}
	live := map[string]Torrent{}
	for _, t := range jobs {
		live[t.Hash] = t
	}
	now := s.now().UTC()
	changed, acted, would, kept := false, 0, 0, 0
	for i := range ds {
		d := &ds[i]
		t, ok := live[d.Hash]
		if !ok {
			// As for torrents: Check marks a download in progress removed; an
			// imported one whose job was removed in the client by hand is done.
			if d.Status == StatusImported {
				s.finish(ctx, d, StatusSeeded, "Its job was removed from "+c.Name()+" outside Reelkeeper after it was imported.", grab.EventRemoved)
				changed = true
			}
			continue
		}
		v := cleanup.EvaluateUsenet(cleanup.Download{
			Imported: d.Status == StatusImported, ImportFail: d.Status == StatusImportFailed, CompletedAt: d.CompletedAt,
		}, cleanup.Torrent{State: t.State, ClientName: c.Name(), Done: t.Done(), Left: t.Left, Message: t.Message}, cfg, now)
		if v.Action == cleanup.Keep {
			kept++
			continue
		}
		if dry {
			recorded, err := s.recordOnce(ctx, d, ActionWouldRemove, v)
			if err != nil {
				return nil, false, err
			}
			if recorded {
				would++
			}
			continue
		}
		if err := s.act(ctx, c, d, v); err != nil {
			s.logger.Error("cleanup couldn't remove a Usenet download", "release", d.ReleaseTitle, "error", err)
			continue
		}
		acted++
	}
	if dry {
		parts = append(parts, fmt.Sprintf("Usenet dry run: %d newly marked \"would remove\"", would))
	} else {
		parts = append(parts, fmt.Sprintf("%d removed from %s", acted, c.Name()))
	}
	parts = append(parts, fmt.Sprintf("%d kept in %s", kept, c.Name()))
	return parts, !changed && acted == 0 && would == 0, nil
}

// act removes a download, and for a stuck one blocklists it and searches
// for another.
func (s *Service) act(ctx context.Context, c Client, d *Download, v cleanup.Verdict) error {
	if err := c.Remove(ctx, d.Category, []string{d.Hash}, true); err != nil {
		return err
	}
	msg := "Cleanup removed it: " + v.Reason
	if v.Action == cleanup.RemoveStuck {
		if d.TitleID != nil {
			if err := s.AddToBlocklist(ctx, BlockedRelease{TitleID: *d.TitleID, ReleaseTitle: d.ReleaseTitle, Hash: d.Hash,
				Indexer: d.Indexer, Quality: d.Quality, Reason: "Cleanup: " + v.Reason,
				Rule: v.Rule, Temporary: tempRules[v.Rule]}); err != nil {
				return err
			}
		}
		if d.VersionID != nil && s.searchAgain != nil {
			if err := s.searchAgain(ctx, *d.VersionID); err != nil {
				s.logger.Warn("couldn't queue a search after cleanup", "error", err)
			}
		}
		msg += " Blocklisted; searching for another release."
		s.tell(ctx, notify.Event{Kind: notify.KindDownloadRemoved, Title: s.movieName(d), TitleID: ptrVal(d.TitleID),
			Message: fmt.Sprintf("Cleanup removed %s: %s Reelkeeper blocklisted it and is searching for another release.", d.ReleaseTitle, v.Reason)})
	}
	// An imported movie stays in the library: only its torrent is gone, so
	// the record becomes "seeded" rather than "removed".
	status := StatusRemoved
	if d.Status == StatusImported {
		status = StatusSeeded
	}
	s.finish(ctx, d, status, msg, grab.EventRemoved)
	_, err := s.recordOnce(ctx, d, ActionRemoved, v)
	return err
}

// recordOnce adds a cleanup event unless the same download already has
// one for the same rule and action, so dry run doesn't repeat itself every
// few minutes.
func (s *Service) recordOnce(ctx context.Context, d *Download, action string, v cleanup.Verdict) (bool, error) {
	exists, err := s.db.DB.NewSelect().Model((*CleanupEvent)(nil)).
		Where("download_id = ? AND rule = ? AND action = ?", d.ID, v.Rule, action).Exists(ctx)
	if err != nil || exists {
		return false, err
	}
	id := d.ID
	_, err = s.db.DB.NewInsert().Model(&CleanupEvent{
		CreatedAt: s.now().UTC(), DownloadID: &id, TitleID: d.TitleID, ReleaseTitle: d.ReleaseTitle,
		Action: action, Rule: v.Rule, Message: v.Reason,
	}).Exec(ctx)
	return err == nil, err
}

// EmptyReplacedFiles empties old .reelkeeper-replaced folders as the settings
// say. It's the daily "Empty replaced files" task.
func (s *Service) EmptyReplacedFiles(ctx context.Context) (string, error) {
	cfg := s.cleanupCfg()
	if cfg.Mode == settings.CleanupOff || cfg.EmptyReplacedDays == 0 || s.replaced == nil {
		return "Emptying replaced files is off.", nil
	}
	dry := cfg.Mode == settings.CleanupDryRun
	paths, err := s.replaced.EmptyReplaced(ctx, time.Duration(cfg.EmptyReplacedDays)*24*time.Hour, dry)
	action := ActionEmptied
	if dry {
		action = ActionWouldEmpty
	}
	for _, p := range paths {
		msg := fmt.Sprintf("Files replaced by upgrades more than %d days ago: %s", cfg.EmptyReplacedDays, p)
		exists, qerr := s.db.DB.NewSelect().Model((*CleanupEvent)(nil)).
			Where("action = ? AND message = ?", action, msg).Exists(ctx)
		if qerr != nil {
			return "", qerr
		}
		if !exists {
			if _, ierr := s.db.DB.NewInsert().Model(&CleanupEvent{CreatedAt: s.now().UTC(), Action: action,
				Rule: "replaced-files", Message: msg}).Exec(ctx); ierr != nil {
				return "", ierr
			}
		}
	}
	if err != nil {
		return "", err
	}
	if dry {
		return fmt.Sprintf("Dry run: would empty %d folders.", len(paths)), nil
	}
	return fmt.Sprintf("Emptied %d folders.", len(paths)), nil
}

// CleanupEvents lists what cleanup did, newest first.
func (s *Service) CleanupEvents(ctx context.Context, before int64, limit int) ([]CleanupEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []CleanupEvent
	q := s.db.DB.NewSelect().Model(&out).Order("id DESC").Limit(int64(limit))
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	return out, q.Scan(ctx)
}

// Orphans are things in Reelkeeper's corner of the download client that Reelkeeper
// doesn't track. They're reported, never removed on their own.
type Orphans struct {
	// Torrents in Reelkeeper's category it has no record of.
	Torrents []Torrent
	// Files are entries in Reelkeeper's download folder that no torrent in its
	// category owns (only when a folder of its own is set).
	Files []string
	// Folder is that download folder ("" when none is set).
	Folder string
}

// FindOrphans looks for orphans.
func (s *Service) FindOrphans(ctx context.Context) (Orphans, error) {
	out := Orphans{Folder: s.settings().Folder}
	c, err := s.client()
	if err != nil {
		return out, err
	}
	ts, err := ourTorrents(ctx, c)
	if err != nil {
		return out, err
	}
	var hashes []string
	if err := s.db.DB.NewSelect().Model((*Download)(nil)).Column("hash").
		Where("status IN (?)", bun.List([]string{StatusDownloading, StatusImported, StatusImportFailed})).
		Scan(ctx, &hashes); err != nil {
		return out, err
	}
	tracked := map[string]bool{}
	for _, h := range hashes {
		tracked[h] = true
	}
	owned := map[string]bool{}
	for _, t := range ts {
		if !tracked[t.Hash] {
			out.Torrents = append(out.Torrents, t)
		}
		owned[topLevel(out.Folder, t.ContentPath)] = true
	}
	if out.Folder == "" {
		return out, nil
	}
	entries, err := os.ReadDir(out.Folder)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, fmt.Errorf("can't read the download folder %s: %w", out.Folder, err)
	}
	for _, e := range entries {
		if !owned[e.Name()] {
			out.Files = append(out.Files, e.Name())
		}
	}
	return out, nil
}

// topLevel is the first path element of p under folder ("" if outside).
func topLevel(folder, p string) string {
	if folder == "" {
		return ""
	}
	rel, err := filepath.Rel(folder, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return strings.Split(filepath.ToSlash(rel), "/")[0]
}

// RemoveOrphanTorrent removes an orphan torrent (and what it downloaded)
// when the user asks. Tracked downloads are refused: use the Queue.
func (s *Service) RemoveOrphanTorrent(ctx context.Context, hash string) error {
	hash = strings.ToLower(hash)
	tracked, err := s.db.DB.NewSelect().Model((*Download)(nil)).
		Where("hash = ? AND status IN (?)", hash, bun.List([]string{StatusDownloading, StatusImported, StatusImportFailed})).
		Exists(ctx)
	if err != nil {
		return err
	}
	if tracked {
		return errors.New("reelkeeper tracks this download; remove it from Activity → Queue instead")
	}
	c, err := s.client()
	if err != nil {
		return err
	}
	// Whichever of Reelkeeper's categories it's in (Remove checks again).
	ts, err := ourTorrents(ctx, c)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(ts, func(t Torrent) bool { return strings.EqualFold(t.Hash, hash) })
	if i < 0 {
		return errors.New("that torrent isn't in Reelkeeper's categories")
	}
	return c.Remove(ctx, ts[i].Category, []string{hash}, true)
}

// DeleteStrayFile deletes an entry in Reelkeeper's download folder that no
// torrent owns, when the user asks.
func (s *Service) DeleteStrayFile(ctx context.Context, name string) error {
	o, err := s.FindOrphans(ctx)
	if err != nil {
		return err
	}
	if o.Folder == "" || name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return errors.New("not a stray file in Reelkeeper's download folder")
	}
	found := false
	for _, f := range o.Files {
		found = found || f == name
	}
	if !found {
		return errors.New("that file isn't stray (a torrent owns it) or is gone")
	}
	p := filepath.Join(o.Folder, name)
	if err := os.RemoveAll(p); err != nil {
		return err
	}
	s.logger.Info("deleted stray download", "path", p, "reason", "asked from Activity → Cleanup")
	return nil
}

// SeedingItem is an imported download still in the download client, with
// what keeps it there.
type SeedingItem struct {
	Download Download
	Torrent  *Torrent // nil if the download client doesn't have it
	// Waiting says, in plain words, what has to happen before cleanup
	// removes it ("" once nothing does, or when cleanup is off).
	Waiting string
}

// Seeding lists imported downloads still seeding.
func (s *Service) Seeding(ctx context.Context) ([]SeedingItem, error) {
	var ds []Download
	if err := s.db.DB.NewSelect().Model(&ds).Where("status = ?", StatusImported).
		Where("protocol <> ?", indexer.ProtocolUsenet).Order("id").Scan(ctx); err != nil {
		return nil, err
	}
	if len(ds) == 0 {
		return nil, nil
	}
	live := map[string]Torrent{}
	if c, err := s.client(); err == nil {
		if ts, err := ourTorrents(ctx, c); err == nil {
			for _, t := range ts {
				live[t.Hash] = t
			}
		}
	}
	for i := range ds {
		if t, ok := live[ds[i].Hash]; ok {
			live[ds[i].Hash] = counted(&ds[i], t)
		}
	}
	cfg := settings.DefaultCleanup()
	if s.cleanupCfg != nil {
		cfg = s.cleanupCfg()
	}
	out := make([]SeedingItem, 0, len(ds))
	for _, d := range ds {
		it := SeedingItem{Download: d}
		if t, ok := live[d.Hash]; ok {
			it.Torrent = &t
			it.Waiting = cleanup.Waiting(cleanup.Download{Imported: true, SeedRatio: d.SeedRatio, SeedTimeMinutes: d.SeedTimeMinutes, CompletedAt: d.CompletedAt},
				cleanup.Torrent{Done: true, Ratio: t.Ratio, SeedingTime: t.SeedingTime}, cfg, s.now().UTC())
		}
		out = append(out, it)
	}
	return out, nil
}

// counted is a torrent as cleanup should see it. A torrent that moved to
// another download client starts from nothing there: no client lets anyone
// set its seeding time or upload counters. Reelkeeper stored what the old
// client had (seconds seeding, bytes uploaded) and adds it to what this one
// reports, so the seeding goal and the tracker's minimum, in time and in
// ratio, neither start over nor get cut short.
//
// The ratio is worked out from the bytes uploaded over the torrent's size
// (a client's own ratio divides by what it downloaded, which for a torrent
// that arrived finished is nothing).
func counted(d *Download, t Torrent) Torrent {
	if d.SeedSecondsOffset == 0 && d.UploadedOffset == 0 && d.MovedAt == nil {
		return t
	}
	t.SeedingTime += time.Duration(d.SeedSecondsOffset) * time.Second
	if t.Size > 0 {
		t.Ratio = float64(t.Uploaded+d.UploadedOffset) / float64(t.Size)
	}
	return t
}
