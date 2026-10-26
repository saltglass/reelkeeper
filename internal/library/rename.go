package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/saltglass/reelkeeper/internal/mediainfo"
	"github.com/saltglass/reelkeeper/internal/naming"
	"github.com/saltglass/reelkeeper/internal/parser"
)

// Renaming gives a movie's or series' existing files the names from the
// naming templates. The safety rules:
//
//   - Only in media folders with "Reelkeeper manages files here" on. Elsewhere
//     the plan is shown but nothing happens.
//   - Files are renamed where they are (same folder), so nothing is copied
//     and a rename can't half-finish.
//   - Nothing is ever overwritten: if the new name is taken, that file is
//     skipped with the reason. The rename itself refuses to replace a file,
//     so one created by another program after the check is safe too
//     (renameNoReplace).
//   - Subtitles and other files sharing the video's name ("Movie.en.srt")
//     are renamed with it, so players keep finding them.
//   - Episode files Reelkeeper isn't sure about (any file with a note on the
//     series page) are never renamed.
//   - Every rename is logged with the old and new path.

// FileRename is the plan (or the outcome) for one video file.
type FileRename struct {
	FileID int64
	From   string // relative to the root folder
	To     string
	Extras []RenamePair // subtitles and other files that go with it
	// Problem says why this file can't be (or wasn't fully) renamed.
	Problem string
	Done    bool
}

// RenamePair is one extra file renamed along with a video.
type RenamePair struct{ From, To string }

// VersionRename groups the file renames of one version.
type VersionRename struct {
	VersionID   int64
	ProfileName string
	RootPath    string
	// Managed is false when Reelkeeper may not change files in this root.
	Managed bool
	Files   []FileRename
}

// SetNaming tells the library where to read the name templates.
func (s *Service) SetNaming(fn func() naming.Templates) { s.naming = fn }

// RenamePlan shows what renaming a title's files would do, without doing it.
// Files whose name is already right are left out.
func (s *Service) RenamePlan(ctx context.Context, titleID int64) ([]VersionRename, error) {
	t := new(Title)
	err := s.db.DB.NewSelect().Model(t).Where("id = ?", titleID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Version
		ProfileName string `bun:"profile_name"`
		RootPath    string `bun:"root_path"`
		Managed     bool   `bun:"managed"`
	}
	err = s.db.DB.NewSelect().Model((*Version)(nil)).
		ColumnExpr("version.*").
		ColumnExpr("p.name AS profile_name").
		ColumnExpr("r.path AS root_path").
		ColumnExpr("r.manage_files AS managed").
		Join("JOIN profiles p ON p.id = version.profile_id").
		Join("JOIN root_folders r ON r.id = version.root_folder_id").
		Where("version.title_id = ?", titleID).
		Order("p.id").
		Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	names, err := s.namer(ctx, t)
	if err != nil {
		return nil, err
	}
	out := make([]VersionRename, 0, len(rows))
	for _, v := range rows {
		var files []MediaFile
		if err := s.db.DB.NewSelect().Model(&files).Where("version_id = ?", v.ID).Order("path").Scan(ctx); err != nil {
			return nil, err
		}
		vr := VersionRename{VersionID: v.ID, ProfileName: v.ProfileName, RootPath: v.RootPath, Managed: v.Managed}
		taken := map[string]bool{}
		for _, f := range files {
			base, problem := names(f, v.ProfileName)
			if problem != "" {
				vr.Files = append(vr.Files, FileRename{FileID: f.ID, From: f.Path, To: f.Path, Problem: problem})
				continue
			}
			fr, ok := planFile(v.RootPath, base, f)
			if !ok {
				continue
			}
			if fr.Problem == "" && taken[fr.To] {
				fr.Problem = "Another file of this version would get the same name. Remove the duplicate first."
			}
			taken[fr.To] = true
			vr.Files = append(vr.Files, fr)
		}
		out = append(out, vr)
	}
	return out, nil
}

// namer returns how a title's files are named: the new base name (without
// extension) for a file, or a problem that rules renaming it out.
func (s *Service) namer(ctx context.Context, t *Title) (func(f MediaFile, profile string) (string, string), error) {
	tpls := s.naming()
	if t.MediaType != Series {
		return func(f MediaFile, profile string) (string, string) {
			return naming.Format(tpls.MovieFile, movieFacts(t, profile), fileFacts(f)), ""
		}, nil
	}
	eps, err := s.fileEpisodes(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	return func(f MediaFile, profile string) (string, string) {
		list := eps[f.ID]
		if f.MatchNote != "" || len(list) == 0 {
			return "", "Reelkeeper isn't sure which episode this is (see the note on the series page), so it won't rename it."
		}
		tpl := tpls.EpisodeFile
		if t.SeriesType == "daily" && list[0].AirDate != "" {
			tpl = tpls.DailyEpisodeFile
		}
		return naming.FormatEpisode(tpl, seriesFacts(t, profile), list, fileFacts(f), tpls.MultiEpisodeStyle), ""
	}, nil
}

// fileEpisodes maps each file of a series to the episodes it holds, with
// their numbers in the series' chosen order, in order.
func (s *Service) fileEpisodes(ctx context.Context, titleID int64) (map[int64][]naming.Episode, error) {
	var rows []struct {
		MediaFileID int64  `bun:"media_file_id"`
		Season      int    `bun:"season"`
		Number      int    `bun:"number"`
		Name        string `bun:"name"`
		AirDate     string `bun:"air_date"`
	}
	err := s.db.DB.NewSelect().TableExpr("episode_files AS ef").
		ColumnExpr("ef.media_file_id, e.season, e.number, e.name, e.air_date").
		Join("JOIN episodes AS e ON e.id = ef.episode_id").
		Where("e.title_id = ? AND e.season IS NOT NULL", titleID).
		OrderExpr("ef.media_file_id, e.season, e.number").
		Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	out := map[int64][]naming.Episode{}
	for _, r := range rows {
		out[r.MediaFileID] = append(out[r.MediaFileID], naming.Episode{Season: r.Season, Number: r.Number, Title: r.Name, AirDate: r.AirDate})
	}
	return out, nil
}

// seriesFacts is what the naming templates know about a series.
func seriesFacts(t *Title, profile string) naming.Series {
	return naming.Series{
		Title: t.Title, OriginalTitle: t.OriginalTitle, Year: t.Year, TMDBID: t.TMDBID, TVDBID: t.TVDBID,
		IMDbID: t.IMDbID, Certification: t.Certification, Profile: profile,
	}
}

// planFile works out one file's new name from its new base name. ok is
// false when the name is already right.
func planFile(root, base string, f MediaFile) (fr FileRename, ok bool) {
	dir, oldName := filepath.Split(f.Path)
	ext := strings.ToLower(filepath.Ext(oldName))
	fr = FileRename{FileID: f.ID, From: f.Path, To: filepath.Join(dir, base+ext)}
	if fr.To == f.Path {
		return fr, false
	}
	switch {
	case f.MissingSince != nil:
		fr.Problem = "The file is missing, so it can't be renamed."
		return fr, true
	case base == "":
		fr.Problem = "The naming template gives an empty name for this file."
		return fr, true
	case len(base+ext) > 250:
		fr.Problem = "The new name would be too long for the filesystem. Shorten the file naming template."
		return fr, true
	}
	if taken(filepath.Join(root, fr.From), filepath.Join(root, fr.To)) {
		fr.Problem = "A file named " + filepath.Base(fr.To) + " is already there; Reelkeeper never overwrites files."
		return fr, true
	}
	// Files sharing the video's name: Movie.en.srt, Movie.nfo...
	oldBase := strings.TrimSuffix(oldName, filepath.Ext(oldName))
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		fr.Problem = "Couldn't read the folder: " + err.Error()
		return fr, true
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || n == oldName || parser.IsVideo(n) || !strings.HasPrefix(n, oldBase+".") {
			continue
		}
		fr.Extras = append(fr.Extras, RenamePair{From: filepath.Join(dir, n), To: filepath.Join(dir, base+n[len(oldBase):])})
	}
	return fr, true
}

// taken reports whether something other than from already exists at to.
// (On case-insensitive filesystems a case-only rename finds itself.)
func taken(from, to string) bool {
	ti, err := os.Lstat(to)
	if err != nil {
		return false
	}
	fi, err := os.Lstat(from)
	return err != nil || !os.SameFile(fi, ti)
}

// Rename renames a movie's files as RenamePlan describes, in managed media
// folders only, and returns the plan with each file's outcome.
func (s *Service) Rename(ctx context.Context, titleID int64) ([]VersionRename, error) {
	plan, err := s.RenamePlan(ctx, titleID)
	if err != nil {
		return nil, err
	}
	changed := false
	for vi := range plan {
		v := &plan[vi]
		if !v.Managed {
			continue
		}
		for fi := range v.Files {
			f := &v.Files[fi]
			if f.Problem != "" {
				continue
			}
			s.renameFile(ctx, v.RootPath, f)
			changed = changed || f.Done
		}
	}
	if changed {
		s.onChange()
	}
	return plan, nil
}

func (s *Service) renameFile(ctx context.Context, root string, f *FileRename) {
	from, to := filepath.Join(root, f.From), filepath.Join(root, f.To)
	// The plan checked that the new name was free, but the folder may have
	// changed since. Checking again wouldn't help (another program could
	// create the file right after), so the rename itself refuses to
	// replace anything.
	if err := s.rename(from, to); err != nil {
		if errors.Is(err, fs.ErrExist) {
			f.Problem = "A file named " + filepath.Base(f.To) + " is already there; Reelkeeper never overwrites files."
		} else {
			f.Problem = "Couldn't rename the file: " + err.Error()
		}
		return
	}
	_, err := s.db.DB.NewUpdate().Model((*MediaFile)(nil)).Set("path = ?", f.To).Where("id = ?", f.FileID).Exec(ctx)
	if err != nil {
		// Put the file back so the library and the disk agree, unless
		// something has taken its old name meanwhile.
		if rerr := s.rename(to, from); rerr != nil {
			s.logger.Error("renamed a file but couldn't record it or undo it; the next scan will pick it up",
				"from", from, "to", to, "error", err, "undoError", rerr)
		}
		f.Problem = "Couldn't record the new name: " + err.Error()
		return
	}
	f.Done = true
	s.logger.Info("renamed file", "from", from, "to", to, "reason", "rename requested")

	var failed []string
	for _, x := range f.Extras {
		xf, xt := filepath.Join(root, x.From), filepath.Join(root, x.To)
		if err := s.rename(xf, xt); err != nil {
			if errors.Is(err, fs.ErrExist) {
				failed = append(failed, filepath.Base(x.From)+" (the new name is taken)")
			} else {
				failed = append(failed, filepath.Base(x.From)+" ("+err.Error()+")")
			}
			continue
		}
		s.logger.Info("renamed file", "from", xf, "to", xt, "reason", "goes with "+filepath.Base(f.To))
	}
	if len(failed) > 0 {
		f.Problem = "The video was renamed, but these weren't: " + strings.Join(failed, ", ")
		s.logger.Warn("some files that go with a renamed video kept their names", "video", to, "files", failed)
	}
}

// rename renames a file on disk without ever replacing another
// (renameNoReplace).
func (s *Service) rename(from, to string) error {
	if s.beforeRename != nil {
		s.beforeRename(from, to)
	}
	err := renameNoReplace(from, to)
	var stranded *strandedError
	if errors.As(err, &stranded) {
		s.logger.Error("a file was left at a temporary name: a change of case could neither finish nor be undone without overwriting another file",
			"file", stranded.At, "wanted", to, "old", from, "error", stranded.Err, "undoError", stranded.UndoErr)
	}
	return err
}

// movieFacts is what the naming templates know about a movie.
func movieFacts(t *Title, profile string) naming.Movie {
	return naming.Movie{
		Title: t.Title, OriginalTitle: t.OriginalTitle, Year: t.Year, TMDBID: t.TMDBID, IMDbID: t.IMDbID,
		Collection: t.Collection, Certification: t.Certification, Profile: profile,
	}
}

// fileFacts is what the naming templates know about a file. The file's own
// headers win over its name where both say something (resolution, codec,
// HDR); the name fills in what headers don't tell (DTS-HD MA, Atmos,
// HDR10+, the release group).
func fileFacts(f MediaFile) naming.File {
	r := f.Release()
	nf := naming.File{
		Quality: f.Quality, Revision: r.Revision, Edition: f.Edition, Group: f.ReleaseGroup, Source: f.Source,
		VideoCodec: r.VideoCodec, VideoBitDepth: r.BitDepth, HDR: r.HDR, AudioChannels: r.Channels,
	}
	nameAudio := ""
	for _, a := range r.Audio {
		if a != "Atmos" {
			nameAudio = a
			break
		}
	}
	nf.AudioCodec = nameAudio
	if m := f.Media(); m != nil {
		if v := m.Video; v != nil {
			nf.VideoCodec = v.Codec
			if v.BitDepth > 0 {
				nf.VideoBitDepth = v.BitDepth
			}
			hdr := slices.Clone(v.HDR)
			if slices.Contains(r.HDR, "HDR10+") && !slices.Contains(hdr, "HDR10+") {
				hdr = append(hdr, "HDR10+")
			}
			nf.HDR = hdr
		}
		if len(m.Audio) > 0 {
			a := m.Audio[0]
			// The name is more specific about the same codec (DTS-HD MA
			// for DTS); otherwise trust the file.
			if nameAudio == "" || !strings.HasPrefix(nameAudio, a.Codec) {
				nf.AudioCodec = a.Codec
			}
			if a.Channels > 0 {
				nf.AudioChannels = mediainfo.ChannelLayout(a.Channels)
			}
		}
	}
	// Radarr's names for Dolby Digital (Plus).
	switch nf.AudioCodec {
	case "DD+":
		nf.AudioCodec = "EAC3"
	case "DD":
		nf.AudioCodec = "AC3"
	}
	if slices.Contains(r.Audio, "Atmos") && (strings.HasPrefix(nf.AudioCodec, "TrueHD") || nf.AudioCodec == "EAC3") {
		nf.AudioCodec += " Atmos"
	}
	return nf
}

// ScoringName is what custom formats are matched against for a file already
// in the library: its name, then what its headers say (HDR, video codec, bit
// depth, audio) and the release group it came from. Renamed files often say
// none of that ("Tron Ares (2025) (2160p) REMUX.mkv"), and most formats look
// for words in the name, so without this a Dolby Vision remux would score as
// if it had nothing and be "upgraded" forever.
func (f MediaFile) ScoringName() string {
	base := filepath.Base(f.Path)
	words := []string{strings.TrimSuffix(base, filepath.Ext(base))}
	nf := fileFacts(f)
	words = append(words, nf.HDR...)
	if nf.VideoCodec != "" {
		words = append(words, nf.VideoCodec)
	}
	if nf.VideoBitDepth >= 10 {
		words = append(words, fmt.Sprintf("%dbit", nf.VideoBitDepth))
	}
	audio := nf.AudioCodec
	// Atmos isn't in the headers, but track names often say it.
	if m := f.Media(); m != nil && len(m.Audio) > 0 && !strings.Contains(audio, "Atmos") &&
		strings.Contains(strings.ToLower(m.Audio[0].Title), "atmos") {
		audio = strings.TrimSpace(audio + " Atmos")
	}
	if audio != "" {
		words = append(words, audio)
	}
	if nf.AudioChannels != "" {
		words = append(words, nf.AudioChannels)
	}
	name := strings.Join(words, " ")
	if g := f.ReleaseGroup; g != "" && !strings.Contains(strings.ToLower(base), strings.ToLower(g)) {
		name += "-" + g
	}
	return name
}
