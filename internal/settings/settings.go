// Package settings stores the settings edited in the UI.
//
// Settings are grouped (General, Security, ...). Each group is one row in the
// settings table holding a JSON document. Groups are cached in memory, since
// auth reads Security on every request, and the cache is replaced on save.
// Credentials inside a group (API key, OIDC client secret) are encrypted in
// the database with internal/secrets.
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/saltglass/reelkeeper/internal/naming"
	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/store"
)

// Group keys, as stored in the settings table.
const (
	GroupGeneral  = "general"
	GroupSecurity = "security"
	// groupKeyCheck holds a value sealed with the secret key, so a changed
	// key is noticed at startup instead of when a credential is first used.
	groupKeyCheck = "keycheck"
)

// keyCheckPlaintext keeps the app's former name because changing it would break existing credentials and older backups.
const keyCheckPlaintext = "aerie"

// Service loads, validates and saves settings.
type Service struct {
	db     *store.Store
	box    *secrets.Box
	logger *slog.Logger

	mu            sync.RWMutex
	general       General
	security      Security
	metadata      Metadata
	naming        naming.Templates
	labels        Labels
	solver        Solver
	grabbing      Grabbing
	notifications Notifications

	downloadClient DownloadClient
	downloadMove   DownloadMove
	cleanup        Cleanup
	backups        Backups
	// sealedKeep holds encrypted values that couldn't be decrypted (the
	// secret key changed). They're written back unchanged on save, so
	// restoring the old key brings them back. Keyed by field path.
	sealedKeep map[string]string

	listenersMu sync.Mutex
	listeners   []func(group string)
}

// New loads every group, filling in defaults for anything never saved, and
// generates the API key on first start.
func New(ctx context.Context, db *store.Store, box *secrets.Box, logger *slog.Logger) (*Service, error) {
	s := &Service{db: db, box: box, logger: logger, sealedKeep: map[string]string{}}

	if err := s.checkKey(ctx); err != nil {
		return nil, err
	}

	s.general = DefaultGeneral()
	if _, err := s.load(ctx, GroupGeneral, &s.general); err != nil {
		return nil, err
	}

	var stored storedSecurity
	stored.Security = DefaultSecurity()
	found, err := s.load(ctx, GroupSecurity, &stored)
	if err != nil {
		return nil, err
	}
	s.security = s.openSecurity(stored)

	if err := s.loadMetadata(ctx); err != nil {
		return nil, err
	}

	s.naming = naming.Defaults()
	if _, err := s.load(ctx, GroupNaming, &s.naming); err != nil {
		return nil, err
	}

	s.solver = DefaultSolver()
	if _, err := s.load(ctx, GroupSolver, &s.solver); err != nil {
		return nil, err
	}

	if err := s.loadDownloadClient(ctx); err != nil {
		return nil, err
	}
	if err := s.loadDownloadMove(ctx); err != nil {
		return nil, err
	}

	s.cleanup = DefaultCleanup()
	if _, err := s.load(ctx, GroupCleanup, &s.cleanup); err != nil {
		return nil, err
	}

	if err := s.loadGrabbing(ctx); err != nil {
		return nil, err
	}

	if err := s.loadNotifications(ctx); err != nil {
		return nil, err
	}

	s.backups = DefaultBackups()
	if _, err := s.load(ctx, GroupBackups, &s.backups); err != nil {
		return nil, err
	}

	s.labels = DefaultLabels()
	if _, err := s.load(ctx, GroupLabels, &s.labels); err != nil {
		return nil, err
	}
	// A part dropped in a later version is ignored rather than refused.
	_ = s.labels.normalize()

	if !found || (s.security.APIKey == "" && s.sealedKeep["apiKey"] == "") {
		s.security.APIKey = NewAPIKey()
		if err := s.saveSecurity(ctx, s.security); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// OnChange registers fn to be called (synchronously) after a group is saved.
func (s *Service) OnChange(fn func(group string)) {
	s.listenersMu.Lock()
	defer s.listenersMu.Unlock()
	s.listeners = append(s.listeners, fn)
}

func (s *Service) notify(group string) {
	s.listenersMu.Lock()
	ls := append([]func(string){}, s.listeners...)
	s.listenersMu.Unlock()
	for _, fn := range ls {
		fn(group)
	}
}

// load reads a group's JSON into dst. Fields missing from the stored JSON
// keep whatever dst already holds (the defaults), so adding a setting in a
// later version doesn't need a migration. found is false if never saved.
func (s *Service) load(ctx context.Context, key string, dst any) (found bool, err error) {
	var value string
	err = s.db.DB.NewRaw("SELECT value FROM settings WHERE key = ?", key).Scan(ctx, &value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("load %s settings: %w", key, err)
	}
	if err := json.Unmarshal([]byte(value), dst); err != nil {
		return false, fmt.Errorf("load %s settings: stored value is not valid JSON: %w", key, err)
	}
	return true, nil
}

// put upserts a group's JSON.
func (s *Service) put(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.DB.NewRaw(
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, string(b), time.Now().UTC(),
	).Exec(ctx)
	if err != nil {
		return fmt.Errorf("save %s settings: %w", key, err)
	}
	return nil
}

// checkKey warns loudly if stored credentials were encrypted with a
// different key than the current one. Reelkeeper still starts, so the user can
// reach the UI and re-enter them.
func (s *Service) checkKey(ctx context.Context) error {
	var sealed string
	found, err := s.load(ctx, groupKeyCheck, &sealed)
	if err != nil {
		return err
	}
	if !found {
		v, err := s.box.Seal(keyCheckPlaintext)
		if err != nil {
			return err
		}
		return s.put(ctx, groupKeyCheck, v)
	}
	if plain, err := s.box.Open(sealed); err != nil || plain != keyCheckPlaintext {
		s.logger.Error("the secret key has changed since credentials were saved; " +
			"stored passwords, secrets and the API key can't be read. Restore the previous " +
			"REELKEEPER_SECRET_KEY, or re-enter them in Settings")
	}
	return nil
}

// openSecret decrypts one stored credential. One that can't be decrypted is
// logged, treated as empty, and remembered so saving doesn't lose it.
func (s *Service) openSecret(field, sealed string) string {
	if sealed == "" {
		return ""
	}
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
