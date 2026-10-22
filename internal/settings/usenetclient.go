package settings

import (
	"context"
	"net/url"
	"strings"

	"github.com/saltglass/reelkeeper/internal/validation"
)

// GroupUsenetClient is the settings group for the Usenet download client
// (Settings → Download Client, Usenet). It is separate from the torrent
// client's group: either, both or neither can be set up, and moving torrents
// between torrent clients (DownloadMove) never involves it.
const GroupUsenetClient = "usenetClient"

// The Usenet clients Reelkeeper can talk to (UsenetClient.Type).
const (
	UsenetSABnzbd = "sabnzbd"
)

// UsenetClient holds the Usenet download client connection. It uses the same
// categories as the torrent client (MovieCategory, TVCategory), created in the
// Usenet client by Reelkeeper.
type UsenetClient struct {
	// Type is which Usenet client it is; only UsenetSABnzbd so far, and
	// settings without one read as it.
	Type string `json:"type"`
	// URL is the client's address, e.g. http://192.168.1.20:8080 for
	// SABnzbd. Empty means no Usenet client is set up.
	URL string `json:"url"`
	// APIKey is SABnzbd's API key (its Config → General page). It is
	// encrypted in the database and never sent to the browser.
	APIKey string `json:"-"`
}

// Configured reports whether a Usenet client is set up.
func (u UsenetClient) Configured() bool { return u.URL != "" }

// ClientType is Type, with the default (SABnzbd) for settings that have none.
func (u UsenetClient) ClientType() string {
	if u.Type == "" {
		return UsenetSABnzbd
	}
	return u.Type
}

// ClientName is what messages call the client, such as "SABnzbd".
func (u UsenetClient) ClientName() string { return UsenetDisplayName(u.ClientType()) }

// UsenetDisplayName is the name of a Usenet client type as a sentence uses it.
func UsenetDisplayName(clientType string) string {
	switch clientType {
	case UsenetSABnzbd:
		return "SABnzbd"
	}
	return clientType
}

type storedUsenetClient struct {
	UsenetClient
	APIKeySealed string `json:"apiKeySealed"`
}

// UsenetClientUpdate is what the user can change. A nil APIKey keeps the
// stored one.
type UsenetClientUpdate struct {
	// Type is UsenetSABnzbd; empty keeps the current one.
	Type   string
	URL    string
	APIKey *string
}

const usenetClientAPIKey = "usenetClient.apiKey"

// UsenetClient returns the Usenet download client settings.
func (s *Service) UsenetClient() UsenetClient {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.usenetClient
}

// ApplyUsenetClient checks an update, filling in what it keeps from the
// current settings (the API key). It's also used to test settings before
// saving them.
func (s *Service) ApplyUsenetClient(u UsenetClientUpdate) (UsenetClient, error) {
	s.mu.RLock()
	next := s.usenetClient
	s.mu.RUnlock()
	oldType := next.ClientType()
	if u.Type != "" {
		next.Type = u.Type
	}
	next.Type = next.ClientType()
	next.URL = strings.TrimRight(strings.TrimSpace(u.URL), "/")
	if u.APIKey != nil {
		next.APIKey = strings.TrimSpace(*u.APIKey)
	} else if next.Type != oldType {
		// A different program has a different key.
		next.APIKey = ""
	}
	var errs validation.Errors
	if next.Type != UsenetSABnzbd {
		errs.Add("type", "Choose SABnzbd.")
	}
	if next.URL != "" && errs == nil {
		p, err := url.Parse(next.URL)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			errs.Add("url", "Enter SABnzbd's address, like http://192.168.1.20:8080.")
		}
		if next.APIKey == "" && s.sealedKeep[usenetClientAPIKey] == "" {
			errs.Add("apiKey", "Enter SABnzbd's API key (in SABnzbd: Config → General → Security).")
		}
	}
	if errs != nil {
		return UsenetClient{}, errs
	}
	return next, nil
}

// SaveUsenetClient validates and stores the Usenet client settings. Clearing
// the address removes the Usenet client and its API key.
func (s *Service) SaveUsenetClient(ctx context.Context, u UsenetClientUpdate) (UsenetClient, error) {
	next, err := s.ApplyUsenetClient(u)
	if err != nil {
		return UsenetClient{}, err
	}
	if !next.Configured() {
		next = UsenetClient{Type: next.Type}
	}
	s.mu.Lock()
	if u.APIKey != nil || !next.Configured() || next.Type != s.usenetClient.ClientType() {
		delete(s.sealedKeep, usenetClientAPIKey)
	}
	sealed, err := s.sealOrKeep(usenetClientAPIKey, next.APIKey)
	if err == nil {
		err = s.put(ctx, GroupUsenetClient, storedUsenetClient{UsenetClient: next, APIKeySealed: sealed})
	}
	if err != nil {
		s.mu.Unlock()
		return UsenetClient{}, err
	}
	s.usenetClient = next
	s.mu.Unlock()
	s.notify(GroupUsenetClient)
	return next, nil
}

func (s *Service) loadUsenetClient(ctx context.Context) error {
	var stored storedUsenetClient
	if _, err := s.load(ctx, GroupUsenetClient, &stored); err != nil {
		return err
	}
	u := stored.UsenetClient
	u.Type = u.ClientType()
	u.APIKey = s.openSecret(usenetClientAPIKey, stored.APIKeySealed)
	s.usenetClient = u
	return nil
}
