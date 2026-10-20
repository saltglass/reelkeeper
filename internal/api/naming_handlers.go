package api

import (
	"errors"
	"net/http"
	"path/filepath"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/naming"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// GetNamingSettings returns the name templates.
func (h *Handlers) GetNamingSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, toNamingSettings(h.Settings.Naming()))
}

// PutNamingSettings saves the name templates.
func (h *Handlers) PutNamingSettings(w http.ResponseWriter, r *http.Request) {
	var body gen.PutNamingSettingsJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	t, err := h.Settings.SaveNaming(r.Context(), fromNamingSettings(body))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toNamingSettings(t))
}

func toNamingSettings(t naming.Templates) gen.NamingSettings {
	return gen.NamingSettings{
		MovieFolder: t.MovieFolder, MovieFile: t.MovieFile, SeriesFolder: t.SeriesFolder, SeasonFolder: t.SeasonFolder,
		EpisodeFile: t.EpisodeFile, DailyEpisodeFile: t.DailyEpisodeFile,
		MultiEpisodeStyle: gen.MultiEpisodeStyle(t.MultiEpisodeStyle),
	}
}

func fromNamingSettings(n gen.NamingSettings) naming.Templates {
	return naming.Templates{
		MovieFolder: n.MovieFolder, MovieFile: n.MovieFile, SeriesFolder: n.SeriesFolder, SeasonFolder: n.SeasonFolder,
		EpisodeFile: n.EpisodeFile, DailyEpisodeFile: n.DailyEpisodeFile, MultiEpisodeStyle: string(n.MultiEpisodeStyle),
	}
}

// PreviewNaming fills unsaved templates in for the sample movies and episodes.
func (h *Handlers) PreviewNaming(w http.ResponseWriter, r *http.Request) {
	var body gen.PreviewNamingJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	t := fromNamingSettings(body)
	out := gen.NamingPreview{Examples: []gen.NamingExample{}, EpisodeExamples: []gen.NamingExample{}, Problems: map[string]string{}}
	var errs validation.Errors
	if errors.As(settings.CheckNaming(t), &errs) {
		for field, msg := range errs {
			out.Problems[field] = msg
		}
	}
	for _, s := range naming.Samples() {
		out.Examples = append(out.Examples, gen.NamingExample{
			Label:  s.Label,
			Folder: naming.Format(t.MovieFolder, s.Movie, naming.File{}),
			File:   naming.Format(t.MovieFile, s.Movie, s.File) + s.Ext,
		})
	}
	for _, s := range naming.EpisodeSamples() {
		tpl := t.EpisodeFile
		if s.Daily {
			tpl = t.DailyEpisodeFile
		}
		out.EpisodeExamples = append(out.EpisodeExamples, gen.NamingExample{
			Label:  s.Label,
			Folder: naming.FormatSeries(t.SeriesFolder, s.Series) + "/" + naming.FormatSeason(t.SeasonFolder, s.Series, s.Episodes[0].Season),
			File:   naming.FormatEpisode(tpl, s.Series, s.Episodes, s.File, t.MultiEpisodeStyle) + s.Ext,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// GetMovieRename shows what renaming would do.
func (h *Handlers) GetMovieRename(w http.ResponseWriter, r *http.Request, id int64) {
	if !h.requireKind(w, r, id, library.Movie) {
		return
	}
	plan, err := h.Library.RenamePlan(r.Context(), id)
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRenames(plan))
}

// RenameMovie renames the files (in managed media folders only).
func (h *Handlers) RenameMovie(w http.ResponseWriter, r *http.Request, id int64) {
	if !h.requireKind(w, r, id, library.Movie) {
		return
	}
	plan, err := h.Library.Rename(r.Context(), id)
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	h.tellRenamed(r, id, plan)
	writeJSON(w, http.StatusOK, toRenames(plan))
}

// tellRenamed lets the media servers (Emby, Jellyfin, Plex) know a title's
// files were renamed.
func (h *Handlers) tellRenamed(r *http.Request, titleID int64, plan []library.VersionRename) {
	if h.Notify == nil {
		return
	}
	for _, v := range plan {
		for _, f := range v.Files {
			if f.Done {
				h.Notify.Notify(r.Context(), notify.Event{Kind: notify.KindRenamed, TitleID: titleID,
					Title: "Renamed files", Message: "Renamed " + f.To, Path: filepath.Join(v.RootPath, f.To)})
				break // one per folder is enough for the media server
			}
		}
	}
}

func toRenames(plan []library.VersionRename) []gen.VersionRename {
	out := make([]gen.VersionRename, 0, len(plan))
	for _, v := range plan {
		gv := gen.VersionRename{
			VersionId: v.VersionID, ProfileName: v.ProfileName, RootPath: v.RootPath, Managed: v.Managed,
			Files: []gen.FileRename{},
		}
		for _, f := range v.Files {
			gf := gen.FileRename{FileId: f.FileID, From: f.From, To: f.To, Problem: f.Problem, Done: f.Done, Extras: []gen.RenamePair{}}
			for _, x := range f.Extras {
				gf.Extras = append(gf.Extras, gen.RenamePair{From: x.From, To: x.To})
			}
			gv.Files = append(gv.Files, gf)
		}
		out = append(out, gv)
	}
	return out
}
