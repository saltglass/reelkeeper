package auth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/netip"
	"time"

	"github.com/saltglass/reelkeeper/internal/settings"
)

// SessionCookie is the name of the browser session cookie.
const SessionCookie = "reelkeeper_session"

// Via says how a request was let in.
type Via string

// Ways a request can be authenticated.
const (
	ViaSession Via = "session"
	ViaAPIKey  Via = "apiKey"
	ViaBypass  Via = "bypass"
)

// Identity is who made a request. User is nil for the API key and the
// bypass list, which aren't tied to an account.
type Identity struct {
	Via  Via
	User *User
	// Token is the session token, for ViaSession.
	Token string
}

// RequestInfo is what Identify learned about a request, whether or not it
// is authenticated.
type RequestInfo struct {
	ClientIP netip.Addr
	HTTPS    bool
	// Identity is nil when the request is not authenticated.
	Identity *Identity
	// BadAPIKey is true when an API key was sent but is wrong. Such
	// requests are refused even from the bypass list, so a misconfigured
	// app gets a clear error instead of working by accident.
	BadAPIKey bool
}

type ctxKey struct{}

// Info returns what Identify stored for this request.
func Info(ctx context.Context) RequestInfo {
	if ri, ok := ctx.Value(ctxKey{}).(RequestInfo); ok {
		return ri
	}
	return RequestInfo{}
}

// Identify works out who is making each request and stores it in the
// request context. It never rejects anything; handlers or Require do.
//
// Order: API key (X-Api-Key header or apikey query parameter), then the
// session cookie, then the bypass list.
func (s *Service) Identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sec := s.settings.Security()
		trusted := settings.Prefixes(sec.TrustedProxies)
		ri := RequestInfo{
			ClientIP: ClientIP(r, trusted),
			HTTPS:    IsHTTPS(r, trusted),
		}

		if key := apiKeyFrom(r); key != "" {
			if sec.APIKey != "" && subtle.ConstantTimeCompare([]byte(key), []byte(sec.APIKey)) == 1 {
				ri.Identity = &Identity{Via: ViaAPIKey}
			} else {
				ri.BadAPIKey = true
			}
		} else if c := SessionCookieFrom(r); c != nil {
			u, err := s.SessionUser(r.Context(), c.Value)
			switch {
			case err != nil && r.Context().Err() != nil:
				// The browser gave up on the request (navigated away):
				// nothing went wrong on our side.
				s.logger.Debug("session lookup cancelled", "error", err)
			case err != nil:
				s.logger.Error("session lookup failed", "error", err)
			}
			if u != nil {
				ri.Identity = &Identity{Via: ViaSession, User: u, Token: c.Value}
			}
		}

		if ri.Identity == nil && !ri.BadAPIKey && inAny(ri.ClientIP, settings.Prefixes(sec.BypassNetworks)) {
			ri.Identity = &Identity{Via: ViaBypass}
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, ri)))
	})
}

// ClientAddr returns the request's real client address as text, using the
// trusted-proxy list, for logging. Empty if it can't be determined.
func (s *Service) ClientAddr(r *http.Request) string {
	ip := ClientIP(r, settings.Prefixes(s.settings.Security().TrustedProxies))
	if !ip.IsValid() {
		return ""
	}
	return ip.String()
}

func apiKeyFrom(r *http.Request) string {
	if k := r.Header.Get("X-Api-Key"); k != "" {
		return k
	}
	return r.URL.Query().Get("apikey")
}

// SetSessionCookie gives the browser its session token. path is the URL
// base plus "/", so the cookie isn't sent to other apps on the same host.
//
// Secure is set whenever the browser reached Reelkeeper over HTTPS (directly or
// via a trusted proxy). It can't be forced on: over plain http on a LAN the
// browser would drop the cookie and sign-in would never work.
func SetSessionCookie(w http.ResponseWriter, r *http.Request, token, path string, days int) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure follows the request scheme; see above
		Name:     SessionCookie,
		Value:    token,
		Path:     path,
		MaxAge:   int((time.Duration(days) * 24 * time.Hour).Seconds()),
		HttpOnly: true,
		Secure:   Info(r.Context()).HTTPS,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie removes the session cookie.
func ClearSessionCookie(w http.ResponseWriter, r *http.Request, path string) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure follows the request scheme
		Name:     SessionCookie,
		Value:    "",
		Path:     path,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   Info(r.Context()).HTTPS,
		SameSite: http.SameSiteLaxMode,
	})
}

// WithIdentity returns ctx carrying an identity, as Identify would store
// it. For tests of handlers that sit behind Identify.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, RequestInfo{Identity: id})
}

// SessionCookieFrom returns the request's session cookie, or nil.
func SessionCookieFrom(r *http.Request) *http.Cookie {
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		return c
	}
	return nil
}
