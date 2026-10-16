package cardigann

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Templates in tracker definitions look like Go templates:
//
//	search/{{ .Keywords }}/{{ if .Config.freeleech }}1{{ else }}0{{ end }}/
//	{{ range .Categories }}filter_cat[{{.}}]=1&{{end}}
//	{{ if and (.Keywords) (eq .Config.sort .False) }}...{{ end }}
//
// But Jackett and Prowlarr, which the definitions are written for, read
// them with their own forgiving reader, and some definitions rely on that:
// 1337x's has a stray ")" that Go's text/template rejects. So this is a
// small reader of its own, tolerant in the same ways:
//
//   - actions: {{ .Path }}, {{ if }}/{{ else }}/{{ else if }}/{{ end }},
//     {{ range .List }} (with {{ . }}, or {{ range $i, $v := .List }})
//   - functions: and, or, not, eq, ne, re_replace, join, len, index
//   - stray closing parentheses are ignored; missing ones are implied
//   - an unknown variable is empty (nil), never an error
//
// Values follow Prowlarr exactly, since that's what definition authors
// test against: .True is "True" and .False is nil; a ticked checkbox is
// ".True" and an unticked one nil; unset query fields are nil. Only nil,
// blank text and empty lists are false (the text "False" is true). eq
// tells nil apart from "".
//
// Errors are only for templates that can't be understood at all.

// Vars are what templates can refer to: .Config, .Keywords, .Query,
// .Categories, .Result, .True, .False, .Today... Values are strings,
// []string, map[string]any, or nil.
type Vars map[string]any

// True is .True's value; .False is nil.
const True = "True"

// CheckboxOn is a ticked checkbox setting's value (Prowlarr's quirk: a
// literal ".True"); an unticked one is nil.
const CheckboxOn = ".True"

// Execute fills in a template.
func Execute(tpl string, v Vars) (string, error) { return ExecuteEscaped(tpl, v, nil) }

// ExecuteEscaped fills in a template, passing every value it prints (not
// the template's own text) through escape: search paths URL-encode the
// keywords this way, as Prowlarr does.
func ExecuteEscaped(tpl string, v Vars, escape func(string) string) (string, error) {
	if !strings.Contains(tpl, "{{") {
		return tpl, nil
	}
	nodes, err := parseTemplate(tpl)
	if err != nil {
		return "", err
	}
	if escape == nil {
		escape = func(s string) string { return s }
	}
	var b strings.Builder
	if err := run(&b, nodes, &scope{vars: v, escape: escape}); err != nil {
		return "", err
	}
	return b.String(), nil
}

// --- parsing ---------------------------------------------------------------

type node interface{}

type textNode string

type exprNode struct{ e expr }

type ifNode struct {
	cond     expr
	then     []node
	elseBody []node
}

type rangeNode struct {
	list     expr
	keyVar   string // "$i" in {{ range $i, $v := .List }}; may be empty
	valueVar string // "$v"; may be empty (then the element is ".")
	body     []node
}

// action is one {{ ... }}, trimmed.
type action struct{ text string }

// lex splits a template into text and actions, applying {{- and -}}
// whitespace trimming.
func lex(tpl string) []any {
	var out []any
	for {
		i := strings.Index(tpl, "{{")
		if i < 0 {
			if tpl != "" {
				out = append(out, tpl)
			}
			return out
		}
		j := strings.Index(tpl[i:], "}}")
		if j < 0 { // an unclosed action is text
			out = append(out, tpl)
			return out
		}
		text, body := tpl[:i], tpl[i+2:i+j]
		rest := tpl[i+j+2:]
		if strings.HasPrefix(body, "-") {
			text = strings.TrimRightFunc(text, unicode.IsSpace)
			body = body[1:]
		}
		if strings.HasSuffix(body, "-") {
			rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
			body = body[:len(body)-1]
		}
		if text != "" {
			out = append(out, text)
		}
		body = strings.TrimSpace(body)
		if !strings.HasPrefix(body, "/*") { // comments vanish
			out = append(out, action{body})
		}
		tpl = rest
	}
}

func parseTemplate(tpl string) ([]node, error) {
	items := lex(tpl)
	nodes, rest, stop, err := parseList(items)
	if err != nil {
		return nil, err
	}
	if stop != "" || len(rest) > 0 {
		// A stray {{ end }} or {{ else }}: ignore it and carry on, as
		// Jackett would print around it.
		more, err := parseTemplate(joinItems(rest))
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, more...)
	}
	return nodes, nil
}

func joinItems(items []any) string {
	var b strings.Builder
	for _, it := range items {
		switch x := it.(type) {
		case string:
			b.WriteString(x)
		case action:
			b.WriteString("{{ " + x.text + " }}")
		}
	}
	return b.String()
}

// parseList parses until an {{ else }}, {{ else if }} or {{ end }}, which
// it reports in stop ("else", "else if <cond>", "end") with the items
// after it.
func parseList(items []any) (nodes []node, rest []any, stop string, err error) {
	for len(items) > 0 {
		it := items[0]
		items = items[1:]
		switch x := it.(type) {
		case string:
			nodes = append(nodes, textNode(x))
		case action:
			word, arg := firstWord(x.text)
			switch word {
			case "end":
				return nodes, items, "end", nil
			case "else":
				return nodes, items, strings.TrimSpace("else " + arg), nil
			case "if":
				n, after, err := parseIf(arg, items)
				if err != nil {
					return nil, nil, "", err
				}
				nodes, items = append(nodes, n), after
			case "range":
				n, after, err := parseRange(arg, items)
				if err != nil {
					return nil, nil, "", err
				}
				nodes, items = append(nodes, n), after
			default:
				e, err := parseExpr(x.text)
				if err != nil {
					return nil, nil, "", err
				}
				nodes = append(nodes, exprNode{e})
			}
		}
	}
	return nodes, nil, "", nil
}

func parseIf(cond string, items []any) (node, []any, error) {
	c, err := parseExpr(cond)
	if err != nil {
		return nil, nil, err
	}
	n := &ifNode{cond: c}
	body, rest, stop, err := parseList(items)
	if err != nil {
		return nil, nil, err
	}
	n.then = body
	switch {
	case stop == "else":
		body, rest2, _, err := parseList(rest)
		if err != nil {
			return nil, nil, err
		}
		n.elseBody, rest = body, rest2
	case strings.HasPrefix(stop, "else if "):
		// {{ else if x }} is an if inside the else, sharing one {{ end }}.
		inner, rest2, err := parseIf(strings.TrimPrefix(stop, "else if "), rest)
		if err != nil {
			return nil, nil, err
		}
		n.elseBody, rest = []node{inner}, rest2
	}
	return n, rest, nil
}

func parseRange(arg string, items []any) (node, []any, error) {
	n := &rangeNode{}
	if i := strings.Index(arg, ":="); i >= 0 {
		vars := strings.Split(arg[:i], ",")
		for k := range vars {
			vars[k] = strings.TrimSpace(vars[k])
		}
		if len(vars) == 2 {
			n.keyVar, n.valueVar = vars[0], vars[1]
		} else {
			n.valueVar = vars[0]
		}
		arg = arg[i+2:]
	}
	e, err := parseExpr(arg)
	if err != nil {
		return nil, nil, err
	}
	n.list = e
	body, rest, stop, err := parseList(items)
	if err != nil {
		return nil, nil, err
	}
	n.body = body
	if stop == "else" { // {{ range }}…{{ else }}…{{ end }}: the else is for empty lists; rare, ignored
		_, rest2, _, err := parseList(rest)
		if err != nil {
			return nil, nil, err
		}
		rest = rest2
	}
	return n, rest, nil
}

func firstWord(s string) (string, string) {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i:])
}

// --- expressions -------------------------------------------------------------

// expr is a value: a field path (.Config.x), a variable ($v, or "." for the
// current range element), a literal, or a function call.
type expr interface{}

type fieldExpr []string // ".Config.sort" → ["Config", "sort"]; "." → nil
type varExpr struct {
	name string
	path []string
}
type literal string
type nilExpr struct{}
type callExpr struct {
	fn   string
	args []expr
}

type token struct {
	kind byte // '(' ')' 's' (string) 'w' (word: field, variable, function, number)
	text string
}

func tokenize(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case ' ', '\t', '\n', '\r':
			i++
		case '(', ')':
			out = append(out, token{kind: c})
			i++
		case '"':
			j := i + 1
			var b strings.Builder
			for ; j < len(s) && s[j] != '"'; j++ {
				if s[j] == '\\' && j+1 < len(s) {
					j++
					switch s[j] {
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					default:
						b.WriteByte(s[j])
					}
					continue
				}
				b.WriteByte(s[j])
			}
			if j >= len(s) {
				return nil, fmt.Errorf("unclosed quote in %q", s)
			}
			out = append(out, token{kind: 's', text: b.String()})
			i = j + 1
		case '`':
			j := strings.IndexByte(s[i+1:], '`')
			if j < 0 {
				return nil, fmt.Errorf("unclosed quote in %q", s)
			}
			out = append(out, token{kind: 's', text: s[i+1 : i+1+j]})
			i += j + 2
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" \t\n\r()\"", rune(s[j])) {
				j++
			}
			out = append(out, token{kind: 'w', text: s[i:j]})
			i = j
		}
	}
	return out, nil
}

func parseExpr(s string) (expr, error) {
	toks, err := tokenize(s)
	if err != nil {
		return nil, err
	}
	p := &exprParser{toks: toks}
	e := p.command()
	// Anything left (stray ")" and the like) is ignored, as Jackett does.
	return e, p.err
}

type exprParser struct {
	toks []token
	pos  int
	err  error
}

func (p *exprParser) peek() *token {
	for p.pos < len(p.toks) && p.toks[p.pos].kind == ')' && p.depth() == 0 {
		p.pos++ // a stray ")" at the top level: skip it
	}
	if p.pos >= len(p.toks) {
		return nil
	}
	return &p.toks[p.pos]
}

// depth counts the parentheses open at the current position.
func (p *exprParser) depth() int {
	d := 0
	for _, t := range p.toks[:p.pos] {
		switch t.kind {
		case '(':
			d++
		case ')':
			if d > 0 {
				d--
			}
		}
	}
	return d
}

// command is a function call ("eq .A .B") or a single term.
func (p *exprParser) command() expr {
	t := p.peek()
	if t == nil {
		return literal("")
	}
	if t.kind == 'w' && !isFunc(t.text) && !strings.ContainsAny(t.text[:1], ".$0123456789-") &&
		p.pos+1 < len(p.toks) && p.toks[p.pos+1].kind != ')' {
		// A word followed by arguments is a function call.
		p.err = fmt.Errorf("unknown template function %q", t.text)
		return literal("")
	}
	if t.kind == 'w' && isFunc(t.text) {
		p.pos++
		c := callExpr{fn: t.text}
		for {
			t := p.peek()
			if t == nil || t.kind == ')' {
				break
			}
			c.args = append(c.args, p.term())
		}
		return c
	}
	return p.term()
}

func (p *exprParser) term() expr {
	t := p.peek()
	if t == nil {
		return literal("")
	}
	p.pos++
	switch t.kind {
	case 's':
		return literal(t.text)
	case '(':
		e := p.command()
		if p.pos < len(p.toks) && p.toks[p.pos].kind == ')' {
			p.pos++
		}
		return e
	}
	w := t.text
	switch {
	case w == ".":
		return fieldExpr(nil)
	case strings.HasPrefix(w, "."):
		return fieldExpr(strings.Split(w[1:], "."))
	case strings.HasPrefix(w, "$"):
		parts := strings.Split(w, ".")
		return varExpr{name: parts[0], path: parts[1:]}
	case w == "true":
		return literal(True)
	case w == "false":
		return nilExpr{}
	}
	return literal(w) // numbers and bare words
}

var funcs = map[string]bool{
	"and": true, "or": true, "not": true, "eq": true, "ne": true,
	"re_replace": true, "join": true, "len": true, "index": true,
}

func isFunc(w string) bool { return funcs[w] }

// --- running -------------------------------------------------------------

type scope struct {
	vars   Vars
	escape func(string) string
	dot    any            // the current range element; nil outside ranges
	locals map[string]any // $i, $v
	parent *scope
}

func (s *scope) local(name string) (any, bool) {
	for sc := s; sc != nil; sc = sc.parent {
		if v, ok := sc.locals[name]; ok {
			return v, true
		}
	}
	return nil, false
}

func run(b *strings.Builder, nodes []node, sc *scope) error {
	for _, n := range nodes {
		switch x := n.(type) {
		case textNode:
			b.WriteString(string(x))
		case exprNode:
			v, err := eval(x.e, sc)
			if err != nil {
				return err
			}
			b.WriteString(sc.escape(text(v)))
		case *ifNode:
			v, err := eval(x.cond, sc)
			if err != nil {
				return err
			}
			body := x.elseBody
			if truthy(v) {
				body = x.then
			}
			if err := run(b, body, sc); err != nil {
				return err
			}
		case *rangeNode:
			v, err := eval(x.list, sc)
			if err != nil {
				return err
			}
			for i, el := range list(v) {
				inner := &scope{vars: sc.vars, escape: sc.escape, dot: el, parent: sc, locals: map[string]any{}}
				if x.keyVar != "" {
					inner.locals[x.keyVar] = strconv.Itoa(i)
				}
				if x.valueVar != "" {
					inner.locals[x.valueVar] = el
				}
				if err := run(b, x.body, inner); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func eval(e expr, sc *scope) (any, error) {
	switch x := e.(type) {
	case literal:
		return string(x), nil
	case nilExpr:
		return nil, nil
	case fieldExpr:
		if len(x) == 0 {
			return sc.dot, nil
		}
		// Inside a range, .Name still means the template's variables
		// (Jackett's reader works that way), unless the element has it.
		if m, ok := sc.dot.(map[string]any); ok {
			if v, ok := lookup(m, x); ok {
				return v, nil
			}
		}
		v, _ := lookup(map[string]any(sc.vars), x)
		return v, nil
	case varExpr:
		v, ok := sc.local(x.name)
		if !ok {
			return nil, nil
		}
		if len(x.path) > 0 {
			m, _ := v.(map[string]any)
			v, _ = lookup(m, x.path)
		}
		return v, nil
	case callExpr:
		return call(x, sc)
	}
	return nil, nil
}

func lookup(m map[string]any, path []string) (any, bool) {
	var cur any = m
	for _, p := range path {
		switch mm := cur.(type) {
		case map[string]any:
			v, ok := mm[p]
			if !ok {
				// Settings names are case-sensitive in definitions, but
				// be forgiving about the first letter (.Query.IMDBID vs .Query.ImdbID).
				for k, vv := range mm {
					if strings.EqualFold(k, p) {
						v, ok = vv, true
						break
					}
				}
			}
			if !ok {
				return nil, false
			}
			cur = v
		case map[string]string:
			v, ok := mm[p]
			if !ok {
				return nil, false
			}
			cur = v
		case Vars:
			return lookup(map[string]any(mm), path)
		default:
			return nil, false
		}
	}
	return cur, true
}

func call(c callExpr, sc *scope) (any, error) {
	args := make([]any, 0, len(c.args))
	for _, a := range c.args {
		v, err := eval(a, sc)
		if err != nil {
			return nil, err
		}
		args = append(args, v)
	}
	switch c.fn {
	case "and":
		for _, a := range args {
			if !truthy(a) {
				return a, nil
			}
		}
		if len(args) == 0 {
			return nil, nil
		}
		return args[len(args)-1], nil
	case "or":
		for _, a := range args {
			if truthy(a) {
				return a, nil
			}
		}
		if len(args) == 0 {
			return nil, nil
		}
		return args[len(args)-1], nil
	case "not":
		if len(args) == 0 || !truthy(args[0]) {
			return True, nil
		}
		return nil, nil
	case "eq", "ne":
		eq := false
		if len(args) >= 2 {
			for _, b := range args[1:] {
				if same(args[0], b) {
					eq = true
				}
			}
		}
		if (c.fn == "eq") == eq {
			return True, nil
		}
		return nil, nil
	case "re_replace":
		if len(args) != 3 {
			return nil, fmt.Errorf("re_replace needs a value, a pattern and a replacement")
		}
		return reReplace(text(args[0]), text(args[1]), text(args[2]))
	case "join":
		if len(args) != 2 {
			return nil, fmt.Errorf("join needs a list and a separator")
		}
		return strings.Join(list(args[0]), text(args[1])), nil
	case "len":
		if len(args) != 1 {
			return "0", nil
		}
		if l, ok := args[0].([]string); ok {
			return strconv.Itoa(len(l)), nil
		}
		return strconv.Itoa(len([]rune(text(args[0])))), nil
	case "index":
		if len(args) != 2 {
			return nil, nil
		}
		switch x := args[0].(type) {
		case []string:
			if i, err := strconv.Atoi(text(args[1])); err == nil && i >= 0 && i < len(x) {
				return x[i], nil
			}
		case map[string]any:
			return x[text(args[1])], nil
		case map[string]string:
			return x[text(args[1])], nil
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unknown template function %q", c.fn)
}

// same compares like Prowlarr's eq: nil equals only nil.
func same(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return text(a) == text(b)
}

// truthy: nil, blank text and empty lists are false.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(x) != ""
	case bool:
		return x
	case []string:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// text writes a value the way templates print it.
func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return True
		}
		return ""
	case []string:
		return strings.Join(x, ",")
	case int:
		return strconv.Itoa(x)
	}
	return fmt.Sprint(v)
}

func list(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case nil:
		return nil
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	}
	return nil
}
