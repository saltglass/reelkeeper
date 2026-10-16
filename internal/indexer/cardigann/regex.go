package cardigann

import (
	"fmt"
	"sync"
	"time"

	"github.com/dlclark/regexp2"
)

// Definitions are written for .NET's regular expressions (lookbehind,
// lookahead and .NET's "$1" replacements included), which Go's regexp
// doesn't support. regexp2 is a port of .NET's engine, so patterns mean
// exactly what their authors tested.

// A pattern that runs away is cut off rather than hanging a search.
const regexTimeout = 2 * time.Second

var (
	regexMu    sync.Mutex
	regexCache = map[string]*regexp2.Regexp{}
)

func compile(pattern string) (*regexp2.Regexp, error) {
	regexMu.Lock()
	defer regexMu.Unlock()
	if re, ok := regexCache[pattern]; ok {
		return re, nil
	}
	re, err := regexp2.Compile(pattern, regexp2.None)
	if err != nil {
		return nil, fmt.Errorf("bad pattern %q: %w", pattern, err)
	}
	re.MatchTimeout = regexTimeout
	if len(regexCache) > 5000 { // definitions use a few hundred; don't grow forever
		regexCache = map[string]*regexp2.Regexp{}
	}
	regexCache[pattern] = re
	return re, nil
}

// reReplace replaces every match, .NET style ("$1", "${name}").
func reReplace(s, pattern, replacement string) (string, error) {
	re, err := compile(pattern)
	if err != nil {
		return "", err
	}
	return re.Replace(s, replacement, -1, -1)
}

// reFind returns the first capture group of the first match (or the
// whole match if the pattern has no groups), and whether it matched.
func reFind(s, pattern string) (string, bool, error) {
	re, err := compile(pattern)
	if err != nil {
		return "", false, err
	}
	m, err := re.FindStringMatch(s)
	if err != nil || m == nil {
		return "", false, err
	}
	groups := m.Groups()
	if len(groups) > 1 {
		return groups[1].String(), true, nil
	}
	return m.String(), true, nil
}
