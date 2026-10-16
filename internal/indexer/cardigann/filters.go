package cardigann

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Filters transform a value step by step: {name: re_replace, args: [...]}.
// Their arguments may be templates themselves ("{{ .Config.x }}"). Dates
// come out as RFC 3339 text, which the field reader understands.

// applyFilters runs filters in order. now is the time "2 hours ago" counts
// from (passed in so tests are repeatable).
func applyFilters(value string, filters []Filter, v Vars, now time.Time) (string, error) {
	for _, f := range filters {
		args := make([]string, len(f.Args))
		for i, a := range f.Args {
			s, err := Execute(a, v)
			if err != nil {
				return "", fmt.Errorf("filter %s: %w", f.Name, err)
			}
			args[i] = s
		}
		var err error
		value, err = applyFilter(value, f.Name, args, now)
		if err != nil {
			return "", fmt.Errorf("filter %s: %w", f.Name, err)
		}
	}
	return value, nil
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func applyFilter(value, name string, args []string, now time.Time) (string, error) {
	switch name {
	case "querystring":
		u, err := url.Parse(value)
		if err != nil {
			return "", nil
		}
		return u.Query().Get(arg(args, 0)), nil
	case "dateparse", "timeparse":
		t, err := parseDotNetDate(strings.TrimSpace(value), arg(args, 0), now)
		if err != nil {
			return "", err
		}
		return t.Format(time.RFC3339), nil
	case "regexp":
		m, _, err := reFind(value, arg(args, 0))
		return m, err
	case "re_replace":
		return reReplace(value, arg(args, 0), arg(args, 1))
	case "split":
		parts := strings.Split(value, arg(args, 0))
		i, err := strconv.Atoi(strings.TrimSpace(arg(args, 1)))
		if err != nil {
			return "", fmt.Errorf("bad index %q", arg(args, 1))
		}
		if i < 0 {
			i += len(parts)
		}
		if i < 0 || i >= len(parts) {
			return "", nil
		}
		return parts[i], nil
	case "replace":
		return strings.ReplaceAll(value, arg(args, 0), arg(args, 1)), nil
	case "trim":
		if len(args) > 0 && args[0] != "" {
			return strings.Trim(value, args[0]), nil
		}
		return strings.TrimSpace(value), nil
	case "prepend":
		return arg(args, 0) + value, nil
	case "append":
		return value + arg(args, 0), nil
	case "tolower":
		return strings.ToLower(value), nil
	case "toupper":
		return strings.ToUpper(value), nil
	case "urldecode":
		if s, err := url.QueryUnescape(value); err == nil {
			return s, nil
		}
		return value, nil
	case "urlencode":
		return url.QueryEscape(value), nil
	case "htmldecode":
		return html.UnescapeString(value), nil
	case "htmlencode":
		return html.EscapeString(value), nil
	case "timeago", "reltime":
		t, ok := parseTimeAgo(value, now)
		if !ok {
			return "", fmt.Errorf("can't read %q as a time ago", value)
		}
		return t.Format(time.RFC3339), nil
	case "fuzzytime":
		t, ok := parseFuzzyTime(value, now)
		if !ok {
			return "", fmt.Errorf("can't read %q as a date", value)
		}
		return t.Format(time.RFC3339), nil
	case "validfilename":
		return strings.Map(func(r rune) rune {
			if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
				return -1
			}
			return r
		}, value), nil
	case "diacritics":
		t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
		s, _, _ := transform.String(t, value)
		return s, nil
	case "jsonjoinarray":
		return jsonJoinArray(value, arg(args, 0), arg(args, 1))
	case "validate":
		// Keep the words of the value that appear in the allowed list:
		// "Drama, Spy, Thriller" with "Action, Drama, Thriller" gives
		// "Drama, Thriller".
		var allowed []string
		for _, a := range strings.Split(arg(args, 0), ",") {
			allowed = append(allowed, strings.ToLower(strings.TrimSpace(a)))
		}
		var keep []string
		for _, w := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '/' || r == '|' }) {
			w = strings.TrimSpace(w)
			for _, a := range allowed {
				if a != "" && strings.EqualFold(w, a) {
					keep = append(keep, w)
					break
				}
			}
		}
		return strings.Join(keep, ", "), nil
	case "hexdump", "strdump":
		return value, nil // debugging aids in Jackett; nothing to do
	}
	return "", fmt.Errorf("unknown filter %q", name)
}

// jsonJoinArray joins the strings of a JSON array found at a path inside
// a JSON value.
func jsonJoinArray(value, path, sep string) (string, error) {
	var doc any
	if err := json.Unmarshal([]byte(value), &doc); err != nil {
		return "", fmt.Errorf("not JSON: %w", err)
	}
	v, _ := jsonPath(doc, path)
	arr, ok := v.([]any)
	if !ok {
		return "", nil
	}
	parts := make([]string, 0, len(arr))
	for _, x := range arr {
		parts = append(parts, jsonText(x))
	}
	return strings.Join(parts, sep), nil
}

// --- dates -----------------------------------------------------------------

// parseDotNetDate parses with a .NET custom date format ("yyyy-MM-dd
// HH:mm:ss zzz"), the style Prowlarr's definitions use. Without a time
// zone in the format, times are UTC.
func parseDotNetDate(value, format string, now time.Time) (time.Time, error) {
	layout := dotNetLayout(format)
	t, err := time.Parse(layout, value)
	if err != nil {
		// Month and day names come in any case ("jan", "JAN").
		if t2, err2 := time.Parse(layout, titleWords(value)); err2 == nil {
			return withYear(t2, layout, now), nil
		}
		return time.Time{}, fmt.Errorf("can't read %q as %q", value, format)
	}
	return withYear(t, layout, now), nil
}

// withYear gives a date written without a year this year's (or last
// year's, if it would be in the future).
func withYear(t time.Time, layout string, now time.Time) time.Time {
	if strings.Contains(layout, "2006") || strings.Contains(layout, "06") {
		return t
	}
	t = time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, t.Location())
	if t.After(now.Add(24 * time.Hour)) {
		t = t.AddDate(-1, 0, 0)
	}
	return t
}

func titleWords(s string) string {
	prev := ' '
	return strings.Map(func(r rune) rune {
		out := unicode.ToLower(r)
		if !unicode.IsLetter(prev) {
			out = unicode.ToUpper(r)
		}
		prev = r
		return out
	}, s)
}

// dotNetLayout translates a .NET custom date format into a Go layout.
func dotNetLayout(f string) string {
	tokens := []struct{ net, goLayout string }{
		{"yyyy", "2006"}, {"yy", "06"},
		{"MMMM", "January"}, {"MMM", "Jan"}, {"MM", "01"}, {"M", "1"},
		{"dddd", "Monday"}, {"ddd", "Mon"}, {"dd", "02"}, {"d", "2"},
		{"HH", "15"}, {"H", "15"}, {"hh", "03"}, {"h", "3"},
		{"mm", "04"}, {"m", "4"}, {"ss", "05"}, {"s", "5"},
		{"fff", "000"}, {"ff", "00"}, {"f", "0"},
		{"tt", "PM"}, {"zzz", "-07:00"}, {"zz", "-07"}, {"z", "-07"},
		{"K", "Z07:00"},
	}
	var b strings.Builder
	for i := 0; i < len(f); {
		switch f[i] {
		case '\'', '"': // quoted literal
			q := f[i]
			j := strings.IndexByte(f[i+1:], q)
			if j < 0 {
				b.WriteString(f[i+1:])
				return b.String()
			}
			b.WriteString(f[i+1 : i+1+j])
			i += j + 2
			continue
		case '\\':
			if i+1 < len(f) {
				b.WriteByte(f[i+1])
			}
			i += 2
			continue
		}
		matched := false
		for _, t := range tokens {
			if strings.HasPrefix(f[i:], t.net) {
				b.WriteString(t.goLayout)
				i += len(t.net)
				matched = true
				break
			}
		}
		if !matched {
			b.WriteByte(f[i])
			i++
		}
	}
	return b.String()
}

var agoRe = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*(sec|second|s\b|min|minute|m\b|hour|hr|h\b|day|d\b|week|wk|w\b|month|mo|year|yr|y\b)`)

// parseTimeAgo reads "2 hours ago", "1 day, 3 hours", "5 mins", "now".
func parseTimeAgo(value string, now time.Time) (time.Time, bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return time.Time{}, false
	}
	if strings.Contains(v, "now") || strings.Contains(v, "just") {
		return now, true
	}
	matches := agoRe.FindAllStringSubmatch(v, -1)
	if len(matches) == 0 {
		return time.Time{}, false
	}
	t := now
	for _, m := range matches {
		n, _ := strconv.ParseFloat(strings.Replace(m[1], ",", ".", 1), 64)
		unit := strings.ToLower(m[2])
		var d time.Duration
		switch {
		case strings.HasPrefix(unit, "s"):
			d = time.Second
		case strings.HasPrefix(unit, "mo"):
			t = t.AddDate(0, -int(n), 0)
			continue
		case strings.HasPrefix(unit, "m"):
			d = time.Minute
		case strings.HasPrefix(unit, "h"):
			d = time.Hour
		case strings.HasPrefix(unit, "d"):
			d = 24 * time.Hour
		case strings.HasPrefix(unit, "w"):
			d = 7 * 24 * time.Hour
		case strings.HasPrefix(unit, "y"):
			t = t.AddDate(-int(n), 0, 0)
			continue
		}
		t = t.Add(-time.Duration(n * float64(d)))
	}
	return t, true
}

// Layouts fuzzytime tries, most specific first.
var fuzzyLayouts = []string{
	time.RFC3339, time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.RFC850, time.ANSIC,
	"2006-01-02 15:04:05 -07:00", "2006-01-02 15:04:05 -0700", "2006-01-02 15:04:05",
	"2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02", "2006/01/02 15:04:05", "2006/01/02",
	"02-01-2006 15:04:05", "02-01-2006 15:04", "02-01-2006", "02.01.2006 15:04:05", "02.01.2006 15:04", "02.01.2006",
	"01/02/2006 15:04:05", "01/02/2006 15:04", "01/02/2006 3:04:05 PM", "01/02/2006",
	"Jan 2 2006 15:04", "Jan 2, 2006 15:04", "Jan 2, 2006", "2 Jan 2006 15:04", "2 Jan 2006",
	"January 2, 2006", "2 January 2006", "Mon, 2 Jan 2006 15:04:05", "Monday, January 2, 2006",
}

var clockRe = regexp.MustCompile(`(\d{1,2}):(\d{2})(?::(\d{2}))?\s*(am|pm)?`)

// parseFuzzyTime reads dates in whatever form a site writes them: "Today
// 12:30", "Yesterday", "3 days ago", a Unix timestamp, or a common date
// format.
func parseFuzzyTime(value string, now time.Time) (time.Time, bool) {
	v := strings.TrimSpace(value)
	lower := strings.ToLower(v)
	day := func(t time.Time) (time.Time, bool) {
		if m := clockRe.FindStringSubmatch(lower); m != nil {
			h, _ := strconv.Atoi(m[1])
			mi, _ := strconv.Atoi(m[2])
			s, _ := strconv.Atoi(m[3])
			if m[4] == "pm" && h < 12 {
				h += 12
			} else if m[4] == "am" && h == 12 {
				h = 0
			}
			return time.Date(t.Year(), t.Month(), t.Day(), h, mi, s, 0, time.UTC), true
		}
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), true
	}
	switch {
	case strings.HasPrefix(lower, "today") || strings.HasPrefix(lower, "now"):
		return day(now)
	case strings.HasPrefix(lower, "yesterday"):
		return day(now.AddDate(0, 0, -1))
	case strings.HasPrefix(lower, "tomorrow"):
		return day(now.AddDate(0, 0, 1))
	case strings.Contains(lower, "ago"):
		return parseTimeAgo(lower, now)
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 100000000 {
		if n > 1e12 {
			return time.UnixMilli(n).UTC(), true
		}
		return time.Unix(n, 0).UTC(), true
	}
	for _, l := range fuzzyLayouts {
		if t, err := time.Parse(l, v); err == nil {
			return t, true
		}
		if t, err := time.Parse(l, titleWords(v)); err == nil {
			return t, true
		}
	}
	// "1 day 3 hours" without "ago".
	if t, ok := parseTimeAgo(lower, now); ok && agoRe.MatchString(lower) {
		return t, true
	}
	return time.Time{}, false
}
