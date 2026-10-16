package settings

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// maxNetworks caps the bypass and trusted-proxy lists; anything longer is
// almost certainly a paste mistake.
const maxNetworks = 100

// Security holds Settings → Security.
type Security struct {
	// LocalLoginEnabled allows username and password sign-in.
	LocalLoginEnabled bool `json:"localLoginEnabled"`
	// BypassNetworks can use Reelkeeper without signing in. Canonical CIDR form.
	BypassNetworks []string `json:"bypassNetworks"`
	// TrustedProxies are reverse proxies whose X-Forwarded-For is believed.
	// Canonical CIDR form.
	TrustedProxies []string `json:"trustedProxies"`
	// SessionDays is how long a sign-in lasts.
	SessionDays int `json:"sessionDays"`
	// APIKey is decrypted in memory and encrypted in the database.
	APIKey string `json:"-"`
	OIDC   OIDC   `json:"oidc"`
}

// OIDC is single sign-on through an OpenID Connect provider such as
// Authentik.
type OIDC struct {
	Enabled   bool   `json:"enabled"`
	IssuerURL string `json:"issuerUrl"`
	ClientID  string `json:"clientId"`
	// ClientSecret is decrypted in memory and encrypted in the database.
	ClientSecret  string   `json:"-"`
	Scopes        []string `json:"scopes"`
	GroupsClaim   string   `json:"groupsClaim"`
	RequiredGroup string   `json:"requiredGroup"`
	ButtonLabel   string   `json:"buttonLabel"`
	// LastSuccessAt is when someone last signed in through this provider.
	// Reset when the issuer or client ID changes.
	LastSuccessAt *time.Time `json:"lastSuccessAt,omitempty"`
}

// storedSecurity is the database form: credentials replaced by their
// encrypted versions.
type storedSecurity struct {
	Security
	APIKeySealed       string `json:"apiKeySealed"`
	ClientSecretSealed string `json:"oidcClientSecretSealed"`
}

// SecurityUpdate is what the user can change. OIDC.ClientSecret nil keeps
// the stored secret; the API key changes only through RegenerateAPIKey.
type SecurityUpdate struct {
	LocalLoginEnabled bool
	BypassNetworks    []string
	TrustedProxies    []string
	SessionDays       int
	OIDC              OIDCUpdate
}

// OIDCUpdate is the user-editable part of OIDC.
type OIDCUpdate struct {
	Enabled       bool
	IssuerURL     string
	ClientID      string
	ClientSecret  *string
	Scopes        []string
	GroupsClaim   string
	RequiredGroup string
	ButtonLabel   string
}

// Defaults for OIDC fields left empty.
const (
	DefaultGroupsClaim = "groups"
	DefaultButtonLabel = "Sign in with SSO"
)

// DefaultScopes are requested when the user doesn't choose others.
var DefaultScopes = []string{"openid", "profile", "email"}

// DefaultSecurity returns the settings used until the user saves any.
func DefaultSecurity() Security {
	return Security{
		LocalLoginEnabled: true,
		BypassNetworks:    []string{},
		TrustedProxies:    []string{},
		SessionDays:       30,
		OIDC: OIDC{
			Scopes:      slices.Clone(DefaultScopes),
			GroupsClaim: DefaultGroupsClaim,
			ButtonLabel: DefaultButtonLabel,
		},
	}
}

// NewAPIKey returns a random 32-character hex key, the same shape as
// Radarr's, so tools that sanity-check the format accept it.
func NewAPIKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ParsePrefix accepts an IP address ("172.16.0.2") or CIDR range
// ("192.168.1.0/24") and returns it as a canonical prefix. A single
// address becomes a /32 (or /128 for IPv6). IPv4-mapped IPv6 addresses are
// treated as IPv4.
func ParsePrefix(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("%q is not a valid IP range", s)
		}
		addr := p.Addr()
		bits := p.Bits()
		if addr.Is4In6() {
			addr = addr.Unmap()
			bits -= 96
			if bits < 0 {
				return netip.Prefix{}, fmt.Errorf("%q is not a valid IP range", s)
			}
		}
		return netip.PrefixFrom(addr.WithZone(""), bits).Masked(), nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not a valid IP address", s)
	}
	addr = addr.Unmap().WithZone("")
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// Prefixes parses a list already validated by SaveSecurity. Entries that
// fail to parse (impossible after validation) are skipped.
func Prefixes(list []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		if p, err := ParsePrefix(s); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// normalizeNetworks validates and canonicalizes a list, dropping blanks and
// duplicates.
func normalizeNetworks(field string, in []string, errs *validation.Errors) ([]string, []netip.Prefix) {
	out := []string{}
	var prefixes []netip.Prefix
	if len(in) > maxNetworks {
		errs.Add(field, fmt.Sprintf("Enter at most %d entries.", maxNetworks))
		return out, nil
	}
	for i, raw := range in {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		p, err := ParsePrefix(raw)
		if err != nil {
			errs.Add(fmt.Sprintf("%s.%d", field, i), err.Error()+". Use an address like 10.0.0.5 or a range like 192.168.1.0/24.")
			continue
		}
		if p.Bits() == 0 {
			errs.Add(fmt.Sprintf("%s.%d", field, i), fmt.Sprintf("%s covers every address on the internet.", p))
			continue
		}
		if slices.Contains(out, p.String()) {
			continue
		}
		out = append(out, p.String())
		prefixes = append(prefixes, p)
	}
	return out, prefixes
}

// validate checks a complete Security value, canonicalizes lists and fills
// in defaults for empty optional fields.
func (sec *Security) validate() validation.Errors {
	var errs validation.Errors

	var bypass, proxies []netip.Prefix
	sec.BypassNetworks, bypass = normalizeNetworks("bypassNetworks", sec.BypassNetworks, &errs)
	sec.TrustedProxies, proxies = normalizeNetworks("trustedProxies", sec.TrustedProxies, &errs)

	// The Traefik trap: every request through a reverse proxy arrives from
	// the proxy's address, so bypassing it would let everyone in.
	for i, b := range bypass {
		for _, p := range proxies {
			if b.Overlaps(p) {
				errs.Add(fmt.Sprintf("bypassNetworks.%d", i), fmt.Sprintf(
					"%s includes the trusted proxy %s. Everything coming through the proxy would skip sign-in. "+
						"List the networks your devices are on instead.", b, p))
				break
			}
		}
	}

	if sec.SessionDays < 1 || sec.SessionDays > 365 {
		errs.Add("sessionDays", "Choose between 1 and 365 days.")
	}

	o := &sec.OIDC
	o.IssuerURL = strings.TrimSpace(o.IssuerURL)
	o.ClientID = strings.TrimSpace(o.ClientID)
	o.GroupsClaim = strings.TrimSpace(o.GroupsClaim)
	o.RequiredGroup = strings.TrimSpace(o.RequiredGroup)
	o.ButtonLabel = strings.TrimSpace(o.ButtonLabel)
	if o.GroupsClaim == "" {
		o.GroupsClaim = DefaultGroupsClaim
	}
	if o.ButtonLabel == "" {
		o.ButtonLabel = DefaultButtonLabel
	}
	o.Scopes = normalizeScopes(o.Scopes)

	if o.Enabled {
		if msg := checkIssuerURL(o.IssuerURL); msg != "" {
			errs.Add("oidc.issuerUrl", msg)
		}
		if o.ClientID == "" {
			errs.Add("oidc.clientId", "Enter the client ID from your identity provider.")
		}
	}

	if !sec.LocalLoginEnabled {
		switch {
		case !o.Enabled:
			errs.Add("localLoginEnabled", "Password sign-in can't be turned off unless single sign-on is on. Otherwise nobody could sign in.")
		case o.LastSuccessAt == nil:
			errs.Add("localLoginEnabled", "Sign in with single sign-on successfully at least once before turning off password sign-in, so you can't lock yourself out.")
		}
	}
	return errs
}

// normalizeScopes trims, drops blanks and duplicates, and makes sure
// "openid" is present (OIDC requires it).
func normalizeScopes(in []string) []string {
	out := []string{"openid"}
	for _, s := range in {
		for f := range strings.FieldsSeq(s) {
			if !slices.Contains(out, f) {
				out = append(out, f)
			}
		}
	}
	if len(out) == 1 && len(in) == 0 {
		return slices.Clone(DefaultScopes)
	}
	return out
}

// checkIssuerURL returns a problem with the issuer URL, or "".
// Plain http is allowed only for providers on this machine (testing).
func checkIssuerURL(raw string) string {
	if raw == "" {
		return "Enter the issuer URL from your identity provider."
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "Enter a full URL, such as https://auth.example.com/application/o/reelkeeper/."
	}
	switch u.Scheme {
	case "https":
		return ""
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return ""
		}
		if ip, err := netip.ParseAddr(host); err == nil && ip.IsLoopback() {
			return ""
		}
		return "Use https. Sign-in tokens must not travel unencrypted."
	default:
		return "Use an https:// URL."
	}
}

// Security returns the current security settings.
func (s *Service) Security() Security {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sec := s.security
	sec.BypassNetworks = slices.Clone(sec.BypassNetworks)
	sec.TrustedProxies = slices.Clone(sec.TrustedProxies)
	sec.OIDC.Scopes = slices.Clone(sec.OIDC.Scopes)
	return sec
}

// SaveSecurity validates and stores u, returning what was stored. Invalid
// input, including changes that would lock everyone out, returns
// validation.Errors and changes nothing.
func (s *Service) SaveSecurity(ctx context.Context, u SecurityUpdate) (Security, error) {
	s.mu.Lock()
	cur := s.security
	next := cur
	next.LocalLoginEnabled = u.LocalLoginEnabled
	next.BypassNetworks = u.BypassNetworks
	next.TrustedProxies = u.TrustedProxies
	next.SessionDays = u.SessionDays
	next.OIDC = OIDC{
		Enabled:       u.OIDC.Enabled,
		IssuerURL:     u.OIDC.IssuerURL,
		ClientID:      u.OIDC.ClientID,
		ClientSecret:  cur.OIDC.ClientSecret,
		Scopes:        u.OIDC.Scopes,
		GroupsClaim:   u.OIDC.GroupsClaim,
		RequiredGroup: u.OIDC.RequiredGroup,
		ButtonLabel:   u.OIDC.ButtonLabel,
		LastSuccessAt: cur.OIDC.LastSuccessAt,
	}
	secretReplaced := false
	if u.OIDC.ClientSecret != nil {
		next.OIDC.ClientSecret = *u.OIDC.ClientSecret
		secretReplaced = true
	}
	// A different provider or client hasn't been proven to work yet.
	if strings.TrimSpace(next.OIDC.IssuerURL) != cur.OIDC.IssuerURL ||
		strings.TrimSpace(next.OIDC.ClientID) != cur.OIDC.ClientID {
		next.OIDC.LastSuccessAt = nil
	}

	if errs := next.validate(); errs != nil {
		s.mu.Unlock()
		return Security{}, errs
	}
	if secretReplaced {
		delete(s.sealedKeep, "oidc.clientSecret")
	}
	if err := s.saveSecurity(ctx, next); err != nil {
		s.mu.Unlock()
		return Security{}, err
	}
	s.security = next
	s.mu.Unlock()
	s.notify(GroupSecurity)
	return s.Security(), nil
}

// RegenerateAPIKey replaces the API key and returns the new one.
func (s *Service) RegenerateAPIKey(ctx context.Context) (string, error) {
	return s.updateSecurity(ctx, func(sec *Security) {
		sec.APIKey = NewAPIKey()
		delete(s.sealedKeep, "apiKey")
	}, func(sec Security) string { return sec.APIKey })
}

// RecordOIDCSuccess notes a successful single sign-on, which unlocks
// turning off password sign-in.
func (s *Service) RecordOIDCSuccess(ctx context.Context, at time.Time) error {
	at = at.UTC().Truncate(time.Second)
	_, err := s.updateSecurity(ctx, func(sec *Security) { sec.OIDC.LastSuccessAt = &at }, nil)
	return err
}

// EnableLocalLogin turns password sign-in back on. Used by
// `reelkeeper reset-auth` when single sign-on is broken.
func (s *Service) EnableLocalLogin(ctx context.Context) error {
	_, err := s.updateSecurity(ctx, func(sec *Security) { sec.LocalLoginEnabled = true }, nil)
	return err
}

// updateSecurity applies an internal change that needs no validation.
func (s *Service) updateSecurity(ctx context.Context, change func(*Security), result func(Security) string) (string, error) {
	s.mu.Lock()
	next := s.security
	change(&next)
	if err := s.saveSecurity(ctx, next); err != nil {
		s.mu.Unlock()
		return "", err
	}
	s.security = next
	s.mu.Unlock()
	s.notify(GroupSecurity)
	if result == nil {
		return "", nil
	}
	return result(next), nil
}

// saveSecurity encrypts the credentials in sec and writes it. Callers hold
// s.mu (or are in New, before the Service is shared).
func (s *Service) saveSecurity(ctx context.Context, sec Security) error {
	stored := storedSecurity{Security: sec}
	var err error
	if stored.APIKeySealed, err = s.sealOrKeep("apiKey", sec.APIKey); err != nil {
		return err
	}
	if stored.ClientSecretSealed, err = s.sealOrKeep("oidc.clientSecret", sec.OIDC.ClientSecret); err != nil {
		return err
	}
	return s.put(ctx, GroupSecurity, stored)
}

// sealOrKeep encrypts plain, or, if plain is empty because the stored value
// couldn't be decrypted, writes the old encrypted value back untouched.
func (s *Service) sealOrKeep(field, plain string) (string, error) {
	if plain == "" {
		if kept, ok := s.sealedKeep[field]; ok {
			return kept, nil
		}
	}
	return s.box.Seal(plain)
}

// openSecurity decrypts the credentials in stored. A value that can't be
// decrypted is logged, treated as empty, and remembered so it isn't lost.
func (s *Service) openSecurity(stored storedSecurity) Security {
	sec := stored.Security
	open := func(field, sealed string) string {
		plain, err := s.box.Open(sealed)
		if err != nil {
			if errors.Is(err, secrets.ErrUndecryptable) {
				s.sealedKeep[field] = sealed
			}
			s.logger.Error("can't read a stored credential", "setting", field, "error", err)
			return ""
		}
		return plain
	}
	sec.APIKey = open("apiKey", stored.APIKeySealed)
	sec.OIDC.ClientSecret = open("oidc.clientSecret", stored.ClientSecretSealed)
	return sec
}

// UnreadableCredentials lists credentials that couldn't be decrypted with
// the current secret key, by field path, so the UI can ask for them again.
func (s *Service) UnreadableCredentials() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.sealedKeep))
	for k := range s.sealedKeep {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
