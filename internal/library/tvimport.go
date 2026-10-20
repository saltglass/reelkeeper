package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/naming"
	"github.com/saltglass/reelkeeper/internal/parser"
)

// EpisodeImportRequest is a finished TV download to import into a series
// version.
type EpisodeImportRequest struct {
	VersionID int64
	// Source is the downloaded file, or the download's folder.
	Source string
	// ReleaseName is the release's name, which often says more about its
	// quality than the file names do (and is all a season pack's files
	// share).
	ReleaseName string
	// ClientName is the download client that reported Source, for the
	// error shown when it isn't there ("" reads as "the download client").
	ClientName string
	// EpisodeIDs are the episodes the download was grabbed for. Files for
	// other episodes are left alone.
	EpisodeIDs []int64
	// Replace is set for a fix (a REPACK or PROPER of what the version has,
	// 2026-10-12): an episode that already has a file is then replaced,
	// the old file and its subtitles moved aside into ReplacedDir. Without
	// it such an episode is skipped.
	Replace bool
}

// EpisodeImportResult says what the import did.
type EpisodeImportResult struct {
	// Imported are the new files, relative to their media folder.
	Imported []string
	// FullPaths are the same with the media folder (for the media servers).
	FullPaths []string
	// Skipped explains files that weren't imported on purpose (an episode
	// that already has a file, one the download wasn't for).
	Skipped []string
	// Replaced are the files moved into ReplacedDir by a fix, relative to
	// the media folder.
	Replaced []string
}

// ImportEpisodes copies the episodes in a finished download into a series
// version's folder, names them with the naming templates, and records
// them. The rules are ImportMovie's (copy, check, rename into place, never
// overwrite, managed folders only), per file. TV stops at the first
// acceptable file (2026-10-11), so an episode that already has a file
// is skipped rather than replaced, unless the request is a fix (Replace).
//
// If some files fail, the others are still imported and the error lists
// the failures; trying again imports only what's still missing.
func (s *Service) ImportEpisodes(ctx context.Context, req EpisodeImportRequest) (EpisodeImportResult, error) {
	var res EpisodeImportResult
	v := new(Version)
	if err := s.db.DB.NewSelect().Model(v).Where("id = ?", req.VersionID).Scan(ctx); err != nil {
		return res, fmt.Errorf("the series version is gone: %w", err)
	}
	t := new(Title)
	if err := s.db.DB.NewSelect().Model(t).Where("id = ?", v.TitleID).Scan(ctx); err != nil {
		return res, err
	}
	root, err := s.RootFolder(ctx, v.RootFolderID)
	if err != nil {
		return res, err
	}
	if !root.ManageFiles {
		return res, fmt.Errorf("%w: %s. Turn on \"Manage files\" for it under Settings → Media Folders to let Reelkeeper import there", ErrNotManaged, root.Path)
	}
	profile := new(Profile)
	_ = s.db.DB.NewSelect().Model(profile).Where("id = ?", v.ProfileID).Scan(ctx)

	files, base, err := episodeVideos(req.Source, req.ClientName)
	if err != nil {
		return res, err
	}

	var episodes []Episode
	if err := s.db.DB.NewSelect().Model(&episodes).Where("title_id = ?", t.ID).
		Where("season IS NOT NULL AND number IS NOT NULL").Scan(ctx); err != nil {
		return res, err
	}
	byNumber := map[[2]int]Episode{}
	byDate := map[string][]Episode{}
	for _, e := range episodes {
		byNumber[[2]int{*e.Season, *e.Number}] = e
		if e.AirDate != "" && *e.Season != 0 {
			byDate[e.AirDate] = append(byDate[e.AirDate], e)
		}
	}
	wantedIDs := map[int64]bool{}
	for _, id := range req.EpisodeIDs {
		wantedIDs[id] = true
	}
	have, err := s.episodesWithFile(ctx, v.ID)
	if err != nil {
		return res, err
	}
	// A season pack's files often leave the season to the pack's name.
	packSeason := -1
	if e := parser.Parse(req.ReleaseName).Episode; e != nil {
		packSeason = e.Season
	}

	tpls := s.naming()
	facts := seriesFacts(t, profile.Name)
	folder := v.Folder
	if folder == "" {
		folder = naming.FormatSeries(tpls.SeriesFolder, facts)
	}
	var failures []string
	imported := 0
	for _, f := range files {
		eps, why := episodesOfFile(f.Path, packSeason, byNumber, byDate)
		if why != "" {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %s", filepath.Base(f.Path), why))
			continue
		}
		var mine []Episode
		for _, e := range eps {
			if wantedIDs[e.ID] {
				mine = append(mine, e)
			}
		}
		if len(mine) == 0 {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: not an episode this download was grabbed for", filepath.Base(f.Path)))
			continue
		}
		var replacing []MediaFile
		if slices.ContainsFunc(eps, func(e Episode) bool { return have[e.ID] }) {
			if !req.Replace {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %s already has a file", filepath.Base(f.Path), episodeLabel(eps)))
				continue
			}
			var why string
			if replacing, why, err = s.filesToReplace(ctx, v.ID, eps); err != nil {
				return res, err
			} else if why != "" {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %s", filepath.Base(f.Path), why))
				continue
			}
		}
		rel, full, aside, err := s.importEpisodeFile(ctx, v, t, root, profile.Name, folder, filepath.Join(base, f.Path), f.Size, req.ReleaseName, eps, len(files) > 1, replacing)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", filepath.Base(f.Path), err))
			continue
		}
		res.Replaced = append(res.Replaced, aside...)
		imported++
		for _, e := range eps {
			have[e.ID] = true
		}
		res.Imported = append(res.Imported, rel)
		res.FullPaths = append(res.FullPaths, full)
	}
	if imported > 0 && v.Folder != folder {
		if _, err := s.db.DB.NewUpdate().Model(v).Set("folder = ?", folder).WherePK().Exec(ctx); err != nil {
			s.logger.Warn("couldn't record the series' folder", "error", err)
		}
	}
	if imported > 0 {
		s.onChange()
	}
	switch {
	case len(failures) > 0:
		return res, fmt.Errorf("%d of the files couldn't be imported: %s", len(failures), strings.Join(failures, "; "))
	case imported == 0 && len(res.Skipped) > 0:
		// Nothing to do may be fine (every episode already has a file), but
		// only if every wanted episode is covered.
		for id := range wantedIDs {
			if !have[id] {
				return res, fmt.Errorf("none of the download's files are the wanted episodes (%s)", strings.Join(res.Skipped, "; "))
			}
		}
	}
	return res, nil
}

// episodeVideos lists the episode files in a download: the file itself,
// or every main video in its folder (samples, trailers and extras are
// skipped). base is what their paths are relative to.
func episodeVideos(source, clientName string) (files []unitFile, base string, err error) {
	st, err := os.Stat(source)
	if err != nil {
		return nil, "", fmt.Errorf("the download isn't where %s says (%s): %w", orClient(clientName), source, err)
	}
	if !st.IsDir() {
		if _, ok := videoFile(filepath.Base(source), st); !ok {
			return nil, "", fmt.Errorf("%s isn't a video file", filepath.Base(source))
		}
		return []unitFile{{Path: filepath.Base(source), Size: st.Size()}}, filepath.Dir(source), nil
	}
	files = videosUnder(source, "", 0)
	if len(files) == 0 {
		return nil, "", errors.New("the download has no episode files in it (only samples, extras or other files)")
	}
	slices.SortFunc(files, func(a, b unitFile) int { return strings.Compare(a.Path, b.Path) })
	return files, source, nil
}

// episodesOfFile works out which episodes a file holds: by air date for a
// daily show's file, otherwise by number, with the season from its name,
// then its season folder, then the pack's. why explains a file it can't
// place.
func episodesOfFile(rel string, packSeason int, byNumber map[[2]int]Episode, byDate map[string][]Episode) (eps []Episode, why string) {
	claim := parser.Parse(filepath.Base(rel)).Episode
	if claim != nil && claim.AirDate != "" {
		switch on := byDate[claim.AirDate]; len(on) {
		case 0:
			return nil, "the series has no episode that aired on " + claim.AirDate
		case 1:
			return on, ""
		default:
			return nil, "the series has several episodes that aired on " + claim.AirDate + ", so the date doesn't say which"
		}
	}
	if claim == nil || len(claim.Episodes) == 0 {
		return nil, "its name doesn't say which episode it is"
	}
	season := claim.Season
	if season < 0 {
		if n, ok := parser.SeasonFolder(filepath.Base(filepath.Dir(rel))); ok {
			season = n
		} else {
			season = packSeason
		}
	}
	if season < 0 {
		return nil, "its name doesn't say which season it is"
	}
	for _, n := range claim.Episodes {
		e, ok := byNumber[[2]int{season, n}]
		if !ok {
			return nil, fmt.Sprintf("the series has no S%02dE%02d", season, n)
		}
		eps = append(eps, e)
	}
	return eps, ""
}

// importEpisodeFile copies one file into place and records it with its
// episodes. It returns the new path relative to the media folder, and in
// full.
func (s *Service) importEpisodeFile(ctx context.Context, v *Version, t *Title, root *RootFolder, profile, folder, src string,
	size int64, releaseName string, eps []Episode, fromPack bool, replacing []MediaFile,
) (string, string, []string, error) {
	// What the file is: its own name, or the release name when the file's
	// name says less (single-file releases and some packs name files "e01").
	name := filepath.Base(src)
	if !fromPack && releaseName != "" && parser.Parse(releaseName).Quality() != "Unknown" {
		name = releaseName
	} else if parser.Parse(name).Quality() == "Unknown" && releaseName != "" {
		name = releaseName
	}
	p := probe(filepath.Dir(src), unitFile{Path: filepath.Base(src), Size: size})
	p.release = parser.Parse(name)
	draft := MediaFile{Edition: p.release.Edition, ReleaseGroup: p.release.Group}
	draft.Resolution, draft.Source, draft.Quality = quality(p.release, p.media)
	if b, err := jsonOf(p.release); err == nil {
		draft.ReleaseInfo = b
	}
	if p.media != nil {
		if b, err := jsonOf(p.media); err == nil {
			draft.MediaInfo = b
		}
	}

	tpls := s.naming()
	facts := seriesFacts(t, profile)
	season := *eps[0].Season
	list := make([]naming.Episode, len(eps))
	for i, e := range eps {
		list[i] = naming.Episode{Season: *e.Season, Number: *e.Number, Title: e.Name, AirDate: e.AirDate}
	}
	tpl := tpls.EpisodeFile
	if t.SeriesType == "daily" && eps[0].AirDate != "" {
		tpl = tpls.DailyEpisodeFile
	}
	baseName := naming.FormatEpisode(tpl, facts, list, fileFacts(draft), tpls.MultiEpisodeStyle)
	rel := filepath.Join(folder, naming.FormatSeason(tpls.SeasonFolder, facts, season), baseName+strings.ToLower(filepath.Ext(src)))
	dest := filepath.Join(root.Path, rel)
	ownsDest := slices.ContainsFunc(replacing, func(f MediaFile) bool { return f.Path == rel })
	if _, err := os.Lstat(dest); err == nil && !ownsDest {
		return "", "", nil, fmt.Errorf("a file named %s is already there; Reelkeeper never overwrites files", filepath.Base(dest))
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o775); err != nil { //nolint:gosec // group-writable: the library may be shared with a group (for example on a NAS)
		return "", "", nil, fmt.Errorf("can't create the season's folder: %w", err)
	}
	tmp := filepath.Join(filepath.Dir(dest), ".reelkeeper-importing-"+filepath.Base(dest))
	if err := copyFile(ctx, src, tmp, size); err != nil {
		_ = os.Remove(tmp)
		return "", "", nil, err
	}
	// A fix: the old files and their subtitles go aside first (never
	// deleted), then the new one is put in place.
	var aside []string
	undoAside := func() {}
	if len(replacing) > 0 {
		var paths []string
		for _, f := range replacing {
			paths = append(paths, f.Path)
			paths = append(paths, sidecars(root.Path, f.Path)...)
		}
		var err error
		if aside, undoAside, err = s.moveAside(root.Path, paths); err != nil {
			_ = os.Remove(tmp)
			return "", "", nil, err
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		undoAside()
		_ = os.Remove(tmp)
		return "", "", nil, fmt.Errorf("couldn't put the new file in place: %w", err)
	}

	now := s.now().UTC()
	mod := now
	if st, err := os.Stat(dest); err == nil {
		mod = st.ModTime().UTC().Truncate(time.Second)
	}
	nums := make([]int, len(eps))
	for i, e := range eps {
		nums[i] = *e.Number
	}
	err := s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bunTx) error {
		f := draft
		f.VersionID, f.RootFolderID, f.Path, f.Size, f.ModifiedAt = v.ID, root.ID, rel, size, mod
		f.AddedAt, f.ProbedAt, f.ProbeError = now, &now, p.err
		if f.MediaInfo == "" {
			f.MediaInfo = "{}"
		}
		f.EpisodeClaim = encodeClaim(&EpisodeClaim{Season: season, Episodes: nums})
		if len(replacing) > 0 {
			ids := make([]int64, len(replacing))
			for i, o := range replacing {
				ids[i] = o.ID
			}
			// The old files' links to their episodes go with them.
			if _, err := tx.NewDelete().Model((*MediaFile)(nil)).Where("id IN (?)", bunList(ids)).Exec(ctx); err != nil {
				return err
			}
		}
		if _, err := tx.NewInsert().Model(&f).Exec(ctx); err != nil {
			return err
		}
		links := make([]EpisodeFile, len(eps))
		for i, e := range eps {
			links[i] = EpisodeFile{EpisodeID: e.ID, MediaFileID: f.ID}
		}
		_, err := tx.NewInsert().Model(&links).Exec(ctx)
		return err
	})
	if err != nil {
		s.logger.Error("imported a file but couldn't record it; the next scan will", "file", dest, "error", err)
		return "", "", nil, fmt.Errorf("the file was copied but couldn't be recorded (a scan will pick it up): %w", err)
	}
	s.logger.Info("imported episode", "series", t.Title, "episodes", episodeLabel(eps), "file", dest, "from", src, "replaced", aside)
	return rel, dest, aside, nil
}

// filesToReplace finds the version's present files for episodes a fix is
// replacing. A file that also holds an episode the fix doesn't cover is
// never replaced (that episode would lose its file); why then says so.
func (s *Service) filesToReplace(ctx context.Context, versionID int64, eps []Episode) (files []MediaFile, why string, err error) {
	ids := make([]int64, len(eps))
	for i, e := range eps {
		ids[i] = e.ID
	}
	if err := s.db.DB.NewSelect().Model(&files).
		Where("version_id = ? AND missing_since IS NULL", versionID).
		Where("id IN (SELECT media_file_id FROM episode_files WHERE episode_id IN (?))", bunList(ids)).
		Order("id").Scan(ctx); err != nil {
		return nil, "", err
	}
	for _, f := range files {
		var linked []int64
		if err := s.db.DB.NewSelect().Table("episode_files").Column("episode_id").Where("media_file_id = ?", f.ID).Scan(ctx, &linked); err != nil {
			return nil, "", err
		}
		for _, id := range linked {
			if !slices.Contains(ids, id) {
				return nil, fmt.Sprintf("the file you have, %s, also holds other episodes this one doesn't cover, so it isn't replaced", filepath.Base(f.Path)), nil
			}
		}
	}
	return files, "", nil
}

// episodesWithFile is the episodes a version has a present file for.
func (s *Service) episodesWithFile(ctx context.Context, versionID int64) (map[int64]bool, error) {
	var ids []int64
	err := s.db.DB.NewSelect().Table("episode_files").ColumnExpr("episode_files.episode_id").
		Join("JOIN media_files mf ON mf.id = episode_files.media_file_id").
		Where("mf.version_id = ? AND mf.missing_since IS NULL", versionID).Scan(ctx, &ids)
	out := map[int64]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, err
}

// episodeLabel names episodes the usual way: "S02E05" or "S02E05-E06".
func episodeLabel(eps []Episode) string {
	if len(eps) == 0 || eps[0].Season == nil || eps[0].Number == nil {
		return ""
	}
	s := fmt.Sprintf("S%02dE%02d", *eps[0].Season, *eps[0].Number)
	if len(eps) > 1 && eps[len(eps)-1].Number != nil {
		s += fmt.Sprintf("-E%02d", *eps[len(eps)-1].Number)
	}
	return s
}
