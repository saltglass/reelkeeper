package parser

// Edition detection. This follows Radarr's EditionRegex (GPL-3.0 like
// Reelkeeper): cuts and special releases such as "Director's Cut", "Extended
// Theatrical Version IMAX", "50th Anniversary Edition", "Open Matte".

import (
	"regexp"
	"strings"
	"unicode"
)

// editionRegex is Radarr's, with one change: Radarr uses a look-ahead to
// require "Cut", "Edition" or "Version" after words like "Special" or
// "Final" ("Special Edition", not the word "special" alone). Here that
// follow-up word is simply part of the match.
var editionRegex = regexp.MustCompile(`(?i)\(?\b(` +
	`(?:Recut.|Extended.|Ultimate.)?` +
	`(?:Director.?s|Collector.?s|Theatrical|Ultimate|Extended|Despecialized|` +
	`(?:Special|Rouge|Final|Assembly|Imperial|Diamond|Signature|Hunter|Rekall).(?:Cut|Edition|Version)|` +
	`\d{2,3}(?:th)?.Anniversary)` +
	`(?:.(?:Cut|Edition|Version))?` +
	`(?:.(?:Extended|Uncensored|Remastered|Unrated|Uncut|Open.?Matte|IMAX|Fan.?Edit))?` +
	`|(?:Uncensored|Remastered|Unrated|Uncut|Open?.Matte|IMAX|Fan.?Edit|Restored|(?:2|3|4)in1)` +
	`)\b\)?`)

// editionMatch is an edition found in a name, with where it sits (in bytes
// of the searched string).
type editionMatch struct {
	text       string // as written, dots turned into spaces
	start, end int
}

// findEditions returns every edition match in s.
func findEditions(s string) []editionMatch {
	var out []editionMatch
	for _, m := range editionRegex.FindAllStringSubmatchIndex(s, -1) {
		out = append(out, editionMatch{
			text:  strings.ReplaceAll(s[m[2]:m[3]], ".", " "),
			start: m[0], end: m[1],
		})
	}
	return out
}

// tidyEdition writes an edition the way Reelkeeper shows it: each word with a
// capital ("EXTENDED CUT" becomes "Extended Cut"), IMAX in capitals, and
// "Directors"/"Collectors" with their apostrophe. Words that already mix
// cases ("FanEdit") are left alone.
func tidyEdition(e string) string {
	words := strings.Fields(e)
	for i, w := range words {
		lw := strings.ToLower(w)
		switch {
		case lw == "imax":
			words[i] = "IMAX"
		case lw == "directors":
			words[i] = "Director's"
		case lw == "collectors":
			words[i] = "Collector's"
		case w == strings.ToUpper(w) || w == lw:
			rs := []rune(lw)
			rs[0] = unicode.ToUpper(rs[0])
			words[i] = string(rs)
		}
	}
	return strings.Join(words, " ")
}
