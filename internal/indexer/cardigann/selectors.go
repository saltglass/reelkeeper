package cardigann

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Selectors pick values out of a result row. HTML (and XML) rows are
// searched with CSS selectors; JSON rows with dotted paths ("files[0].name",
// "meta.poster", "$" for the row itself).

// errNotFound means a selector matched nothing.
var errNotFound = errors.New("not found")

// --- HTML ------------------------------------------------------------------

// htmlValue reads one selector block from an HTML element.
func htmlValue(row *goquery.Selection, s Selector, v Vars, now time.Time) (string, error) {
	if s.Text != nil {
		out, err := Execute(*s.Text, v)
		if err != nil {
			return "", err
		}
		return applyFilters(out, s.Filters, v, now)
	}
	sel, err := Execute(s.Selector, v)
	if err != nil {
		return "", err
	}
	el := row
	switch {
	case sel == "":
	case strings.HasPrefix(sel, ":root"):
		// ":root …" searches the whole page, not just the row.
		root := row
		for root.Parent().Length() > 0 {
			root = root.Parent()
		}
		rest := strings.TrimSpace(strings.TrimPrefix(sel, ":root"))
		if rest == "" {
			el = root
		} else {
			el = root.Find(rest).First()
		}
	default:
		el = row.Find(sel).First()
		if el.Length() == 0 && row.Is(sel) {
			el = row // the row itself matches
		}
	}
	if el.Length() == 0 {
		return "", errNotFound
	}
	if s.Remove != "" {
		el = el.Clone()
		el.Find(s.Remove).Remove()
	}
	var value string
	switch {
	case len(s.Case) > 0:
		found := false
		for _, kv := range s.Case {
			if kv.Key == "*" || el.Is(kv.Key) || el.Find(kv.Key).Length() > 0 {
				if value, err = Execute(kv.Value, v); err != nil {
					return "", err
				}
				found = true
				break
			}
		}
		if !found {
			return "", errNotFound
		}
	case s.Attribute != "":
		a, ok := el.Attr(s.Attribute)
		if !ok {
			return "", errNotFound
		}
		value = a
	default:
		value = strings.TrimSpace(el.Text())
	}
	return applyFilters(value, s.Filters, v, now)
}

// --- JSON ------------------------------------------------------------------

// jsonValue reads one selector block from a JSON row.
func jsonValue(row any, s Selector, v Vars, now time.Time) (string, error) {
	if s.Text != nil {
		out, err := Execute(*s.Text, v)
		if err != nil {
			return "", err
		}
		return applyFilters(out, s.Filters, v, now)
	}
	sel, err := Execute(s.Selector, v)
	if err != nil {
		return "", err
	}
	val, ok := jsonPath(row, sel)
	if !ok || val == nil {
		return "", errNotFound
	}
	value := jsonText(val)
	if len(s.Case) > 0 {
		found := false
		for _, kv := range s.Case {
			if kv.Key == "*" || kv.Key == value {
				if value, err = Execute(kv.Value, v); err != nil {
					return "", err
				}
				found = true
				break
			}
		}
		if !found {
			return "", errNotFound
		}
	}
	return applyFilters(value, s.Filters, v, now)
}

// jsonPath follows a dotted path with optional [index] parts: "data",
// "files[0].name", "$" or "$.numFound".
func jsonPath(doc any, path string) (any, bool) {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimPrefix(path, ".")
	cur := doc
	if path == "" {
		return cur, true
	}
	for _, part := range splitPath(path) {
		name, idx := part, -1
		if i := strings.IndexByte(part, '['); i >= 0 && strings.HasSuffix(part, "]") {
			name = part[:i]
			inner := strings.Trim(part[i+1:len(part)-1], `'"`)
			if n, err := strconv.Atoi(inner); err == nil {
				idx = n
			} else {
				name = inner // ['key'] form
				if i > 0 {
					m, ok := cur.(map[string]any)
					if !ok {
						return nil, false
					}
					cur = m[part[:i]]
				}
			}
		}
		if name != "" {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			v, ok := m[name]
			if !ok {
				return nil, false
			}
			cur = v
		}
		if idx >= 0 {
			a, ok := cur.([]any)
			if !ok || idx >= len(a) {
				return nil, false
			}
			cur = a[idx]
		}
	}
	return cur, true
}

// splitPath splits on dots outside brackets.
func splitPath(p string) []string {
	var parts []string
	depth, start := 0, 0
	for i, c := range p {
		switch c {
		case '[':
			depth++
		case ']':
			depth--
		case '.':
			if depth == 0 {
				parts = append(parts, p[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, p[start:])
}

// jsonText writes a JSON value the way .NET's JSON library prints it,
// which is what definitions compare against: true is "True", 3 is "3".
func jsonText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		// .NET prints booleans as True/False; definitions' case blocks
		// match those words.
		if x {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// jsonRows selects the result rows from a JSON document. The selector is a
// path, optionally followed by filters: "data:not(free:contains(0))",
// "$:has(username:contains(bob))".
func jsonRows(doc any, rows Rows, v Vars) ([]any, error) {
	sel, err := Execute(rows.Selector.Selector, v)
	if err != nil {
		return nil, err
	}
	path, pseudo := splitPseudo(sel)
	val, ok := jsonPath(doc, path)
	if !ok || val == nil {
		if rows.MissingAttributeEqualsNoResults {
			return nil, nil
		}
		return nil, fmt.Errorf("no %q in the response", path)
	}
	var list []any
	switch x := val.(type) {
	case []any:
		list = x
	case map[string]any:
		list = []any{x}
	default:
		return nil, fmt.Errorf("%q isn't a list", path)
	}
	var out []any
	for _, el := range list {
		keep, err := matchPseudo(el, pseudo)
		if err != nil {
			return nil, err
		}
		if !keep {
			continue
		}
		if rows.Attribute == "" {
			out = append(out, el)
			continue
		}
		inner, ok := jsonPath(el, rows.Attribute)
		if !ok {
			if rows.MissingAttributeEqualsNoResults {
				continue
			}
			return nil, fmt.Errorf("a row has no %q", rows.Attribute)
		}
		if !rows.Multiple {
			out = append(out, inner)
			continue
		}
		// Grouped results ("a movie with its torrents"): one row per
		// child, which also sees its parent's fields.
		children, _ := inner.([]any)
		parent, _ := el.(map[string]any)
		for _, c := range children {
			merged := map[string]any{}
			for k, pv := range parent {
				if k != rows.Attribute {
					merged[k] = pv
				}
			}
			if cm, ok := c.(map[string]any); ok {
				for k, cv := range cm {
					merged[k] = cv
				}
			}
			out = append(out, merged)
		}
	}
	return out, nil
}

// splitPseudo separates "data:not(x:contains(1))" into "data" and
// ":not(x:contains(1))".
func splitPseudo(sel string) (string, string) {
	depth := 0
	for i, c := range sel {
		switch c {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ':':
			if depth == 0 {
				return sel[:i], sel[i:]
			}
		}
	}
	return sel, ""
}

// matchPseudo applies a chain of :has(), :not() and :contains() filters
// to one JSON row.
func matchPseudo(el any, pseudo string) (bool, error) {
	for pseudo != "" {
		name, arg, rest, err := nextPseudo(pseudo)
		if err != nil {
			return false, err
		}
		var ok bool
		switch name {
		case "has":
			ok = jsonHas(el, arg)
		case "not":
			ok = !jsonHas(el, arg)
		case "contains":
			ok = strings.Contains(jsonText(el), strings.Trim(arg, `"'`))
		default:
			return false, fmt.Errorf("unknown JSON filter :%s", name)
		}
		if !ok {
			return false, nil
		}
		pseudo = rest
	}
	return true, nil
}

// jsonHas: "path" exists (and isn't empty), or "path:contains(text)".
func jsonHas(el any, sub string) bool {
	path, pseudo := splitPseudo(sub)
	val, ok := jsonPath(el, path)
	if !ok || val == nil {
		return false
	}
	if pseudo == "" {
		return truthy(jsonText(val))
	}
	keep, err := matchPseudo(val, pseudo)
	return err == nil && keep
}

// nextPseudo splits ":not(a:contains(1)):has(b)" into "not", "a:contains(1)"
// and ":has(b)".
func nextPseudo(s string) (name, arg, rest string, err error) {
	if !strings.HasPrefix(s, ":") {
		return "", "", "", fmt.Errorf("can't read %q", s)
	}
	open := strings.IndexByte(s, '(')
	if open < 0 {
		return "", "", "", fmt.Errorf("can't read %q", s)
	}
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[1:open], s[open+1 : i], s[i+1:], nil
			}
		}
	}
	return "", "", "", fmt.Errorf("unbalanced parentheses in %q", s)
}
