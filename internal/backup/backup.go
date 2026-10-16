// Package backup writes and restores Reelkeeper's own database backups.
//
// A backup is every table's rows in a neutral form, so one made on SQLite
// restores into Postgres and back (decided 2026-10-10: no pg_dump, which
// would tie the image to the server's version and still need a second
// method for SQLite). The file is gzip-compressed text, one JSON value per
// line:
//
//	{"header": {...}}                       what made it, the schema version, the key check
//	{"table": "titles", "columns": [...]}   then that table's rows, one array each
//	[1, "movie", 603, ...]
//	...
//	{"end": {"rows": {...}, "sha256": "..."}}
//
// The last line counts the rows and checksums everything before it, so a
// cut-short or damaged file is found before anything is restored.
//
// Stored credentials stay sealed with REELKEEPER_SECRET_KEY inside a backup.
// The header carries the database's sealed key check, so a restore with a
// different key is refused up front instead of leaving every credential
// unreadable.
package backup

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/store"
)

// Format names the file format; FormatVersion changes if it ever does.
const (
	Format        = "reelkeeper-backup"
	FormatVersion = 1
)

// Kinds of backup, shown in the list.
const (
	KindManual    = "manual"
	KindScheduled = "scheduled"
	// KindSafety is made just before a restore replaces the database, so
	// the restore itself can be undone.
	KindSafety = "safety"
)

// Errors a restore (or a check of a file) can give, each with a plain
// message for the Backups page.
var (
	ErrNotBackup = errors.New("this file isn't a Reelkeeper backup")
	ErrDamaged   = errors.New("this backup is damaged or incomplete")
	ErrNewer     = errors.New("this backup was made by a newer Reelkeeper; update Reelkeeper first, then restore it")
	ErrWrongKey  = errors.New("this backup was made with a different REELKEEPER_SECRET_KEY; start Reelkeeper with the key it was made with, then restore it")
	ErrMismatch  = errors.New("this backup's tables don't match this Reelkeeper's database")
)

// Header describes a backup.
type Header struct {
	Format        string    `json:"format"`
	FormatVersion int       `json:"formatVersion"`
	App           string    `json:"app"` // Reelkeeper's version
	Engine        string    `json:"engine"`
	SchemaVersion int64     `json:"schemaVersion"`
	CreatedAt     time.Time `json:"createdAt"`
	Kind          string    `json:"kind"`
	// KeyCheck is the database's key check, sealed with the secret key the
	// backup's credentials are sealed with.
	KeyCheck string `json:"keyCheck"`
}

// Meta is what the caller adds to a new backup's header.
type Meta struct {
	App      string
	Kind     string
	KeyCheck string
}

// Summary is a checked backup: its header and how many rows each table has.
type Summary struct {
	Header Header         `json:"header"`
	Rows   map[string]int `json:"rows"`
	Total  int            `json:"total"`
}

type line struct {
	Header  *Header   `json:"header,omitempty"`
	Table   string    `json:"table,omitempty"`
	Columns []Column  `json:"columns,omitempty"`
	End     *endBlock `json:"end,omitempty"`
}

type endBlock struct {
	Rows   map[string]int `json:"rows"`
	SHA256 string         `json:"sha256"`
}

// Write backs up the whole database to w, from one consistent snapshot.
func Write(ctx context.Context, s *store.Store, w io.Writer, meta Meta) (*Summary, error) {
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return nil, err
	}
	// One snapshot for every table, so the rows agree with each other.
	// SQLite is opened with _txlock=immediate, which makes an ordinary
	// transaction take the write lock for its whole length and would stop
	// every other write while the backup runs. A read-only transaction
	// starts with a plain deferred "begin" instead: in WAL mode that is one
	// consistent snapshot that writers don't have to wait for.
	opts := &sql.TxOptions{ReadOnly: true}
	if s.Engine == store.Postgres {
		opts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	tx, err := s.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	tables, err := readCatalog(ctx, tx, s.Engine)
	if err != nil {
		return nil, err
	}
	if tables, err = ordered(tables); err != nil {
		return nil, err
	}

	gz := gzip.NewWriter(w)
	sum := sha256.New()
	body := io.MultiWriter(gz, sum)
	h := Header{Format: Format, FormatVersion: FormatVersion, App: meta.App, Engine: string(s.Engine),
		SchemaVersion: version, CreatedAt: time.Now().UTC().Truncate(time.Second), Kind: meta.Kind, KeyCheck: meta.KeyCheck}
	out := &Summary{Header: h, Rows: map[string]int{}}
	if err := writeLine(body, line{Header: &h}); err != nil {
		return nil, err
	}
	for _, t := range tables {
		if skipped[t.name] {
			continue
		}
		if err := writeLine(body, line{Table: t.name, Columns: t.columns}); err != nil {
			return nil, err
		}
		n, err := writeRows(ctx, tx, t, body)
		if err != nil {
			return nil, fmt.Errorf("back up %s: %w", t.name, err)
		}
		out.Rows[t.name] = n
		out.Total += n
	}
	if err := writeLine(gz, line{End: &endBlock{Rows: out.Rows, SHA256: hex.EncodeToString(sum.Sum(nil))}}); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func writeRows(ctx context.Context, tx bun.Tx, t *table, w io.Writer) (int, error) {
	names := make([]string, len(t.columns))
	for i, c := range t.columns {
		names[i] = quote(c.Name)
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(names, ", ")+" FROM "+quote(t.name))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	vals := make([]any, len(t.columns))
	ptrs := make([]any, len(t.columns))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		row := make([]any, len(vals))
		for i, v := range vals {
			if row[i], err = encode(v, t.columns[i].Kind); err != nil {
				return n, fmt.Errorf("column %s: %w", t.columns[i].Name, err)
			}
		}
		b, err := json.Marshal(row)
		if err != nil {
			return n, err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

func writeLine(w io.Writer, l line) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// ReadHeader reads just a backup's header (for listing backups).
func ReadHeader(r io.Reader) (*Header, error) {
	rd, err := newReader(r)
	if err != nil {
		return nil, err
	}
	return rd.header()
}

// Inspect reads a whole backup and checks it: the format, every row, the
// row counts and the checksum. Nothing is written anywhere.
func Inspect(r io.Reader) (*Summary, error) {
	return scan(r, nil)
}

// visitor receives a backup's tables and rows as scan reads them.
type visitor struct {
	table func(name string, columns []Column) error
	row   func(values []any) error
}

// scan reads a backup from start to end, handing each table and row to v
// (when not nil), and checks the counts and checksum at the end.
func scan(r io.Reader, v *visitor) (*Summary, error) {
	rd, err := newReader(r)
	if err != nil {
		return nil, err
	}
	h, err := rd.header()
	if err != nil {
		return nil, err
	}
	out := &Summary{Header: *h, Rows: map[string]int{}}
	current := ""
	var columns []Column
	for {
		raw, err := rd.next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: it ends before its last line", ErrDamaged)
		}
		if err != nil {
			return nil, err
		}
		if len(raw) > 0 && raw[0] == '[' {
			if current == "" {
				return nil, fmt.Errorf("%w: rows before any table", ErrDamaged)
			}
			var vals []any
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			dec.UseNumber()
			if err := dec.Decode(&vals); err != nil || len(vals) != len(columns) {
				return nil, fmt.Errorf("%w: a row of %s can't be read", ErrDamaged, current)
			}
			if v != nil {
				if err := v.row(vals); err != nil {
					return nil, err
				}
			}
			out.Rows[current]++
			out.Total++
			rd.hashLast()
			continue
		}
		var l line
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, fmt.Errorf("%w: a line can't be read", ErrDamaged)
		}
		switch {
		case l.End != nil:
			if got := hex.EncodeToString(rd.sum.Sum(nil)); got != l.End.SHA256 {
				return nil, fmt.Errorf("%w: its checksum doesn't match", ErrDamaged)
			}
			if len(l.End.Rows) != len(out.Rows) {
				return nil, fmt.Errorf("%w: it should hold %d tables, not %d", ErrDamaged, len(l.End.Rows), len(out.Rows))
			}
			for name, n := range l.End.Rows {
				if out.Rows[name] != n {
					return nil, fmt.Errorf("%w: %s should have %d rows, not %d", ErrDamaged, name, n, out.Rows[name])
				}
			}
			return out, nil
		case l.Table != "":
			if _, seen := out.Rows[l.Table]; seen {
				return nil, fmt.Errorf("%w: table %s appears twice", ErrDamaged, l.Table)
			}
			current, columns = l.Table, l.Columns
			out.Rows[current] = 0
			if v != nil {
				if err := v.table(current, columns); err != nil {
					return nil, err
				}
			}
			rd.hashLast()
		default:
			return nil, fmt.Errorf("%w: an unexpected line", ErrDamaged)
		}
	}
}

// reader reads a backup line by line, keeping the checksum of every line
// before the last.
type reader struct {
	br   *bufio.Reader
	sum  hash.Hash
	last []byte
}

func newReader(r io.Reader) (*reader, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, ErrNotBackup
	}
	return &reader{br: bufio.NewReaderSize(gz, 1<<16), sum: sha256.New()}, nil
}

// next returns the next line without its newline. The line is added to the
// checksum by hashLast, once it's known not to be the end line.
func (r *reader) next() ([]byte, error) {
	b, err := r.br.ReadBytes('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && len(b) == 0 {
			return nil, io.EOF
		}
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: its last line is cut short", ErrDamaged)
		}
		return nil, fmt.Errorf("%w: %w", ErrDamaged, err)
	}
	r.last = b
	return b[:len(b)-1], nil
}

func (r *reader) hashLast() { r.sum.Write(r.last) }

func (r *reader) header() (*Header, error) {
	raw, err := r.next()
	if err != nil {
		return nil, ErrNotBackup
	}
	var l line
	if err := json.Unmarshal(raw, &l); err != nil || l.Header == nil || l.Header.Format != Format {
		return nil, ErrNotBackup
	}
	if l.Header.FormatVersion > FormatVersion {
		return nil, ErrNewer
	}
	r.hashLast()
	return l.Header, nil
}
