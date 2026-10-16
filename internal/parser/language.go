package parser

// Language detection. This follows Radarr's LanguageParser (GPL-3.0 like
// Reelkeeper). Names state audio languages in many ways: whole words
// ("French"), short codes ("FR", "VFF", "ENG"), dubbing tags ("PL.DUB",
// "Dublado"), and codes that are only safe in capitals ("DE", "ES").
//
// The text given to parseLanguages must have the title and the release
// group taken out first (Parse does that), because words in those are not
// claims about the audio: "The German" is a title, and "DTS-ES" is a sound
// format, not Spanish.

import (
	"regexp"
	"strings"
)

// languageWords are plain words that count as a language wherever they
// appear in the name, in the order Radarr checks them.
var languageWords = []struct{ word, language string }{
	{"english", "English"}, {"spanish", "Spanish"}, {"danish", "Danish"}, {"dutch", "Dutch"},
	{"japanese", "Japanese"}, {"icelandic", "Icelandic"}, {"mandarin", "Chinese"}, {"cantonese", "Chinese"},
	{"chinese", "Chinese"}, {"korean", "Korean"}, {"russian", "Russian"}, {"romanian", "Romanian"},
	{"hindi", "Hindi"}, {"arabic", "Arabic"}, {"thai", "Thai"}, {"bulgarian", "Bulgarian"},
	{"polish", "Polish"}, {"vietnamese", "Vietnamese"}, {"swedish", "Swedish"}, {"norwegian", "Norwegian"},
	{"finnish", "Finnish"}, {"turkish", "Turkish"}, {"portuguese", "Portuguese"},
	{"brazilian", "Portuguese (Brazil)"}, {"hungarian", "Hungarian"}, {"hebrew", "Hebrew"},
	{"ukrainian", "Ukrainian"}, {"persian", "Persian"}, {"bengali", "Bengali"}, {"slovak", "Slovak"},
	{"latvian", "Latvian"}, {"latino", "Spanish (Latino)"}, {"tamil", "Tamil"}, {"telugu", "Telugu"},
	{"malayalam", "Malayalam"}, {"kannada", "Kannada"}, {"albanian", "Albanian"},
	{"afrikaans", "Afrikaans"}, {"marathi", "Marathi"}, {"tagalog", "Tagalog"},
}

var (
	languageWordRegex *regexp.Regexp
	languageByWord    = map[string]string{}
)

func init() {
	words := make([]string, 0, len(languageWords))
	for _, w := range languageWords {
		words = append(words, w.word)
		languageByWord[w.word] = w.language
	}
	languageWordRegex = regexp.MustCompile(`(?i)\b(` + strings.Join(words, "|") + `)\b`)
}

// languageRegex is Radarr's case-insensitive pattern: short codes and
// tags. Each alternative is a named group, so the match says which language
// it was. Radarr's "orig/original" group is left out: "original audio" is
// not a language.
//
// Go's \b only knows ASCII letters, so the one tag ending in an accented
// letter ("català") ends with an explicit "next character is not a letter".
var languageRegex = regexp.MustCompile(`(?i)(?:\W|_|^)(?P<english>\beng\b)` +
	`|(?P<italian>\b(?:ita|italian)\b)` +
	`|(?P<german>(?:swiss)?german\b|videomann|ger[. ]dub|\bger\b)` +
	`|(?P<flemish>flemish)` +
	`|(?P<bulgarian>bgaudio)` +
	`|(?P<romanian>rodubbed)` +
	`|(?P<brazilian>\b(?:dublado|pt-BR)\b)` +
	`|(?P<greek>greek)` +
	`|(?P<french>\b(?:FR|VO|VF|VFF|VFQ|VFI|VF2|TRUEFRENCH|FRENCH|FRE|FRA)\b)` +
	`|(?P<russian>\b(?:rus|ru)\b)` +
	`|(?P<hungarian>\b(?:HUNDUB|HUN)\b)` +
	`|(?P<hebrew>\b(?:HebDub|HebDubbed)\b)` +
	`|(?P<polish>\b(?:PL\W?DUB|DUB\W?PL|LEK\W?PL|PL\W?LEK)\b)` +
	`|(?P<chinese>\[(?:CH[ST]|BIG5|GB)\]|简|繁|字幕)` +
	`|(?P<ukrainian>(?:\dx)?UKR)` +
	`|(?P<spanish>\b(?:español|castellano)\b)` +
	`|(?P<catalan>\b(?:catalan?|catalán)\b|\bcatalà(?:[^\p{L}\p{N}_]|$))` +
	`|(?P<latvian>\b(?:lat|lav|lv)\b)` +
	`|(?P<telugu>\btel\b)` +
	`|(?P<vietnamese>\bVIE\b)` +
	`|(?P<japanese>\bJAP\b)` +
	`|(?P<korean>\bKOR\b)` +
	`|(?P<urdu>\burdu\b)` +
	`|(?P<romansh>\b(?:romansh|rumantsch|romansch)\b)` +
	`|(?P<mongolian>\b(?:mongolian|khalkha)\b)` +
	`|(?P<georgian>\b(?:georgian|geo|ka|kat)\b)`)

// languageGroups maps the pattern's group names to English language names.
var languageGroups = map[string]string{
	"english": "English", "italian": "Italian", "german": "German", "flemish": "Flemish",
	"bulgarian": "Bulgarian", "romanian": "Romanian", "brazilian": "Portuguese (Brazil)", "greek": "Greek",
	"french": "French", "russian": "Russian", "hungarian": "Hungarian", "hebrew": "Hebrew",
	"polish": "Polish", "chinese": "Chinese", "ukrainian": "Ukrainian", "spanish": "Spanish",
	"catalan": "Catalan", "latvian": "Latvian", "telugu": "Telugu", "vietnamese": "Vietnamese",
	"japanese": "Japanese", "korean": "Korean", "urdu": "Urdu", "romansh": "Romansh",
	"mongolian": "Mongolian", "georgian": "Georgian",
}

// upperCodeRegex finds two-letter codes that only count in capitals, since
// in lower case they are ordinary words ("en", "de", "es", "pl"). Radarr's
// CaseSensitiveLanguageRegex.
var upperCodeRegex = regexp.MustCompile(`\b(EN|LT|CZ|PL|BG|SK|DE|ES)\b`)

var upperCodeLanguage = map[string]string{
	"EN": "English", "LT": "Lithuanian", "CZ": "Czech", "PL": "Polish",
	"BG": "Bulgarian", "SK": "Slovak", "DE": "German", "ES": "Spanish",
}

// isSubSeparator is the character class Radarr writes as [\W|_|^]: a
// non-word character, or one of | _ ^.
func isSubSeparator(c byte) bool {
	isWord := c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
	return !isWord || c == '_' || c == '|' || c == '^'
}

// upperCodeCounts applies Radarr's look-around rules to one capital code at
// text[start:end]: it does not count inside subtitle tags ("SUB.ES",
// "ES-SUB"), and "ES" does not count right after DTS ("DTS-ES" is a sound
// format).
func upperCodeCounts(text string, start, end int) bool {
	if start >= 4 && strings.EqualFold(text[start-4:start-1], "sub") && isSubSeparator(text[start-1]) {
		return false
	}
	if end+4 <= len(text) && isSubSeparator(text[end]) && strings.EqualFold(text[end+1:end+4], "sub") {
		return false
	}
	if text[start:end] == "ES" && start >= 4 && strings.EqualFold(text[start-4:start-1], "dts") &&
		strings.ContainsRune("._ -", rune(text[start-1])) {
		return false
	}
	return true
}

// parseLanguages returns the audio languages a name states, once each, in
// English. Empty means the name states none (usually the original language).
func parseLanguages(text string) []string {
	var out []string
	add := func(l string) {
		for _, have := range out {
			if have == l {
				return
			}
		}
		out = append(out, l)
	}

	// Radarr looks for these as plain substrings, which finds "hindi" in
	// "Shindig". Whole words only here.
	for _, m := range languageWordRegex.FindAllStringSubmatch(text, -1) {
		add(languageByWord[strings.ToLower(m[1])])
	}

	for _, m := range upperCodeRegex.FindAllStringSubmatchIndex(text, -1) {
		if upperCodeCounts(text, m[2], m[3]) {
			add(upperCodeLanguage[text[m[2]:m[3]]])
		}
	}

	names := languageRegex.SubexpNames()
	for _, m := range languageRegex.FindAllStringSubmatchIndex(text, -1) {
		for i, n := range names {
			if n != "" && m[2*i] >= 0 {
				add(languageGroups[n])
			}
		}
	}
	return out
}
