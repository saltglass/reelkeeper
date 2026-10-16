package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/store"
)

// RestoreOptions control a restore.
type RestoreOptions struct {
	// CheckKey reports whether the backup's sealed key check opens with the
	// secret key Reelkeeper is running with. A backup without one (nothing
	// was ever sealed) passes.
	CheckKey func(sealed string) bool
	// ScratchDir holds the temporary database used to bring a backup from
	// an older Reelkeeper up to date. It's emptied afterwards.
	ScratchDir string
}

// Restore replaces everything in s with the backup at path.
//
// The backup is checked in full first (format, checksum, key, version);
// nothing changes if any check fails. A backup from an older Reelkeeper is
// first loaded into a scratch SQLite database at its own schema version and
// brought up to date by the normal migrations. The replacement itself is
// one transaction: if any part fails, the database is left as it was.
//
// s must already be migrated to the newest schema, and nothing else may be
// using it: Reelkeeper restores at startup, before its tasks run.
func Restore(ctx context.Context, s *store.Store, path string, opts RestoreOptions) (*Summary, error) {
	sum, err := inspectFile(path)
	if err != nil {
		return nil, err
	}
	h := sum.Header
	current, err := s.SchemaVersion(ctx)
	if err != nil {
		return nil, err
	}
	if h.SchemaVersion > current {
		return nil, ErrNewer
	}
	if h.KeyCheck != "" && opts.CheckKey != nil && !opts.CheckKey(h.KeyCheck) {
		return nil, ErrWrongKey
	}
	src := path
	if h.SchemaVersion < current {
		upgraded, cleanup, err := upgrade(ctx, path, h, opts.ScratchDir)
		defer cleanup()
		if err != nil {
			return nil, fmt.Errorf("bring the backup up to date: %w", err)
		}
		src = upgraded
	}
	if err := load(ctx, s, src); err != nil {
		return nil, err
	}
	return sum, nil
}

func inspectFile(path string) (*Summary, error) {
	f, err := os.Open(path) // #nosec G304 G703 -- a backup in the backup folder, or a restore request Reelkeeper wrote
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Inspect(f)
}

// upgrade loads an older backup into a scratch SQLite database at the
// backup's schema version, migrates it to the newest version, and writes
// it out again as a backup at that version. The engines' migrations are
// kept equivalent (a test checks both folders match), so SQLite can stand
// in for either.
func upgrade(ctx context.Context, path string, h Header, dir string) (string, func(), error) {
	if dir == "" {
		dir = os.TempDir()
	}
	tmp, err := os.MkdirTemp(dir, "restore-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	scratch, err := store.Open(ctx, filepath.Join(tmp, "scratch.db"))
	if err != nil {
		return "", cleanup, err
	}
	defer scratch.Close()
	if err := scratch.MigrateTo(ctx, h.SchemaVersion); err != nil {
		return "", cleanup, err
	}
	if err := load(ctx, scratch, path); err != nil {
		return "", cleanup, err
	}
	if _, err := scratch.Migrate(ctx, ""); err != nil {
		return "", cleanup, err
	}
	out := filepath.Join(tmp, "upgraded.backup")
	f, err := os.Create(out) // #nosec G304 -- inside a temporary folder made just above
	if err != nil {
		return "", cleanup, err
	}
	defer f.Close()
	if _, err := Write(ctx, scratch, f, Meta{App: h.App, Kind: h.Kind, KeyCheck: h.KeyCheck}); err != nil {
		return "", cleanup, err
	}
	return out, cleanup, f.Close()
}

// load empties every table of s and fills them from the backup at path, in
// one transaction. The backup must be at s's schema version.
func load(ctx context.Context, s *store.Store, path string) error {
	f, err := os.Open(path) // #nosec G304 G703 -- checked by Restore before it gets here
	if err != nil {
		return err
	}
	defer f.Close()
	return s.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		tables, err := readCatalog(ctx, tx, s.Engine)
		if err != nil {
			return err
		}
		if tables, err = ordered(tables); err != nil {
			return err
		}
		if err := emptyAll(ctx, tx, s.Engine, tables); err != nil {
			return err
		}
		ins := &inserter{ctx: ctx, tx: tx, engine: s.Engine, byName: map[string]*table{}}
		for _, t := range tables {
			if t.name != "goose_db_version" {
				ins.byName[t.name] = t
			}
		}
		if _, err := scan(f, &visitor{table: ins.startTable, row: ins.add}); err != nil {
			return err
		}
		if err := ins.flush(); err != nil {
			return err
		}
		if err := ins.checkComplete(); err != nil {
			return err
		}
		if s.Engine == store.Postgres {
			return resetSequences(ctx, tx, tables)
		}
		return nil
	})
}

// emptyAll deletes every row Reelkeeper keeps (skipped tables included:
// sessions belong to users, who are replaced). goose's version table is
// left alone.
func emptyAll(ctx context.Context, tx bun.Tx, engine store.Engine, tables []*table) error {
	var names []string
	for _, t := range tables {
		if t.name != "goose_db_version" {
			names = append(names, t.name)
		}
	}
	if engine == store.Postgres {
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = quote(n)
		}
		_, err := tx.ExecContext(ctx, "TRUNCATE "+strings.Join(quoted, ", "))
		return err
	}
	// Children before parents, so foreign keys hold at every step.
	for i := len(names) - 1; i >= 0; i-- {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+quote(names[i])); err != nil {
			return fmt.Errorf("empty %s: %w", names[i], err)
		}
	}
	return nil
}

// inserter writes a backup's rows, a batch at a time.
type inserter struct {
	ctx    context.Context
	tx     bun.Tx
	engine store.Engine
	byName map[string]*table
	seen   map[string]bool
	// The table being filled: its target columns (in the backup's order)
	// and the rows waiting to be written.
	cur   *table
	cols  []Column
	batch [][]any
}

const batchRows = 200

func (in *inserter) startTable(name string, columns []Column) error {
	if err := in.flush(); err != nil {
		return err
	}
	t := in.byName[name]
	if t == nil || skipped[name] {
		return fmt.Errorf("%w: it has a table %q this Reelkeeper doesn't", ErrMismatch, name)
	}
	target := map[string]Column{}
	for _, c := range t.columns {
		target[c.Name] = c
	}
	if len(columns) != len(t.columns) {
		return fmt.Errorf("%w: %s has %d columns in the backup and %d here", ErrMismatch, name, len(columns), len(t.columns))
	}
	cols := make([]Column, len(columns))
	for i, c := range columns {
		tc, ok := target[c.Name]
		if !ok {
			return fmt.Errorf("%w: %s.%s isn't in this database", ErrMismatch, name, c.Name)
		}
		cols[i] = tc
	}
	if in.seen == nil {
		in.seen = map[string]bool{}
	}
	in.seen[name] = true
	in.cur, in.cols = t, cols
	return nil
}

func (in *inserter) add(values []any) error {
	row := make([]any, len(values))
	for i, v := range values {
		d, err := decode(v, in.cols[i].Kind)
		if err != nil {
			return fmt.Errorf("%w: %s.%s: %w", ErrDamaged, in.cur.name, in.cols[i].Name, err)
		}
		row[i] = d
	}
	in.batch = append(in.batch, row)
	if len(in.batch) >= batchRows {
		return in.flush()
	}
	return nil
}

// flush writes the waiting rows as one INSERT. Bun fills the placeholders
// in itself, writing each value the way this engine's columns expect.
func (in *inserter) flush() error {
	if len(in.batch) == 0 {
		return nil
	}
	names := make([]string, len(in.cols))
	for i, c := range in.cols {
		names[i] = quote(c.Name)
	}
	one := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(in.cols)), ", ") + ")"
	var sb strings.Builder
	sb.WriteString("INSERT INTO " + quote(in.cur.name) + " (" + strings.Join(names, ", ") + ")")
	if in.engine == store.Postgres && in.cur.alwaysIdentity {
		sb.WriteString(" OVERRIDING SYSTEM VALUE")
	}
	sb.WriteString(" VALUES ")
	args := make([]any, 0, len(in.batch)*len(in.cols))
	for i, row := range in.batch {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(one)
		args = append(args, row...)
	}
	if _, err := in.tx.NewRaw(sb.String(), args...).Exec(in.ctx); err != nil {
		return fmt.Errorf("restore %s: %w", in.cur.name, err)
	}
	in.batch = in.batch[:0]
	return nil
}

// checkComplete makes sure the backup had every table this database has,
// so nothing is silently left empty.
func (in *inserter) checkComplete() error {
	var missing []string
	for name := range in.byName {
		if !skipped[name] && !in.seen[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%w: it has no %s", ErrMismatch, strings.Join(missing, ", "))
	}
	return nil
}

// resetSequences makes Postgres hand out IDs after the restored ones.
func resetSequences(ctx context.Context, tx bun.Tx, tables []*table) error {
	for _, t := range tables {
		if t.identity == "" {
			continue
		}
		q := fmt.Sprintf("SELECT setval(pg_get_serial_sequence(?, ?), COALESCE((SELECT MAX(%s) FROM %s), 0) + 1, false)",
			quote(t.identity), quote(t.name))
		if _, err := tx.NewRaw(q, quote(t.name), t.identity).Exec(ctx); err != nil {
			return fmt.Errorf("reset the ID sequence of %s: %w", t.name, err)
		}
	}
	return nil
}

// IsUserError reports whether err is one the user can act on (a bad,
// newer or mismatched backup, or the wrong key), as opposed to a failure
// of Reelkeeper or its database.
func IsUserError(err error) bool {
	for _, e := range []error{ErrNotBackup, ErrDamaged, ErrNewer, ErrWrongKey, ErrMismatch} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
