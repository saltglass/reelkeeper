// Package auth decides who may use Reelkeeper: local accounts with passwords,
// single sign-on (OIDC), the API key, and the IP bypass list. It also owns
// server-side sessions.
//
// The HTTP side (middleware, cookies, client IPs) is in middleware.go and
// clientip.go; single sign-on is in oidc.go.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// Errors returned to the API layer, which turns them into status codes.
var (
	ErrInvalidCredentials = errors.New("wrong username or password")
	ErrLocalLoginDisabled = errors.New("password sign-in is turned off; use single sign-on")
	ErrAlreadySetUp       = errors.New("an account already exists")
	ErrWrongPassword      = errors.New("the current password is wrong")
	ErrNoPassword         = errors.New("this account signs in through single sign-on only")
)

// RateLimitedError means too many failed attempts; try again after RetryAfter.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("too many failed sign-in attempts; try again in %s", e.RetryAfter.Round(time.Second))
}

// User is an account.
type User struct {
	bun.BaseModel `bun:"table:users"`

	ID           int64      `bun:"id,pk,autoincrement"`
	Username     string     `bun:"username"`
	PasswordHash *string    `bun:"password_hash"`
	OIDCIssuer   *string    `bun:"oidc_issuer"`
	OIDCSubject  *string    `bun:"oidc_subject"`
	CreatedAt    time.Time  `bun:"created_at"`
	LastLoginAt  *time.Time `bun:"last_login_at"`
}

// HasPassword reports whether the account can sign in with a password.
func (u *User) HasPassword() bool { return u.PasswordHash != nil && *u.PasswordHash != "" }

// SSOLinked reports whether the account can sign in through single sign-on.
func (u *User) SSOLinked() bool { return u.OIDCSubject != nil && *u.OIDCSubject != "" }

type session struct {
	bun.BaseModel `bun:"table:sessions"`

	TokenHash  string    `bun:"token_hash,pk"`
	UserID     int64     `bun:"user_id"`
	CreatedAt  time.Time `bun:"created_at"`
	ExpiresAt  time.Time `bun:"expires_at"`
	LastSeenAt time.Time `bun:"last_seen_at"`
	IP         string    `bun:"ip"`
	UserAgent  string    `bun:"user_agent"`
}

// sessionTouchInterval limits how often a session's expiry is pushed back,
// so browsing doesn't write to the database on every request.
const sessionTouchInterval = time.Hour

// Service implements sign-in, sessions and access checks.
type Service struct {
	db       *store.Store
	settings *settings.Service
	logger   *slog.Logger
	limiter  *limiter
	oidc     *oidcFlow
	now      func() time.Time
}

// New returns a Service.
func New(db *store.Store, s *settings.Service, logger *slog.Logger) *Service {
	svc := &Service{
		db:       db,
		settings: s,
		logger:   logger,
		limiter:  newLimiter(10, 15*time.Minute),
		now:      time.Now,
	}
	svc.oidc = newOIDCFlow(svc)
	return svc
}

// SetupRequired reports whether no account exists yet.
func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	n, err := s.db.DB.NewSelect().Model((*User)(nil)).Count(ctx)
	return n == 0, err
}

// Client describes where a request came from, recorded on sessions.
type Client struct {
	IP        string
	UserAgent string
}

// Setup creates the first account and signs it in. It fails with
// ErrAlreadySetUp once any account exists.
func (s *Service) Setup(ctx context.Context, username, password string, c Client) (token string, u *User, err error) {
	username = strings.TrimSpace(username)
	var errs validation.Errors
	checkUsername(username, &errs)
	checkPassword("password", password, username, &errs)
	if errs != nil {
		return "", nil, errs
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", nil, err
	}

	err = s.db.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		n, err := tx.NewSelect().Model((*User)(nil)).Count(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrAlreadySetUp
		}
		now := s.now().UTC()
		u = &User{Username: username, PasswordHash: &hash, CreatedAt: now, LastLoginAt: &now}
		_, err = tx.NewInsert().Model(u).Exec(ctx)
		return err
	})
	if err != nil {
		return "", nil, err
	}
	s.logger.Info("first account created", "username", username, "ip", c.IP)
	token, err = s.createSession(ctx, u.ID, c)
	return token, u, err
}

// Login checks a username and password and starts a session.
func (s *Service) Login(ctx context.Context, username, password string, c Client) (string, *User, error) {
	if !s.LocalLoginAllowed() {
		return "", nil, ErrLocalLoginDisabled
	}
	userKey := "user:" + strings.ToLower(strings.TrimSpace(username))
	ipKey := "ip:" + c.IP
	if wait := max(s.limiter.blocked(userKey), s.limiter.blocked(ipKey)); wait > 0 {
		return "", nil, &RateLimitedError{RetryAfter: wait}
	}

	u, err := s.userByName(ctx, username)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, err
	}
	hash := dummyHash
	if u != nil && u.HasPassword() {
		hash = *u.PasswordHash
	}
	if len(password) > maxPasswordLen {
		password = password[:maxPasswordLen]
	}
	ok, verr := VerifyPassword(password, hash)
	if verr != nil {
		s.logger.Error("can't check password", "username", username, "error", verr)
	}
	if !ok || u == nil || !u.HasPassword() {
		s.limiter.fail(userKey)
		s.limiter.fail(ipKey)
		s.logger.Warn("failed sign-in", "username", username, "ip", c.IP)
		return "", nil, ErrInvalidCredentials
	}

	s.limiter.reset(userKey)
	s.limiter.reset(ipKey)
	s.touchLogin(ctx, u)
	token, err := s.createSession(ctx, u.ID, c)
	if err != nil {
		return "", nil, err
	}
	s.logger.Info("signed in", "username", u.Username, "method", "password", "ip", c.IP)
	return token, u, nil
}

// LocalLoginAllowed reports whether password sign-in is on.
func (s *Service) LocalLoginAllowed() bool {
	return s.settings.Security().LocalLoginEnabled
}

// Logout ends the session with this token. Unknown tokens are ignored.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.DB.NewDelete().Model((*session)(nil)).Where("token_hash = ?", hashToken(token)).Exec(ctx)
	return err
}

// ChangePassword sets a new password after checking the current one, and
// ends the account's other sessions.
func (s *Service) ChangePassword(ctx context.Context, userID int64, current, next, keepToken string) error {
	u, err := s.userByID(ctx, userID)
	if err != nil {
		return err
	}
	if !u.HasPassword() {
		return ErrNoPassword
	}
	if ok, _ := VerifyPassword(current, *u.PasswordHash); !ok {
		return ErrWrongPassword
	}
	var errs validation.Errors
	checkPassword("newPassword", next, u.Username, &errs)
	if errs != nil {
		return errs
	}
	if err := s.setPassword(ctx, u.ID, next); err != nil {
		return err
	}
	_, err = s.db.DB.NewDelete().Model((*session)(nil)).
		Where("user_id = ?", u.ID).Where("token_hash <> ?", hashToken(keepToken)).Exec(ctx)
	s.logger.Info("password changed", "username", u.Username)
	return err
}

func (s *Service) setPassword(ctx context.Context, userID int64, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.db.DB.NewUpdate().Model((*User)(nil)).Set("password_hash = ?", hash).Where("id = ?", userID).Exec(ctx)
	return err
}

// SessionUser returns the user for a session token, or nil if the token is
// unknown or expired. It pushes the expiry back while the session is used.
func (s *Service) SessionUser(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, nil
	}
	var sess session
	err := s.db.DB.NewSelect().Model(&sess).Where("token_hash = ?", hashToken(token)).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	if !now.Before(sess.ExpiresAt) {
		_, _ = s.db.DB.NewDelete().Model(&sess).WherePK().Exec(ctx)
		return nil, nil
	}
	if now.Sub(sess.LastSeenAt) > sessionTouchInterval {
		days := s.settings.Security().SessionDays
		_, err := s.db.DB.NewUpdate().Model(&sess).
			Set("last_seen_at = ?", now).
			Set("expires_at = ?", now.Add(time.Duration(days)*24*time.Hour)).
			WherePK().Exec(ctx)
		if err != nil {
			s.logger.Warn("can't extend session", "error", err)
		}
	}
	u, err := s.userByID(ctx, sess.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// PruneSessions deletes expired sessions and returns how many it removed.
// It runs daily as a task; expired sessions are also refused on use.
func (s *Service) PruneSessions(ctx context.Context) (int64, error) {
	res, err := s.db.DB.NewDelete().Model((*session)(nil)).Where("expires_at <= ?", s.now().UTC()).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// createSession stores a new session and returns its token, which only the
// browser keeps; the database holds a hash.
func (s *Service) createSession(ctx context.Context, userID int64, c Client) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	now := s.now().UTC()
	days := s.settings.Security().SessionDays
	ua := c.UserAgent
	if len(ua) > 300 {
		ua = ua[:300]
	}
	sess := &session{
		TokenHash:  hashToken(token),
		UserID:     userID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(time.Duration(days) * 24 * time.Hour),
		LastSeenAt: now,
		IP:         c.IP,
		UserAgent:  ua,
	}
	if _, err := s.db.DB.NewInsert().Model(sess).Exec(ctx); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) touchLogin(ctx context.Context, u *User) {
	now := s.now().UTC()
	u.LastLoginAt = &now
	_, _ = s.db.DB.NewUpdate().Model(u).Column("last_login_at").WherePK().Exec(ctx)
}

func (s *Service) userByName(ctx context.Context, username string) (*User, error) {
	u := new(User)
	err := s.db.DB.NewSelect().Model(u).Where("lower(username) = lower(?)", strings.TrimSpace(username)).Scan(ctx)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Service) userByID(ctx context.Context, id int64) (*User, error) {
	u := new(User)
	if err := s.db.DB.NewSelect().Model(u).Where("id = ?", id).Scan(ctx); err != nil {
		return nil, err
	}
	return u, nil
}

// ResetAuth is the way back in when single sign-on is broken or the
// password is forgotten: it turns password sign-in on, gives the first
// account a new random password (creating an "admin" account if none
// exists), and ends every session. It returns the username and password.
func (s *Service) ResetAuth(ctx context.Context) (username, password string, err error) {
	if err := s.settings.EnableLocalLogin(ctx); err != nil {
		return "", "", err
	}
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	password = base64.RawURLEncoding.EncodeToString(raw[:])

	u := new(User)
	err = s.db.DB.NewSelect().Model(u).Order("id").Limit(1).Scan(ctx)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		hash, herr := HashPassword(password)
		if herr != nil {
			return "", "", herr
		}
		u = &User{Username: "admin", PasswordHash: &hash, CreatedAt: s.now().UTC()}
		if _, err := s.db.DB.NewInsert().Model(u).Exec(ctx); err != nil {
			return "", "", err
		}
	case err != nil:
		return "", "", err
	default:
		if err := s.setPassword(ctx, u.ID, password); err != nil {
			return "", "", err
		}
	}
	if _, err := s.db.DB.NewDelete().Model((*session)(nil)).Where("1 = 1").Exec(ctx); err != nil {
		return "", "", err
	}
	s.logger.Warn("authentication was reset: password sign-in is on and all sessions ended", "username", u.Username)
	return u.Username, password, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func checkUsername(username string, errs *validation.Errors) {
	n := utf8.RuneCountInString(username)
	switch {
	case n == 0:
		errs.Add("username", "Enter a username.")
	case n > 64:
		errs.Add("username", "Use 64 characters or fewer.")
	case strings.ContainsFunc(username, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		errs.Add("username", "The username contains invalid characters.")
	}
}
