package auth

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := newLimiter(3, 10*time.Minute)
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if l.blocked("k") != 0 {
			t.Fatalf("blocked after %d failures", i)
		}
		l.fail("k")
		now = now.Add(time.Minute)
	}
	// Failures at 12:00, 12:01, 12:02; now 12:03. Blocked until 12:10.
	if got := l.blocked("k"); got != 7*time.Minute {
		t.Errorf("wait = %s, want 7m", got)
	}
	if l.blocked("other") != 0 {
		t.Error("other keys are unaffected")
	}
	now = now.Add(7 * time.Minute)
	if l.blocked("k") != 0 {
		t.Error("should be unblocked once the oldest failure leaves the window")
	}
	l.fail("k")
	l.fail("k")
	l.reset("k")
	if l.blocked("k") != 0 || len(l.failures) != 0 {
		t.Error("reset should clear the key")
	}
}
