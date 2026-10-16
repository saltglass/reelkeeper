//go:build lab

package main

import (
	"crypto/sha1" // #nosec G505 -- fake torrent hashes only
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"
)

// gen holds the random sources and the value pools every generator uses.
type gen struct {
	rng *rand.Rand
	now time.Time
}

var words = []string{
	"Amélie", "Crème", "Brûlée", "Zoë", "東京物語", "Nightfall", "Harbor", "Ünïcödé", "Straße", "Ångström",
	"The", "of", "and", "Dark", "Light", "Echo", "Garden", "Winter", "Summer", "Quiet", "Storm", "Neon", "Paper",
	"River", "Mirror", "Skyline", "Café", "Señor", "Naïve", "Über", "Fjord", "ممر", "Ελλάδα", "עברית", "🎬", "🌟",
	"Return", "Final", "Last", "Midnight", "Iron", "Glass", "Velvet", "Empire", "Lost", "Hidden", "Blue", "Red",
}

var edgeStrings = []string{
	"", " ", "  leading and trailing  ", "O'Brien", `say "hi"`, `back\slash`, "line1\nline2", "tab\there",
	"CR\r\nLF", "100% sure", "50_50", "a?b?c", "$1 $2 $$", ":name @p ?", "'; DROP TABLE titles; --",
	"<b>bold</b> & more", "{{template}}", "zero​width", "😀😃😄", "👨‍👩‍👧‍👦 family", "Ω≈ç√∫", "日本語のタイトル",
	"ЩЪЫЬ", "é (combining)", "null", "NULL", "true", "0", "[]", "{}", `"quoted"`, `\n literal`, "%s %d %v",
	strings.Repeat("long ", 800),
}

var genres = []string{"Action", "Drama", "Comedy", "Horror", "Sci-Fi", "Thriller", "Animation", "Documentary", "Romance", "Crime", "Fantasy", "Western"}

func (g *gen) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return g.rng.IntN(n)
}
func (g *gen) chance(p float64) bool     { return g.rng.Float64() < p }
func (g *gen) pickStr(l []string) string { return l[g.intn(len(l))] }
func (g *gen) between(lo, hi int) int    { return lo + g.intn(hi-lo+1) }
func (g *gen) word() string              { return g.pickStr(words) }
func (g *gen) hash40(i int) string {
	h := sha1.Sum([]byte(fmt.Sprintf("lab-%d-%d", i, g.rng.Uint64()))) // #nosec G401
	return hex.EncodeToString(h[:])
}

func (g *gen) phrase(n int) string {
	p := make([]string, n)
	for i := range p {
		p[i] = g.word()
	}
	return strings.Join(p, " ")
}

// text is a general-purpose string with the odd awkward one mixed in.
func (g *gen) text() string {
	switch r := g.rng.Float64(); {
	case r < 0.08:
		return g.pickStr(edgeStrings)
	case r < 0.12:
		return "" // empty, as opposed to NULL
	case r < 0.15:
		return g.phrase(5) + "\n" + g.phrase(8) + "\n\n" + g.phrase(3)
	case r < 0.16:
		return strings.Repeat(g.phrase(6)+". ", g.between(50, 400))
	}
	return g.phrase(g.between(1, 5))
}

// title is a movie or series name.
func (g *gen) title(i int) string {
	if g.chance(0.04) {
		return fmt.Sprintf("%s #%d", g.pickStr(edgeStrings[2:30]), i)
	}
	return fmt.Sprintf("%s %s %d", g.word(), g.word(), i)
}

var edgeFloats = []float64{0, 0.1 + 0.2, 1e-9, 123456.78901234567, 1.0 / 3.0, 5e-324, 1.7976931348623157e308, -1e-9, 1e21, 100, 0.5, 9007199254740993, math.Pi, -0.0000123}

func (g *gen) float() float64 {
	if g.chance(0.08) {
		return edgeFloats[g.intn(len(edgeFloats))]
	}
	return math.Round(g.rng.Float64()*1e6) / 1e4
}

// randTime is mostly recent, sometimes far in the past or future, always
// with microseconds (what both engines hold exactly).
func (g *gen) randTime() time.Time {
	var t time.Time
	switch r := g.rng.Float64(); {
	case r < 0.70:
		t = g.now.Add(-time.Duration(g.rng.Int64N(int64(2 * 365 * 24 * time.Hour))))
	case r < 0.85:
		t = time.Date(2000+g.intn(20), time.Month(1+g.intn(12)), 1+g.intn(28), g.intn(24), g.intn(60), g.intn(60), 0, time.UTC)
	case r < 0.90:
		t = []time.Time{time.Date(1901, 12, 13, 20, 45, 52, 0, time.UTC), time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(1999, 12, 31, 23, 59, 59, 0, time.UTC)}[g.intn(4)]
	case r < 0.95:
		t = []time.Time{time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2038, 1, 19, 3, 14, 8, 0, time.UTC),
			time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), g.now.Add(24 * time.Hour)}[g.intn(4)]
	default:
		t = g.now.Add(-time.Duration(g.rng.Int64N(int64(30 * 24 * time.Hour))))
	}
	return t.Truncate(time.Second).Add(time.Duration(g.intn(1_000_000)) * time.Microsecond).UTC()
}

// recentTime is a sane timestamp within the last `days` days.
func (g *gen) recentTime(days int) time.Time {
	d := time.Duration(g.rng.Int64N(int64(days) * int64(24*time.Hour)))
	return g.now.Add(-d).Truncate(time.Second).Add(time.Duration(g.intn(1_000_000)) * time.Microsecond).UTC()
}

func (g *gen) date() string {
	return g.now.AddDate(0, 0, -g.intn(365*30)).Format("2006-01-02")
}

// anInt: a mix of ordinary and edge values that fit `bits`.
func (g *gen) anInt(bits int) int64 {
	maxV := int64(math.MaxInt32)
	minV := int64(math.MinInt32)
	switch bits {
	case 16:
		maxV, minV = math.MaxInt16, math.MinInt16
	case 64:
		maxV, minV = math.MaxInt64, math.MinInt64
	}
	switch r := g.rng.Float64(); {
	case r < 0.12:
		return 0
	case r < 0.16:
		return -1
	case r < 0.18:
		return -int64(g.intn(5000))
	case r < 0.20:
		return maxV
	case r < 0.21:
		return minV
	case r < 0.22:
		return maxV - int64(g.intn(5))
	case r < 0.80:
		return int64(g.intn(2000))
	}
	if bits == 64 {
		return g.rng.Int64N(1 << 40)
	}
	return int64(g.intn(1 << 20))
}

// jsonValue makes a small JSON document: an array or an object, nested a little.
func (g *gen) jsonValue(array bool, depth int) any {
	scalar := func() any {
		switch g.intn(6) {
		case 0:
			return g.text()
		case 1:
			return int64(g.intn(100000)) - 500
		case 2:
			return g.float()
		case 3:
			return g.chance(0.5)
		case 4:
			return nil
		}
		return g.phrase(2)
	}
	if array {
		n := g.intn(5)
		out := make([]any, n)
		for i := range out {
			out[i] = scalar()
		}
		return out
	}
	out := map[string]any{}
	for i, n := 0, g.intn(6); i < n; i++ {
		k := fmt.Sprintf("k%d_%s", i, g.word())
		if depth < 2 && g.chance(0.2) {
			out[k] = g.jsonValue(g.chance(0.5), depth+1)
		} else {
			out[k] = scalar()
		}
	}
	return out
}

func (g *gen) jsonText(array bool) string {
	v := g.jsonValue(array, 0)
	// Floats like 1e21 and 5e-324 are valid JSON and fine on both engines.
	b, _ := json.Marshal(v)
	return string(b)
}
