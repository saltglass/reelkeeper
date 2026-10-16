package grab

import (
	"context"
	"encoding/json"
	"time"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/store"
)

// History events (stored; never rename one that has shipped).
const (
	EventSearched    = "searched"
	EventRSS         = "rss"
	EventWouldGrab   = "would-grab"
	EventGrabbed     = "grabbed"
	EventGrabSkipped = "grab-skipped"
	// Downloads (recorded by internal/download).
	EventImported     = "imported"
	EventImportFailed = "import-failed"
	EventRemoved      = "removed"
	// EventPaused, EventResumed and EventMovedToTop: the user's buttons on
	// Activity → Queue. The download keeps its status.
	EventPaused     = "paused"
	EventResumed    = "resumed"
	EventMovedToTop = "moved-to-top"
	// EventMoved: a torrent moved to another download client (1.3).
	// EventMoveFailed: it couldn't, and stayed in the old one.
	EventMoved      = "moved"
	EventMoveFailed = "move-failed"
	// EventBackfill: the backfill queue started or finished a show.
	EventBackfill = "backfill"
	// EventFromRadarr is a record brought over from Radarr's own history
	// at cutover (internal/migrate).
	EventFromRadarr = "from-radarr"
	// EventFromSonarr is the same for a record brought over from Sonarr.
	EventFromSonarr = "from-sonarr"
)

// Event is one row of History.
type Event struct {
	bun.BaseModel `bun:"table:history"`

	ID          int64     `bun:"id,pk,autoincrement"`
	CreatedAt   time.Time `bun:"created_at"`
	TitleID     *int64    `bun:"title_id"`
	VersionID   *int64    `bun:"version_id"`
	Event       string    `bun:"event"`
	SourceTitle string    `bun:"source_title"`
	Indexer     string    `bun:"indexer"`
	Quality     string    `bun:"quality"`
	Score       int       `bun:"score"`
	Message     string    `bun:"message"`
	Data        string    `bun:"data"` // JSON, see EventData
}

// EventData is an event's details.
type EventData struct {
	// Trigger says what started a search: "manual", "added", "scheduled",
	// "rss" or "backfill" (or "picked", for a release the user chose).
	Trigger string `json:"trigger,omitempty"`
	// Releases are what a search or RSS sync found for the version, best
	// first, with the verdict on each (at most maxRecorded).
	Releases []Verdict `json:"releases,omitempty"`
	// More counts releases left out of Releases.
	More int `json:"more,omitempty"`
	// IndexerErrors are indexers that failed during the search.
	IndexerErrors []string `json:"indexerErrors,omitempty"`
	// Chosen is the release picked for a grab event.
	Chosen *Verdict `json:"chosen,omitempty"`
	// EpisodeIDs are the episodes a TV event is about.
	EpisodeIDs []int64 `json:"episodeIds,omitempty"`
}

// Verdict is the decision engine's view of one release, as History shows
// it.
type Verdict struct {
	Name       string        `json:"name"`
	Indexer    string        `json:"indexer"`
	Size       int64         `json:"size"`
	Seeders    int           `json:"seeders"`
	Quality    string        `json:"quality"`
	Score      int           `json:"score"`
	Formats    []FormatScore `json:"formats,omitempty"`
	Accepted   bool          `json:"accepted"`
	Upgrade    bool          `json:"upgrade,omitempty"`
	Rejections []string      `json:"rejections,omitempty"`
}

// FormatScore is a custom format a release matched.
type FormatScore struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
}

// maxRecorded caps the releases kept per event. Searches on popular
// movies return hundreds; the best ones and a sample of the rest explain
// the decision.
const maxRecorded = 50

// keepSearches is how long search and RSS events are kept. Grab events
// are kept for good: they're the record of what was downloaded and why.
const keepSearches = 90 * 24 * time.Hour

// History stores and lists events.
type History struct {
	db  *store.Store
	now func() time.Time
}

// NewHistory returns the history store.
func NewHistory(db *store.Store) *History { return &History{db: db, now: time.Now} }

// Add records an event.
func (h *History) Add(ctx context.Context, e *Event, data EventData) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	e.Data = string(b)
	if e.CreatedAt.IsZero() {
		e.CreatedAt = h.now().UTC()
	}
	_, err = h.db.DB.NewInsert().Model(e).Exec(ctx)
	return err
}

// ListOptions narrow a History listing.
type ListOptions struct {
	TitleID *int64
	Events  []string // empty: all
	Before  int64    // only events with a smaller ID (paging); 0: newest
	Limit   int
}

// List returns events newest first.
func (h *History) List(ctx context.Context, o ListOptions) ([]Event, error) {
	limit := o.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []Event
	q := h.db.DB.NewSelect().Model(&out).Order("id DESC").Limit(int64(limit))
	if o.TitleID != nil {
		q = q.Where("title_id = ?", *o.TitleID)
	}
	if len(o.Events) > 0 {
		q = q.Where("event IN (?)", bun.List(o.Events))
	}
	if o.Before > 0 {
		q = q.Where("id < ?", o.Before)
	}
	return out, q.Scan(ctx)
}

// recently reports whether the version has an event of this kind for this
// release since the given time.
func (h *History) recently(ctx context.Context, versionID int64, event, sourceTitle string, since time.Time) (bool, error) {
	return h.db.DB.NewSelect().Model((*Event)(nil)).
		Where("version_id = ? AND event = ? AND source_title = ? AND created_at >= ?", versionID, event, sourceTitle, since.UTC()).
		Exists(ctx)
}

// countSince counts events of a kind since a time (grabs today).
func (h *History) countSince(ctx context.Context, event string, since time.Time) (int, error) {
	n, err := h.db.DB.NewSelect().Model((*Event)(nil)).
		Where("event = ? AND created_at >= ?", event, since.UTC()).Count(ctx)
	return int(n), err
}

// countSinceFor counts one version's events of a kind since a time (the
// backfill quota).
func (h *History) countSinceFor(ctx context.Context, event string, versionID int64, since time.Time) (int, error) {
	n, err := h.db.DB.NewSelect().Model((*Event)(nil)).
		Where("event = ? AND version_id = ? AND created_at >= ?", event, versionID, since.UTC()).Count(ctx)
	return int(n), err
}

// Prune deletes search and RSS events older than keepSearches.
func (h *History) Prune(ctx context.Context) (int64, error) {
	res, err := h.db.DB.NewDelete().Model((*Event)(nil)).
		Where("event IN (?)", bun.List([]string{EventSearched, EventRSS})).
		Where("created_at < ?", h.now().UTC().Add(-keepSearches)).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
