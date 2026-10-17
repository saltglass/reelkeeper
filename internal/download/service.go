package download

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/cleanup"
	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/jobs"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
)

// Download statuses (stored; never rename one that has shipped).
const (
	StatusDownloading  = "downloading"
	StatusImported     = "imported"
	StatusImportFailed = "import-failed"
	StatusRemoved      = "removed"
	// StatusSeeded: imported, seeded as long as needed, and removed from
	// the download client by cleanup.
	StatusSeeded = "seeded"
)

// Download is a torrent Reelkeeper sent to the download client.
type Download struct {
	bun.BaseModel `bun:"table:downloads"`

	ID              int64      `bun:"id,pk,autoincrement"`
	CreatedAt       time.Time  `bun:"created_at"`
	TitleID         *int64     `bun:"title_id"`
	VersionID       *int64     `bun:"version_id"`
	Hash            string     `bun:"hash"`
	Category        string     `bun:"category"`
	ReleaseTitle    string     `bun:"release_title"`
	IndexerID       *int64     `bun:"indexer_id"`
	Indexer         string     `bun:"indexer"`
	Quality         string     `bun:"quality"`
	Score           int        `bun:"score"`
	Size            int64      `bun:"size"`
	Upgrade         bool       `bun:"upgrade"` // replaces a file the version has: a movie upgrade or a TV fix
	SeedRatio       *float64   `bun:"seed_ratio"`
	SeedTimeMinutes *int       `bun:"seed_time_minutes"`
	Status          string     `bun:"status"`
	Message         string     `bun:"message"`
	CompletedAt     *time.Time `bun:"completed_at"`
	ImportedAt      *time.Time `bun:"imported_at"`
	ImportedPath    string     `bun:"imported_path"`
	// EpisodesLabel names a TV download's episodes ("S02E05", "season 2
	// (8 wanted episodes)"); empty for movies.
	EpisodesLabel string `bun:"episodes_label"`
	// CleanupTrack is what cleanup remembers between checks (JSON), and
	// LimitsSet whether the client's own seeding limits were lifted.
	CleanupTrack string `bun:"cleanup_track"`
	LimitsSet    bool   `bun:"limits_set"`
	// Requested: somebody asked for it (a Seerr request). It goes to the top of
	// the download client's queue, and again when it moves to another client.
	// Reelkeeper notes this itself because not every client has tags to hold
	// it (Deluge has none); qBittorrent and Transmission still get the
	// "requested" tag.
	Requested bool `bun:"requested"`

	// Client is the type of download client holding the torrent
	// (settings.ClientQBittorrent, ClientTransmission or ClientDeluge). It is
	// empty on downloads from before 1.3: the client that is set up.
	Client string `bun:"client"`
	// The Move fields say how far a move to another client has got (see
	// move.go); MoveState is empty when the download isn't moving.
	MoveState     string `bun:"move_state"`
	MoveTo        string `bun:"move_to"`
	MoveOldBytes  int64  `bun:"move_old_bytes"`
	MoveWasPaused bool   `bun:"move_was_paused"`
	MoveLayout    string `bun:"move_layout"`
	// MoveStartedAt is when the download was marked to move (nil when it isn't
	// moving). A move still unfinished an hour later gives up (moveGiveUp).
	MoveStartedAt *time.Time `bun:"move_started_at"`
	// SeedSecondsOffset and UploadedOffset are what earlier clients had
	// already seeded and uploaded when the torrent moved. No client lets
	// anyone set its own counters, so cleanup adds these to what the
	// current client reports (see Service.counted).
	SeedSecondsOffset int64      `bun:"seed_seconds_offset"`
	UploadedOffset    int64      `bun:"uploaded_offset"`
	MovedAt           *time.Time `bun:"moved_at"`
}

// Client is what Reelkeeper needs from a download client.
type Client interface {
	// Name is what messages call the client, such as "qBittorrent".
	Name() string
	Version(ctx context.Context) (string, error)
	EnsureCategory(ctx context.Context, name, savePath string) error
	MoveCategory(ctx context.Context, from, to string, hashes []string) error
	Add(ctx context.Context, a Add) (string, error)
	Torrents(ctx context.Context, category string) ([]Torrent, error)
	Remove(ctx context.Context, category string, hashes []string, deleteFiles bool) error
	NoShareLimits(ctx context.Context, category string, hashes []string) error
	// TopPriority moves these torrents to the top of the client's queue.
	TopPriority(ctx context.Context, category string, hashes []string) error
	// Stop pauses these torrents, and Start resumes them.
	Stop(ctx context.Context, category string, hashes []string) error
	Start(ctx context.Context, category string, hashes []string) error
	TrackerMessages(ctx context.Context, category, hash string) ([]string, error)

	// What moving a torrent to another client needs (move.go). Each call
	// only touches Reelkeeper's own torrents, like the ones above.
	//
	// Source gives back the torrent's .torrent file, or its magnet link when
	// the file can't be had. It fails with a NoSourceError when neither can.
	Source(ctx context.Context, category, hash string) (Source, error)
	// Layout says where the torrent's files are.
	Layout(ctx context.Context, category, hash string) (Layout, error)
	// Recheck starts the client's own check of a stopped torrent's files.
	Recheck(ctx context.Context, category, hash string) error
	// SetFolder moves a torrent's files to the folder it belongs in, if the
	// client has to be told (Transmission, Deluge); it never deletes anything.
	SetFolder(ctx context.Context, category, hash, folder string) error
}

// Fetcher gets releases from indexers (indexer.Service).
type Fetcher interface {
	Fetch(ctx context.Context, rel indexer.Release) (indexer.Fetched, error)
	Get(ctx context.Context, id int64) (*indexer.Indexer, error)
}

// Importer puts finished downloads into the library (library.Service).
type Importer interface {
	ImportMovie(ctx context.Context, req library.ImportRequest) (library.ImportResult, error)
	ImportEpisodes(ctx context.Context, req library.EpisodeImportRequest) (library.EpisodeImportResult, error)
}

// Service tracks Reelkeeper's downloads: it sends grabs to the download client, watches
// them, and imports them when they finish. It implements grab.Grabber and
// grab.Queue.
type Service struct {
	db       *store.Store
	settings func() settings.DownloadClient
	idx      Fetcher
	lib      Importer
	history  *grab.History
	logger   *slog.Logger
	now      func() time.Time
	// NewClient builds the client for the current settings (tests swap
	// in a fake).
	NewClient func(settings.DownloadClient) Client
	// builtin runs the built-in client's engine (SetBuiltin); nil when the
	// server doesn't have one.
	builtin *BuiltinHost

	// Cleanup (see cleanup.go); set with SetCleanup.
	cleanupCfg  func() settings.Cleanup
	searchAgain func(ctx context.Context, versionID int64) error
	replaced    ReplacedEmptier

	syncMu sync.Mutex // one check (and import) at a time

	// notify tells Emby, email and webhooks what happened (SetNotifier).
	notify func(ctx context.Context, ev notify.Event)
	// clientFailures counts checks in a row that couldn't reach the
	// download client.
	clientFailures int
	// vis tracks finished downloads Reelkeeper can't see yet (visible.go).
	vis visibility
	// exists reports whether a download's path is on disk (tests swap it).
	exists func(path string) bool
	// freeSpace reports the bytes free on the filesystem holding a path, or
	// -1 when it can't be told (tests swap it).
	freeSpace func(path string) int64
	// wantedTitles gives the titles that still want something (the
	// blocklist's "dormant" test; see SetWantedTitles).
	wantedTitles func(ctx context.Context) (map[int64]bool, error)

	// box encrypts the torrent files and magnet links kept for moving
	// (sources.go); nil in tests that don't need it.
	box *secrets.Box
	// moves holds the move in progress, and jobs runs it (move.go).
	moves  MoveStore
	jobs   *jobs.Manager
	moveMu sync.Mutex // one move task at a time
	// moveHook is called with each step a download is about to take (tests
	// use it to simulate a restart there).
	moveHook func(state string) error
	// rename renames a file on disk (tests swap it); moveTiming says how long
	// the move waits for the clients (tests shorten it).
	rename     func(oldPath, newPath string) error
	moveTiming moveTiming
}

// SetNotifier connects notifications.
func (s *Service) SetNotifier(fn func(ctx context.Context, ev notify.Event)) { s.notify = fn }

func (s *Service) tell(ctx context.Context, ev notify.Event) {
	if s.notify != nil {
		s.notify(ctx, ev)
	}
}

// movieName is a download's movie for messages: the release name when the
// movie itself isn't known.
func (s *Service) movieName(d *Download) string { return d.ReleaseTitle }

// NewService returns the downloads service.
func NewService(db *store.Store, cfg func() settings.DownloadClient, idx Fetcher, lib Importer, history *grab.History, logger *slog.Logger) *Service {
	s := &Service{
		db: db, settings: cfg, idx: idx, lib: lib, history: history, logger: logger, now: time.Now,
		vis: visibility{missingSince: map[int64]time.Time{}}, exists: onDisk, freeSpace: library.FreeSpace,
		rename: os.Rename, moveTiming: defaultMoveTiming,
	}
	s.NewClient = func(c settings.DownloadClient) Client {
		if c.ClientType() == settings.ClientBuiltin {
			return &Builtin{cfg: c, host: s.builtin}
		}
		return newClient(c)
	}
	return s
}

// SetBuiltin connects the host of the built-in client's engine, which the
// server starts and stops as the settings change.
func (s *Service) SetBuiltin(h *BuiltinHost) { s.builtin = h }

// newClient builds the client the settings choose. (The built-in client needs
// the server's engine, which Service.NewClient adds.)
func newClient(c settings.DownloadClient) Client {
	switch c.ClientType() {
	case settings.ClientTransmission:
		return &Transmission{URL: c.URL, Username: c.Username, Password: c.Password}
	case settings.ClientDeluge:
		return &Deluge{URL: c.URL, Password: c.Password}
	case settings.ClientBuiltin:
		return &Builtin{cfg: c}
	}
	return &QBittorrent{URL: c.URL, Username: c.Username, Password: c.Password}
}

// ErrNoClient means no download client is set up.
var ErrNoClient = errors.New("no download client is set up (Settings → Download Client)")

func (s *Service) client() (Client, error) {
	cfg := s.settings()
	if !cfg.Configured() {
		return nil, ErrNoClient
	}
	return s.NewClient(cfg), nil
}

// ClientName is the name of the download client in use, for messages. When
// none is set up it is "the download client".
func (s *Service) ClientName() string {
	c, err := s.client()
	if err != nil {
		return "the download client"
	}
	return c.Name()
}

// Test checks a download client's settings (saved or not) and returns its
// version.
func (s *Service) Test(ctx context.Context, cfg settings.DownloadClient) (string, error) {
	if !cfg.Configured() {
		return "", ErrNoClient
	}
	c := s.NewClient(cfg)
	v, err := c.Version(ctx)
	if err != nil {
		return "", err
	}
	// Listing Reelkeeper's category proves the session works for more than
	// the login. The built-in client has no session, and may not be running
	// yet (it is tested before it is saved).
	if cfg.ClientType() != settings.ClientBuiltin {
		if _, err := c.Torrents(ctx, settings.MovieCategory); err != nil {
			return "", err
		}
	}
	return v, nil
}

// Grab sends a chosen release to the download client and starts tracking
// it.
func (s *Service) Grab(ctx context.Context, g grab.Grabbed) error {
	if s.Moving() {
		return grab.ErrMoving
	}
	c, err := s.client()
	if err != nil {
		return err
	}
	category, label := settings.MovieCategory, ""
	tid, vid := g.Version.Title.ID, g.Version.Version.ID
	if g.TV != nil {
		category, label = settings.TVCategory, g.TV.Label
		tid, vid = g.TV.Title.ID, g.TV.Version.ID
	}
	if err := s.checkRoom(ctx, c, g.Release.Size); err != nil {
		return err
	}
	f, err := s.idx.Fetch(ctx, g.Release)
	if err != nil {
		return err
	}
	if err := c.EnsureCategory(ctx, category, ""); err != nil {
		return err
	}
	tags := []string{"reelkeeper", g.Release.Indexer}
	if g.Requested {
		tags = append(tags, "requested")
	}
	hash, err := c.Add(ctx, Add{
		Magnet: f.Magnet, Torrent: f.Torrent, Category: category,
		Tags: tags, SavePath: s.settings().Folder,
	})
	if err != nil {
		return err
	}
	// Something somebody asked for (Seerr) goes to the front of the
	// client's queue. Not worth failing the grab over.
	if g.Requested {
		if err := c.TopPriority(ctx, category, []string{hash}); err != nil {
			s.logger.Warn("couldn't move a requested download to the top of the download client's queue", "client", c.Name(), "error", err)
		}
	}
	// Reelkeeper's cleanup decides when its torrents stop seeding, not
	// qBittorrent's global limits (which could stop them before a
	// tracker's minimum). Retried by cleanup if this fails.
	limitsSet := false
	if s.cleanupCfg != nil && s.cleanupCfg().Mode != settings.CleanupOff {
		if err := c.NoShareLimits(ctx, category, []string{hash}); err != nil {
			s.logger.Warn("couldn't lift the download client's seeding limits for a new download; cleanup will retry", "client", c.Name(), "error", err)
		} else {
			limitsSet = true
		}
	}
	iid := g.Release.IndexerID
	d := &Download{
		CreatedAt: s.now().UTC(), TitleID: &tid, VersionID: &vid, Hash: hash, Category: category,
		ReleaseTitle: g.Release.Title, Indexer: g.Release.Indexer, Quality: g.Quality,
		Score: g.Score, Size: g.Release.Size, Upgrade: g.Upgrade, Status: StatusDownloading,
		CleanupTrack: "{}", LimitsSet: limitsSet, EpisodesLabel: label, Client: s.settings().ClientType(), Requested: g.Requested,
	}
	// The tracker's seeding rules as they are now: cleanup (M5) keeps the
	// torrent until they're met, even if the rules change later.
	if ix, err := s.idx.Get(ctx, iid); err == nil && iid != 0 {
		d.IndexerID = &iid
		d.SeedRatio, d.SeedTimeMinutes = ix.SeedRatio, ix.SeedTimeMinutes
	}
	if g.Release.MinimumRatio > 0 && (d.SeedRatio == nil || *d.SeedRatio < g.Release.MinimumRatio) {
		r := g.Release.MinimumRatio
		d.SeedRatio = &r
	}
	if m := int(g.Release.MinimumSeedTime / 60); m > 0 && (d.SeedTimeMinutes == nil || *d.SeedTimeMinutes < m) {
		d.SeedTimeMinutes = &m
	}
	var id int64
	err = s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := upsertDownload(ctx, tx, d); err != nil {
			return err
		}
		if g.TV == nil {
			return nil
		}
		// Which episodes it's for (replacing any from an earlier grab of
		// the same torrent).
		if err := tx.NewSelect().Model((*Download)(nil)).Column("id").Where("hash = ?", d.Hash).Scan(ctx, &id); err != nil {
			return err
		}
		if _, err := tx.NewDelete().Model((*DownloadEpisode)(nil)).Where("download_id = ?", id).Exec(ctx); err != nil {
			return err
		}
		rows := make([]DownloadEpisode, len(g.TV.EpisodeIDs))
		for i, e := range g.TV.EpisodeIDs {
			rows[i] = DownloadEpisode{DownloadID: id, EpisodeID: e}
		}
		if len(rows) == 0 {
			return nil
		}
		_, err := tx.NewInsert().Model(&rows).Exec(ctx)
		return err
	})
	if err != nil {
		return err
	}
	// What was sent is kept while the torrent is active, so another client
	// can be given the same torrent (see sources.go). Failing to keep it
	// doesn't fail the grab: the torrent is already in the client.
	if err := s.keepSource(ctx, d.Hash, Source{Torrent: f.Torrent, Magnet: f.Magnet}); err != nil {
		s.logger.Warn("couldn't keep the torrent for a later move to another download client", "release", d.ReleaseTitle, "error", err)
	}
	return nil
}

// DownloadEpisode records an episode a TV download is for.
type DownloadEpisode struct {
	bun.BaseModel `bun:"table:download_episodes"`

	DownloadID int64 `bun:"download_id,pk"`
	EpisodeID  int64 `bun:"episode_id,pk"`
}

// upsertDownload records a new download. The same torrent can be grabbed
// again after it was removed: its old record then becomes the new one.
// (Ignoring the conflict instead once left a finished download untracked
// and never imported.)
func upsertDownload(ctx context.Context, db bun.IDB, d *Download) error {
	_, err := db.NewInsert().Model(d).On("CONFLICT (hash) DO UPDATE").
		Set("created_at = EXCLUDED.created_at").
		Set("title_id = EXCLUDED.title_id").
		Set("version_id = EXCLUDED.version_id").
		Set("category = EXCLUDED.category").
		Set("release_title = EXCLUDED.release_title").
		Set("indexer_id = EXCLUDED.indexer_id").
		Set("indexer = EXCLUDED.indexer").
		Set("quality = EXCLUDED.quality").
		Set("score = EXCLUDED.score").
		Set("size = EXCLUDED.size").
		Set("upgrade = EXCLUDED.upgrade").
		Set("seed_ratio = EXCLUDED.seed_ratio").
		Set("seed_time_minutes = EXCLUDED.seed_time_minutes").
		Set("status = EXCLUDED.status").
		Set("message = ''").
		Set("completed_at = NULL").
		Set("imported_at = NULL").
		Set("imported_path = ''").
		Set("cleanup_track = EXCLUDED.cleanup_track").
		Set("limits_set = EXCLUDED.limits_set").
		Set("episodes_label = EXCLUDED.episodes_label").
		Set("requested = EXCLUDED.requested").
		Set("client = EXCLUDED.client").
		Set("move_state = ''").
		Set("move_to = ''").
		Set("move_old_bytes = 0").
		Set("move_was_paused = FALSE").
		Set("move_layout = ''").
		Set("move_started_at = NULL").
		Set("seed_seconds_offset = 0").
		Set("uploaded_offset = 0").
		Set("moved_at = NULL").
		Exec(ctx)
	return err
}

// ActiveVersions are the versions with a download that hasn't finished
// importing (including failed imports, which wait for the user).
func (s *Service) ActiveVersions(ctx context.Context) (map[int64]bool, error) {
	var ids []int64
	err := s.db.DB.NewSelect().Model((*Download)(nil)).Column("version_id").
		Where("status IN (?)", bun.List([]string{StatusDownloading, StatusImportFailed})).
		Where("version_id IS NOT NULL").Scan(ctx, &ids)
	out := map[int64]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, err
}

// ActiveEpisodes are the episodes with a download that hasn't finished
// importing, by series version (versions of one series share episodes). A
// season pack's download counts for every episode it was grabbed for.
func (s *Service) ActiveEpisodes(ctx context.Context) (map[int64]map[int64]bool, error) {
	var rows []struct {
		VersionID int64 `bun:"version_id"`
		EpisodeID int64 `bun:"episode_id"`
	}
	err := s.db.DB.NewSelect().TableExpr("download_episodes AS de").ColumnExpr("d.version_id, de.episode_id").
		Join("JOIN downloads AS d ON d.id = de.download_id").
		Where("d.status IN (?)", bun.List([]string{StatusDownloading, StatusImportFailed})).
		Where("d.version_id IS NOT NULL").Scan(ctx, &rows)
	out := map[int64]map[int64]bool{}
	for _, r := range rows {
		if out[r.VersionID] == nil {
			out[r.VersionID] = map[int64]bool{}
		}
		out[r.VersionID][r.EpisodeID] = true
	}
	return out, err
}

// CurrentEpisodeDownloads tells, for a series version's episodes that have
// a file, which Reelkeeper download that file came from, when the download
// was imported after since. TV fixes (a REPACK or PROPER) only replace such
// files. The link between file and download is not stored; a file counts as
// the download's when it was added to the library between the download
// being grabbed and being imported and the download actually imported
// something (a download that found every episode already filled imported
// nothing). An episode with no such download is left out. If several
// qualify, the latest import wins.
func (s *Service) CurrentEpisodeDownloads(ctx context.Context, versionID int64, since time.Time) (map[int64]decision.Current, error) {
	var rows []struct {
		EpisodeID    int64      `bun:"episode_id"`
		FileAddedAt  time.Time  `bun:"file_added_at"`
		DownloadID   int64      `bun:"download_id"`
		ReleaseTitle string     `bun:"release_title"`
		CreatedAt    time.Time  `bun:"created_at"`
		ImportedAt   *time.Time `bun:"imported_at"`
	}
	err := s.db.DB.NewSelect().TableExpr("episode_files AS ef").
		ColumnExpr("ef.episode_id, mf.added_at AS file_added_at, d.id AS download_id, d.release_title, d.created_at, d.imported_at").
		Join("JOIN media_files AS mf ON mf.id = ef.media_file_id").
		Join("JOIN download_episodes AS de ON de.episode_id = ef.episode_id").
		Join("JOIN downloads AS d ON d.id = de.download_id").
		Where("mf.version_id = ? AND mf.missing_since IS NULL", versionID).
		Where("d.version_id = ? AND d.category = ?", versionID, settings.TVCategory).
		Where("d.status IN (?)", bun.List([]string{StatusImported, StatusSeeded})).
		Where("d.imported_path <> '' AND d.imported_at IS NOT NULL").Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	out := map[int64]decision.Current{}
	for _, r := range rows {
		if r.ImportedAt == nil || r.ImportedAt.Before(since) ||
			r.FileAddedAt.Before(r.CreatedAt) || r.FileAddedAt.After(*r.ImportedAt) {
			continue
		}
		if cur, ok := out[r.EpisodeID]; !ok || r.ImportedAt.After(cur.ImportedAt) {
			out[r.EpisodeID] = decision.Current{DownloadID: r.DownloadID, Release: r.ReleaseTitle, ImportedAt: *r.ImportedAt}
		}
	}
	return out, nil
}

// Item is one row of Activity → Queue: Reelkeeper's record plus what the
// download client says now (nil if it doesn't have the torrent).
type Item struct {
	Download Download
	Torrent  *Torrent
	// EpisodeIDs are the episodes a TV download is for (none for movies).
	EpisodeIDs []int64
}

// Queue lists downloads still in progress or waiting for attention, with
// their live state. clientErr is set when the download client couldn't be
// asked; the list still shows what Reelkeeper knows.
func (s *Service) Queue(ctx context.Context) (items []Item, clientErr error, err error) {
	var ds []Download
	if err := s.db.DB.NewSelect().Model(&ds).
		Where("status IN (?)", bun.List([]string{StatusDownloading, StatusImportFailed})).
		Order("id").Scan(ctx); err != nil {
		return nil, nil, err
	}
	live := map[string]Torrent{}
	if len(ds) > 0 {
		live, clientErr = s.liveTorrents(ctx, ds)
	}
	episodes := map[int64][]int64{}
	if len(ds) > 0 {
		ids := make([]int64, len(ds))
		for i, d := range ds {
			ids[i] = d.ID
		}
		var rows []DownloadEpisode
		if err := s.db.DB.NewSelect().Model(&rows).Where("download_id IN (?)", bun.List(ids)).
			OrderExpr("download_id, episode_id").Scan(ctx); err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			episodes[r.DownloadID] = append(episodes[r.DownloadID], r.EpisodeID)
		}
	}
	for _, d := range ds {
		it := Item{Download: d, EpisodeIDs: episodes[d.ID]}
		if t, ok := live[s.liveKey(&d)]; ok {
			it.Torrent = &t
		}
		items = append(items, it)
	}
	return items, clientErr, nil
}

// liveKey is what live torrents are filed under: the client's type and the
// hash, since during a move both clients hold the same torrent.
func (s *Service) liveKey(d *Download) string {
	return d.heldBy(s.settings().ClientType()) + "/" + d.Hash
}

// liveTorrents lists the torrents of every client that holds one of the
// downloads: the one that is set up and, while a move runs, the one the
// torrents are moving away from. err is the first client that couldn't be
// asked; what could be read is returned anyway.
func (s *Service) liveTorrents(ctx context.Context, ds []Download) (map[string]Torrent, error) {
	live := map[string]Torrent{}
	cfg := s.settings()
	clients := map[string]settings.DownloadClient{cfg.ClientType(): cfg}
	if s.moves != nil {
		if mv := s.moves.DownloadMove(); mv.Active {
			clients[mv.Old.ClientType()] = mv.Old
		}
	}
	var first error
	asked := map[string]bool{}
	for i := range ds {
		typ := ds[i].heldBy(cfg.ClientType())
		if asked[typ] {
			continue
		}
		asked[typ] = true
		conf, ok := clients[typ]
		if !ok || !conf.Configured() {
			if typ == cfg.ClientType() && first == nil {
				first = ErrNoClient
			}
			continue
		}
		ts, err := ourTorrents(ctx, s.NewClient(conf))
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		for _, t := range ts {
			live[typ+"/"+t.Hash] = t
		}
	}
	return live, first
}

// QueueSize is how many downloads Queue lists, movies and TV together: the
// number the queue limit is checked against. A season pack is one download.
func (s *Service) QueueSize(ctx context.Context) (int, error) {
	n, err := s.db.DB.NewSelect().Model((*Download)(nil)).
		Where("status IN (?)", bun.List([]string{StatusDownloading, StatusImportFailed})).Count(ctx)
	return int(n), err
}

// QueueCount is how many downloads Queue lists, of each kind.
type QueueCount struct {
	Movies, TV int
	// NeedsAttention is the downloads whose import failed.
	NeedsAttention int
}

// QueueCounts counts what Queue lists, from Reelkeeper's own records only (it
// doesn't ask the download client, so it's cheap enough for dashboards that
// poll). A download is TV when it's in the TV category.
func (s *Service) QueueCounts(ctx context.Context) (QueueCount, error) {
	var ds []Download
	var c QueueCount
	if err := s.db.DB.NewSelect().Model(&ds).Column("status", "category").
		Where("status IN (?)", bun.List([]string{StatusDownloading, StatusImportFailed})).Scan(ctx); err != nil {
		return c, err
	}
	for _, d := range ds {
		if d.Category == settings.TVCategory {
			c.TV++
		} else {
			c.Movies++
		}
		if d.Status == StatusImportFailed {
			c.NeedsAttention++
		}
	}
	return c, nil
}

// Check looks at every download in progress: finished ones are imported,
// ones that disappeared from the download client are marked removed. It's
// the "Check downloads" task. quiet is true when nothing happened beyond
// "still downloading" (no import, failure, removal or unseen file), so the
// run isn't worth listing.
func (s *Service) Check(ctx context.Context) (msg string, quiet bool, err error) {
	if s.Moving() {
		return "Paused while torrents move to another download client.", true, nil
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	var ds []Download
	if err := s.db.DB.NewSelect().Model(&ds).Where("status = ?", StatusDownloading).Order("id").Scan(ctx); err != nil {
		return "", false, err
	}
	if len(ds) == 0 {
		return "Nothing is downloading.", true, nil
	}
	c, err := s.client()
	if err != nil {
		return "", false, err
	}
	ts, err := ourTorrents(ctx, c)
	if err != nil {
		// Three checks in a row (about three minutes) before anyone is
		// told: a restart of the download client isn't worth an email.
		s.clientFailures++
		if s.clientFailures == 3 {
			s.tell(ctx, notify.Event{Kind: notify.KindClientUnreachable, Title: c.Name(),
				Message: "Reelkeeper can't reach " + c.Name() + ", so downloads aren't being checked or imported: " + err.Error()})
		}
		return "", false, err
	}
	s.clientFailures = 0
	live := map[string]Torrent{}
	for _, t := range ts {
		live[t.Hash] = t
	}
	folderUnseen, dir := s.folderUnseen(ts)
	s.reportFolder(ctx, folderUnseen, dir)
	var imported, failed, gone, waiting, unseen int
	for i := range ds {
		d := &ds[i]
		t, ok := live[d.Hash]
		switch {
		case !ok:
			gone++
			s.finish(ctx, d, StatusRemoved, "It was removed from "+c.Name()+" outside Reelkeeper.", grab.EventRemoved)
		case !t.Done():
			waiting++
		case s.waitForFiles(ctx, d, t, folderUnseen):
			unseen++
		default:
			if s.importOne(ctx, d, t) {
				imported++
			} else {
				failed++
			}
		}
	}
	parts := []string{fmt.Sprintf("%d downloading", waiting)}
	if unseen > 0 {
		parts = append(parts, fmt.Sprintf("%d finished but not visible to Reelkeeper yet", unseen))
	}
	if imported > 0 {
		parts = append(parts, fmt.Sprintf("%d imported", imported))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed to import", failed))
	}
	if gone > 0 {
		parts = append(parts, fmt.Sprintf("%d removed outside Reelkeeper", gone))
	}
	// Quiet means nothing happened: downloads are still going, and that's all.
	return strings.Join(parts, ", ") + ".", imported+failed+gone+unseen == 0, nil
}

// importOne imports a finished download and records the outcome.
func (s *Service) importOne(ctx context.Context, d *Download, t Torrent) bool {
	now := s.now().UTC()
	if d.CompletedAt == nil {
		c := now
		if t.CompletedOn != nil {
			c = *t.CompletedOn
		}
		d.CompletedAt = &c
	}
	if d.VersionID == nil {
		s.finish(ctx, d, StatusImportFailed, "The movie or series was removed from Reelkeeper, so there's nowhere to import it.", grab.EventImportFailed)
		return false
	}
	if d.Category == settings.TVCategory {
		return s.importEpisodes(ctx, d, t)
	}
	res, err := s.lib.ImportMovie(ctx, library.ImportRequest{VersionID: *d.VersionID, Source: t.ContentPath, ReleaseName: d.ReleaseTitle, ClientName: s.ClientName()})
	if err != nil {
		s.finish(ctx, d, StatusImportFailed, "Import failed: "+err.Error(), grab.EventImportFailed)
		s.tell(ctx, notify.Event{Kind: notify.KindImportFailed, Title: s.movieName(d), TitleID: ptrVal(d.TitleID),
			Message: fmt.Sprintf("%s couldn't be imported: %s It's waiting in Activity → Queue with a Retry button.", d.ReleaseTitle, err)})
		return false
	}
	s.tell(ctx, notify.Event{Kind: notify.KindImported, Title: s.movieName(d), TitleID: ptrVal(d.TitleID), Path: res.FullPath,
		Message: "Imported " + res.Path})
	d.ImportedAt, d.ImportedPath = &now, res.Path
	msg := "Imported as " + res.Path + "."
	if len(res.Replaced) > 0 {
		msg += " The file it replaced was moved to " + strings.Join(res.Replaced, ", ") + "."
	}
	s.finish(ctx, d, StatusImported, msg, grab.EventImported)
	return true
}

// importEpisodes imports a finished TV download: the episodes it was
// grabbed for.
func (s *Service) importEpisodes(ctx context.Context, d *Download, t Torrent) bool {
	var ids []int64
	if err := s.db.DB.NewSelect().Model((*DownloadEpisode)(nil)).Column("episode_id").
		Where("download_id = ?", d.ID).Order("episode_id").Scan(ctx, &ids); err != nil {
		s.finish(ctx, d, StatusImportFailed, "Import failed: couldn't read which episodes it's for: "+err.Error(), grab.EventImportFailed)
		return false
	}
	if len(ids) == 0 {
		s.finish(ctx, d, StatusImportFailed, "The episodes it was for were removed from Reelkeeper, so there's nothing to import.", grab.EventImportFailed)
		return false
	}
	res, err := s.lib.ImportEpisodes(ctx, library.EpisodeImportRequest{
		VersionID: *d.VersionID, Source: t.ContentPath, ReleaseName: d.ReleaseTitle, ClientName: s.ClientName(), EpisodeIDs: ids,
		// A TV download marked as an upgrade is a fix (a REPACK or PROPER
		// of the files already there), which replaces them.
		Replace: d.Upgrade,
	})
	if len(res.Imported) > 0 {
		// Emby is told about the show's folder, which covers a new season
		// folder too.
		show := filepath.Dir(filepath.Dir(res.FullPaths[0]))
		s.tell(ctx, notify.Event{Kind: notify.KindImported, Title: s.movieName(d), TitleID: ptrVal(d.TitleID), Path: show,
			Message: "Imported " + strings.Join(res.Imported, ", ")})
	}
	if err != nil {
		msg := "Import failed: " + err.Error()
		if len(res.Imported) > 0 {
			msg = fmt.Sprintf("Imported %d of the files; the rest failed: %s", len(res.Imported), err)
		}
		s.finish(ctx, d, StatusImportFailed, msg, grab.EventImportFailed)
		s.tell(ctx, notify.Event{Kind: notify.KindImportFailed, Title: s.movieName(d), TitleID: ptrVal(d.TitleID),
			Message: fmt.Sprintf("%s couldn't be imported: %s It's waiting in Activity → Queue with a Retry button.", d.ReleaseTitle, err)})
		return false
	}
	now := s.now().UTC()
	d.ImportedAt = &now
	var msg string
	switch len(res.Imported) {
	case 0:
		msg = "Nothing to import: every episode it was for already has a file."
	case 1:
		d.ImportedPath = res.Imported[0]
		msg = "Imported as " + res.Imported[0] + "."
	default:
		d.ImportedPath = filepath.Dir(res.Imported[0])
		msg = fmt.Sprintf("Imported %d episode files into %s.", len(res.Imported), d.ImportedPath)
	}
	if len(res.Replaced) > 0 {
		msg += " The files it replaced were moved to " + strings.Join(res.Replaced, ", ") + "."
	}
	if len(res.Skipped) > 0 {
		msg += " Skipped: " + strings.Join(res.Skipped, "; ") + "."
	}
	s.finish(ctx, d, StatusImported, msg, grab.EventImported)
	return true
}

// ourTorrents lists the torrents in all of Reelkeeper's categories.
// keepFree is the space to keep free on the download disk (Settings →
// Cleanup; 0 when turned off).
func (s *Service) keepFree() int64 {
	if s.cleanupCfg == nil {
		return int64(settings.DefaultCleanup().KeepFreeGB) << 30
	}
	return int64(s.cleanupCfg().KeepFreeGB) << 30
}

// downloadFree is the space free on the download disk, or -1 when it can't
// be told (no download folder set, or the folder can't be read).
func (s *Service) downloadFree() int64 {
	folder := s.settings().Folder
	if folder == "" {
		return -1
	}
	return s.freeSpace(folder)
}

// checkRoom refuses a grab that would leave the download disk with less
// than the space kept free, counting what downloads in progress still
// need. When the free space can't be told, it lets the grab through.
func (s *Service) checkRoom(ctx context.Context, c Client, size int64) error {
	reserve := s.keepFree()
	free := s.downloadFree()
	if reserve <= 0 || free < 0 {
		return nil
	}
	var pending int64
	if ts, err := ourTorrents(ctx, c); err == nil {
		for _, t := range ts {
			pending += t.Left
		}
	}
	if free-pending-size >= reserve {
		return nil
	}
	return fmt.Errorf("%w: the download disk has %s free and downloads in progress still need %s, so this release (%s) "+
		"would leave less than the %s kept free (Settings → Cleanup). Cleanup makes room once finished downloads have seeded",
		grab.ErrNotEnoughSpace, cleanup.Size(free), cleanup.Size(pending), cleanup.Size(size), cleanup.Size(reserve))
}

// DiskShort says, in plain words, why the download disk can't take another
// download right now, or "" when it can: the space free, less what
// downloads in progress still need, is already below the space kept free.
// Backfill asks before searching, so it doesn't search for releases it
// couldn't grab. When the space (or the client) can't be told, it says
// nothing is wrong.
func (s *Service) DiskShort(ctx context.Context) (string, error) {
	reserve := s.keepFree()
	free := s.downloadFree()
	if reserve <= 0 || free < 0 {
		return "", nil
	}
	c, err := s.client()
	if err != nil {
		return "", nil
	}
	ts, err := ourTorrents(ctx, c)
	if err != nil {
		return "", nil
	}
	var pending int64
	for _, t := range ts {
		pending += t.Left
	}
	if free-pending >= reserve {
		return "", nil
	}
	return fmt.Sprintf("the download disk has %s free and downloads in progress still need %s, which is less than the %s kept free (Settings → Cleanup).",
		cleanup.Size(free), cleanup.Size(pending), cleanup.Size(reserve)), nil
}

func ourTorrents(ctx context.Context, c Client) ([]Torrent, error) {
	var out []Torrent
	for _, cat := range settings.Categories {
		ts, err := c.Torrents(ctx, cat)
		if err != nil {
			return nil, err
		}
		out = append(out, ts...)
	}
	return out, nil
}

// finish records a download's new status, and the event in History.
func (s *Service) finish(ctx context.Context, d *Download, status, message, event string) {
	d.Status, d.Message = status, message
	if _, err := s.db.DB.NewUpdate().Model(d).
		Column("status", "message", "completed_at", "imported_at", "imported_path").WherePK().Exec(ctx); err != nil {
		s.logger.Error("couldn't record a download's status", "release", d.ReleaseTitle, "error", err)
	}
	// A torrent that is gone from the client, or no longer Reelkeeper's to
	// track, doesn't need its file or magnet link kept.
	if status == StatusRemoved || status == StatusSeeded {
		s.dropSource(ctx, d.ID)
	}
	s.record(ctx, d, event, message)
	level := slog.LevelInfo
	if status != StatusImported {
		level = slog.LevelWarn
	}
	s.logger.Log(ctx, level, "download "+status, "release", d.ReleaseTitle, "message", message)
}

// record writes an event about a download to History.
func (s *Service) record(ctx context.Context, d *Download, event, message string) {
	ev := &grab.Event{TitleID: d.TitleID, VersionID: d.VersionID, Event: event, SourceTitle: d.ReleaseTitle,
		Indexer: d.Indexer, Quality: d.Quality, Score: d.Score, Message: message}
	if err := s.history.Add(ctx, ev, grab.EventData{}); err != nil {
		s.logger.Error("couldn't record a download in History", "release", d.ReleaseTitle, "error", err)
	}
}

// RetryImport tries a failed import again.
func (s *Service) RetryImport(ctx context.Context, id int64) error {
	if s.Moving() {
		return errors.New("imports wait while torrents move to another download client (System → Tasks)")
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	d, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	if d.Status != StatusImportFailed {
		return errors.New("only a failed import can be retried")
	}
	c, err := s.client()
	if err != nil {
		return err
	}
	ts, err := c.Torrents(ctx, d.Category)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(ts, func(t Torrent) bool { return t.Hash == d.Hash })
	if i < 0 {
		return errors.New(c.Name() + " no longer has this download")
	}
	if !s.importOne(ctx, d, ts[i]) {
		return errors.New(d.Message)
	}
	return nil
}

// Pause stops a download in the download client. The download stays in the queue,
// still "downloading" as far as Reelkeeper's records go.
func (s *Service) Pause(ctx context.Context, id int64) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	c, d, t, err := s.unfinished(ctx, id)
	if err != nil {
		return err
	}
	if t.Paused() {
		return errors.New("this download is already paused")
	}
	// Cleanup lifts the client's seeding limits on its first pass over a
	// download, and that also starts the torrent. Do it now, before
	// stopping, so no later pass can quietly resume a paused download.
	if !d.LimitsSet {
		if err := c.NoShareLimits(ctx, d.Category, []string{d.Hash}); err != nil {
			return err
		}
		if _, err := s.db.DB.NewUpdate().Model((*Download)(nil)).Set("limits_set = TRUE").
			Where("id = ?", d.ID).Exec(ctx); err != nil {
			return err
		}
	}
	if err := c.Stop(ctx, d.Category, []string{d.Hash}); err != nil {
		return err
	}
	s.record(ctx, d, grab.EventPaused, "Paused by you.")
	return nil
}

// Resume starts a paused download again.
func (s *Service) Resume(ctx context.Context, id int64) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	c, d, t, err := s.unfinished(ctx, id)
	if err != nil {
		return err
	}
	if !t.Paused() {
		return errors.New("this download isn't paused")
	}
	if err := c.Start(ctx, d.Category, []string{d.Hash}); err != nil {
		return err
	}
	s.record(ctx, d, grab.EventResumed, "Resumed by you.")
	return nil
}

// MoveToTop puts a download first in the download client's queue. It only
// matters while the client limits how many torrents download at once.
func (s *Service) MoveToTop(ctx context.Context, id int64) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	c, d, t, err := s.unfinished(ctx, id)
	if err != nil {
		return err
	}
	if t.Paused() {
		return errors.New("resume this download first")
	}
	if err := c.TopPriority(ctx, d.Category, []string{d.Hash}); err != nil {
		return err
	}
	s.record(ctx, d, grab.EventMovedToTop, "Moved to the top of "+c.Name()+"'s queue by you.")
	return nil
}

// unfinished finds a download that is still being downloaded, with the
// client and its torrent: what Pause, Resume and MoveToTop work on. The
// caller holds syncMu.
func (s *Service) unfinished(ctx context.Context, id int64) (Client, *Download, Torrent, error) {
	d, err := s.get(ctx, id)
	if err != nil {
		return nil, nil, Torrent{}, err
	}
	if err := movingError(d); err != nil {
		return nil, nil, Torrent{}, err
	}
	switch d.Status {
	case StatusDownloading:
	case StatusImportFailed:
		return nil, nil, Torrent{}, errors.New("a failed import has nothing left to download")
	default:
		return nil, nil, Torrent{}, errors.New("this download has already finished")
	}
	c, err := s.client()
	if err != nil {
		return nil, nil, Torrent{}, err
	}
	ts, err := c.Torrents(ctx, d.Category)
	if err != nil {
		return nil, nil, Torrent{}, err
	}
	i := slices.IndexFunc(ts, func(t Torrent) bool { return t.Hash == d.Hash })
	if i < 0 {
		return nil, nil, Torrent{}, errors.New(c.Name() + " no longer has this download")
	}
	if ts[i].Done() {
		return nil, nil, Torrent{}, errors.New("this download has already finished")
	}
	return c, d, ts[i], nil
}

func (s *Service) get(ctx context.Context, id int64) (*Download, error) {
	d := new(Download)
	err := s.db.DB.NewSelect().Model(d).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

// ErrNotFound means there's no such download.
var ErrNotFound = errors.New("no such download")

// RemoveOptions say what else to do when removing a download.
type RemoveOptions struct {
	// Blocklist the release, so it's never grabbed again.
	Blocklist bool
	Reason    string
}

// Remove removes a download from the download client, with its
// downloaded data (it was never imported, or the import failed), and
// optionally blocklists it. The caller decides whether to search again.
func (s *Service) Remove(ctx context.Context, id int64, opt RemoveOptions) (*Download, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	d, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := movingError(d); err != nil {
		return nil, err
	}
	if d.Status != StatusDownloading && d.Status != StatusImportFailed {
		return nil, errors.New("only downloads still in the queue can be removed")
	}
	c, err := s.client()
	if err != nil {
		return nil, err
	}
	ts, err := c.Torrents(ctx, d.Category)
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc(ts, func(t Torrent) bool { return t.Hash == d.Hash }) {
		if err := c.Remove(ctx, d.Category, []string{d.Hash}, true); err != nil {
			return nil, err
		}
	}
	msg := "Removed from the queue."
	if opt.Blocklist && d.TitleID != nil {
		reason := opt.Reason
		if reason == "" {
			reason = "Removed from the queue"
		}
		// The user's own choice: it stays blocked until they unblock it.
		if err := s.AddToBlocklist(ctx, BlockedRelease{TitleID: *d.TitleID, ReleaseTitle: d.ReleaseTitle, Hash: d.Hash,
			Indexer: d.Indexer, Quality: d.Quality, Reason: reason, Rule: BlockRemoved}); err != nil {
			return nil, err
		}
		msg = "Removed from the queue and blocklisted."
	}
	s.finish(ctx, d, StatusRemoved, msg, grab.EventRemoved)
	return d, nil
}

// RemoveForTitle removes every download still in the queue for a title,
// with its downloaded data, before the title is deleted. Imported
// downloads are left seeding: they're the cleanup engine's to remove.
func (s *Service) RemoveForTitle(ctx context.Context, titleID int64) error {
	return s.removeWhere(ctx, "title_id = ?", titleID)
}

// RemoveForVersion is RemoveForTitle for one version of a movie.
func (s *Service) RemoveForVersion(ctx context.Context, versionID int64) error {
	return s.removeWhere(ctx, "version_id = ?", versionID)
}

func (s *Service) removeWhere(ctx context.Context, where string, arg int64) error {
	var ids []int64
	if err := s.db.DB.NewSelect().Model((*Download)(nil)).Column("id").
		Where(where, arg).
		Where("status IN (?)", bun.List([]string{StatusDownloading, StatusImportFailed})).
		Scan(ctx, &ids); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.Remove(ctx, id, RemoveOptions{}); err != nil {
			return err
		}
	}
	return nil
}

var _ bun.BeforeAppendModelHook = (*Download)(nil)

// BeforeAppendModel fills in the JSON column when a record is written
// without it (Postgres refuses an empty string as JSON).
func (d *Download) BeforeAppendModel(_ context.Context, _ bun.Query) error {
	if d.CleanupTrack == "" {
		d.CleanupTrack = "{}"
	}
	return nil
}

func ptrVal(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
