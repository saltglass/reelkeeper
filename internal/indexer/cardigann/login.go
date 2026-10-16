package cardigann

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Logging in. Public trackers have no login block. Private ones log in by:
//
//   - form: load the login page, fill in its form (keeping hidden fields
//     such as CSRF tokens) and submit it
//   - post: post the inputs straight to the login path
//   - get / oneurl: request a URL carrying the credentials (a passkey)
//   - cookie: use a cookie the user copied from their browser
//
// Afterwards the definition's error selectors and login test say whether
// it worked. Captchas can't be solved; such trackers need cookie login.

func (e *Engine) ensureLogin(ctx context.Context) error {
	if e.Def.Login == nil || e.loggedIn {
		return nil
	}
	if err := e.Login(ctx); err != nil {
		return err
	}
	e.loggedIn = true
	return nil
}

// Login signs in to the tracker.
func (e *Engine) Login(ctx context.Context) error {
	l := e.Def.Login
	if l == nil {
		return nil
	}
	v := e.baseVars()
	h, err := e.headers(l.Headers, v)
	if err != nil {
		return err
	}
	if len(h) == 0 {
		if h, err = e.headers(e.Def.Search.Headers, v); err != nil {
			return err
		}
	}
	base, err := e.base()
	if err != nil {
		return err
	}
	if len(l.Cookies) > 0 {
		e.HTTP.SetCookies(base, parseCookies(strings.Join(l.Cookies, "; ")))
	}

	var resp *page
	switch l.Method {
	case "cookie":
		cookie := e.Settings["cookie"]
		if strings.TrimSpace(cookie) == "" {
			return fmt.Errorf("%w: this tracker logs in with a browser cookie; paste it in the settings", ErrLoginFailed)
		}
		e.HTTP.SetCookies(base, parseCookies(cookie))
	case "form", "":
		resp, err = e.loginForm(ctx, v, h)
	case "post":
		resp, err = e.loginPost(ctx, v, h)
	case "get":
		resp, err = e.loginGet(ctx, v, h)
	case "oneurl":
		oneURL, _ := l.Inputs.Get("oneurl")
		path, terr := Execute(l.Path+oneURL, v)
		if terr != nil {
			return terr
		}
		u, rerr := e.resolve(path, nil)
		if rerr != nil {
			return rerr
		}
		resp, err = e.fetch(ctx, http.MethodGet, u, nil, h, true)
	default:
		return fmt.Errorf("this definition logs in with %q, which Reelkeeper doesn't support", l.Method)
	}
	if err != nil {
		return err
	}
	if resp != nil {
		if doc, err := goquery.NewDocumentFromReader(bytes.NewReader(resp.body)); err == nil {
			if msg := pageError(doc.Selection, l.Error, v, e.now()); msg != "" {
				return fmt.Errorf("%w: the tracker said: %s", ErrLoginFailed, msg)
			}
		}
	}
	return e.loginTest(ctx, v, h)
}

func (e *Engine) loginForm(ctx context.Context, v Vars, h http.Header) (*page, error) {
	l := e.Def.Login
	path, err := Execute(l.Path, v)
	if err != nil {
		return nil, err
	}
	pageURL, err := e.resolve(path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := e.fetch(ctx, http.MethodGet, pageURL, nil, h, true)
	if err != nil {
		return nil, err
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(resp.body))
	if err != nil {
		return nil, err
	}
	if l.Captcha != nil {
		return nil, fmt.Errorf("%w: this tracker shows a captcha at login, which Reelkeeper can't solve. Use cookie login if the tracker offers it", ErrLoginFailed)
	}
	formSel := l.Form
	if formSel == "" {
		formSel = "form"
	}
	form := doc.Find(formSel).First()
	if form.Length() == 0 {
		return nil, fmt.Errorf("%w: no login form on %s; the definition may be out of date", ErrLoginFailed, pageURL)
	}
	values := url.Values{}
	// The form's own fields first: hidden tokens, defaults.
	form.Find("input[name], select[name], textarea[name]").Each(func(_ int, s *goquery.Selection) {
		name, _ := s.Attr("name")
		typ, _ := s.Attr("type")
		if typ == "checkbox" || typ == "radio" {
			if _, on := s.Attr("checked"); !on {
				return
			}
		}
		val, _ := s.Attr("value")
		if goquery.NodeName(s) == "select" {
			val, _ = s.Find("option[selected]").Attr("value")
		}
		values.Set(name, val)
	})
	for _, kv := range l.SelectorInputs {
		val, err := htmlValue(doc.Selection, kv.Value, v, e.now())
		if err != nil {
			return nil, fmt.Errorf("%w: couldn't read %s from the login page", ErrLoginFailed, kv.Key)
		}
		values.Set(kv.Key, val)
	}
	for _, kv := range l.Inputs {
		val, err := Execute(kv.Value, v)
		if err != nil {
			return nil, err
		}
		values.Set(kv.Key, val)
	}
	action, _ := form.Attr("action")
	if l.SubmitPath != "" {
		if action, err = Execute(l.SubmitPath, v); err != nil {
			return nil, err
		}
	}
	submitURL, err := e.resolve(action, resp.url)
	if err != nil {
		return nil, err
	}
	method := http.MethodPost
	if m, _ := form.Attr("method"); strings.EqualFold(m, "get") {
		method = http.MethodGet
		submitURL.RawQuery = e.encode(values)
		values = nil
	}
	return e.fetch(ctx, method, submitURL, values, h, true)
}

func (e *Engine) loginInputs(ctx context.Context, v Vars, h http.Header) (url.Values, error) {
	l := e.Def.Login
	values := url.Values{}
	if len(l.GetSelectorInputs) > 0 {
		path, err := Execute(l.Path, v)
		if err != nil {
			return nil, err
		}
		u, err := e.resolve(path, nil)
		if err != nil {
			return nil, err
		}
		resp, err := e.fetch(ctx, http.MethodGet, u, nil, h, true)
		if err != nil {
			return nil, err
		}
		doc, err := goquery.NewDocumentFromReader(bytes.NewReader(resp.body))
		if err != nil {
			return nil, err
		}
		for _, kv := range l.GetSelectorInputs {
			val, err := htmlValue(doc.Selection, kv.Value, v, e.now())
			if err != nil {
				return nil, fmt.Errorf("%w: couldn't read %s from the login page", ErrLoginFailed, kv.Key)
			}
			values.Set(kv.Key, val)
		}
	}
	for _, kv := range l.Inputs {
		val, err := Execute(kv.Value, v)
		if err != nil {
			return nil, err
		}
		values.Set(kv.Key, val)
	}
	return values, nil
}

func (e *Engine) loginPost(ctx context.Context, v Vars, h http.Header) (*page, error) {
	values, err := e.loginInputs(ctx, v, h)
	if err != nil {
		return nil, err
	}
	path, err := Execute(firstNonEmpty(e.Def.Login.SubmitPath, e.Def.Login.Path), v)
	if err != nil {
		return nil, err
	}
	u, err := e.resolve(path, nil)
	if err != nil {
		return nil, err
	}
	return e.fetch(ctx, http.MethodPost, u, values, h, true)
}

func (e *Engine) loginGet(ctx context.Context, v Vars, h http.Header) (*page, error) {
	values, err := e.loginInputs(ctx, v, h)
	if err != nil {
		return nil, err
	}
	path, err := Execute(e.Def.Login.Path, v)
	if err != nil {
		return nil, err
	}
	u, err := e.resolve(path, nil)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	for k, vs := range values {
		for _, x := range vs {
			q.Add(k, x)
		}
	}
	u.RawQuery = e.encode(q)
	return e.fetch(ctx, http.MethodGet, u, nil, h, true)
}

// loginTest checks a page that only shows when logged in.
func (e *Engine) loginTest(ctx context.Context, v Vars, h http.Header) error {
	t := e.Def.Login.Test
	if t == nil || t.Path == "" {
		return nil
	}
	path, err := Execute(t.Path, v)
	if err != nil {
		return err
	}
	u, err := e.resolve(path, nil)
	if err != nil {
		return err
	}
	resp, err := e.fetch(ctx, http.MethodGet, u, nil, h, false)
	if err != nil {
		return err
	}
	if resp.status >= 300 {
		return fmt.Errorf("%w: the tracker sent us back to log in (HTTP %d); check the username, password or cookie", ErrLoginFailed, resp.status)
	}
	if t.Selector != "" {
		doc, err := goquery.NewDocumentFromReader(bytes.NewReader(resp.body))
		if err != nil || doc.Find(t.Selector).Length() == 0 {
			return fmt.Errorf("%w: the tracker didn't show us as logged in; check the username, password or cookie", ErrLoginFailed)
		}
	}
	return nil
}

// parseCookies reads "a=1; b=2" into cookies.
func parseCookies(s string) []*http.Cookie {
	var out []*http.Cookie
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && k != "" {
			// Cookies we send to the tracker, not ones we set in a
			// browser: Secure/HttpOnly/SameSite don't apply.
			out = append(out, &http.Cookie{Name: strings.TrimSpace(k), Value: strings.TrimSpace(v)}) //nolint:gosec // client-side cookie
		}
	}
	return out
}
