package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// updateType is Emby's word for what happened to a path.
func updateType(kind string) string {
	switch kind {
	case KindImported:
		return "Created"
	case KindDeleted:
		return "Deleted"
	}
	return "Modified"
}

// sendEmby tells Emby about one folder, so it rescans just that folder.
// (Never a full library refresh: Emby runs on a shared family computer.)
func sendEmby(ctx context.Context, c settings.EmbyNotify, ev Event) error {
	body, err := json.Marshal(map[string]any{"Updates": []map[string]string{{
		"Path":       translatePath(folderOf(ev.Path), c.PathFrom, c.PathTo),
		"UpdateType": updateType(ev.Kind),
	}}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/emby/Library/Media/Updated", bytes.NewReader(body))
	if err != nil {
		return cleanErr(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Token", c.APIKey)
	_, err = do(req)
	if err != nil {
		return fmt.Errorf("emby: %w", err)
	}
	return nil
}

func testEmby(ctx context.Context, c settings.EmbyNotify) (string, error) {
	if c.URL == "" {
		return "", errors.New("enter Emby's address first")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/emby/System/Info", nil)
	if err != nil {
		return "", cleanErr(err)
	}
	req.Header.Set("X-Emby-Token", c.APIKey)
	b, err := do(req)
	if err != nil {
		return "", fmt.Errorf("couldn't reach Emby: %w", err)
	}
	var info struct{ ServerName, Version string }
	if json.Unmarshal(b, &info) != nil || info.Version == "" {
		return "", errors.New("that address answered, but it doesn't look like Emby")
	}
	return fmt.Sprintf("Connected to Emby %s (%s)", info.Version, info.ServerName), nil
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
