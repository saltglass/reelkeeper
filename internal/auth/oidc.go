package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/uptrace/bun"
	"golang.org/x/oauth2"

	"github.com/saltglass/reelkeeper/internal/settings"
)

// OIDCStateCookie ties a sign-in attempt to the browser that started it,
// so an attacker can't trick someone into finishing the attacker's sign-in.
const OIDCStateCookie = "reelkeeper_oidc_state"

// CallbackPath is the single sign-on return address below the URL base.
const CallbackPath = "/api/v1/auth/oidc/callback"

const (
	pendingTTL       = 10 * time.Minute
	maxPending       = 1000
	providerCacheTTL = time.Hour
)

// OIDCError is a single sign-on failure with a code the login page can
// show a message for.
type OIDCError struct {
	// Code is one of: sso_disabled, sso_expired, sso_denied, sso_failed,
	// sso_link_conflict.
	Code string
	// Message is safe to show to the user.
	Message string
	Err     error
}

func (e *OIDCError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *OIDCError) Unwrap() error { return e.Err }

type pendingLogin struct {
	verifier    string
	nonce       string
	returnTo    string
	redirectURL string
	issuer      string
	clientID    string
	created     time.Time
	// linkUserID is set when a signed-in account is linking single sign-on
	// to itself instead of signing in.
	linkUserID int64
}

type cachedProvider struct {
	provider *oidc.Provider
	fetched  time.Time
}

// oidcFlow holds in-flight sign-ins (in memory: they last minutes) and
// cached provider discovery documents.
type oidcFlow struct {
	svc    *Service
	client *http.Client

	mu        sync.Mutex
	pending   map[string]pendingLogin
	providers map[string]cachedProvider
}

func newOIDCFlow(svc *Service) *oidcFlow {
	return &oidcFlow{
		svc:       svc,
		client:    &http.Client{Timeout: 15 * time.Second},
		pending:   map[string]pendingLogin{},
		providers: map[string]cachedProvider{},
	}
}

// SetHTTPClient replaces the client used to talk to the identity provider
// (tests use it to reach a fake provider).
func (s *Service) SetHTTPClient(c *http.Client) { s.oidc.client = c }

func (f *oidcFlow) ctx(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, f.client)
}

func (f *oidcFlow) provider(ctx context.Context, issuer string) (*oidc.Provider, error) {
	f.mu.Lock()
	c, ok := f.providers[issuer]
	f.mu.Unlock()
	if ok && time.Since(c.fetched) < providerCacheTTL {
		return c.provider, nil
	}
	p, err := oidc.NewProvider(f.ctx(ctx), issuer)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.providers[issuer] = cachedProvider{provider: p, fetched: time.Now()}
	f.mu.Unlock()
	return p, nil
}

// StartOIDC begins a single sign-on. It returns the provider URL to send
// the browser to, and the state value to put in OIDCStateCookie.
func (s *Service) StartOIDC(ctx context.Context, redirectURL, returnTo string) (authURL, state string, err error) {
	return s.startOIDC(ctx, redirectURL, returnTo, 0)
}

// StartOIDCLink begins linking single sign-on to the signed-in account
// userID. FinishOIDC completes it.
func (s *Service) StartOIDCLink(ctx context.Context, redirectURL, returnTo string, userID int64) (authURL, state string, err error) {
	u, err := s.userByID(ctx, userID)
	if err != nil {
		return "", "", err
	}
	if !u.HasPassword() {
		return "", "", ErrNoPassword
	}
	return s.startOIDC(ctx, redirectURL, returnTo, userID)
}

func (s *Service) startOIDC(ctx context.Context, redirectURL, returnTo string, linkUserID int64) (authURL, state string, err error) {
	o := s.settings.Security().OIDC
	if !o.Enabled {
		return "", "", &OIDCError{Code: "sso_disabled", Message: "Single sign-on is not set up"}
	}
	p, err := s.oidc.provider(ctx, o.IssuerURL)
	if err != nil {
		return "", "", &OIDCError{Code: "sso_failed", Message: "Couldn't reach the identity provider", Err: err}
	}

	state = randomString(32)
	nonce := randomString(32)
	verifier := oauth2.GenerateVerifier()
	conf := oauthConfig(o, p, redirectURL)

	f := s.oidc
	f.mu.Lock()
	f.prunePending()
	if len(f.pending) >= maxPending {
		f.mu.Unlock()
		return "", "", &OIDCError{Code: "sso_failed", Message: "Too many sign-ins in progress; try again in a few minutes"}
	}
	f.pending[state] = pendingLogin{
		verifier: verifier, nonce: nonce, returnTo: SafeReturnPath(returnTo),
		redirectURL: redirectURL, issuer: o.IssuerURL, clientID: o.ClientID, created: time.Now(),
		linkUserID: linkUserID,
	}
	f.mu.Unlock()

	return conf.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), state, nil
}

// FinishOIDC completes a single sign-on: it checks the state against the
// browser's cookie, exchanges the code (with the PKCE verifier), verifies the
// ID token, enforces the required group, and starts a session. It returns
// the session token and the path to send the browser to.
func (s *Service) FinishOIDC(ctx context.Context, stateParam, stateCookie, code string, c Client) (token, returnTo string, err error) {
	f := s.oidc
	if stateParam == "" || subtle.ConstantTimeCompare([]byte(stateParam), []byte(stateCookie)) != 1 {
		return "", "", &OIDCError{Code: "sso_expired", Message: "The sign-in attempt expired or was started in another browser. Please try again"}
	}
	f.mu.Lock()
	pl, ok := f.pending[stateParam]
	delete(f.pending, stateParam)
	f.mu.Unlock()
	if !ok || time.Since(pl.created) > pendingTTL {
		return "", "", &OIDCError{Code: "sso_expired", Message: "The sign-in attempt expired. Please try again"}
	}

	o := s.settings.Security().OIDC
	if !o.Enabled || o.IssuerURL != pl.issuer || o.ClientID != pl.clientID {
		return "", "", &OIDCError{Code: "sso_expired", Message: "Single sign-on settings changed during sign-in. Please try again"}
	}
	p, err := f.provider(ctx, o.IssuerURL)
	if err != nil {
		return "", "", &OIDCError{Code: "sso_failed", Message: "Couldn't reach the identity provider", Err: err}
	}

	conf := oauthConfig(o, p, pl.redirectURL)
	tok, err := conf.Exchange(f.ctx(ctx), code, oauth2.VerifierOption(pl.verifier))
	if err != nil {
		return "", "", &OIDCError{Code: "sso_failed", Message: "The identity provider rejected the sign-in. Check the client ID and secret", Err: err}
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return "", "", &OIDCError{Code: "sso_failed", Message: "The identity provider didn't return an ID token"}
	}
	idToken, err := p.Verifier(&oidc.Config{ClientID: o.ClientID}).Verify(f.ctx(ctx), rawID)
	if err != nil {
		return "", "", &OIDCError{Code: "sso_failed", Message: "The ID token from the identity provider isn't valid", Err: err}
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(pl.nonce)) != 1 {
		return "", "", &OIDCError{Code: "sso_failed", Message: "The ID token doesn't match this sign-in attempt"}
	}

	claims := map[string]any{}
	if err := idToken.Claims(&claims); err != nil {
		return "", "", &OIDCError{Code: "sso_failed", Message: "Couldn't read the ID token", Err: err}
	}
	// Some providers only put groups in the userinfo response.
	if _, has := claims[o.GroupsClaim]; !has && o.RequiredGroup != "" {
		if ui, err := p.UserInfo(f.ctx(ctx), oauth2.StaticTokenSource(tok)); err == nil {
			extra := map[string]any{}
			if ui.Claims(&extra) == nil && ui.Subject == idToken.Subject {
				if g, ok := extra[o.GroupsClaim]; ok {
					claims[o.GroupsClaim] = g
				}
			}
		}
	}

	if o.RequiredGroup != "" && !hasGroup(claims[o.GroupsClaim], o.RequiredGroup) {
		s.logger.Warn("single sign-on refused: not in the required group",
			"subject", idToken.Subject, "username", claims["preferred_username"], "group", o.RequiredGroup, "ip", c.IP)
		return "", "", &OIDCError{Code: "sso_denied", Message: fmt.Sprintf("Your account isn't in the %q group, which is required to use Reelkeeper", o.RequiredGroup)}
	}

	var u *User
	if pl.linkUserID != 0 {
		u, err = s.linkIdentity(ctx, pl.linkUserID, o.IssuerURL, idToken.Subject)
	} else {
		u, err = s.oidcUser(ctx, o.IssuerURL, idToken.Subject, claims)
	}
	if err != nil {
		return "", "", err
	}
	if err := s.settings.RecordOIDCSuccess(ctx, s.now()); err != nil {
		s.logger.Warn("can't record single sign-on success", "error", err)
	}
	s.touchLogin(ctx, u)
	token, err = s.createSession(ctx, u.ID, c)
	if err != nil {
		return "", "", err
	}
	s.logger.Info("signed in", "username", u.Username, "method", "sso", "ip", c.IP)
	return token, pl.returnTo, nil
}

// oidcUser finds the account linked to this provider identity, creating
// one on first sign-in. Accounts are never linked by username alone: a
// provider account named "dan" does not take over a local account "dan".
func (s *Service) oidcUser(ctx context.Context, issuer, subject string, claims map[string]any) (*User, error) {
	u := new(User)
	err := s.db.DB.NewSelect().Model(u).Where("oidc_issuer = ? AND oidc_subject = ?", issuer, subject).Scan(ctx)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	base := firstString(claims, "preferred_username", "email", "name")
	if base == "" {
		base = "sso-user"
	}
	if len(base) > 50 {
		base = base[:50]
	}
	now := s.now().UTC()
	for i := 0; i < 20; i++ {
		name := base
		switch {
		case i == 1:
			name = base + "-sso"
		case i > 1:
			name = fmt.Sprintf("%s-sso%d", base, i)
		}
		u = &User{Username: name, OIDCIssuer: &issuer, OIDCSubject: &subject, CreatedAt: now}
		if _, err := s.db.DB.NewInsert().Model(u).Exec(ctx); err == nil {
			s.logger.Info("account created through single sign-on", "username", name)
			return u, nil
		}
		// Most likely the username is taken; try the next variant.
		if _, lookupErr := s.userByName(ctx, name); lookupErr != nil {
			return nil, fmt.Errorf("create single sign-on account: %w", err)
		}
	}
	return nil, errors.New("create single sign-on account: no free username")
}

// linkIdentity attaches a provider identity to the account userID, so both
// sign-in methods reach it. If the identity belongs to an account that only
// signs in through single sign-on (made by an earlier sign-in before the
// accounts were linked), that account is merged in: it is removed and its
// sessions end. An identity owned by another password account is refused.
func (s *Service) linkIdentity(ctx context.Context, userID int64, issuer, subject string) (*User, error) {
	var target *User
	var merged string
	err := s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		target = new(User)
		if err := tx.NewSelect().Model(target).Where("id = ?", userID).Scan(ctx); err != nil {
			return err
		}
		owner := new(User)
		err := tx.NewSelect().Model(owner).Where("oidc_issuer = ? AND oidc_subject = ?", issuer, subject).Scan(ctx)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		case owner.ID == target.ID:
			return nil // already linked
		case owner.HasPassword():
			return &OIDCError{Code: "sso_link_conflict", Message: fmt.Sprintf(
				"That single sign-on account is already linked to the Reelkeeper account %q", owner.Username)}
		default:
			if _, err := tx.NewDelete().Model(owner).WherePK().Exec(ctx); err != nil {
				return err
			}
			merged = owner.Username
		}
		target.OIDCIssuer, target.OIDCSubject = &issuer, &subject
		_, err = tx.NewUpdate().Model(target).Column("oidc_issuer", "oidc_subject").WherePK().Exec(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	if merged != "" {
		s.logger.Info("single sign-on linked; merged the separate single sign-on account", "username", target.Username, "removed", merged)
	} else {
		s.logger.Info("single sign-on linked", "username", target.Username)
	}
	return target, nil
}

// CheckOIDC fetches an issuer's discovery document and explains the result.
func (s *Service) CheckOIDC(ctx context.Context, issuer string) (ok bool, message string) {
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		return false, "Enter the issuer URL first."
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(s.oidc.ctx(ctx), issuer)
	if err != nil {
		var mismatch *oidc.IssuerMismatchError
		if errors.As(err, &mismatch) {
			return false, fmt.Sprintf("The provider calls itself %q. Enter exactly that, including any trailing slash.", mismatch.Discovered)
		}
		return false, "Couldn't load the provider's configuration from " +
			strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration: " + err.Error()
	}
	var meta struct {
		Methods []string `json:"code_challenge_methods_supported"`
	}
	_ = p.Claims(&meta)
	msg := "Found the provider. Sign-in page: " + p.Endpoint().AuthURL
	if len(meta.Methods) > 0 && !containsFold(meta.Methods, "S256") {
		msg += ". Warning: it doesn't advertise PKCE (S256), which Reelkeeper uses; sign-in may fail."
	}
	return true, msg
}

// CallbackURL is the address to register with the identity provider,
// derived from how the browser reached Reelkeeper (through a trusted proxy's
// forwarded headers, if any). A forged Host header can't be abused here:
// the provider only redirects to addresses registered with it.
func CallbackURL(r *http.Request, trusted []netip.Prefix, urlBase string) string {
	scheme := "http"
	if IsHTTPS(r, trusted) {
		scheme = "https"
	}
	return scheme + "://" + ExternalHost(r, trusted) + urlBase + CallbackPath
}

func oauthConfig(o settings.OIDC, p *oidc.Provider, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     o.ClientID,
		ClientSecret: o.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  redirectURL,
		Scopes:       o.Scopes,
	}
}

// prunePending drops expired sign-in attempts. Caller holds f.mu.
func (f *oidcFlow) prunePending() {
	for k, v := range f.pending {
		if time.Since(v.created) > pendingTTL {
			delete(f.pending, k)
		}
	}
}

// SafeReturnPath keeps post-login redirects inside Reelkeeper: only a path that
// starts with a single "/" is allowed, so "//evil.example" and
// "https://evil.example" become "/".
func SafeReturnPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") ||
		strings.ContainsAny(p, "\r\n") {
		return "/"
	}
	return p
}

// hasGroup accepts the groups claim as a list of strings or a single string.
func hasGroup(claim any, group string) bool {
	switch v := claim.(type) {
	case string:
		return v == group
	case []any:
		for _, g := range v {
			if s, ok := g.(string); ok && s == group {
				return true
			}
		}
	case []string:
		for _, s := range v {
			if s == group {
				return true
			}
		}
	}
	return false
}

func firstString(claims map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := claims[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
