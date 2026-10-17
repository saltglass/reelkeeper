package settings

import (
	"context"
	"errors"

	"github.com/saltglass/reelkeeper/internal/secrets"
)

// GroupDownloadMove is the settings group that holds a move between download
// clients while it is running (not something the user edits).
const GroupDownloadMove = "downloadMove"

const downloadMovePassword = "downloadMove.password"

// DownloadMove is a move of torrents from one download client to another that
// is in progress. Changing the client's type replaces the connection settings,
// so the old client's address and login are kept here until the move ends, and
// then dropped. Reelkeeper's grabbing, imports and cleanup wait while one is
// Active.
type DownloadMove struct {
	// Active is true from the moment the move starts until its last torrent is
	// done.
	Active bool `json:"active"`
	// Old is the connection to the client the torrents are moving from. Its
	// password is stored encrypted and never leaves the server.
	Old DownloadClient `json:"old"`
}

type storedDownloadMove struct {
	DownloadMove
	PasswordSealed string `json:"passwordSealed"`
}

// DownloadMove returns the move in progress (Active is false when none is).
func (s *Service) DownloadMove() DownloadMove {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.downloadMove
}

// SaveDownloadMove records a move, with the old client's connection (its
// password encrypted).
func (s *Service) SaveDownloadMove(ctx context.Context, m DownloadMove) error {
	sealed, err := s.box.Seal(m.Old.Password)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if err := s.put(ctx, GroupDownloadMove, storedDownloadMove{DownloadMove: m, PasswordSealed: sealed}); err != nil {
		s.mu.Unlock()
		return err
	}
	s.downloadMove = m
	s.mu.Unlock()
	s.notify(GroupDownloadMove)
	return nil
}

// ClearDownloadMove ends the move: the old client's connection is forgotten.
func (s *Service) ClearDownloadMove(ctx context.Context) error {
	s.mu.Lock()
	if err := s.put(ctx, GroupDownloadMove, storedDownloadMove{}); err != nil {
		s.mu.Unlock()
		return err
	}
	s.downloadMove = DownloadMove{}
	s.mu.Unlock()
	s.notify(GroupDownloadMove)
	return nil
}

func (s *Service) loadDownloadMove(ctx context.Context) error {
	var stored storedDownloadMove
	if _, err := s.load(ctx, GroupDownloadMove, &stored); err != nil {
		return err
	}
	m := stored.DownloadMove
	m.Old.Type = m.Old.ClientType()
	if stored.PasswordSealed != "" {
		if pw, err := s.box.Open(stored.PasswordSealed); err != nil {
			// The old client can't be logged into, so the move will report it.
			if !errors.Is(err, secrets.ErrUndecryptable) {
				return err
			}
			s.logger.Error("can't read a stored credential", "setting", downloadMovePassword, "error", err)
		} else {
			m.Old.Password = pw
		}
	}
	s.downloadMove = m
	return nil
}
