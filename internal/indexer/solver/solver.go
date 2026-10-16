// Package solver sends requests through a FlareSolverr-compatible
// Cloudflare solver such as TRAWL. The solver loads the page in a real
// browser, gets past the challenge, and hands back the page. Reelkeeper never
// solves challenges itself.
//
// The whole request goes through the solver, not just its cookies:
// clearance cookies are tied to the solver's browser and address, so
// reusing them from Reelkeeper is fragile.
package solver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Client talks to the solver.
type Client struct {
	URL     string        // e.g. http://trawl:8191
	Timeout time.Duration // how long the solver may take per page
	HTTP    *http.Client  // to reach the solver itself; nil means a default
}

// DefaultTimeout is how long a solve may take unless set.
const DefaultTimeout = 60 * time.Second

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	// The solver needs its own time, plus a margin for the answer.
	return &http.Client{Timeout: c.timeout() + 15*time.Second}
}

// Check asks the solver whether it's running.
func (c *Client) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/", nil)
	if err != nil {
		return err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("can't reach the solver at %s: %w", c.URL, err)
	}
	defer resp.Body.Close()
	var status struct {
		Msg     string `json:"msg"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&status); err != nil || !strings.Contains(strings.ToLower(status.Msg), "ready") {
		return fmt.Errorf("%s doesn't look like a FlareSolverr-compatible solver", c.URL)
	}
	return nil
}

type cookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain,omitempty"`
	Path   string `json:"path,omitempty"`
}

type command struct {
	Cmd        string   `json:"cmd"`
	URL        string   `json:"url"`
	MaxTimeout int64    `json:"maxTimeout"`
	PostData   string   `json:"postData,omitempty"`
	Cookies    []cookie `json:"cookies,omitempty"`
}

type answer struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Solution struct {
		URL       string            `json:"url"`
		Status    int               `json:"status"`
		Headers   map[string]string `json:"headers"`
		Response  string            `json:"response"`
		Cookies   []cookie          `json:"cookies"`
		UserAgent string            `json:"userAgent"`
	} `json:"solution"`
}

// ErrUnsupported means a request can't go through the solver (it only
// does GETs and form POSTs).
var ErrUnsupported = errors.New("this request can't be sent through the solver")

// Do sends one request through the solver with the given cookies, and
// returns the page as an ordinary response plus the cookies the browser
// ended up with. The browser follows redirects itself.
func (c *Client) Do(req *http.Request, cookies []*http.Cookie) (*http.Response, []*http.Cookie, error) {
	cmd := command{URL: req.URL.String(), MaxTimeout: c.timeout().Milliseconds()}
	switch req.Method {
	case http.MethodGet:
		cmd.Cmd = "request.get"
	case http.MethodPost:
		if ct := req.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
			return nil, nil, fmt.Errorf("%w: a %s POST", ErrUnsupported, ct)
		}
		cmd.Cmd = "request.post"
		if req.Body != nil {
			b, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, nil, err
			}
			cmd.PostData = string(b)
		}
	default:
		return nil, nil, fmt.Errorf("%w: method %s", ErrUnsupported, req.Method)
	}
	for _, ck := range cookies {
		cmd.Cookies = append(cmd.Cookies, cookie{Name: ck.Name, Value: ck.Value})
	}
	body, err := json.Marshal(cmd)
	if err != nil {
		return nil, nil, err
	}
	sreq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, strings.TrimRight(c.URL, "/")+"/v1", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	sreq.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(sreq)
	if err != nil {
		return nil, nil, fmt.Errorf("can't reach the solver at %s: %w", c.URL, err)
	}
	defer resp.Body.Close()
	var a answer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&a); err != nil {
		return nil, nil, fmt.Errorf("the solver's answer couldn't be read: %w", err)
	}
	if a.Status != "ok" {
		msg := a.Message
		if msg == "" {
			msg = "status " + a.Status
		}
		return nil, nil, fmt.Errorf("the solver couldn't load the page: %s", msg)
	}

	out := &http.Response{
		StatusCode: a.Solution.Status,
		Status:     fmt.Sprintf("%d %s", a.Solution.Status, http.StatusText(a.Solution.Status)),
		Header:     http.Header{},
		Request:    req,
		Body:       io.NopCloser(strings.NewReader(unwrapJSON(a.Solution.Response))),
	}
	for k, v := range a.Solution.Headers {
		out.Header.Set(k, v)
	}
	if final, err := url.Parse(a.Solution.URL); err == nil && a.Solution.URL != "" {
		r2 := req.Clone(req.Context())
		r2.URL = final
		out.Request = r2
	}
	var got []*http.Cookie
	for _, ck := range a.Solution.Cookies {
		got = append(got, &http.Cookie{Name: ck.Name, Value: ck.Value, Domain: ck.Domain, Path: ck.Path}) //nolint:gosec // cookies we keep for the tracker, not ones we set
	}
	return out, got, nil
}

// A browser shows a JSON answer as a page with the JSON in a <pre>; give
// back the JSON itself.
var preJSON = regexp.MustCompile(`(?s)^\s*<html[^>]*>.*?<body[^>]*>\s*<pre[^>]*>(.*)</pre>.*</body>\s*</html>\s*$`)

func unwrapJSON(page string) string {
	if m := preJSON.FindStringSubmatch(page); m != nil {
		inner := strings.TrimSpace(html.UnescapeString(m[1]))
		if strings.HasPrefix(inner, "{") || strings.HasPrefix(inner, "[") {
			return inner
		}
	}
	return page
}
