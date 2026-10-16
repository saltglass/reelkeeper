package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Testing unsaved settings (the Test button in the add or edit dialog)
// checks them without saving anything. So that the result isn't lost when
// those exact settings are then saved, the outcome is remembered for a
// while and becomes the saved indexer's health; otherwise a freshly tested
// indexer would show "Not tested yet".
//
// Only a fingerprint of the settings is kept (a SHA-256 hash), never the
// settings themselves, because they include passwords and cookies. Any
// change after the test gives a different fingerprint, so the test no
// longer counts.

// draftTestTTL is how long a test of unsaved settings counts towards
// saving them.
const draftTestTTL = 30 * time.Minute

type draftTest struct {
	at  time.Time
	err string // "" when the test passed
}

// draftKey fingerprints what a test depends on: which indexer is being
// edited (0 for a new one) and how Reelkeeper connects to it.
func draftKey(id int64, in Input, plain, secret map[string]string) string {
	b, _ := json.Marshal(struct {
		ID         int64
		Kind       string
		Definition string
		BaseURL    string
		UseSolver  bool
		Plain      map[string]string
		Secret     map[string]string
	}{id, in.Kind, in.Definition, in.BaseURL, in.UseSolver, plain, secret})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// rememberDraftTest notes the outcome of testing unsaved settings.
func (s *Service) rememberDraftTest(key string, err error) {
	now := s.now()
	t := draftTest{at: now}
	if err != nil {
		t.err = err.Error()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, old := range s.draftTests {
		if now.Sub(old.at) > draftTestTTL {
			delete(s.draftTests, k)
		}
	}
	s.draftTests[key] = t
}

// applyDraftTest gives ix the health from a recent test of exactly these
// settings, if there was one. It reports whether there was.
func (s *Service) applyDraftTest(ix *Indexer, key string) bool {
	s.mu.Lock()
	t, ok := s.draftTests[key]
	delete(s.draftTests, key)
	s.mu.Unlock()
	if !ok || s.now().Sub(t.at) > draftTestTTL {
		return false
	}
	at := t.at.UTC()
	ix.RetryAfter = nil
	if t.err == "" {
		ix.Failures, ix.LastError, ix.LastSuccessAt = 0, "", &at
	} else {
		// Failing, but with no wait before Reelkeeper tries it again: the test
		// was a person checking, not a search going wrong.
		ix.Failures, ix.LastError, ix.LastFailureAt = 1, t.err, &at
	}
	return true
}
