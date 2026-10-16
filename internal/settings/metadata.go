package settings

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// GroupMetadata is the settings group for TMDB.
const GroupMetadata = "metadata"

// Metadata holds the TMDB settings (Settings → General → Metadata).
type Metadata struct {
	// TMDBAPIKey is a TMDB "API key" (32 characters) or "API Read Access
	// Token" (a long token starting with "eyJ"). Encrypted in the database.
	TMDBAPIKey string `json:"-"`
	// Language is the language for titles and overviews, such as "en-US".
	Language string `json:"language"`
}

type storedMetadata struct {
	Metadata
	TMDBAPIKeySealed string `json:"tmdbApiKeySealed"`
}

// MetadataUpdate is what the user can change. A nil key keeps the stored one.
type MetadataUpdate struct {
	TMDBAPIKey *string
	Language   string
}

// DefaultMetadata returns the settings used until the user saves any.
func DefaultMetadata() Metadata { return Metadata{Language: "en-US"} }

var languageRe = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)

// Metadata returns the current TMDB settings.
func (s *Service) Metadata() Metadata {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.metadata
}

// SaveMetadata validates and stores the TMDB settings.
func (s *Service) SaveMetadata(ctx context.Context, u MetadataUpdate) (Metadata, error) {
	s.mu.Lock()
	next := s.metadata
	next.Language = strings.TrimSpace(u.Language)
	if u.TMDBAPIKey != nil {
		next.TMDBAPIKey = strings.TrimSpace(*u.TMDBAPIKey)
		delete(s.sealedKeep, "metadata.tmdbApiKey")
	}
	var errs validation.Errors
	if !languageRe.MatchString(next.Language) {
		errs.Add("language", "Use a language code like en-US or de-DE.")
	}
	if k := next.TMDBAPIKey; k != "" && len(k) != 32 && !strings.HasPrefix(k, "eyJ") {
		errs.Add("tmdbApiKey", "That doesn't look like a TMDB key. Copy the \"API Key\" (32 characters) or the \"API Read Access Token\" from themoviedb.org → Settings → API.")
	}
	if errs != nil {
		s.mu.Unlock()
		return Metadata{}, errs
	}
	if err := s.saveMetadata(ctx, next); err != nil {
		s.mu.Unlock()
		return Metadata{}, err
	}
	s.metadata = next
	s.mu.Unlock()
	s.notify(GroupMetadata)
	return next, nil
}

func (s *Service) saveMetadata(ctx context.Context, m Metadata) error {
	sealed, err := s.sealOrKeep("metadata.tmdbApiKey", m.TMDBAPIKey)
	if err != nil {
		return err
	}
	return s.put(ctx, GroupMetadata, storedMetadata{Metadata: m, TMDBAPIKeySealed: sealed})
}

func (s *Service) loadMetadata(ctx context.Context) error {
	stored := storedMetadata{Metadata: DefaultMetadata()}
	if _, err := s.load(ctx, GroupMetadata, &stored); err != nil {
		return err
	}
	m := stored.Metadata
	if key, err := s.box.Open(stored.TMDBAPIKeySealed); err != nil {
		if errors.Is(err, secrets.ErrUndecryptable) {
			s.sealedKeep["metadata.tmdbApiKey"] = stored.TMDBAPIKeySealed
		}
		s.logger.Error("can't read a stored credential", "setting", "metadata.tmdbApiKey", "error", err)
	} else {
		m.TMDBAPIKey = key
	}
	s.metadata = m
	return nil
}
