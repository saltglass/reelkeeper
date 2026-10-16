package decision

import (
	"slices"
	"strings"
)

// Language IDs follow Radarr's, because custom formats (from Radarr and
// TRaSH Guides) refer to languages by these numbers. The table is adapted
// from Radarr's Language.cs and IsoLanguages.cs (GPL-3.0).
const (
	LanguageUnknown  = 0
	LanguageEnglish  = 1
	LanguageAny      = -1 // in a condition: any language at all
	LanguageOriginal = -2 // in a condition: the movie's original language
)

// Language is one language Reelkeeper can recognise in a release name.
type Language struct {
	ID   int
	Name string // as the parser writes it, e.g. "Portuguese (Brazil)"
	// Codes are ISO 639-1 codes, sometimes with a region ("pt-br"), as
	// TMDB gives a movie's original language.
	Codes []string
}

// Languages lists every language by Radarr ID.
var Languages = []Language{
	{1, "English", []string{"en"}},
	{2, "French", []string{"fr"}},
	{3, "Spanish", []string{"es"}},
	{4, "German", []string{"de"}},
	{5, "Italian", []string{"it"}},
	{6, "Danish", []string{"da"}},
	{7, "Dutch", []string{"nl"}},
	{8, "Japanese", []string{"ja"}},
	{9, "Icelandic", []string{"is"}},
	{10, "Chinese", []string{"zh", "cn"}},
	{11, "Russian", []string{"ru"}},
	{12, "Polish", []string{"pl"}},
	{13, "Vietnamese", []string{"vi"}},
	{14, "Swedish", []string{"sv"}},
	{15, "Norwegian", []string{"no", "nb"}},
	{16, "Finnish", []string{"fi"}},
	{17, "Turkish", []string{"tr"}},
	{18, "Portuguese", []string{"pt"}},
	{19, "Flemish", nil},
	{20, "Greek", []string{"el"}},
	{21, "Korean", []string{"ko"}},
	{22, "Hungarian", []string{"hu"}},
	{23, "Hebrew", []string{"he"}},
	{24, "Lithuanian", []string{"lt"}},
	{25, "Czech", []string{"cs"}},
	{26, "Hindi", []string{"hi"}},
	{27, "Romanian", []string{"ro"}},
	{28, "Thai", []string{"th"}},
	{29, "Bulgarian", []string{"bg"}},
	{30, "Portuguese (Brazil)", []string{"pt-br"}},
	{31, "Arabic", []string{"ar"}},
	{32, "Ukrainian", []string{"uk"}},
	{33, "Persian", []string{"fa"}},
	{34, "Bengali", []string{"bn"}},
	{35, "Slovak", []string{"sk"}},
	{36, "Latvian", []string{"lv"}},
	{37, "Spanish (Latino)", []string{"es-mx"}},
	{38, "Catalan", []string{"ca"}},
	{39, "Croatian", []string{"hr"}},
	{40, "Serbian", []string{"sr"}},
	{41, "Bosnian", []string{"bs"}},
	{42, "Estonian", []string{"et"}},
	{43, "Tamil", []string{"ta"}},
	{44, "Indonesian", []string{"id"}},
	{45, "Telugu", []string{"te"}},
	{46, "Macedonian", []string{"mk"}},
	{47, "Slovenian", []string{"sl"}},
	{48, "Malayalam", []string{"ml"}},
	{49, "Kannada", []string{"kn"}},
	{50, "Albanian", []string{"sq"}},
	{51, "Afrikaans", []string{"af"}},
	{52, "Marathi", []string{"mr"}},
	{53, "Tagalog", []string{"tl"}},
	{54, "Urdu", []string{"ur"}},
	{55, "Romansh", []string{"rm"}},
	{56, "Mongolian", []string{"mn"}},
	{57, "Georgian", []string{"ka"}},
}

// LanguageByName finds a language by name, ignoring case. Unknown names
// give LanguageUnknown.
func LanguageByName(name string) int {
	for _, l := range Languages {
		if strings.EqualFold(l.Name, name) {
			return l.ID
		}
	}
	return LanguageUnknown
}

// LanguageByCode finds a language from TMDB's original_language code, such
// as "fr" or "pt". Unknown codes give LanguageUnknown.
func LanguageByCode(code string) int {
	code = strings.ToLower(code)
	for _, l := range Languages {
		if slices.Contains(l.Codes, code) {
			return l.ID
		}
	}
	return LanguageUnknown
}

// LanguageName is the name for a language ID, or "Unknown".
func LanguageName(id int) string {
	switch id {
	case LanguageAny:
		return "Any"
	case LanguageOriginal:
		return "Original"
	}
	for _, l := range Languages {
		if l.ID == id {
			return l.Name
		}
	}
	return "Unknown"
}
