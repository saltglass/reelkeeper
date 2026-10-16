package sonarr

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/decision"
	"github.com/saltglass/reelkeeper/internal/library"
)

// Sonarr's JSON, field for field (names, casing and types as Sonarr 4
// sends them; see testdata/compat/sonarr for recorded answers).
// Extra fields would be harmless; missing ones aren't.

type language struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type image struct {
	CoverType string `json:"coverType"`
	URL       string `json:"url"`
	RemoteURL string `json:"remoteUrl"`
}

type alternateTitle struct {
	Title        string `json:"title"`
	SeasonNumber int    `json:"seasonNumber"`
}

type ratings struct {
	Votes int     `json:"votes"`
	Value float64 `json:"value"`
}

// qualityDef is one of Sonarr's qualities. Sonarr numbers and names them
// differently from Radarr (whose names Reelkeeper uses), and has no
// modifier.
type qualityDef struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Source     string `json:"source"`
	Resolution int    `json:"resolution"`
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

// sonarrQualities maps Reelkeeper's quality names to Sonarr's. Qualities
// Sonarr doesn't have (CAM, BR-DISK and the like) are left out; a file of
// one reports as Unknown.
var sonarrQualities = map[string]qualityDef{
	"Unknown":      {0, "Unknown", "unknown", 0},
	"SDTV":         {1, "SDTV", "television", 480},
	"DVD":          {2, "DVD", "dvd", 480},
	"WEBDL-1080p":  {3, "WEBDL-1080p", "web", 1080},
	"HDTV-720p":    {4, "HDTV-720p", "television", 720},
	"WEBDL-720p":   {5, "WEBDL-720p", "web", 720},
	"Bluray-720p":  {6, "Bluray-720p", "bluray", 720},
	"Bluray-1080p": {7, "Bluray-1080p", "bluray", 1080},
	"WEBDL-480p":   {8, "WEBDL-480p", "web", 480},
	"HDTV-1080p":   {9, "HDTV-1080p", "television", 1080},
	"Raw-HD":       {10, "Raw-HD", "televisionRaw", 1080},
	"WEBRip-480p":  {12, "WEBRip-480p", "webRip", 480},
	"Bluray-480p":  {13, "Bluray-480p", "bluray", 480},
	"WEBRip-720p":  {14, "WEBRip-720p", "webRip", 720},
	"WEBRip-1080p": {15, "WEBRip-1080p", "webRip", 1080},
	"HDTV-2160p":   {16, "HDTV-2160p", "television", 2160},
	"WEBRip-2160p": {17, "WEBRip-2160p", "webRip", 2160},
	"WEBDL-2160p":  {18, "WEBDL-2160p", "web", 2160},
	"Bluray-2160p": {19, "Bluray-2160p", "bluray", 2160},
	"Remux-1080p":  {20, "Bluray-1080p Remux", "blurayRaw", 1080},
	"Remux-2160p":  {21, "Bluray-2160p Remux", "blurayRaw", 2160},
	"Bluray-576p":  {22, "Bluray-576p", "bluray", 576},
}

// qualityOf is a Reelkeeper quality in Sonarr's terms (ok false when
// Sonarr has no such quality).
func qualityOf(name string) (qualityDef, bool) {
	q, ok := decision.QualityByName(name)
	if !ok {
		return sonarrQualities["Unknown"], false
	}
	sq, ok := sonarrQualities[q.Name]
	if !ok {
		return sonarrQualities["Unknown"], false
	}
	return sq, true
}

type seasonStatistics struct {
	PreviousAiring    string   `json:"previousAiring,omitempty"`
	NextAiring        string   `json:"nextAiring,omitempty"`
	EpisodeFileCount  int      `json:"episodeFileCount"`
	EpisodeCount      int      `json:"episodeCount"`
	TotalEpisodeCount int      `json:"totalEpisodeCount"`
	SizeOnDisk        int64    `json:"sizeOnDisk"`
	ReleaseGroups     []string `json:"releaseGroups"`
	PercentOfEpisodes float64  `json:"percentOfEpisodes"`
}

type seriesStatistics struct {
	SeasonCount int `json:"seasonCount"`
	seasonStatistics
}

type season struct {
	SeasonNumber int               `json:"seasonNumber"`
	Monitored    bool              `json:"monitored"`
	Statistics   *seasonStatistics `json:"statistics,omitempty"`
}

// series is Sonarr's SeriesResource.
type series struct {
	ID                int64            `json:"id,omitempty"`
	Title             string           `json:"title"`
	AlternateTitles   []alternateTitle `json:"alternateTitles"`
	SortTitle         string           `json:"sortTitle"`
	Status            string           `json:"status"`
	Ended             bool             `json:"ended"`
	Overview          string           `json:"overview"`
	PreviousAiring    string           `json:"previousAiring,omitempty"`
	NextAiring        string           `json:"nextAiring,omitempty"`
	Network           string           `json:"network"`
	AirTime           string           `json:"airTime"`
	Images            []image          `json:"images"`
	OriginalLanguage  language         `json:"originalLanguage"`
	RemotePoster      string           `json:"remotePoster,omitempty"`
	Seasons           []season         `json:"seasons"`
	Year              int              `json:"year"`
	Path              string           `json:"path,omitempty"`
	QualityProfileID  int64            `json:"qualityProfileId"`
	SeasonFolder      bool             `json:"seasonFolder"`
	Monitored         bool             `json:"monitored"`
	MonitorNewItems   string           `json:"monitorNewItems"`
	UseSceneNumbering bool             `json:"useSceneNumbering"`
	Runtime           int              `json:"runtime"`
	TvdbID            int              `json:"tvdbId"`
	TvRageID          int              `json:"tvRageId"`
	TvMazeID          int              `json:"tvMazeId"`
	TmdbID            int              `json:"tmdbId"`
	FirstAired        string           `json:"firstAired,omitempty"`
	LastAired         string           `json:"lastAired,omitempty"`
	SeriesType        string           `json:"seriesType"`
	CleanTitle        string           `json:"cleanTitle"`
	ImdbID            string           `json:"imdbId"`
	TitleSlug         string           `json:"titleSlug"`
	RootFolderPath    string           `json:"rootFolderPath,omitempty"`
	Folder            string           `json:"folder,omitempty"`
	Certification     string           `json:"certification"`
	Genres            []string         `json:"genres"`
	Tags              []int64          `json:"tags"`
	Added             string           `json:"added"`
	Ratings           ratings          `json:"ratings"`
	Statistics        seriesStatistics `json:"statistics"`
	LanguageProfileID int              `json:"languageProfileId"`
}

// episode is Sonarr's EpisodeResource.
type episode struct {
	SeriesID                 int64   `json:"seriesId"`
	TvdbID                   int     `json:"tvdbId"`
	EpisodeFileID            int64   `json:"episodeFileId"`
	SeasonNumber             int     `json:"seasonNumber"`
	EpisodeNumber            int     `json:"episodeNumber"`
	Title                    string  `json:"title"`
	AirDate                  string  `json:"airDate,omitempty"`
	AirDateUtc               string  `json:"airDateUtc,omitempty"`
	Runtime                  int     `json:"runtime"`
	Overview                 string  `json:"overview,omitempty"`
	HasFile                  bool    `json:"hasFile"`
	Monitored                bool    `json:"monitored"`
	UnverifiedSceneNumbering bool    `json:"unverifiedSceneNumbering"`
	ID                       int64   `json:"id"`
	Series                   *series `json:"series,omitempty"`
}

// sonarrTime writes a time the way Sonarr does.
func sonarrTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// sonarrDate turns TMDB's "2021-11-03" into "2021-11-03T00:00:00Z". TMDB
// has air dates, not times, so midnight UTC stands in.
func sonarrDate(d string) string {
	if len(d) != 10 {
		return ""
	}
	return d + "T00:00:00Z"
}

// cleanTitle is Sonarr's "cleanTitle": lower case, letters and digits only.
func cleanTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// seriesStatus is Sonarr's status for TMDB's: ended, continuing, or
// upcoming when nothing has aired yet.
func seriesStatus(t library.Title, today string) string {
	switch t.Status {
	case "Ended", "Canceled":
		return "ended"
	}
	if t.ReleaseDate == "" || t.ReleaseDate > today {
		return "upcoming"
	}
	return "continuing"
}

// imageBase is where TMDB's images are.
const imageBase = "https://image.tmdb.org/t/p/original"

// seriesResource builds a SeriesResource from a title, its episodes, and
// its version in this profile (nil for a lookup result that isn't in it).
func (h *handler) seriesResource(t library.Title, eps []library.Episode, ps *library.ProfileSeries) series {
	today := h.now().UTC().Format("2006-01-02")
	s := series{
		Title: t.Title, SortTitle: strings.ToLower(t.SortTitle), Status: seriesStatus(t, today), Overview: t.Overview,
		Network: t.Network, Year: t.Year, Runtime: t.Runtime, TvdbID: t.TVDBID, TmdbID: t.TMDBID,
		FirstAired: sonarrDate(t.ReleaseDate), LastAired: sonarrDate(t.LastAirDate), SeriesType: firstOf(t.SeriesType, "standard"),
		CleanTitle: cleanTitle(t.Title), ImdbID: t.IMDbID, TitleSlug: fmt.Sprint(t.TMDBID), Certification: t.Certification,
		Genres: t.GenreList(), Tags: []int64{}, Ratings: ratings{Value: t.VoteAverage}, SeasonFolder: true,
		MonitorNewItems: "all", LanguageProfileID: 1, AlternateTitles: []alternateTitle{}, Images: []image{},
		Seasons: []season{}, Added: "0001-01-01T00:00:00Z",
	}
	s.Ended = s.Status == "ended"
	if s.Genres == nil {
		s.Genres = []string{}
	}
	if id := decision.LanguageByCode(t.OriginalLanguage); id != decision.LanguageUnknown {
		s.OriginalLanguage = language{id, decision.LanguageName(id)}
	} else {
		s.OriginalLanguage = language{0, "Unknown"}
	}
	for _, alt := range t.AltTitleList() {
		s.AlternateTitles = append(s.AlternateTitles, alternateTitle{Title: alt, SeasonNumber: -1})
	}
	if t.PosterPath != "" {
		s.Images = append(s.Images, image{CoverType: "poster", URL: imageBase + t.PosterPath, RemoteURL: imageBase + t.PosterPath})
	}
	if t.BackdropPath != "" {
		s.Images = append(s.Images, image{CoverType: "fanart", URL: imageBase + t.BackdropPath, RemoteURL: imageBase + t.BackdropPath})
	}
	s.Statistics.ReleaseGroups = []string{}

	if ps == nil {
		// A lookup result: what Sonarr would add, with every season but
		// the specials ticked.
		s.Folder = folderName(t)
		if t.PosterPath != "" {
			s.RemotePoster = imageBase + t.PosterPath
		}
		for _, n := range seasonNumbers(eps) {
			s.Seasons = append(s.Seasons, season{SeasonNumber: n, Monitored: n > 0})
			if n > 0 {
				s.Statistics.SeasonCount++
			}
		}
		s.Monitored = true
		return s
	}

	// In the library: this profile's version.
	v := ps.Version
	s.ID, s.QualityProfileID, s.Monitored = v.ID, v.ProfileID, v.Monitored
	s.Added = sonarrTime(v.CreatedAt)
	s.RootFolderPath = ps.Root.Path
	s.Path = path.Join(ps.Root.Path, v.Folder)
	if v.MonitorFrom == nil {
		s.MonitorNewItems = "none"
	}
	if ps.Tags != nil {
		s.Tags = ps.Tags
	}
	stats := seasonStats(ps, today)
	groups := map[string]bool{}
	for _, n := range seasonNumbers(eps) {
		st := stats[n]
		s.Seasons = append(s.Seasons, season{SeasonNumber: n, Monitored: st.monitored, Statistics: &st.seasonStatistics})
		if n > 0 {
			s.Statistics.SeasonCount++
		}
		// Sonarr's series totals leave the specials out, except for the
		// episode total.
		s.Statistics.TotalEpisodeCount += st.TotalEpisodeCount
		if n == 0 {
			continue
		}
		s.Statistics.EpisodeFileCount += st.EpisodeFileCount
		s.Statistics.EpisodeCount += st.EpisodeCount
		s.Statistics.SizeOnDisk += st.SizeOnDisk
		for _, g := range st.ReleaseGroups {
			if !groups[g] {
				groups[g] = true
				s.Statistics.ReleaseGroups = append(s.Statistics.ReleaseGroups, g)
			}
		}
		s.PreviousAiring = later(s.PreviousAiring, st.PreviousAiring)
		s.NextAiring = sooner(s.NextAiring, st.NextAiring)
	}
	s.Statistics.PreviousAiring, s.Statistics.NextAiring = "", ""
	s.Statistics.PercentOfEpisodes = percent(s.Statistics.EpisodeFileCount, s.Statistics.EpisodeCount)
	return s
}

// folderName is the folder a series would get.
func folderName(t library.Title) string {
	if t.Year == 0 {
		return t.Title
	}
	return fmt.Sprintf("%s (%d)", t.Title, t.Year)
}

type seasonSummary struct {
	seasonStatistics
	monitored bool
}

// seasonStats works out each season's numbers the way Sonarr counts
// them: episodeCount is the monitored episodes that have aired plus any
// with a file, and a file's size counts in the season of its first
// episode.
func seasonStats(ps *library.ProfileSeries, today string) map[int]*seasonSummary {
	out := map[int]*seasonSummary{}
	seasonOf := map[int64]int{}
	for _, e := range ps.Episodes {
		n := *e.Season
		st := out[n]
		if st == nil {
			st = &seasonSummary{seasonStatistics: seasonStatistics{ReleaseGroups: []string{}}}
			out[n] = st
		}
		seasonOf[e.ID] = n
		st.TotalEpisodeCount++
		has, mon := ps.HasFile[e.ID], ps.Monitored[e.ID]
		aired := e.AirDate != "" && e.AirDate <= today
		if has {
			st.EpisodeFileCount++
		}
		if has || (mon && aired) {
			st.EpisodeCount++
		}
		st.monitored = st.monitored || mon
		switch {
		case aired:
			st.PreviousAiring = later(st.PreviousAiring, sonarrDate(e.AirDate))
		case e.AirDate != "":
			st.NextAiring = sooner(st.NextAiring, sonarrDate(e.AirDate))
		}
	}
	for _, f := range ps.Files {
		eps := ps.FileEpisodes[f.ID]
		if len(eps) == 0 {
			continue
		}
		st := out[seasonOf[eps[0]]]
		if st == nil {
			continue
		}
		st.SizeOnDisk += f.Size
		if f.ReleaseGroup != "" && !slices.Contains(st.ReleaseGroups, f.ReleaseGroup) {
			st.ReleaseGroups = append(st.ReleaseGroups, f.ReleaseGroup)
		}
	}
	for _, st := range out {
		st.PercentOfEpisodes = percent(st.EpisodeFileCount, st.EpisodeCount)
	}
	return out
}

func seasonNumbers(eps []library.Episode) []int {
	seen := map[int]bool{}
	var out []int
	for _, e := range eps {
		if e.Season != nil && !seen[*e.Season] {
			seen[*e.Season] = true
			out = append(out, *e.Season)
		}
	}
	sort.Ints(out)
	return out
}

func percent(n, of int) float64 {
	if of == 0 {
		return 0
	}
	return float64(n) * 100 / float64(of)
}

func later(a, b string) string {
	if b > a {
		return b
	}
	return a
}

func sooner(a, b string) string {
	if a == "" || (b != "" && b < a) {
		return b
	}
	return a
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// episodeFiles maps each episode of a version to the file holding it.
func episodeFiles(ps *library.ProfileSeries) map[int64]int64 {
	out := map[int64]int64{}
	for fileID, eps := range ps.FileEpisodes {
		for _, e := range eps {
			if out[e] == 0 || fileID < out[e] {
				out[e] = fileID
			}
		}
	}
	return out
}

// episodeResource builds an EpisodeResource for one episode of a version.
func episodeResource(ps *library.ProfileSeries, e library.Episode, files map[int64]int64) episode {
	return episode{
		SeriesID: ps.Version.ID, EpisodeFileID: files[e.ID], SeasonNumber: *e.Season, EpisodeNumber: *e.Number,
		Title: firstOf(e.Name, "TBA"), AirDate: e.AirDate, AirDateUtc: sonarrDate(e.AirDate), Runtime: e.Runtime,
		Overview: e.Overview, HasFile: ps.HasFile[e.ID], Monitored: ps.Monitored[e.ID], ID: e.ID,
	}
}
