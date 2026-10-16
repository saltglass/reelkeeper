package api

import (
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/auth"
	"github.com/saltglass/reelkeeper/internal/events"
	"github.com/saltglass/reelkeeper/internal/settings"
)

// GetAuthState tells the login page what to show.
func (h *Handlers) GetAuthState(w http.ResponseWriter, r *http.Request) {
	need, err := h.Auth.SetupRequired(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	sec := h.Settings.Security()
	out := gen.AuthState{
		SetupRequired:     need,
		LocalLoginEnabled: sec.LocalLoginEnabled,
		OidcEnabled:       sec.OIDC.Enabled,
		OidcButtonLabel:   sec.OIDC.ButtonLabel,
	}
	if id := auth.Info(r.Context()).Identity; id != nil {
		out.Authenticated = true
		via := gen.AuthStateVia(id.Via)
		out.Via = &via
		if id.User != nil {
			out.User = toUser(id.User)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// Setup creates the first account.
func (h *Handlers) Setup(w http.ResponseWriter, r *http.Request) {
	var body gen.SetupJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	token, _, err := h.Auth.Setup(r.Context(), body.Username, body.Password, client(r))
	if errors.Is(err, auth.ErrAlreadySetUp) {
		writeError(w, http.StatusConflict, "already_set_up", "An account already exists. Sign in instead.", nil)
		return
	}
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	auth.SetSessionCookie(w, r, token, h.cookiePath(), h.Settings.Security().SessionDays)
	w.WriteHeader(http.StatusNoContent)
}

// SetupRestore starts a new install from a backup (the first-run screen's
// alternative to creating an account). Like Setup, it only works while no
// account exists; the restore itself checks that again when it runs.
func (h *Handlers) SetupRestore(w http.ResponseWriter, r *http.Request) {
	needed, err := h.Auth.SetupRequired(r.Context())
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if !needed {
		writeError(w, http.StatusConflict, "already_set_up", "An account already exists. Sign in and restore from System → Backups instead.", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if _, err := h.Backups.StageSetupRestore(r.Context(), r.Body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "That file is larger than 1 GiB, which is more than a backup should be.", nil)
			return
		}
		h.writeBackupError(w, r, err)
		return
	}
	h.Logger.Warn("first-run restore requested", "client", client(r).IP)
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "Reelkeeper is restarting to restore the backup."})
}

// Login signs in with a password.
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var body gen.LoginJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	token, _, err := h.Auth.Login(r.Context(), body.Username, body.Password, client(r))
	var limited *auth.RateLimitedError
	switch {
	case err == nil:
		auth.SetSessionCookie(w, r, token, h.cookiePath(), h.Settings.Security().SessionDays)
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Wrong username or password.", nil)
	case errors.Is(err, auth.ErrLocalLoginDisabled):
		writeError(w, http.StatusForbidden, "password_login_disabled", "Password sign-in is turned off. Use single sign-on.", nil)
	case errors.As(err, &limited):
		secs := int(math.Ceil(limited.RetryAfter.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeError(w, http.StatusTooManyRequests, "rate_limited",
			"Too many failed attempts. Try again in "+humanSeconds(secs)+".", nil)
	default:
		h.writeServiceError(w, r, err)
	}
}

// Logout ends this browser's session.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if c := auth.SessionCookieFrom(r); c != nil {
		if err := h.Auth.Logout(r.Context(), c.Value); err != nil {
			h.writeServiceError(w, r, err)
			return
		}
	}
	auth.ClearSessionCookie(w, r, h.cookiePath())
	w.WriteHeader(http.StatusNoContent)
}

// ChangePassword changes the signed-in account's password.
func (h *Handlers) ChangePassword(w http.ResponseWriter, r *http.Request) {
	id := auth.Info(r.Context()).Identity
	if id == nil || id.User == nil {
		writeError(w, http.StatusForbidden, "no_account",
			"You're using Reelkeeper without an account (API key or bypass list), so there's no password to change.", nil)
		return
	}
	var body gen.ChangePasswordJSONRequestBody
	if !decode(w, r, &body) {
		return
	}
	err := h.Auth.ChangePassword(r.Context(), id.User.ID, body.CurrentPassword, body.NewPassword, id.Token)
	switch {
	case err == nil:
		h.Events.Publish(events.Event{Type: events.SessionEnded, Data: map[string]any{}})
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, auth.ErrWrongPassword):
		writeError(w, http.StatusForbidden, "wrong_password", "The current password is wrong.", nil)
	case errors.Is(err, auth.ErrNoPassword):
		writeError(w, http.StatusForbidden, "no_password", "This account signs in through single sign-on only.", nil)
	default:
		h.writeServiceError(w, r, err)
	}
}

// OidcLogin sends the browser to the identity provider.
func (h *Handlers) OidcLogin(w http.ResponseWriter, r *http.Request, params gen.OidcLoginParams) {
	returnTo := "/"
	if params.ReturnTo != nil {
		returnTo = *params.ReturnTo
	}
	trusted := settings.Prefixes(h.Settings.Security().TrustedProxies)
	authURL, state, err := h.Auth.StartOIDC(r.Context(), auth.CallbackURL(r, trusted, h.URLBase), returnTo)
	if err != nil {
		var oe *auth.OIDCError
		if errors.As(err, &oe) && oe.Code == "sso_disabled" {
			writeError(w, http.StatusNotFound, "sso_disabled", "Single sign-on is not set up.", nil)
			return
		}
		h.Logger.Error("single sign-on couldn't start", "error", err)
		h.redirectToLogin(w, r, oidcErrorCode(err), "")
		return
	}
	h.setStateCookie(w, r, state)
	// authURL is the configured provider's sign-in endpoint.
	http.Redirect(w, r, authURL, http.StatusFound) // #nosec G710 -- target comes from admin settings, not the request
}

// OidcLink starts linking single sign-on to the signed-in account.
func (h *Handlers) OidcLink(w http.ResponseWriter, r *http.Request, params gen.OidcLinkParams) {
	id := auth.Info(r.Context()).Identity
	if id == nil || id.User == nil || !id.User.HasPassword() {
		writeError(w, http.StatusForbidden, "no_account",
			"Sign in with your username and password first; single sign-on is linked to that account.", nil)
		return
	}
	returnTo := "/"
	if params.ReturnTo != nil {
		returnTo = *params.ReturnTo
	}
	trusted := settings.Prefixes(h.Settings.Security().TrustedProxies)
	authURL, state, err := h.Auth.StartOIDCLink(r.Context(), auth.CallbackURL(r, trusted, h.URLBase), returnTo, id.User.ID)
	if err != nil {
		var oe *auth.OIDCError
		if errors.As(err, &oe) && oe.Code == "sso_disabled" {
			writeError(w, http.StatusNotFound, "sso_disabled", "Single sign-on is not set up.", nil)
			return
		}
		h.Logger.Error("single sign-on linking couldn't start", "error", err)
		h.redirectToLogin(w, r, oidcErrorCode(err), "")
		return
	}
	h.setStateCookie(w, r, state)
	// authURL is the configured provider's sign-in endpoint.
	http.Redirect(w, r, authURL, http.StatusFound) // #nosec G710 -- target comes from admin settings, not the request
}

// OidcCallback finishes single sign-on.
func (h *Handlers) OidcCallback(w http.ResponseWriter, r *http.Request, params gen.OidcCallbackParams) {
	// The state cookie is single-use either way.
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- deleting the cookie
		Name: auth.OIDCStateCookie, Value: "", Path: h.URLBase + auth.CallbackPath, MaxAge: -1,
		HttpOnly: true, Secure: auth.Info(r.Context()).HTTPS, SameSite: http.SameSiteLaxMode,
	})

	if params.Error != nil {
		desc := ""
		if params.ErrorDescription != nil {
			desc = *params.ErrorDescription
		}
		h.Logger.Warn("identity provider refused sign-in", "error", *params.Error, "description", desc)
		code := "sso_failed"
		if *params.Error == "access_denied" {
			code = "sso_denied"
		}
		detail := *params.Error
		if desc != "" {
			detail += ": " + desc
		}
		h.redirectToLogin(w, r, code, "The identity provider said: "+detail)
		return
	}

	var stateCookie string
	if c, err := r.Cookie(auth.OIDCStateCookie); err == nil {
		stateCookie = c.Value
	}
	token, returnTo, err := h.Auth.FinishOIDC(r.Context(), deref(params.State), stateCookie, deref(params.Code), client(r))
	if err != nil {
		h.Logger.Warn("single sign-on failed", "error", err)
		detail := ""
		var oe *auth.OIDCError
		if errors.As(err, &oe) {
			detail = oe.Message
		}
		h.redirectToLogin(w, r, oidcErrorCode(err), detail)
		return
	}
	auth.SetSessionCookie(w, r, token, h.cookiePath(), h.Settings.Security().SessionDays)
	// returnTo was reduced to a path inside Reelkeeper by auth.SafeReturnPath.
	http.Redirect(w, r, h.URLBase+returnTo, http.StatusFound) // #nosec G710 -- sanitized by SafeReturnPath
}

// setStateCookie binds a single sign-on attempt to this browser.
func (h *Handlers) setStateCookie(w http.ResponseWriter, r *http.Request, state string) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure follows the request scheme, as for the session cookie
		Name:     auth.OIDCStateCookie,
		Value:    state,
		Path:     h.URLBase + auth.CallbackPath,
		MaxAge:   600,
		HttpOnly: true,
		Secure:   auth.Info(r.Context()).HTTPS,
		// Lax lets the cookie come back on the provider's top-level redirect.
		SameSite: http.SameSiteLaxMode,
	})
}

// maxDetail caps the provider message passed to the login page.
const maxDetail = 200

// redirectToLogin sends the browser to the login page with an error code
// and, when there is one, the identity provider's own explanation.
func (h *Handlers) redirectToLogin(w http.ResponseWriter, r *http.Request, code, detail string) {
	q := url.Values{"error": {code}}
	if detail != "" {
		if len(detail) > maxDetail {
			detail = detail[:maxDetail]
		}
		q.Set("detail", detail)
	}
	http.Redirect(w, r, h.URLBase+"/login?"+q.Encode(), http.StatusFound)
}

func oidcErrorCode(err error) string {
	var oe *auth.OIDCError
	if errors.As(err, &oe) {
		return oe.Code
	}
	return "sso_failed"
}

func client(r *http.Request) auth.Client {
	ip := auth.Info(r.Context()).ClientIP
	s := ""
	if ip.IsValid() {
		s = ip.String()
	}
	return auth.Client{IP: s, UserAgent: r.UserAgent()}
}

func toUser(u *auth.User) *gen.User {
	return &gen.User{Id: u.ID, Username: u.Username, HasPassword: u.HasPassword(), SsoLinked: u.SSOLinked()}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func humanSeconds(s int) string {
	if s < 60 {
		return strconv.Itoa(s) + " seconds"
	}
	m := (s + 59) / 60
	if m == 1 {
		return "1 minute"
	}
	return strconv.Itoa(m) + " minutes"
}
