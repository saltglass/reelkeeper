// Package parser reads release and file names such as
//
//	28.Days.Later.2002.1080p.BluRay.DDP5.1.x265.10bit-GalaxyRG265.mkv
//	Despicable Me 4 (2024) {tmdb-519182} - [WEBRip-2160p][AAC 5.1][DV HDR10Plus][HEVC]-PSA.mkv
//
// and returns what they claim: title, year, IDs, resolution, source, codecs,
// HDR, audio, edition, release group and so on.
//
// Everything here is a pure function of the name: no I/O, no lookups. Names
// can be wrong, so the scanner prefers facts read from the file itself
// (internal/mediainfo) where both exist.
//
// How it works, in short: the name is cut into tokens at separators (space,
// dot, underscore, brackets...). Each token is recognised as a quality
// marker (1080p, BluRay, x265...), a year, or plain text. The title is the
// text before the year that ends it, or before the first quality marker if
// there is no year.
//
// Quality (quality.go), release group (group.go), edition (edition.go) and
// languages (language.go) follow Radarr's own parser rules, adapted to Go.
// Radarr is GPL-3.0 like Reelkeeper, and its test cases run against this parser
// (radarr_test.go, testdata/parser/radarr). Those parts read the whole name,
// but with the title swapped for a stand-in first, so a word in the title
// ("The German", "Spider-Man") is never mistaken for a language or a group.
package parser

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Source is where a release came from, best first.
type Source string

// Sources, from best to worst. The scanner and (later) quality profiles
// rely on this order (see rank). The string values are stored in the
// database and must not change.
//
// The last group is Radarr's odd ones, which a library normally should not
// contain: SourceRegional (a DVD from a foreign region, often a poor
// transfer), SourceWorkprint (an unfinished cut). They are kept so that a
// name which says so is not mistaken for something better.
const (
	SourceRemux     Source = "Remux"
	SourceBRDisk    Source = "BR-DISK" // a whole Blu-ray disc (BDMV/ISO), not a movie file
	SourceBluRay    Source = "BluRay"  // a Blu-ray re-encode
	SourceWebDL     Source = "WEB-DL"
	SourceWebRip    Source = "WEBRip"
	SourceRawHD     Source = "Raw-HD" // an untouched HD broadcast capture (MPEG-2 transport stream)
	SourceHDTV      Source = "HDTV"   // also SD television captures when the resolution is low
	SourceDVDR      Source = "DVD-R"  // a full DVD disc image
	SourceDVD       Source = "DVD"
	SourceScreener  Source = "Screener"
	SourceTeleCine  Source = "TeleCine"
	SourceTeleSync  Source = "TeleSync"
	SourceCam       Source = "CAM"
	SourceRegional  Source = "Regional"
	SourceWorkprint Source = "Workprint"
	SourceUnknown   Source = ""
)

// Release is what a name says about a release.
type Release struct {
	Title string `json:"title"`
	Year  int    `json:"year,omitempty"`

	// IDs written into the name, such as {tmdb-11674} or tt0289043.
	TMDBID int    `json:"tmdbId,omitempty"`
	IMDbID string `json:"imdbId,omitempty"`
	TVDBID int    `json:"tvdbId,omitempty"`

	// Resolution is the vertical resolution: 2160, 1080, 720, 576, 480, or 0.
	Resolution int    `json:"resolution,omitempty"`
	Source     Source `json:"source,omitempty"`
	// Service is the streaming service for web releases (AMZN, NF, ATVP...).
	Service string `json:"service,omitempty"`

	VideoCodec string   `json:"videoCodec,omitempty"` // H.264, H.265, AV1, VC-1, MPEG-2, XviD
	BitDepth   int      `json:"bitDepth,omitempty"`   // 10 when the name says 10bit
	HDR        []string `json:"hdr,omitempty"`        // DV, HDR10+, HDR10, HDR, HLG
	Audio      []string `json:"audio,omitempty"`      // TrueHD, Atmos, DTS-HD MA, DTS-X, DTS, DD+, DD, AAC, FLAC, Opus, LPCM
	Channels   string   `json:"channels,omitempty"`   // 2.0, 5.1, 7.1

	// Languages are the audio languages the name states, in English
	// ("French", "Portuguese (Brazil)"). Empty when it states none, which
	// usually means the film's original language.
	Languages []string `json:"languages,omitempty"`

	// Edition is a cut or special release: Extended, Director's Cut, IMAX...
	Edition string `json:"edition,omitempty"`
	// Revision is 2 or more for PROPER/REPACK releases that replace an
	// earlier one.
	Revision int `json:"revision,omitempty"`
	// Proper is set when the name says PROPER (as opposed to REPACK, RERIP,
	// "v2" or REAL). A PROPER is another group's fix for a bad release; a
	// REPACK is a group fixing its own, which TV fixes tell apart.
	Proper bool `json:"proper,omitempty"`
	// Group is the release group, such as PSA or FLUX.
	Group string `json:"group,omitempty"`

	// Episode is set for TV names: which season and episode(s) the name
	// claims. Nil for movies.
	Episode *Episode `json:"episode,omitempty"`
}

// videoExtensions are stripped from the end of names.
var videoExtensions = []string{
	".mkv", ".mp4", ".m4v", ".avi", ".ts", ".m2ts", ".wmv", ".mov", ".mpg", ".mpeg", ".webm", ".iso",
}

// IsVideo reports whether a file name has a video extension.
func IsVideo(name string) bool {
	ext := strings.ToLower(name[strings.LastIndexByte(name, '.')+1:])
	return strings.Contains(name, ".") && slices.Contains(videoExtensions, "."+ext)
}

var (
	// Junk that some sites put in front of names:
	// "www.UIndex.org    -    Name", "[ www.Torrenting.com ] - Name",
	// "[TorrentCouch.net] - Name". Only "www." addresses or bracketed ones:
	// a bare "The.Sea.Beast" must not lose "The.Se" as if it were a site.
	junkPrefix = regexp.MustCompile(`(?i)^\s*(?:\[\s*(?:www\.)?[a-z0-9-]+\.[a-z]{2,6}\s*\]|www\.[a-z0-9-]+\.[a-z]{2,6})\s*(?:-\s*)?`)

	// Plex/Radarr edition tags: "{edition-Extended}".
	editionTag = regexp.MustCompile(`(?i)\{\s*edition-([^}]+)\}`)

	tmdbTag = regexp.MustCompile(`(?i)[\[{(]\s*tmdb(?:id)?\s*[-=: ]\s*(\d+)\s*[\]})]`)
	tvdbTag = regexp.MustCompile(`(?i)[\[{(]\s*tvdb(?:id)?\s*[-=: ]\s*(\d+)\s*[\]})]`)
	imdbTag = regexp.MustCompile(`(?i)[\[{(]?\s*(?:imdb(?:id)?\s*[-=: ]\s*)?(tt\d{7,9})\s*[\]})]?`)

	// Site tags after the group: "[TGx]", "[rarbg]", and any short tag
	// stuck to the group itself: "-DRONES[EtHD]", "-BYNDR[PiaSub]". Tags
	// that Radarr counts as release groups ("[QxR]", "[YTS.MX]") are not
	// stripped; see group.go.
	trailingSiteTag     = regexp.MustCompile(`(?i)\s*\[(?:ethd|tgx|rarbg|publichd|eztv(?:x)?|ettv|rartv|ion10|prof|psa)\]\s*$`)
	attachedTrailingTag = regexp.MustCompile(`(-[A-Za-z0-9]{2,24})\[[A-Za-z0-9.]{2,12}\]\s*$`)

	yearRe = regexp.MustCompile(`^(18\d{2}|19\d{2}|20\d{2})$`)
	resRe  = regexp.MustCompile(`(?i)^(\d{3,4})[pi]$`)
	// wordRes is a word with a resolution stuck on: "Alive-1080p".
	wordRes = regexp.MustCompile(`(?i)^(.*[a-z].*)-(\d{3,4}p)$`)
)

// akaRegex finds "a.k.a." between two parts of a title, which Radarr writes
// "AKA".
var akaRegex = regexp.MustCompile(`(?i)(^|\s)A\.K\.A\.?(\s|$)`)

// token is one piece of a name with its position (in runes of the
// normalised name), so the title can be cut out in its original spelling.
type token struct {
	text  string // as written
	lower string
	start int
	end   int
}

// animePrefix is a release group written first in brackets: "[Group] Title".
var animePrefix = regexp.MustCompile(`^\[([^\]]+)\][-_. ]?`)

// softMarkers are tokens that mark an edition or revision rather than the
// quality. They end the title like any marker, but unlike quality markers
// they may sit before the year ("Movie.Extended.2009.1080p"), so they must
// not stop the search for it.
var softMarkers = map[string]bool{
	"proper": true, "repack": true, "rerip": true, "real": true, "remastered": true, "imax": true,
	"extended": true, "unrated": true, "uncut": true, "theatrical": true, "criterion": true,
	"uncensored": true, "redux": true, "director's": true, "directors": true, "cut": true, "final": true,
}

var versionToken = regexp.MustCompile(`^v\d{1,2}$`)

// Parse reads a release, file or folder name.
func Parse(name string) Release {
	var r Release
	s := strings.TrimSpace(name)
	for _, ext := range videoExtensions {
		if len(s) > len(ext) && strings.EqualFold(s[len(s)-len(ext):], ext) {
			s = s[:len(s)-len(ext)]
			break
		}
	}
	s = junkPrefix.ReplaceAllString(s, "")

	if m := editionTag.FindStringSubmatch(s); m != nil {
		r.Edition = strings.TrimSpace(m[1])
		s = editionTag.ReplaceAllString(s, " ")
	}
	tagEdition := r.Edition != ""

	// IDs first, then remove them so they don't look like years or titles.
	if m := tmdbTag.FindStringSubmatch(s); m != nil {
		r.TMDBID, _ = strconv.Atoi(m[1])
		s = tmdbTag.ReplaceAllString(s, " ")
	}
	if m := tvdbTag.FindStringSubmatch(s); m != nil {
		r.TVDBID, _ = strconv.Atoi(m[1])
		s = tvdbTag.ReplaceAllString(s, " ")
	}
	if m := imdbTag.FindStringSubmatch(s); m != nil {
		r.IMDbID = strings.ToLower(m[1])
		s = imdbTag.ReplaceAllString(s, " ")
	}
	s = strings.TrimSpace(s)

	// "[Group] Title ...": anime style, the bracket is the release group.
	animeGroup := ""
	if m := animePrefix.FindStringSubmatch(s); m != nil {
		rest := strings.TrimSpace(s[len(m[0]):])
		if rest != "" && !strings.HasPrefix(rest, "[") && !strings.HasPrefix(rest, "(") &&
			!yearRe.MatchString(m[1]) && !isMarker(strings.ToLower(m[1])) {
			animeGroup = strings.TrimSpace(m[1])
			s = rest
		}
	}

	s = trailingSiteTag.ReplaceAllString(s, "")
	s = attachedTrailingTag.ReplaceAllStringFunc(s, func(m string) string {
		// Keep a tag that is itself a known release group ("-Name[TAoE]").
		if g := attachedTrailingTag.FindStringSubmatch(m); g != nil {
			if open := strings.LastIndexByte(m, '['); open > 0 && exactGroupRegex.MatchString(m[open+1:len(m)-1]) {
				return m
			}
			return g[1]
		}
		return m
	})
	tokens := tokenize(s)
	n := len(tokens)
	runes := []rune(s)

	// Find the markers: the first quality marker (resolution, source,
	// codec...) and the first edition/revision one. A scratch Release is
	// used because the real one only takes markers from after the title.
	hardFirst, softFirst := n, n
	var scratch Release
	for i := range tokens {
		if scratch.apply(tokens, i) {
			if isSoftMarker(tokens, i) {
				softFirst = min(softFirst, i)
			} else {
				hardFirst = min(hardFirst, i)
			}
		}
	}

	// TV names: an episode marker ("S01E02") ends the series title the way
	// quality markers end a movie's.
	markerStart := min(hardFirst, softFirst)
	ep, epStart, epEnd := findEpisode(tokens, markerStart)
	yearLimit := hardFirst
	if ep != nil {
		yearLimit = min(yearLimit, epStart)
	}

	// The year that ends the title: the last year before the first quality
	// marker. "1917 (2019)" and "Blade Runner 2049 (2017)" keep their
	// numbers in the title; a lone "1917" is a title, not a year.
	titleStart := 0
	yearAt := -1
	for i := 1; i < yearLimit; i++ {
		if yearRe.MatchString(tokens[i].text) {
			yearAt = i
		}
	}
	if yearAt < 0 && n > 1 && yearRe.MatchString(tokens[0].text) && tokens[0].start > 0 &&
		(runes[tokens[0].start-1] == '(' || runes[tokens[0].start-1] == '[') {
		// "(1995) Movie Name": the year comes first, in brackets.
		yearAt, titleStart = 0, 1
	}
	if yearAt < 0 && ep == nil {
		// No year before the quality markers. Some names put it after
		// ("Title FRENCH BluRay 720p 2016"); take the first one there.
		for i := hardFirst + 1; i < n; i++ {
			if yearRe.MatchString(tokens[i].text) {
				yearAt = i
				break
			}
		}
	}

	// Where the title ends: at the earliest of the year, a marker, a
	// language word that German/French releases put right after the title
	// ("Movie.German.DL"), an episode marker, an edition written just
	// before the year, and (anime) a version tag or a second bracket.
	cut := n
	consider := func(i int) {
		if i > titleStart && i < cut {
			cut = i
		}
	}
	if hardFirst == titleStart && hardFirst < n {
		cut = hardFirst // a name that opens with a quality marker has no title
	}
	consider(hardFirst)
	consider(softFirst)
	if yearAt > titleStart {
		consider(yearAt)
	}
	if ep != nil {
		cut = min(cut, epStart)
	}
	langCut := n
	for i := 1; i < n; i++ {
		if (tokens[i].lower == "german" || tokens[i].lower == "truefrench") && tokens[i-1].lower != "good" && tokens[i-1].lower != "the" &&
			!yearBefore(tokens, i) {
			langCut = i
			break
		}
	}
	consider(langCut)
	if animeGroup != "" {
		for i := 1; i < n; i++ {
			if versionToken.MatchString(tokens[i].lower) || runes[tokens[i].start-1] == '[' {
				consider(i)
				break
			}
		}
	}

	// Editions: Radarr's pattern finds the ones this parser has no word for
	// ("Special Edition", "50th Anniversary", "Despecialized"). One written
	// right before the anchor (year, marker or end) ends the title; one
	// after the title is the release's edition. Anything else is part of
	// the title ("The Ultimate Warrior").
	anchor := min(hardFirst, langCut)
	if yearAt > titleStart {
		anchor = min(anchor, yearAt)
	}
	if ep != nil {
		anchor = min(anchor, epStart)
	}
	toByte := runeOffsets(s)
	anchorByte := len(s)
	if anchor < n {
		anchorByte = toByte[tokens[anchor].start]
	}
	regexEdition := ""
	for _, m := range findEditions(s) {
		first := tokenAtOrAfter(tokens, runeIndex(toByte, m.start))
		if first <= titleStart || first >= n {
			continue // at the very start: that is the title, not an edition
		}
		if m.end <= anchorByte && gapIsShort(s[m.end:anchorByte]) {
			regexEdition = m.text
			consider(first)
			break
		}
	}
	titleEnd := cut
	if regexEdition == "" {
		for _, m := range findEditions(s) {
			if first := tokenAtOrAfter(tokens, runeIndex(toByte, m.start)); first >= titleEnd && first > titleStart && first < n {
				regexEdition = m.text
				break
			}
		}
	}

	if yearAt >= 0 {
		r.Year, _ = strconv.Atoi(tokens[yearAt].text)
	}
	if titleEnd > titleStart {
		r.Title = cleanTitle(s, tokens[titleStart].start, tokens[titleEnd-1].end)
	}

	// Everything after the title is read for details.
	for i := titleEnd; i < n; i++ {
		r.apply(tokens, i)
	}
	if ep != nil {
		if epEnd < markerStart {
			t := cleanTitle(s, tokens[epEnd].start, tokens[markerStart-1].end)
			if !yearRe.MatchString(t) {
				ep.Title = t
			}
		}
		r.Episode = ep
	}
	if !tagEdition && regexEdition != "" {
		r.Edition = tidyEdition(regexEdition)
	}

	// Quality: Radarr's rules over the whole name decide the source. The
	// tokens above only find where the title ends ("web" alone could be
	// part of a title), so their source is replaced. The resolution found
	// by the rules wins over the token's; a token-only one ("4k") stays.
	tokenSource := r.Source
	q := guessQuality(name)
	r.Source = q.source
	if q.resolution != 0 {
		r.Resolution = q.resolution
	}
	if !q.known && tokenSource == SourceRemux {
		// "Title (2010) (REMUX)": no resolution, so Radarr shrugs. Reelkeeper
		// still wants to know it is a remux, not a re-encode.
		r.Source = SourceRemux
	}
	// An .iso file is a whole disc image, whatever its name says
	// ("Insidious (2011) [Bluray-1080p]-R-US.iso").
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), ".iso") {
		r.Source = SourceBRDisk
	}
	r.Revision = revisionOf(name)
	r.Proper = properRegex.MatchString(strings.ReplaceAll(name, "_", " "))

	// Group and languages are read from the name with the title replaced by
	// a stand-in: a dash or a language word in the title says nothing about
	// the release ("Spider-Man", "The German").
	titleStandIn := "A Movie"
	if titleEnd > titleStart && strings.Contains(string(runes[tokens[titleStart].start:tokens[titleEnd-1].end]), ".") {
		titleStandIn = "A.Movie"
	}
	var stand []standIn
	if titleEnd > titleStart {
		stand = append(stand, standIn{tokens[titleStart].start, tokens[titleEnd-1].end, titleStandIn})
	}
	if ep != nil {
		// Likewise the episode marker ("3x10-3x11" is not a group "3x11")
		// and the episode's title ("The Russian Mob" is not a language).
		stand = append(stand, standIn{tokens[epStart].start, tokens[epEnd-1].end, "S01E01"})
	}
	groupText := replaceSpans(runes, stand)
	if ep != nil && epEnd < markerStart {
		stand = append(stand, standIn{tokens[epEnd].start, tokens[markerStart-1].end, "An Episode"})
	}
	rest := replaceSpans(runes, stand)
	r.Group = animeGroup
	if r.Group == "" {
		r.Group = parseGroup(groupText)
	}
	if r.Group == "" {
		r.Group = specBlockGroup(s)
	}
	langText := rest
	if r.Group != "" {
		langText = strings.ReplaceAll(rest, r.Group, "RlsGrp")
	}
	r.Languages = parseLanguages(langText)
	return r
}

// standIn is a stretch of a name (in runes) to swap for other text.
type standIn struct {
	from, to int
	with     string
}

// replaceSpans swaps each span for its stand-in text. Spans must not
// overlap; they are applied in order of position.
func replaceSpans(runes []rune, spans []standIn) string {
	slices.SortFunc(spans, func(a, b standIn) int { return a.from - b.from })
	var b strings.Builder
	pos := 0
	for _, sp := range spans {
		if sp.from < pos || sp.to > len(runes) {
			continue
		}
		b.WriteString(string(runes[pos:sp.from]))
		b.WriteString(sp.with)
		pos = sp.to
	}
	b.WriteString(string(runes[pos:]))
	return b.String()
}

// isSoftMarker reports whether the marker at tokens[i] is an edition or
// revision word rather than a quality marker.
func isSoftMarker(tokens []token, i int) bool {
	return softMarkers[tokens[i].lower]
}

// yearBefore reports whether any token before i is a year.
func yearBefore(tokens []token, i int) bool {
	for j := 0; j < i; j++ {
		if yearRe.MatchString(tokens[j].text) {
			return true
		}
	}
	return false
}

// gapIsShort reports whether the text between an edition and what follows
// it is at most three characters of separators ("." or ") ").
func gapIsShort(gap string) bool {
	if len(gap) > 3 {
		return false
	}
	for _, c := range gap {
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			return false
		}
	}
	return true
}

// runeOffsets maps a rune position in s to its byte offset (with one extra
// entry for the end), because tokens count runes and regexp counts bytes.
func runeOffsets(s string) []int {
	offs := make([]int, 0, len(s)+1)
	for i := range s {
		offs = append(offs, i)
	}
	return append(offs, len(s))
}

// runeIndex is the rune position of byte offset b.
func runeIndex(offs []int, b int) int {
	i, _ := slices.BinarySearch(offs, b)
	return i
}

// tokenAtOrAfter returns the index of the first token starting at rune
// position pos or later (len(tokens) if none).
func tokenAtOrAfter(tokens []token, pos int) int {
	for i, t := range tokens {
		if t.start >= pos {
			return i
		}
	}
	return len(tokens)
}

// separators split tokens. Two kinds of dot are kept so the token stays
// whole: channel layouts ("5.1", "DDP5.1": one digit, dot, one digit) and
// codec names ("H.264", "H.265": a lone H). Everything else splits, so
// "Nuremberg.2025.2160p" becomes three tokens.
func isSeparator(rs []rune, i int) bool {
	c := rs[i]
	switch c {
	case ' ', '_', '[', ']', '(', ')', '{', '}', ',':
		return true
	case '.':
		if digitRun(rs, i-1, -1) == 1 && digitRun(rs, i+1, 1) == 1 {
			return false
		}
		if i > 0 && (rs[i-1] == 'h' || rs[i-1] == 'H') && digitRun(rs, i+1, 1) == 3 &&
			(i == 1 || !unicode.IsLetter(rs[i-2])) {
			return false
		}
		return true
	}
	return false
}

// digitRun counts consecutive digits starting at i and moving by step.
func digitRun(rs []rune, i, step int) int {
	n := 0
	for ; i >= 0 && i < len(rs) && unicode.IsDigit(rs[i]); i += step {
		n++
	}
	return n
}

func tokenize(s string) []token {
	rs := []rune(s)
	var out []token
	start := -1
	add := func(t string, start, end int) {
		out = append(out, token{text: t, lower: strings.ToLower(t), start: start, end: end})
	}
	flush := func(end int) {
		if start >= 0 {
			t := strings.Trim(string(rs[start:end]), "-")
			if t != "" {
				// A resolution stuck to a word: "Alive-1080p" is "Alive"
				// and "1080p". Combined markers such as "Bluray-1080p"
				// stay whole; apply reads those itself.
				if m := wordRes.FindStringSubmatch(t); m != nil && sourceFor(strings.ToLower(m[1]), "") == SourceUnknown {
					cut := start + len([]rune(m[1]))
					add(m[1], start, cut)
					add(m[2], cut+1, end)
				} else {
					add(t, start, end)
				}
			}
			start = -1
		}
	}
	for i := range rs {
		if isSeparator(rs, i) {
			flush(i)
			continue
		}
		if start < 0 {
			start = i
		}
	}
	flush(len(rs))
	return out
}

// cleanTitle returns the title between two rune offsets, with dots and
// underscores turned into spaces and stray punctuation trimmed.
func cleanTitle(s string, from, to int) string {
	rs := []rune(s)
	t := string(rs[from:to])
	// Dots are word separators in scene names ("The.Sea.Beast"), but keep
	// them in abbreviations like "Mr. Smith" or "S.W.A.T." when spaced.
	if !strings.Contains(t, " ") {
		t = strings.ReplaceAll(t, ".", " ")
	}
	t = strings.ReplaceAll(t, "_", " ")
	t = strings.Join(strings.Fields(t), " ")
	t = akaRegex.ReplaceAllString(t, "${1}AKA${2}")
	t = strings.Trim(t, " -([{")
	return t
}

// apply records what token i means, if it's a quality marker, and reports
// whether it was one.
func (r *Release) apply(tokens []token, i int) bool {
	return r.applyOpt(tokens, i, true)
}

// applyOpt is apply; attached says whether "marker-GROUP" tokens such as
// "x264-GRP" count as markers.
func (r *Release) applyOpt(tokens []token, i int, attached bool) bool {
	t := tokens[i].lower
	next := ""
	if i+1 < len(tokens) {
		next = tokens[i+1].lower
	}
	prev := ""
	if i > 0 {
		prev = tokens[i-1].lower
	}

	// Radarr-style combined tokens: "bluray-1080p", "webdl-2160p", "remux-2160p".
	if src, res, ok := strings.Cut(t, "-"); ok {
		if m := resRe.FindStringSubmatch(res); m != nil {
			if s := sourceFor(src, ""); s != SourceUnknown {
				r.setSource(s)
				r.Resolution, _ = strconv.Atoi(m[1])
				return true
			}
		}
	}

	if m := resRe.FindStringSubmatch(t); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n == 2160 || n == 1080 || n == 720 || n == 576 || n == 480 || n == 360 {
			r.Resolution = n
			return true
		}
	}
	switch t {
	case "4k", "uhd":
		if r.Resolution == 0 {
			r.Resolution = 2160
		}
		return true
	}

	if s := sourceFor(t, next); s != SourceUnknown {
		r.setSource(s)
		return true
	}

	// Service codes are ordinary words too ("Red", "It", "Max"), so they
	// only count right after a resolution or right before a web source:
	// "2160p.iT.WEB-DL", "1080p DSNP WEB-DL".
	if svc, ok := services[t]; ok && i > 0 {
		_, nextIsWeb := map[string]bool{"web-dl": true, "webdl": true, "web": true, "webrip": true}[next]
		if resRe.MatchString(prev) || nextIsWeb {
			r.Service = svc
			return true
		}
	}

	switch t {
	case "x264", "h264", "h.264", "avc":
		r.VideoCodec = "H.264"
		return true
	case "x265", "h265", "h.265", "hevc":
		r.VideoCodec = "H.265"
		return true
	case "av1":
		r.VideoCodec = "AV1"
		return true
	case "vc-1", "vc1":
		r.VideoCodec = "VC-1"
		return true
	case "mpeg2", "mpeg-2":
		r.VideoCodec = "MPEG-2"
		return true
	case "xvid", "divx":
		r.VideoCodec = "XviD"
		return true
	case "10bit", "10-bit", "hi10p":
		r.BitDepth = 10
		return true
	case "8bit", "8-bit":
		return true
	}

	switch t {
	case "dv", "dovi", "dolby.vision":
		r.addHDR("DV")
		return true
	case "hdr10+", "hdr10plus":
		r.addHDR("HDR10+")
		return true
	case "hdr10":
		r.addHDR("HDR10")
		return true
	case "hdr":
		r.addHDR("HDR")
		return true
	case "hlg":
		r.addHDR("HLG")
		return true
	case "dolby":
		if next == "vision" {
			r.addHDR("DV")
			return true
		}
	case "vision":
		if prev == "dolby" {
			return true
		}
	}

	if r.applyAudio(t, next) {
		return true
	}

	switch t {
	case "proper", "repack", "rerip", "real":
		return true // a marker (it ends the title); the revision comes from revisionOf
	case "remastered", "imax", "extended", "unrated", "uncut", "theatrical", "criterion", "uncensored", "open.matte", "redux":
		r.setEdition(editionName[t])
		return true
	case "director's", "directors":
		if next == "cut" {
			r.setEdition("Director's Cut")
			return true
		}
	case "cut":
		if prev == "director's" || prev == "directors" || prev == "final" {
			return true
		}
	case "final":
		if next == "cut" {
			r.setEdition("Final Cut")
			return true
		}
	}

	// Last resort: a marker with the group stuck on: "x264-RARBG",
	// "H.265-BYNDR", "AAC-LAMA". Tried last so whole tokens such as
	// "DTS-HD" win. The group itself is picked up by findGroup.
	if j := strings.LastIndexByte(t, '-'); attached && j > 0 && j < len(t)-1 {
		left, right := tokens[i].text[:j], t[j+1:]
		if !strings.Contains(left, "-") && !isMarker(right) && isMarker(strings.ToLower(left)) {
			return r.apply([]token{{text: left, lower: strings.ToLower(left)}}, 0)
		}
	}
	return false
}

var editionName = map[string]string{
	"remastered": "Remastered", "imax": "IMAX", "extended": "Extended", "unrated": "Unrated",
	"uncut": "Uncut", "theatrical": "Theatrical", "criterion": "Criterion", "uncensored": "Uncensored",
	"open.matte": "Open Matte", "redux": "Redux",
}

func (r *Release) setEdition(e string) {
	if r.Edition == "" {
		r.Edition = e
	} else if !strings.Contains(r.Edition, e) {
		r.Edition += " " + e
	}
}

func (r *Release) addHDR(h string) {
	if !slices.Contains(r.HDR, h) {
		r.HDR = append(r.HDR, h)
	}
}

func (r *Release) addAudio(a string) {
	if !slices.Contains(r.Audio, a) {
		r.Audio = append(r.Audio, a)
	}
}

// setSource keeps the best source when a name mentions several (a remux
// of a Blu-ray says both).
func (r *Release) setSource(s Source) {
	if r.Source == SourceUnknown || rank(s) < rank(r.Source) {
		r.Source = s
	}
}

func rank(s Source) int {
	order := []Source{SourceRemux, SourceBRDisk, SourceBluRay, SourceWebDL, SourceWebRip, SourceRawHD,
		SourceHDTV, SourceDVDR, SourceDVD, SourceScreener, SourceTeleCine, SourceTeleSync, SourceCam,
		SourceRegional, SourceWorkprint}
	if i := slices.Index(order, s); i >= 0 {
		return i
	}
	return len(order)
}

func sourceFor(t, next string) Source {
	switch t {
	case "remux", "bdremux":
		return SourceRemux
	case "bluray", "blu-ray", "bdrip", "brrip", "bdmv", "bd", "uhdbluray":
		return SourceBluRay
	case "web-dl", "webdl", "web":
		if t == "web" && next == "dl" {
			return SourceWebDL
		}
		return SourceWebDL
	case "webrip", "web-rip":
		return SourceWebRip
	case "hdtv", "pdtv", "sdtv":
		return SourceHDTV
	case "dvdrip", "dvd", "dvd9", "dvd5", "dvdr":
		return SourceDVD
	case "dvdscr", "screener", "scr", "bdscr":
		return SourceScreener
	case "telecine", "tc", "hdtc":
		return SourceTeleCine
	case "telesync", "hdts", "pdvd", "ts":
		return SourceTeleSync
	case "cam", "hdcam", "camrip":
		return SourceCam
	}
	return SourceUnknown
}

var services = map[string]string{
	"amzn": "AMZN", "nf": "NF", "atvp": "ATVP", "dsnp": "DSNP", "hmax": "HMAX", "max": "MAX",
	"hulu": "HULU", "pcok": "PCOK", "pmtp": "PMTP", "it": "iT", "itunes": "iT", "stan": "STAN",
	"cr": "CR", "red": "RED", "dcu": "DCU", "crav": "CRAV", "hbo": "HBO", "ma": "MA",
}

// applyAudio recognises audio formats and channel layouts.
func (r *Release) applyAudio(t, next string) bool {
	// "DDP5.1", "DD+5.1", "DD5.1", "AAC2.0", "TrueHD7.1", "DTS-HD.MA.5.1"
	codec, ch := splitChannels(t)
	if ch != "" {
		r.Channels = ch
		if codec == "" {
			return true
		}
		t = codec
	}
	switch t {
	case "truehd":
		r.addAudio("TrueHD")
	case "atmos":
		r.addAudio("Atmos")
	case "dts-hd", "dtshd", "dts-hdma", "dts-hd.ma", "dtshd-ma", "dts-ma":
		r.addAudio("DTS-HD MA")
	case "dts-x", "dtsx":
		r.addAudio("DTS-X")
	case "dts":
		if next == "hd" || next == "ma" {
			r.addAudio("DTS-HD MA")
		} else {
			r.addAudio("DTS")
		}
	case "ma", "hd":
		return slices.Contains(r.Audio, "DTS-HD MA") && t == "ma"
	case "ddp", "dd+", "eac3", "e-ac-3", "ddplus":
		r.addAudio("DD+")
	case "dd", "ac3", "ac-3", "dolby.digital":
		r.addAudio("DD")
	case "aac", "aac-lc", "he-aac":
		r.addAudio("AAC")
	case "flac":
		r.addAudio("FLAC")
	case "opus":
		r.addAudio("Opus")
	case "lpcm", "pcm":
		r.addAudio("LPCM")
	case "mp3":
		r.addAudio("MP3")
	case "6ch":
		r.Channels = "5.1"
	case "8ch":
		r.Channels = "7.1"
	case "2ch":
		r.Channels = "2.0"
	default:
		return ch != ""
	}
	return true
}

var channelsRe = regexp.MustCompile(`^(.*?)([1-9]\.[0-2])$`)

// splitChannels cuts "ddp5.1" into ("ddp", "5.1"), and "5.1" into ("", "5.1").
func splitChannels(t string) (codec, channels string) {
	m := channelsRe.FindStringSubmatch(t)
	if m == nil {
		return t, ""
	}
	switch m[2] {
	case "1.0", "2.0", "2.1", "5.1", "6.1", "7.1":
		return strings.TrimRight(m[1], "-."), m[2]
	}
	return t, ""
}

// specBlockGroup finds a group written as the last word of a bracketed
// spec: "(… x265 HEVC 10bit AAC 5.1 Tigole)". Radarr only knows such groups
// from a fixed list; Reelkeeper also accepts any unknown last word, as long as
// everything before it in the block is quality markers and the word itself
// is neither a marker, a year nor a language ("ENG-ITA-FRA").
func specBlockGroup(s string) string {
	i := strings.LastIndex(s, "(")
	if i < 0 {
		return ""
	}
	inner := s[i+1:]
	j := strings.IndexByte(inner, ')')
	if j < 0 {
		return ""
	}
	words := strings.Fields(inner[:j])
	if len(words) < 3 {
		return ""
	}
	last := words[len(words)-1]
	if isMarker(strings.ToLower(last)) || yearRe.MatchString(last) || !isMarkerList(words[:len(words)-1]) ||
		len(parseLanguages(last)) > 0 || strings.Contains(last, "-") {
		return ""
	}
	return last
}

// isMarker reports whether a single word is a quality marker.
func isMarker(w string) bool {
	var r Release
	return r.apply([]token{{text: w, lower: w}}, 0)
}

// isMarkerList reports whether most words are quality markers, which tells
// a "(1080p BluRay x265 Tigole)" spec apart from a title in parentheses.
func isMarkerList(words []string) bool {
	n := 0
	for _, w := range words {
		if isMarker(strings.ToLower(w)) {
			n++
		}
	}
	return n*2 >= len(words) && n >= 2
}
