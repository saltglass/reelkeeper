//go:build lab

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/saltglass/reelkeeper/internal/store"
)

// A dump file:
//
//	#DUMP {"engine":"sqlite"}
//	#TABLE <name> <rows> <col,col,...>      columns sorted by name
//	[canonical row as a JSON array]         rows sorted as text
//	#END

func splitList(s string) map[string]bool {
	out := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out[p] = true
		}
	}
	return out
}

// snapshot opens one consistent read-only view of the database.
type snapshot struct {
	q interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}
	done func()
}

func openSnapshot(ctx context.Context, d *database) (*snapshot, error) {
	if d.engine == store.Postgres {
		tx, err := d.sql.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			return nil, err
		}
		return &snapshot{q: tx, done: func() { _ = tx.Rollback() }}, nil
	}
	// A plain deferred BEGIN: a read transaction in WAL mode, which doesn't block the app's writers.
	// (The store's connection setting makes sql.Tx begin IMMEDIATE, which would.)
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &snapshot{q: conn, done: func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK"); _ = conn.Close() }}, nil
}

func sortedCols(t *tableInfo) []*column {
	cols := append([]*column(nil), t.Cols...)
	sort.Slice(cols, func(i, j int) bool { return cols[i].Name < cols[j].Name })
	return cols
}

// tableRows reads every row of t as canonical lines (unsorted).
func tableRows(ctx context.Context, s *snapshot, t *tableInfo) ([]string, []*column, error) {
	cols := sortedCols(t)
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = qid(c.Name)
	}
	rows, err := s.q.QueryContext(ctx, "SELECT "+strings.Join(names, ", ")+" FROM "+qid(t.Name))
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", t.Name, err)
	}
	defer rows.Close()
	var lines []string
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", t.Name, err)
		}
		out := make([]any, len(cols))
		for i, c := range cols {
			if out[i], err = canonValue(c, vals[i]); err != nil {
				return nil, nil, fmt.Errorf("%s.%s: %w", t.Name, c.Name, err)
			}
		}
		b, err := json.Marshal(out)
		if err != nil {
			return nil, nil, err
		}
		lines = append(lines, string(b))
	}
	return lines, cols, rows.Err()
}

// writeDump writes the canonical dump of every table (except skip) to w and returns the row counts.
func writeDump(ctx context.Context, d *database, w io.Writer, skip map[string]bool) (map[string]int, error) {
	cat, err := readCatalog(ctx, d.sql, d.engine)
	if err != nil {
		return nil, err
	}
	snap, err := openSnapshot(ctx, d)
	if err != nil {
		return nil, err
	}
	defer snap.done()
	bw := bufio.NewWriterSize(w, 1<<20)
	fmt.Fprintf(bw, "#DUMP {\"engine\":%q}\n", d.engine)
	names := make([]string, 0, len(cat.Tables))
	for n := range cat.Tables {
		if n != "goose_db_version" && !skip[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	counts := map[string]int{}
	for _, n := range names {
		lines, cols, err := tableRows(ctx, snap, cat.Tables[n])
		if err != nil {
			return nil, err
		}
		sort.Strings(lines)
		cn := make([]string, len(cols))
		for i, c := range cols {
			cn[i] = c.Name
		}
		fmt.Fprintf(bw, "#TABLE %s %d %s\n", n, len(lines), strings.Join(cn, ","))
		for _, l := range lines {
			bw.WriteString(l)
			bw.WriteByte('\n')
		}
		counts[n] = len(lines)
	}
	bw.WriteString("#END\n")
	return counts, bw.Flush()
}

func cmdDump(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("dump", flag.ExitOnError)
	db := fs.String("db", "", "database")
	out := fs.String("out", "", "output file")
	skip := fs.String("skip", "", "tables to leave out")
	_ = fs.Parse(args)
	d := openDB(ctx, *db)
	defer d.close()
	f, err := os.Create(*out)
	if err != nil {
		fatal("%v", err)
	}
	defer f.Close()
	counts, err := writeDump(ctx, d, f, splitList(*skip))
	if err != nil {
		fatal("dump: %v", err)
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	fmt.Printf("dumped %d tables, %d rows from %s\n", len(counts), total, d.label)
}

func cmdFingerprint(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("fingerprint", flag.ExitOnError)
	db := fs.String("db", "", "database")
	skip := fs.String("skip", "", "tables to leave out")
	_ = fs.Parse(args)
	d := openDB(ctx, *db)
	defer d.close()
	h := sha256.New()
	counts, err := writeDump(ctx, d, h, splitList(*skip))
	if err != nil {
		fatal("fingerprint: %v", err)
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	fmt.Printf("%s rows=%d\n", hex.EncodeToString(h.Sum(nil))[:16], total)
}

func cmdCounts(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("counts", flag.ExitOnError)
	db := fs.String("db", "", "database")
	_ = fs.Parse(args)
	d := openDB(ctx, *db)
	defer d.close()
	cat := mustCatalog(ctx, d)
	out := map[string]int{}
	for n := range cat.Tables {
		if n == "goose_db_version" {
			continue
		}
		var c int
		if err := d.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qid(n)).Scan(&c); err != nil {
			fatal("count %s: %v", n, err)
		}
		out[n] = c
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}

func cmdFKCheck(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("fkcheck", flag.ExitOnError)
	db := fs.String("db", "", "database")
	_ = fs.Parse(args)
	d := openDB(ctx, *db)
	defer d.close()
	cat := mustCatalog(ctx, d)
	bad := 0
	if d.engine == store.SQLite {
		rows, err := d.sql.QueryContext(ctx, "PRAGMA foreign_key_check")
		if err != nil {
			fatal("%v", err)
		}
		n := 0
		for rows.Next() {
			n++
		}
		rows.Close()
		fmt.Printf("PRAGMA foreign_key_check: %d violations\n", n)
		bad += n
	} else {
		var total, unvalidated int
		_ = d.sql.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE NOT convalidated) FROM pg_constraint
			WHERE contype='f' AND connamespace = current_schema()::regnamespace`).Scan(&total, &unvalidated)
		fmt.Printf("postgres enforces %d foreign keys (%d not validated)\n", total, unvalidated)
		bad += unvalidated
	}
	// An orphan check of our own, on both engines.
	checked := 0
	for _, n := range cat.Order {
		t := cat.Tables[n]
		for _, fk := range t.FKs {
			q := fmt.Sprintf(`SELECT count(*) FROM %s c WHERE c.%s IS NOT NULL AND NOT EXISTS (SELECT 1 FROM %s p WHERE p.%s = c.%s)`,
				qid(n), qid(fk.Col), qid(fk.RefTable), qid(fk.RefCol), qid(fk.Col))
			var c int
			if err := d.sql.QueryRowContext(ctx, q).Scan(&c); err != nil {
				fatal("orphan check %s.%s: %v", n, fk.Col, err)
			}
			checked++
			if c > 0 {
				fmt.Printf("ORPHANS: %s.%s -> %s.%s: %d rows\n", n, fk.Col, fk.RefTable, fk.RefCol, c)
				bad += c
			}
		}
	}
	fmt.Printf("orphan scan: %d foreign keys checked\n", checked)
	if bad > 0 {
		fmt.Println("FKCHECK FAILED")
		os.Exit(1)
	}
	fmt.Println("FKCHECK OK")
}
