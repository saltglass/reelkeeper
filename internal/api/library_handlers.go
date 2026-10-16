package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/jobs"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/metadata"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// --- Metadata settings -------------------------------------------------------

// GetMetadataSettings returns the TMDB settings (never the key itself).
func (h *Handlers) GetMetadataSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.toMetadata(h.Settings.Metadata()))
}

// PutMetadataSettings saves the TMDB settings.
func (h *Handlers) PutMetadataSettings(w http.ResponseWriter, r *http.Request) {
	var body gen.PutMetadataSettingsJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	m, err := h.Settings.SaveMetadata(r.Context(), settings.MetadataUpdate{TMDBAPIKey: body.TmdbApiKey, Language: body.Language})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.toMetadata(m))
}

func (h *Handlers) toMetadata(m settings.Metadata) gen.MetadataSettings {
	return gen.MetadataSettings{TmdbApiKeySet: m.TMDBAPIKey != "", Language: m.Language}
}

// CheckMetadata tries the TMDB key (the one given, or the stored one).
func (h *Handlers) CheckMetadata(w http.ResponseWriter, r *http.Request) {
	var body gen.CheckMetadataJSONRequestBody
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	key := h.Settings.Metadata().TMDBAPIKey
	if body.TmdbApiKey != nil && *body.TmdbApiKey != "" {
		key = *body.TmdbApiKey
	}
	c := metadata.New(func() string { return key }, func() string { return h.Settings.Metadata().Language })
	if h.TMDBBaseURL != "" {
		c.SetBaseURL(h.TMDBBaseURL)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := c.Check(ctx); err != nil {
		writeJSON(w, http.StatusOK, gen.CheckResult{Ok: false, Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, gen.CheckResult{Ok: true, Message: "The key works. TMDB answered."})
}

// --- Profiles and media folders ----------------------------------------------

// GetProfiles lists quality profiles with how much each is used.
func (h *Handlers) GetProfiles(w http.ResponseWriter, r *http.Request) {
	ps, err := h.Library.ProfilesWithUsage(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := make([]gen.Profile, 0, len(ps))
	for _, p := range ps {
		out = append(out, toProfile(p.Profile, p.Usage))
	}
	writeJSON(w, http.StatusOK, out)
}

func toProfile(p library.Profile, u library.ProfileUsage) gen.Profile {
	return gen.Profile{
		Id: p.ID, Name: p.Name, Slug: p.Slug, AppliesTo: gen.ProfileAppliesTo(p.AppliesTo),
		MediaFolders: u.MediaFolders, Movies: u.Movies, Series: u.Series,
	}
}

// AddProfile adds a quality profile.
func (h *Handlers) AddProfile(w http.ResponseWriter, r *http.Request) {
	var body gen.AddProfileJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	// Without a preset the profile keeps the default for its slug (HD, or
	// Remux for "remux") until its settings are first saved.
	var preset *decision.Preset
	if body.Preset != nil && *body.Preset != "" {
		p, ok := decision.PresetByID(*body.Preset)
		if !ok {
			h.writeServiceError(w, r, validation.Errors{"preset": "That preset doesn't exist."})
			return
		}
		preset = &p
	}
	p, err := h.Library.CreateProfile(r.Context(), body.Name, string(body.AppliesTo))
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	if preset != nil {
		if _, err := h.Library.SaveProfileSimple(r.Context(), p.ID, preset.Settings); err != nil {
			h.writeLibraryError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, toProfile(*p, library.ProfileUsage{}))
}

// UpdateProfile renames a profile or changes what it applies to.
func (h *Handlers) UpdateProfile(w http.ResponseWriter, r *http.Request, id int64) {
	var body gen.UpdateProfileJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	p, err := h.Library.UpdateProfile(r.Context(), id, body.Name, string(body.AppliesTo))
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	u, err := h.Library.ProfileUsageOf(r.Context(), p.ID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toProfile(*p, u))
}

// DeleteProfile deletes a profile nothing uses.
func (h *Handlers) DeleteProfile(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.Library.DeleteProfile(r.Context(), id); err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetRootFolders lists media folders with their counts.
func (h *Handlers) GetRootFolders(w http.ResponseWriter, r *http.Request) {
	roots, err := h.Library.RootFolders(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := make([]gen.RootFolder, 0, len(roots))
	for _, root := range roots {
		g, err := h.toRoot(r.Context(), root)
		if err != nil {
			h.writeServiceError(w, r, err)
			return
		}
		out = append(out, g)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handlers) toRoot(ctx context.Context, root library.RootFolder) (gen.RootFolder, error) {
	st, err := h.Library.Stats(ctx, root)
	if err != nil {
		return gen.RootFolder{}, err
	}
	return gen.RootFolder{
		Id: root.ID, Path: root.Path, MediaType: gen.RootFolderMediaType(root.MediaType),
		DefaultProfileId: root.DefaultProfileID, ManageFiles: root.ManageFiles, LastScanAt: root.LastScanAt,
		Titles: st.Titles, Files: st.Files, Missing: st.Missing, Review: st.Review, Size: st.Size, FreeSpace: st.FreeSpace,
	}, nil
}

// AddRootFolder adds a media folder and queues a scan of it.
func (h *Handlers) AddRootFolder(w http.ResponseWriter, r *http.Request) {
	var body gen.AddRootFolderJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	root, err := h.Library.AddRootFolder(r.Context(), library.RootInput{
		Path: body.Path, MediaType: string(body.MediaType), DefaultProfileID: body.DefaultProfileId,
	})
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if _, _, err := h.queueScan(r.Context(), root.ID, jobs.System); err != nil {
		h.Logger.Warn("couldn't queue the first scan", "path", root.Path, "error", err)
	}
	g, err := h.toRoot(r.Context(), *root)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// UpdateRootFolder changes a media folder's profile or file management.
func (h *Handlers) UpdateRootFolder(w http.ResponseWriter, r *http.Request, id int64) {
	var body gen.UpdateRootFolderJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	root, err := h.Library.UpdateRootFolder(r.Context(), id, body.DefaultProfileId, body.ManageFiles)
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	g, err := h.toRoot(r.Context(), *root)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// RemoveRootFolder forgets a media folder (files untouched).
func (h *Handlers) RemoveRootFolder(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.Library.RemoveRootFolder(r.Context(), id); err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ScanRootFolder queues a scan of one media folder.
func (h *Handlers) ScanRootFolder(w http.ResponseWriter, r *http.Request, id int64) {
	if _, err := h.Library.RootFolder(r.Context(), id); err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	job, _, err := h.queueScan(r.Context(), id, jobs.Manual)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ToJob(h.Jobs, *job))
}

func (h *Handlers) queueScan(ctx context.Context, rootID int64, trigger jobs.Trigger) (*jobs.Job, bool, error) {
	return library.QueueScan(ctx, h.Jobs, rootID, trigger)
}

// BrowseFolders lists folders for the folder picker.
func (h *Handlers) BrowseFolders(w http.ResponseWriter, _ *http.Request, params gen.BrowseFoldersParams) {
	dir := "/"
	if params.Path != nil && *params.Path != "" {
		dir = filepath.Clean(*params.Path)
	}
	folders, err := library.BrowseFolders(dir)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "Can't open that folder.", map[string]string{"path": err.Error()})
		return
	}
	out := gen.FolderListing{Path: dir, Folders: make([]struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}, 0, len(folders))}
	if dir != "/" {
		parent := filepath.Dir(dir)
		out.Parent = &parent
	}
	for _, f := range folders {
		out.Folders = append(out.Folders, struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}{f.Name, f.Path})
	}
	writeJSON(w, http.StatusOK, out)
}

// --- Review -----------------------------------------------------------------

// GetReviewItems lists folders waiting for review.
func (h *Handlers) GetReviewItems(w http.ResponseWriter, r *http.Request) {
	items, err := h.Library.ReviewItems(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	roots, err := h.Library.RootFolders(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	rootPath := map[int64]string{}
	rootType := map[int64]string{}
	for _, root := range roots {
		rootPath[root.ID] = root.Path
		rootType[root.ID] = root.MediaType
	}
	out := make([]gen.ReviewItem, 0, len(items))
	for _, it := range items {
		cands := make([]gen.Candidate, 0)
		for _, c := range it.CandidateList() {
			cands = append(cands, gen.Candidate{TmdbId: c.TMDBID, Title: c.Title, Year: c.Year, Overview: c.Overview, PosterPath: c.PosterPath, Score: float32(c.Score)})
		}
		out = append(out, gen.ReviewItem{
			Id: it.ID, RootFolderId: it.RootFolderID, RootPath: rootPath[it.RootFolderID],
			MediaType: gen.ReviewItemMediaType(rootType[it.RootFolderID]), Path: it.Path,
			TitleGuess: it.TitleGuess, YearGuess: it.YearGuess, Reason: it.Reason, Candidates: cands, Files: it.FileList(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ResolveReviewItem says which movie a folder is.
func (h *Handlers) ResolveReviewItem(w http.ResponseWriter, r *http.Request, id int64) {
	var body gen.ResolveReviewItemJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	t, err := h.Library.ResolveReview(r.Context(), id, body.TmdbId)
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, gen.ResolvedTitle{
		MediaType: gen.ResolvedTitleMediaType(t.MediaType), Id: t.ID, Title: t.Title, Year: t.Year,
	})
}

// IgnoreReviewItem ignores a folder from now on.
func (h *Handlers) IgnoreReviewItem(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.Library.IgnoreReview(r.Context(), id); err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Movies -----------------------------------------------------------------

// GetMovies lists every movie.
func (h *Handlers) GetMovies(w http.ResponseWriter, r *http.Request) {
	movies, err := h.Library.Movies(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := make([]gen.MovieSummary, 0, len(movies))
	for _, m := range movies {
		out = append(out, toMovieSummary(m))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetMovie returns one movie with its files.
func (h *Handlers) GetMovie(w http.ResponseWriter, r *http.Request, id int64) {
	m, err := h.Library.MovieDetail(r.Context(), id)
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toMovieDetail(*m))
}

// AddMovie adds a movie by TMDB ID.
func (h *Handlers) AddMovie(w http.ResponseWriter, r *http.Request) {
	var body gen.AddMovieJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	monitored := body.Monitored == nil || *body.Monitored
	m, err := h.Library.AddMovie(r.Context(), body.TmdbId, body.ProfileId, body.RootFolderId, monitored)
	if errors.Is(err, library.ErrInUse) {
		writeError(w, http.StatusConflict, "in_library", "That movie is already in the library with this profile.", nil)
		return
	}
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	if monitored {
		for _, v := range m.Versions {
			if v.ProfileID == body.ProfileId && v.Monitored {
				h.searchOnAdd(r.Context(), v.ID)
			}
		}
	}
	writeJSON(w, http.StatusOK, toMovieDetail(*m))
}

// LookupMovies searches TMDB.
func (h *Handlers) LookupMovies(w http.ResponseWriter, r *http.Request, params gen.LookupMoviesParams) {
	res, err := h.Library.Lookup(r.Context(), params.Q)
	if err != nil {
		writeError(w, http.StatusBadGateway, "tmdb", tmdbMessage(err), nil)
		return
	}
	out := make([]gen.LookupResult, 0, len(res))
	for _, x := range res {
		lr := gen.LookupResult{TmdbId: x.ID, Title: x.Title, Year: x.Year(), Overview: x.Overview, PosterPath: x.PosterPath, InLibrary: x.MovieID != 0}
		if x.MovieID != 0 {
			id := x.MovieID
			lr.MovieId = &id
		}
		out = append(out, lr)
	}
	writeJSON(w, http.StatusOK, out)
}

// RefreshMovie re-fetches a movie's details.
func (h *Handlers) RefreshMovie(w http.ResponseWriter, r *http.Request, id int64) {
	m, err := h.Library.RefreshMovie(r.Context(), id)
	if errors.Is(err, library.ErrNotFound) {
		h.writeLibraryError(w, r, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "tmdb", tmdbMessage(err), nil)
		return
	}
	writeJSON(w, http.StatusOK, toMovieDetail(*m))
}

// UpdateVersion turns monitoring on or off.
func (h *Handlers) UpdateVersion(w http.ResponseWriter, r *http.Request, id, versionID int64) {
	var body gen.UpdateVersionJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	if !h.requireKind(w, r, id, library.Movie) {
		return
	}
	wasMonitored := true
	if body.Monitored {
		if m, err := h.Library.MovieDetail(r.Context(), id); err == nil {
			for _, v := range m.Versions {
				if v.ID == versionID {
					wasMonitored = v.Monitored
				}
			}
		}
	}
	if err := h.Library.SetMonitored(r.Context(), id, versionID, body.Monitored); err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	if body.Monitored && !wasMonitored {
		h.searchOnAdd(r.Context(), versionID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveVersion removes a version without files.
func (h *Handlers) RemoveVersion(w http.ResponseWriter, r *http.Request, id, versionID int64) {
	if !h.requireKind(w, r, id, library.Movie) {
		return
	}
	h.removeVersion(w, r, id, versionID)
}

// removeVersion is shared by the movie and series endpoints.
func (h *Handlers) removeVersion(w http.ResponseWriter, r *http.Request, id, versionID int64) {
	err := h.Library.RemoveVersion(r.Context(), id, versionID)
	if errors.Is(err, library.ErrHasFiles) {
		writeError(w, http.StatusConflict, "has_files",
			"This version has files, and the next scan would find them again. Remove or move the files first.", nil)
		return
	}
	if err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) writeLibraryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, library.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "That item doesn't exist (any more).", nil)
		return
	}
	if _, ok := validation.As(err); ok {
		h.writeServiceError(w, r, err)
		return
	}
	if errors.Is(err, metadata.ErrNoKey) || errors.Is(err, metadata.ErrBadKey) || errors.Is(err, metadata.ErrRateLimit) {
		writeError(w, http.StatusBadGateway, "tmdb", tmdbMessage(err), nil)
		return
	}
	h.writeServiceError(w, r, err)
}

func tmdbMessage(err error) string {
	switch {
	case errors.Is(err, metadata.ErrNoKey), errors.Is(err, metadata.ErrBadKey), errors.Is(err, metadata.ErrRateLimit):
		return err.Error()
	}
	return "TMDB couldn't be reached: " + err.Error()
}

func toVersionSummary(v library.VersionView) gen.VersionSummary {
	hdr := v.HDR
	if hdr == nil {
		hdr = []string{}
	}
	return gen.VersionSummary{
		Id: v.ID, ProfileId: v.ProfileID, ProfileName: v.ProfileName, RootFolderId: v.RootFolderID, RootPath: v.RootPath,
		Folder: v.Folder, Monitored: v.Monitored, Files: v.Files, Missing: v.Missing, Size: v.Size,
		Quality: v.Quality, Resolution: v.Resolution, Hdr: hdr,
		VideoCodec: v.VideoCodec, AudioCodec: v.AudioCodec, AudioChannels: v.AudioChannels,
		ReleaseGroup: v.ReleaseGroup, Edition: v.Edition, EpisodesWithFiles: v.EpisodesWithFiles,
	}
}

func toVersionDetail(v library.VersionView) gen.VersionDetail {
	s := toVersionSummary(v)
	vd := gen.VersionDetail{
		Id: s.Id, ProfileId: s.ProfileId, ProfileName: s.ProfileName, RootFolderId: s.RootFolderId, RootPath: s.RootPath,
		Folder: s.Folder, Monitored: s.Monitored, Files: s.Files, Missing: s.Missing, Size: s.Size,
		Quality: s.Quality, Resolution: s.Resolution, Hdr: s.Hdr,
		VideoCodec: s.VideoCodec, AudioCodec: s.AudioCodec, AudioChannels: s.AudioChannels,
		ReleaseGroup: s.ReleaseGroup, Edition: s.Edition, EpisodesWithFiles: s.EpisodesWithFiles,
		MediaFiles: make([]gen.MediaFile, 0, len(v.MediaFiles)),
	}
	for _, f := range v.MediaFiles {
		vd.MediaFiles = append(vd.MediaFiles, toMediaFile(v.RootPath, f))
	}
	return vd
}

func toMovieSummary(m library.MovieView) gen.MovieSummary {
	versions := make([]gen.VersionSummary, 0, len(m.Versions))
	for _, v := range m.Versions {
		versions = append(versions, toVersionSummary(v))
	}
	genres := m.GenreList()
	if genres == nil {
		genres = []string{}
	}
	return gen.MovieSummary{
		Id: m.ID, TmdbId: m.TMDBID, ImdbId: m.IMDbID, Title: m.Title.Title, SortTitle: m.SortTitle, Year: m.Year,
		Runtime: m.Runtime, Genres: genres, PosterPath: m.PosterPath, AddedAt: m.CreatedAt, Versions: versions,
	}
}

func toMovieDetail(m library.MovieView) gen.MovieDetail {
	genres := m.GenreList()
	if genres == nil {
		genres = []string{}
	}
	out := gen.MovieDetail{
		Id: m.ID, TmdbId: m.TMDBID, ImdbId: m.IMDbID, Title: m.Title.Title, OriginalTitle: m.OriginalTitle, Year: m.Year,
		Overview: m.Overview, Runtime: m.Runtime, Genres: genres, Certification: m.Certification, Collection: m.Collection,
		ReleaseDate: m.ReleaseDate, TheatricalRelease: m.TheatricalRelease, DigitalRelease: m.DigitalRelease,
		PhysicalRelease: m.PhysicalRelease, PosterPath: m.PosterPath, BackdropPath: m.BackdropPath,
		VoteAverage: float32(m.VoteAverage), MetadataUpdatedAt: m.MetadataUpdatedAt, AddedAt: m.CreatedAt,
		Versions: make([]gen.VersionDetail, 0, len(m.Versions)),
	}
	for _, v := range m.Versions {
		out.Versions = append(out.Versions, toVersionDetail(v))
	}
	return out
}

func toMediaFile(root string, f library.MediaFile) gen.MediaFile {
	g := gen.MediaFile{
		Id: f.ID, Path: filepath.Join(root, f.Path), Size: f.Size, Quality: f.Quality, Resolution: f.Resolution,
		Source: f.Source, Edition: f.Edition, ReleaseGroup: f.ReleaseGroup, MissingSince: f.MissingSince, AddedAt: f.AddedAt,
		Audio: []gen.MediaAudio{}, Subtitles: []gen.MediaSubtitle{},
	}
	if f.MatchNote != "" {
		n := f.MatchNote
		g.MatchNote = &n
	}
	if f.ProbeError != "" {
		e := f.ProbeError
		g.ProbeError = &e
	}
	m := f.Media()
	if m == nil {
		return g
	}
	g.DurationSeconds = int(m.Duration.Seconds())
	if v := m.Video; v != nil {
		g.Video = &gen.MediaVideo{Codec: v.Codec, Width: v.Width, Height: v.Height, BitDepth: nz(v.BitDepth), Hdr: &v.HDR, DvProfile: nz(v.DVProfile)}
	}
	for _, a := range m.Audio {
		ch, lang, title, def := a.Channels, a.Language, a.Title, a.Default
		g.Audio = append(g.Audio, gen.MediaAudio{Codec: a.Codec, Channels: &ch, Language: &lang, Title: &title, Default: &def})
	}
	for _, s := range m.Subtitles {
		lang, title, forced := s.Language, s.Title, s.Forced
		g.Subtitles = append(g.Subtitles, gen.MediaSubtitle{Codec: s.Codec, Language: &lang, Title: &title, Forced: &forced})
	}
	return g
}

func nz(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// --- Images -----------------------------------------------------------------

var imageFile = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}\.(jpg|png)$`)

// GetTmdbImage serves a TMDB image from Reelkeeper's cache, fetching it once.
// Browsers never talk to TMDB directly.
func (h *Handlers) GetTmdbImage(w http.ResponseWriter, r *http.Request, size gen.GetTmdbImageParamsSize, file string) {
	if !size.Valid() || !imageFile.MatchString(file) {
		writeError(w, http.StatusNotFound, "not_found", "No such image.", nil)
		return
	}
	dir := filepath.Join(h.DataDir, "cache", "tmdb", string(size))
	path := filepath.Join(dir, file)
	if _, err := os.Stat(path); err != nil {
		if err := fetchImage(r.Context(), h.ImageBaseURL+"/"+string(size)+"/"+file, dir, path); err != nil {
			h.Logger.Debug("couldn't fetch image", "file", file, "error", err)
			writeError(w, http.StatusNotFound, "not_found", "The image isn't available.", nil)
			return
		}
	}
	w.Header().Set("Cache-Control", "private, max-age=2592000, immutable")
	http.ServeFile(w, r, path)
}

func fetchImage(ctx context.Context, url, dir, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, 10<<20))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("empty image")
	}
	return os.Rename(tmp.Name(), path)
}

// DeleteMovie deletes a movie, its queued downloads, and (when asked) its
// files.
func (h *Handlers) DeleteMovie(w http.ResponseWriter, r *http.Request, id int64, params gen.DeleteMovieParams) {
	if !h.requireKind(w, r, id, library.Movie) {
		return
	}
	deleteFiles := params.DeleteFiles != nil && *params.DeleteFiles
	// Refuse before anything changes (downloads included) when the files
	// can't be deleted.
	if deleteFiles {
		if err := h.Library.CheckDeleteFiles(r.Context(), id); errors.Is(err, library.ErrNotManaged) {
			writeError(w, http.StatusUnprocessableEntity, "not_managed", capitalize(err.Error())+".", nil)
			return
		} else if err != nil {
			h.writeLibraryError(w, r, err)
			return
		}
	}
	// Downloads go next: their data lives in qBittorrent's folder, not the
	// library, and a failure here must stop everything before files go.
	if err := h.Downloads.RemoveForTitle(r.Context(), id); err != nil && !errors.Is(err, download.ErrNoClient) {
		writeError(w, http.StatusBadGateway, "download_client",
			"The download client couldn't remove this movie's downloads, so nothing was deleted: "+err.Error(), nil)
		return
	}
	res, err := h.Library.DeleteMovie(r.Context(), id, deleteFiles)
	switch {
	case errors.Is(err, library.ErrNotManaged):
		writeError(w, http.StatusUnprocessableEntity, "not_managed", capitalize(err.Error())+".", nil)
		return
	case errors.Is(err, library.ErrNotFound):
		h.writeLibraryError(w, r, err)
		return
	case err != nil:
		h.Logger.Error("deleting a movie failed", "movie", id, "error", err)
		writeError(w, http.StatusInternalServerError, "delete_failed", capitalize(err.Error())+".", nil)
		return
	}
	if deleteFiles && len(res.Deleted) > 0 && h.Notify != nil {
		// Emby drops the movie at once instead of at its next scan.
		h.Notify.Notify(r.Context(), notify.Event{Kind: notify.KindDeleted, TitleID: id, Title: "Movie deleted",
			Message: "Deleted " + res.Deleted[0], Path: res.Deleted[len(res.Deleted)-1]})
	}
	writeJSON(w, http.StatusOK, gen.DeleteMovieResult{Deleted: nonNil(res.Deleted), Skipped: nonNil(res.Skipped)})
}

// ListSkippedFolders lists the folders scans leave alone.
func (h *Handlers) ListSkippedFolders(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Library.SkippedFolders(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out := make([]gen.SkippedFolder, 0, len(rows))
	paths := map[int64]string{}
	for _, f := range rows {
		p, ok := paths[f.RootFolderID]
		if !ok {
			root, err := h.Library.RootFolder(r.Context(), f.RootFolderID)
			if err != nil {
				h.writeServiceError(w, r, err)
				return
			}
			p = root.Path
			paths[f.RootFolderID] = p
		}
		out = append(out, gen.SkippedFolder{
			Id: f.ID, RootFolderId: f.RootFolderID, RootPath: p, Path: f.Path, Title: f.Title, CreatedAt: f.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// UnskipFolder lets scans see a skipped folder again.
func (h *Handlers) UnskipFolder(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.Library.UnskipFolder(r.Context(), id); err != nil {
		h.writeLibraryError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
