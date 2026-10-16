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

// DownloadLink returns where a result is actually downloaded from: a
// magnet link or a .torrent URL (fetched later with the same session).
// Many trackers list a details page in their results; the definition's
// download block says how to find the real link there.
func (e *Engine) DownloadLink(ctx context.Context, r Result) (string, error) {
	d := e.Def.Download
	direct := firstNonEmpty(r.DownloadURL, r.MagnetURL)
	if d == nil || (len(d.Selectors) == 0 && d.InfoHash == nil && d.Before == nil) {
		if direct == "" {
			return "", fmt.Errorf("the tracker gave no download link")
		}
		return direct, nil
	}
	if err := e.ensureLogin(ctx); err != nil {
		return "", err
	}
	page := firstNonEmpty(r.DownloadURL, r.DetailsURL)
	if page == "" {
		return direct, nil
	}
	pageURL, err := url.Parse(page)
	if err != nil {
		return "", err
	}
	v := e.baseVars()
	v["DownloadUri"] = uriVars(pageURL)
	h, err := e.headers(d.Headers, v)
	if err != nil {
		return "", err
	}

	if b := d.Before; b != nil {
		path, err := Execute(b.Path, v)
		if err != nil {
			return "", err
		}
		u, err := e.resolve(path, nil)
		if err != nil {
			return "", err
		}
		form := url.Values{}
		for _, kv := range b.Inputs {
			val, err := Execute(kv.Value, v)
			if err != nil {
				return "", err
			}
			form.Set(kv.Key, val)
		}
		method := http.MethodGet
		if strings.EqualFold(b.Method, "post") {
			method = http.MethodPost
		} else if len(form) > 0 {
			u.RawQuery = e.encode(form)
			form = nil
		}
		if _, err := e.fetch(ctx, method, u, form, h, true); err != nil {
			return "", fmt.Errorf("before downloading: %w", err)
		}
	}
	if len(d.Selectors) == 0 && d.InfoHash == nil {
		return direct, nil
	}

	resp, err := e.fetch(ctx, http.MethodGet, pageURL, nil, h, true)
	if err != nil {
		return "", err
	}
	if resp.status >= 400 {
		return "", fmt.Errorf("the tracker answered HTTP %d for the release page", resp.status)
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(resp.body))
	if err != nil {
		return "", err
	}
	if ih := d.InfoHash; ih != nil {
		hash, err := htmlValue(doc.Selection, ih.Hash, v, e.now())
		if err == nil && hash != "" {
			title, _ := htmlValue(doc.Selection, ih.Title, v, e.now())
			if title == "" {
				title = r.Title
			}
			return "magnet:?xt=urn:btih:" + strings.ToLower(hash) + "&dn=" + url.QueryEscape(title), nil
		}
	}
	for _, s := range d.Selectors {
		val, err := htmlValue(doc.Selection, s, v, e.now())
		if err != nil || strings.TrimSpace(val) == "" {
			continue
		}
		if strings.HasPrefix(val, "magnet:") {
			return val, nil
		}
		u, err := e.resolve(val, resp.url)
		if err != nil {
			continue
		}
		return u.String(), nil
	}
	return "", fmt.Errorf("couldn't find the download link on the release page; the definition may be out of date")
}

// uriVars is .DownloadUri as templates see it (named after .NET's Uri).
func uriVars(u *url.URL) map[string]any {
	q := map[string]any{}
	for k, vs := range u.Query() {
		if len(vs) > 0 {
			q[k] = vs[0]
		}
	}
	pq := u.EscapedPath()
	if u.RawQuery != "" {
		pq += "?" + u.RawQuery
	}
	return map[string]any{
		"AbsoluteUri": u.String(), "AbsolutePath": u.EscapedPath(), "PathAndQuery": pq, "Query": q,
	}
}
