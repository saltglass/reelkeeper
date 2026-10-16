package settings

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// GroupBackups is the settings group for System → Backups.
const GroupBackups = "backups"

// Backups controls Reelkeeper's own database backups (2026-10-11:
// the folder is the user's choice; daily at 03:30, keeping 14).
type Backups struct {
	// Folder is where backups are written. Empty means a "backups" folder
	// inside Reelkeeper's data folder.
	Folder string `json:"folder"`
	// Scheduled turns the daily backup on.
	Scheduled bool `json:"scheduled"`
	// Time is when the daily backup runs, "HH:MM" in local time.
	Time string `json:"time"`
	// Keep is how many scheduled and manual backups to keep; older ones are
	// deleted after each new backup. Safety backups (made before a
	// restore) are kept apart: the newest 3.
	Keep int `json:"keep"`
}

// DefaultBackups backs up daily at 03:30, keeping 14, in the data folder.
func DefaultBackups() Backups { return Backups{Scheduled: true, Time: "03:30", Keep: 14} }

func (b *Backups) normalize() validation.Errors {
	var errs validation.Errors
	b.Folder = strings.TrimSpace(b.Folder)
	if b.Folder != "" && !filepath.IsAbs(b.Folder) {
		errs.Add("folder", "Use a full path, such as /backups, or leave it empty for the data folder.")
	}
	if _, err := time.Parse("15:04", b.Time); err != nil {
		errs.Add("time", "Use a time like 03:30 (24-hour clock).")
	}
	if b.Keep < 1 || b.Keep > 365 {
		errs.Add("keep", "Keep between 1 and 365 backups.")
	}
	return errs
}

// Backups returns the backup settings.
func (s *Service) Backups() Backups {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.backups
}

// SaveBackups validates and stores the backup settings.
func (s *Service) SaveBackups(ctx context.Context, v Backups) (Backups, error) {
	if errs := v.normalize(); errs != nil {
		return Backups{}, errs
	}
	s.mu.Lock()
	if err := s.put(ctx, GroupBackups, v); err != nil {
		s.mu.Unlock()
		return Backups{}, err
	}
	s.backups = v
	s.mu.Unlock()
	s.notify(GroupBackups)
	return v, nil
}

// ReadKeyCheck returns the database's key check, sealed with the secret
// key ("" before the first start). Backups carry it, so a restore can tell
// whether the key it runs with opens the backup's stored credentials. It
// works before settings load: a pending restore runs then.
func ReadKeyCheck(ctx context.Context, db *store.Store) (string, error) {
	s := &Service{db: db}
	var sealed string
	_, err := s.load(ctx, groupKeyCheck, &sealed)
	return sealed, err
}

// KeyOpens reports whether sealed (a key check from a backup) was sealed
// with box's key.
func KeyOpens(box *secrets.Box, sealed string) bool {
	plain, err := box.Open(sealed)
	return err == nil && plain == keyCheckPlaintext
}
