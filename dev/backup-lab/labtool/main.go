//go:build lab

// Command labtool is the backup lab's toolbox: it seeds a Reelkeeper
// database with a lot of varied data, dumps it in a canonical form that
// compares equal across SQLite and Postgres, compares dumps, mutates data,
// damages backup files, and checks foreign keys.
//
// It never uses internal/backup: that is what the lab tests.
//
//	labtool seed        --db DB [--scale 1] [--seed N] [--key K] [--kinds a,b]
//	labtool dump        --db DB --out FILE [--skip t1,t2]
//	labtool fingerprint --db DB [--skip t1,t2]
//	labtool compare     A B [--ignore tables] [--ignore-cols t.c,...]
//	labtool mutate      --db DB [--seconds S] [--ops N]
//	labtool fkcheck     --db DB
//	labtool counts      --db DB
//	labtool quiesce     --db DB --kinds a,b,c
//	labtool touchjob    --db DB --kind K --ago 20m
//	labtool sql         --db DB [--exec STMT | --query SELECT]
//	labtool damage      --in FILE --out FILE --kind KIND
//
// DB is a SQLite file path or a postgres:// URL.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/saltglass/reelkeeper/internal/store"
)

func stderr() io.Writer { return os.Stderr }
func exit(code int)     { os.Exit(code) }

// database is an open Reelkeeper database.
type database struct {
	st     *store.Store
	sql    *sql.DB
	engine store.Engine
	label  string
}

func openDB(ctx context.Context, dsn string) *database {
	if dsn == "" {
		fatal("--db is required")
	}
	if store.EngineFor(dsn) == store.SQLite {
		if _, err := os.Stat(dsn); err != nil {
			fatal("%v", err)
		}
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		fatal("open %s: %v", redact(dsn), err)
	}
	return &database{st: st, sql: st.DB.DB, engine: st.Engine, label: redact(dsn)}
}

func (d *database) close() { _ = d.st.Close() }

func redact(dsn string) string {
	if i := strings.Index(dsn, "@"); i > 0 && strings.HasPrefix(dsn, "postgres") {
		return "postgres://***@" + dsn[i+1:]
	}
	return dsn
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, "usage: labtool <seed|dump|fingerprint|compare|mutate|fkcheck|counts|quiesce|touchjob|sql|damage> [flags]\n")
		os.Exit(2)
	}
	ctx := context.Background()
	args := os.Args[2:]
	switch os.Args[1] {
	case "seed":
		cmdSeed(ctx, args)
	case "dump":
		cmdDump(ctx, args)
	case "fingerprint":
		cmdFingerprint(ctx, args)
	case "compare":
		cmdCompare(args)
	case "mutate":
		cmdMutate(ctx, args)
	case "fkcheck":
		cmdFKCheck(ctx, args)
	case "counts":
		cmdCounts(ctx, args)
	case "quiesce":
		cmdQuiesce(ctx, args)
	case "touchjob":
		cmdTouchJob(ctx, args)
	case "sql":
		cmdSQL(ctx, args)
	case "verify-blocklist":
		cmdVerifyBlocklist(ctx, args)
	case "damage":
		cmdDamage(args)
	default:
		fatal("unknown command %q", os.Args[1])
	}
}
