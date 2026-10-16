package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/saltglass/reelkeeper/internal/metadata"
	"github.com/saltglass/reelkeeper/internal/parser"
)

// Scanning a TV media folder. It holds one folder per series, in any of
// the layouts real libraries use:
//
//	Show (2020) {tvdb-362829}/Show (2020) - S01E01 - Pilot [WEBDL-1080p].mkv
//	Show/Season 1/Show.S01E01.1080p.WEB.h264-GRP.mkv
//	Show (2006)/Show 07 Great Plains (1080p x265 GRP).mkv
//
// Each series folder is one unit, identified like a movie: IDs in the
// folder name first, then a TMDB search, with doubtful ones sent to the
// review screen. Each file then gets an episode claim (from its name, or
// its season folder) and is placed on its episode(s); see place.go.
// Two folders for the same series in one media folder become one series
// whose episodes list both copies.

// discoverSeries groups a TV media folder's video files: one unit per
// series folder, plus one per loose file directly in the media folder.
func discoverSeries(root string) ([]unit, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var units []unit
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		st, err := os.Stat(filepath.Join(root, name)) // follows symlinks
		if err != nil {
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
		if files := videosUnder(root, name, 1); len(files) > 0 {
			units = append(units, unit{Path: name, Files: files})
		}
	}
	sort.Slice(units, func(i, j int) bool { return units[i].Path < units[j].Path })
	return units, nil
}

// claimFor works out which episode a file claims to be. seriesTitle is the
// series' name as its folder gives it, for names like "Show 07 Title"
// that number episodes without saying "E07".
func claimFor(rel string, r parser.Release, seriesTitle string) EpisodeClaim {
	// The nearest season folder between the series folder and the file.
	folderSeason, inSeasonFolder := -1, false
	dirs := strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/")
	for i := len(dirs) - 1; i >= 1; i-- {
		if n, ok := parser.SeasonFolder(dirs[i]); ok {
			folderSeason, inSeasonFolder = n, true
			break
		}
	}

	if ep := r.Episode; ep != nil {
		c := EpisodeClaim{Season: ep.Season, Episodes: ep.Episodes, AirDate: ep.AirDate, Title: ep.Title}
		if c.Season < 0 && c.AirDate == "" && inSeasonFolder {
			c.Season = folderSeason
		}
		return c
	}
	if c, ok := numberAfterSeriesName(r.Title, seriesTitle); ok {
		if inSeasonFolder {
			c.Season = folderSeason
		}
		return c
	}
	return EpisodeClaim{Season: folderSeason}
}

// numberAfterSeriesName reads "Planet Earth 07 Great Plains" as episode 7,
// titled "Great Plains", when the series is "Planet Earth". It only does
// so when the name starts with the series' name, so a title that merely
// contains a number isn't mistaken for an episode.
func numberAfterSeriesName(title, series string) (EpisodeClaim, bool) {
	words := strings.Fields(title)
	sw := strings.Fields(normalize(series))
	if len(sw) == 0 || len(words) <= len(sw) || normalize(strings.Join(words[:len(sw)], " ")) != strings.Join(sw, " ") {
		return EpisodeClaim{}, false
	}
	num := words[len(sw)]
	if len(num) > 3 || strings.Trim(num, "0123456789") != "" {
		return EpisodeClaim{}, false
	}
	n := 0
	_, _ = fmt.Sscanf(num, "%d", &n)
	if n == 0 {
		return EpisodeClaim{}, false
	}
	return EpisodeClaim{Season: -1, Episodes: []int{n}, Title: strings.Join(words[len(sw)+1:], " ")}, true
}

// seriesHints are what a unit's names say about which series it is, most
// specific first: the series folder, then the title most of its files
// share.
func seriesHints(u unit, probes []probed) []parser.Release {
	var hints []parser.Release
	if !u.loose() {
		hints = append(hints, parser.Parse(u.Path))
	}
	count := map[string]int{}
	byKey := map[string]parser.Release{}
	for _, p := range probes {
		r := p.release
		if r.Title == "" {
			continue
		}
		k := normalize(r.Title) + fmt.Sprint(r.Year)
		count[k]++
		byKey[k] = r
	}
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if count[keys[i]] != count[keys[j]] {
			return count[keys[i]] > count[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		hints = append(hints, byKey[k])
	}
	return hints
}

// identifySeries works out which TMDB series a unit is. It returns the
// TMDB ID, or 0 with candidates and a reason when it isn't sure.
func (s *Service) identifySeries(ctx context.Context, u unit, probes []probed) (int, []Candidate, string, error) {
	hints := seriesHints(u, probes)

	// 1. IDs written in names. In a TV folder, {tmdb-N} is a TV ID.
	for _, h := range hints {
		if h.TMDBID != 0 {
			return h.TMDBID, nil, "", nil
		}
	}
	for _, h := range hints {
		var r *metadata.SeriesResult
		var err error
		switch {
		case h.TVDBID != 0:
			r, err = s.tmdb.FindSeriesByTVDB(ctx, h.TVDBID)
		case h.IMDbID != "":
			r, err = s.tmdb.FindSeriesByIMDb(ctx, h.IMDbID)
		default:
			continue
		}
		if err == nil {
			return r.ID, nil, "", nil
		}
		if stop := stopping(err); stop != nil {
			return 0, nil, "", stop
		}
	}

	// 2. Search by name and year, folder first.
	var best []Candidate
	var reason string
	tried := map[string]bool{}
	for _, h := range hints {
		if h.Title == "" {
			continue
		}
		key := normalize(h.Title) + fmt.Sprint(h.Year)
		if tried[key] {
			continue
		}
		tried[key] = true
		results, err := s.tmdb.SearchTV(ctx, h.Title, h.Year)
		if err == nil && len(results) == 0 && h.Year != 0 {
			// The year may be off, or belong to a revival; try without.
			results, err = s.tmdb.SearchTV(ctx, h.Title, 0)
		}
		if stop := stopping(err); stop != nil {
			return 0, nil, "", stop
		}
		if err != nil {
			return 0, nil, "", err
		}
		cands := rankSeries(h.Title, h.Year, results)
		if w, _ := decide(cands); w != nil {
			return w.TMDBID, nil, "", nil
		}
		if len(cands) > 0 {
			// Close candidates: which one has the episodes the files name?
			if id, ok := s.seriesTiebreak(ctx, h, cands, probes); ok {
				return id, nil, "", nil
			}
		}
		_, why := decide(cands)
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
		reason = "Couldn't tell which series this is from its name."
	}
	return 0, best, reason, nil
}

// rankSeries ranks TV search results exactly like movies: by name
// (original name and alternative titles too) and the year it started.
func rankSeries(title string, year int, results []metadata.SeriesResult) []Candidate {
	asMovies := make([]metadata.MovieResult, 0, len(results))
	for _, r := range results {
		asMovies = append(asMovies, metadata.MovieResult{
			ID: r.ID, Title: r.Name, OriginalTitle: r.OriginalName, ReleaseDate: r.FirstAirDate,
			Overview: r.Overview, PosterPath: r.PosterPath, Popularity: r.Popularity, VoteCount: r.VoteCount,
		})
	}
	return rank(title, year, asMovies)
}

// seriesTiebreak fetches the top candidates and rescores them with their
// alternative titles and with how many of the files' episodes each one
// actually has: "S07E12" rules out a series with five seasons.
func (s *Service) seriesTiebreak(ctx context.Context, h parser.Release, cands []Candidate, probes []probed) (int, bool) {
	var claimed []epKey
	for _, p := range probes {
		if ep := p.release.Episode; ep != nil && ep.Season >= 0 {
			for _, n := range ep.Episodes {
				claimed = append(claimed, epKey{ep.Season, n})
			}
		}
	}
	top := cands[:min(3, len(cands))]
	rescored := make([]Candidate, 0, len(top))
	for _, c := range top {
		m, err := s.tmdb.Series(ctx, c.TMDBID)
		if err != nil {
			return 0, false
		}
		sc := score(h.Title, h.Year, metadata.MovieResult{Title: m.Name, OriginalTitle: m.OriginalName, ReleaseDate: m.FirstAirDate}, m.AlternativeTitles...)
		if len(claimed) > 0 {
			has := map[epKey]bool{}
			for _, season := range m.Seasons {
				for _, e := range season.Episodes {
					has[epKey{e.Season, e.Number}] = true
				}
			}
			found := 0
			for _, k := range claimed {
				if has[k] {
					found++
				}
			}
			switch frac := float64(found) / float64(len(claimed)); {
			case frac >= 0.9:
				sc += 0.08
			case frac < 0.5:
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

// probeSeriesFiles reads each file's name and headers, and its episode
// claim.
func probeSeriesFiles(root string, u unit, files []unitFile) []probed {
	seriesTitle := ""
	if !u.loose() {
		seriesTitle = parser.Parse(u.Path).Title
	}
	out := make([]probed, 0, len(files))
	for _, f := range files {
		p := probe(root, f)
		title := seriesTitle
		if title == "" {
			title = p.release.Title
		}
		c := claimFor(f.Path, p.release, title)
		p.claim = &c
		out = append(out, p)
	}
	return out
}

// scanSeriesUnit processes one series folder (or loose episode file).
func (s *Service) scanSeriesUnit(ctx context.Context, root *RootFolder, u unit, byPath map[string]*MediaFile, item *ScanItem) (outcome, error) {
	if item != nil && item.Status == Ignored {
		return outUnchanged, nil
	}
	changed, versionID, err := s.changedFiles(ctx, u, byPath)
	if err != nil || len(changed) == 0 {
		return outUnchanged, err
	}
	if item != nil && versionID == 0 && sameFiles(item.FileList(), u.Files) {
		return outReview, nil
	}
	probes := probeSeriesFiles(root.Path, u, changed)

	// Files added to a series Reelkeeper already knows.
	if versionID != 0 {
		v := new(Version)
		if err := s.db.DB.NewSelect().Model(v).Where("id = ?", versionID).Scan(ctx); err != nil {
			return 0, err
		}
		if err := s.saveSeriesFiles(ctx, root.ID, v, probes, byPath); err != nil {
			return 0, err
		}
		return outUpdated, nil
	}

	match, cands, reason, err := s.identifySeries(ctx, u, probes)
	if err != nil {
		return 0, err
	}
	if match == 0 {
		return outReview, s.saveReview(ctx, root.ID, u, cands, reason, item)
	}
	title, err := s.ensureSeries(ctx, match)
	if err != nil {
		return 0, err
	}
	folder := u.Path
	if u.loose() {
		folder = ""
	}
	v, conflict, err := s.ensureVersion(ctx, title, root, folder)
	if err != nil {
		return 0, err
	}
	if conflict != "" {
		c := []Candidate{{TMDBID: title.TMDBID, Title: title.Title, Year: title.Year, PosterPath: title.PosterPath, Score: 1}}
		return outReview, s.saveReview(ctx, root.ID, u, c, conflict, item)
	}
	if err := s.saveSeriesFiles(ctx, root.ID, v, probes, byPath); err != nil {
		return 0, err
	}
	if item != nil {
		if _, err := s.db.DB.NewDelete().Model(item).WherePK().Exec(ctx); err != nil {
			return 0, err
		}
	}
	return outAdded, nil
}

// saveSeriesFiles stores probed files in a series version and places the
// series' files on its episodes.
func (s *Service) saveSeriesFiles(ctx context.Context, rootID int64, v *Version, probes []probed, byPath map[string]*MediaFile) error {
	for _, p := range probes {
		if err := s.saveFile(ctx, rootID, v.ID, p, byPath[p.file.Path]); err != nil {
			return err
		}
	}
	t, err := s.series(ctx, v.TitleID)
	if err != nil {
		return err
	}
	return s.placeFiles(ctx, t)
}
