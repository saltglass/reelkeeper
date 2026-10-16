package notify

import "time"

// SetClock replaces the clock, for the de-duplication tests.
func SetClock(s *Service, now func() time.Time) { s.now = now }

// BuildMessageForTest exposes buildMessage.
var BuildMessageForTest = buildMessage
