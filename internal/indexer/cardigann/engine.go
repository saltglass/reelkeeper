package cardigann

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/text/encoding/htmlindex"

	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/indexer/newznab"
)

// HTTP is how the engine reaches the tracker (see core.HTTP).
type HTTP = core.HTTP

// Engine runs one tracker's definition with the user's settings.
type Engine struct {
	Def *Definition
	// Settings are the user's values by setting name (secrets included):
	// text as typed, a select's chosen key, a checkbox "true"/"false".
	Settings map[string]string
	// BaseURL is the tracker address to use; empty means the
	// definition's first link.
	BaseURL string
	HTTP    HTTP
	Now     func() time.Time

	loggedIn bool
}

// Query is the shared search type.
type Query = core.Query

// Result is the shared release type.
type Result = core.Release

// ErrLoginFailed means the tracker refused the login.
var ErrLoginFailed = errors.New("login failed")

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

func (e *Engine) base() (*url.URL, error) {
	b := e.BaseURL
	if b == "" {
		b = e.Def.Links[0]
	}
	if !strings.HasSuffix(b, "/") {
		b += "/"
	}
	return url.Parse(b)
}

// resolve turns a path from the definition or a page into an absolute URL.
func (e *Engine) resolve(path string, against *url.URL) (*url.URL, error) {
	if against == nil {
		b, err := e.base()
		if err != nil {
			return nil, err
		}
		against = b
	}
	u, err := url.Parse(strings.TrimSpace(path))
	if err != nil {
		return nil, fmt.Errorf("bad link %q: %w", path, err)
	}
	return against.ResolveReference(u), nil
}

// baseVars are the variables every template sees.
func (e *Engine) baseVars() Vars {
	cfg := map[string]any{}
	for _, s := range e.Def.Settings {
		val, ok := e.Settings[s.Name]
		if !ok {
			cfg[s.Name] = s.SettingDefault()
			continue
		}
		if s.Type == "checkbox" {
			cfg[s.Name] = checkbox(strings.EqualFold(val, "true") || val == CheckboxOn)
		} else {
			cfg[s.Name] = val
		}
	}
	b, _ := e.base()
	if b != nil {
		cfg["sitelink"] = b.String()
	}
	return Vars{
		"Config": cfg,
		"True":   True,
		"False":  nil,
		"Today":  map[string]any{"Year": strconv.Itoa(e.now().Year())},
	}
}

// queryVars adds a search's own variables. Unset values are nil, as in
// Prowlarr, so "eq .Query.IMDBID .False" means "no IMDb ID".
func (e *Engine) queryVars(q Query) Vars {
	v := e.baseVars()
	str := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	num := func(n int) any {
		if n == 0 {
			return nil
		}
		return strconv.Itoa(n)
	}
	query := map[string]any{
		"Type": q.Type, "Q": str(q.Keywords), "Categories": catStrings(q.Categories),
		"Year": num(q.Year), "IMDBID": str(q.IMDbID), "IMDBIDShort": str(strings.TrimPrefix(q.IMDbID, "tt")),
		"TMDBID": num(q.TMDBID), "TVDBID": num(q.TVDBID), "Season": num(q.Season), "Ep": str(q.Episode),
	}
	if q.Season > 0 || q.Episode != "" {
		query["Episode"] = episodeString(q.Season, q.Episode)
	}
	// The keywords sent to the tracker: title, year, episode.
	var words []string
	for _, k := range []string{"Q", "Year"} {
		if s, ok := query[k].(string); ok && strings.TrimSpace(s) != "" {
			words = append(words, s)
		}
	}
	if q.Type == "tv-search" {
		words = slices.DeleteFunc(words, func(w string) bool { return w == query["Year"] })
	}
	if s, ok := query["Episode"].(string); ok && s != "" {
		words = append(words, s)
	}
	query["Keywords"] = strings.Join(words, " ")
	v["Query"] = query
	return v
}

// episodeString is how trackers want an episode searched: "S02E05",
// "S02" for a whole season, or a daily show's date as given.
func episodeString(season int, ep string) string {
	if strings.ContainsAny(ep, "/-.") { // a date
		return ep
	}
	s := fmt.Sprintf("S%02d", season)
	if n, err := strconv.Atoi(ep); err == nil {
		s += fmt.Sprintf("E%02d", n)
	}
	return s
}

func catStrings(ids []int) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.Itoa(id))
	}
	return out
}

// --- categories --------------------------------------------------------------

// trackerCats maps standard categories to the tracker's own IDs. With
// none asked for, it's the definition's default categories (or all).
func (e *Engine) trackerCats(want []int) []string {
	var out []string
	add := func(id string) {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	for _, m := range e.mappings() {
		if len(want) == 0 {
			if m.Default {
				add(m.ID)
			}
			continue
		}
		if newznab.Within(newznab.ID(m.Cat), want) {
			add(m.ID)
		}
	}
	if len(want) == 0 && len(out) == 0 {
		for _, m := range e.mappings() {
			add(m.ID)
		}
	}
	return out
}

// mappings lists the category mappings, including the older "categories"
// form (tracker ID → standard name).
func (e *Engine) mappings() []CategoryMapping {
	out := slices.Clone(e.Def.Caps.CategoryMappings)
	for id, name := range e.Def.Caps.Categories {
		out = append(out, CategoryMapping{ID: id, Cat: name})
	}
	return out
}

// standardCats maps a tracker category (by ID, or by description) to
// standard IDs.
func (e *Engine) standardCats(value string, byDesc bool) []int {
	var out []int
	for _, m := range e.mappings() {
		key := m.ID
		if byDesc {
			key = m.Desc
		}
		if strings.EqualFold(strings.TrimSpace(value), key) {
			if id := newznab.ID(m.Cat); id != 0 && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

// --- requests ----------------------------------------------------------------

// page is a fetched response: status, headers, final URL (after any
// redirects) and the body, decoded from the tracker's character set.
type page struct {
	status int
	header http.Header
	url    *url.URL
	body   []byte
}

// fetch sends a request and reads the whole answer.
func (e *Engine) fetch(ctx context.Context, method string, u *url.URL, form url.Values, headers http.Header, follow bool) (*page, error) {
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(e.encode(form))
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if method == http.MethodPost && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := e.HTTP.Do(req, follow)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("reading the answer: %w", err)
	}
	final := u
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL
	}
	return &page{status: resp.StatusCode, header: resp.Header, url: final, body: e.decode(data)}, nil
}

// encode writes form values in the tracker's character set.
func (e *Engine) encode(form url.Values) string {
	enc, err := htmlindex.Get(e.Def.Encoding)
	if e.Def.Encoding == "" || err != nil || strings.EqualFold(e.Def.Encoding, "utf-8") {
		return form.Encode()
	}
	converted := url.Values{}
	for k, vs := range form {
		for _, v := range vs {
			if s, err := enc.NewEncoder().String(v); err == nil {
				v = s
			}
			converted.Add(k, v)
		}
	}
	return converted.Encode()
}

func (e *Engine) decode(b []byte) []byte {
	if e.Def.Encoding == "" || strings.EqualFold(e.Def.Encoding, "utf-8") {
		return b
	}
	enc, err := htmlindex.Get(e.Def.Encoding)
	if err != nil {
		return b
	}
	out, err := enc.NewDecoder().Bytes(b)
	if err != nil {
		return b
	}
	return out
}

func (e *Engine) headers(h OrderedHeaders, v Vars) (http.Header, error) {
	out := http.Header{}
	for _, kv := range h {
		for _, val := range kv.Value {
			s, err := Execute(val, v)
			if err != nil {
				return nil, err
			}
			out.Add(kv.Key, s)
		}
	}
	return out, nil
}

// --- searching ---------------------------------------------------------------

// request is one prepared search request.
type request struct {
	method   string
	u        *url.URL
	form     url.Values
	path     SearchPath
	vars     Vars
	follow   bool
	response string // "", "json", "xml"
}

// Search runs a search and returns what the tracker found. Rows that
// can't be read are skipped; the error is only for a search that failed
// as a whole.
//
// Engine is a core.Client.
func (e *Engine) Search(ctx context.Context, q Query) ([]Result, error) {
	if err := e.ensureLogin(ctx); err != nil {
		return nil, err
	}
	reqs, err := e.requests(q)
	if err != nil {
		return nil, err
	}
	var out []Result
	seen := map[string]bool{}
	for _, r := range reqs {
		results, err := e.run(ctx, r, true)
		if err != nil {
			return nil, err
		}
		for _, res := range results {
			key := res.GUID
			if key == "" {
				key = res.DetailsURL + res.DownloadURL + res.MagnetURL
			}
			if !seen[key] {
				seen[key] = true
				out = append(out, res)
			}
		}
	}
	return out, nil
}

// requests prepares a search's requests, one per matching search path.
func (e *Engine) requests(q Query) ([]request, error) {
	v := e.queryVars(q)
	query := v["Query"].(map[string]any)
	kw, err := applyFilters(text(query["Keywords"]), e.Def.Search.KeywordsFilters, v, e.now())
	if err != nil {
		return nil, fmt.Errorf("keywords: %w", err)
	}
	v["Keywords"] = kw
	cats := e.trackerCats(q.Categories)

	var out []request
	seen := map[string]bool{}
	for _, p := range e.Def.SearchPaths() {
		pv := cloneVars(v)
		pv["Categories"] = cats
		if len(p.Categories) > 0 && len(cats) > 0 {
			negate := p.Categories[0] == "!"
			only := p.Categories
			if negate {
				only = only[1:]
			}
			var inter []string
			for _, c := range cats {
				if slices.Contains(only, c) {
					inter = append(inter, c)
				}
			}
			if (len(inter) > 0) == negate {
				continue
			}
			if !negate {
				pv["Categories"] = inter
			}
		}
		path, err := ExecuteEscaped(p.Path, pv, pathEscape)
		if err != nil {
			return nil, fmt.Errorf("search path: %w", err)
		}
		u, err := e.resolve(path, nil)
		if err != nil {
			return nil, err
		}
		params := url.Values{}
		var order []string // keep inputs in the definition's order
		addParam := func(k, val string) {
			if _, ok := params[k]; !ok {
				order = append(order, k)
			}
			params.Add(k, val)
		}
		var inputs []OrderedStrings
		if p.InheritInputs == nil || *p.InheritInputs {
			inputs = append(inputs, e.Def.Search.Inputs)
		}
		inputs = append(inputs, p.Inputs)
		for _, set := range inputs {
			for _, kv := range set {
				if kv.Key == "$raw" {
					raw, err := ExecuteEscaped(kv.Value, pv, url.QueryEscape)
					if err != nil {
						return nil, err
					}
					for _, part := range strings.Split(raw, "&") {
						k, val, _ := strings.Cut(part, "=")
						if k == "" {
							continue
						}
						ku, _ := url.QueryUnescape(k)
						vu, _ := url.QueryUnescape(val)
						addParam(ku, vu)
					}
					continue
				}
				val, err := Execute(kv.Value, pv)
				if err != nil {
					return nil, fmt.Errorf("input %s: %w", kv.Key, err)
				}
				if strings.TrimSpace(val) != "" || e.Def.Search.AllowEmptyInputs {
					addParam(kv.Key, val)
				}
			}
		}
		method := http.MethodGet
		if strings.EqualFold(p.Method, "post") {
			method = http.MethodPost
		}
		r := request{method: method, u: u, path: p, vars: pv, follow: p.FollowRedirect}
		if p.Response != nil {
			r.response = p.Response.Type
		}
		if method == http.MethodGet && len(params) > 0 {
			q := u.Query()
			for _, k := range order {
				for _, val := range params[k] {
					q.Add(k, val)
				}
			}
			u.RawQuery = e.encode(q)
		} else {
			r.form = params
		}
		key := method + " " + u.String()
		if method == http.MethodGet && seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out, nil
}

// pathEscape escapes a value for a URL path the way Prowlarr does
// (query escaping, with spaces as %20).
func pathEscape(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }

func cloneVars(v Vars) Vars {
	out := make(Vars, len(v))
	for k, x := range v {
		out[k] = x
	}
	return out
}

// run sends one search request and reads its results. If the answer
// shows we were logged out, it logs in again and retries once.
func (e *Engine) run(ctx context.Context, r request, retry bool) ([]Result, error) {
	h, err := e.headers(e.Def.Search.Headers, r.vars)
	if err != nil {
		return nil, err
	}
	resp, err := e.fetch(ctx, r.method, r.u, r.form, h, r.follow)
	if err != nil {
		return nil, err
	}
	if e.loginNeeded(resp) {
		if !retry || e.Def.Login == nil {
			return nil, fmt.Errorf("%w: the tracker still asks to log in", ErrLoginFailed)
		}
		e.loggedIn = false
		if err := e.ensureLogin(ctx); err != nil {
			return nil, err
		}
		return e.run(ctx, r, false)
	}
	if resp.status >= 400 {
		return nil, fmt.Errorf("the tracker answered HTTP %d", resp.status)
	}
	if loc := resp.header.Get("Location"); resp.status >= 300 && loc != "" {
		return nil, fmt.Errorf("the tracker redirected the search to %s; check the tracker address", loc)
	}
	text, err := applyFilters(string(resp.body), e.Def.Search.PreprocessingFilters, r.vars, e.now())
	if err != nil {
		return nil, fmt.Errorf("preprocessing: %w", err)
	}
	if r.path.Response != nil && r.path.Response.NoResultsMessage != "" && strings.Contains(text, r.path.Response.NoResultsMessage) {
		return nil, nil
	}
	switch r.response {
	case "json":
		return e.readJSON(text, r)
	case "xml":
		return e.readMarkup(text, r, true)
	}
	return e.readMarkup(text, r, false)
}

// loginNeeded: with a login block, a redirect, an HTTP error, or an HTML
// page without the login test's selector means the session expired.
func (e *Engine) loginNeeded(resp *page) bool {
	if e.Def.Login == nil || e.Def.Login.Method == "cookie" && e.Def.Login.Test == nil {
		return false
	}
	if resp.status >= 300 {
		return true
	}
	t := e.Def.Login.Test
	ct := resp.header.Get("Content-Type")
	if t != nil && t.Selector != "" && (ct == "" || strings.Contains(ct, "text/html")) {
		doc, err := goquery.NewDocumentFromReader(bytes.NewReader(resp.body))
		return err == nil && doc.Find(t.Selector).Length() == 0
	}
	return false
}

// pageError returns the message of the first matching error block.
func pageError(doc *goquery.Selection, blocks []ErrorBlock, v Vars, now time.Time) string {
	for _, b := range blocks {
		if b.Selector == "" || doc.Find(b.Selector).Length() == 0 {
			continue
		}
		if b.Message != nil {
			if msg, err := htmlValue(doc, *b.Message, v, now); err == nil && msg != "" {
				return msg
			}
		}
		return strings.TrimSpace(doc.Find(b.Selector).First().Text())
	}
	return ""
}

// readMarkup reads HTML (or XML) results.
func (e *Engine) readMarkup(page string, r request, isXML bool) ([]Result, error) {
	doc, err := parseMarkup(page, isXML)
	if err != nil {
		return nil, fmt.Errorf("reading the results page: %w", err)
	}
	root := doc.Selection
	if msg := pageError(root, e.Def.Search.Error, r.vars, e.now()); msg != "" {
		return nil, fmt.Errorf("the tracker said: %s", msg)
	}
	sel, err := Execute(e.Def.Search.Rows.Selector.Selector, r.vars)
	if err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	var rows []*goquery.Selection
	root.Find(sel).Each(func(_ int, s *goquery.Selection) { rows = append(rows, s) })

	// "after: N": a result spans N+1 rows; fold the extra rows into the
	// first.
	if after := e.Def.Search.Rows.After; after > 0 {
		var merged []*goquery.Selection
		for i := 0; i < len(rows); i += after + 1 {
			row := rows[i]
			for j := 1; j <= after && i+j < len(rows); j++ {
				row.AppendSelection(rows[i+j].Children())
			}
			merged = append(merged, row)
		}
		rows = merged
	}

	var out []Result
	for _, row := range rows {
		res, ok := e.readRow(r, func(s Selector, v Vars) (string, error) { return htmlValue(row, s, v, e.now()) })
		if !ok {
			continue
		}
		if res.PublishDate.IsZero() && e.Def.Search.Rows.DateHeaders != nil {
			res.PublishDate = e.dateHeader(row, *e.Def.Search.Rows.DateHeaders, r.vars)
		}
		out = append(out, res)
	}
	return e.finish(out), nil
}

// dateHeader finds the date from the nearest header row above a result.
func (e *Engine) dateHeader(row *goquery.Selection, s Selector, v Vars) time.Time {
	prev := row.Prev()
	if prev.Length() == 0 {
		prev = row.Parent().Prev()
	}
	for prev.Length() > 0 {
		if val, err := htmlValue(prev, s, v, e.now()); err == nil && val != "" {
			if t, ok := parseFuzzyTime(val, e.now()); ok {
				return t
			}
		}
		cur := prev
		prev = cur.Prev()
		if prev.Length() == 0 {
			prev = cur.Parent().Prev()
		}
	}
	return time.Time{}
}

// jsonRow is a JSON result row; parent is set for grouped results, where
// selectors starting with ".." read the group.
type jsonRow struct {
	obj    any
	parent any
}

// readJSON reads JSON results.
func (e *Engine) readJSON(page string, r request) ([]Result, error) {
	dec := json.NewDecoder(strings.NewReader(page))
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("the tracker didn't answer with JSON: %w", err)
	}
	rowsSel := e.Def.Search.Rows
	var rows []jsonRow
	if rowsSel.Multiple {
		// Grouped results: each row's attribute holds the releases.
		groupSel := rowsSel
		groupSel.Attribute, groupSel.Multiple = "", false
		groups, err := jsonRows(doc, groupSel, r.vars)
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			inner, _ := jsonPath(g, rowsSel.Attribute)
			list, _ := inner.([]any)
			for _, item := range list {
				rows = append(rows, jsonRow{obj: item, parent: g})
			}
		}
	} else {
		objs, err := jsonRows(doc, rowsSel, r.vars)
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			rows = append(rows, jsonRow{obj: o})
		}
	}
	var out []Result
	for _, row := range rows {
		res, ok := e.readRow(r, func(s Selector, v Vars) (string, error) {
			target := row.obj
			if strings.HasPrefix(s.Selector, "..") && row.parent != nil {
				target = row.parent
				s.Selector = strings.TrimPrefix(s.Selector, "..")
			}
			return jsonValue(target, s, v, e.now())
		})
		if ok {
			out = append(out, res)
		}
	}
	return e.finish(out), nil
}

// Fields that may be missing without dropping the row (Prowlarr's list).
var alwaysOptional = []string{"imdb", "imdbid", "tmdbid", "rageid", "tvdbid", "tvmazeid", "traktid", "doubanid", "poster", "banner", "description", "genre"}

// readRow reads one row's fields in definition order (later fields can use
// earlier ones as .Result.name) and builds a result. A required field that
// can't be read drops the row.
func (e *Engine) readRow(r request, value func(Selector, Vars) (string, error)) (Result, bool) {
	res := Result{DownloadFactor: 1, UploadFactor: 1}
	resultVars := map[string]any{}
	v := cloneVars(r.vars)
	v["Result"] = resultVars
	base := r.u
	for _, kv := range e.Def.Search.Fields {
		parts := strings.Split(kv.Key, "|")
		name, mods := parts[0], parts[1:]
		optional := kv.Value.Optional || slices.Contains(mods, "optional") || slices.Contains(alwaysOptional, name)
		val, err := value(kv.Value, v)
		if (err != nil || strings.TrimSpace(val) == "") && optional {
			def := ""
			if kv.Value.Default != nil {
				def, _ = Execute(*kv.Value.Default, v)
			}
			if strings.TrimSpace(def) == "" {
				resultVars[name] = nil
				continue
			}
			val, err = def, nil
		}
		if err != nil {
			if kv.Value.Default != nil {
				if def, derr := Execute(*kv.Value.Default, v); derr == nil {
					val, err = def, nil
				}
			}
			if err != nil {
				return Result{}, false
			}
		}
		stored, err := e.setField(&res, name, mods, strings.TrimSpace(val), base)
		if err != nil {
			if optional {
				resultVars[name] = nil
				continue
			}
			return Result{}, false
		}
		resultVars[name] = stored
	}
	if res.Title == "" || res.DownloadURL == "" && res.MagnetURL == "" && res.InfoHash == "" && res.DetailsURL == "" {
		return Result{}, false
	}
	return res, true
}

// setField stores a field's value in the result and returns the value
// later templates see (as Prowlarr does: URLs made absolute, numbers
// normalised).
func (e *Engine) setField(r *Result, name string, mods []string, val string, base *url.URL) (any, error) {
	abs := func(s string) (string, error) {
		u, err := e.resolve(s, base)
		if err != nil {
			return "", err
		}
		return u.String(), nil
	}
	switch name {
	case "download":
		if val == "" {
			return nil, nil
		}
		if strings.HasPrefix(val, "magnet:") {
			r.MagnetURL = val
		} else {
			u, err := abs(val)
			if err != nil {
				return nil, err
			}
			r.DownloadURL = u
			val = u
		}
		r.GUID = val
	case "magnet":
		r.MagnetURL = val
	case "infohash":
		r.InfoHash = strings.ToLower(val)
	case "details", "comments":
		u, err := abs(val)
		if err != nil {
			return nil, err
		}
		if name == "details" || r.DetailsURL == "" {
			r.DetailsURL = u
		}
		val = u
	case "title":
		if slices.Contains(mods, "append") {
			r.Title += val
		} else {
			r.Title = val
		}
		val = r.Title
	case "description":
		if slices.Contains(mods, "append") {
			r.Description += val
		} else {
			r.Description = val
		}
		val = r.Description
	case "category", "categorydesc":
		cats := e.standardCats(val, name == "categorydesc")
		for _, c := range cats {
			if !slices.Contains(r.Categories, c) {
				r.Categories = append(r.Categories, c)
			}
		}
		val = strings.Join(catStrings(r.Categories), ",")
	case "size":
		n, err := parseSize(val)
		if err != nil {
			return nil, err
		}
		r.Size = n
		val = strconv.FormatInt(n, 10)
	case "seeders", "leechers", "files", "grabs":
		n, err := parseCount(val)
		if err != nil {
			return nil, err
		}
		if n >= 5000000 { // nonsense some sites show; Prowlarr does the same
			n = 0
		}
		switch name {
		case "seeders":
			r.Seeders = n
		case "leechers":
			r.Leechers = n
		case "files":
			r.Files = n
		case "grabs":
			r.Grabs = n
		}
		val = strconv.Itoa(n)
	case "date", "publishdate":
		t, ok := parseFuzzyTime(val, e.now())
		if !ok {
			return nil, fmt.Errorf("can't read the date %q", val)
		}
		r.PublishDate = t
		val = t.Format(time.RFC1123Z)
	case "downloadvolumefactor", "uploadvolumefactor", "minimumratio":
		f, err := parseFloat(val)
		if err != nil {
			return nil, err
		}
		switch name {
		case "downloadvolumefactor":
			r.DownloadFactor = f
		case "uploadvolumefactor":
			r.UploadFactor = f
		default:
			r.MinimumRatio = f
		}
		val = strconv.FormatFloat(f, 'f', -1, 64)
	case "minimumseedtime":
		f, err := parseFloat(val)
		if err != nil {
			return nil, err
		}
		r.MinimumSeedTime = int64(f)
		val = strconv.FormatInt(r.MinimumSeedTime, 10)
	case "imdb", "imdbid", "tmdbid", "tvdbid", "year":
		n := firstNumber(val)
		switch name {
		case "imdb", "imdbid":
			r.IMDbID = n
		case "tmdbid":
			r.TMDBID = n
		case "tvdbid":
			r.TVDBID = n
		case "year":
			r.Year = n
		}
		val = strconv.Itoa(n)
	case "poster":
		if val != "" {
			u, err := abs(val)
			if err != nil {
				return nil, err
			}
			r.PosterURL, val = u, u
		}
	case "genre":
		r.Genres = val
	}
	return val, nil
}

// finish fills in what the fields imply: magnet links from info hashes
// (public trackers only), hashes from magnet links.
func (e *Engine) finish(rs []Result) []Result {
	for i := range rs {
		r := &rs[i]
		if r.MagnetURL == "" && r.InfoHash != "" && e.Def.Type != "private" {
			r.MagnetURL = "magnet:?xt=urn:btih:" + r.InfoHash + "&dn=" + url.QueryEscape(r.Title)
		}
		if r.InfoHash == "" && r.MagnetURL != "" {
			if u, err := url.Parse(r.MagnetURL); err == nil {
				for _, xt := range u.Query()["xt"] {
					if h, ok := strings.CutPrefix(strings.ToLower(xt), "urn:btih:"); ok {
						r.InfoHash = h
					}
				}
			}
		}
		if r.GUID == "" {
			r.GUID = firstNonEmpty(r.DetailsURL, r.DownloadURL, r.MagnetURL, r.InfoHash)
		}
		if len(r.Categories) == 0 {
			r.Categories = []int{}
		}
	}
	return rs
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// --- numbers -----------------------------------------------------------------

// normalizeNumber turns "1,234.5", "1.234,5" and "1 234" into "1234.5".
func normalizeNumber(s string, integer bool) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || r == '.' || r == ',' || r == '-' {
			return r
		}
		return -1
	}, s)
	if integer {
		// Thousands separators only: "1,234" and "1.234" are 1234.
		return strings.NewReplacer(",", "", ".", "").Replace(s)
	}
	if strings.Contains(s, ",") && strings.Contains(s, ".") {
		if strings.LastIndex(s, ",") > strings.LastIndex(s, ".") {
			s = strings.ReplaceAll(strings.ReplaceAll(s, ".", ""), ",", ".") // 1.234,5
		} else {
			s = strings.ReplaceAll(s, ",", "") // 1,234.5
		}
	} else if strings.Count(s, ",") == 1 && len(s)-strings.Index(s, ",")-1 != 3 {
		s = strings.Replace(s, ",", ".", 1) // 1,5
	} else {
		s = strings.ReplaceAll(s, ",", "")
	}
	return s
}

func parseFloat(s string) (float64, error) {
	n := normalizeNumber(s, false)
	if n == "" {
		return 0, fmt.Errorf("not a number: %q", s)
	}
	return strconv.ParseFloat(n, 64)
}

func parseCount(s string) (int, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	n := normalizeNumber(s, true)
	if strings.Trim(n, "-") == "" {
		return 0, nil // "N/A", "-"
	}
	v, err := strconv.Atoi(n)
	if err != nil {
		return 0, fmt.Errorf("not a number: %q", s)
	}
	return v, nil
}

// parseSize reads "1.4 GB", "700 MiB", "1,234.5 MB" or plain bytes.
func parseSize(s string) (int64, error) {
	unit := strings.ToLower(strings.ReplaceAll(strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return -1
	}, s), "i", ""))
	f, err := parseFloat(s)
	if err != nil {
		return 0, err
	}
	mult := 1.0
	switch {
	case strings.Contains(unit, "kb"):
		mult = 1 << 10
	case strings.Contains(unit, "mb"):
		mult = 1 << 20
	case strings.Contains(unit, "gb"):
		mult = 1 << 30
	case strings.Contains(unit, "tb"):
		mult = 1 << 40
	}
	return int64(f * mult), nil
}

// firstNumber returns the first run of digits as a number: "tt0111161" → 111161.
func firstNumber(s string) int {
	start := strings.IndexAny(s, "0123456789")
	if start < 0 {
		return 0
	}
	end := start
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, _ := strconv.Atoi(s[start:end])
	return n
}

// Test logs in (if the tracker needs it) and browses the newest releases,
// which must find something; a definition that reads nothing from a
// working site is out of date.
func (e *Engine) Test(ctx context.Context) error {
	e.loggedIn = false
	res, err := e.Search(ctx, Query{Type: "search"})
	if err != nil {
		return err
	}
	if len(res) == 0 {
		return fmt.Errorf("the tracker answered, but Reelkeeper found no releases on its page; the definition may be out of date")
	}
	return nil
}

var _ core.Client = (*Engine)(nil)

// Session is the connection the indexer uses (cookies, request spacing,
// solver), so a release's .torrent can be fetched the same way.
func (e *Engine) Session() core.HTTP { return e.HTTP }
