// Package naming turns templates such as "{Movie Title} ({Release Year})"
// or "{Series TitleYear} - S{season:00}E{episode:00} - {Episode Title}"
// into folder and file names.
//
// The syntax is Radarr's for movies and Sonarr's for TV, so naming schemes
// from TRaSH Guides can be pasted in unchanged:
//
//   - {Movie Title} is replaced by the movie's title.
//   - Characters just inside the braces are kept only when the token has a
//     value: {[Quality Full]} gives "[Bluray-1080p]", or nothing at all when
//     the quality is unknown. Likewise {-Release Group} and {(Release Year)}.
//   - The separator in the token name is used in the value: {Movie.Title}
//     gives "The.Dark.Knight".
//   - Upper or lower case token names give upper or lower case values:
//     {MOVIE TITLE}, {movie title}.
//   - {tmdb-{TmdbId}}, {tvdb-{TvdbId}}, {imdb-{ImdbId}} and
//     {edition-{Edition Tags}} write the Plex/Jellyfin tags "{tmdb-155}"
//     and so on, or nothing when empty.
//   - {season:00} and {episode:00} pad numbers with zeros: "S01E02". Files
//     holding several episodes are written in the chosen multi-episode
//     style, such as "S05E01-E02".
//
// Characters that aren't allowed in file names are replaced, and empty
// brackets and dangling separators left by empty tokens are removed.
package naming

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Templates are the naming settings.
type Templates struct {
	MovieFolder string `json:"movieFolder"`
	MovieFile   string `json:"movieFile"`

	SeriesFolder     string `json:"seriesFolder"`
	SeasonFolder     string `json:"seasonFolder"`
	EpisodeFile      string `json:"episodeFile"`
	DailyEpisodeFile string `json:"dailyEpisodeFile"`
	// MultiEpisodeStyle is how a file holding several episodes writes
	// them; one of MultiEpisodeStyles.
	MultiEpisodeStyle string `json:"multiEpisodeStyle"`
}

// Defaults are used until the user saves their own. The TV ones match the
// names Sonarr gives with TRaSH Guides' recommended scheme.
func Defaults() Templates {
	return Templates{
		MovieFolder: "{Movie Title} ({Release Year})",
		MovieFile:   "{Movie Title} ({Release Year}) {[Edition Tags]}{[Quality Full]}{[MediaInfo VideoDynamicRangeType]}{-Release Group}",

		SeriesFolder:      "{Series TitleYear} {tvdb-{TvdbId}}",
		SeasonFolder:      "Season {season}",
		EpisodeFile:       "{Series TitleYear} - S{season:00}E{episode:00} - {Episode CleanTitle} " + episodeQuality,
		DailyEpisodeFile:  "{Series TitleYear} - {Air-Date} - {Episode CleanTitle} " + episodeQuality,
		MultiEpisodeStyle: "prefixed-range",
	}
}

const episodeQuality = "{[Quality Full]}{[MediaInfo VideoDynamicRangeType]}[{MediaInfo AudioCodec}{ MediaInfo AudioChannels}]{[MediaInfo VideoCodec]}{-Release Group}"

// MultiEpisodeStyles are Sonarr's ways of writing a file that holds
// episodes 1 to 3 of season 1.
var MultiEpisodeStyles = []struct{ ID, Example string }{
	{"prefixed-range", "S01E01-E03"},
	{"range", "S01E01-03"},
	{"extend", "S01E01-02-03"},
	{"scene", "S01E01-E02-E03"},
	{"repeat", "S01E01E02E03"},
	{"duplicate", "S01E01.S01E02.S01E03"},
}

// Series is what a template knows about a series.
type Series struct {
	Title         string
	OriginalTitle string
	Year          int
	TMDBID        int
	TVDBID        int
	IMDbID        string
	Certification string
	Profile       string
}

// Episode is what a template knows about one episode in a file.
type Episode struct {
	Season  int
	Number  int
	Title   string
	AirDate string // "2024-03-15"
}

// Movie is what a template knows about the movie.
type Movie struct {
	Title         string
	OriginalTitle string
	Year          int
	TMDBID        int
	IMDbID        string
	Collection    string
	Certification string
	// Profile is the version's profile name (HD, Remux), for keeping
	// several versions in one folder: "Movie (2020) - Remux.mkv".
	Profile string
}

// File is what a template knows about one video file.
type File struct {
	Quality  string // Bluray-1080p
	Revision int    // 2 or more for PROPER/REPACK
	Edition  string // Extended, Director's Cut, IMAX
	Group    string // release group

	Source        string // remux, bluray, webdl, webrip, hdtv, dvd...
	VideoCodec    string // H.264, H.265, AV1...
	VideoBitDepth int
	HDR           []string // DV, HDR10+, HDR10, HLG
	AudioCodec    string   // TrueHD Atmos, DTS-HD MA, EAC3, AC3, AAC...
	AudioChannels string   // 7.1, 5.1, 2.0
}

// facts is everything a template can refer to. Movies fill movie, TV
// fills series (and episodes for file names).
type facts struct {
	movie    Movie
	series   Series
	episodes []Episode
	file     File
}

// Which templates a token may appear in.
type kindSet uint8

const (
	forMovie kindSet = 1 << iota
	forSeries
	forSeason
	forEpisode
	forFile
)

type token struct {
	kinds kindSet
	value func(x *facts) string
}

// Token groups: where each kind of template may use them.
const (
	movieTokens   = forMovie
	seriesTokens  = forSeries | forSeason | forEpisode
	episodeTokens = forEpisode
	fileTokens    = forFile
	idTokens      = forMovie | forSeries | forSeason | forEpisode
)

// tokens maps a token's key (lower case, no separators) to its value.
var tokens = map[string]token{
	"movietitle":               {movieTokens, func(x *facts) string { return x.movie.Title }},
	"moviecleantitle":          {movieTokens, func(x *facts) string { return cleanTitle(x.movie.Title) }},
	"movietitlethe":            {movieTokens, func(x *facts) string { return titleThe(x.movie.Title) }},
	"moviecleantitlethe":       {movieTokens, func(x *facts) string { return cleanTitle(titleThe(x.movie.Title)) }},
	"movieoriginaltitle":       {movieTokens, func(x *facts) string { return x.movie.OriginalTitle }},
	"movietitlefirstcharacter": {movieTokens, func(x *facts) string { return firstCharacter(x.movie.Title) }},
	"moviecollection":          {movieTokens, func(x *facts) string { return x.movie.Collection }},
	"moviecertification":       {movieTokens, func(x *facts) string { return x.movie.Certification }},
	"releaseyear":              {movieTokens, func(x *facts) string { return num(x.movie.Year) }},

	"seriestitle":            {seriesTokens, func(x *facts) string { return x.series.Title }},
	"seriescleantitle":       {seriesTokens, func(x *facts) string { return cleanTitle(x.series.Title) }},
	"seriestitleyear":        {seriesTokens, func(x *facts) string { return titleYear(x.series.Title, x.series.Year) }},
	"seriescleantitleyear":   {seriesTokens, func(x *facts) string { return cleanTitle(titleYear(x.series.Title, x.series.Year)) }},
	"seriestitlewithoutyear": {seriesTokens, func(x *facts) string { return withoutYear(x.series.Title) }},
	"seriescleantitlewithoutyear": {seriesTokens, func(x *facts) string {
		return cleanTitle(withoutYear(x.series.Title))
	}},
	"seriestitlethe":            {seriesTokens, func(x *facts) string { return titleThe(x.series.Title) }},
	"seriescleantitlethe":       {seriesTokens, func(x *facts) string { return cleanTitle(titleThe(x.series.Title)) }},
	"seriestitlefirstcharacter": {seriesTokens, func(x *facts) string { return firstCharacter(x.series.Title) }},
	"seriesyear":                {seriesTokens, func(x *facts) string { return num(x.series.Year) }},
	"tvdbid":                    {seriesTokens, func(x *facts) string { return num(x.series.TVDBID) }},

	"season":            {forSeason | forEpisode, func(x *facts) string { return firstEpisode(x).seasonText() }},
	"episode":           {episodeTokens, func(x *facts) string { return num(firstEpisode(x).Number) }},
	"episodetitle":      {episodeTokens, func(x *facts) string { return episodeTitles(x.episodes) }},
	"episodecleantitle": {episodeTokens, func(x *facts) string { return cleanTitle(episodeTitles(x.episodes)) }},
	// Written with spaces; {Air-Date} and {Air.Date} use their separator.
	"airdate": {episodeTokens, func(x *facts) string { return strings.ReplaceAll(firstEpisode(x).AirDate, "-", " ") }},

	"tmdbid": {idTokens, func(x *facts) string {
		if x.movie.TMDBID != 0 {
			return num(x.movie.TMDBID)
		}
		return num(x.series.TMDBID)
	}},
	"imdbid":      {idTokens, func(x *facts) string { return x.movie.IMDbID + x.series.IMDbID }},
	"profilename": {idTokens | fileTokens, func(x *facts) string { return x.movie.Profile + x.series.Profile }},

	"editiontags":   {fileTokens, func(x *facts) string { return x.file.Edition }},
	"releasegroup":  {fileTokens, func(x *facts) string { return x.file.Group }},
	"qualitytitle":  {fileTokens, func(x *facts) string { return x.file.Quality }},
	"qualityproper": {fileTokens, func(x *facts) string { return proper(x.file) }},
	"qualityfull": {fileTokens, func(x *facts) string {
		return strings.TrimSpace(x.file.Quality + " " + proper(x.file))
	}},
	"mediainfovideocodec":    {fileTokens, func(x *facts) string { return videoCodec(x.file) }},
	"mediainfovideobitdepth": {fileTokens, func(x *facts) string { return num(x.file.VideoBitDepth) }},
	"mediainfovideodynamicrange": {fileTokens, func(x *facts) string {
		if len(x.file.HDR) > 0 {
			return "HDR"
		}
		return ""
	}},
	"mediainfovideodynamicrangetype": {fileTokens, func(x *facts) string { return dynamicRangeType(x.file.HDR) }},
	"mediainfoaudiocodec":            {fileTokens, func(x *facts) string { return x.file.AudioCodec }},
	"mediainfoaudiochannels":         {fileTokens, func(x *facts) string { return x.file.AudioChannels }},
	"mediainfosimple": {fileTokens, func(x *facts) string {
		return strings.TrimSpace(videoCodec(x.file) + " " + x.file.AudioCodec)
	}},
	// Accepted so TRaSH Guides schemes work, but empty for now: Reelkeeper
	// doesn't detect 3D, and custom formats arrive with quality profiles.
	"mediainfo3d":   {fileTokens, func(*facts) string { return "" }},
	"customformats": {fileTokens, func(*facts) string { return "" }},
}

var (
	// The Plex/Jellyfin ID and edition tags, written with literal braces.
	tagRe = regexp.MustCompile(`(?i)^\{(tmdb-|tvdb-|imdb-|edition-)\{([A-Za-z0-9 ._-]+)\}\}`)
	// A token: optional text kept with it, its name, an optional format
	// (":00" pads numbers, ":30" shortens titles), optional text after.
	tokenRe = regexp.MustCompile(`^\{([- ._\[(]*)([A-Za-z0-9]+(?:[- ._][A-Za-z0-9]+)*?)(?::([0-9]+))?([- ._)\]]*)\}`)
	// The season-and-episode part of a TV template: "S{season:00}E{episode:00}".
	seasonEpisodeRe = regexp.MustCompile(`(?i)([A-Za-z]*)\{season(?::(0+))?\}([^{}\s]{0,3}?)\{episode(?::(0+))?\}`)
)

// Kind is what a template names.
type Kind int

// Template kinds.
const (
	MovieFolder Kind = iota
	MovieFile
	SeriesFolder
	SeasonFolder
	EpisodeFile
	DailyEpisodeFile
)

func (k Kind) allows(t token) bool {
	allowed := map[Kind]kindSet{
		MovieFolder: forMovie | forFile, MovieFile: forMovie | forFile,
		SeriesFolder: forSeries, SeasonFolder: forSeason,
		EpisodeFile: forEpisode | forFile, DailyEpisodeFile: forEpisode | forFile,
	}[k]
	return t.kinds&allowed != 0
}

// Check checks a movie template. It returns "" when the template is fine,
// or the first problem in plain words.
func Check(tpl string) string { return CheckKind(MovieFile, tpl) }

// CheckKind checks a template of the given kind.
func CheckKind(k Kind, tpl string) string {
	if strings.TrimSpace(tpl) == "" {
		return "A name can't be empty."
	}
	if strings.ContainsAny(tpl, `/\`) {
		return "Leave out / and \\: this is one name, not a path."
	}
	var unknown []string
	used := map[string]bool{}
	_ = walk(tpl, func(key, raw, _ string) string {
		t, ok := tokens[key]
		if !ok || !k.allows(t) {
			unknown = append(unknown, raw)
		}
		used[key] = true
		return ""
	})
	if len(unknown) > 0 {
		return "Reelkeeper doesn't know " + strings.Join(unknown, ", ") + " here. Check the spelling against the token list."
	}
	hasTitle := func(prefix string) bool {
		for key := range used {
			if strings.HasPrefix(key, prefix) && strings.Contains(key, "title") && !strings.HasSuffix(key, "firstcharacter") {
				return true
			}
		}
		return false
	}
	switch k {
	case MovieFolder, MovieFile:
		if !hasTitle("movie") {
			return "Include {Movie Title} (or another title token) so each movie gets its own name."
		}
	case SeriesFolder:
		if !hasTitle("series") {
			return "Include {Series Title} (or another series title token) so each series gets its own folder."
		}
	case SeasonFolder:
		if !used["season"] {
			return "Include {season} so each season gets its own folder."
		}
	case EpisodeFile:
		if !used["season"] || !used["episode"] {
			return "Include {season} and {episode}, for example S{season:00}E{episode:00}, so each episode gets its own name."
		}
	case DailyEpisodeFile:
		if !used["airdate"] && (!used["season"] || !used["episode"]) {
			return "Include {Air-Date} so each episode gets its own name."
		}
	}
	return ""
}

// Format fills in a movie template. Unknown tokens are left out; check
// templates with Check before saving them.
func Format(tpl string, m Movie, f File) string {
	return format(tpl, &facts{movie: m, file: f})
}

// FormatSeries fills in a series folder template.
func FormatSeries(tpl string, s Series) string {
	return format(tpl, &facts{series: s})
}

// FormatSeason fills in a season folder template. Specials always go in
// "Specials", as Sonarr, Emby, Jellyfin and Plex expect.
func FormatSeason(tpl string, s Series, season int) string {
	if season == 0 {
		return "Specials"
	}
	return format(tpl, &facts{series: s, episodes: []Episode{{Season: season}}})
}

// FormatEpisode fills in an episode file template for a file holding one
// or more episodes (of one season), written in the given multi-episode
// style.
func FormatEpisode(tpl string, s Series, eps []Episode, f File, style string) string {
	if len(eps) > 1 {
		tpl = expandEpisodes(tpl, eps, style)
	}
	return format(tpl, &facts{series: s, episodes: eps, file: f})
}

func format(tpl string, x *facts) string {
	out := walk(tpl, func(key, _, spec string) string {
		t, ok := tokens[key]
		if !ok {
			return ""
		}
		return applySpec(key, t.value(x), spec)
	})
	return SafeName(tidy(out))
}

// applySpec applies a token's format: ":00" pads numbers with zeros,
// ":30" shortens a title to 30 characters.
func applySpec(key, v, spec string) string {
	if spec == "" || v == "" {
		return v
	}
	if key == "season" || key == "episode" {
		for len(v) < len(spec) {
			v = "0" + v
		}
		return v
	}
	if n, err := strconv.Atoi(spec); err == nil && n > 0 {
		if r := []rune(v); len(r) > n {
			return strings.TrimSpace(string(r[:n]))
		}
	}
	return v
}

// expandEpisodes writes a multi-episode file's numbers into the template's
// "S{season:00}E{episode:00}" part, in the chosen style.
func expandEpisodes(tpl string, eps []Episode, style string) string {
	m := seasonEpisodeRe.FindStringSubmatchIndex(tpl)
	if m == nil {
		return tpl
	}
	part := func(i int) string {
		if m[2*i] < 0 {
			return ""
		}
		return tpl[m[2*i]:m[2*i+1]]
	}
	prefix, seasonPad, sep, epPad := part(1), part(2), part(3), part(4)
	season := applySpec("season", strconv.Itoa(eps[0].Season), seasonPad)
	ep := func(e Episode) string { return applySpec("episode", strconv.Itoa(e.Number), epPad) }
	first, last := ep(eps[0]), ep(eps[len(eps)-1])

	b := prefix + season + sep + first
	switch style {
	case "range":
		b += "-" + last
	case "extend":
		for _, e := range eps[1:] {
			b += "-" + ep(e)
		}
	case "scene":
		for _, e := range eps[1:] {
			b += "-" + sep + ep(e)
		}
	case "repeat":
		for _, e := range eps[1:] {
			b += sep + ep(e)
		}
	case "duplicate":
		for _, e := range eps[1:] {
			b += "." + prefix + season + sep + ep(e)
		}
	default: // "prefixed-range"
		b += "-" + sep + last
	}
	return tpl[:m[0]] + b + tpl[m[1]:]
}

// walk copies tpl, replacing each token with what value returns for it,
// applying the token's prefix, suffix, separator and case.
func walk(tpl string, value func(key, raw, spec string) string) string {
	var b strings.Builder
	for i := 0; i < len(tpl); {
		rest := tpl[i:]
		if rest[0] != '{' {
			b.WriteByte(rest[0])
			i++
			continue
		}
		if m := tagRe.FindStringSubmatch(rest); m != nil {
			v := value(key(m[2]), "{"+m[2]+"}", "")
			if v != "" {
				b.WriteString("{" + strings.ToLower(m[1]) + v + "}")
			}
			i += len(m[0])
			continue
		}
		if m := tokenRe.FindStringSubmatch(rest); m != nil {
			name := m[2]
			v := value(key(name), "{"+name+"}", m[3])
			if v != "" {
				b.WriteString(m[1] + styled(name, v) + m[4])
			}
			i += len(m[0])
			continue
		}
		b.WriteByte('{')
		i++
	}
	return b.String()
}

// key is a token's lookup key: "MediaInfo VideoCodec" → "mediainfovideocodec".
func key(name string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '.' || r == '_' || r == '-' {
			return -1
		}
		return unicode.ToLower(r)
	}, name)
}

// styled applies the token name's separator and case to its value.
func styled(name, v string) string {
	if i := strings.IndexAny(name, " ._-"); i >= 0 && name[i] != ' ' {
		v = strings.Join(strings.Fields(v), string(name[i]))
	}
	letters := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return r
		}
		return -1
	}, name)
	switch {
	case letters != "" && letters == strings.ToUpper(letters):
		return strings.ToUpper(v)
	case letters != "" && letters == strings.ToLower(letters):
		return strings.ToLower(v)
	}
	return v
}

var (
	emptyBrackets = regexp.MustCompile(`\(\s*\)|\[\s*\]`)
	spaces        = regexp.MustCompile(`\s{2,}`)
	dashes        = regexp.MustCompile(`(\s-\s*){2,}`)
)

// tidy removes what empty tokens leave behind.
func tidy(s string) string {
	s = emptyBrackets.ReplaceAllString(s, "")
	s = spaces.ReplaceAllString(s, " ")
	s = dashes.ReplaceAllString(s, " - ")
	return strings.Trim(s, " -._")
}

// SafeName replaces characters that aren't allowed in file names on common
// filesystems (and on SMB/NFS shares used by Windows clients).
func SafeName(s string) string {
	r := strings.NewReplacer(":", " -", "/", "-", "\\", "-", "?", "", "*", "", "\"", "'", "<", "", ">", "", "|", "-")
	out := strings.Join(strings.Fields(r.Replace(s)), " ")
	return strings.TrimRight(out, ". ")
}

func num(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func proper(f File) string {
	switch {
	case f.Revision == 2:
		return "Proper"
	case f.Revision > 2:
		return "v" + strconv.Itoa(f.Revision)
	}
	return ""
}

// cleanTitle drops punctuation: "Dune: Part Two" → "Dune Part Two".
func cleanTitle(s string) string {
	s = strings.ReplaceAll(s, "&", "and")
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == ' ', r == '-':
			return r
		case r == '\'' || r == '’':
			return -1
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

var articles = []string{"The ", "A ", "An "}

// titleThe moves a leading article to the end: "The Dark Knight" → "Dark
// Knight, The".
func titleThe(s string) string {
	for _, a := range articles {
		if strings.HasPrefix(s, a) && len(s) > len(a) {
			return s[len(a):] + ", " + strings.TrimSpace(a)
		}
	}
	return s
}

func sortTitle(s string) string {
	for _, a := range articles {
		if strings.HasPrefix(s, a) && len(s) > len(a) {
			return s[len(a):]
		}
	}
	return s
}

// videoCodec writes the codec the way Radarr does: x264/x265 for encodes,
// h264/h265 for untouched web and disc video.
func videoCodec(f File) string {
	encode := slices.Contains([]string{"bluray", "webrip", "hdtv", "dvd"}, strings.ToLower(f.Source))
	switch f.VideoCodec {
	case "H.264":
		if encode {
			return "x264"
		}
		return "h264"
	case "H.265":
		if encode {
			return "x265"
		}
		return "h265"
	case "VC-1":
		return "VC1"
	case "MPEG-2":
		return "MPEG2"
	}
	return f.VideoCodec
}

// dynamicRangeType: "DV HDR10", "HDR10+", "HLG"... (DV first, then the rest).
func dynamicRangeType(hdr []string) string {
	order := []string{"DV", "HDR10+", "HDR10", "HLG", "HDR"}
	var out []string
	for _, o := range order {
		if slices.Contains(hdr, o) {
			out = append(out, o)
		}
	}
	// Plain "HDR" adds nothing next to a specific format.
	if len(out) > 1 && out[len(out)-1] == "HDR" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, " ")
}

// Sample is an example movie for previews in the settings.
type Sample struct {
	Label string
	Movie Movie
	File  File
	Ext   string
}

// Samples are the movies the settings preview names: one with every detail
// filled in, one with almost none, to show how empty tokens disappear.
func Samples() []Sample {
	return []Sample{
		{
			Label: "A remux with everything",
			Movie: Movie{
				Title: "The Dark Knight", OriginalTitle: "The Dark Knight", Year: 2008, TMDBID: 155, IMDbID: "tt0468569",
				Collection: "The Dark Knight Collection", Certification: "PG-13", Profile: "Remux",
			},
			File: File{
				Quality: "Remux-2160p", Edition: "IMAX", Group: "FraMeSToR", Source: "remux", VideoCodec: "H.265",
				VideoBitDepth: 10, HDR: []string{"DV", "HDR10"}, AudioCodec: "TrueHD Atmos", AudioChannels: "7.1",
			},
			Ext: ".mkv",
		},
		{
			Label: "A file with few details",
			Movie: Movie{Title: "Amélie", OriginalTitle: "Le Fabuleux Destin d'Amélie Poulain", Year: 2001, TMDBID: 194, Profile: "HD"},
			File:  File{Quality: "WEBDL-1080p", Source: "webdl", VideoCodec: "H.264", AudioCodec: "AAC", AudioChannels: "2.0"},
			Ext:   ".mp4",
		},
	}
}

func firstCharacter(title string) string {
	for _, r := range sortTitle(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return strings.ToUpper(string(r))
		}
	}
	return ""
}

var trailingYear = regexp.MustCompile(`\s*\((19|20)\d{2}\)$`)

// titleYear is "Title (2020)", unless the title already ends with its year.
func titleYear(title string, year int) string {
	if year == 0 || trailingYear.MatchString(title) {
		return title
	}
	return title + " (" + strconv.Itoa(year) + ")"
}

// withoutYear drops a year TMDB keeps in some titles: "Doctor Who (2005)".
func withoutYear(title string) string { return trailingYear.ReplaceAllString(title, "") }

func firstEpisode(x *facts) Episode {
	if len(x.episodes) == 0 {
		return Episode{Season: -1}
	}
	return x.episodes[0]
}

// seasonText is the season number as text; season 0 (Specials) is "0".
func (e Episode) seasonText() string {
	if e.Season < 0 {
		return ""
	}
	return strconv.Itoa(e.Season)
}

var partSuffix = regexp.MustCompile(`(?i)\s*(?:\((?:part\s*)?\d+\)|,?\s+part\s+\d+)$`)

// episodeTitles names the episodes in a file: one title, or "A Little
// Kiss" for "A Little Kiss (1)" and "(2)", or "Title One + Title Two".
func episodeTitles(eps []Episode) string {
	if len(eps) == 0 {
		return ""
	}
	if len(eps) == 1 {
		return eps[0].Title
	}
	base := partSuffix.ReplaceAllString(eps[0].Title, "")
	same := base != ""
	titles := make([]string, 0, len(eps))
	for _, e := range eps {
		titles = append(titles, e.Title)
		if partSuffix.ReplaceAllString(e.Title, "") != base {
			same = false
		}
	}
	if same {
		return base
	}
	return strings.Join(titles, " + ")
}

// EpisodeSample is an example episode file for previews in the settings.
type EpisodeSample struct {
	Label    string
	Series   Series
	Episodes []Episode
	File     File
	Daily    bool
	Ext      string
}

// EpisodeSamples are the episodes the settings preview names: an ordinary
// one, a file holding two episodes, a special and a daily show.
func EpisodeSamples() []EpisodeSample {
	expanse := Series{Title: "The Expanse", Year: 2015, TMDBID: 63639, TVDBID: 280619, IMDbID: "tt3230854", Profile: "HD"}
	return []EpisodeSample{
		{
			Label: "An episode", Series: expanse,
			Episodes: []Episode{{Season: 4, Number: 5, Title: "Oppressor", AirDate: "2019-12-12"}},
			File:     File{Quality: "WEBDL-1080p", Source: "webdl", VideoCodec: "H.264", AudioCodec: "EAC3", AudioChannels: "5.1", Group: "NTb"},
			Ext:      ".mkv",
		},
		{
			Label: "Two episodes in one file", Series: expanse,
			Episodes: []Episode{{Season: 1, Number: 9, Title: "Critical Mass"}, {Season: 1, Number: 10, Title: "Leviathan Wakes"}},
			File:     File{Quality: "Bluray-1080p", Source: "bluray", VideoCodec: "H.264", AudioCodec: "DTS", AudioChannels: "5.1", Group: "ROVERS"},
			Ext:      ".mkv",
		},
		{
			Label: "A special", Series: expanse,
			Episodes: []Episode{{Season: 0, Number: 1, Title: "Behind the Scenes"}},
			File:     File{Quality: "WEBDL-720p", Source: "webdl", VideoCodec: "H.264"},
			Ext:      ".mkv",
		},
		{
			Label: "A daily show", Series: Series{Title: "The Daily Show", Year: 1996, TMDBID: 2224, TVDBID: 71256},
			Episodes: []Episode{{Season: 2024, Number: 51, Title: "Guest Name", AirDate: "2024-03-15"}},
			File:     File{Quality: "WEBDL-720p", Source: "webdl", VideoCodec: "H.264", AudioCodec: "AAC", AudioChannels: "2.0"},
			Daily:    true,
			Ext:      ".mkv",
		},
	}
}
