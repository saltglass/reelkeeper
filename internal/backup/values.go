package backup

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// encode turns a value read from the database into its neutral form for
// the backup file, given its column's kind:
//
//	null            nil
//	int             JSON number (an int64, exact)
//	float           JSON number
//	bool            true / false
//	text, json      the text (JSON as its JSON text)
//	time            RFC 3339 in UTC, to the microsecond (Postgres' precision)
//	bytes           base64
func encode(v any, kind string) (any, error) {
	if v == nil {
		return nil, nil
	}
	if b, ok := v.([]byte); ok {
		if kind == kindBytes {
			return base64.StdEncoding.EncodeToString(b), nil
		}
		v = string(b)
	}
	switch kind {
	case kindBool:
		switch x := v.(type) {
		case bool:
			return x, nil
		case int64:
			return x != 0, nil
		}
	case kindInt:
		switch x := v.(type) {
		case int64:
			return x, nil
		case bool:
			if x {
				return int64(1), nil
			}
			return int64(0), nil
		}
	case kindFloat:
		switch x := v.(type) {
		case float64:
			return x, nil
		case int64:
			return float64(x), nil
		}
	case kindTime:
		switch x := v.(type) {
		case time.Time:
			return formatTime(x), nil
		case string:
			// A SQLite TIMESTAMP column whose text the driver didn't parse.
			t, err := parseTime(x)
			if err != nil {
				return nil, err
			}
			return formatTime(t), nil
		}
	case kindText, kindJSON:
		switch x := v.(type) {
		case string:
			return x, nil
		case time.Time:
			return formatTime(x), nil
		}
	}
	// Anything else is a type this column shouldn't hold; keep it readable.
	switch x := v.(type) {
	case string, bool, int64, float64:
		return x, nil
	case time.Time:
		return formatTime(x), nil
	}
	return nil, fmt.Errorf("can't back up a %T value in a %s column", v, kind)
}

func formatTime(t time.Time) string {
	return t.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
}

// timeLayouts are the ways a timestamp can be written: the backup's own,
// Bun's SQLite format, and plain dates.
var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02",
}

func parseTime(s string) (time.Time, error) {
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q isn't a date and time", s)
}

// decode turns a value from the backup file (as decoded JSON, numbers as
// json.Number) into what the receiving column, of kind, should get.
func decode(v any, kind string) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch kind {
	case kindBool:
		switch x := v.(type) {
		case bool:
			return x, nil
		case json.Number:
			return x.String() != "0", nil
		case string:
			return strconv.ParseBool(x)
		}
	case kindInt:
		switch x := v.(type) {
		case json.Number:
			return x.Int64()
		case bool:
			if x {
				return int64(1), nil
			}
			return int64(0), nil
		}
	case kindFloat:
		if x, ok := v.(json.Number); ok {
			return x.Float64()
		}
	case kindTime:
		if x, ok := v.(string); ok {
			return parseTime(x)
		}
	case kindBytes:
		if x, ok := v.(string); ok {
			return base64.StdEncoding.DecodeString(x)
		}
	case kindText, kindJSON:
		switch x := v.(type) {
		case string:
			return x, nil
		case json.Number:
			return x.String(), nil
		case bool:
			return strconv.FormatBool(x), nil
		}
	}
	return nil, fmt.Errorf("a %s column can't take %s", kind, describe(v))
}

func describe(v any) string {
	s := fmt.Sprint(v)
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return strings.TrimSpace(fmt.Sprintf("%T %s", v, s))
}
