package backup

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/store"
)

// Kinds of column, the same on both engines. A backup records each value in
// a neutral form and the kind its column had; a restore converts the value
// to the kind the receiving column has. The engines don't always agree:
// SQLite declares some flags INTEGER where Postgres has boolean, and keeps
// JSON and some timestamps in TEXT columns.
const (
	kindInt   = "int"
	kindFloat = "float"
	kindBool  = "bool"
	kindText  = "text"
	kindJSON  = "json"
	kindTime  = "time"
	kindBytes = "bytes"
)

// Column is one column of a table and its kind.
type Column struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// table is what a backup or restore needs to know about one table.
type table struct {
	name    string
	columns []Column
	// refs are the tables this one points at (foreign keys).
	refs []string
	// identity is a column whose values a Postgres sequence hands out;
	// after a restore the sequence must continue past the restored rows.
	identity string
	// alwaysIdentity: Postgres refuses explicit values for it unless the
	// insert says OVERRIDING SYSTEM VALUE.
	alwaysIdentity bool
}

// skipped are tables a backup leaves out. goose's version table belongs to
// the schema (the backup records the version instead); sign-in sessions
// are short-lived, and after a restore everyone signs in again.
var skipped = map[string]bool{"goose_db_version": true, "sessions": true}

// readCatalog lists the database's own tables (on Postgres, those in the
// current schema), with columns, kinds, foreign keys and identity columns.
// Every table is included, skipped ones too: a restore must empty them.
func readCatalog(ctx context.Context, db bun.IDB, engine store.Engine) ([]*table, error) {
	if engine == store.Postgres {
		return readPostgresCatalog(ctx, db)
	}
	return readSQLiteCatalog(ctx, db)
}

func readSQLiteCatalog(ctx context.Context, db bun.IDB) ([]*table, error) {
	var names []string
	if err := db.NewRaw("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name").
		Scan(ctx, &names); err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	var out []*table
	for _, n := range names {
		t := &table{name: n}
		var cols []struct {
			Name string `bun:"name"`
			Type string `bun:"type"`
			PK   int    `bun:"pk"`
		}
		if err := db.NewRaw("SELECT name, type, pk FROM pragma_table_info(?) ORDER BY cid", n).Scan(ctx, &cols); err != nil {
			return nil, fmt.Errorf("columns of %s: %w", n, err)
		}
		for _, c := range cols {
			t.columns = append(t.columns, Column{Name: c.Name, Kind: sqliteKind(c.Type)})
		}
		var refs []string
		if err := db.NewRaw(`SELECT DISTINCT "table" FROM pragma_foreign_key_list(?)`, n).Scan(ctx, &refs); err != nil {
			return nil, fmt.Errorf("foreign keys of %s: %w", n, err)
		}
		t.refs = refs
		out = append(out, t)
	}
	return out, nil
}

// sqliteKind maps a declared SQLite type to a kind, following SQLite's own
// affinity rules where they apply.
func sqliteKind(declared string) string {
	d := strings.ToUpper(declared)
	switch {
	case strings.Contains(d, "BOOL"):
		return kindBool
	case strings.Contains(d, "INT"):
		return kindInt
	case strings.Contains(d, "REAL"), strings.Contains(d, "FLOA"), strings.Contains(d, "DOUBLE"):
		return kindFloat
	case strings.Contains(d, "TIMESTAMP"), strings.Contains(d, "DATETIME"):
		return kindTime
	case strings.Contains(d, "JSON"):
		return kindJSON
	case strings.Contains(d, "BLOB"):
		return kindBytes
	default:
		return kindText
	}
}

func readPostgresCatalog(ctx context.Context, db bun.IDB) ([]*table, error) {
	var cols []struct {
		Table    string `bun:"table_name"`
		Column   string `bun:"column_name"`
		Type     string `bun:"data_type"`
		Identity string `bun:"is_identity"`
		Generate string `bun:"identity_generation"`
		Default  string `bun:"column_default"`
	}
	if err := db.NewRaw(`
		SELECT c.table_name, c.column_name, c.data_type, c.is_identity,
		       COALESCE(c.identity_generation, '') AS identity_generation,
		       COALESCE(c.column_default, '') AS column_default
		FROM information_schema.columns c
		JOIN information_schema.tables t
		  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = current_schema() AND t.table_type = 'BASE TABLE'
		ORDER BY c.table_name, c.ordinal_position`).Scan(ctx, &cols); err != nil {
		return nil, fmt.Errorf("list columns: %w", err)
	}
	byName := map[string]*table{}
	var out []*table
	for _, c := range cols {
		t := byName[c.Table]
		if t == nil {
			t = &table{name: c.Table}
			byName[c.Table] = t
			out = append(out, t)
		}
		t.columns = append(t.columns, Column{Name: c.Column, Kind: postgresKind(c.Type)})
		switch {
		case c.Identity == "YES":
			t.identity, t.alwaysIdentity = c.Column, c.Generate == "ALWAYS"
		case strings.HasPrefix(c.Default, "nextval("):
			t.identity = c.Column
		}
	}
	var fks []struct {
		From string `bun:"from_table"`
		To   string `bun:"to_table"`
	}
	if err := db.NewRaw(`
		SELECT DISTINCT cl.relname AS from_table, ref.relname AS to_table
		FROM pg_constraint con
		JOIN pg_class cl ON cl.oid = con.conrelid
		JOIN pg_class ref ON ref.oid = con.confrelid
		JOIN pg_namespace ns ON ns.oid = cl.relnamespace
		WHERE con.contype = 'f' AND ns.nspname = current_schema()`).Scan(ctx, &fks); err != nil {
		return nil, fmt.Errorf("list foreign keys: %w", err)
	}
	for _, fk := range fks {
		if t := byName[fk.From]; t != nil {
			t.refs = append(t.refs, fk.To)
		}
	}
	return out, nil
}

func postgresKind(dataType string) string {
	switch dataType {
	case "boolean":
		return kindBool
	case "smallint", "integer", "bigint":
		return kindInt
	case "real", "double precision", "numeric":
		return kindFloat
	case "json", "jsonb":
		return kindJSON
	case "bytea":
		return kindBytes
	}
	if strings.HasPrefix(dataType, "timestamp") || dataType == "date" {
		return kindTime
	}
	return kindText
}

// ordered sorts tables so each comes after the tables it points at: the
// order rows can be inserted in with foreign keys checked. Reelkeeper has
// no table pointing at itself, nor any loop between tables; one would be
// reported rather than guessed around.
func ordered(tables []*table) ([]*table, error) {
	byName := map[string]*table{}
	for _, t := range tables {
		byName[t.name] = t
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].name < tables[j].name })
	var out []*table
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(t *table) error
	visit = func(t *table) error {
		switch state[t.name] {
		case 1:
			return fmt.Errorf("tables %s refer to each other in a loop, which backups can't order", t.name)
		case 2:
			return nil
		}
		state[t.name] = 1
		refs := append([]string(nil), t.refs...)
		sort.Strings(refs)
		for _, r := range refs {
			if r == t.name {
				return fmt.Errorf("table %s refers to itself, which backups can't order", t.name)
			}
			if rt := byName[r]; rt != nil {
				if err := visit(rt); err != nil {
					return err
				}
			}
		}
		state[t.name] = 2
		out = append(out, t)
		return nil
	}
	for _, t := range tables {
		if err := visit(t); err != nil {
			return nil, err
		}
	}
	return out, nil
}
