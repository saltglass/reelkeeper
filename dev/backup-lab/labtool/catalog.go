//go:build lab

package main

// The labtool reads a database's own catalog (tables, columns, keys) so it
// works on any schema version. This is deliberately a separate
// implementation from internal/backup's catalog: the backup package is what
// the lab tests, so the lab must not share its code.

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/saltglass/reelkeeper/internal/store"
)

// Kinds of column, as the lab sees them.
const (
	kInt   = "int"
	kFloat = "float"
	kBool  = "bool"
	kText  = "text"
	kJSON  = "json"
	kTime  = "time"
	kBytes = "bytes"
)

// jsonColumns are the columns that hold JSON (JSONB on Postgres, TEXT on
// SQLite), from the Postgres migrations. SQLite can't tell us.
var jsonColumns = map[string]bool{
	"settings.value": true, "jobs.args": true, "titles.genres": true, "titles.alt_titles": true,
	"media_files.release_info": true, "media_files.media_info": true, "media_files.episode_claim": true,
	"scan_items.candidates": true, "scan_items.files": true, "indexers.settings": true,
	"indexers.categories": true, "profiles.simple": true, "profiles.advanced": true,
	"history.data": true, "downloads.cleanup_track": true,
}

// wideInts are INTEGER columns that are BIGINT on Postgres (the SQLite
// migrations call them all INTEGER), so max-int64 values are safe there
// and in both directions between the engines.
var wideInts = map[string]bool{
	"media_files.size": true, "downloads.size": true, "indexer_usage.total_ms": true,
}

type column struct {
	Name     string
	Decl     string
	Kind     string
	NotNull  bool
	Default  string
	Identity bool // the database hands out its values (rowid alias or identity column)
	Bits     int  // for ints: 16, 32 or 64
	PK       bool
}

type foreignKey struct {
	Col, RefTable, RefCol string
}

type tableInfo struct {
	Name    string
	Cols    []*column
	byName  map[string]*column
	FKs     []foreignKey
	Uniques [][]string // unique column sets, primary keys included
}

func (t *tableInfo) col(name string) *column { return t.byName[name] }

func (t *tableInfo) fkFor(col string) *foreignKey {
	for i := range t.FKs {
		if t.FKs[i].Col == col {
			return &t.FKs[i]
		}
	}
	return nil
}

type catalog struct {
	Engine store.Engine
	Tables map[string]*tableInfo
	Order  []string // parents before children
}

func (c *catalog) table(name string) *tableInfo { return c.Tables[name] }

func readCatalog(ctx context.Context, db *sql.DB, engine store.Engine) (*catalog, error) {
	c := &catalog{Engine: engine, Tables: map[string]*tableInfo{}}
	var err error
	if engine == store.Postgres {
		err = c.readPostgres(ctx, db)
	} else {
		err = c.readSQLite(ctx, db)
	}
	if err != nil {
		return nil, err
	}
	for _, t := range c.Tables {
		t.byName = map[string]*column{}
		for _, col := range t.Cols {
			t.byName[col.Name] = col
			if jsonColumns[t.Name+"."+col.Name] {
				col.Kind = kJSON
			}
			if col.Kind == kInt && col.Bits == 0 {
				col.Bits = 32
				if wideInts[t.Name+"."+col.Name] {
					col.Bits = 64
				}
			}
			// ids and references are 64-bit on both engines
			if t.fkFor(col.Name) != nil || (col.PK && col.Identity) {
				col.Bits = 64
			}
		}
	}
	c.sortTables()
	return c, nil
}

func (c *catalog) sortTables() {
	names := make([]string, 0, len(c.Tables))
	for n := range c.Tables {
		names = append(names, n)
	}
	sort.Strings(names)
	done := map[string]bool{}
	var visit func(n string)
	visit = func(n string) {
		if done[n] {
			return
		}
		done[n] = true
		for _, fk := range c.Tables[n].FKs {
			if fk.RefTable != n && c.Tables[fk.RefTable] != nil {
				visit(fk.RefTable)
			}
		}
		c.Order = append(c.Order, n)
	}
	for _, n := range names {
		visit(n)
	}
}

func sqliteKind(decl string) string {
	d := strings.ToUpper(decl)
	switch {
	case strings.Contains(d, "BOOL"):
		return kBool
	case strings.Contains(d, "INT"):
		return kInt
	case strings.Contains(d, "REAL"), strings.Contains(d, "FLOA"), strings.Contains(d, "DOUB"):
		return kFloat
	case strings.Contains(d, "TIMESTAMP"), strings.Contains(d, "DATETIME"):
		return kTime
	case strings.Contains(d, "BLOB"):
		return kBytes
	}
	return kText
}

func (c *catalog) readSQLite(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		names = append(names, n)
	}
	rows.Close()
	for _, n := range names {
		t := &tableInfo{Name: n}
		cr, err := db.QueryContext(ctx, `SELECT name, type, "notnull", COALESCE(dflt_value,''), pk FROM pragma_table_info(?) ORDER BY cid`, n)
		if err != nil {
			return err
		}
		pkCount := 0
		for cr.Next() {
			var col column
			var pk int
			var nn int
			if err := cr.Scan(&col.Name, &col.Decl, &nn, &col.Default, &pk); err != nil {
				return err
			}
			col.NotNull = nn != 0
			col.Kind = sqliteKind(col.Decl)
			col.PK = pk > 0
			if pk > 0 {
				pkCount++
			}
			if col.Kind == kInt && pk > 0 && strings.EqualFold(col.Decl, "INTEGER") {
				col.Identity = true // INTEGER PRIMARY KEY is the rowid
			}
			if col.Kind == kInt && strings.Contains(strings.ToUpper(col.Decl), "BIGINT") {
				col.Bits = 64
			}
			t.Cols = append(t.Cols, &col)
		}
		cr.Close()
		if pkCount > 1 { // a composite key is not a rowid alias
			for _, col := range t.Cols {
				col.Identity = false
			}
		}
		fr, err := db.QueryContext(ctx, `SELECT "from", "table", COALESCE("to",'id') FROM pragma_foreign_key_list(?)`, n)
		if err != nil {
			return err
		}
		for fr.Next() {
			var fk foreignKey
			if err := fr.Scan(&fk.Col, &fk.RefTable, &fk.RefCol); err != nil {
				return err
			}
			t.FKs = append(t.FKs, fk)
		}
		fr.Close()
		ir, err := db.QueryContext(ctx, `SELECT name FROM pragma_index_list(?) WHERE "unique" = 1 AND partial = 0`, n)
		if err != nil {
			return err
		}
		var idx []string
		for ir.Next() {
			var s string
			if err := ir.Scan(&s); err != nil {
				return err
			}
			idx = append(idx, s)
		}
		ir.Close()
		for _, ix := range idx {
			xr, err := db.QueryContext(ctx, `SELECT COALESCE(name,'') FROM pragma_index_info(?) ORDER BY seqno`, ix)
			if err != nil {
				return err
			}
			var cols []string
			expr := false
			for xr.Next() {
				var s string
				if err := xr.Scan(&s); err != nil {
					return err
				}
				if s == "" {
					expr = true
				}
				cols = append(cols, s)
			}
			xr.Close()
			if !expr && len(cols) > 0 {
				t.Uniques = append(t.Uniques, cols)
			}
		}
		c.Tables[n] = t
	}
	return nil
}

func pgKind(dt string) (string, int) {
	switch dt {
	case "boolean":
		return kBool, 0
	case "smallint":
		return kInt, 16
	case "integer":
		return kInt, 32
	case "bigint":
		return kInt, 64
	case "real", "double precision", "numeric":
		return kFloat, 0
	case "json", "jsonb":
		return kJSON, 0
	case "bytea":
		return kBytes, 0
	}
	if strings.HasPrefix(dt, "timestamp") || dt == "date" {
		return kTime, 0
	}
	return kText, 0
}

func (c *catalog) readPostgres(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `
		SELECT c.table_name, c.column_name, c.data_type, c.is_nullable, COALESCE(c.column_default,''), c.is_identity
		FROM information_schema.columns c
		JOIN information_schema.tables t ON t.table_schema=c.table_schema AND t.table_name=c.table_name
		WHERE c.table_schema = current_schema() AND t.table_type='BASE TABLE'
		ORDER BY c.table_name, c.ordinal_position`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var tn, cn, dt, nullable, def, ident string
		if err := rows.Scan(&tn, &cn, &dt, &nullable, &def, &ident); err != nil {
			return err
		}
		t := c.Tables[tn]
		if t == nil {
			t = &tableInfo{Name: tn}
			c.Tables[tn] = t
		}
		kind, bits := pgKind(dt)
		t.Cols = append(t.Cols, &column{Name: cn, Decl: dt, Kind: kind, Bits: bits, NotNull: nullable == "NO", Default: def,
			Identity: ident == "YES" || strings.HasPrefix(def, "nextval(")})
	}
	rows.Close()
	fr, err := db.QueryContext(ctx, `
		SELECT cl.relname, a.attname, rcl.relname, ra.attname
		FROM pg_constraint c
		JOIN pg_class cl ON cl.oid = c.conrelid
		JOIN pg_class rcl ON rcl.oid = c.confrelid
		CROSS JOIN LATERAL unnest(c.conkey, c.confkey) AS k(attnum, rattnum)
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		JOIN pg_attribute ra ON ra.attrelid = c.confrelid AND ra.attnum = k.rattnum
		WHERE c.contype = 'f' AND cl.relnamespace = current_schema()::regnamespace`)
	if err != nil {
		return err
	}
	for fr.Next() {
		var tn string
		var fk foreignKey
		if err := fr.Scan(&tn, &fk.Col, &fk.RefTable, &fk.RefCol); err != nil {
			return err
		}
		if t := c.Tables[tn]; t != nil {
			t.FKs = append(t.FKs, fk)
		}
	}
	fr.Close()
	ur, err := db.QueryContext(ctx, `
		SELECT t.relname, i.indexrelid::bigint, a.attname, i.indisprimary
		FROM pg_index i
		JOIN pg_class t ON t.oid = i.indrelid
		CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		WHERE i.indisunique AND i.indexprs IS NULL AND i.indpred IS NULL AND k.attnum > 0
		  AND t.relnamespace = current_schema()::regnamespace
		ORDER BY t.relname, i.indexrelid, k.ord`)
	if err != nil {
		return err
	}
	type key struct {
		t string
		i int64
	}
	sets := map[key][]string{}
	var keys []key
	pks := map[key]bool{}
	for ur.Next() {
		var tn, an string
		var id int64
		var pk bool
		if err := ur.Scan(&tn, &id, &an, &pk); err != nil {
			return err
		}
		k := key{tn, id}
		if _, ok := sets[k]; !ok {
			keys = append(keys, k)
		}
		sets[k] = append(sets[k], an)
		pks[k] = pk
	}
	ur.Close()
	for _, k := range keys {
		t := c.Tables[k.t]
		if t == nil {
			continue
		}
		t.Uniques = append(t.Uniques, sets[k])
		if pks[k] {
			for _, cn := range sets[k] {
				for _, col := range t.Cols {
					if col.Name == cn {
						col.PK = true
					}
				}
			}
		}
	}
	return nil
}

func qid(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func (c *catalog) colList(t *tableInfo, cols []string) string {
	q := make([]string, len(cols))
	for i, n := range cols {
		q[i] = qid(n)
	}
	return strings.Join(q, ", ")
}

func mustCatalog(ctx context.Context, d *database) *catalog {
	c, err := readCatalog(ctx, d.sql, d.engine)
	if err != nil {
		fatal("read the catalog: %v", err)
	}
	if len(c.Tables) == 0 {
		fatal("%s has no tables (is it migrated?)", d.label)
	}
	return c
}

func fatal(format string, a ...any) {
	fmt.Fprintf(stderr(), "labtool: "+format+"\n", a...)
	exit(1)
}
