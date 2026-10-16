package auth

import (
	"sync"
	"time"
)

// limiter slows down password guessing. After `limit` failures for the same
// key (a username or an IP) within `window`, that key is blocked until the
// oldest failure is older than the window. Kept in memory: a restart clears
// it, which is acceptable for a single-process app.
type limiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	failures map[string][]time.Time
	now      func() time.Time
}

// maxLimiterKeys bounds memory if someone sprays many usernames.
const maxLimiterKeys = 10000

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{limit: limit, window: window, failures: map[string][]time.Time{}, now: time.Now}
}

// blocked returns how long key must wait, or 0 if it may try now.
func (l *limiter) blocked(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.recent(key)
	if len(recent) < l.limit {
		return 0
	}
	return recent[len(recent)-l.limit].Add(l.window).Sub(l.now())
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.failures) >= maxLimiterKeys {
		l.prune()
	}
	l.failures[key] = append(l.recent(key), l.now())
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

// recent returns key's failures still inside the window. Caller holds mu.
func (l *limiter) recent(key string) []time.Time {
	cutoff := l.now().Add(-l.window)
	list := l.failures[key]
	i := 0
	for i < len(list) && !list[i].After(cutoff) {
		i++
	}
	list = list[i:]
	if len(list) == 0 {
		delete(l.failures, key)
	} else {
		l.failures[key] = list
	}
	return list
}

// prune drops keys with no recent failures. Caller holds mu.
func (l *limiter) prune() {
	for k := range l.failures {
		l.recent(k)
	}
}
