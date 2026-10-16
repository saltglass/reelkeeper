// Package migrate brings over what Radarr knows at cutover, read-only:
// which movies are wanted and monitored, Seerr's tags, the history of what
// was grabbed and imported, and the blocklist. Reelkeeper's own scan has already
// found the files; this adds what only Radarr knows, and reports where the
// two disagree, so you can check before switching Radarr off.
//
// Two steps, both reading Radarr only: Preview builds the report and says
// what an import would do, changing nothing; Import does it. Importing
// twice is safe: history and blocklist entries are only brought over once.
//
// Sonarr's import (sonarr.go) works the same way for TV; the two share the
// HTTP code, the error type and the history and blocklist bookkeeping.
package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/store"
)

// Library is what the import needs from the library.
type Library interface {
	RootFolders(ctx context.Context) ([]library.RootFolder, error)
	ProfileMovies(ctx context.Context, profileID int64, f library.ProfileMovieFilter) ([]library.ProfileMovie, error)
	SeriesLibrary
	EnsureVersion(ctx context.Context, tmdbID int, profileID, rootID int64, folder string, monitored bool) (*library.Version, bool, error)
	SetMonitored(ctx context.Context, titleID, versionID int64, monitored bool) error
	CreateTag(ctx context.Context, label string) (*library.Tag, error)
	VersionTags(ctx context.Context, versionIDs []int64) (map[int64][]int64, error)
	SetVersionTags(ctx context.Context, versionID int64, tagIDs []int64) error
}

// Service runs imports.
type Service struct {
	db        *store.Store
	lib       Library
	history   *grab.History
	blocklist func(ctx context.Context, b download.BlockedRelease) error
	client    *http.Client
}

// New returns the import service.
func New(db *store.Store, lib Library, history *grab.History, blocklist func(context.Context, download.BlockedRelease) error) *Service {
	return &Service{db: db, lib: lib, history: history, blocklist: blocklist, client: &http.Client{Timeout: 60 * time.Second}}
}

// Options say which Radarr or Sonarr to read and which Reelkeeper profile
// its titles belong to (Radarr → HD, the Remux instance → Remux, Sonarr →
// the TV profile).
type Options struct {
	URL       string
	APIKey    string
	ProfileID int64
}

// Issue is a place where Radarr and Reelkeeper disagree.
type Issue struct {
	TMDBID int    `json:"tmdbId"`
	Title  string `json:"title"`
	Year   int    `json:"year"`
	// Kind: missing-in-reelkeeper, quality-differs, other-file, only-in-reelkeeper,
	// no-media-folder.
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// Failures on the other app's side (unreachable, key refused, not the app
// it should be): the user can fix those, unlike Reelkeeper's own errors.
// ErrArr matches any of them; ErrRadarr and ErrSonarr only their own.
var (
	ErrArr    = errors.New("arr")
	ErrRadarr = errors.New("radarr")
	ErrSonarr = errors.New("sonarr")
)

// arrErr wraps a failure on Radarr's or Sonarr's side, keeping the plain
// message, so errors.Is(err, ErrArr) holds (and ErrRadarr or ErrSonarr for
// the app it was about).
type arrErr struct{ app, msg string }

func (e arrErr) Error() string { return e.msg }
func (e arrErr) Is(target error) bool {
	return target == ErrArr || (e.app == "Radarr" && target == ErrRadarr) || (e.app == "Sonarr" && target == ErrSonarr)
}

// arrFailure words a failure on app's side ("Radarr" or "Sonarr").
func arrFailure(app, format string, args ...any) error {
	return arrErr{app, fmt.Sprintf(format, args...)}
}

// Report is what a preview found (and, after an import, what it did).
type Report struct {
	// Instance is Radarr's instance name ("Radarr", "Remux") and Version
	// its version.
	Instance  string  `json:"instance"`
	Version   string  `json:"version"`
	Movies    int     `json:"movies"`
	WithFile  int     `json:"withFile"`
	Monitored int     `json:"monitored"`
	Wanted    int     `json:"wanted"` // monitored, without a file in Radarr
	ToAdd     int     `json:"toAdd"`  // not in this Reelkeeper profile yet
	History   int     `json:"history"`
	Blocklist int     `json:"blocklist"`
	Issues    []Issue `json:"issues"`

	Imported       bool `json:"imported"`
	Added          int  `json:"added"`
	MonitorChanged int  `json:"monitorChanged"`
	HistoryAdded   int  `json:"historyAdded"`
	BlocklistAdded int  `json:"blocklistAdded"`
}

// Radarr's answers, only the fields used. Never the history's download
// URLs: they carry Prowlarr's API key.
type rMovie struct {
	ID             int64  `json:"id"`
	TmdbID         int    `json:"tmdbId"`
	Title          string `json:"title"`
	Year           int    `json:"year"`
	Monitored      bool   `json:"monitored"`
	HasFile        bool   `json:"hasFile"`
	Path           string `json:"path"`
	RootFolderPath string `json:"rootFolderPath"`
	Tags           []int  `json:"tags"`
	MovieFile      *struct {
		RelativePath string `json:"relativePath"`
		Quality      struct {
			Quality struct {
				Name string `json:"name"`
			} `json:"quality"`
		} `json:"quality"`
	} `json:"movieFile"`
}

type rTag struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

type rRecord struct {
	ID          int64  `json:"id"`
	MovieID     int64  `json:"movieId"`
	SourceTitle string `json:"sourceTitle"`
	EventType   string `json:"eventType"`
	Date        string `json:"date"`
	Message     string `json:"message"`
	Indexer     string `json:"indexer"`
	Quality     struct {
		Quality struct {
			Name string `json:"name"`
		} `json:"quality"`
	} `json:"quality"`
	Data struct {
		Indexer         string `json:"indexer"`
		Size            string `json:"size"`
		ImportedPath    string `json:"importedPath"`
		Reason          string `json:"reason"`
		Message         string `json:"message"`
		TorrentInfoHash string `json:"torrentInfoHash"`
	} `json:"data"`
	TorrentInfoHash string `json:"torrentInfoHash"`
}

type page[T any] struct {
	TotalRecords int `json:"totalRecords"`
	Records      []T `json:"records"`
}

// get reads one answer from Radarr's or Sonarr's v3 API (app is "Radarr" or
// "Sonarr", used in the messages).
func (s *Service) get(ctx context.Context, app string, o Options, p string, out any) error {
	u := strings.TrimRight(o.URL, "/") + "/api/v3/" + p
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", o.APIKey)
	resp, err := s.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the address carries no secret, but keep messages short
		}
		return arrFailure(app, "can't reach %s: %v", app, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return arrFailure(app, "%s refused the API key", app)
	case resp.StatusCode != http.StatusOK:
		return arrFailure(app, "%s answered %s for %s", app, resp.Status, strings.SplitN(p, "?", 2)[0])
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<20)).Decode(out); err != nil {
		return arrFailure(app, "%s's answer for %s didn't make sense: %v", app, strings.SplitN(p, "?", 2)[0], err)
	}
	return nil
}

// all reads every page of a paged Radarr or Sonarr list.
func all[T any](ctx context.Context, s *Service, app string, o Options, p string) ([]T, error) {
	var out []T
	for n := 1; ; n++ {
		var pg page[T]
		if err := s.get(ctx, app, o, fmt.Sprintf("%s?page=%d&pageSize=500&sortKey=date&sortDirection=ascending", p, n), &pg); err != nil {
			return nil, err
		}
		out = append(out, pg.Records...)
		if len(pg.Records) == 0 || len(out) >= pg.TotalRecords {
			return out, nil
		}
	}
}

type snapshot struct {
	instance  string
	version   string
	movies    []rMovie
	tags      map[int]string
	history   []rRecord
	blocklist []rRecord
}

func (s *Service) read(ctx context.Context, o Options) (*snapshot, error) {
	var status struct {
		AppName      string `json:"appName"`
		InstanceName string `json:"instanceName"`
		Version      string `json:"version"`
	}
	if err := s.get(ctx, "Radarr", o, "system/status", &status); err != nil {
		return nil, err
	}
	if status.AppName != "Radarr" {
		return nil, arrFailure("Radarr", "That address is %s, not Radarr", firstNonEmpty(status.AppName, "something else"))
	}
	snap := &snapshot{instance: firstNonEmpty(status.InstanceName, "Radarr"), version: status.Version, tags: map[int]string{}}
	if err := s.get(ctx, "Radarr", o, "movie", &snap.movies); err != nil {
		return nil, err
	}
	var tags []rTag
	if err := s.get(ctx, "Radarr", o, "tag", &tags); err != nil {
		return nil, err
	}
	for _, t := range tags {
		snap.tags[t.ID] = t.Label
	}
	var err error
	if snap.history, err = all[rRecord](ctx, s, "Radarr", o, "history"); err != nil {
		return nil, err
	}
	if snap.blocklist, err = all[rRecord](ctx, s, "Radarr", o, "blocklist"); err != nil {
		return nil, err
	}
	return snap, nil
}

// Preview reads Radarr and reports, changing nothing.
func (s *Service) Preview(ctx context.Context, o Options) (*Report, error) {
	snap, err := s.read(ctx, o)
	if err != nil {
		return nil, err
	}
	return s.compare(ctx, o, snap)
}

// compare builds the report.
func (s *Service) compare(ctx context.Context, o Options, snap *snapshot) (*Report, error) {
	rep := &Report{Instance: snap.instance, Version: snap.version, Movies: len(snap.movies), History: len(snap.history), Blocklist: len(snap.blocklist), Issues: []Issue{}}
	pms, err := s.lib.ProfileMovies(ctx, o.ProfileID, library.ProfileMovieFilter{})
	if err != nil {
		return nil, err
	}
	reelkeeper := map[int]library.ProfileMovie{}
	for _, pm := range pms {
		reelkeeper[pm.Title.TMDBID] = pm
	}
	roots, err := s.lib.RootFolders(ctx)
	if err != nil {
		return nil, err
	}
	inRadarr := map[int]bool{}
	for _, m := range snap.movies {
		inRadarr[m.TmdbID] = true
		if m.HasFile {
			rep.WithFile++
		}
		if m.Monitored {
			rep.Monitored++
			if !m.HasFile {
				rep.Wanted++
			}
		}
		issue := func(kind, detail string) {
			rep.Issues = append(rep.Issues, Issue{TMDBID: m.TmdbID, Title: m.Title, Year: m.Year, Kind: kind, Detail: detail})
		}
		pm, ok := reelkeeper[m.TmdbID]
		if !ok {
			rep.ToAdd++
			if rootFor(roots, m.RootFolderPath) == nil {
				issue("no-media-folder", fmt.Sprintf("Radarr keeps it in %s, which isn't one of Reelkeeper's movie folders, so it can't be brought over.", m.RootFolderPath))
			} else if m.HasFile {
				issue("missing-in-reelkeeper", fmt.Sprintf("Radarr has %s (%s); Reelkeeper's scan didn't find it.", radarrFile(m), m.MovieFile.Quality.Quality.Name))
			}
			continue
		}
		switch {
		case m.HasFile && len(pm.Files) == 0:
			issue("missing-in-reelkeeper", fmt.Sprintf("Radarr has %s (%s); Reelkeeper has no file for it.", radarrFile(m), m.MovieFile.Quality.Quality.Name))
		case m.HasFile && len(pm.Files) > 0:
			want := radarrFile(m)
			found := false
			for _, f := range pm.Files {
				if path.Join(pm.Root.Path, f.Path) == want {
					found = true
					if q := m.MovieFile.Quality.Quality.Name; q != "" && f.Quality != "" && q != f.Quality {
						issue("quality-differs", fmt.Sprintf("Radarr says %s, Reelkeeper says %s (Reelkeeper trusts the file's own headers over its name).", q, f.Quality))
					}
				}
			}
			if !found {
				issue("other-file", fmt.Sprintf("Radarr's file is %s; Reelkeeper's is %s.", want, path.Join(pm.Root.Path, pm.Files[0].Path)))
			}
		}
	}
	for tmdb, pm := range reelkeeper {
		if !inRadarr[tmdb] {
			rep.Issues = append(rep.Issues, Issue{TMDBID: tmdb, Title: pm.Title.Title, Year: pm.Title.Year, Kind: "only-in-reelkeeper",
				Detail: "Reelkeeper has it in this profile; Radarr doesn't."})
		}
	}
	return rep, nil
}

func radarrFile(m rMovie) string {
	if m.MovieFile == nil {
		return m.Path
	}
	return path.Join(m.Path, m.MovieFile.RelativePath)
}

func rootFor(roots []library.RootFolder, p string) *library.RootFolder {
	p = strings.TrimRight(p, "/")
	for i := range roots {
		if roots[i].MediaType == library.Movie && strings.TrimRight(roots[i].Path, "/") == p {
			return &roots[i]
		}
	}
	return nil
}

// Import brings Radarr's movies, monitoring, tags, history and blocklist
// over into the profile, and returns the report.
func (s *Service) Import(ctx context.Context, o Options) (*Report, error) {
	snap, err := s.read(ctx, o)
	if err != nil {
		return nil, err
	}
	rep, err := s.compare(ctx, o, snap)
	if err != nil {
		return nil, err
	}
	roots, err := s.lib.RootFolders(ctx)
	if err != nil {
		return nil, err
	}
	source := snap.instance
	versionOf := map[int64]*library.Version{} // Radarr movie id → Reelkeeper version
	titleOf := map[int64]int64{}
	for _, m := range snap.movies {
		root := rootFor(roots, m.RootFolderPath)
		if root == nil || m.TmdbID <= 0 {
			continue
		}
		folder := strings.TrimPrefix(strings.TrimPrefix(m.Path, strings.TrimRight(root.Path, "/")), "/")
		v, created, err := s.lib.EnsureVersion(ctx, m.TmdbID, o.ProfileID, root.ID, folder, m.Monitored)
		if err != nil {
			return nil, fmt.Errorf("%s (%d): %w", m.Title, m.Year, err)
		}
		if created {
			rep.Added++
		} else if v.Monitored != m.Monitored {
			if err := s.lib.SetMonitored(ctx, v.TitleID, v.ID, m.Monitored); err != nil {
				return nil, err
			}
			rep.MonitorChanged++
		}
		versionOf[m.ID], titleOf[m.ID] = v, v.TitleID
		if err := s.importTags(ctx, v.ID, m.Tags, snap.tags); err != nil {
			return nil, err
		}
	}
	for _, r := range snap.history {
		v := versionOf[r.MovieID]
		if v == nil {
			continue
		}
		fresh, err := s.claim(ctx, source+"/history", r.ID)
		if err != nil {
			return nil, err
		}
		if !fresh {
			continue
		}
		at, _ := time.Parse(time.RFC3339, r.Date)
		tid, vid := v.TitleID, v.ID
		ev := &grab.Event{CreatedAt: at.UTC(), TitleID: &tid, VersionID: &vid, Event: grab.EventFromRadarr,
			SourceTitle: r.SourceTitle, Indexer: r.Data.Indexer, Quality: r.Quality.Quality.Name,
			Message: historyMessage(source, r)}
		if err := s.history.Add(ctx, ev, grab.EventData{Trigger: "radarr"}); err != nil {
			return nil, err
		}
		rep.HistoryAdded++
	}
	for _, r := range snap.blocklist {
		tid, ok := titleOf[r.MovieID]
		if !ok {
			continue
		}
		fresh, err := s.claim(ctx, source+"/blocklist", r.ID)
		if err != nil {
			return nil, err
		}
		if !fresh {
			continue
		}
		reason := "Blocklisted in " + source
		if r.Message != "" {
			reason += ": " + r.Message
		}
		if err := s.blocklist(ctx, download.BlockedRelease{TitleID: tid, ReleaseTitle: r.SourceTitle,
			Hash: strings.ToLower(r.TorrentInfoHash), Indexer: r.Indexer, Quality: r.Quality.Quality.Name, Reason: reason, Rule: download.BlockRadarr}); err != nil {
			return nil, err
		}
		rep.BlocklistAdded++
	}
	rep.Imported = true
	return rep, nil
}

// importTags adds Radarr's tags (by label) to the version's own.
func (s *Service) importTags(ctx context.Context, versionID int64, ids []int, labels map[int]string) error {
	if len(ids) == 0 {
		return nil
	}
	have, err := s.lib.VersionTags(ctx, []int64{versionID})
	if err != nil {
		return err
	}
	tags := have[versionID]
	for _, id := range ids {
		label, ok := labels[id]
		if !ok {
			continue
		}
		t, err := s.lib.CreateTag(ctx, label)
		if err != nil {
			return err
		}
		tags = append(tags, t.ID)
	}
	return s.lib.SetVersionTags(ctx, versionID, tags)
}

// claim records that a record is being brought over, and reports whether
// it's new (false: an earlier import already did).
func (s *Service) claim(ctx context.Context, source string, id int64) (bool, error) {
	res, err := s.db.DB.NewRaw("INSERT INTO imported_records (source, external_id) VALUES (?, ?) ON CONFLICT DO NOTHING", source, id).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// historyMessage words a Radarr history record for Reelkeeper's History.
func historyMessage(source string, r rRecord) string {
	what := map[string]string{
		"grabbed":                "grabbed",
		"downloadFolderImported": "imported",
		"movieFileImported":      "imported",
		"downloadFailed":         "saw a download fail:",
		"movieFileDeleted":       "deleted the file",
		"movieFileRenamed":       "renamed the file",
		"downloadIgnored":        "ignored the download",
		"movieFolderImported":    "imported the folder",
	}[r.EventType]
	if what == "" {
		what = r.EventType
	}
	msg := fmt.Sprintf("%s %s %s", source, what, r.SourceTitle)
	if r.Data.Indexer != "" && r.EventType == "grabbed" {
		msg += " from " + r.Data.Indexer
	}
	if r.Data.ImportedPath != "" {
		msg += " as " + r.Data.ImportedPath
	}
	if reason := firstNonEmpty(r.Data.Reason, r.Data.Message); reason != "" {
		msg += " (" + reason + ")"
	}
	return msg + "."
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
