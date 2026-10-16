package parser

// Quality detection. This follows Radarr's QualityParser closely (Radarr is
// GPL-3.0 like Reelkeeper), because Radarr's naming of qualities is the common
// language of Seerr, TRaSH Guides and quality profiles. The idea: look at
// the whole name for a source (Blu-ray, WEB-DL, HDTV...) and a resolution,
// then combine them into one quality such as "Bluray-1080p".
//
// Radarr's patterns use look-ahead and look-behind, which Go's regexp does
// not have. Each place where one was needed is marked and done in code.

import (
	"regexp"
	"strconv"
	"strings"
)

// qualityGuess is what the quality rules found in a name.
type qualityGuess struct {
	source     Source
	resolution int  // vertical resolution the name states (or that Radarr assumes), else 0
	known      bool // false when the name says nothing about quality
}

// join builds one pattern from alternatives, because Go has no
// "ignore whitespace" mode to keep long patterns readable.
func join(parts ...string) string { return strings.Join(parts, "|") }

var (
	// Follows Radarr's QualityParser.SourceRegex. The trailing
	// "(?:\b|$|[ .])" is Radarr's too.
	sourceRegex = regexp.MustCompile(`(?i)\b(?:` + join(
		// Radarr writes BD(?!$): a bare "BD" at the very end of the name
		// is not a source. That is checked in code after matching.
		`(?P<bluray>M?Blu[-_. ]?Ray|HD[-_. ]?DVD|BD|UHD2?BD|BDISO|BDMux|BD25|BD50|BR[-_. ]?DISK)`,
		`(?P<webdl>`+join(
			`WEB[-_. ]?DL(?:mux)?`, `AmazonHD`, `AmazonSD`, `iTunesHD`, `MaxdomeHD`, `NetflixU?HD`, `WebHD`, `HBOMaxHD`, `DisneyHD`,
			`[. ]WEB[. ](?:[xh][ .]?26[45]|AVC|HEVC|DDP?5[. ]1)`,
			`[. ](?-i:WEB)$`,
			`(?:\d{3,4}0p)[-. ](?:Hybrid[-_. ]?)?WEB[-. ]`,
			`[-. ]WEB[-. ]\d{3,4}0p`,
			`\b\s/\sWEB\s/\s\b`,
		)+`)`,
		`(?P<webrip>WebRip|Web-Rip|WEBMux)`,
		`(?P<hdtv>HDTV)`,
		`(?P<bdrip>BDRip|BDLight|HD[-_. ]?DVDRip|UHDBDRip)`,
		`(?P<brrip>BRRip)`,
		`(?P<dvdr>\d?x?M?DVD-?[R59])`,
		`(?P<dvd>DVD|DVDRip|xvidvd)`, // Radarr: DVD(?!-R); "DVD-R" is already taken by dvdr above
		`(?P<dsr>WS[-_. ]DSR|DSR)`,
		`(?P<regional>R[0-9]{1}|REGIONAL)`,
		`(?P<scr>SCR|SCREENER|DVDSCR|DVDSCREENER)`,
		`(?P<ts>TS[-_. ]|TELESYNCH?|HD-TS|HDTS|PDVD|TSRip|HDTSRip)`,
		`(?P<tc>TC|TELECINE|HD-TC|HDTC)`,
		`(?P<cam>CAMRIP|(?:NEW)?CAM|HD-?CAM(?:Rip)?|HQCAM)`,
		`(?P<wp>WORKPRINT|WP)`,
		`(?P<pdtv>PDTV)`,
		`(?P<sdtv>SDTV)`,
		`(?P<tvrip>TVRip)`,
	) + `)(?:\b|$|[ .])`)

	// "AMZN.WEB.", "NF.WEB.": a streaming service tag before WEB means
	// WEB-DL, unless "Rip" follows. Radarr does that with a look-ahead
	// inside sourceRegex; here it is a second pattern checked in code.
	serviceWebRegex = regexp.MustCompile(`(?i)\b(?:AMZN|NF|DP)[. -]WEB[. -]`)

	rawHDRegex = regexp.MustCompile(`(?i)\b(?:RawHD|Raw[-_. ]HD)\b`)
	mpeg2Regex = regexp.MustCompile(`(?i)\bMPEG[-_. ]?2\b`)

	resolutionRegex = regexp.MustCompile(`(?i)\b(?:` + join(
		`(?P<r360>360p)`,
		`(?P<r480>480p|480i|640x480|848x480)`,
		`(?P<r540>540p)`,
		`(?P<r576>576p)`,
		`(?P<r720>720p|1280x720|960p)`,
		`(?P<r1080>1080p|1920x1080|1440p|FHD|1080i|4kto1080p)`,
		`(?P<r2160>2160p|3840x2160|4k[-_. ](?:UHD|HEVC|BD|H\.?265)|(?:UHD|HEVC|BD|H\.?265)[-_. ]4k)`,
	) + `)\b`)
	// No resolution in the name: assume 4K if it says UHD.
	altResolutionRegex = regexp.MustCompile(`(?i)\bUHD\b|\[4K\]`)

	codecRegex    = regexp.MustCompile(`(?i)\b(?:(?P<x264>x264)|(?P<h264>h264)|(?P<xvidhd>XvidHD)|(?P<xvid>X-?vid)|(?P<divx>divx))\b`)
	otherSourceHD = regexp.MustCompile(`(?i)HD[-_. ]TV`)
	otherSourceSD = regexp.MustCompile(`(?i)SD[-_. ]TV`)
	animeBluray   = regexp.MustCompile(`(?i)bd(?:720|1080|2160)|[-_. (\[]bd[-_. )\]]`)
	animeWebDL    = regexp.MustCompile(`(?i)\[WEB\]|[\[(]WEB[ .]`)
	highDefPdtv   = regexp.MustCompile(`(?i)hr[-_. ]ws`)

	remuxRegex     = regexp.MustCompile(`(?i)(?:[_. \[]|\d{4}p-|\bHybrid-)(?:(?:BD|UHD)[-_. ]?)?Remux\b|(?:(?:BD|UHD)[-_. ]?)?Remux[_. ]\d{4}p`)
	germanRemuxRe  = regexp.MustCompile(`(?i)\d{4}.*German.*\b(?:AVC|HEVC|VC[_. -]?1|MVC|MPEG[_. -]?2)`)
	germanRemuxBlu = regexp.MustCompile(`(?i)Blu-?ray`)

	properRegex  = regexp.MustCompile(`(?i)\bproper\b`)
	repackRegex  = regexp.MustCompile(`(?i)\b(?:repack\d?|rerip\d?)\b`)
	realRegex    = regexp.MustCompile(`\bREAL\b`)
	versionRegex = regexp.MustCompile(`(?i)\d[-._ ]?v(\d)[-._ ]|\[v(\d)\]|repack(\d)|rerip(\d)`)

	// Pieces of Radarr's BRDISKRegex (see isBRDisk).
	brdiskNot       = regexp.MustCompile(`(?i)\b(?:` + join(`720p`, `MKV`, `XviD`, `WMV`, `d3g`, `(?:BD)?REMUX`, `[xh][-_. ]?26[45]`, `BDRip`, `German.*[DM]L`) + `)\b`)
	brdiskDVD       = regexp.MustCompile(`(?i)\b(HD[._ -]?)?DVD\b`)
	brdiskGerman    = regexp.MustCompile(`(?i)\d{4}\b.*German.*\b(?:AVC|HEVC|VC[-_. ]?1|MVC|MPEG[-_. ]?2)\b`)
	brdiskFormat    = regexp.MustCompile(`(?i)\b(?:Blu[-_. ]?ray|BD|HD[-_. ]?DVD)\b`)
	brdiskVideo     = regexp.MustCompile(`(?i)\b(?:AVC|HEVC|VC[-_. ]?1|MVC|MPEG[-_. ]?2|BDMV|ISO)\b`)
	brdiskComplete  = regexp.MustCompile(`(?i)\b(?:COMPLETE|Dis[ck]\b)`)
	brdiskDiscWords = regexp.MustCompile(`(?i)\b(?:3D[-_. ]?BD|BR[-_. ]?DISK|Full[-_. ]?Blu[-_. ]?ray)\b`)
	brdiskSizes     = regexp.MustCompile(`(?i)(?:BD|UHD)[-_. ]?(?:25|50|66|100|ISO)`)
	brdiskBluHDDVD  = regexp.MustCompile(`(?i)Blu[-_. ]?ray|HD[-_. ]?DVD`)
	startsWithWord  = regexp.MustCompile(`^\w`)
)

// isBRDisk follows Radarr's BRDISKRegex: a full Blu-ray disc image or
// folder (BDMV, ISO, BD25/50/66/100, "Complete Bluray"), which is not a
// re-encode. The original is one long look-ahead pattern; this is the same
// logic as separate checks. Anything that shows the video was re-encoded
// or remuxed (x264, 720p, REMUX, DVD, MKV...) rules it out.
func isBRDisk(name string) bool {
	if brdiskNot.MatchString(name) || brdiskGerman.MatchString(name) {
		return false
	}
	// A plain "DVD" counts against it; "HD-DVD" does not.
	for _, m := range brdiskDVD.FindAllStringSubmatch(name, -1) {
		if m[1] == "" {
			return false
		}
	}
	lower := strings.ToLower(name)
	if startsWithWord.MatchString(name) && strings.Contains(lower, "1080p") && strings.Contains(lower, "hevc") {
		return false
	}
	// A Blu-ray name that also names a disc video codec, BDMV or ISO.
	if brdiskFormat.MatchString(name) && brdiskVideo.MatchString(name) {
		return true
	}
	switch {
	case brdiskComplete.MatchString(name) && brdiskBluHDDVD.MatchString(name):
		return true
	case brdiskDiscWords.MatchString(name):
		return true
	case startsWithWord.MatchString(name) && brdiskSizes.MatchString(name):
		return true
	}
	return false
}

// parseResolution follows Radarr's QualityParser.ParseResolution. 1440p and
// FHD count as 1080; "UHD" with no resolution counts as 2160.
func parseResolution(name string) int {
	if m := resolutionRegex.FindStringSubmatch(name); m != nil {
		for i, n := range resolutionRegex.SubexpNames() {
			if m[i] != "" && n != "" {
				v, _ := strconv.Atoi(strings.TrimPrefix(n, "r"))
				return v
			}
		}
	}
	if altResolutionRegex.MatchString(name) {
		return 2160
	}
	return 0
}

// lastSource finds the last source marker in the name, as Radarr does (the
// last one wins: "Movie.DVDRip.2010.BluRay" is a Blu-ray). It returns the
// name of the pattern group that matched, or "".
func lastSource(name string) string {
	group := ""
	last := -1
	names := sourceRegex.SubexpNames()
	for _, m := range sourceRegex.FindAllStringSubmatchIndex(name, -1) {
		g := ""
		for i, n := range names {
			if n != "" && m[2*i] >= 0 {
				g = n
				break
			}
		}
		// BD(?!$): a bare BD at the very end is not a source.
		if g == "bluray" && m[1] == len(name) && strings.EqualFold(strings.TrimRight(name[m[0]:m[1]], " ."), "BD") {
			continue
		}
		if m[0] > last {
			last, group = m[0], g
		}
	}
	// (?:AMZN|NF|DP)[. -]WEB[. -](?!Rip)
	for _, m := range serviceWebRegex.FindAllStringIndex(name, -1) {
		if len(name) >= m[1]+3 && strings.EqualFold(name[m[1]:m[1]+3], "rip") {
			continue
		}
		if m[0] >= last {
			last, group = m[0], "webdl"
		}
	}
	return group
}

// guessQuality works out source and resolution from the whole name (with
// its file extension, if any). It follows QualityParser.ParseQualityName,
// except that it never guesses from a file extension or an "x264" alone:
// a quality guessed from nothing would be worse than none.
func guessQuality(name string) qualityGuess {
	norm := strings.TrimSpace(strings.ReplaceAll(name, "_", " "))
	lower := strings.ToLower(norm)
	srcGroup := lastSource(norm)
	res := parseResolution(norm)
	codec := codecRegex.FindStringSubmatch(norm)
	hasCodec := func(n string) bool {
		for i, sn := range codecRegex.SubexpNames() {
			if sn == n && codec != nil && codec[i] != "" {
				return true
			}
		}
		return false
	}
	remux := remuxRegex.MatchString(norm) || (germanRemuxRe.MatchString(norm) && germanRemuxBlu.MatchString(norm))

	g := func(src Source, resolution int) qualityGuess {
		return qualityGuess{source: src, resolution: resolution, known: true}
	}
	lowRes := res == 360 || res == 480 || res == 540 || res == 576

	if rawHDRegex.MatchString(norm) && !isBRDisk(norm) {
		return g(SourceRawHD, res)
	}

	switch srcGroup {
	case "bluray":
		switch {
		case isBRDisk(norm):
			return g(SourceBRDisk, res)
		case hasCodec("xvid") || hasCodec("divx"):
			return g(SourceBluRay, 480)
		case res == 2160 || res == 1080:
			if remux {
				return g(SourceRemux, res)
			}
			return g(SourceBluRay, res)
		case res == 720 || res == 576:
			return g(SourceBluRay, res)
		case lowRes:
			return g(SourceBluRay, res)
		case remux: // a remux that names no resolution is taken as 1080p
			return g(SourceRemux, 1080)
		}
		return g(SourceBluRay, 720)
	case "webdl":
		switch {
		case res == 2160 || res == 1080 || res == 720:
			return g(SourceWebDL, res)
		case strings.Contains(name, "[WEBDL]"):
			return g(SourceWebDL, 720)
		}
		return g(SourceWebDL, res)
	case "webrip":
		return g(SourceWebRip, res)
	case "scr":
		return g(SourceScreener, res)
	case "cam":
		return g(SourceCam, res)
	case "ts":
		return g(SourceTeleSync, res)
	case "tc":
		return g(SourceTeleCine, res)
	case "wp":
		return g(SourceWorkprint, res)
	case "regional":
		return g(SourceRegional, res)
	case "hdtv":
		switch {
		case mpeg2Regex.MatchString(norm): // HDTV captured as MPEG-2 is a raw broadcast
			return g(SourceRawHD, res)
		case res == 2160 || res == 1080 || res == 720:
			return g(SourceHDTV, res)
		case strings.Contains(name, "[HDTV]"):
			return g(SourceHDTV, 720)
		}
		return g(SourceHDTV, res)
	case "bdrip", "brrip":
		if res == 0 {
			return g(SourceBluRay, 480) // Radarr: a BDRip with no resolution is SD
		}
		return g(SourceBluRay, res)
	case "dvdr":
		return g(SourceDVDR, res)
	case "dvd":
		return g(SourceDVD, res)
	case "pdtv", "sdtv", "dsr", "tvrip":
		switch {
		case res == 1080 || strings.Contains(lower, "1080p"):
			return g(SourceHDTV, 1080)
		case res == 720 || strings.Contains(lower, "720p"):
			return g(SourceHDTV, 720)
		case highDefPdtv.MatchString(norm):
			return g(SourceHDTV, 720)
		}
		return g(SourceHDTV, res)
	}

	// No source word at all. A remux says Blu-ray by itself.
	if remux {
		switch res {
		case 480:
			return g(SourceBluRay, 480)
		case 720:
			return g(SourceBluRay, 720)
		case 2160, 1080:
			return g(SourceRemux, res)
		}
	}

	if animeBluray.MatchString(norm) {
		switch {
		case lowRes || strings.Contains(lower, "480p"):
			return g(SourceDVD, res)
		case res == 1080 || strings.Contains(lower, "1080p"):
			return remuxOr(remux, 1080)
		case res == 2160 || strings.Contains(lower, "2160p"):
			return remuxOr(remux, 2160)
		case remux && res != 720:
			return g(SourceRemux, 1080)
		}
		return g(SourceBluRay, 720)
	}

	if animeWebDL.MatchString(norm) {
		switch {
		case lowRes || strings.Contains(lower, "480p"):
			return g(SourceWebDL, 480)
		case res == 1080 || strings.Contains(lower, "1080p"):
			return g(SourceWebDL, 1080)
		case res == 2160 || strings.Contains(lower, "2160p"):
			return g(SourceWebDL, 2160)
		}
		return g(SourceWebDL, 720)
	}

	// A resolution and nothing else: Radarr calls that HDTV (SDTV if low).
	if res != 0 {
		return g(SourceUnknown, res)
	}

	switch {
	case strings.Contains(lower, "848x480"):
		if strings.Contains(norm, "dvd") {
			return g(SourceDVD, 480)
		}
		if strings.Contains(lower, "bluray") {
			return g(SourceBluRay, 480)
		}
		return g(SourceUnknown, 480)
	case strings.Contains(lower, "bluray720p"):
		return g(SourceBluRay, 720)
	case strings.Contains(lower, "bluray1080p"):
		return g(SourceBluRay, 1080)
	case strings.Contains(lower, "bluray2160p"):
		return g(SourceBluRay, 2160)
	}
	if otherSourceSD.MatchString(norm) {
		return g(SourceHDTV, 480)
	}
	if otherSourceHD.MatchString(norm) {
		return g(SourceHDTV, 720)
	}
	return qualityGuess{}
}

func remuxOr(remux bool, res int) qualityGuess {
	if remux {
		return qualityGuess{source: SourceRemux, resolution: res, known: true}
	}
	return qualityGuess{source: SourceBluRay, resolution: res, known: true}
}

// revisionOf follows QualityParser.ParseQualityModifiers: 0 when the name
// says nothing, 2 for PROPER or REPACK, 3 for REPACK2, and so on. "v2"
// style version tags count too.
func revisionOf(name string) int {
	norm := strings.ReplaceAll(name, "_", " ")
	version := 0
	if m := versionRegex.FindStringSubmatch(norm); m != nil {
		for _, s := range m[1:] {
			if s != "" {
				version, _ = strconv.Atoi(s)
				break
			}
		}
	}
	rev := version
	if properRegex.MatchString(norm) || repackRegex.MatchString(norm) {
		if version > 0 {
			rev = version + 1
		} else {
			rev = 2
		}
	}
	// "REAL" in capitals says an earlier release was mislabelled; Radarr
	// counts it apart from the version, Reelkeeper as one more revision.
	if realRegex.MatchString(norm) {
		rev = max(rev, 1) + 1
	}
	return rev
}

// Quality returns the quality the way Radarr names it: "Bluray-1080p",
// "WEBDL-2160p", "Remux-2160p", "SDTV", "BR-DISK", or "Unknown" when the
// name says nothing about it. Radarr's quality list is the shared language
// of Seerr and TRaSH Guides, so Reelkeeper uses its names exactly.
//
// It works from Source and Resolution alone, so a Release built by hand
// (as the scanner does when the file's headers give a better resolution)
// gets the same answer as a parsed name.
func (r Release) Quality() string {
	height := bucketResolution(r.Resolution)
	// withRes picks "<prefix>-<height>p" when Radarr has that quality for
	// the source, and def otherwise.
	withRes := func(prefix string, allowed []int, def string) string {
		for _, a := range allowed {
			if a == height {
				return prefix + "-" + strconv.Itoa(a) + "p"
			}
		}
		return def
	}
	switch r.Source {
	case SourceRemux:
		switch height {
		case 2160:
			return "Remux-2160p"
		case 720:
			return "Bluray-720p" // Radarr has no 720p remux
		case 480, 576:
			return "Bluray-480p"
		}
		return "Remux-1080p"
	case SourceBluRay:
		def := "Bluray-480p"
		if height == 0 {
			def = "Bluray-720p" // Radarr's guess when a Blu-ray names no resolution
		}
		return withRes("Bluray", []int{2160, 1080, 720, 576}, def)
	case SourceWebDL:
		return withRes("WEBDL", []int{2160, 1080, 720}, "WEBDL-480p")
	case SourceWebRip:
		return withRes("WEBRip", []int{2160, 1080, 720}, "WEBRip-480p")
	case SourceHDTV:
		return withRes("HDTV", []int{2160, 1080, 720}, "SDTV")
	case SourceDVD:
		return "DVD"
	case SourceDVDR:
		return "DVD-R"
	case SourceScreener:
		return "DVDSCR"
	case SourceTeleCine:
		return "TELECINE"
	case SourceTeleSync:
		return "TELESYNC"
	case SourceCam:
		return "CAM"
	case SourceWorkprint:
		return "WORKPRINT"
	case SourceRegional:
		return "REGIONAL"
	case SourceBRDisk:
		return "BR-DISK"
	case SourceRawHD:
		return "Raw-HD"
	}
	// No source: a resolution alone is read as a TV capture.
	if r.Resolution == 0 {
		return "Unknown"
	}
	return withRes("HDTV", []int{2160, 1080, 720}, "SDTV")
}

// bucketResolution maps the odd heights names can state (1440, 540, 360...)
// onto the ones Radarr has qualities for: 2160, 1080, 720, 576 or 480
// (0 stays 0).
func bucketResolution(h int) int {
	switch {
	case h >= 2160:
		return 2160
	case h >= 1080:
		return 1080
	case h >= 720:
		return 720
	case h == 576:
		return 576
	case h > 0:
		return 480
	}
	return 0
}
