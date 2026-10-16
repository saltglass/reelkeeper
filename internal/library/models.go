// Package library is Reelkeeper's knowledge of what's on disk: root folders,
// titles (movies, later series), their versions (HD, Remux...) and media
// files. It scans folders, identifies what each folder holds with TMDB's
// help, and keeps a review queue for folders it isn't sure about.
//
// While a root folder's ManageFiles switch is off, Reelkeeper only reads it.
package library

import (
	"encoding/json"
	"time"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/mediainfo"
	"github.com/saltglass/reelkeeper/internal/parser"
)

// Media types.
const (
	Movie  = "movie"
	Series = "series"
)

// Profile is a quality profile. In M1 it's a name only.
type Profile struct {
	bun.BaseModel `bun:"table:profiles"`

	ID        int64     `bun:"id,pk,autoincrement" json:"id"`
	Name      string    `bun:"name" json:"name"`
	Slug      string    `bun:"slug" json:"slug"`
	AppliesTo string    `bun:"applies_to" json:"appliesTo"`
	CreatedAt time.Time `bun:"created_at" json:"-"`

	// Mode is "simple" or "advanced"; Simple and Advanced hold each form
	// as JSON (see profile_settings.go).
	Mode     string `bun:"mode" json:"-"`
	Simple   string `bun:"simple" json:"-"`
	Advanced string `bun:"advanced" json:"-"`
}

// RootFolder is a folder Reelkeeper scans for media.
type RootFolder struct {
	bun.BaseModel `bun:"table:root_folders"`

	ID               int64  `bun:"id,pk,autoincrement"`
	Path             string `bun:"path"`
	MediaType        string `bun:"media_type"`
	DefaultProfileID int64  `bun:"default_profile_id"`
	ManageFiles      bool   `bun:"manage_files"`
	// UpgradeFound (movie folders): whether movies found here, from files
	// already on disk or a Radarr import, start with Version.Upgrades on.
	// New folders default to off. Series folders store true and ignore it.
	UpgradeFound bool       `bun:"upgrade_found"`
	CreatedAt    time.Time  `bun:"created_at"`
	LastScanAt   *time.Time `bun:"last_scan_at"`
}

// Title is a movie (or, from M2, a series) and its TMDB metadata.
type Title struct {
	bun.BaseModel `bun:"table:titles"`

	ID                int64      `bun:"id,pk,autoincrement"`
	MediaType         string     `bun:"media_type"`
	TMDBID            int        `bun:"tmdb_id"`
	IMDbID            string     `bun:"imdb_id"`
	TVDBID            int        `bun:"tvdb_id"`
	Title             string     `bun:"title"`
	OriginalTitle     string     `bun:"original_title"`
	OriginalLanguage  string     `bun:"original_language"` // ISO 639-1 ("en"), "" if unknown
	SortTitle         string     `bun:"sort_title"`
	Year              int        `bun:"year"`
	Overview          string     `bun:"overview"`
	Runtime           int        `bun:"runtime"`
	Status            string     `bun:"status"`
	Genres            string     `bun:"genres"` // JSON array
	Collection        string     `bun:"collection"`
	Certification     string     `bun:"certification"`
	ReleaseDate       string     `bun:"release_date"`
	TheatricalRelease string     `bun:"theatrical_release"`
	DigitalRelease    string     `bun:"digital_release"`
	PhysicalRelease   string     `bun:"physical_release"`
	PosterPath        string     `bun:"poster_path"`
	BackdropPath      string     `bun:"backdrop_path"`
	Popularity        float64    `bun:"popularity"`
	VoteAverage       float64    `bun:"vote_average"`
	AltTitles         string     `bun:"alt_titles"` // JSON array
	MetadataUpdatedAt *time.Time `bun:"metadata_updated_at"`
	CreatedAt         time.Time  `bun:"created_at"`

	// Series only.
	Network     string `bun:"network"`
	SeriesType  string `bun:"series_type"` // "standard" or "daily"
	LastAirDate string `bun:"last_air_date"`
	// EpisodeOrder is the order the series' files follow: "" for TMDB's
	// aired order, or a TMDB episode group ID (DVD order and the like).
	EpisodeOrder     string `bun:"episode_order"`
	EpisodeOrderName string `bun:"episode_order_name"`
}

// GenreList decodes Genres.
func (t Title) GenreList() []string { return decodeList(t.Genres) }

// AltTitleList decodes AltTitles.
func (t Title) AltTitleList() []string { return decodeList(t.AltTitles) }

func decodeList(s string) []string {
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func encodeList(l []string) string {
	if l == nil {
		l = []string{}
	}
	b, _ := json.Marshal(l)
	return string(b)
}

// Version is one copy of a title in one profile.
type Version struct {
	bun.BaseModel `bun:"table:versions"`

	ID           int64  `bun:"id,pk,autoincrement"`
	TitleID      int64  `bun:"title_id"`
	ProfileID    int64  `bun:"profile_id"`
	RootFolderID int64  `bun:"root_folder_id"`
	Folder       string `bun:"folder"` // relative to the root folder
	Monitored    bool   `bun:"monitored"`
	// Upgrades (movies only): whether Reelkeeper may replace the file with a
	// better release. Off, it keeps the file it has, but still searches
	// when the file is missing. Series versions keep it true and ignore it.
	// Bun writes false for a zero bool, so every insert sets it.
	Upgrades  bool      `bun:"upgrades"`
	CreatedAt time.Time `bun:"created_at"`
	// LastSearchAt is when Reelkeeper last searched indexers for this version.
	LastSearchAt *time.Time `bun:"last_search_at"`
	// MonitorFrom (series only, "YYYY-MM-DD"): episodes airing on or after
	// it are wanted unless an EpisodeMonitoring row says otherwise. Nil:
	// none are, by default.
	MonitorFrom *string `bun:"monitor_from"`
}

// EpisodeMonitoring overrides, for one series version, whether an episode
// is wanted (see Version.MonitorFrom).
type EpisodeMonitoring struct {
	bun.BaseModel `bun:"table:episode_monitoring"`

	VersionID int64 `bun:"version_id,pk"`
	EpisodeID int64 `bun:"episode_id,pk"`
	Monitored bool  `bun:"monitored"`
}

// MediaFile is a video file belonging to a version.
type MediaFile struct {
	bun.BaseModel `bun:"table:media_files"`

	ID           int64      `bun:"id,pk,autoincrement"`
	VersionID    int64      `bun:"version_id"`
	RootFolderID int64      `bun:"root_folder_id"`
	Path         string     `bun:"path"` // relative to the root folder
	Size         int64      `bun:"size"`
	ModifiedAt   time.Time  `bun:"modified_at"`
	Resolution   int        `bun:"resolution"`
	Source       string     `bun:"source"`
	Quality      string     `bun:"quality"`
	Edition      string     `bun:"edition"`
	ReleaseGroup string     `bun:"release_group"`
	ReleaseInfo  string     `bun:"release_info"` // parser.Release as JSON
	MediaInfo    string     `bun:"media_info"`   // mediainfo.Info as JSON
	ProbeError   string     `bun:"probe_error"`
	AddedAt      time.Time  `bun:"added_at"`
	ProbedAt     *time.Time `bun:"probed_at"`
	MissingSince *time.Time `bun:"missing_since"`
	// Episodes only: what the scanner read about the file's episode (JSON
	// EpisodeClaim), and a plain-English problem placing it, if any.
	EpisodeClaim string `bun:"episode_claim"`
	MatchNote    string `bun:"match_note"`
}

// Release decodes ReleaseInfo.
func (f MediaFile) Release() parser.Release {
	var r parser.Release
	_ = json.Unmarshal([]byte(f.ReleaseInfo), &r)
	return r
}

// Media decodes MediaInfo; nil if the file couldn't be read.
func (f MediaFile) Media() *mediainfo.Info {
	if f.MediaInfo == "" || f.MediaInfo == "{}" || f.MediaInfo == "null" {
		return nil
	}
	var m mediainfo.Info
	if json.Unmarshal([]byte(f.MediaInfo), &m) != nil {
		return nil
	}
	return &m
}

// Review statuses.
const (
	NeedsReview = "needs_review"
	Ignored     = "ignored"
)

// ScanItem is a folder (or loose file) the scanner couldn't identify for
// sure. It stays until someone picks the right title or ignores it.
type ScanItem struct {
	bun.BaseModel `bun:"table:scan_items"`

	ID           int64     `bun:"id,pk,autoincrement"`
	RootFolderID int64     `bun:"root_folder_id"`
	Path         string    `bun:"path"`
	Status       string    `bun:"status"`
	TitleGuess   string    `bun:"title_guess"`
	YearGuess    int       `bun:"year_guess"`
	Reason       string    `bun:"reason"`
	Candidates   string    `bun:"candidates"` // JSON []Candidate
	Files        string    `bun:"files"`      // JSON []string, relative paths
	CreatedAt    time.Time `bun:"created_at"`
	UpdatedAt    time.Time `bun:"updated_at"`
}

// CandidateList decodes Candidates.
func (s ScanItem) CandidateList() []Candidate {
	var out []Candidate
	_ = json.Unmarshal([]byte(s.Candidates), &out)
	return out
}

// FileList decodes Files.
func (s ScanItem) FileList() []string { return decodeList(s.Files) }

// Candidate is a possible TMDB match for a folder.
type Candidate struct {
	TMDBID     int     `json:"tmdbId"`
	Title      string  `json:"title"`
	Year       int     `json:"year"`
	Overview   string  `json:"overview"`
	PosterPath string  `json:"posterPath"`
	Score      float64 `json:"score"` // 0-1
	Votes      int     `json:"votes"` // TMDB vote count: how well known the film is
}

// Season is one of a series' seasons as TMDB has them (0 is Specials).
type Season struct {
	bun.BaseModel `bun:"table:seasons"`

	ID           int64  `bun:"id,pk,autoincrement"`
	TitleID      int64  `bun:"title_id"`
	SeasonNumber int    `bun:"season_number"`
	Name         string `bun:"name"`
	Overview     string `bun:"overview"`
	AirDate      string `bun:"air_date"`
	PosterPath   string `bun:"poster_path"`
}

// Episode is one episode of a series. AiredSeason/AiredNumber are TMDB's
// default numbers; Season/Number are its numbers in the series' chosen
// order (nil when that order leaves it out).
type Episode struct {
	bun.BaseModel `bun:"table:episodes"`

	ID          int64  `bun:"id,pk,autoincrement"`
	TitleID     int64  `bun:"title_id"`
	TMDBID      int    `bun:"tmdb_id"`
	AiredSeason int    `bun:"aired_season"`
	AiredNumber int    `bun:"aired_number"`
	Season      *int   `bun:"season"`
	Number      *int   `bun:"number"`
	Name        string `bun:"name"`
	Overview    string `bun:"overview"`
	AirDate     string `bun:"air_date"`
	Runtime     int    `bun:"runtime"`
	StillPath   string `bun:"still_path"`
	EpisodeType string `bun:"episode_type"`
}

// EpisodeFile links a file to an episode it holds.
type EpisodeFile struct {
	bun.BaseModel `bun:"table:episode_files"`

	EpisodeID   int64 `bun:"episode_id,pk"`
	MediaFileID int64 `bun:"media_file_id,pk"`
}

// EpisodeClaim is what a file's name (or its season folder) says about
// which episode it holds. Kept on the file so it can be placed again when
// the series' episode order changes.
type EpisodeClaim struct {
	Season   int    `json:"season"` // -1 when nothing says
	Episodes []int  `json:"episodes,omitempty"`
	AirDate  string `json:"airDate,omitempty"`
	Title    string `json:"title,omitempty"`
}

// Claim decodes EpisodeClaim; Season is -1 if the file has none.
func (f MediaFile) Claim() EpisodeClaim {
	c := EpisodeClaim{Season: -1}
	if f.EpisodeClaim != "" && f.EpisodeClaim != "{}" {
		_ = json.Unmarshal([]byte(f.EpisodeClaim), &c)
	}
	return c
}
