package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/store/storetest"
	"github.com/saltglass/reelkeeper/internal/validation"
)

var client = Client{IP: "192.168.1.50", UserAgent: "test"}

type fixture struct {
	db       *store.Store
	settings *settings.Service
	auth     *Service
	clock    *time.Time
}

func newFixture(t *testing.T, db *store.Store) *fixture {
	t.Helper()
	box, err := secrets.New("test-secret-key-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	st, err := settings.New(context.Background(), db, box, logger)
	if err != nil {
		t.Fatal(err)
	}
	a := New(db, st, logger)
	now := time.Now().UTC()
	f := &fixture{db: db, settings: st, auth: a, clock: &now}
	a.now = func() time.Time { return *f.clock }
	return f
}

func (f *fixture) advance(d time.Duration) { *f.clock = f.clock.Add(d) }

func (f *fixture) setup(t *testing.T) string {
	t.Helper()
	token, _, err := f.auth.Setup(context.Background(), "Dan", "a-good-password", client)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (f *fixture) saveSecurity(t *testing.T, change func(*settings.SecurityUpdate)) {
	t.Helper()
	sec := f.settings.Security()
	u := settings.SecurityUpdate{
		LocalLoginEnabled: sec.LocalLoginEnabled,
		BypassNetworks:    sec.BypassNetworks,
		TrustedProxies:    sec.TrustedProxies,
		SessionDays:       sec.SessionDays,
		OIDC: settings.OIDCUpdate{
			Enabled: sec.OIDC.Enabled, IssuerURL: sec.OIDC.IssuerURL, ClientID: sec.OIDC.ClientID,
			Scopes: sec.OIDC.Scopes, GroupsClaim: sec.OIDC.GroupsClaim,
			RequiredGroup: sec.OIDC.RequiredGroup, ButtonLabel: sec.OIDC.ButtonLabel,
		},
	}
	change(&u)
	if _, err := f.settings.SaveSecurity(context.Background(), u); err != nil {
		t.Fatal(err)
	}
}

func TestSetupOnlyOnce(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)

		need, err := f.auth.SetupRequired(ctx)
		if err != nil || !need {
			t.Fatalf("SetupRequired = %v, %v", need, err)
		}

		_, _, err = f.auth.Setup(ctx, "dan", "short", client)
		if errs, ok := validation.As(err); !ok || errs["password"] == "" {
			t.Errorf("short password: %v", err)
		}
		_, _, err = f.auth.Setup(ctx, "dan", "DAN", client)
		if errs, ok := validation.As(err); !ok || errs["password"] == "" {
			t.Errorf("password equal to username (short too): %v", err)
		}
		_, _, err = f.auth.Setup(ctx, "  ", "a-good-password", client)
		if errs, ok := validation.As(err); !ok || errs["username"] == "" {
			t.Errorf("blank username: %v", err)
		}

		token := f.setup(t)
		if u, _ := f.auth.SessionUser(ctx, token); u == nil || u.Username != "Dan" {
			t.Errorf("setup should sign in, got %+v", u)
		}
		if _, _, err := f.auth.Setup(ctx, "intruder", "another-password", client); !errors.Is(err, ErrAlreadySetUp) {
			t.Errorf("second setup: %v", err)
		}
		if need, _ := f.auth.SetupRequired(ctx); need {
			t.Error("setup should no longer be required")
		}
	})
}

func TestLogin(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.setup(t)

		token, u, err := f.auth.Login(ctx, "dan", "a-good-password", client)
		if err != nil || token == "" || u.Username != "Dan" {
			t.Fatalf("login (case-insensitive username): %v", err)
		}
		if _, _, err := f.auth.Login(ctx, "Dan", "wrong-password", client); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("wrong password: %v", err)
		}
		if _, _, err := f.auth.Login(ctx, "nobody", "a-good-password", client); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("unknown user must look the same as a wrong password: %v", err)
		}

		var stored string
		if err := db.DB.NewRaw("SELECT token_hash FROM sessions LIMIT 1").Scan(ctx, &stored); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stored, token) || len(stored) != 64 {
			t.Error("only a hash of the session token may be stored")
		}
	})
}

func TestLoginRateLimit(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.setup(t)
		for i := 0; i < 10; i++ {
			_, _, _ = f.auth.Login(ctx, "dan", "wrong-password", client)
		}
		_, _, err := f.auth.Login(ctx, "dan", "a-good-password", client)
		var rl *RateLimitedError
		if !errors.As(err, &rl) || rl.RetryAfter <= 0 {
			t.Errorf("expected rate limiting even with the right password, got %v", err)
		}
		// Another IP is also refused for this username...
		other := Client{IP: "192.168.1.99"}
		if _, _, err := f.auth.Login(ctx, "dan", "a-good-password", other); !errors.As(err, &rl) {
			t.Errorf("username lock should apply from any IP: %v", err)
		}
	})
}

func TestLocalLoginDisabled(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.setup(t)
		f.saveSecurity(t, func(u *settings.SecurityUpdate) {
			u.OIDC.Enabled, u.OIDC.IssuerURL, u.OIDC.ClientID = true, "https://auth.example.com/", "reelkeeper"
		})
		if err := f.settings.RecordOIDCSuccess(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		f.saveSecurity(t, func(u *settings.SecurityUpdate) { u.LocalLoginEnabled = false })

		if _, _, err := f.auth.Login(ctx, "dan", "a-good-password", client); !errors.Is(err, ErrLocalLoginDisabled) {
			t.Errorf("login with password sign-in off: %v", err)
		}
	})
}

func TestSessionExpiryAndSliding(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		token := f.setup(t) // 30-day sessions by default

		f.advance(29 * 24 * time.Hour)
		if u, _ := f.auth.SessionUser(ctx, token); u == nil {
			t.Fatal("session should still be valid on day 29")
		}
		// That use pushed the expiry to day 59.
		f.advance(29 * 24 * time.Hour)
		if u, _ := f.auth.SessionUser(ctx, token); u == nil {
			t.Fatal("using a session should extend it")
		}
		f.advance(31 * 24 * time.Hour)
		if u, _ := f.auth.SessionUser(ctx, token); u != nil {
			t.Error("session unused for 31 days should have expired")
		}
		if u, _ := f.auth.SessionUser(ctx, "made-up-token"); u != nil {
			t.Error("unknown token accepted")
		}
	})
}

func TestLogoutAndChangePassword(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		first := f.setup(t)
		second, u, _ := f.auth.Login(ctx, "dan", "a-good-password", client)

		if err := f.auth.ChangePassword(ctx, u.ID, "wrong", "brand-new-password", second); !errors.Is(err, ErrWrongPassword) {
			t.Errorf("wrong current password: %v", err)
		}
		err := f.auth.ChangePassword(ctx, u.ID, "a-good-password", "x", second)
		if errs, ok := validation.As(err); !ok || errs["newPassword"] == "" {
			t.Errorf("weak new password: %v", err)
		}
		if err := f.auth.ChangePassword(ctx, u.ID, "a-good-password", "brand-new-password", second); err != nil {
			t.Fatal(err)
		}
		if u, _ := f.auth.SessionUser(ctx, first); u != nil {
			t.Error("other sessions should end when the password changes")
		}
		if u, _ := f.auth.SessionUser(ctx, second); u == nil {
			t.Error("the session that changed the password should survive")
		}
		if _, _, err := f.auth.Login(ctx, "dan", "brand-new-password", client); err != nil {
			t.Errorf("new password: %v", err)
		}

		if err := f.auth.Logout(ctx, second); err != nil {
			t.Fatal(err)
		}
		if u, _ := f.auth.SessionUser(ctx, second); u != nil {
			t.Error("logged-out session still works")
		}
	})
}

func TestResetAuth(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)

		// With no accounts, an admin account is created.
		name, pw, err := f.auth.ResetAuth(ctx)
		if err != nil || name != "admin" || len(pw) < 12 {
			t.Fatalf("reset on empty: %q %q %v", name, pw, err)
		}
		if _, _, err := f.auth.Login(ctx, "admin", pw, client); err != nil {
			t.Errorf("printed password should work: %v", err)
		}
	})
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		token := f.setup(t)
		f.saveSecurity(t, func(u *settings.SecurityUpdate) {
			u.OIDC.Enabled, u.OIDC.IssuerURL, u.OIDC.ClientID = true, "https://auth.example.com/", "reelkeeper"
		})
		_ = f.settings.RecordOIDCSuccess(ctx, time.Now())
		f.saveSecurity(t, func(u *settings.SecurityUpdate) { u.LocalLoginEnabled = false })

		name, pw, err := f.auth.ResetAuth(ctx)
		if err != nil || name != "Dan" {
			t.Fatalf("reset: %q %v", name, err)
		}
		if !f.settings.Security().LocalLoginEnabled {
			t.Error("reset should turn password sign-in back on")
		}
		if u, _ := f.auth.SessionUser(ctx, token); u != nil {
			t.Error("reset should end all sessions")
		}
		if _, _, err := f.auth.Login(ctx, "dan", pw, client); err != nil {
			t.Errorf("new password: %v", err)
		}
	})
}

// --- Middleware -----------------------------------------------------------

// probe returns what Identify decided for a request.
func probe(t *testing.T, f *fixture, r *http.Request) RequestInfo {
	t.Helper()
	var got RequestInfo
	h := f.auth.Identify(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = Info(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), r)
	return got
}

func newReq(remote string) *http.Request {
	r := httptest.NewRequest("GET", "/api/v1/system/status", nil)
	r.RemoteAddr = remote
	return r
}

func TestIdentify(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		token := f.setup(t)
		apiKey := f.settings.Security().APIKey
		f.saveSecurity(t, func(u *settings.SecurityUpdate) {
			u.TrustedProxies = []string{"172.16.0.2"}
			u.BypassNetworks = []string{"192.168.1.0/24"}
		})

		t.Run("nothing", func(t *testing.T) {
			if ri := probe(t, f, newReq("203.0.113.9:1")); ri.Identity != nil {
				t.Errorf("anonymous request identified as %+v", ri.Identity)
			}
		})
		t.Run("API key header", func(t *testing.T) {
			r := newReq("203.0.113.9:1")
			r.Header.Set("X-Api-Key", apiKey)
			if ri := probe(t, f, r); ri.Identity == nil || ri.Identity.Via != ViaAPIKey {
				t.Errorf("got %+v", ri)
			}
		})
		t.Run("API key query parameter", func(t *testing.T) {
			r := newReq("203.0.113.9:1")
			r.URL.RawQuery = "apikey=" + apiKey
			if ri := probe(t, f, r); ri.Identity == nil || ri.Identity.Via != ViaAPIKey {
				t.Errorf("got %+v", ri)
			}
		})
		t.Run("wrong API key is refused even from the bypass list", func(t *testing.T) {
			r := newReq("192.168.1.50:1")
			r.Header.Set("X-Api-Key", "wrong")
			if ri := probe(t, f, r); ri.Identity != nil || !ri.BadAPIKey {
				t.Errorf("got %+v", ri)
			}
		})
		t.Run("session cookie", func(t *testing.T) {
			r := newReq("203.0.113.9:1")
			r.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
			ri := probe(t, f, r)
			if ri.Identity == nil || ri.Identity.Via != ViaSession || ri.Identity.User.Username != "Dan" {
				t.Errorf("got %+v", ri)
			}
		})
		t.Run("bogus session cookie", func(t *testing.T) {
			r := newReq("203.0.113.9:1")
			r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "forged"})
			if ri := probe(t, f, r); ri.Identity != nil {
				t.Errorf("got %+v", ri.Identity)
			}
		})
		t.Run("bypass from a listed network", func(t *testing.T) {
			if ri := probe(t, f, newReq("192.168.1.50:1")); ri.Identity == nil || ri.Identity.Via != ViaBypass {
				t.Errorf("got %+v", ri)
			}
		})
		t.Run("spoofed X-Forwarded-For from outside is not bypassed", func(t *testing.T) {
			r := newReq("203.0.113.9:1")
			r.Header.Set("X-Forwarded-For", "192.168.1.50")
			if ri := probe(t, f, r); ri.Identity != nil {
				t.Errorf("spoofed header granted access: %+v", ri)
			}
		})
		t.Run("bypass through the trusted proxy", func(t *testing.T) {
			r := newReq("172.16.0.2:1")
			r.Header.Set("X-Forwarded-For", "192.168.1.50")
			if ri := probe(t, f, r); ri.Identity == nil || ri.Identity.Via != ViaBypass {
				t.Errorf("got %+v", ri)
			}
		})
		t.Run("outsider through the trusted proxy is not bypassed", func(t *testing.T) {
			r := newReq("172.16.0.2:1")
			r.Header.Set("X-Forwarded-For", "203.0.113.9")
			if ri := probe(t, f, r); ri.Identity != nil {
				t.Errorf("got %+v", ri)
			}
		})
		t.Run("outsider prepending a bypass address through the proxy", func(t *testing.T) {
			r := newReq("172.16.0.2:1")
			r.Header.Set("X-Forwarded-For", "192.168.1.50, 203.0.113.9")
			if ri := probe(t, f, r); ri.Identity != nil {
				t.Errorf("got %+v", ri)
			}
		})
		t.Run("the proxy itself is not bypassed", func(t *testing.T) {
			if ri := probe(t, f, newReq("172.16.0.2:1")); ri.Identity != nil {
				t.Errorf("got %+v", ri)
			}
		})
	})
}

func TestSessionCookieFlags(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		f := newFixture(t, db)
		f.saveSecurity(t, func(u *settings.SecurityUpdate) { u.TrustedProxies = []string{"172.16.0.2"} })

		for _, c := range []struct {
			remote, proto string
			secure        bool
		}{
			{"172.16.0.2:1", "https", true},
			{"192.168.1.5:1", "https", false}, // header from an untrusted client
			{"192.168.1.5:1", "", false},
		} {
			r := newReq(c.remote)
			if c.proto != "" {
				r.Header.Set("X-Forwarded-Proto", c.proto)
			}
			var cookie *http.Cookie
			h := f.auth.Identify(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				SetSessionCookie(w, r, "tok", "/reelkeeper/", 30)
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			cookie = rec.Result().Cookies()[0]
			if cookie.Secure != c.secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/reelkeeper/" {
				t.Errorf("%s %q: cookie %+v", c.remote, c.proto, cookie)
			}
		}
	})
}

func TestPruneSessions(t *testing.T) {
	storetest.Each(t, func(t *testing.T, db *store.Store) {
		ctx := context.Background()
		f := newFixture(t, db)
		f.setup(t)
		_, _, _ = f.auth.Login(ctx, "dan", "a-good-password", client)
		f.advance(31 * 24 * time.Hour) // both sessions expire
		fresh, _, _ := f.auth.Login(ctx, "dan", "a-good-password", client)

		n, err := f.auth.PruneSessions(ctx)
		if err != nil || n != 2 {
			t.Errorf("pruned %d, %v; want 2", n, err)
		}
		if u, _ := f.auth.SessionUser(ctx, fresh); u == nil {
			t.Error("a current session must survive")
		}
	})
}
