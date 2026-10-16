//go:build lab

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Canonical forms, so a SQLite database and a Postgres database holding the
// same data produce identical dump lines:
//
//	bool    0 or 1
//	int     the integer
//	float   shortest exact decimal (strconv 'g', -1)
//	time    UTC, truncated to the microsecond, RFC 3339
//	json    re-serialized with sorted keys and normalised numbers
//	text    as is
//	bytes   base64
//	NULL    null

var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999 -0700",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02",
}

func parseTime(s string) (time.Time, bool) {
	// Go's String() appends a monotonic reading ("m=+1.23") that is no part of the time.
	if i := strings.Index(s, " m="); i > 0 {
		s = s[:i]
	}
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func canonTime(t time.Time) string {
	return t.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")
}

// canonValue converts a value read from a database into its canonical form:
// nil, int64, string, or json.Number (for floats).
func canonValue(col *column, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if b, ok := v.([]byte); ok {
		if col.Kind == kBytes {
			return base64.StdEncoding.EncodeToString(b), nil
		}
		v = string(b)
	}
	switch col.Kind {
	case kBool:
		switch x := v.(type) {
		case bool:
			if x {
				return int64(1), nil
			}
			return int64(0), nil
		case int64:
			if x != 0 {
				return int64(1), nil
			}
			return int64(0), nil
		case string:
			if b, err := strconv.ParseBool(x); err == nil {
				if b {
					return int64(1), nil
				}
				return int64(0), nil
			}
		}
	case kInt:
		switch x := v.(type) {
		case int64:
			return x, nil
		case bool:
			if x {
				return int64(1), nil
			}
			return int64(0), nil
		case float64:
			if x == float64(int64(x)) {
				return int64(x), nil
			}
		case string:
			if n, err := strconv.ParseInt(x, 10, 64); err == nil {
				return n, nil
			}
		}
	case kFloat:
		switch x := v.(type) {
		case float64:
			return json.Number(strconv.FormatFloat(x, 'g', -1, 64)), nil
		case float32:
			return json.Number(strconv.FormatFloat(float64(x), 'g', -1, 32)), nil
		case int64:
			return json.Number(strconv.FormatFloat(float64(x), 'g', -1, 64)), nil
		case string:
			if f, err := strconv.ParseFloat(x, 64); err == nil {
				return json.Number(strconv.FormatFloat(f, 'g', -1, 64)), nil
			}
		}
	case kTime:
		switch x := v.(type) {
		case time.Time:
			return canonTime(x), nil
		case string:
			if t, ok := parseTime(x); ok {
				return canonTime(t), nil
			}
			return "unparsed:" + x, nil
		}
	case kJSON:
		if s, ok := v.(string); ok {
			if c, ok := canonJSON(s); ok {
				return c, nil
			}
			return "invalid-json:" + s, nil
		}
	case kText:
		switch x := v.(type) {
		case string:
			return x, nil
		case time.Time:
			return canonTime(x), nil
		case int64:
			return strconv.FormatInt(x, 10), nil
		case float64:
			return strconv.FormatFloat(x, 'g', -1, 64), nil
		}
	}
	return nil, fmt.Errorf("can't canonicalize a %T in a %s column", v, col.Kind)
}

func canonJSON(s string) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	if dec.More() {
		return "", false
	}
	var b bytes.Buffer
	writeCanon(&b, v)
	return b.String(), true
}

func writeCanon(b *bytes.Buffer, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case json.Number:
		b.WriteString(canonNumber(x))
	case string:
		b.Write(jsonString(x))
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanon(b, e)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.Write(jsonString(k))
			b.WriteByte(':')
			writeCanon(b, x[k])
		}
		b.WriteByte('}')
	}
}

func jsonString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// canonNumber writes a JSON number in one form however it was written
// (1.0, 1e0 and 1 are the same number; Postgres' jsonb prints 5e-324 in full).
// The form is 0.<digits>e<exponent> with no leading or trailing zeros in the digits.
func canonNumber(n json.Number) string {
	s := string(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimLeft(s, "+-")
	mant, exp := s, 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mant = s[:i]
		exp, _ = strconv.Atoi(strings.TrimPrefix(s[i+1:], "+"))
	}
	intPart, frac := mant, ""
	if i := strings.Index(mant, "."); i >= 0 {
		intPart, frac = mant[:i], mant[i+1:]
	}
	digits := intPart + frac
	point := len(intPart) + exp
	trimmed := strings.TrimLeft(digits, "0")
	point -= len(digits) - len(trimmed)
	trimmed = strings.TrimRight(trimmed, "0")
	if trimmed == "" {
		return "0"
	}
	out := trimmed + "e" + strconv.Itoa(point)
	if neg {
		out = "-" + out
	}
	return out
}
