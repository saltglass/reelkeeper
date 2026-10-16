// Package coretest gives tests a plain core.HTTP with a cookie jar.
package coretest

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"
)

// HTTP is a core.HTTP for tests. Requests records "METHOD /path?query".
type HTTP struct {
	Jar      *cookiejar.Jar
	Requests []string
}

// New returns an HTTP with an empty cookie jar.
func New() *HTTP {
	jar, _ := cookiejar.New(nil)
	return &HTTP{Jar: jar}
}

// Do sends the request.
func (h *HTTP) Do(req *http.Request, follow bool) (*http.Response, error) {
	h.Requests = append(h.Requests, req.Method+" "+req.URL.RequestURI())
	c := &http.Client{Jar: h.Jar, Timeout: 5 * time.Second}
	if !follow {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return c.Do(req) //nolint:gosec // tests talk to their own fake servers
}

// SetCookies stores cookies.
func (h *HTTP) SetCookies(u *url.URL, c []*http.Cookie) { h.Jar.SetCookies(u, c) }

// Cookies returns cookies.
func (h *HTTP) Cookies(u *url.URL) []*http.Cookie { return h.Jar.Cookies(u) }
