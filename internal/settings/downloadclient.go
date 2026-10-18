package settings

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"

	"github.com/saltglass/reelkeeper/internal/download/builtin"
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
	// ClientBuiltin is Reelkeeper's own torrent client (internal/download/builtin).
	// It has no address or login: it is set up by its Folder and the fields
	// below it.
	ClientBuiltin = "builtin"
)

// The built-in client's defaults and limits.
const (
	DefaultListenPort         = 6881
	DefaultMaxActiveDownloads = 3
	DefaultMaxActiveSeeds     = 25
	MaxMaxActiveDownloads     = 50
	MaxMaxActiveSeeds         = 10000
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
	// ClientTransmission, ClientDeluge or ClientBuiltin. Settings saved before
	// there was a choice have no type, and read as qBittorrent.
	Type string `json:"type"`
	// URL is the client's address: qBittorrent's Web UI (e.g.
	// http://192.168.1.20:8080), Transmission's RPC address (e.g.
	// http://192.168.1.20:9091) or Deluge's web UI (e.g. http://192.168.1.20:8112).
	// Empty means no download client is set up. Always empty for the built-in
	// client, which has no address.
	URL string `json:"url"`
	// Username is empty for Deluge, whose web UI has a password only.
	Username string `json:"username"`
	// Password is encrypted in the database and never sent to the
	// browser.
	Password string `json:"-"`
	// Folder is where Reelkeeper's downloads go, as the client sees it (e.g.
	// /mnt/downloads/reelkeeper); empty means the client's default. A folder
	// of its own lets cleanup spot stray files without mistaking other
	// apps' downloads for them. The built-in client needs one: it is the
	// only thing that says it is set up.
	Folder string `json:"folder"`

	// The rest is for the built-in client only (and zero for the others).
	//
	// ListenPort is the port peers connect to, TCP and UDP.
	ListenPort int `json:"listenPort"`
	// MaxActiveDownloads is how many unfinished torrents download at once; the
	// others wait their turn.
	MaxActiveDownloads int `json:"maxActiveDownloads"`
	// MaxActiveSeeds is how many finished torrents upload at once; 0 means no
	// limit.
	MaxActiveSeeds int `json:"maxActiveSeeds"`
	// DownloadLimitKBs and UploadLimitKBs are speed limits in KB/s; 0 means no
	// limit.
	DownloadLimitKBs int `json:"downloadLimitKBs"`
	UploadLimitKBs   int `json:"uploadLimitKBs"`

	// The built-in client's VPN (WireGuard). With a VPN config, everything the
	// client does goes through the VPN and ListenPort is not used (the
	// provider's forwarded port is, ForwardedPort); without one the client
	// uses the computer's own network, which shows peers the real address.
	//
	// VPNConfig is the whole config file, stored encrypted (it holds the
	// private key) and never sent to the browser.
	VPNConfig string `json:"-"`
	// VPNUnreadable: a VPN config is stored but couldn't be decrypted (the
	// secret key changed). The client then stays held rather than treating the
	// missing config as "no VPN": it must never fall back to unprotected.
	VPNUnreadable bool `json:"-"`
	// ForwardedPort is the port the VPN provider forwards to this client, so
	// peers can connect to it; 0 means outgoing connections only.
	ForwardedPort int `json:"forwardedPort"`
	// VPNHeld: the VPN was removed, and downloads stay stopped until the person
	// starts them without it (Service.ReleaseVPNHold). Saved so that a restart
	// doesn't release it.
	VPNHeld bool `json:"vpnHeld"`
}

// What the status page says when a stored VPN can't be used.
const vpnUnreadableMessage = "The saved VPN config can't be read, probably because Reelkeeper's secret key changed. Upload the VPN config again."

// VPNInfo describes the stored VPN config for the browser: what it contains
// (never a secret), or why it can't be used. Both are empty without a VPN.
func (d DownloadClient) VPNInfo() (summary *builtin.VPNSummary, problem string) {
	switch {
	case d.VPNUnreadable:
		return nil, vpnUnreadableMessage
	case d.VPNConfig == "":
		return nil, ""
	}
	cfg, err := builtin.ParseWireGuard(d.VPNConfig)
	if err != nil {
		return nil, "The saved VPN config can't be used: " + err.Error()
	}
	sum := cfg.Summary()
	return &sum, ""
}

// HasVPN reports whether a VPN is set up for the built-in client (even one
// that can't be read right now).
func (d DownloadClient) HasVPN() bool { return d.VPNConfig != "" || d.VPNUnreadable }

// Configured reports whether a download client is set up. The built-in client
// has no address, so it is set up once it has a folder.
func (d DownloadClient) Configured() bool {
	if d.ClientType() == ClientBuiltin {
		return d.Folder != ""
	}
	return d.URL != ""
}

// withBuiltinDefaults gives the built-in client's settings their starting
// values: the port and the queue sizes, with no speed limits.
func (d DownloadClient) withBuiltinDefaults() DownloadClient {
	d.ListenPort = DefaultListenPort
	d.MaxActiveDownloads = DefaultMaxActiveDownloads
	d.MaxActiveSeeds = DefaultMaxActiveSeeds
	d.DownloadLimitKBs, d.UploadLimitKBs = 0, 0
	return d
}

// ClientType is Type, with the default (qBittorrent) for settings that
// have none.
func (d DownloadClient) ClientType() string {
	if d.Type == "" {
		return ClientQBittorrent
	}
	return d.Type
}

// ClientName is what messages call the client: "qBittorrent",
// "Transmission", "Deluge" or "the built-in client".
func (d DownloadClient) ClientName() string { return ClientDisplayName(d.ClientType()) }

// AddressPrompt asks for the client's address, in the client's own words.
func (d DownloadClient) AddressPrompt() string {
	switch d.ClientType() {
	case ClientTransmission:
		return "Enter Transmission's RPC address, like http://192.168.1.20:9091."
	case ClientDeluge:
		return "Enter Deluge's web UI address, like http://192.168.1.20:8112."
	case ClientBuiltin:
		return "The built-in client has no address."
	}
	return "Enter qBittorrent's Web UI address, like http://192.168.1.20:8080."
}

// ClientDisplayName is the name of a client type as a sentence uses it. The
// built-in client's is "the built-in client" (it begins no sentence in lower
// case: the code that starts a sentence with a name capitalizes it, and the
// web app labels its choices "Built-in" itself).
func ClientDisplayName(clientType string) string {
	switch clientType {
	case ClientTransmission:
		return "Transmission"
	case ClientDeluge:
		return "Deluge"
	case ClientBuiltin:
		return "the built-in client"
	}
	return "qBittorrent"
}

type storedDownloadClient struct {
	DownloadClient
	PasswordSealed  string `json:"passwordSealed"`
	VPNConfigSealed string `json:"vpnConfigSealed,omitempty"`
}

// DownloadClientUpdate is what the user can change. A nil password keeps
// the stored one.
type DownloadClientUpdate struct {
	// Type is ClientQBittorrent, ClientTransmission, ClientDeluge or
	// ClientBuiltin; empty keeps the current one.
	Type     string
	URL      string
	Username string
	Password *string
	Folder   string
	// The built-in client's settings; nil keeps the current value.
	ListenPort         *int
	MaxActiveDownloads *int
	MaxActiveSeeds     *int
	DownloadLimitKBs   *int
	UploadLimitKBs     *int
	// The built-in client's VPN. A nil VPNConfig keeps the current one; a
	// config replaces it; an empty one removes it, which needs
	// ConfirmUnprotected because peers will then see the real address.
	VPNConfig          *string
	ForwardedPort      *int
	ConfirmUnprotected bool
}

const (
	downloadClientPassword = "downloadClient.password"
	downloadClientVPN      = "downloadClient.vpnConfig"
)

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
	known := next.Type == ClientQBittorrent || next.Type == ClientTransmission || next.Type == ClientDeluge || next.Type == ClientBuiltin
	if !known {
		errs.Add("type", "Choose qBittorrent, Transmission, Deluge or Built-in.")
	}
	if next.Type == ClientBuiltin {
		// No address and no login; the folder, port and limits instead.
		next.URL, next.Username, next.Password = "", "", ""
		if oldType != ClientBuiltin {
			// Coming from another client: start from the defaults.
			next = next.withBuiltinDefaults()
		}
		setInt(&next.ListenPort, u.ListenPort)
		setInt(&next.MaxActiveDownloads, u.MaxActiveDownloads)
		setInt(&next.MaxActiveSeeds, u.MaxActiveSeeds)
		setInt(&next.DownloadLimitKBs, u.DownloadLimitKBs)
		setInt(&next.UploadLimitKBs, u.UploadLimitKBs)
		setInt(&next.ForwardedPort, u.ForwardedPort)
		applyVPN(&next, u, &errs)
		validateBuiltin(next, &errs)
	} else if known {
		next.ListenPort, next.MaxActiveDownloads, next.MaxActiveSeeds, next.DownloadLimitKBs, next.UploadLimitKBs = 0, 0, 0, 0, 0
		next.VPNConfig, next.VPNUnreadable, next.ForwardedPort, next.VPNHeld = "", false, 0, false
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

func setInt(dst *int, src *int) {
	if src != nil {
		*dst = *src
	}
}

// applyVPN works out the VPN part of an update (see DownloadClientUpdate).
func applyVPN(next *DownloadClient, u DownloadClientUpdate, errs *validation.Errors) {
	if u.VPNConfig != nil {
		text := *u.VPNConfig
		switch {
		case strings.TrimSpace(text) == "":
			if next.HasVPN() {
				if !u.ConfirmUnprotected {
					errs.Add("confirmUnprotected", "Removing the VPN means peers will see your real IP address. Confirm that you want to download without a VPN.")
					return
				}
				next.VPNConfig, next.VPNUnreadable, next.ForwardedPort = "", false, 0
				// Downloads stay stopped until the person starts them without
				// the VPN.
				next.VPNHeld = true
			}
		default:
			if _, err := builtin.ParseWireGuard(text); err != nil {
				errs.Add("vpnConfig", err.Error())
				return
			}
			next.VPNConfig, next.VPNUnreadable, next.VPNHeld = text, false, false
		}
	}
	if !next.HasVPN() {
		next.ForwardedPort = 0
		return
	}
	if next.ForwardedPort < 0 || next.ForwardedPort > 65535 {
		errs.Add("forwardedPort", "Enter the port your VPN provider forwards to you (1 to 65535), or 0 for none.")
	}
}

// validateBuiltin checks the built-in client's settings. An empty folder is
// allowed (it means the client isn't set up, like an empty address does for
// the others); one that is given must be somewhere Reelkeeper can write.
func validateBuiltin(d DownloadClient, errs *validation.Errors) {
	if d.Folder != "" {
		if !strings.HasPrefix(d.Folder, "/") {
			errs.Add("folder", "Enter a full path inside Reelkeeper's container, like /downloads.")
		} else if err := CheckWritableFolder(d.Folder); err != nil {
			errs.Add("folder", err.Error())
		}
	}
	// Behind a VPN the listen port isn't used (the provider's forwarded port
	// is), so it isn't checked.
	if !d.HasVPN() && (d.ListenPort < 1024 || d.ListenPort > 65535) {
		errs.Add("listenPort", "Enter a port from 1024 to 65535.")
	}
	if d.MaxActiveDownloads < 1 || d.MaxActiveDownloads > MaxMaxActiveDownloads {
		errs.Add("maxActiveDownloads", fmt.Sprintf("Enter a number from 1 to %d.", MaxMaxActiveDownloads))
	}
	if d.MaxActiveSeeds < 0 || d.MaxActiveSeeds > MaxMaxActiveSeeds {
		errs.Add("maxActiveSeeds", fmt.Sprintf("Enter a number from 0 (no limit) to %d.", MaxMaxActiveSeeds))
	}
	if d.DownloadLimitKBs < 0 || d.DownloadLimitKBs > maxLimitKBs {
		errs.Add("downloadLimitKBs", "Enter a speed in KB/s, or 0 for no limit.")
	}
	if d.UploadLimitKBs < 0 || d.UploadLimitKBs > maxLimitKBs {
		errs.Add("uploadLimitKBs", "Enter a speed in KB/s, or 0 for no limit.")
	}
}

// maxLimitKBs is a speed limit nobody will reach (10 GB/s); it only keeps
// typing mistakes out.
const maxLimitKBs = 10_000_000

// CheckWritableFolder says, in plain words, whether Reelkeeper can put files in
// a folder: it must exist and be a folder, and a file must be creatable in it.
func CheckWritableFolder(folder string) error {
	st, err := os.Stat(folder)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s doesn't exist inside Reelkeeper's container. Create it, or mount it, first.", folder) //nolint:staticcheck // ends with a sentence
	case err != nil:
		return fmt.Errorf("Reelkeeper can't look at %s (%w).", folder, err) //nolint:staticcheck // see above
	case !st.IsDir():
		return fmt.Errorf("%s isn't a folder.", folder) //nolint:staticcheck // see above
	}
	f, err := os.CreateTemp(folder, ".reelkeeper-write-test-*")
	if err != nil {
		return fmt.Errorf("Reelkeeper can't write to %s (%w). Check the folder's permissions for the user Reelkeeper runs as.", folder, errors.Unwrap(err)) //nolint:staticcheck // see above
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
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
	if !next.Configured() {
		reset := DownloadClient{Type: next.Type}
		if next.Type == ClientBuiltin {
			reset = reset.withBuiltinDefaults()
			// The VPN outlives the folder. Clearing the folder must not be a
			// way round the confirmation that removing the VPN needs, or round
			// the hold that follows it, and a VPN entered before the folder is
			// kept for when the folder is filled in.
			reset.VPNConfig, reset.VPNUnreadable, reset.ForwardedPort, reset.VPNHeld = next.VPNConfig, next.VPNUnreadable, next.ForwardedPort, next.VPNHeld
		}
		next = reset
	}
	s.mu.Lock()
	if u.Password != nil || !next.Configured() || next.Type != s.downloadClient.ClientType() {
		delete(s.sealedKeep, downloadClientPassword)
	}
	// A VPN that can't be read is kept (sealed as it was) until it is
	// replaced or removed, or the client type changes.
	if u.VPNConfig != nil || next.Type != s.downloadClient.ClientType() {
		delete(s.sealedKeep, downloadClientVPN)
	}
	err = s.storeDownloadClient(ctx, next)
	if err != nil {
		s.mu.Unlock()
		return DownloadClient{}, err
	}
	s.downloadClient = next
	s.mu.Unlock()
	s.notify(GroupDownloadClient)
	return next, nil
}

// storeDownloadClient writes the settings, encrypting the password and the
// VPN config. The caller holds the lock.
func (s *Service) storeDownloadClient(ctx context.Context, next DownloadClient) error {
	sealed, err := s.sealOrKeep(downloadClientPassword, next.Password)
	if err != nil {
		return err
	}
	vpn, err := s.sealOrKeep(downloadClientVPN, next.VPNConfig)
	if err != nil {
		return err
	}
	if next.VPNConfig == "" && s.sealedKeep[downloadClientVPN] == "" {
		vpn = "" // nothing stored, nothing to keep
	}
	return s.put(ctx, GroupDownloadClient, storedDownloadClient{DownloadClient: next, PasswordSealed: sealed, VPNConfigSealed: vpn})
}

// ReleaseVPNHold lets the built-in client's downloads start again after the
// VPN was removed ("Start without VPN"). It is refused while a VPN is set up.
func (s *Service) ReleaseVPNHold(ctx context.Context) (DownloadClient, error) {
	s.mu.Lock()
	next := s.downloadClient
	if next.HasVPN() {
		s.mu.Unlock()
		return DownloadClient{}, errors.New("a VPN is set up, so downloads aren't held; remove it first to download without one")
	}
	if !next.VPNHeld {
		s.mu.Unlock()
		return next, nil
	}
	next.VPNHeld = false
	if err := s.storeDownloadClient(ctx, next); err != nil {
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
	if stored.VPNConfigSealed != "" {
		if cfg, err := s.box.Open(stored.VPNConfigSealed); err != nil {
			if errors.Is(err, secrets.ErrUndecryptable) {
				s.sealedKeep[downloadClientVPN] = stored.VPNConfigSealed
			}
			// The VPN is still set up, it just can't be read: it must not read
			// as "no VPN".
			d.VPNUnreadable = true
			s.logger.Error("can't read a stored credential", "setting", downloadClientVPN, "error", err)
		} else {
			d.VPNConfig = cfg
		}
	}
	s.downloadClient = d
	return nil
}
