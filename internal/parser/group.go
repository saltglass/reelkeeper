package parser

// Release group detection. This follows Radarr's ReleaseGroupParser
// (GPL-3.0 like Reelkeeper). The group is usually the word after the last dash
// ("x264-SPARKS"), with a long list of ways that goes wrong: a dash inside a
// quality word ("WEB-DL", "DTS-HD"), a site tag stuck after the group,
// groups written in brackets, and groups that break the pattern.
//
// Radarr's main pattern leans on look-ahead and look-behind, which Go's
// regexp lacks, so scanForGroup does that part by hand.

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	// Groups that do not follow "-GROUP", matched anywhere as whole words.
	// The last match wins. Radarr's ExceptionReleaseGroupRegexExact.
	exactGroupRegex = regexp.MustCompile(`(?i)\b(KRaLiMaRKo|MeM\.GP|E\.N\.D|D-Z0N3|Koten_Gars|BluDragon|ZØNEHD|HQMUX|VARYG|YIFY|YTS(?:.(?:MX|LT|AG))?|TMd|Eml HDTeam|LMain|DarQ|BEN THE MEN|TAoE|QxR|126811)\b`)

	// Groups whose releases end "... Group)" or "... Group]". Radarr's
	// ExceptionReleaseGroupRegex; it checks the character before with a
	// look-behind and the one after with a look-ahead, so here both are part
	// of the match and the group is taken from the capture.
	bracketGroupRegex = regexp.MustCompile(`(?i)[._ \[](Silence|afm72|Panda|Ghost|MONOLITH|Tigole|Joy|ImE|UTR|t3nzin|Anime Time|Project Angel|Hakata Ramen|HONE|GiLG|Vyndros|SEV|Garshasp|Kappa|Natty|RCVR|SAMPA|YOGI|r00t|EDGE2020|RZeroX|FreetheFish|Anna|Bandi|Qman|theincognito|HDO|DusIctv|DHD|CtrlHD|-ZR-|ADC|XZVN|RH|Kametsu|Celdra)[\])]`)

	// Junk some indexers add after the real group: "-NTb-Rakuv".
	cleanGroupRegex = regexp.MustCompile(`(?i)(-(RP|1|NZBGeek|Obfuscated|Obfuscation|Scrambled|sample|Pre|postbot|xpost|Rakuv[a-z0-9]*|WhiteRev|BUYMORE|AsRequested|AlternativeToRequested|GEROV|Z0iDS3N|Chamele0n|4P|4Planet|AlteZachen|RePACKPOST))+$`)

	// Anime style "[Group] Title".
	animeGroupRegex = regexp.MustCompile(`^\[(\S(?:.*?\S)?)\]`)

	// A group written last in brackets: "Title.1080p [rl]".
	bracketTailRegex = regexp.MustCompile(`(?i)[-._ ]\[([a-z0-9]+)\]$`)

	// Tracker tags that are never the group: "...-GRP [rarbg]".
	torrentSuffixRegex = regexp.MustCompile(`(?i)\[(?:ettv|rartv|rarbg|cttv|publichd)\]$`)

	invalidGroupRegex = regexp.MustCompile(`(?i)^([se]\d+|[0-9a-f]{8})$`)
	allDigitsRegex    = regexp.MustCompile(`^\d+$`)

	// Things that look like a group after a dash but are not. Radarr checks
	// these with a look-behind right after the candidate group.
	notGroupSuffixRegex = regexp.MustCompile(`(?i)(?:WEB-(?:DL|Rip)|Blu-Ray|480p|576p|720p|1080p|2160p|DTS-HD|DTS-X|DTS-MA|DTS-ES|-ES|-EN|-CAT|-ENG|-JAP|-GER|-FRA|-FRE|-ITA|-HDRip|\d{1,2}-bit|[ ._]\d{4}-\d{2}|-\d{2}|tmdb(?:id)?-\d+|tt\d{7,8})$`)
	resolutionAfter     = regexp.MustCompile(`(?i)480p|576p|720p|1080p|2160p`)
)

// isAlnum reports whether c is a letter or digit that Radarr's [a-z0-9]
// would match (ASCII only, case-insensitive).
func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// alnumRun returns the end of the run of letters and digits starting at i.
func alnumRun(s string, i int) int {
	for i < len(s) && isAlnum(s[i]) {
		i++
	}
	return i
}

// scanForGroup finds "-GROUP" candidates the way Radarr's main pattern does
// and returns the last one, or "". A candidate is a dash followed by one or
// two dash-joined words ("MeGusta", "Blu-bits"), where nothing later in the
// name is a resolution, the word is not one of the lookalikes in
// notGroupSuffixRegex, and the word ends at a boundary.
func scanForGroup(s string) string {
	// Where the last resolution and the last quality marker start, found
	// once so a long name with many dashes stays fast.
	lastRes, lastMarker := -1, -1
	if locs := resolutionAfter.FindAllStringIndex(s, -1); len(locs) > 0 {
		lastRes = locs[len(locs)-1][0]
	}
	offs := runeOffsets(s)
	for _, t := range tokenize(s) {
		if isMarker(t.lower) {
			lastMarker = offs[t.start]
		}
	}
	last := ""
	i := 0
	for i < len(s) {
		if s[i] != '-' {
			i++
			continue
		}
		// The two ways the pattern can read the word after the dash: with a
		// second dash-joined part (tried first, as Radarr's greedy "?"
		// does), or without it.
		first := alnumRun(s, i+1)
		if first == i+1 {
			i++
			continue
		}
		type option struct{ end, part2 int } // part2 is the length of "-xxx", or 0
		var options []option
		if first < len(s) && s[first] == '-' {
			if second := alnumRun(s, first+1); second > first+1 {
				options = append(options, option{second, second - first})
			}
		}
		options = append(options, option{first, 0})

		found := false
		for _, o := range options {
			if o.end < len(s) {
				// Nothing may be a resolution after the word (at least one
				// character later): "-Name.1080p" is not a group.
				if lastRes >= o.end+1 {
					continue
				}
			}
			// The word must not end like one of the lookalikes, with or
			// without its own second part.
			// Only the last few characters matter, so look at a window
			// (scanning the whole prefix at every dash would be quadratic).
			before := s[max(0, o.end-48):o.end]
			bad := notGroupSuffixRegex.MatchString(before)
			if !bad && o.part2 > 0 && o.part2 < len(before) {
				bad = notGroupSuffixRegex.MatchString(before[:len(before)-o.part2])
			}
			if bad {
				continue
			}
			// Reelkeeper addition: a real group is the last thing in the name. If
			// a quality marker follows ("ENG-ITA-Comm - x264 bluray"), the
			// word is something else.
			if lastMarker >= o.end {
				continue
			}
			// The word must end at a boundary.
			if o.end < len(s) {
				c := s[o.end]
				if isAlnum(c) || c >= 0x80 && unicode.IsLetter([]rune(s[o.end:])[0]) {
					continue
				}
			}
			last = s[i+1 : o.end]
			i = o.end
			found = true
			break
		}
		if !found {
			i++
		}
	}
	return last
}

// parseGroup follows ReleaseGroupParser.ParseReleaseGroup. s is the release
// name without its extension, ideally with the title replaced by a stand-in
// so a dash in the title ("Spider-Man") is not read as a group.
func parseGroup(s string) string {
	s = strings.TrimSpace(s)

	if m := animeGroupRegex.FindStringSubmatch(s); m != nil {
		return m[1]
	}

	s = torrentSuffixRegex.ReplaceAllString(s, "")
	s = cleanGroupRegex.ReplaceAllString(s, "")

	if all := exactGroupRegex.FindAllStringSubmatch(s, -1); len(all) > 0 {
		return all[len(all)-1][1]
	}
	if all := bracketGroupRegex.FindAllStringSubmatch(s, -1); len(all) > 0 {
		return all[len(all)-1][1]
	}

	group := scanForGroup(s)
	if m := bracketTailRegex.FindStringSubmatch(s); m != nil && !isMarker(strings.ToLower(m[1])) && !yearRe.MatchString(m[1]) {
		// A bracketed group at the very end comes after any dash group
		// ("[YTS]"), unless it is only a quality tag ("[1080p]", "[HDR]").
		group = m[1]
	}
	if group == "" || allDigitsRegex.MatchString(group) || invalidGroupRegex.MatchString(group) {
		return ""
	}
	return group
}
