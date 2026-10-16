package api

import "sort"

// PublicOperationsForTest lists the public endpoints, sorted.
func PublicOperationsForTest() []string {
	out := make([]string, 0, len(publicOperations))
	for k := range publicOperations {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
