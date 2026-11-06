package grab

import (
	"strings"

	"github.com/saltglass/reelkeeper/internal/indexer"
)

// splitNoClient separates the indexers a search didn't ask because no client
// of their kind is set up (a Usenet indexer without a Usenet client) from
// those that failed or were resting. Only the latter make a search that
// found nothing count as undone and be tried again soon: asking again
// won't help until a client is set up. The former come back as a sentence
// for the History message (empty when there are none), so "0 releases"
// says which indexers weren't asked, and why.
func splitNoClient(all []indexer.IndexerError) (failed []indexer.IndexerError, note string) {
	byReason := map[string][]string{}
	var reasons []string
	for _, f := range all {
		if !f.NoClient {
			failed = append(failed, f)
			continue
		}
		if _, ok := byReason[f.Message]; !ok {
			reasons = append(reasons, f.Message)
		}
		byReason[f.Message] = append(byReason[f.Message], f.Indexer)
	}
	for _, r := range reasons {
		names := byReason[r]
		verb := "wasn't"
		if len(names) > 1 {
			verb = "weren't"
		}
		// The reason reads "not searched: no ... is set up (...)".
		why := strings.TrimPrefix(r, "not searched: ")
		note += " " + joinNames(names) + " " + verb + " searched: " + why + "."
	}
	return failed, note
}

// joinNames lists names in plain English: "a", "a and b", "a, b and c".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
