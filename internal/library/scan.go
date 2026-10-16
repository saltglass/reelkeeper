package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/mediainfo"
	"github.com/saltglass/reelkeeper/internal/metadata"
	"github.com/saltglass/reelkeeper/internal/parser"
)

// A scan walks a root folder and brings the database in line with it:
//
//  1. Group video files into units, one per movie: a folder, a loose file,
//     or each sub-folder of a collection folder ("Batman (REMUX)/…").
//  2. Units whose files are unchanged since the last scan are skipped.
//  3. New or changed files are probed (name + headers) and the unit is
//     identified: ID tags in names first, then a TMDB search.
//  4. Identified units become titles, versions and files; doubtful ones
//     become review items. Files that disappeared are marked missing,
//     never deleted from the database automatically.
//
// The scan only reads the disk. It never renames, moves or deletes files.

// Progress reports scan progress (0-100) and a plain-English message.
type Progress func(percent int, message string)

// ScanResult summarises a scan.
type ScanResult struct {
	Units     int // movies or series (folders/files) found
	Unchanged int // skipped because nothing changed
	Added     int // newly identified
	Updated   int // files added to or changed in known titles
	Review    int // waiting in the review screen
	Missing   int // files gone since the last scan
	Failed    int // couldn't be processed (e.g. TMDB unreachable)

	noun string // "movies" or "series", for Summary
}

// Summary is a plain-English one-liner.
func (r ScanResult) Summary() string {
	noun := r.noun
	if noun == "" {
		noun = "movies"
	}
	parts := []string{fmt.Sprintf("%d %s found", r.Units, noun)}
	if r.Added > 0 {
		parts = append(parts, fmt.Sprintf("%d added", r.Added))
	}
	if r.Updated > 0 {
		parts = append(parts, fmt.Sprintf("%d updated", r.Updated))
	}
	if r.Review > 0 {
		parts = append(parts, fmt.Sprintf("%d need review", r.Review))
	}
	if r.Missing > 0 {
		parts = append(parts, fmt.Sprintf("%d files missing", r.Missing))
	}
	if r.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d couldn't be checked", r.Failed))
	}
	return strings.Join(parts, ", ")
}

type unitFile struct {
	Path    string // relative to the root
	Size    int64
	ModTime time.Time
}

type unit struct {
	Path  string // folder or loose file, relative to the root
	Files []unitFile
}

// loose reports whether the unit is a single file directly in the root
// folder rather than a folder.
func (u unit) loose() bool { return len(u.Files) == 1 && u.Files[0].Path == u.Path }

// Folders that hold extras, previews or system files, never movies.
var skipDirs = map[string]bool{
	"extras": true, "featurettes": true, "behind the scenes": true, "deleted scenes": true,
	"interviews": true, "scenes": true, "shorts": true, "trailers": true, "other": true,
	"plex versions": true, "@eadir": true, ".appledouble": true, "lost+found": true,
	"$recycle.bin": true, "subs": true, "subtitles": true, "sample": true, "samples": true,
}

// Plex-style extras: "Movie (2020)-trailer.mkv" and friends.
var extraSuffix = regexp.MustCompile(`(?i)-(trailer|featurette|behindthescenes|deleted|interview|scene|short|other)$`)
var sampleWord = regexp.MustCompile(`(?i)(^|[\s._\-\[(])sample([\s._\-\])]|$)`)

const maxDepth = 4

// discover walks a root folder and groups its video files into units.
func discover(root string) ([]unit, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var units []unit
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(root, name)
		st, err := os.Stat(full) // follows symlinks
		if err != nil || strings.HasPrefix(name, ".") {
			continue
		}
		if !st.IsDir() {
			if f, ok := videoFile(name, st); ok {
				f.Path = name
				units = append(units, unit{Path: name, Files: []unitFile{f}})
			}
			continue
		}
		if skipDirs[strings.ToLower(name)] {
			continue
		}
		if subs := collectionParts(full); len(subs) > 0 {
			for _, sub := range subs {
				rel := filepath.Join(name, sub)
				if files := videosUnder(root, rel, 1); len(files) > 0 {
					units = append(units, unit{Path: rel, Files: files})
				}
			}
			continue
		}
		if files := videosUnder(root, name, 1); len(files) > 0 {
			units = append(units, unit{Path: name, Files: files})
		}
	}
	sort.Slice(units, func(i, j int) bool { return units[i].Path < units[j].Path })
	return units, nil
}

// collectionParts returns the sub-folders of dir if dir is a collection
// ("Batman (REMUX)" holding "Batman Begins (2005)", "The Dark Knight
// (2008)"...): its own name has no year, and at least two sub-folders name
// different movies with years.
func collectionParts(dir string) []string {
	if parser.Parse(filepath.Base(dir)).Year != 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var subs []string
	seen := map[string]bool{}
	for _, e := range entries {
		st, err := os.Stat(filepath.Join(dir, e.Name()))
		if err != nil || !st.IsDir() || skipDirs[strings.ToLower(e.Name())] {
			continue
		}
		r := parser.Parse(e.Name())
		if r.Year == 0 {
			continue
		}
		key := normalize(r.Title) + fmt.Sprint(r.Year)
		if !seen[key] {
			seen[key] = true
			subs = append(subs, e.Name())
		}
	}
	if len(subs) < 2 {
		return nil
	}
	return subs
}

// videosUnder lists video files below root/rel, skipping extras.
func videosUnder(root, rel string, depth int) []unitFile {
	if depth > maxDepth {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(root, rel))
	if err != nil {
		return nil
	}
	var out []unitFile
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		p := filepath.Join(rel, name)
		st, err := os.Stat(filepath.Join(root, p))
		if err != nil {
			continue
		}
		if st.IsDir() {
			if !skipDirs[strings.ToLower(name)] {
				out = append(out, videosUnder(root, p, depth+1)...)
			}
			continue
		}
		if f, ok := videoFile(name, st); ok {
			f.Path = p
			out = append(out, f)
		}
	}
	return out
}

// videoFile reports whether a file is a main video (not a sample, trailer
// or other extra).
func videoFile(name string, st os.FileInfo) (unitFile, bool) {
	if !parser.IsVideo(name) || strings.HasPrefix(name, "._") {
		return unitFile{}, false
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if extraSuffix.MatchString(base) {
		return unitFile{}, false
	}
	if sampleWord.MatchString(base) && st.Size() < 500<<20 {
		return unitFile{}, false
	}
	return unitFile{Size: st.Size(), ModTime: st.ModTime().UTC().Truncate(time.Second)}, true
}

// errStop aborts a scan (no TMDB key, key rejected).
type errStop struct{ error }

// Scan scans one root folder.
func (s *Service) Scan(ctx context.Context, rootID int64, progress Progress) (ScanResult, error) {
	var res ScanResult
	root, err := s.RootFolder(ctx, rootID)
	if err != nil {
		return res, err
	}
	if progress == nil {
		progress = func(int, string) {}
	}
	progress(0, "Looking through "+root.Path)
	discoverUnits, scanOne := discover, s.scanUnit
	res.noun = "movies"
	if root.MediaType == Series {
		discoverUnits, scanOne = discoverSeries, s.scanSeriesUnit
		res.noun = "series"
	}
	units, err := discoverUnits(root.Path)
	if err != nil {
		return res, fmt.Errorf("can't read %s: %w", root.Path, err)
	}
	// Folders of movies deleted from Reelkeeper with their files kept.
	skip, err := s.skipped(ctx, root.ID)
	if err != nil {
		return res, err
	}
	units = slices.DeleteFunc(units, func(u unit) bool { return skip[u.Path] })
	res.Units = len(units)

	// What the database already knows about this root.
	var known []MediaFile
	if err := s.db.DB.NewSelect().Model(&known).Where("root_folder_id = ?", root.ID).Scan(ctx); err != nil {
		return res, err
	}
	byPath := make(map[string]*MediaFile, len(known))
	for i := range known {
		byPath[known[i].Path] = &known[i]
	}
	var items []ScanItem
	if err := s.db.DB.NewSelect().Model(&items).Where("root_folder_id = ?", root.ID).Scan(ctx); err != nil {
		return res, err
	}
	itemByPath := map[string]*ScanItem{}
	for i := range items {
		itemByPath[items[i].Path] = &items[i]
	}

	seen := map[string]bool{}
	for i, u := range units {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		progress(i*100/max(len(units), 1), fmt.Sprintf("Checking %s (%d of %d)", u.Path, i+1, len(units)))
		for _, f := range u.Files {
			seen[f.Path] = true
		}
		outcome, err := scanOne(ctx, root, u, byPath, itemByPath[u.Path])
		var stop errStop
		if errors.As(err, &stop) {
			return res, stop.error
		}
		if err != nil {
			s.logger.Warn("couldn't process a folder", "path", u.Path, "error", err)
			res.Failed++
			continue
		}
		switch outcome {
		case outUnchanged:
			res.Unchanged++
		case outAdded:
			res.Added++
		case outUpdated:
			res.Updated++
		case outReview:
			res.Review++
		}
	}

	// Files that vanished: mark missing; files that came back: unmark.
	now := s.now().UTC()
	for _, f := range known {
		switch {
		case !seen[f.Path] && f.MissingSince == nil:
			f.MissingSince = &now
			if _, err := s.db.DB.NewUpdate().Model(&f).Column("missing_since").WherePK().Exec(ctx); err != nil {
				return res, err
			}
			res.Missing++
		case !seen[f.Path]:
			res.Missing++
		}
	}
	// Review items whose folder is gone.
	for _, it := range items {
		gone := true
		for _, f := range it.FileList() {
			if seen[f] {
				gone = false
				break
			}
		}
		if gone {
			if _, err := s.db.DB.NewDelete().Model(&it).WherePK().Exec(ctx); err != nil {
				return res, err
			}
		}
	}
	root.LastScanAt = &now
	if _, err := s.db.DB.NewUpdate().Model(root).Column("last_scan_at").WherePK().Exec(ctx); err != nil {
		return res, err
	}
	progress(100, res.Summary())
	if res.Added+res.Updated+res.Missing > 0 || res.Review > 0 {
		s.onChange()
	}
	return res, nil
}

type outcome int

const (
	outUnchanged outcome = iota
	outAdded
	outUpdated
	outReview
)

// scanUnit processes one movie folder (or loose file).
func (s *Service) scanUnit(ctx context.Context, root *RootFolder, u unit, byPath map[string]*MediaFile, item *ScanItem) (outcome, error) {
	if item != nil && item.Status == Ignored {
		return outUnchanged, nil
	}

	changed, versionID, err := s.changedFiles(ctx, u, byPath)
	if err != nil || len(changed) == 0 {
		return outUnchanged, err
	}
	// A doubtful folder whose files haven't changed stays in review
	// without asking TMDB again.
	if item != nil && versionID == 0 && sameFiles(item.FileList(), u.Files) {
		return outReview, nil
	}

	probes := make([]probed, 0, len(changed))
	for _, f := range changed {
		probes = append(probes, probe(root.Path, f))
	}

	// Files added to a movie Reelkeeper already knows.
	if versionID != 0 {
		for _, p := range probes {
			if err := s.saveFile(ctx, root.ID, versionID, p, byPath[p.file.Path]); err != nil {
				return 0, err
			}
		}
		return outUpdated, nil
	}

	match, cands, reason, err := s.identify(ctx, u, probes)
	if err != nil {
		return 0, err
	}
	if match == 0 {
		return outReview, s.saveReview(ctx, root.ID, u, cands, reason, item)
	}

	title, err := s.ensureTitle(ctx, match)
	if err != nil {
		return 0, err
	}
	folder := u.Path
	if u.loose() {
		folder = "" // a file directly in the root folder
	}
	v, conflict, err := s.ensureVersion(ctx, title, root, folder)
	if err != nil {
		return 0, err
	}
	if conflict != "" {
		c := []Candidate{{TMDBID: title.TMDBID, Title: title.Title, Year: title.Year, PosterPath: title.PosterPath, Score: 1}}
		return outReview, s.saveReview(ctx, root.ID, u, c, conflict, item)
	}
	for _, p := range probes {
		if err := s.saveFile(ctx, root.ID, v.ID, p, byPath[p.file.Path]); err != nil {
			return 0, err
		}
	}
	if item != nil {
		if _, err := s.db.DB.NewDelete().Model(item).WherePK().Exec(ctx); err != nil {
			return 0, err
		}
	}
	return outAdded, nil
}

// changedFiles returns a unit's new or changed files, and the version (if
// any) its known files already belong to. Known files that had gone
// missing and are back are marked present again.
func (s *Service) changedFiles(ctx context.Context, u unit, byPath map[string]*MediaFile) ([]unitFile, int64, error) {
	var changed []unitFile
	var versionID int64
	for _, f := range u.Files {
		k := byPath[f.Path]
		if k == nil || k.Size != f.Size || !k.ModTime().Equal(f.ModTime) {
			changed = append(changed, f)
		}
		if k != nil {
			versionID = k.VersionID
			if k.MissingSince != nil {
				k.MissingSince = nil
				if _, err := s.db.DB.NewUpdate().Model(k).Column("missing_since").WherePK().Exec(ctx); err != nil {
					return nil, 0, err
				}
			}
		}
	}
	return changed, versionID, nil
}

// ModTime is the stored modification time (named for symmetry with
// unitFile).
func (f MediaFile) ModTime() time.Time { return f.ModifiedAt.UTC().Truncate(time.Second) }

func sameFiles(stored []string, files []unitFile) bool {
	if len(stored) != len(files) {
		return false
	}
	set := map[string]bool{}
	for _, s := range stored {
		set[s] = true
	}
	for _, f := range files {
		if !set[f.Path] {
			return false
		}
	}
	return true
}

type probed struct {
	file    unitFile
	release parser.Release
	media   *mediainfo.Info
	err     string
	claim   *EpisodeClaim // episodes only
}

func probe(root string, f unitFile) probed {
	p := probed{file: f, release: parser.Parse(filepath.Base(f.Path))}
	info, err := mediainfo.Read(filepath.Join(root, f.Path))
	if err != nil {
		p.err = err.Error()
	} else {
		p.media = info
	}
	return p
}

// identify works out which TMDB movie a unit is. It returns the TMDB ID,
// or 0 with candidates and a reason when it isn't sure.
func (s *Service) identify(ctx context.Context, u unit, probes []probed) (int, []Candidate, string, error) {
	// Hints from the most specific to the least: the folder (for
	// collection parts, the sub-folder), then each file name.
	var hints []parser.Release
	if !u.loose() {
		hints = append(hints, parser.Parse(filepath.Base(u.Path)))
	}
	for _, p := range probes {
		hints = append(hints, p.release)
	}

	// 1. IDs written in names.
	for _, h := range hints {
		if h.TMDBID != 0 {
			return h.TMDBID, nil, "", nil
		}
	}
	for _, h := range hints {
		if h.IMDbID != "" {
			r, err := s.tmdb.FindByIMDb(ctx, h.IMDbID)
			if err == nil {
				return r.ID, nil, "", nil
			}
			if stop := stopping(err); stop != nil {
				return 0, nil, "", stop
			}
		}
	}

	// 2. Search by title and year, folder first.
	var best []Candidate
	var reason string
	tried := map[string]bool{}
	runtime := 0
	for _, p := range probes {
		if p.media != nil && p.media.Duration > 0 {
			runtime = int(p.media.Duration.Minutes())
			break
		}
	}
	for _, h := range hints {
		if h.Title == "" {
			continue
		}
		key := normalize(h.Title) + fmt.Sprint(h.Year)
		if tried[key] {
			continue
		}
		tried[key] = true
		results, err := s.tmdb.SearchMovies(ctx, h.Title, h.Year)
		if stop := stopping(err); stop != nil {
			return 0, nil, "", stop
		}
		if err != nil {
			return 0, nil, "", err
		}
		if len(results) == 0 && h.Year != 0 {
			// Years in folder names are sometimes off; try without.
			if results, err = s.tmdb.SearchMovies(ctx, h.Title, 0); err != nil {
				if stop := stopping(err); stop != nil {
					return 0, nil, "", stop
				}
				return 0, nil, "", err
			}
		}
		cands := rank(h.Title, h.Year, results)
		w, why := decide(cands)
		if w != nil {
			return w.TMDBID, nil, "", nil
		}
		if len(cands) > 0 && runtime > 0 {
			// Two or more close candidates: the file's real runtime and
			// TMDB's alternative titles often settle it.
			if id, ok := s.tiebreak(ctx, h, cands, runtime); ok {
				return id, nil, "", nil
			}
		}
		// Keep the best candidate list and the reason that goes with it.
		if len(best) == 0 || (len(cands) > 0 && cands[0].Score > best[0].Score) {
			best, reason = cands, why
		} else if reason == "" {
			reason = why
		}
	}
	if len(best) > 5 {
		best = best[:5]
	}
	if reason == "" {
		reason = "Couldn't tell which movie this is from its name."
	}
	return 0, best, reason, nil
}

// tiebreak fetches details for the top candidates and rescores them with
// alternative titles, nudged by how well each runtime fits the file.
func (s *Service) tiebreak(ctx context.Context, h parser.Release, cands []Candidate, runtime int) (int, bool) {
	top := cands[:min(3, len(cands))]
	rescored := make([]Candidate, 0, len(top))
	for _, c := range top {
		m, err := s.tmdb.Movie(ctx, c.TMDBID)
		if err != nil {
			return 0, false
		}
		sc := score(h.Title, h.Year, metadata.MovieResult{Title: m.Title, OriginalTitle: m.OriginalTitle, ReleaseDate: m.ReleaseDate}, m.AlternativeTitles...)
		if m.Runtime > 0 {
			diff := float64(abs(m.Runtime-runtime)) / float64(m.Runtime)
			switch {
			case diff <= 0.05:
				sc += 0.08
			case diff > 0.25:
				sc -= 0.15
			}
		}
		c.Score = min(sc, 1)
		rescored = append(rescored, c)
	}
	sort.SliceStable(rescored, func(i, j int) bool { return rescored[i].Score > rescored[j].Score })
	if w, _ := decide(rescored); w != nil {
		return w.TMDBID, true
	}
	return 0, false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// stopping turns key problems into errors that abort the whole scan: no
// point asking TMDB about 300 more folders with a missing key.
func stopping(err error) error {
	if errors.Is(err, metadata.ErrNoKey) || errors.Is(err, metadata.ErrBadKey) {
		return errStop{err}
	}
	return nil
}

// ensureTitle returns the title for a TMDB movie, fetching its details the
// first time.
func (s *Service) ensureTitle(ctx context.Context, tmdbID int) (*Title, error) {
	t := new(Title)
	err := s.db.DB.NewSelect().Model(t).Where("media_type = ? AND tmdb_id = ?", Movie, tmdbID).Scan(ctx)
	if err == nil {
		return t, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	m, err := s.tmdb.Movie(ctx, tmdbID)
	if stop := stopping(err); stop != nil {
		return nil, stop
	}
	if err != nil {
		return nil, err
	}
	t = titleFromMovie(m, s.now().UTC())
	if _, err := s.db.DB.NewInsert().Model(t).Exec(ctx); err != nil {
		return nil, err
	}
	return t, nil
}

func titleFromMovie(m *metadata.Movie, now time.Time) *Title {
	return &Title{
		MediaType: Movie, TMDBID: m.ID, IMDbID: m.IMDbID, TVDBID: m.TVDBID, Title: m.Title,
		OriginalTitle: m.OriginalTitle, OriginalLanguage: m.OriginalLanguage, SortTitle: sortTitle(m.Title),
		Year: m.Year(), Overview: m.Overview, Runtime: m.Runtime, Status: m.Status, Genres: encodeList(m.Genres), Collection: m.Collection,
		Certification: m.Certification, ReleaseDate: m.ReleaseDate, TheatricalRelease: m.Theatrical,
		DigitalRelease: m.Digital, PhysicalRelease: m.Physical, PosterPath: m.PosterPath,
		BackdropPath: m.BackdropPath, Popularity: m.Popularity, VoteAverage: m.VoteAverage,
		AltTitles: encodeList(m.AlternativeTitles), MetadataUpdatedAt: &now, CreatedAt: now,
	}
}

// ensureVersion finds or creates the title's version for the root folder's
// profile. If that version already lives in another root folder, it
// returns a conflict explanation instead.
func (s *Service) ensureVersion(ctx context.Context, t *Title, root *RootFolder, folder string) (*Version, string, error) {
	v := new(Version)
	err := s.db.DB.NewSelect().Model(v).Where("title_id = ? AND profile_id = ?", t.ID, root.DefaultProfileID).Scan(ctx)
	if err == nil {
		if v.RootFolderID != root.ID {
			other, _ := s.RootFolder(ctx, v.RootFolderID)
			where := "another folder"
			if other != nil {
				where = other.Path
			}
			return nil, fmt.Sprintf("%s (%d) is already in %s with the same quality profile. Keep one copy, or give this folder a different profile.", t.Title, t.Year, where), nil
		}
		return v, "", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, "", err
	}
	// A movie found on disk is upgraded only if its folder says so. A
	// series ignores the setting (TV has no upgrades).
	v = &Version{TitleID: t.ID, ProfileID: root.DefaultProfileID, RootFolderID: root.ID, Folder: folder, Monitored: true,
		Upgrades: t.MediaType == Series || root.UpgradeFound, CreatedAt: s.now().UTC()}
	if t.MediaType == Series {
		// A show found on disk wants only episodes from now on; older
		// missing ones can be turned on per season (2026-10-11).
		today := s.today()
		v.MonitorFrom = &today
	}
	if _, err := s.db.DB.NewInsert().Model(v).Exec(ctx); err != nil {
		return nil, "", err
	}
	return v, "", nil
}

// saveFile records (or updates) a probed file in a version.
func (s *Service) saveFile(ctx context.Context, rootID, versionID int64, p probed, existing *MediaFile) error {
	rel, _ := json.Marshal(p.release)
	med := []byte("{}")
	if p.media != nil {
		med, _ = json.Marshal(p.media)
	}
	now := s.now().UTC()
	f := &MediaFile{
		VersionID: versionID, RootFolderID: rootID, Path: p.file.Path, Size: p.file.Size, ModifiedAt: p.file.ModTime,
		ReleaseInfo: string(rel), MediaInfo: string(med), ProbeError: p.err, AddedAt: now, ProbedAt: &now,
		Edition: p.release.Edition, ReleaseGroup: p.release.Group, EpisodeClaim: encodeClaim(p.claim),
	}
	f.Resolution, f.Source, f.Quality = quality(p.release, p.media)
	if existing != nil {
		f.ID, f.AddedAt = existing.ID, existing.AddedAt
		_, err := s.db.DB.NewUpdate().Model(f).WherePK().Exec(ctx)
		return err
	}
	_, err := s.db.DB.NewInsert().Model(f).Exec(ctx)
	return err
}

// quality combines the name's claims with the file's facts. Resolution
// comes from the headers when they're readable; the source (Remux, WEB-DL)
// can only come from the name.
func quality(r parser.Release, m *mediainfo.Info) (resolution int, source, label string) {
	resolution = r.Resolution
	if m != nil && m.Video != nil {
		if res := m.Video.Resolution(); res > 0 {
			resolution = res
		}
	}
	src := r.Source
	switch {
	case src != parser.SourceUnknown:
		label = parser.Release{Source: src, Resolution: resolution}.Quality()
	case resolution > 0:
		// Radarr would call a bare resolution "HDTV-1080p". The name never
		// said TV, so the label stays just the resolution.
		label = strconv.Itoa(resolution) + "p"
	}
	return resolution, string(src), label
}

// saveReview records (or refreshes) a review item.
func (s *Service) saveReview(ctx context.Context, rootID int64, u unit, cands []Candidate, reason string, existing *ScanItem) error {
	guess := parser.Parse(filepath.Base(u.Path))
	if guess.Title == "" && len(u.Files) > 0 {
		guess = parser.Parse(filepath.Base(u.Files[0].Path))
	}
	files := make([]string, 0, len(u.Files))
	for _, f := range u.Files {
		files = append(files, f.Path)
	}
	if cands == nil {
		cands = []Candidate{}
	}
	cb, _ := json.Marshal(cands)
	now := s.now().UTC()
	it := &ScanItem{
		RootFolderID: rootID, Path: u.Path, Status: NeedsReview, TitleGuess: guess.Title, YearGuess: guess.Year,
		Reason: reason, Candidates: string(cb), Files: encodeList(files), CreatedAt: now, UpdatedAt: now,
	}
	if existing != nil {
		it.ID, it.CreatedAt = existing.ID, existing.CreatedAt
		_, err := s.db.DB.NewUpdate().Model(it).WherePK().Exec(ctx)
		return err
	}
	_, err := s.db.DB.NewInsert().Model(it).Exec(ctx)
	return err
}
