// Package storetest gives tests a fresh, migrated database on every engine.
//
// SQLite always runs. PostgreSQL runs too when REELKEEPER_TEST_POSTGRES holds a
// connection URL (`just test-pg` sets it to the dev stack's Postgres). Each
// Postgres test gets its own schema, dropped when the test ends, so tests
// can run in parallel against one database.
package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/saltglass/reelkeeper/internal/store"
)

// PostgresEnv names the variable holding the test Postgres URL.
const PostgresEnv = "REELKEEPER_TEST_POSTGRES"

// Each runs fn as a subtest once per available engine, with a fresh
// migrated store each time.
func Each(t *testing.T, fn func(t *testing.T, s *store.Store)) {
	t.Helper()
	for _, engine := range Engines() {
		t.Run(string(engine), func(t *testing.T) {
			fn(t, Open(t, engine))
		})
	}
}

// Engines lists the engines tests should cover in this run.
func Engines() []store.Engine {
	if os.Getenv(PostgresEnv) != "" {
		return []store.Engine{store.SQLite, store.Postgres}
	}
	return []store.Engine{store.SQLite}
}

// Open returns a fresh, fully migrated store for engine, closed and removed
// when the test ends.
func Open(t *testing.T, engine store.Engine) *store.Store {
	t.Helper()
	s := OpenEmpty(t, engine)
	if _, err := s.Migrate(context.Background(), ""); err != nil {
		t.Fatalf("migrate %s: %v", engine, err)
	}
	return s
}

// OpenEmpty is like Open but runs no migrations.
func OpenEmpty(t *testing.T, engine store.Engine) *store.Store {
	t.Helper()
	dsn := DSN(t, engine)
	s, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open %s: %v", engine, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// DSN returns a connection string for a new, empty database on engine.
func DSN(t *testing.T, engine store.Engine) string {
	t.Helper()
	switch engine {
	case store.SQLite:
		return filepath.Join(t.TempDir(), "reelkeeper.db")
	case store.Postgres:
		return postgresSchema(t)
	default:
		t.Fatalf("unknown engine %q", engine)
		return ""
	}
}

// postgresSchema creates a uniquely named schema and returns a URL whose
// search_path points at it, so everything the test creates lands there.
func postgresSchema(t *testing.T) string {
	t.Helper()
	base := os.Getenv(PostgresEnv)
	if base == "" {
		t.Skipf("%s not set", PostgresEnv)
	}

	var b [6]byte
	_, _ = rand.Read(b[:])
	schema := "test_" + hex.EncodeToString(b[:])

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx := context.Background()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema (is the dev stack running? `just dev-stack`): %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse %s: %v", PostgresEnv, err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
