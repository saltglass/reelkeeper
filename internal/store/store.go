// Package store owns the database connection and schema migrations.
//
// Reelkeeper supports two engines: SQLite (the zero-config default) and
// PostgreSQL. Each engine has its own folder of goose migrations under
// migrations/, and every schema change must add a file to both with the
// same version number (a test enforces this).
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	// Registers the "pgx" database/sql driver.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/dialect/sqlitedialect"

	// Registers the pure-Go "sqlite" database/sql driver (no CGO).
	_ "modernc.org/sqlite"
)

// Engine names a supported database engine.
type Engine string

// Supported engines. The values double as the migrations folder names.
const (
	SQLite   Engine = "sqlite"
	Postgres Engine = "postgres"
)

// EngineFor reports which engine a connection string refers to: postgres://
// and postgresql:// URLs are PostgreSQL, anything else is a SQLite file path.
func EngineFor(dsn string) Engine {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return Postgres
	}
	return SQLite
}

// Store is an open database. DB is the Bun query layer; feature packages
// build their queries on it.
type Store struct {
	DB     *bun.DB
	Engine Engine
	// migrations is this engine's migration folder. Tests can swap it.
	migrations fs.FS
}

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var migrationFiles embed.FS

// Open connects to the database named by dsn and checks that it answers.
// It does not run migrations; call Migrate for that.
func Open(ctx context.Context, dsn string) (*Store, error) {
	engine := EngineFor(dsn)
	var (
		sqldb *sql.DB
		db    *bun.DB
		err   error
	)

	switch engine {
	case Postgres:
		sqldb, err = sql.Open("pgx", dsn)
		if err != nil {
			return nil, fmt.Errorf("open postgres: %w", err)
		}
		db = bun.NewDB(sqldb, pgdialect.New())

	case SQLite:
		if dir := filepath.Dir(dsn); dir != "" {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, fmt.Errorf("create database folder: %w", err)
			}
		}
		sqldb, err = sql.Open("sqlite", sqliteDSN(dsn))
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}
		db = bun.NewDB(sqldb, sqlitedialect.New())
	}

	if err := sqldb.PingContext(ctx); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("connect to %s database: %w", engine, redactErr(err, dsn))
	}

	return &Store{DB: db, Engine: engine, migrations: migrationsFor(engine)}, nil
}

// sqliteDSN adds the connection settings every SQLite connection needs:
//   - foreign_keys: SQLite ignores foreign keys unless asked.
//   - journal_mode WAL: readers don't block the writer.
//   - busy_timeout: wait for a lock instead of failing at once.
//   - synchronous NORMAL: safe with WAL and much faster than FULL.
//   - _txlock=immediate: transactions take the write lock up front, which
//     avoids "database is locked" errors when two writers race.
//
// The path is escaped because it's read as a URI: a "?" or "#" in a folder
// name would otherwise cut it short and open a different file.
func sqliteDSN(path string) string {
	escaped := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
	return "file:" + escaped +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_txlock=immediate"
}

// Close closes the database.
func (s *Store) Close() error {
	return s.DB.Close()
}

// Ping checks that the database still answers.
func (s *Store) Ping(ctx context.Context) error {
	return s.DB.PingContext(ctx)
}

func migrationsFor(engine Engine) fs.FS {
	sub, err := fs.Sub(migrationFiles, "migrations/"+string(engine))
	if err != nil {
		// The paths are fixed at compile time by go:embed, so this can't
		// happen unless the embed directive above is changed.
		panic(err)
	}
	return sub
}

// redactErr removes the password from a Postgres URL if a driver error
// happens to echo the connection string. Errors without it are returned
// unchanged.
func redactErr(err error, dsn string) error {
	u, perr := url.Parse(dsn)
	if perr != nil || u.User == nil {
		return err
	}
	pw, ok := u.User.Password()
	if !ok || pw == "" || !strings.Contains(err.Error(), pw) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), pw, "REDACTED"))
}
