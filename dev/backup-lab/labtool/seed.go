//go:build lab

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/auth"
	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/store"
)

type rec map[string]any

// seeder fills a migrated database from its catalog.
type seeder struct {
	*gen
	ctx   context.Context
	d     *database
	cat   *catalog
	box   *secrets.Box
	scale float64
	kinds []string

	nextID map[string]int64
	ids    map[string][]int64
	// keep holds the few columns of some tables that later tables refer back to.
	keep map[string][]rec
	byID map[string]map[int64]rec
	seen map[string]map[string]bool

	// derived indexes, built as tables are filled
	moviesTitles, seriesTitles []rec
	movieVersions, seriesVers  []rec
	episodesByTitle            map[int64][]int64
	tvFiles                    []rec
	downloadsTV                []rec
	rootsByType                map[string][]rec
	profilesFor                map[string][]int64
	counts                     map[string]int
	// pickHook, when set (by mutate), chooses parent ids from the live database.
	pickHook func(table string) (int64, bool)
}

// Rows per table at scale 1: 4,000 movies, 400 series, 40,000 episodes, about as many media
// files, and 120,000 history rows.
var baseRows = map[string]int{
	"titles": 4400, "versions": 4900, "seasons": 3200, "episodes": 40000, "media_files": 44000, "episode_files": 36000,
	"scan_items": 300, "skipped_folders": 200, "history": 120000, "downloads": 3000, "blocklist": 3000,
	"cleanup_events": 3000, "download_episodes": 2500, "episode_monitoring": 6000, "episode_searches": 12000,
	"version_tags": 800, "indexer_usage": 2160, "imported_records": 3000, "jobs": 3000,
}

// Small tables that don't grow with the scale.
var fixedRows = map[string]int{"root_folders": 8, "profiles": 6, "tags": 25, "indexers": 12, "backfill": 30}

// tables the seeder leaves to special code or to the app.
var specialTables = map[string]bool{"users": true, "sessions": true, "settings": true, "goose_db_version": true}

func cmdSeed(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	db := fs.String("db", "", "database")
	scale := fs.Float64("scale", 1, "1 = about 4,000 movies, 400 series, 40,000 episodes, 120,000 history rows")
	seedN := fs.Uint64("seed", 1, "random seed")
	key := fs.String("key", "", "secret key to seal indexer credentials with (the instance's own)")
	kinds := fs.String("kinds", "", "task kinds to quiesce (comma separated)")
	_ = fs.Parse(args)
	d := openDB(ctx, *db)
	defer d.close()
	s := &seeder{
		gen: &gen{rng: rand.New(rand.NewPCG(*seedN, 0x5eed)), now: time.Now().UTC().Truncate(time.Second)},
		ctx: ctx, d: d, scale: *scale, cat: mustCatalog(ctx, d),
		nextID: map[string]int64{}, ids: map[string][]int64{}, keep: map[string][]rec{}, byID: map[string]map[int64]rec{},
		seen: map[string]map[string]bool{}, episodesByTitle: map[int64][]int64{}, rootsByType: map[string][]rec{},
		profilesFor: map[string][]int64{}, counts: map[string]int{},
	}
	if *key != "" {
		b, err := secrets.New(*key)
		if err != nil {
			fatal("%v", err)
		}
		s.box = b
	}
	for _, k := range strings.Split(*kinds, ",") {
		if k = strings.TrimSpace(k); k != "" {
			s.kinds = append(s.kinds, k)
		}
	}
	start := time.Now()
	if err := s.run(); err != nil {
		fatal("seed: %v", err)
	}
	total := 0
	names := make([]string, 0)
	for n, c := range s.counts {
		total += c
		names = append(names, n)
	}
	sort.Strings(names)
	var parts []string
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", n, s.counts[n]))
	}
	fmt.Printf("seeded %d rows in %.1fs (scale %.2f): %s\n", total, time.Since(start).Seconds(), s.scale, strings.Join(parts, " "))
}

func (s *seeder) rowsFor(table string) int {
	if n, ok := fixedRows[table]; ok {
		return n
	}
	base, ok := baseRows[table]
	if !ok {
		base = 200 // a table the lab doesn't know: some rows anyway
	}
	return max(1, int(float64(base)*s.scale))
}

func (s *seeder) run() error {
	for _, name := range s.cat.Order {
		if specialTables[name] {
			continue
		}
		if err := s.preload(s.cat.Tables[name]); err != nil {
			return err
		}
	}
	for _, name := range s.cat.Order {
		t := s.cat.Tables[name]
		if specialTables[name] {
			continue
		}
		if err := s.seedTable(t); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := s.seedUsers(); err != nil {
		return fmt.Errorf("users: %w", err)
	}
	if err := s.seedSettings(); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	return s.quiesce()
}

// preload learns what the table already holds (the app's own rows), so ids and unique values don't collide.
func (s *seeder) preload(t *tableInfo) error {
	if pk := idColumn(t); pk != nil {
		rows, err := s.d.sql.QueryContext(s.ctx, "SELECT "+qid(pk.Name)+" FROM "+qid(t.Name)+" ORDER BY 1")
		if err != nil {
			return err
		}
		var maxID int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			s.ids[t.Name] = append(s.ids[t.Name], id)
			maxID = max(maxID, id)
		}
		rows.Close()
		s.nextID[t.Name] = maxID + 1
	}
	return nil
}

func idColumn(t *tableInfo) *column {
	for _, c := range t.Cols {
		if c.PK && c.Identity {
			return c
		}
	}
	return nil
}

func (s *seeder) pickID(table string) (int64, bool) {
	if s.pickHook != nil {
		return s.pickHook(table)
	}
	l := s.ids[table]
	if len(l) == 0 {
		return 0, false
	}
	return l[s.intn(len(l))], true
}

func (s *seeder) set(t *tableInfo, r rec, col string, v any) {
	if t.col(col) != nil {
		r[col] = v
	}
}

// seedTable generates and inserts one table's rows.
func (s *seeder) seedTable(t *tableInfo) error {
	n := s.rowsFor(t.Name)
	id := idColumn(t)
	var cols []string
	for _, c := range t.Cols {
		cols = append(cols, c.Name)
	}
	s.prepareTable(t)
	var batch [][]any
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := s.insert(t, cols, batch)
		batch = batch[:0]
		return err
	}
	made := 0
	for i := 0; i < n; i++ {
		var row rec
		ok := false
		for try := 0; try < 40 && !ok; try++ {
			row = s.makeRow(t, i)
			if row["__skip"] != nil {
				break
			}
			ok = s.claimUnique(t, row)
		}
		if row["__skip"] != nil {
			continue
		}
		if !ok {
			continue // can't find another unique combination; the table is as full as it gets
		}
		if id != nil {
			s.nextID[t.Name] += int64(s.intn(3)) // gaps, like a database that has had deletes
			v := s.nextID[t.Name]
			s.nextID[t.Name]++
			row[id.Name] = v
			s.ids[t.Name] = append(s.ids[t.Name], v)
		}
		s.remember(t, row)
		vals := make([]any, len(cols))
		for j, c := range cols {
			vals[j] = row[c]
		}
		batch = append(batch, vals)
		made++
		if len(batch) >= 200 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	s.counts[t.Name] = made
	return s.afterTable(t)
}

// makeRow builds one row: each column by its kind and hints, then the table's own touches.
func (s *seeder) makeRow(t *tableInfo, i int) rec {
	row := rec{}
	for _, c := range t.Cols {
		if c.PK && c.Identity {
			continue
		}
		row[c.Name] = s.value(t, c, i)
	}
	if f := fillers[t.Name]; f != nil {
		f(s, t, row, i)
	}
	return row
}

func (s *seeder) inUnique(t *tableInfo, col string) bool {
	for _, u := range t.Uniques {
		for _, c := range u {
			if c == col {
				return true
			}
		}
	}
	return false
}

func (s *seeder) uniqueKeys(t *tableInfo, row rec) []string {
	keys := make([]string, len(t.Uniques))
	for i, u := range t.Uniques {
		parts := make([]string, len(u))
		for j, c := range u {
			parts[j] = fmt.Sprint(row[c])
			if row[c] == nil {
				parts[j] = "\x00NULL" // NULLs never collide
			}
		}
		keys[i] = fmt.Sprintf("%d:%s", i, strings.Join(parts, "\x1f"))
		if strings.Contains(keys[i], "\x00NULL") {
			keys[i] = ""
		}
	}
	return keys
}

// claimUnique reserves the row's unique values; false if another row has them.
func (s *seeder) claimUnique(t *tableInfo, row rec) bool {
	if len(t.Uniques) == 0 {
		return true
	}
	if s.seen[t.Name] == nil {
		s.seen[t.Name] = map[string]bool{}
	}
	keys := s.uniqueKeys(t, row)
	// The row's own id isn't known yet, and ids never collide.
	for _, k := range keys {
		if k != "" && s.seen[t.Name][k] {
			return false
		}
	}
	for _, k := range keys {
		if k != "" {
			s.seen[t.Name][k] = true
		}
	}
	return true
}

// value is a column's generic value from its kind and name; fillers refine it.
func (s *seeder) value(t *tableInfo, c *column, i int) any {
	if fk := t.fkFor(c.Name); fk != nil {
		if !c.NotNull && s.chance(0.12) {
			return nil
		}
		if id, ok := s.pickID(fk.RefTable); ok {
			return id
		}
		return nil
	}
	if !c.NotNull && !c.PK && s.chance(0.25) {
		return nil
	}
	key := t.Name + "." + c.Name
	if vals, ok := enumHints[key]; ok {
		return s.pickStr(vals)
	}
	unique := s.inUnique(t, c.Name)
	switch c.Kind {
	case kBool:
		return s.chance(0.5)
	case kInt:
		if unique {
			return int64(i + 1)
		}
		if h, ok := intHints[key]; ok {
			return h(s)
		}
		return s.anInt(c.Bits)
	case kFloat:
		return s.float()
	case kTime:
		return s.randTime()
	case kJSON:
		return s.jsonDefault(c)
	case kBytes:
		return []byte(s.text())
	}
	if unique {
		return fmt.Sprintf("%s-%d %s", c.Name, i, s.word())
	}
	if h, ok := textHints[key]; ok {
		return h(s, i)
	}
	return s.text()
}

func (s *seeder) jsonDefault(c *column) string {
	return s.jsonText(strings.Contains(c.Default, "[]"))
}

// insert writes rows (in the order of cols) in one transaction.
func (s *seeder) insert(t *tableInfo, cols []string, rows [][]any) error {
	override := ""
	if s.d.engine == store.Postgres {
		if id := idColumn(t); id != nil {
			override = " OVERRIDING SYSTEM VALUE"
		}
	}
	head := "INSERT INTO " + qid(t.Name) + " (" + s.cat.colList(t, cols) + ")" + override + " VALUES "
	one := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + ")"
	return s.d.st.DB.RunInTx(s.ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		const per = 100
		for lo := 0; lo < len(rows); lo += per {
			hi := min(lo+per, len(rows))
			var sb strings.Builder
			sb.WriteString(head)
			args := make([]any, 0, (hi-lo)*len(cols))
			for k := lo; k < hi; k++ {
				if k > lo {
					sb.WriteString(", ")
				}
				sb.WriteString(one)
				args = append(args, rows[k]...)
			}
			if _, err := tx.NewRaw(sb.String(), args...).Exec(ctx); err != nil {
				return fmt.Errorf("insert: %w", err)
			}
		}
		return nil
	})
}

func (s *seeder) afterTable(t *tableInfo) error {
	if s.d.engine != store.Postgres {
		return nil
	}
	id := idColumn(t)
	if id == nil {
		return nil
	}
	q := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s', '%s'), COALESCE((SELECT MAX(%s) FROM %s), 1))`,
		qid(t.Name), id.Name, qid(id.Name), qid(t.Name))
	_, err := s.d.sql.ExecContext(s.ctx, q)
	return err
}

// --- users, sessions, settings, quiesce ----------------------------------------

func (s *seeder) seedUsers() error {
	users := s.cat.table("users")
	if users == nil {
		return nil
	}
	type u struct {
		name, pass, issuer, subject string
	}
	list := []u{
		{"seed-user-one", "Seed-User-Pass-1!", "", ""},
		{"Ünï Cödé User", "Pässwörd-Ünï-2?", "", ""},
		{"sso-only-user", "", "https://auth.invalid/application/o/lab/", "sub-1234"},
	}
	var userIDs []int64
	for _, x := range list {
		var hash any
		if x.pass != "" {
			h, err := auth.HashPassword(x.pass)
			if err != nil {
				return err
			}
			hash = h
		}
		row := rec{"username": x.name, "password_hash": hash, "created_at": s.recentTime(300)}
		if x.issuer != "" {
			row["oidc_issuer"], row["oidc_subject"] = x.issuer, x.subject
		} else {
			row["oidc_issuer"], row["oidc_subject"] = nil, nil
		}
		row["last_login_at"] = nil
		if x.pass != "" {
			row["last_login_at"] = s.recentTime(10)
		}
		id := s.take(users)
		row["id"] = id
		userIDs = append(userIDs, id)
		if err := s.insertRec(users, row); err != nil {
			return err
		}
	}
	s.counts["users"] = len(list)
	sess := s.cat.table("sessions")
	if sess == nil {
		return nil
	}
	for i := 0; i < 5; i++ {
		h := sha256.Sum256([]byte(fmt.Sprintf("lab-session-%d", i)))
		row := rec{"token_hash": hex.EncodeToString(h[:]), "user_id": userIDs[i%len(userIDs)],
			"created_at": s.recentTime(3), "expires_at": s.now.Add(48 * time.Hour), "last_seen_at": s.recentTime(1),
			"ip": "10.9.8.7", "user_agent": "labtool/1 (seeded) ✓"}
		if err := s.insertRec(sess, row); err != nil {
			return err
		}
	}
	s.counts["sessions"] = 5
	return s.afterTable(users)
}

func (s *seeder) take(t *tableInfo) int64 {
	if s.nextID[t.Name] == 0 {
		var m int64
		_ = s.d.sql.QueryRowContext(s.ctx, "SELECT COALESCE(MAX(id),0) FROM "+qid(t.Name)).Scan(&m)
		s.nextID[t.Name] = m + 1
	}
	v := s.nextID[t.Name]
	s.nextID[t.Name]++
	return v
}

func (s *seeder) insertRec(t *tableInfo, row rec) error {
	var cols []string
	var vals []any
	for _, c := range t.Cols {
		v, ok := row[c.Name]
		if !ok {
			continue
		}
		cols = append(cols, c.Name)
		vals = append(vals, v)
	}
	return s.insert(t, cols, [][]any{vals})
}

// seedSettings adds odd rows to settings that the app ignores (it reads only the groups it knows),
// for the backup's handling of awkward text and numbers inside JSON.
func (s *seeder) seedSettings() error {
	t := s.cat.table("settings")
	if t == nil {
		return nil
	}
	docs := map[string]any{
		"lab-edge-text": map[string]any{"emoji": "😀🎬👨‍👩‍👧‍👦", "accents": "Amélie Zoë Straße 東京", "quotes": `'single' "double" \back`, "newlines": "a\nb\r\nc\td",
			"empty": "", "null": nil, "placeholder": "? $1 :x @y %s", "sql": "'; DROP TABLE settings; --"},
		"lab-edge-numbers": map[string]any{"zero": 0, "neg": -1, "big": int64(9223372036854775807), "small": int64(-9223372036854775808),
			"float": 0.30000000000000004, "tiny": 1e-9, "huge": 1.7976931348623157e308, "exp": json.Number("1.5E+3"), "frac": 123456.78901234567, "bool": true, "bool2": false},
		"lab-edge-nested": map[string]any{"a": []any{1, "two", nil, []any{}, map[string]any{}}, "b": map[string]any{"c": map[string]any{"d": []any{true}}}},
		"lab-edge-long":   strings.Repeat("0123456789abcdef 長い文字列 ", 70000), // about 2 MB
		"lab-edge-string": "just a string",
		"lab-edge-number": 42,
		"lab-edge-null":   nil,
		"lab-edge-empty":  map[string]any{},
	}
	names := make([]string, 0, len(docs))
	for k := range docs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		b, err := json.Marshal(docs[k])
		if err != nil {
			return err
		}
		row := rec{"key": k, "value": string(b), "updated_at": s.randTime()}
		if err := s.insertRec(t, row); err != nil {
			return err
		}
	}
	s.counts["settings(extra)"] = len(names)
	return nil
}

// quiesce gives every background task a finished run far in the future, so nothing is "due" and
// Reelkeeper does no background work (scans, searches, pruning) that would change the data under test.
func (s *seeder) quiesce() error {
	return quiesceJobs(s.ctx, s.d, s.cat, s.kinds)
}

func quiesceJobs(ctx context.Context, d *database, cat *catalog, kinds []string) error {
	t := cat.table("jobs")
	if t == nil || len(kinds) == 0 {
		return nil
	}
	when := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	return d.st.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for _, k := range kinds {
			cols := []string{"kind", "args", "status", "triggered_by", "progress", "message", "error", "attempts", "created_at", "started_at", "finished_at"}
			vals := []any{k, "{}", "succeeded", "schedule", 100, "Lab: kept quiet", "", 1, when, when, when}
			if t.col("quiet") != nil {
				cols, vals = append(cols, "quiet"), append(vals, true)
			}
			q := "INSERT INTO " + qid("jobs") + " (" + (&catalog{}).colList(t, cols) + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + ")"
			if _, err := tx.NewRaw(q, vals...).Exec(ctx); err != nil {
				return err
			}
		}
		return nil
	})
}

func cmdQuiesce(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("quiesce", flag.ExitOnError)
	db := fs.String("db", "", "database")
	kinds := fs.String("kinds", "", "task kinds")
	_ = fs.Parse(args)
	d := openDB(ctx, *db)
	defer d.close()
	cat := mustCatalog(ctx, d)
	var list []string
	for k := range splitList(*kinds) {
		list = append(list, k)
	}
	sort.Strings(list)
	if err := quiesceJobs(ctx, d, cat, list); err != nil {
		fatal("quiesce: %v", err)
	}
	fmt.Printf("quiesced %d task kinds\n", len(list))
}

var _ = os.Stdout
