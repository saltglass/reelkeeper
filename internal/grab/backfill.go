package grab

// The backfill queue (2026-10-12): filling in a show's older missing
// episodes used to mean turning its seasons on by hand, waiting until
// they were done, then picking the next show. The queue does the waiting
// and picking. Shows are lined up; one at a time, Reelkeeper turns on every
// aired episode the show has no file for, searches (season packs for
// finished seasons, as ever), waits while anything is downloading, and
// searches again. When a search finds nothing acceptable and nothing is on
// its way, the show has everything it can find: it's finished and the next
// one starts. Leftovers stay wanted, so RSS and the scheduled pass go on
// looking, and the show is listed under "Needs a decision", where the user
// can see the best releases the profile turned down and take one anyway.
//
// It is deliberately small: no settings, no pause switch, no emails. A
// show is in the queue once; one show is worked on at a time; at most
// BackfillGrabsPerDay grabs a day go to it.
//
// The states of a row (table backfill) are:
//
//	queued    waiting for its turn
//	active    being worked on (at most one row)
//	decision  finished with episodes still missing
//	done      finished with everything
//
// The Backfill task (BackfillRun) moves a row along every 15 minutes.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// Backfill states (stored; never rename one that has shipped).
const (
	BackfillQueued   = "queued"
	BackfillActive   = "active"
	BackfillDecision = "decision"
	BackfillDone     = "done"
)

// BackfillGrabsPerDay is how many grabs a day go to the show being
// backfilled (2026-10-12; a season pack counts once). It is a constant
// on purpose: it needs no setting. Grabs also count toward the
// queue limit too: they take places in the queue.
const BackfillGrabsPerDay = 5

// backfillQuotaText is why a grab is held back once the quota is used.
var backfillQuotaText = fmt.Sprintf("Backfill's %d grabs for today are used; it continues tomorrow.", BackfillGrabsPerDay)

// maxShowsPerRun bounds how many shows one run of the task may finish and
// start in a row (a queue of shows with nothing missing).
const maxShowsPerRun = 50

// optionsKept is how long the choices shown under "Show options" can still
// be taken.
const optionsKept = 15 * time.Minute

// optionsPerUnit is how many releases "Show options" offers for each
// season or episode.
const optionsPerUnit = 3

// Errors the API turns into answers.
var (
	// ErrBackfillNotFound: no such row in the queue.
	ErrBackfillNotFound = errors.New("no such backfill row")
	// ErrBackfillQueued: the show is already waiting in the queue or being
	// worked on.
	ErrBackfillQueued = errors.New("already in the backfill queue")
	// ErrOptionGone: the option was never offered, or was offered too long
	// ago.
	ErrOptionGone = errors.New("that option has expired")
)

// BackfillRefusal is a reason a release picked by the user can't be
// grabbed (the same ones that stop an automatic grab).
type BackfillRefusal string

func (r BackfillRefusal) Error() string { return string(r) }

// BackfillRow is one show in the queue.
type BackfillRow struct {
	bun.BaseModel `bun:"table:backfill"`

	ID         int64      `bun:"id,pk,autoincrement"`
	VersionID  int64      `bun:"version_id"`
	Position   int        `bun:"position"`
	State      string     `bun:"state"`
	AddedAt    time.Time  `bun:"added_at"`
	StartedAt  *time.Time `bun:"started_at"`
	FinishedAt *time.Time `bun:"finished_at"`
	// Found and Missing are the counts when it finished.
	Found   int  `bun:"found"`
	Missing int  `bun:"missing"`
	Hidden  bool `bun:"hidden"`
}

// BackfillShow is a row with its show and how far it has got.
type BackfillShow struct {
	ID         int64
	VersionID  int64
	TitleID    int64
	Title      string
	Year       int
	PosterPath string
	State      string
	// Found is how many of the show's aired episodes (specials left out)
	// have a file; Missing how many it still wants; Downloading how many of
	// those are on their way.
	Found, Missing, Downloading int
	StartedAt, FinishedAt       *time.Time
}

// BackfillList is the Backfill tab.
type BackfillList struct {
	// Now is the show being worked on (nil: none).
	Now *BackfillShow
	// Next are the shows waiting, in order.
	Next []BackfillShow
	// Decision are finished shows with episodes still missing, newest
	// first, less those hidden.
	Decision []BackfillShow
	// GrabsToday is how many grabs today's show has had; GrabsPerDay the
	// most it gets.
	GrabsToday, GrabsPerDay int
}

// episodeCounts sorts a version's episodes the way backfill sees them: the
// aired ones (before today, as searches use; specials left out), which have
// a file, and which are wanted now (monitored, no file). The last two are
// what the task finishes on; turnOn are all aired ones without a file.
func (s *Service) episodeCounts(sv library.SeriesVersion) (have int, turnOn, wanted []library.Episode) {
	today := s.localToday()
	aired := func(e library.Episode) bool {
		return *e.Season != 0 && e.AirDate != "" && e.AirDate < today
	}
	for _, e := range sv.Episodes {
		switch {
		case !aired(e):
		case sv.HasFile[e.ID]:
			have++
		default:
			turnOn = append(turnOn, e)
		}
	}
	for _, e := range sv.Wanted {
		if aired(e) {
			wanted = append(wanted, e)
		}
	}
	return have, turnOn, wanted
}

func (s *Service) backfillRow(ctx context.Context, id int64) (*BackfillRow, error) {
	row := new(BackfillRow)
	err := s.history.db.DB.NewSelect().Model(row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBackfillNotFound
	}
	return row, err
}

func (s *Service) backfillActive(ctx context.Context) (*BackfillRow, error) {
	row := new(BackfillRow)
	err := s.history.db.DB.NewSelect().Model(row).Where("state = ?", BackfillActive).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return row, err
}

func (s *Service) backfillEvent(sv library.SeriesVersion, msg string) *Event {
	tid, vid := sv.Title.ID, sv.Version.ID
	return &Event{TitleID: &tid, VersionID: &vid, Event: EventBackfill, Message: msg}
}

// BackfillAdd puts a series version at the end of the queue; if nothing is
// being worked on, it starts at once (the Backfill task does the first
// search: the caller should run it now). A show that finished earlier can
// be added again.
func (s *Service) BackfillAdd(ctx context.Context, versionID int64) (*BackfillRow, error) {
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	svs, err := s.lib.SeriesVersions(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if len(svs) == 0 {
		return nil, ErrBackfillNotFound
	}
	sv := svs[0]
	if !sv.Version.Monitored {
		return nil, validation.Errors{"versionId": "Turn Monitored on for this version first."}
	}
	if _, turnOn, _ := s.episodeCounts(sv); len(turnOn) == 0 {
		return nil, validation.Errors{"versionId": "There are no aired episodes missing."}
	}
	db := s.history.db.DB
	var last sql.NullInt64
	if err := db.NewSelect().Model((*BackfillRow)(nil)).ColumnExpr("MAX(position)").Scan(ctx, &last); err != nil {
		return nil, err
	}
	row := &BackfillRow{VersionID: versionID, Position: int(last.Int64) + 1, State: BackfillQueued, AddedAt: s.now().UTC()}
	var old BackfillRow
	switch err := db.NewSelect().Model(&old).Where("version_id = ?", versionID).Scan(ctx); {
	case err == nil && (old.State == BackfillQueued || old.State == BackfillActive):
		return nil, ErrBackfillQueued
	case err == nil:
		// Finished before: it goes to the back of the line again.
		row.ID = old.ID
		if _, err := db.NewUpdate().Model(row).Column("position", "state", "added_at", "started_at", "finished_at", "found", "missing", "hidden").
			Where("id = ?", old.ID).Exec(ctx); err != nil {
			return nil, err
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err := db.NewInsert().Model(row).Exec(ctx); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	if _, err := s.backfillAdvanceLocked(ctx); err != nil {
		return nil, err
	}
	return s.backfillRow(ctx, row.ID)
}

// backfillAdvance starts the next show if none is being worked on.
func (s *Service) backfillAdvance(ctx context.Context) (*BackfillRow, error) {
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	return s.backfillAdvanceLocked(ctx)
}

func (s *Service) backfillAdvanceLocked(ctx context.Context) (*BackfillRow, error) {
	for {
		if active, err := s.backfillActive(ctx); err != nil || active != nil {
			return nil, err
		}
		next := new(BackfillRow)
		err := s.history.db.DB.NewSelect().Model(next).Where("state = ?", BackfillQueued).Order("position", "id").Limit(1).Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		ok, err := s.backfillStart(ctx, next)
		if err != nil || ok {
			return next, err
		}
	}
}

// backfillStart makes a queued show the active one: every aired episode it
// has no file for is turned on, through the same per-episode choices as
// turning them on by hand (so they show on the series page and survive the
// version's "new episodes only" rule). It returns false, having dropped the
// row, when the show is gone.
func (s *Service) backfillStart(ctx context.Context, row *BackfillRow) (bool, error) {
	svs, err := s.lib.SeriesVersions(ctx, row.VersionID)
	if err != nil {
		return false, err
	}
	if len(svs) == 0 {
		_, err := s.history.db.DB.NewDelete().Model(row).WherePK().Exec(ctx)
		return false, err
	}
	sv := svs[0]
	_, turnOn, _ := s.episodeCounts(sv)
	ids := make([]int64, len(turnOn))
	for i, e := range turnOn {
		ids[i] = e.ID
	}
	if err := s.lib.SetEpisodesMonitored(ctx, row.VersionID, ids, true); err != nil {
		return false, err
	}
	now := s.now().UTC()
	row.State, row.StartedAt = BackfillActive, &now
	if _, err := s.history.db.DB.NewUpdate().Model(row).Column("state", "started_at").WherePK().Exec(ctx); err != nil {
		return false, err
	}
	msg := fmt.Sprintf("Backfill started %s: %s turned on.", sv.Title.Title, countText(len(ids), "missing episode"))
	return true, s.history.Add(ctx, s.backfillEvent(sv, msg), EventData{EpisodeIDs: ids})
}

// BackfillRemove takes a show out of the queue (or off the Needs a decision
// list). Its episodes stay as they are. Removing the active show starts the
// next.
func (s *Service) BackfillRemove(ctx context.Context, id int64) error {
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	row, err := s.backfillRow(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.history.db.DB.NewDelete().Model(row).WherePK().Exec(ctx); err != nil {
		return err
	}
	if row.State == BackfillActive {
		_, err = s.backfillAdvanceLocked(ctx)
	}
	return err
}

// BackfillMove swaps a waiting show with the one above ("up") or below
// ("down") it in the queue.
func (s *Service) BackfillMove(ctx context.Context, id int64, direction string) error {
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	row, err := s.backfillRow(ctx, id)
	if err != nil {
		return err
	}
	if row.State != BackfillQueued {
		return validation.Errors{"id": "Only a show waiting in line can be moved."}
	}
	var queued []BackfillRow
	if err := s.history.db.DB.NewSelect().Model(&queued).Where("state = ?", BackfillQueued).Order("position", "id").Scan(ctx); err != nil {
		return err
	}
	i := slices.IndexFunc(queued, func(r BackfillRow) bool { return r.ID == id })
	j := i - 1
	if direction == "down" {
		j = i + 1
	}
	if i < 0 || j < 0 || j >= len(queued) {
		return nil // already first or last
	}
	// Positions are handed out in order, so swapping two rows' keeps the
	// rest as they are.
	a, b := queued[i], queued[j]
	return s.history.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for _, r := range []BackfillRow{{ID: a.ID, Position: b.Position}, {ID: b.ID, Position: a.Position}} {
			if _, err := tx.NewUpdate().Model(&r).Column("position").WherePK().Exec(ctx); err != nil {
				return err
			}
		}
		return nil
	})
}

// BackfillHide takes a finished show off the Needs a decision list. Its
// missing episodes stay wanted.
func (s *Service) BackfillHide(ctx context.Context, id int64) error {
	row, err := s.backfillRow(ctx, id)
	if err != nil {
		return err
	}
	if row.State != BackfillDecision {
		return validation.Errors{"id": "Only a finished show with episodes still missing can be hidden."}
	}
	row.Hidden = true
	_, err = s.history.db.DB.NewUpdate().Model(row).Column("hidden").WherePK().Exec(ctx)
	return err
}

// BackfillListAll builds the Backfill tab.
func (s *Service) BackfillListAll(ctx context.Context) (*BackfillList, error) {
	var rows []BackfillRow
	if err := s.history.db.DB.NewSelect().Model(&rows).Where("state <> ?", BackfillDone).Order("position", "id").Scan(ctx); err != nil {
		return nil, err
	}
	var downloading map[int64]map[int64]bool
	if s.queue != nil {
		var err error
		if downloading, err = s.queue.ActiveEpisodes(ctx); err != nil {
			return nil, err
		}
	}
	out := &BackfillList{Next: []BackfillShow{}, Decision: []BackfillShow{}, GrabsPerDay: BackfillGrabsPerDay}
	for _, r := range rows {
		if r.State == BackfillDecision && r.Hidden {
			continue
		}
		svs, err := s.lib.SeriesVersions(ctx, r.VersionID)
		if err != nil {
			return nil, err
		}
		if len(svs) == 0 {
			continue
		}
		sv := svs[0]
		have, _, wanted := s.episodeCounts(sv)
		show := BackfillShow{
			ID: r.ID, VersionID: r.VersionID, TitleID: sv.Title.ID, Title: sv.Title.Title, Year: sv.Title.Year,
			PosterPath: sv.Title.PosterPath, State: r.State, Found: have, Missing: len(wanted),
			Downloading: len(downloading[r.VersionID]), StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		}
		switch r.State {
		case BackfillActive:
			out.Now = &show
			if out.GrabsToday, err = s.history.countSinceFor(ctx, EventGrabbed, r.VersionID, s.dayStart()); err != nil {
				return nil, err
			}
		case BackfillQueued:
			out.Next = append(out.Next, show)
		case BackfillDecision:
			out.Decision = append(out.Decision, show)
		}
	}
	slices.SortStableFunc(out.Decision, func(a, b BackfillShow) int {
		if a.FinishedAt == nil || b.FinishedAt == nil {
			return 0
		}
		return b.FinishedAt.Compare(*a.FinishedAt)
	})
	return out, nil
}

// backfillQuotaReached reports whether the version is the show being
// backfilled and has had its day's grabs already.
func (s *Service) backfillQuotaReached(ctx context.Context, versionID int64) (bool, error) {
	active, err := s.history.db.DB.NewSelect().Model((*BackfillRow)(nil)).
		Where("version_id = ? AND state = ?", versionID, BackfillActive).Exists(ctx)
	if err != nil || !active {
		return false, err
	}
	n, err := s.history.countSinceFor(ctx, EventGrabbed, versionID, s.dayStart())
	return n >= BackfillGrabsPerDay, err
}

// grabLimited reports whether a grab for the version would be held back
// by the queue limit or backfill's quota.
func (s *Service) grabLimited(ctx context.Context, mode string, versionID int64) (bool, error) {
	if full, err := s.queueFull(ctx, mode); err != nil || full {
		return full, err
	}
	return s.backfillQuotaReached(ctx, versionID)
}

// diskChecker is implemented by a queue that can tell the download disk is
// already short of space (download.Service does).
type diskChecker interface {
	DiskShort(ctx context.Context) (string, error)
}

// backfillStep says what a run did for the active show.
type backfillStep int

const (
	stepWaiting  backfillStep = iota // nothing to do yet
	stepSearched                     // searched; results are in History
	stepFinished                     // the show is finished; the next may start
)

// BackfillRun is the Backfill task: it moves the active show along (see
// the file's comment), starting the next when one finishes. quiet is true
// when it did nothing worth listing.
func (s *Service) BackfillRun(ctx context.Context) (msg string, quiet bool, err error) {
	changed := false
	if n, err := s.backfillCloseDecisions(ctx); err != nil {
		return "", false, err
	} else if n > 0 {
		changed = true
	}
	var notes []string
	for range maxShowsPerRun {
		row, err := s.backfillActive(ctx)
		if err != nil {
			return "", false, err
		}
		if row == nil {
			if row, err = s.backfillAdvance(ctx); err != nil {
				return "", false, err
			}
			if row == nil {
				if !changed && len(notes) == 0 {
					return "Nothing is waiting in the backfill queue.", true, nil
				}
				return strings.Join(append(notes, "The backfill queue is empty."), " "), false, nil
			}
			changed = true
		}
		step, note, err := s.backfillStep(ctx, row)
		if err != nil {
			return "", false, err
		}
		if note != "" {
			notes = append(notes, note)
		}
		switch step {
		case stepSearched:
			return strings.Join(notes, " "), false, nil
		case stepWaiting:
			return strings.Join(notes, " "), !changed, nil
		}
		changed = true // finished: start the next
	}
	return strings.Join(notes, " "), false, nil
}

// backfillStep does one pass over the active show.
func (s *Service) backfillStep(ctx context.Context, row *BackfillRow) (backfillStep, string, error) {
	svs, err := s.lib.SeriesVersions(ctx, row.VersionID)
	if err != nil {
		return stepWaiting, "", err
	}
	if len(svs) == 0 {
		_, err := s.history.db.DB.NewDelete().Model(row).WherePK().Exec(ctx)
		return stepFinished, "A show in the queue was removed from the library.", err
	}
	sv := svs[0]
	name := sv.Title.Title

	// 1. Something on its way: nothing to do until it has been imported.
	if s.queue != nil {
		active, err := s.queue.ActiveVersions(ctx)
		if err != nil {
			return stepWaiting, "", err
		}
		if active[row.VersionID] {
			return stepWaiting, fmt.Sprintf("%s: waiting for downloads to finish.", name), nil
		}
	}
	// 2. Nothing left to want: finished with everything.
	have, _, wanted := s.episodeCounts(sv)
	if len(wanted) == 0 {
		return stepFinished, "", s.backfillFinish(ctx, row, sv, have, 0)
	}
	// 3. The day's grabs for backfill are used up.
	used, err := s.history.countSinceFor(ctx, EventGrabbed, row.VersionID, s.dayStart())
	if err != nil {
		return stepWaiting, "", err
	}
	if used >= BackfillGrabsPerDay {
		return stepWaiting, fmt.Sprintf("%s: today's %d backfill grabs are used; it continues tomorrow.", name, BackfillGrabsPerDay), nil
	}
	// 4. Nothing could be grabbed anyway: grabbing is off or a dry run, the
	// queue is full, or the download disk is short of
	// space. Searching now would only use the indexers' limits.
	g := s.settings()
	if g.TVMode != settings.GrabOn {
		return stepWaiting, fmt.Sprintf("%s: waiting; TV grabbing isn't On (Settings → Download Client).", name), nil
	}
	if s.moving() {
		return stepWaiting, fmt.Sprintf("%s: waiting; torrents are moving to another download client.", name), nil
	}
	if full, err := s.queueFull(ctx, g.TVMode); err != nil {
		return stepWaiting, "", err
	} else if full {
		return stepWaiting, fmt.Sprintf("%s: waiting; the queue is full.", name), nil
	}
	if why := s.diskShort(ctx); why != "" {
		return stepWaiting, fmt.Sprintf("%s: waiting; %s", name, why), nil
	}
	// 5. Search for everything it wants.
	since := row.AddedAt
	if row.StartedAt != nil {
		since = *row.StartedAt
	}
	res, err := s.searchSeries(ctx, row.VersionID, TriggerBackfill, since)
	if err != nil {
		return stepWaiting, "", err
	}
	switch {
	case res.Grabbed > 0:
		return stepSearched, fmt.Sprintf("%s: grabbed %s.", name, countText(res.Grabbed, "release")), nil
	case res.Held > 0:
		return stepSearched, fmt.Sprintf("%s: found releases but couldn't grab them yet (see History).", name), nil
	case res.Incomplete > 0 || res.Units == 0:
		return stepSearched, fmt.Sprintf("%s: not everything could be searched yet; trying again later.", name), nil
	}
	// Nothing acceptable anywhere, and nothing on its way: it has all it
	// can find.
	return stepFinished, "", s.backfillFinish(ctx, row, sv, have, len(wanted))
}

// backfillFinish ends the active show's turn: done when nothing is
// missing, otherwise it goes to Needs a decision. A row the user removed
// meanwhile stays removed.
func (s *Service) backfillFinish(ctx context.Context, row *BackfillRow, sv library.SeriesVersion, have, missing int) error {
	state := BackfillDone
	msg := fmt.Sprintf("Backfill finished %s: all %d episodes.", sv.Title.Title, have)
	if missing > 0 {
		state = BackfillDecision
		msg = fmt.Sprintf("Backfill finished %s: %d of %d episodes; %d still wanted.", sv.Title.Title, have, have+missing, missing)
	}
	now := s.now().UTC()
	res, err := s.history.db.DB.NewUpdate().Model((*BackfillRow)(nil)).
		Set("state = ?", state).Set("finished_at = ?", now).Set("found = ?", have).Set("missing = ?", missing).
		Where("id = ? AND state = ?", row.ID, BackfillActive).Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	return s.history.Add(ctx, s.backfillEvent(sv, msg), EventData{})
}

// backfillCloseDecisions marks the finished shows whose last missing
// episodes turned up (RSS and the scheduled pass go on looking) as done. It
// returns how many.
func (s *Service) backfillCloseDecisions(ctx context.Context) (int, error) {
	var rows []BackfillRow
	if err := s.history.db.DB.NewSelect().Model(&rows).Where("state = ?", BackfillDecision).Scan(ctx); err != nil {
		return 0, err
	}
	closed := 0
	for _, r := range rows {
		svs, err := s.lib.SeriesVersions(ctx, r.VersionID)
		if err != nil {
			return closed, err
		}
		if len(svs) == 0 {
			continue
		}
		have, _, wanted := s.episodeCounts(svs[0])
		if len(wanted) > 0 {
			continue
		}
		res, err := s.history.db.DB.NewUpdate().Model((*BackfillRow)(nil)).
			Set("state = ?", BackfillDone).Set("found = ?", have).Set("missing = ?", 0).
			Where("id = ? AND state = ?", r.ID, BackfillDecision).Exec(ctx)
		if err != nil {
			return closed, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		closed++
		msg := fmt.Sprintf("Backfill finished %s: all %d episodes.", svs[0].Title.Title, have)
		if err := s.history.Add(ctx, s.backfillEvent(svs[0], msg), EventData{}); err != nil {
			return closed, err
		}
	}
	return closed, nil
}

// BackfillOption is a release the profile turned down, offered for a
// missing season or episode.
type BackfillOption struct {
	// Token identifies the option when it is taken; see BackfillGrabOption.
	Token   string
	Title   string
	Size    int64
	Quality string
	Indexer string
	Seeders int
	// Reasons are why the profile turned it down, in plain English.
	Reasons []string
}

// BackfillUnit is one missing season (as a pack) or episode, with its
// options.
type BackfillUnit struct {
	Label      string
	EpisodeIDs []int64
	Options    []BackfillOption
}

// BackfillOptionList is what "Show options" offers for a show.
type BackfillOptionList struct {
	Units []BackfillUnit
	// Omitted counts units left out to keep one request small; hide or
	// remove found ones and ask again to see them.
	Omitted int
}

// pickedOption is what a token stands for. It lives in memory only for
// optionsKept; the release is never taken from the client.
type pickedOption struct {
	rowID      int64
	versionID  int64
	episodeIDs []int64
	label      string
	rel        indexer.Release
	d          decision.Decision
	cov        decision.Covers
	at         time.Time
}

// BackfillOptions searches once more for each missing unit of a show and
// offers, for each, the releases with the most seeders that the profile
// turned down. Never offered: blocklisted releases, releases nobody seeds,
// and releases for the wrong show, season or episode. Nothing is stored
// except the short-lived tokens that let the user take one.
func (s *Service) BackfillOptions(ctx context.Context, id int64) (*BackfillOptionList, error) {
	row, err := s.backfillRow(ctx, id)
	if err != nil {
		return nil, err
	}
	ws, err := s.loadTV(ctx, row.VersionID)
	if err != nil {
		return nil, err
	}
	if len(ws) == 0 {
		return nil, ErrBackfillNotFound
	}
	w := ws[0]
	us := s.units(w)
	out := &BackfillOptionList{Units: []BackfillUnit{}}
	if len(us) > maxUnitsPerSearch {
		out.Omitted = len(us) - maxUnitsPerSearch
		us = us[:maxUnitsPerSearch]
	}
	blocked := func(string, string) bool { return false }
	if s.queue != nil {
		if blocked, err = s.queue.Blocklisted(ctx, w.sv.Title.ID); err != nil {
			return nil, err
		}
	}
	s.optMu.Lock()
	s.forgetOldOptionsLocked()
	s.optMu.Unlock()
	// A finished season's packs are offered for its single episodes too
	// (see packFallback), from one season search each.
	packs := map[int][]indexer.Release{}
	for _, u := range us {
		rels, _, err := s.idx.Search(ctx, u.query(), indexer.SearchOptions{})
		if err != nil {
			return nil, err
		}
		cs, err := s.decideTV(ctx, w, u.target(), dedupe(rels))
		if err != nil {
			return nil, err
		}
		// What each candidate would be grabbed for: the unit's episodes, or a
		// fallback pack's.
		forIDs := map[*tvCandidate][]int64{}
		if pu, ok := s.packFallback(u, newSearchRun()); ok {
			prels, searched := packs[u.season]
			if !searched {
				if prels, _, err = s.idx.Search(ctx, pu.query(), indexer.SearchOptions{}); err != nil {
					return nil, err
				}
				prels = dedupe(prels)
				packs[u.season] = prels
			}
			pcs, err := s.decideTV(ctx, w, pu.target(), prels)
			if err != nil {
				return nil, err
			}
			// The episode search's packs were turned down only for being
			// packs; the season search judged them as packs.
			cs = slices.DeleteFunc(cs, func(c tvCandidate) bool { return c.cov.Pack })
			for _, c := range pcs {
				if c.cov.Pack {
					cs = append(cs, c)
				}
			}
			for i := range cs {
				if cs[i].cov.Pack {
					forIDs[&cs[i]] = pu.episodeIDs()
				}
			}
		}
		var offered []*tvCandidate
		for i := range cs {
			c := &cs[i]
			if c.d.Accepted || c.d.WrongContent || c.rel.Seeders <= 0 || blocked(c.rel.Title, c.rel.InfoHash) {
				continue
			}
			offered = append(offered, c)
		}
		slices.SortStableFunc(offered, func(a, b *tvCandidate) int { return b.rel.Seeders - a.rel.Seeders })
		unit := BackfillUnit{Label: u.label(), EpisodeIDs: u.episodeIDs(), Options: []BackfillOption{}}
		for _, c := range offered[:min(len(offered), optionsPerUnit)] {
			ids, ok := forIDs[c]
			if !ok {
				ids = u.episodeIDs()
			}
			token, err := s.rememberOption(pickedOption{
				rowID: id, versionID: row.VersionID, episodeIDs: ids, label: u.label(), rel: c.rel, d: c.d, cov: c.cov,
			})
			if err != nil {
				return nil, err
			}
			unit.Options = append(unit.Options, BackfillOption{
				Token: token, Title: c.rel.Title, Size: c.rel.Size, Quality: c.d.Quality.Name, Indexer: c.rel.Indexer,
				Seeders: c.rel.Seeders, Reasons: c.d.Rejections,
			})
		}
		out.Units = append(out.Units, unit)
	}
	return out, nil
}

// rememberOption keeps a release the user may take and returns its token:
// random, so it can't be guessed or forged.
func (s *Service) rememberOption(o pickedOption) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	o.at = s.now()
	s.optMu.Lock()
	defer s.optMu.Unlock()
	s.options[token] = o
	return token, nil
}

func (s *Service) forgetOldOptionsLocked() {
	for k, o := range s.options {
		if s.now().Sub(o.at) > optionsKept {
			delete(s.options, k)
		}
	}
}

// BackfillGrabOption grabs the release behind a token from "Show options",
// outside the profile. It still refuses what an automatic grab refuses
// when the grab can't work: a media folder with "Manage files" off, not
// enough free space, a blocklisted release. The queue limit and
// backfill's quota don't apply, since the user chose it (the grab does
// count toward them). It returns the History message.
func (s *Service) BackfillGrabOption(ctx context.Context, id int64, token string) (string, error) {
	s.optMu.Lock()
	s.forgetOldOptionsLocked()
	o, ok := s.options[token]
	s.optMu.Unlock()
	if !ok || o.rowID != id {
		return "", ErrOptionGone
	}
	ws, err := s.loadTV(ctx, o.versionID)
	if err != nil {
		return "", err
	}
	if len(ws) == 0 {
		return "", ErrBackfillNotFound
	}
	w := ws[0]
	// The episodes the release holds that are still wanted (and not already
	// downloading), as of now.
	var ids []int64
	for _, e := range w.wanted {
		if slices.Contains(o.episodeIDs, e.ID) && covers(o.cov, e) {
			ids = append(ids, e.ID)
		}
	}
	if len(ids) == 0 {
		return "", BackfillRefusal("Those episodes are no longer waiting for a release.")
	}
	if s.queue != nil {
		blocked, err := s.queue.Blocklisted(ctx, w.sv.Title.ID)
		if err != nil {
			return "", err
		}
		if blocked(o.rel.Title, o.rel.InfoHash) {
			return "", BackfillRefusal("That release is on the blocklist.")
		}
	}
	if s.grabber == nil {
		return "", BackfillRefusal("No download client is set up yet (Settings → Download Client).")
	}
	if reason, err := s.unmanaged(ctx, w.sv.Version.RootFolderID); err != nil {
		return "", err
	} else if reason != "" {
		return "", BackfillRefusal(reason)
	}
	p := tvPick{c: tvCandidate{candidate: candidate{rel: o.rel, d: o.d}, cov: o.cov}, ids: ids}
	label := pickLabel(w, p)
	tg := &TVGrab{Title: w.sv.Title, Version: w.sv.Version, EpisodeIDs: ids, Label: label}
	if err := s.grabber.Grab(ctx, Grabbed{Release: o.rel, Quality: o.d.Quality.Name, Score: o.d.Score, TV: tg}); err != nil {
		if errors.Is(err, ErrMoving) {
			return "", BackfillRefusal("Torrents are moving to another download client; try again when that's done (System → Tasks).")
		}
		if errors.Is(err, ErrNotEnoughSpace) {
			return "", BackfillRefusal(strings.TrimPrefix(err.Error(), ErrNotEnoughSpace.Error()+": ") + ".")
		}
		return "", BackfillRefusal("The download client refused it: " + err.Error())
	}
	s.optMu.Lock()
	delete(s.options, token)
	s.optMu.Unlock()

	v := verdict(p.c.candidate)
	ev := s.tvEvent(w, EventGrabbed, "")
	ev.SourceTitle, ev.Indexer, ev.Quality, ev.Score = o.rel.Title, o.rel.Indexer, o.d.Quality.Name, o.d.Score
	ev.Message = fmt.Sprintf("Grabbed %s for %s (%s, from %s). Picked by you outside the profile: %s",
		o.rel.Title, label, o.d.Quality.Name, o.rel.Indexer, strings.Join(o.d.Rejections, " "))
	return ev.Message, s.history.Add(ctx, ev, EventData{Trigger: TriggerPicked, Chosen: &v, EpisodeIDs: ids})
}
