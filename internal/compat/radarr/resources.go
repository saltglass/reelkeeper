package radarr

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/parser"
)

// Radarr's JSON, field for field (names, casing and types as Radarr 6
// sends them; see testdata/compat/radarr for recorded answers). Extra
// fields would be harmless; missing ones aren't.

type language struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type image struct {
	CoverType string `json:"coverType"`
	URL       string `json:"url"`
	RemoteURL string `json:"remoteUrl"`
}

type rating struct {
	Votes int     `json:"votes"`
	Value float64 `json:"value"`
	Type  string  `json:"type"`
}

type alternateTitle struct {
	SourceType      string `json:"sourceType"`
	MovieMetadataID int64  `json:"movieMetadataId"`
	Title           string `json:"title"`
	ID              int64  `json:"id"`
}

type qualityDef struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Source     string `json:"source"`
	Resolution int    `json:"resolution"`
	Modifier   string `json:"modifier"`
}

type revision struct {
	Version  int  `json:"version"`
	Real     int  `json:"real"`
	IsRepack bool `json:"isRepack"`
}

type qualityModel struct {
	Quality  qualityDef `json:"quality"`
	Revision revision   `json:"revision"`
}

type mediaInfo struct {
	AudioBitrate          int     `json:"audioBitrate"`
	AudioChannels         float64 `json:"audioChannels"`
	AudioCodec            string  `json:"audioCodec"`
	AudioLanguages        string  `json:"audioLanguages"`
	AudioStreamCount      int     `json:"audioStreamCount"`
	VideoBitDepth         int     `json:"videoBitDepth"`
	VideoBitrate          int     `json:"videoBitrate"`
	VideoCodec            string  `json:"videoCodec"`
	VideoFps              float64 `json:"videoFps"`
	VideoDynamicRange     string  `json:"videoDynamicRange"`
	VideoDynamicRangeType string  `json:"videoDynamicRangeType"`
	Resolution            string  `json:"resolution"`
	RunTime               string  `json:"runTime"`
	ScanType              string  `json:"scanType"`
	Subtitles             string  `json:"subtitles"`
}

type movieFile struct {
	ID                  int64        `json:"id"`
	MovieID             int64        `json:"movieId"`
	RelativePath        string       `json:"relativePath"`
	Path                string       `json:"path"`
	Size                int64        `json:"size"`
	DateAdded           string       `json:"dateAdded"`
	SceneName           string       `json:"sceneName"`
	ReleaseGroup        string       `json:"releaseGroup"`
	Edition             string       `json:"edition"`
	Languages           []language   `json:"languages"`
	Quality             qualityModel `json:"quality"`
	CustomFormats       []any        `json:"customFormats"`
	CustomFormatScore   int          `json:"customFormatScore"`
	IndexerFlags        int          `json:"indexerFlags"`
	MediaInfo           mediaInfo    `json:"mediaInfo"`
	OriginalFilePath    string       `json:"originalFilePath"`
	QualityCutoffNotMet bool         `json:"qualityCutoffNotMet"`
}

type statistics struct {
	MovieFileCount     int          `json:"movieFileCount"`
	SizeOnDisk         int64        `json:"sizeOnDisk"`
	ReleaseGroups      []string     `json:"releaseGroups"`
	MovieFileQualities []qualityDef `json:"movieFileQualities"`
}

type collection struct {
	Title  string `json:"title"`
	TmdbID int    `json:"tmdbId"`
}

// movie is Radarr's MovieResource.
type movie struct {
	ID                    int64             `json:"id,omitempty"`
	Title                 string            `json:"title"`
	OriginalTitle         string            `json:"originalTitle"`
	OriginalLanguage      language          `json:"originalLanguage"`
	AlternateTitles       []alternateTitle  `json:"alternateTitles"`
	SecondaryYearSourceID int               `json:"secondaryYearSourceId"`
	SortTitle             string            `json:"sortTitle"`
	SizeOnDisk            int64             `json:"sizeOnDisk"`
	Status                string            `json:"status"`
	Overview              string            `json:"overview"`
	InCinemas             string            `json:"inCinemas,omitempty"`
	PhysicalRelease       string            `json:"physicalRelease,omitempty"`
	DigitalRelease        string            `json:"digitalRelease,omitempty"`
	ReleaseDate           string            `json:"releaseDate,omitempty"`
	Images                []image           `json:"images"`
	Website               string            `json:"website"`
	Year                  int               `json:"year"`
	YouTubeTrailerID      string            `json:"youTubeTrailerId"`
	Studio                string            `json:"studio"`
	Path                  string            `json:"path,omitempty"`
	QualityProfileID      int64             `json:"qualityProfileId"`
	HasFile               bool              `json:"hasFile"`
	MovieFileID           int64             `json:"movieFileId"`
	Monitored             bool              `json:"monitored"`
	MinimumAvailability   string            `json:"minimumAvailability"`
	IsAvailable           bool              `json:"isAvailable"`
	FolderName            string            `json:"folderName"`
	Runtime               int               `json:"runtime"`
	CleanTitle            string            `json:"cleanTitle"`
	ImdbID                string            `json:"imdbId"`
	TmdbID                int               `json:"tmdbId"`
	TitleSlug             string            `json:"titleSlug"`
	RootFolderPath        string            `json:"rootFolderPath,omitempty"`
	Certification         string            `json:"certification"`
	Genres                []string          `json:"genres"`
	Keywords              []string          `json:"keywords"`
	Tags                  []int64           `json:"tags"`
	Added                 string            `json:"added"`
	Ratings               map[string]rating `json:"ratings"`
	MovieFile             *movieFile        `json:"movieFile,omitempty"`
	Collection            *collection       `json:"collection,omitempty"`
	Popularity            float64           `json:"popularity"`
	LastSearchTime        string            `json:"lastSearchTime,omitempty"`
	Statistics            statistics        `json:"statistics"`
	// Lookup results only: the folder Radarr would create, and the poster.
	Folder       string `json:"folder,omitempty"`
	RemotePoster string `json:"remotePoster,omitempty"`
}

// radarrTime writes a time the way Radarr does.
func radarrTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// radarrDate turns TMDB's "2021-11-03" into Radarr's "2021-11-03T00:00:00Z".
func radarrDate(d string) string {
	if len(d) != 10 {
		return ""
	}
	return d + "T00:00:00Z"
}

var radarrSource = map[decision.Source]string{
	decision.SourceUnknown: "unknown", decision.SourceCam: "cam", decision.SourceTeleSync: "telesync",
	decision.SourceTeleCine: "telecine", decision.SourceWorkprint: "workprint", decision.SourceDVD: "dvd",
	decision.SourceTV: "tv", decision.SourceWebDL: "webdl", decision.SourceWebRip: "webrip", decision.SourceBluRay: "bluray",
}

var radarrModifier = map[decision.Modifier]string{
	decision.ModifierNone: "none", decision.ModifierRegional: "regional", decision.ModifierScreener: "screener",
	decision.ModifierRawHD: "rawhd", decision.ModifierBRDisk: "brdisk", decision.ModifierRemux: "remux",
}

func qualityOf(q decision.Quality) qualityDef {
	return qualityDef{ID: q.ID, Name: q.Name, Source: radarrSource[q.Source], Resolution: q.Resolution, Modifier: radarrModifier[q.Modifier]}
}

// fileQuality is a library file's quality in Radarr's terms.
func fileQuality(f library.MediaFile) decision.Quality {
	return decision.QualityOf(parser.Release{Source: parser.Source(f.Source), Resolution: f.Resolution})
}

// cleanTitle is Radarr's "cleanTitle": lower case, letters and digits only.
func cleanTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// status is Radarr's movie status.
func status(t library.Title, now time.Time) string {
	today := now.UTC().Format("2006-01-02")
	switch {
	case t.Status == "Released" || (t.DigitalRelease != "" && t.DigitalRelease <= today) || (t.PhysicalRelease != "" && t.PhysicalRelease <= today):
		return "released"
	case t.TheatricalRelease != "" && t.TheatricalRelease <= today:
		return "inCinemas"
	}
	return "announced"
}

// imageBase is where TMDB's images are, as Radarr's remoteUrl gives them.
const imageBase = "https://image.tmdb.org/t/p/original"

// movieResource builds a MovieResource from a title, and its version in
// this profile (nil for a lookup result that isn't in the library).
func (h *handler) movieResource(t library.Title, pm *library.ProfileMovie, prof *decision.Profile) movie {
	now := h.now()
	m := movie{
		Title: t.Title, OriginalTitle: t.OriginalTitle, SortTitle: strings.ToLower(t.SortTitle),
		Status: status(t, now), Overview: t.Overview, InCinemas: radarrDate(t.TheatricalRelease),
		PhysicalRelease: radarrDate(t.PhysicalRelease), DigitalRelease: radarrDate(t.DigitalRelease),
		Year: t.Year, Runtime: t.Runtime, CleanTitle: cleanTitle(t.Title), ImdbID: t.IMDbID, TmdbID: t.TMDBID,
		TitleSlug: fmt.Sprint(t.TMDBID), Certification: t.Certification, Genres: t.GenreList(),
		Keywords: []string{}, Tags: []int64{},
		// Reelkeeper only has TMDB's rating; the others are there (as Radarr
		// always sends them) but empty.
		Ratings: map[string]rating{
			"tmdb": {Value: t.VoteAverage, Type: "user"}, "imdb": {Type: "user"}, "metacritic": {Type: "user"},
			"rottenTomatoes": {Type: "user"}, "trakt": {Type: "user"},
		},
		Popularity: t.Popularity, MinimumAvailability: "released", AlternateTitles: []alternateTitle{},
		Images: []image{}, Statistics: statistics{ReleaseGroups: []string{}, MovieFileQualities: []qualityDef{}},
	}
	m.ReleaseDate = firstOf(m.DigitalRelease, m.PhysicalRelease)
	m.IsAvailable = m.Status == "released"
	if id := decision.LanguageByCode(t.OriginalLanguage); id != decision.LanguageUnknown {
		m.OriginalLanguage = language{id, decision.LanguageName(id)}
	} else {
		m.OriginalLanguage = language{0, "Unknown"}
	}
	for i, alt := range t.AltTitleList() {
		m.AlternateTitles = append(m.AlternateTitles, alternateTitle{SourceType: "tmdb", MovieMetadataID: t.ID, Title: alt, ID: int64(i + 1)})
	}
	if t.PosterPath != "" {
		m.Images = append(m.Images, image{CoverType: "poster", URL: imageBase + t.PosterPath, RemoteURL: imageBase + t.PosterPath})
	}
	if t.BackdropPath != "" {
		m.Images = append(m.Images, image{CoverType: "fanart", URL: imageBase + t.BackdropPath, RemoteURL: imageBase + t.BackdropPath})
	}
	if t.Collection != "" {
		m.Collection = &collection{Title: t.Collection}
	}
	m.Added = radarrTime(t.CreatedAt)
	if pm == nil {
		m.Folder = fmt.Sprintf("%s (%d)", t.Title, t.Year)
		if t.PosterPath != "" {
			m.RemotePoster = imageBase + t.PosterPath
		}
		return m
	}

	// In the library: this profile's version.
	v := pm.Version
	m.ID, m.QualityProfileID, m.Monitored = v.ID, v.ProfileID, v.Monitored
	m.Added = radarrTime(v.CreatedAt)
	m.RootFolderPath = pm.Root.Path
	m.Path = path.Join(pm.Root.Path, v.Folder)
	m.FolderName = m.Path
	if pm.Tags != nil {
		m.Tags = pm.Tags
	}
	if v.LastSearchAt != nil {
		m.LastSearchTime = radarrTime(*v.LastSearchAt)
	}
	// The best file stands for the movie's file, as Radarr has one per movie.
	var best *library.MediaFile
	for i := range pm.Files {
		f := &pm.Files[i]
		m.Statistics.SizeOnDisk += f.Size
		m.Statistics.MovieFileCount++
		if best == nil || prof.Rank(fileQuality(*f)) > prof.Rank(fileQuality(*best)) {
			best = f
		}
	}
	m.SizeOnDisk = m.Statistics.SizeOnDisk
	if best != nil {
		m.HasFile, m.MovieFileID = true, best.ID
		mf := h.fileResource(*best, pm, prof)
		m.MovieFile = &mf
		m.Statistics.MovieFileQualities = append(m.Statistics.MovieFileQualities, mf.Quality.Quality)
		if mf.ReleaseGroup != "" {
			m.Statistics.ReleaseGroups = append(m.Statistics.ReleaseGroups, mf.ReleaseGroup)
		}
		if m.Path == pm.Root.Path { // a loose file directly in the media folder
			m.Path = path.Join(pm.Root.Path, path.Dir(best.Path))
		}
	}
	return m
}

func (h *handler) fileResource(f library.MediaFile, pm *library.ProfileMovie, prof *decision.Profile) movieFile {
	q := fileQuality(f)
	r := f.Release()
	mf := movieFile{
		ID: f.ID, MovieID: pm.Version.ID, RelativePath: relTo(pm.Version.Folder, f.Path),
		Path: filepath.Join(pm.Root.Path, f.Path), Size: f.Size, DateAdded: radarrTime(f.AddedAt),
		SceneName: "", ReleaseGroup: f.ReleaseGroup, Edition: f.Edition, Languages: []language{},
		Quality:       qualityModel{Quality: qualityOf(q), Revision: revision{Version: max(r.Revision, 1)}},
		CustomFormats: []any{},
	}
	for _, name := range r.Languages {
		if id := decision.LanguageByName(name); id != decision.LanguageUnknown {
			mf.Languages = append(mf.Languages, language{id, name})
		}
	}
	if len(mf.Languages) == 0 {
		mf.Languages = append(mf.Languages, language{decision.LanguageEnglish, "English"})
	}
	if mi := f.Media(); mi != nil {
		if d := mi.Duration; d > 0 {
			mf.MediaInfo.RunTime = fmt.Sprintf("%d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
		}
		if v := mi.Video; v != nil {
			mf.MediaInfo.VideoCodec, mf.MediaInfo.VideoBitDepth = v.Codec, v.BitDepth
			mf.MediaInfo.Resolution = fmt.Sprintf("%dx%d", v.Width, v.Height)
			if len(v.HDR) > 0 {
				mf.MediaInfo.VideoDynamicRange, mf.MediaInfo.VideoDynamicRangeType = "HDR", strings.Join(v.HDR, " ")
			}
		}
		if len(mi.Audio) > 0 {
			mf.MediaInfo.AudioCodec, mf.MediaInfo.AudioChannels = mi.Audio[0].Codec, channels(mi.Audio[0].Channels)
			mf.MediaInfo.AudioStreamCount = len(mi.Audio)
		}
	}
	// Radarr's meaning is "would still upgrade", so it's false while the
	// version's upgrades are off.
	mf.QualityCutoffNotMet = pm.Version.Upgrades && !prof.CutoffMet(&decision.Existing{Name: f.ScoringName(), FileName: filepath.Base(f.Path), Quality: q, Revision: r.Revision, Size: f.Size},
		decision.Movie{Title: pm.Title.Title, Year: pm.Title.Year, Runtime: pm.Title.Runtime, OriginalLanguage: decision.LanguageByCode(pm.Title.OriginalLanguage)})
	return mf
}

func relTo(folder, p string) string {
	if folder != "" && strings.HasPrefix(p, folder+"/") {
		return strings.TrimPrefix(p, folder+"/")
	}
	return filepath.Base(p)
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// channels writes a channel count the way Radarr does: 6 is 5.1, 8 is 7.1.
func channels(n int) float64 {
	switch {
	case n >= 6:
		return float64(n-1) + 0.1
	case n > 0:
		return float64(n)
	}
	return 0
}
