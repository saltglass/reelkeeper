//go:build lab

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strings"
	"time"
)

func cmdTouchJob(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("touchjob", flag.ExitOnError)
	db := fs.String("db", "", "database")
	kind := fs.String("kind", "", "task kind")
	ago := fs.Duration("ago", 20*time.Minute, "how long ago its last run started")
	_ = fs.Parse(args)
	d := openDB(ctx, *db)
	defer d.close()
	when := time.Now().UTC().Add(-*ago).Truncate(time.Microsecond)
	res, err := d.st.DB.NewRaw(`UPDATE jobs SET started_at = ?, finished_at = ?, created_at = ?, status = 'succeeded'
		WHERE id = (SELECT MAX(id) FROM jobs WHERE kind = ?)`, when, when, when, *kind).Exec(ctx)
	if err != nil {
		fatal("%v", err)
	}
	n, _ := res.RowsAffected()
	fmt.Printf("moved %d job(s) of %s to %s\n", n, *kind, when.Format(time.RFC3339))
}

func cmdSQL(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("sql", flag.ExitOnError)
	db := fs.String("db", "", "database")
	exec := fs.String("exec", "", "statement to run")
	query := fs.String("query", "", "query to print (tab separated)")
	_ = fs.Parse(args)
	d := openDB(ctx, *db)
	defer d.close()
	if *exec != "" {
		res, err := d.sql.ExecContext(ctx, *exec)
		if err != nil {
			fatal("%v", err)
		}
		n, _ := res.RowsAffected()
		fmt.Printf("%d rows affected\n", n)
	}
	if *query != "" {
		rows, err := d.sql.QueryContext(ctx, *query)
		if err != nil {
			fatal("%v", err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				fatal("%v", err)
			}
			out := make([]string, len(cols))
			for i, v := range vals {
				if b, ok := v.([]byte); ok {
					v = string(b)
				}
				out[i] = fmt.Sprint(v)
			}
			fmt.Println(strings.Join(out, "\t"))
		}
	}
}

// --- damaging backup files ----------------------------------------------------

func gunzip(b []byte) []byte {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		fatal("not a gzip file: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		fatal("gunzip: %v", err)
	}
	return out
}

func gz(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

// refix recomputes the checksum in the last line so a hand-edited backup is internally consistent
// (a "forged" file: every check except the ones on its meaning passes).
func refix(lines [][]byte) []byte {
	endIdx := len(lines) - 1
	h := sha256.New()
	for _, l := range lines[:endIdx] {
		h.Write(l)
		h.Write([]byte("\n"))
	}
	var end struct {
		End struct {
			Rows   map[string]int `json:"rows"`
			SHA256 string         `json:"sha256"`
		} `json:"end"`
	}
	if err := json.Unmarshal(lines[endIdx], &end); err != nil {
		fatal("last line: %v", err)
	}
	end.End.SHA256 = hex.EncodeToString(h.Sum(nil))
	b, _ := json.Marshal(end)
	lines[endIdx] = b
	return joinLines(lines)
}

func joinLines(lines [][]byte) []byte {
	return append(bytes.Join(lines, []byte("\n")), '\n')
}

func splitLines(b []byte) [][]byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	return bytes.Split(b, []byte("\n"))
}

func editHeader(lines [][]byte, edit func(h map[string]any)) {
	var l map[string]map[string]any
	if err := json.Unmarshal(lines[0], &l); err != nil {
		fatal("header: %v", err)
	}
	edit(l["header"])
	lines[0], _ = json.Marshal(l)
}

func cmdDamage(args []string) {
	fs := flag.NewFlagSet("damage", flag.ExitOnError)
	in := fs.String("in", "", "a good backup")
	out := fs.String("out", "", "where to write the damaged copy")
	kind := fs.String("kind", "", "flip|truncate|droplastline|dropmiddleline|garbage|garbage-inside|empty|header|header-newer|header-older|header-format|notbackup|notbackup-gz|gzcrc|gztrailer|headeronly")
	_ = fs.Parse(args)
	src, err := os.ReadFile(*in)
	if err != nil {
		fatal("%v", err)
	}
	var res []byte
	switch *kind {
	case "flip":
		res = append([]byte(nil), src...)
		res[len(res)/2] ^= 0x10
	case "truncate":
		res = src[:len(src)/2]
	case "empty":
		res = []byte{}
	case "garbage":
		g := make([]byte, 200)
		for i := range g {
			g[i] = byte(rand.IntN(256))
		}
		res = append(append([]byte(nil), src...), g...)
	case "garbage-inside": // extra lines after the end line, inside the compressed text
		res = gz(append(gunzip(src), []byte("[1,2,3]\n{\"junk\":true}\n")...))
	case "droplastline":
		l := splitLines(gunzip(src))
		res = gz(joinLines(l[:len(l)-1]))
	case "dropmiddleline":
		l := splitLines(gunzip(src))
		// remove a data row (a line starting with '[') from the middle
		for i := len(l) / 2; i < len(l)-1; i++ {
			if len(l[i]) > 0 && l[i][0] == '[' {
				l = append(l[:i], l[i+1:]...)
				break
			}
		}
		res = gz(joinLines(l))
	case "headeronly":
		l := splitLines(gunzip(src))
		res = gz(joinLines(l[:1]))
	case "header": // schemaVersion edited, checksum left alone
		l := splitLines(gunzip(src))
		editHeader(l, func(h map[string]any) { h["schemaVersion"] = 999 })
		res = gz(joinLines(l))
	case "header-newer": // schemaVersion edited and the checksum made to match
		l := splitLines(gunzip(src))
		editHeader(l, func(h map[string]any) { h["schemaVersion"] = 999 })
		res = gz(refix(l))
	case "header-older":
		l := splitLines(gunzip(src))
		editHeader(l, func(h map[string]any) { h["schemaVersion"] = 5 })
		res = gz(refix(l))
	case "header-format":
		l := splitLines(gunzip(src))
		editHeader(l, func(h map[string]any) { h["formatVersion"] = 2 })
		res = gz(refix(l))
	case "notbackup":
		res = []byte("Dear diary,\nthis is not a backup.\n" + strings.Repeat("lorem ipsum ", 500))
	case "notbackup-gz":
		res = gz([]byte("{\"hello\":\"world\"}\nthis is not a backup\n"))
	case "gzcrc":
		res = append([]byte(nil), src...)
		res[len(res)-8] ^= 0xff // the CRC-32 in the gzip trailer
	case "gztrailer":
		res = src[:len(src)-8]
	default:
		fatal("unknown damage kind %q", *kind)
	}
	if err := os.WriteFile(*out, res, 0o644); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("wrote %s (%d bytes, %s)\n", *out, len(res), *kind)
}

// cmdVerifyBlocklist checks the columns migration 18 derives for old rows (blocklist.rule and
// blocklist.expires_at) against the rules in that migration, written out again here from its SQL:
//
//	rule: by the reason's wording ("Cleanup: ..." kinds, "Blocklisted in ..." by media type, else "removed")
//	expires_at: created_at + 30 days for "Cleanup: %" reasons except rule "unregistered"; otherwise NULL.
func cmdVerifyBlocklist(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("verify-blocklist", flag.ExitOnError)
	db := fs.String("db", "", "database")
	_ = fs.Parse(args)
	d := openDB(ctx, *db)
	defer d.close()
	rows, err := d.sql.QueryContext(ctx, `SELECT b.id, b.reason, b.created_at, b.rule, b.expires_at, COALESCE(t.media_type,'')
		FROM blocklist b LEFT JOIN titles t ON t.id = b.title_id`)
	if err != nil {
		fatal("%v", err)
	}
	defer rows.Close()
	cat := &column{Kind: kTime}
	bad, n, subMs := 0, 0, 0
	for rows.Next() {
		var id int64
		var reason, rule, media string
		var created, expires any
		if err := rows.Scan(&id, &reason, &created, &rule, &expires, &media); err != nil {
			fatal("%v", err)
		}
		n++
		want := "removed"
		switch {
		case strings.HasPrefix(reason, "Cleanup: The tracker says this torrent no longer exists"):
			want = "unregistered"
		case strings.HasPrefix(reason, "Cleanup: qBittorrent reports an error or missing files"):
			want = "missing"
		case strings.HasPrefix(reason, "Cleanup: It has been trying to get the torrent's details"):
			want = "metadata"
		case strings.HasPrefix(reason, "Cleanup: No progress for"):
			want = "stalled"
		case strings.HasPrefix(reason, "Cleanup: Slower than"):
			want = "slow"
		case strings.HasPrefix(reason, "Cleanup: Its import failed"):
			want = "import-failed"
		case strings.HasPrefix(reason, "Cleanup: "):
			want = "cleanup"
		case strings.HasPrefix(reason, "Blocklisted in ") && media == "series":
			want = "sonarr"
		case strings.HasPrefix(reason, "Blocklisted in "):
			want = "radarr"
		}
		c, _ := canonValue(cat, created)
		var wantExp any
		if strings.HasPrefix(reason, "Cleanup: ") && want != "unregistered" {
			t, _ := time.Parse("2006-01-02T15:04:05.000000Z", fmt.Sprint(c))
			wantExp = canonTime(t.Add(30 * 24 * time.Hour))
		}
		gotExp, _ := canonValue(cat, expires)
		// SQLite's migration works in milliseconds (strftime %f), Postgres' in microseconds; accept either.
		expOK := fmt.Sprint(gotExp) == fmt.Sprint(wantExp)
		if !expOK && gotExp != nil && wantExp != nil {
			g, _ := time.Parse("2006-01-02T15:04:05.000000Z", fmt.Sprint(gotExp))
			w, _ := time.Parse("2006-01-02T15:04:05.000000Z", fmt.Sprint(wantExp))
			if diff := w.Sub(g); diff > -time.Millisecond && diff < time.Millisecond {
				expOK = true
				subMs++
			}
		}
		if rule != want || !expOK {
			bad++
			if bad <= 5 {
				fmt.Printf("blocklist %d: rule %q (want %q), expires %v (want %v), reason %.60q\n", id, rule, want, gotExp, wantExp, reason)
			}
		}
	}
	fmt.Printf("blocklist: %d rows checked, %d wrong (%d expires_at values are rounded down to the millisecond by the migration)\n", n, bad, subMs)
	if bad > 0 {
		os.Exit(1)
	}
}
