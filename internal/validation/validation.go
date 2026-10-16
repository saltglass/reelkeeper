// Package validation collects field-by-field problems with user input, so
// the API can tell the UI exactly which field is wrong and why.
package validation

import (
	"errors"
	"sort"
	"strings"
)

// Errors maps a field path (such as "oidc.issuerUrl") to a plain-English
// problem. A nil or empty Errors means the input is valid.
type Errors map[string]string

// Add records a problem with field. The first problem per field wins.
func (e *Errors) Add(field, message string) {
	if *e == nil {
		*e = Errors{}
	}
	if _, exists := (*e)[field]; !exists {
		(*e)[field] = message
	}
}

// Err returns e as an error, or nil when there are no problems.
func (e Errors) Err() error {
	if len(e) == 0 {
		return nil
	}
	return e
}

// Error lists the problems in field order.
func (e Errors) Error() string {
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+e[k])
	}
	return "invalid input: " + strings.Join(parts, "; ")
}

// As returns the validation errors inside err, if any.
func As(err error) (Errors, bool) {
	var e Errors
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
