package decision

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/saltglass/reelkeeper/internal/parser"
)

// Movie is what the engine needs to know about the movie being searched
// for.
type Movie struct {
	Title     string
	Year      int
	AltTitles []string
	// Runtime in minutes, for size limits. 0 skips them.
	Runtime int
	// OriginalLanguage as a Radarr language ID (see LanguageByCode).
	OriginalLanguage int
}

// Candidate is one release found on an indexer.
type Candidate struct {
	Name    string
	Size    int64 // bytes, 0 if the indexer didn't say
	Seeders int
	// MinSeeders rejects releases with fewer seeders; 0 accepts any.
	MinSeeders int
	// IndexerFlags are Radarr's indexer flags (freeleech, internal...),
	// which some custom formats test.
	IndexerFlags int
	// Blocklisted is set by the caller when the release is on the
	// blocklist.
	Blocklisted bool
}

// Existing describes the file a version already has, for upgrade checks.
type Existing struct {
	// Name is what title patterns are matched against: for a library file,
	// library.MediaFile.ScoringName (its name plus what its headers say).
	Name     string
	FileName string
	Quality  Quality
	Revision int // 2+ for PROPER/REPACK
	// Languages as Radarr language IDs; empty means "as the name says".
	Languages []int
	Size      int64
}

// Decision is the engine's verdict on one release.
type Decision struct {
	Name     string         `json:"name"`
	Release  parser.Release `json:"release"`
	Quality  Quality        `json:"-"`
	Revision int            `json:"revision"`
	// Languages are Radarr language IDs: those the name states, or the
	// movie's original language when it states none.
	Languages []int         `json:"languages"`
	Formats   []FormatMatch `json:"formats"`
	Score     int           `json:"score"`
	// Accepted means the profile would grab it. Rejections say why not, in
	// plain words, one sentence each.
	Accepted   bool     `json:"accepted"`
	Rejections []string `json:"rejections,omitempty"`
	// Upgrade is set when the release would replace an existing file.
	Upgrade bool `json:"upgrade,omitempty"`
	// WrongContent is set when a TV release is for something else than was
	// searched for (another show, season or episode, or a name Reelkeeper
	// can't make sense of), as opposed to being the right content that the profile
	// turns down (quality, size, score, seeders). Backfill's "options" never
	// offer wrong content. Not part of the stored or shown verdict.
	WrongContent bool `json:"-"`

	rank    int // position of the quality in the profile, higher is better
	seeders int
	size    int64
}

// Decide checks one release against a profile. have is the file the
// version already has, or nil.
//
// Every check runs, even after one fails, so the reasons list everything
// that's wrong with a release rather than just the first thing.
func Decide(p *Profile, m Movie, c Candidate, have *Existing) Decision {
	r := parser.Parse(c.Name)
	d := Decision{
		Name: c.Name, Release: r, Quality: QualityOf(r), Revision: max(r.Revision, 1),
		Languages: languagesOf(r, m), seeders: c.Seeders, size: c.Size,
	}
	d.rank = p.rank(d.Quality)
	d.Score, d.Formats = p.Score(Input{
		Name: c.Name, Release: r, Quality: d.Quality, Languages: d.Languages,
		OriginalLanguage: m.OriginalLanguage, Size: c.Size, IndexerFlags: c.IndexerFlags, Year: m.Year,
	})
	reject := func(format string, args ...any) { d.Rejections = append(d.Rejections, fmt.Sprintf(format, args...)) }

	if why := wrongMovie(r, m); why != "" {
		reject("%s", why)
	}
	switch {
	case d.Quality.ID == 0:
		reject("Reelkeeper can't tell the quality from the name.")
	case !p.Allowed(d.Quality):
		reject("%s isn't a quality this profile accepts.", d.Quality.Name)
	}
	if why := p.sizeProblem(d.Quality, c.Size, m.Runtime, fmt.Sprintf("a %d-minute movie", m.Runtime)); why != "" {
		reject("%s", why)
	}
	if blocked := blockedFormats(d.Formats); len(blocked) > 0 {
		reject("It matches %s, which this profile blocks.", list(blocked))
	} else if d.Score < p.MinScore {
		reject("Its custom-format score is %d; this profile needs at least %d.", d.Score, p.MinScore)
	}
	for _, req := range p.Required {
		if !slices.ContainsFunc(d.Formats, func(f FormatMatch) bool { return slices.Contains(req.AnyOf, f.Name) }) {
			reject("It doesn't have %s, which this profile requires.", req.Label)
		}
	}
	if c.Blocklisted {
		reject("It's on the blocklist.")
	}
	if c.MinSeeders > 0 && c.Seeders < c.MinSeeders {
		reject("It has %s; at least %d needed.", plural(c.Seeders, "seeder"), c.MinSeeders)
	}
	if have != nil {
		if why := p.notAnUpgrade(d, m, have); why != "" {
			reject("%s", why)
		} else {
			d.Upgrade = true
		}
	}
	d.Accepted = len(d.Rejections) == 0
	return d
}

// notAnUpgrade explains why d wouldn't improve on the existing file, or
// returns "" when it would.
func (p *Profile) notAnUpgrade(d Decision, m Movie, have *Existing) string {
	if !p.UpgradesAllowed {
		return "There's already a file, and this profile doesn't upgrade."
	}
	haveScore := p.existingScore(have, m)
	haveRank, cutoff := p.haveRank(have.Quality), p.itemIndex(p.UpgradeUntil)
	haveName := fmt.Sprintf("%s, score %d", have.Quality.Name, haveScore)

	if haveRank == len(p.Items) {
		return fmt.Sprintf("The current file (%s) is already better than anything this profile grabs.", haveName)
	}
	// A PROPER/REPACK fixes a broken copy of the same quality, so it counts
	// even once upgrades have stopped (as in Radarr).
	proper := d.rank == haveRank && d.Revision > max(have.Revision, 1) && p.PreferPropers
	if haveRank >= cutoff && haveScore >= p.UpgradeUntilScore && !proper {
		if p.UpgradeUntilScore <= BlockScore+1 { // no score target (simple editor)
			return fmt.Sprintf("The current file (%s) already reaches \"upgrade until\" (%s).", haveName, p.UpgradeUntil)
		}
		return fmt.Sprintf("The current file (%s) already reaches \"upgrade until\" (%s, score %d).",
			haveName, p.UpgradeUntil, p.UpgradeUntilScore)
	}
	switch {
	case d.rank < haveRank:
		return fmt.Sprintf("It's a lower quality than the current file (%s).", haveName)
	case d.rank > haveRank && haveRank < cutoff:
		return "" // better quality, and quality upgrades haven't stopped yet
	case d.rank == haveRank && d.Revision != max(have.Revision, 1):
		// Same quality: a PROPER/REPACK fixes a broken release.
		if d.Revision > max(have.Revision, 1) && p.PreferPropers {
			return ""
		}
		if d.Revision < max(have.Revision, 1) {
			return fmt.Sprintf("The current file is a newer revision (%s).", haveName)
		}
	}
	// Otherwise (same quality, or a higher one once quality upgrades have
	// stopped) only a better custom-format score counts, as in Radarr.
	if haveScore < p.UpgradeUntilScore && d.Score >= haveScore+max(p.MinUpgradeStep, 1) {
		return ""
	}
	return fmt.Sprintf("It isn't better than the current file (%s).", haveName)
}

// existingScore is the custom-format score of a file the version has.
func (p *Profile) existingScore(have *Existing, m Movie) int {
	score, _ := p.FileScore(have, m)
	return score
}

// FileScore is the custom-format score of a file the version has, and the
// formats it matches (the Wanted page shows them).
func (p *Profile) FileScore(have *Existing, m Movie) (int, []FormatMatch) {
	hr := parser.Parse(have.Name)
	langs := have.Languages
	if len(langs) == 0 {
		langs = languagesOf(hr, m)
	}
	return p.Score(Input{
		Name: have.Name, FileName: have.FileName, Release: hr, Quality: have.Quality, Languages: langs,
		OriginalLanguage: m.OriginalLanguage, Size: have.Size, Year: m.Year,
	})
}

// QualityReached reports whether q is at or above "upgrade until", so only
// a better custom-format score is still wanted.
func (p *Profile) QualityReached(q Quality) bool {
	return p.haveRank(q) >= p.itemIndex(p.UpgradeUntil)
}

// CutoffMet reports whether a file leaves nothing to upgrade: upgrades
// are off, or it has reached "upgrade until" in both quality and score.
// Automatic searches skip versions whose file meets the cutoff.
func (p *Profile) CutoffMet(have *Existing, m Movie) bool {
	if !p.UpgradesAllowed {
		return true
	}
	if p.haveRank(have.Quality) == len(p.Items) {
		return true // better than anything the profile grabs, whatever its score
	}
	return p.QualityReached(have.Quality) && p.existingScore(have, m) >= p.UpgradeUntilScore
}

// SameMovie reports whether a parsed name is for movie m (title or an
// alternative title, and a year within one of the movie's). RSS sync uses
// it to find which wanted movie a new release is for.
func SameMovie(r parser.Release, m Movie) bool { return wrongMovie(r, m) == "" }

// sizeProblem checks the size limits for q, or returns "".
// what describes the runtime in messages ("a 112-minute movie").
func (p *Profile) sizeProblem(q Quality, size int64, runtime int, what string) string {
	lim, ok := p.Sizes[q.Name]
	if !ok || size <= 0 || runtime <= 0 {
		return ""
	}
	mb := float64(size) / (1 << 20)
	gbFor := func(mbPerMin float64) string { return fmt.Sprintf("%.1f GB", mbPerMin*float64(runtime)/1024) }
	switch {
	case lim.Min > 0 && mb < lim.Min*float64(runtime):
		return fmt.Sprintf("At %s it's too small: %s for %s needs at least %s.",
			humanSize(size), q.Name, what, gbFor(lim.Min))
	case lim.Max > 0 && mb > lim.Max*float64(runtime):
		return fmt.Sprintf("At %s it's too big: %s for %s is limited to %s.",
			humanSize(size), q.Name, what, gbFor(lim.Max))
	}
	return ""
}

// Best sorts decisions best first: accepted before rejected, then by
// quality (and revision, when propers are preferred), custom-format score,
// then enough seeders, size and seeders (see Compare). Ties keep their order. It's what picks the release to
// grab: the first accepted one.
func (p *Profile) Best(ds []Decision) { slices.SortStableFunc(ds, p.Compare) }

// Compare orders two decisions the way Best does: negative when a is the
// better choice.
func (p *Profile) Compare(a, b Decision) int {
	{
		if a.Accepted != b.Accepted {
			if a.Accepted {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(b.rank, a.rank); c != 0 {
			return c
		}
		if p.PreferPropers {
			if c := cmp.Compare(b.Revision, a.Revision); c != 0 {
				return c
			}
		}
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		// A release with enough seeders downloads fine; past that, size
		// says more than popularity: within the size limits, bigger is
		// usually less compressed. (A small re-encode with 145 seeders
		// once beat an 8 GB web release with 14.) Below enough seeders,
		// seeders come first, in steps (1, 10, 100...).
		aOK, bOK := a.seeders >= enoughSeeders, b.seeders >= enoughSeeders
		switch {
		case aOK != bOK:
			if aOK {
				return -1
			}
			return 1
		case aOK:
			if c := cmp.Compare(b.size, a.size); c != 0 {
				return c
			}
			return cmp.Compare(b.seeders, a.seeders)
		}
		if c := cmp.Compare(seedStep(b.seeders), seedStep(a.seeders)); c != 0 {
			return c
		}
		return cmp.Compare(b.size, a.size)
	}
}

// enoughSeeders is when a torrent can be counted on to finish; past it,
// more seeders don't make a release better.
const enoughSeeders = 5

func seedStep(n int) int {
	if n <= 0 {
		return 0
	}
	return int(math.Log10(float64(n))) + 1
}

// languagesOf is the release's languages as Radarr IDs. A name that states
// no language is taken to be in the movie's original language, as Radarr
// does.
func languagesOf(r parser.Release, m Movie) []int {
	var ids []int
	for _, name := range r.Languages {
		if id := LanguageByName(name); id != LanguageUnknown && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return []int{m.OriginalLanguage}
	}
	return ids
}

// wrongMovie explains why the name isn't this movie, or returns "".
func wrongMovie(r parser.Release, m Movie) string {
	if r.Title == "" {
		return "Reelkeeper can't tell which movie this is from the name."
	}
	got := matchKey(r.Title)
	titleOK := got == matchKey(m.Title)
	for _, t := range m.AltTitles {
		titleOK = titleOK || got == matchKey(t)
	}
	// Release years can be a year off the official one (festival vs.
	// cinema release), so Radarr and Reelkeeper both allow one year either way.
	yearOK := r.Year == 0 || m.Year == 0 || abs(r.Year-m.Year) <= 1
	switch {
	case !titleOK && r.Year != 0:
		return fmt.Sprintf("The name is for %q (%d), not %s (%d).", r.Title, r.Year, m.Title, m.Year)
	case !titleOK:
		return fmt.Sprintf("The name is for %q, not %s.", r.Title, m.Title)
	case !yearOK:
		return fmt.Sprintf("The name says %d, but %s is from %d.", r.Year, m.Title, m.Year)
	}
	return ""
}

// TitleKey reduces a title to what two spellings of it share, so RSS sync
// can look up which wanted movie a release name is for.
func TitleKey(s string) string { return matchKey(s) }

// matchKey reduces a title to what two spellings of it share: lower case,
// accents and punctuation removed, "&" read as "and".
func matchKey(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "&", " and ")
	var b strings.Builder
	space := false
	for _, r := range norm.NFD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r): // combining accent: drop
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
		case r == '\'' || r == '’': // "Schindler's" = "Schindlers"
		default:
			space = true
		}
	}
	return b.String()
}

func blockedFormats(fs []FormatMatch) []string {
	var names []string
	for _, f := range fs {
		if f.Score <= BlockScore {
			names = append(names, "\""+f.Name+"\"")
		}
	}
	return names
}

// list joins names as "a", "a and b" or "a, b and c".
func list(names []string) string {
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func humanSize(b int64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%.0f MB", float64(b)/(1<<20))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
