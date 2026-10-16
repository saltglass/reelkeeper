package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
)

// fakeProvider is a minimal OpenID Connect provider for tests. It signs ID
// tokens with its own RSA key, checks PKCE and the client secret, and lets
// each test choose the claims and misbehave on purpose.
type fakeProvider struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	issuer string // what it reports as its issuer

	clientID, clientSecret string

	mu    sync.Mutex
	codes map[string]authRequest
	// Knobs for tests:
	claims         map[string]any // extra ID token claims
	userinfoClaims map[string]any
	wrongNonce     bool
	wrongAudience  bool
	tokenCalls     int
}

type authRequest struct {
	nonce, challenge, redirectURI string
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{
		t: t, key: key, clientID: "reelkeeper", clientSecret: "s3cret",
		codes:  map[string]authRequest{},
		claims: map[string]any{"preferred_username": "dan", "groups": []string{"media-admins"}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("/jwks", p.jwks)
	mux.HandleFunc("/token", p.token)
	mux.HandleFunc("/userinfo", p.userinfo)
	p.srv = httptest.NewServer(mux)
	p.issuer = p.srv.URL + "/"
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeProvider) discovery(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                p.issuer,
		"authorization_endpoint":                p.srv.URL + "/authorize",
		"token_endpoint":                        p.srv.URL + "/token",
		"jwks_uri":                              p.srv.URL + "/jwks",
		"userinfo_endpoint":                     p.srv.URL + "/userinfo",
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (p *fakeProvider) jwks(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
		"n": b64(p.key.N.Bytes()), "e": b64(big.NewInt(int64(p.key.E)).Bytes()),
	}}})
}

// authorize plays the part of the user signing in at the provider: it reads
// the URL Reelkeeper redirected to and returns the code the provider would send
// back, after checking Reelkeeper asked properly.
func (p *fakeProvider) authorize(authURL string) (code, state string) {
	p.t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		p.t.Fatal(err)
	}
	q := u.Query()
	if q.Get("client_id") != p.clientID || q.Get("response_type") != "code" {
		p.t.Fatalf("bad authorize request: %s", authURL)
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		p.t.Fatalf("PKCE missing from authorize request: %s", authURL)
	}
	if !strings.Contains(q.Get("scope"), "openid") {
		p.t.Fatalf("openid scope missing: %s", q.Get("scope"))
	}
	code = randomString(16)
	p.mu.Lock()
	p.codes[code] = authRequest{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirectURI: q.Get("redirect_uri")}
	p.mu.Unlock()
	return code, q.Get("state")
}

func (p *fakeProvider) token(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokenCalls++
	_ = r.ParseForm()
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.clientID || secret != p.clientSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	req, ok := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	if !ok || r.PostForm.Get("redirect_uri") != req.redirectURI {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if b64(sum[:]) != req.challenge {
		http.Error(w, `{"error":"invalid_grant","error_description":"PKCE verification failed"}`, http.StatusBadRequest)
		return
	}

	claims := map[string]any{
		"iss": p.issuer, "aud": p.clientID, "sub": "user-123",
		"iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(),
		"nonce": req.nonce,
	}
	if p.wrongNonce {
		claims["nonce"] = "something-else"
	}
	if p.wrongAudience {
		claims["aud"] = "another-app"
	}
	for k, v := range p.claims {
		claims[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "access-" + randomString(8), "token_type": "Bearer", "expires_in": 300,
		"id_token": p.sign(claims),
	})
}

func (p *fakeProvider) userinfo(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{"sub": "user-123"}
	for k, v := range p.userinfoClaims {
		out[k] = v
	}
	_ = json.NewEncoder(w).Encode(out)
}

// sign makes an RS256 JWT.
func (p *fakeProvider) sign(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	body, _ := json.Marshal(claims)
	signing := b64(header) + "." + b64(body)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		p.t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// oidcFixture configures Reelkeeper to use the fake provider.
func oidcFixture(t *testing.T, db *store.Store, requiredGroup string) (*fixture, *fakeProvider) {
	t.Helper()
	f := newFixture(t, db)
	p := newFakeProvider(t)
	secret := p.clientSecret
	f.saveSecurity(t, func(u *settings.SecurityUpdate) {
		u.OIDC = settings.OIDCUpdate{
			Enabled: true, IssuerURL: p.issuer, ClientID: p.clientID, ClientSecret: &secret,
			Scopes: []string{"openid", "profile", "email"}, GroupsClaim: "groups", RequiredGroup: requiredGroup,
		}
	})
	return f, p
}

const redirect = "https://reelkeeper.example/api/v1/auth/oidc/callback"

// signIn runs a whole single sign-on and returns FinishOIDC's results.
func signIn(t *testing.T, f *fixture, p *fakeProvider, returnTo string) (token, back string, err error) {
	t.Helper()
	ctx := context.Background()
	authURL, state, err := f.auth.StartOIDC(ctx, redirect, returnTo)
	if err != nil {
		t.Fatal(err)
	}
	code, gotState := p.authorize(authURL)
	if gotState != state {
		t.Fatalf("state in URL %q != cookie state %q", gotState, state)
	}
	return f.auth.FinishOIDC(ctx, state, state, code, client)
}

func oidcCode(err error) string {
	var oe *OIDCError
	if errors.As(err, &oe) {
		return oe.Code
	}
	return ""
}

func TestOIDCHappyPath(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, p := oidcFixture(t, db, "media-admins")

		token, back, err := signIn(t, f, p, "/settings/security")
		if err != nil {
			t.Fatal(err)
		}
		if back != "/settings/security" {
			t.Errorf("returnTo = %q", back)
		}
		u, _ := f.auth.SessionUser(ctx, token)
		if u == nil || u.Username != "dan" || u.HasPassword() {
			t.Fatalf("user = %+v", u)
		}
		if f.settings.Security().OIDC.LastSuccessAt == nil {
			t.Error("a successful sign-in should be recorded")
		}

		// Signing in again reuses the same account.
		token2, _, err := signIn(t, f, p, "")
		if err != nil {
			t.Fatal(err)
		}
		u2, _ := f.auth.SessionUser(ctx, token2)
		if u2 == nil || u2.ID != u.ID {
			t.Errorf("second sign-in made a new account: %+v vs %+v", u2, u)
		}
	})
}

func TestOIDCRequiredGroup(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f, p := oidcFixture(t, db, "media-admins")

		p.claims["groups"] = []string{"family"}
		if _, _, err := signIn(t, f, p, ""); oidcCode(err) != "sso_denied" {
			t.Errorf("wrong group: %v", err)
		}
		p.claims["groups"] = "media-admins" // single string form
		if _, _, err := signIn(t, f, p, ""); err != nil {
			t.Errorf("group as a string: %v", err)
		}

		// Groups only available from the userinfo endpoint.
		delete(p.claims, "groups")
		p.userinfoClaims = map[string]any{"groups": []string{"media-admins"}}
		if _, _, err := signIn(t, f, p, ""); err != nil {
			t.Errorf("groups from userinfo: %v", err)
		}
		p.userinfoClaims = nil
		if _, _, err := signIn(t, f, p, ""); oidcCode(err) != "sso_denied" {
			t.Errorf("no groups anywhere: %v", err)
		}
	})
}

func TestOIDCStateChecks(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, p := oidcFixture(t, db, "")

		authURL, state, _ := f.auth.StartOIDC(ctx, redirect, "/")
		code, _ := p.authorize(authURL)

		// The callback arrives in a browser that didn't start this sign-in.
		if _, _, err := f.auth.FinishOIDC(ctx, state, "other-browser-state", code, client); oidcCode(err) != "sso_expired" {
			t.Errorf("cookie mismatch: %v", err)
		}
		if _, _, err := f.auth.FinishOIDC(ctx, state, "", code, client); oidcCode(err) != "sso_expired" {
			t.Errorf("missing cookie: %v", err)
		}

		authURL, state, _ = f.auth.StartOIDC(ctx, redirect, "/")
		code, _ = p.authorize(authURL)
		if _, _, err := f.auth.FinishOIDC(ctx, state, state, code, client); err != nil {
			t.Fatal(err)
		}
		// Replaying the same callback fails.
		if _, _, err := f.auth.FinishOIDC(ctx, state, state, code, client); oidcCode(err) != "sso_expired" {
			t.Errorf("replay: %v", err)
		}
		if _, _, err := f.auth.FinishOIDC(ctx, "", "", code, client); oidcCode(err) != "sso_expired" {
			t.Errorf("empty state: %v", err)
		}
	})
}

func TestOIDCTokenChecks(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f, p := oidcFixture(t, db, "")

		p.wrongNonce = true
		if _, _, err := signIn(t, f, p, ""); oidcCode(err) != "sso_failed" {
			t.Errorf("wrong nonce: %v", err)
		}
		p.wrongNonce = false

		p.wrongAudience = true
		if _, _, err := signIn(t, f, p, ""); oidcCode(err) != "sso_failed" {
			t.Errorf("token for another app: %v", err)
		}
		p.wrongAudience = false

		p.clientSecret = "rotated"
		if _, _, err := signIn(t, f, p, ""); oidcCode(err) != "sso_failed" {
			t.Errorf("wrong client secret: %v", err)
		}
	})
}

func TestOIDCDoesNotTakeOverLocalAccount(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, p := oidcFixture(t, db, "")
		_, local, err := f.auth.Setup(ctx, "dan", "a-good-password", client)
		if err != nil {
			t.Fatal(err)
		}
		token, _, err := signIn(t, f, p, "")
		if err != nil {
			t.Fatal(err)
		}
		u, _ := f.auth.SessionUser(ctx, token)
		if u == nil || u.ID == local.ID || u.Username != "dan-sso" {
			t.Errorf("SSO user = %+v; must not be the local account %d", u, local.ID)
		}
	})
}

func TestOIDCDisabledOrSettingsChanged(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, p := oidcFixture(t, db, "")

		authURL, state, _ := f.auth.StartOIDC(ctx, redirect, "/")
		code, _ := p.authorize(authURL)
		f.saveSecurity(t, func(u *settings.SecurityUpdate) { u.OIDC.ClientID = "changed" })
		if _, _, err := f.auth.FinishOIDC(ctx, state, state, code, client); oidcCode(err) != "sso_expired" {
			t.Errorf("settings changed mid-sign-in: %v", err)
		}

		f.saveSecurity(t, func(u *settings.SecurityUpdate) { u.OIDC.Enabled = false })
		if _, _, err := f.auth.StartOIDC(ctx, redirect, "/"); oidcCode(err) != "sso_disabled" {
			t.Errorf("disabled: %v", err)
		}
	})
}

func TestCheckOIDC(t *testing.T) {
	db := storetest.Open(t, store.SQLite)
	f := newFixture(t, db)
	p := newFakeProvider(t)

	if ok, msg := f.auth.CheckOIDC(context.Background(), p.issuer); !ok || !strings.Contains(msg, "/authorize") {
		t.Errorf("good issuer: %v %q", ok, msg)
	}
	// Missing trailing slash: the provider reports a different issuer.
	ok, msg := f.auth.CheckOIDC(context.Background(), strings.TrimSuffix(p.issuer, "/"))
	if ok || !strings.Contains(msg, p.issuer) {
		t.Errorf("issuer mismatch: %v %q", ok, msg)
	}
	if ok, _ := f.auth.CheckOIDC(context.Background(), p.srv.URL+"/nothing-here/"); ok {
		t.Error("missing discovery document should fail")
	}
}

// linkIn runs a whole "link single sign-on" flow for the account userID.
func linkIn(t *testing.T, f *fixture, p *fakeProvider, userID int64) (token, back string, err error) {
	t.Helper()
	ctx := context.Background()
	authURL, state, err := f.auth.StartOIDCLink(ctx, redirect, "/settings/security", userID)
	if err != nil {
		return "", "", err
	}
	code, _ := p.authorize(authURL)
	return f.auth.FinishOIDC(ctx, state, state, code, client)
}

func TestOIDCLinkToLocalAccount(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, p := oidcFixture(t, db, "media-admins")
		_, local, err := f.auth.Setup(ctx, "dan", "a-good-password", client)
		if err != nil {
			t.Fatal(err)
		}

		token, back, err := linkIn(t, f, p, local.ID)
		if err != nil {
			t.Fatal(err)
		}
		if back != "/settings/security" {
			t.Errorf("returnTo = %q", back)
		}
		u, _ := f.auth.SessionUser(ctx, token)
		if u == nil || u.ID != local.ID || !u.SSOLinked() || !u.HasPassword() {
			t.Fatalf("after linking: %+v", u)
		}

		// Single sign-on now reaches the local account.
		token, _, err = signIn(t, f, p, "")
		if err != nil {
			t.Fatal(err)
		}
		if u, _ := f.auth.SessionUser(ctx, token); u == nil || u.ID != local.ID {
			t.Errorf("SSO sign-in should reach the linked account, got %+v", u)
		}
		// Linking again is harmless.
		if _, _, err := linkIn(t, f, p, local.ID); err != nil {
			t.Errorf("re-link: %v", err)
		}
	})
}

// The situation from the first trial deploy: SSO was used before linking,
// which created "dan-sso". Linking merges it into "dan".
func TestOIDCLinkMergesSSOOnlyAccount(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, p := oidcFixture(t, db, "")
		_, local, err := f.auth.Setup(ctx, "dan", "a-good-password", client)
		if err != nil {
			t.Fatal(err)
		}
		ssoToken, _, err := signIn(t, f, p, "")
		if err != nil {
			t.Fatal(err)
		}
		separate, _ := f.auth.SessionUser(ctx, ssoToken)
		if separate == nil || separate.Username != "dan-sso" {
			t.Fatalf("expected a separate dan-sso account first, got %+v", separate)
		}

		if _, _, err := linkIn(t, f, p, local.ID); err != nil {
			t.Fatal(err)
		}
		if u, _ := f.auth.SessionUser(ctx, ssoToken); u != nil {
			t.Error("the merged account's sessions should end")
		}
		n, err := db.DB.NewSelect().Model((*User)(nil)).Where("username = ?", "dan-sso").Count(ctx)
		if err != nil || n != 0 {
			t.Errorf("dan-sso should be removed (count %d, %v)", n, err)
		}
		token, _, err := signIn(t, f, p, "")
		if err != nil {
			t.Fatal(err)
		}
		if u, _ := f.auth.SessionUser(ctx, token); u == nil || u.ID != local.ID {
			t.Errorf("SSO should now reach dan, got %+v", u)
		}
	})
}

func TestOIDCLinkRefusesAnotherPasswordAccount(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, p := oidcFixture(t, db, "")
		_, first, err := f.auth.Setup(ctx, "dan", "a-good-password", client)
		if err != nil {
			t.Fatal(err)
		}
		hash, _ := HashPassword("another-password")
		second := &User{Username: "other", PasswordHash: &hash, CreatedAt: time.Now()}
		if _, err := db.DB.NewInsert().Model(second).Exec(ctx); err != nil {
			t.Fatal(err)
		}

		if _, _, err := linkIn(t, f, p, first.ID); err != nil {
			t.Fatal(err)
		}
		_, _, err = linkIn(t, f, p, second.ID)
		if oidcCode(err) != "sso_link_conflict" {
			t.Errorf("linking an identity owned by another password account: %v", err)
		}
		if u, _ := f.auth.userByID(ctx, first.ID); u == nil || !u.SSOLinked() {
			t.Error("the first account must keep its link")
		}
	})
}

func TestOIDCLinkStillChecksGroupAndPassword(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f, p := oidcFixture(t, db, "media-admins")
		_, local, err := f.auth.Setup(ctx, "dan", "a-good-password", client)
		if err != nil {
			t.Fatal(err)
		}
		p.claims["groups"] = []string{"family"}
		if _, _, err := linkIn(t, f, p, local.ID); oidcCode(err) != "sso_denied" {
			t.Errorf("link without the required group: %v", err)
		}
		if u, _ := f.auth.userByID(ctx, local.ID); u.SSOLinked() {
			t.Error("a refused link must not attach the identity")
		}

		// An account without a password has nothing to link to.
		p.claims["groups"] = []string{"media-admins"}
		ssoOnly := &User{Username: "sso-only", CreatedAt: time.Now()}
		if _, err := db.DB.NewInsert().Model(ssoOnly).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.auth.StartOIDCLink(ctx, redirect, "/", ssoOnly.ID); !errors.Is(err, ErrNoPassword) {
			t.Errorf("link for an account without a password: %v", err)
		}
	})
}
