package settings

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// GroupDownloadClient is the settings group for the download client
// (Settings → Download Client).
const GroupDownloadClient = "downloadClient"

// The download clients Reelkeeper can talk to (DownloadClient.Type).
const (
	ClientQBittorrent  = "qbittorrent"
	ClientTransmission = "transmission"
	ClientDeluge       = "deluge"
)

// MovieCategory is Reelkeeper's category for movies in the download client
// (a qBittorrent category, or a Transmission or Deluge label). It's fixed: cleanup only
// ever touches torrents in Reelkeeper's own categories, so they must never be
// shared with another app.
const MovieCategory = "reelkeeper-movies"

// TVCategory is Reelkeeper's category for TV, fixed for the same reason.
const TVCategory = "reelkeeper-tv"

// Categories are all of Reelkeeper's categories: the only ones
// it ever reads, changes or cleans up.
var Categories = []string{MovieCategory, TVCategory}

// DownloadClient holds the download client connection.
type DownloadClient struct {
	// Type is which download client it is: ClientQBittorrent,
	// ClientTransmission or ClientDeluge. Settings saved before there was a choice have no
	// type, and read as qBittorrent.
	Type string `json:"type"`
	// URL is the client's address: qBittorrent's Web UI (e.g.
	// http://192.168.1.20:8080), Transmission's RPC address (e.g.
	// http://192.168.1.20:9091) or Deluge's web UI (e.g. http://192.168.1.20:8112).
	// Empty means no download client is set up.
	URL string `json:"url"`
	// Username is empty for Deluge, whose web UI has a password only.
	Username string `json:"username"`
	// Password is encrypted in the database and never sent to the
	// browser.
	Password string `json:"-"`
	// Folder is where Reelkeeper's downloads go, as the client sees it (e.g.
	// /mnt/downloads/reelkeeper); empty means the client's default. A folder
	// of its own lets cleanup spot stray files without mistaking other
	// apps' downloads for them.
	Folder string `json:"folder"`
}

// Configured reports whether a download client is set up.
func (d DownloadClient) Configured() bool { return d.URL != "" }

// ClientType is Type, with the default (qBittorrent) for settings that
// have none.
func (d DownloadClient) ClientType() string {
	if d.Type == "" {
		return ClientQBittorrent
	}
	return d.Type
}

// ClientName is what messages call the client: "qBittorrent",
// "Transmission" or "Deluge".
func (d DownloadClient) ClientName() string { return ClientDisplayName(d.ClientType()) }

// AddressPrompt asks for the client's address, in the client's own words.
func (d DownloadClient) AddressPrompt() string {
	switch d.ClientType() {
	case ClientTransmission:
		return "Enter Transmission's RPC address, like http://192.168.1.20:9091."
	case ClientDeluge:
		return "Enter Deluge's web UI address, like http://192.168.1.20:8112."
	}
	return "Enter qBittorrent's Web UI address, like http://192.168.1.20:8080."
}

// ClientDisplayName is the name of a client type, as people write it.
func ClientDisplayName(clientType string) string {
	switch clientType {
	case ClientTransmission:
		return "Transmission"
	case ClientDeluge:
		return "Deluge"
	}
	return "qBittorrent"
}

type storedDownloadClient struct {
	DownloadClient
	PasswordSealed string `json:"passwordSealed"`
}

// DownloadClientUpdate is what the user can change. A nil password keeps
// the stored one.
type DownloadClientUpdate struct {
	// Type is ClientQBittorrent, ClientTransmission or ClientDeluge; empty
	// keeps the current one.
	Type     string
	URL      string
	Username string
	Password *string
	Folder   string
}

const downloadClientPassword = "downloadClient.password"

// DownloadClient returns the download client settings.
func (s *Service) DownloadClient() DownloadClient {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.downloadClient
}

// ApplyDownloadClient checks an update, filling in what it keeps from the
// current settings (the password). It's also used to test settings before
// saving them.
func (s *Service) ApplyDownloadClient(u DownloadClientUpdate) (DownloadClient, error) {
	s.mu.RLock()
	next := s.downloadClient
	s.mu.RUnlock()
	oldType := next.ClientType()
	if u.Type != "" {
		next.Type = u.Type
	}
	next.Type = next.ClientType()
	next.URL = strings.TrimRight(strings.TrimSpace(u.URL), "/")
	next.Username = strings.TrimSpace(u.Username)
	if next.Type == ClientDeluge {
		// Deluge's web UI has a password and no username: whatever the form
		// sent (a leftover from another client) is dropped.
		next.Username = ""
	}
	next.Folder = strings.TrimRight(strings.TrimSpace(u.Folder), "/")
	if u.Password != nil {
		next.Password = *u.Password
	} else if next.Type != oldType {
		// A different program has a different login: the stored password
		// isn't kept across the switch.
		next.Password = ""
	}
	var errs validation.Errors
	if next.Type != ClientQBittorrent && next.Type != ClientTransmission && next.Type != ClientDeluge {
		errs.Add("type", "Choose qBittorrent, Transmission or Deluge.")
	}
	if next.URL != "" && errs == nil {
		name := next.ClientName()
		p, err := url.Parse(next.URL)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			errs.Add("url", next.AddressPrompt())
		}
		// Transmission's RPC login is optional (it can be turned off);
		// qBittorrent's Web UI always has one.
		if next.Username == "" && next.Type == ClientQBittorrent {
			errs.Add("username", "Enter the Web UI username.")
		}
		if next.Folder != "" && !strings.HasPrefix(next.Folder, "/") {
			errs.Add("folder", "Enter a full path as "+name+" sees it, like /mnt/downloads/reelkeeper, or leave it empty.")
		}
	}
	if errs != nil {
		return DownloadClient{}, errs
	}
	return next, nil
}

// SaveDownloadClient validates and stores the download client settings.
// Clearing the address removes the download client (and its password), but
// the type is remembered: switching to the other client moves Reelkeeper's
// torrents (see download.Service.BeginMove), which needs to know which client
// they are in, so removing the client first mustn't hide that. A change of
// type drops the stored password (the other program has its own login); the
// move keeps the old connection separately (DownloadMove).
func (s *Service) SaveDownloadClient(ctx context.Context, u DownloadClientUpdate) (DownloadClient, error) {
	next, err := s.ApplyDownloadClient(u)
	if err != nil {
		return DownloadClient{}, err
	}
	if next.URL == "" {
		next = DownloadClient{Type: next.Type}
	}
	s.mu.Lock()
	if u.Password != nil || next.URL == "" || next.Type != s.downloadClient.ClientType() {
		delete(s.sealedKeep, downloadClientPassword)
	}
	sealed, err := s.sealOrKeep(downloadClientPassword, next.Password)
	if err == nil {
		err = s.put(ctx, GroupDownloadClient, storedDownloadClient{DownloadClient: next, PasswordSealed: sealed})
	}
	if err != nil {
		s.mu.Unlock()
		return DownloadClient{}, err
	}
	s.downloadClient = next
	s.mu.Unlock()
	s.notify(GroupDownloadClient)
	return next, nil
}

func (s *Service) loadDownloadClient(ctx context.Context) error {
	var stored storedDownloadClient
	if _, err := s.load(ctx, GroupDownloadClient, &stored); err != nil {
		return err
	}
	d := stored.DownloadClient
	d.Type = d.ClientType()
	if stored.PasswordSealed != "" {
		if pw, err := s.box.Open(stored.PasswordSealed); err != nil {
			if errors.Is(err, secrets.ErrUndecryptable) {
				s.sealedKeep[downloadClientPassword] = stored.PasswordSealed
			}
			s.logger.Error("can't read a stored credential", "setting", downloadClientPassword, "error", err)
		} else {
			d.Password = pw
		}
	}
	s.downloadClient = d
	return nil
}
