//go:build lab

package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/store"
)

// mutate changes the database the way a busy Reelkeeper might: it inserts, updates and deletes rows
// across many tables, each as its own transaction, so a backup can be taken while it runs.
func cmdMutate(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("mutate", flag.ExitOnError)
	db := fs.String("db", "", "database")
	seconds := fs.Float64("seconds", 0, "keep going this long (0: just --ops operations)")
	ops := fs.Int("ops", 400, "operations to do (when --seconds is 0)")
	seedN := fs.Uint64("seed", uint64(time.Now().UnixNano()), "random seed")
	pause := fs.Duration("pause", 0, "pause between operations")
	_ = fs.Parse(args)
	d := openDB(ctx, *db)
	defer d.close()
	cat := mustCatalog(ctx, d)
	s := &seeder{
		gen: &gen{rng: rand.New(rand.NewPCG(*seedN, 7)), now: time.Now().UTC()}, ctx: ctx, d: d, cat: cat,
		nextID: map[string]int64{}, ids: map[string][]int64{}, keep: map[string][]rec{}, byID: map[string]map[int64]rec{},
		seen: map[string]map[string]bool{}, counts: map[string]int{},
	}
	s.pickHook = func(table string) (int64, bool) {
		pk := idColumn(cat.Tables[table])
		if pk == nil {
			return 0, false
		}
		var id int64
		err := d.sql.QueryRowContext(ctx, "SELECT "+qid(pk.Name)+" FROM "+qid(table)+" ORDER BY random() LIMIT 1").Scan(&id)
		return id, err == nil
	}
	var tables []string
	for _, n := range cat.Order {
		if !specialTables[n] {
			tables = append(tables, n)
		}
	}
	rowid := "rowid"
	if d.engine == store.Postgres {
		rowid = "ctid"
	}
	stats := map[string]int{}
	errs := map[string]int{}
	note := func(kind string, err error) {
		if err == nil {
			stats[kind+" ok"]++
			return
		}
		stats[kind+" failed"]++
		msg := err.Error()
		if len(msg) > 110 {
			msg = msg[:110]
		}
		errs[msg]++
	}
	deadline := time.Now().Add(time.Duration(*seconds * float64(time.Second)))
	for n := 0; ; n++ {
		if *seconds > 0 {
			if time.Now().After(deadline) {
				break
			}
		} else if n >= *ops {
			break
		}
		t := cat.Tables[tables[s.intn(len(tables))]]
		r := s.rng.Float64()
		if t.Name == "jobs" && r >= 0.45 {
			continue // the lab's "no background work" rows live here; only add to jobs
		}
		switch {
		case r < 0.45: // insert
			i := 50_000_000 + n*7 + s.intn(7)
			row := s.makeRowGeneric(t, i)
			cols := make([]string, 0, len(t.Cols))
			for _, c := range t.Cols {
				if c.PK && c.Identity {
					continue
				}
				cols = append(cols, c.Name)
			}
			vals := make([]any, len(cols))
			for j, c := range cols {
				vals[j] = row[c]
			}
			// The identity column is left to the database, as the app does.
			note("insert", s.insertAuto(t, cols, vals))
		case r < 0.80: // update
			var set []string
			var vals []any
			cols := append([]*column(nil), t.Cols...)
			s.rng.Shuffle(len(cols), func(a, b int) { cols[a], cols[b] = cols[b], cols[a] })
			for _, c := range cols {
				if len(set) >= 1+s.intn(3) {
					break
				}
				if c.PK || s.inUnique(t, c.Name) {
					continue
				}
				set = append(set, qid(c.Name)+" = ?")
				vals = append(vals, s.value(t, c, 60_000_000+n))
			}
			if len(set) == 0 {
				continue
			}
			q := fmt.Sprintf("UPDATE %s SET %s WHERE %s IN (SELECT %s FROM %s ORDER BY random() LIMIT %d)",
				qid(t.Name), strings.Join(set, ", "), rowid, rowid, qid(t.Name), 1+s.intn(3))
			_, err := d.st.DB.NewRaw(q, vals...).Exec(ctx)
			note("update", err)
		default: // delete
			if t.Name == "profiles" || t.Name == "root_folders" {
				continue
			}
			q := fmt.Sprintf("DELETE FROM %s WHERE %s IN (SELECT %s FROM %s ORDER BY random() LIMIT %d)",
				qid(t.Name), rowid, rowid, qid(t.Name), 1+s.intn(4))
			_, err := d.st.DB.NewRaw(q).Exec(ctx)
			note("delete", err)
		}
		if *pause > 0 {
			time.Sleep(*pause)
		}
	}
	keys := make([]string, 0, len(stats))
	for k := range stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, stats[k]))
	}
	fmt.Println("mutate:", strings.Join(parts, " "))
	i := 0
	for msg, c := range errs {
		if i++; i > 4 {
			break
		}
		fmt.Printf("  error x%d: %s\n", c, msg)
	}
}

// makeRowGeneric builds a row from column kinds and hints only, with no table fillers.
func (s *seeder) makeRowGeneric(t *tableInfo, i int) rec {
	row := rec{}
	for _, c := range t.Cols {
		if c.PK && c.Identity {
			continue
		}
		row[c.Name] = s.value(t, c, i)
	}
	return row
}

func (s *seeder) insertAuto(t *tableInfo, cols []string, vals []any) error {
	q := "INSERT INTO " + qid(t.Name) + " (" + s.cat.colList(t, cols) + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + ")"
	_, err := s.d.st.DB.NewRaw(q, vals...).Exec(s.ctx)
	return err
}
