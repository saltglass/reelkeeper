package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/settings"
)

var httpClient = &http.Client{
	Timeout: sendTimeout,
	// A redirect would resend the token to wherever it points.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func do(req *http.Request) ([]byte, error) {
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, cleanErr(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, statusError(resp.StatusCode)
	}
	return body, nil
}

func statusError(code int) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("it answered %d %s; check the API key or secret", code, http.StatusText(code))
	case http.StatusNotFound:
		return errors.New("it answered 404 Not Found; check the address")
	}
	return fmt.Errorf("it answered %d %s", code, http.StatusText(code))
}

// updateType is the media server's word for what happened to a path.
func updateType(kind string) string {
	switch kind {
	case KindImported:
		return "Created"
	case KindDeleted:
		return "Deleted"
	}
	return "Modified"
}

// mediaServer is what differs between Emby and Jellyfin. Jellyfin began as a
// fork of Emby and kept its "media updated" call, but drops Emby's /emby
// prefix and, from 10.11, may refuse Emby's X-Emby-Token header (newer
// versions do by default), so it gets the standard Authorization header.
type mediaServer struct {
	name   string
	prefix string // before every API path
	auth   func(req *http.Request, key string)
	// looksRight tells this server's /System/Info answer from another's.
	looksRight func(info systemInfo) bool
}

type systemInfo struct{ ServerName, Version, ProductName string }

var (
	emby = mediaServer{
		name: "Emby", prefix: "/emby",
		auth:       func(req *http.Request, key string) { req.Header.Set("X-Emby-Token", key) },
		looksRight: func(info systemInfo) bool { return info.Version != "" },
	}
	jellyfin = mediaServer{
		name: "Jellyfin",
		auth: func(req *http.Request, key string) {
			req.Header.Set("Authorization", `MediaBrowser Token="`+key+`"`)
		},
		looksRight: func(info systemInfo) bool {
			return info.Version != "" && strings.Contains(info.ProductName, "Jellyfin")
		},
	}
)

// sendMediaServer tells Emby or Jellyfin about one folder, so it rescans just
// that folder. (Never a full library refresh: the server may be a shared
// family computer.)
func sendMediaServer(ctx context.Context, m mediaServer, c settings.MediaServerNotify, ev Event) error {
	body, err := json.Marshal(map[string]any{"Updates": []map[string]string{{
		"Path":       translatePath(folderOf(ev.Path), c.PathFrom, c.PathTo),
		"UpdateType": updateType(ev.Kind),
	}}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+m.prefix+"/Library/Media/Updated", bytes.NewReader(body))
	if err != nil {
		return cleanErr(err)
	}
	req.Header.Set("Content-Type", "application/json")
	m.auth(req, c.APIKey)
	if _, err = do(req); err != nil {
		return fmt.Errorf("%s: %w", strings.ToLower(m.name), err)
	}
	return nil
}

// testMediaServer checks the address and API key by asking for the server's
// details, which needs a valid key.
func testMediaServer(ctx context.Context, m mediaServer, c settings.MediaServerNotify) (string, error) {
	if c.URL == "" {
		return "", fmt.Errorf("enter %s's address first", m.name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+m.prefix+"/System/Info", nil)
	if err != nil {
		return "", cleanErr(err)
	}
	m.auth(req, c.APIKey)
	b, err := do(req)
	if err != nil {
		return "", fmt.Errorf("couldn't reach %s: %w", m.name, err)
	}
	var info systemInfo
	if json.Unmarshal(b, &info) != nil || !m.looksRight(info) {
		return "", fmt.Errorf("that address answered, but it doesn't look like %s", m.name)
	}
	return fmt.Sprintf("Connected to %s %s (%s)", m.name, info.Version, info.ServerName), nil
}

type webhookPayload struct {
	Event   string    `json:"event"`
	Title   string    `json:"title"`
	Message string    `json:"message"`
	TitleID int64     `json:"titleId"`
	Path    string    `json:"path"`
	At      time.Time `json:"at"`
}

func sendWebhook(ctx context.Context, c settings.WebhookNotify, ev Event) error {
	if c.URL == "" {
		return errors.New("enter the webhook address first")
	}
	body, err := json.Marshal(webhookPayload{ev.Kind, ev.Title, ev.Message, ev.TitleID, ev.Path, ev.At})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return cleanErr(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Reelkeeper")
	if c.Secret != "" {
		req.Header.Set("X-Reelkeeper-Secret", c.Secret)
	}
	if _, err := do(req); err != nil {
		return fmt.Errorf("webhook: %w", err)
	}
	return nil
}
