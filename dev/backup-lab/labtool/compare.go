//go:build lab

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

type tableData struct {
	name string
	n    int
	cols []string
	rows []string
}

type dumpReader struct {
	sc   *bufio.Scanner
	f    *os.File
	head *tableData
	done bool
}

func openDump(path string) *dumpReader {
	f, err := os.Open(path)
	if err != nil {
		fatal("%v", err)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 512<<20)
	r := &dumpReader{sc: sc, f: f}
	if !sc.Scan() || !strings.HasPrefix(sc.Text(), "#DUMP") {
		fatal("%s is not a labtool dump", path)
	}
	return r
}

// next returns the next table, or nil at the end.
func (r *dumpReader) next() *tableData {
	if r.head != nil {
		t := r.head
		r.head = nil
		return t
	}
	for r.sc.Scan() {
		line := r.sc.Text()
		if line == "#END" {
			return nil
		}
		if !strings.HasPrefix(line, "#TABLE ") {
			fatal("unexpected line in a dump: %.80s", line)
		}
		parts := strings.SplitN(line, " ", 4)
		n, _ := strconv.Atoi(parts[2])
		t := &tableData{name: parts[1], n: n}
		if len(parts) > 3 && parts[3] != "" {
			t.cols = strings.Split(parts[3], ",")
		}
		t.rows = make([]string, 0, n)
		for i := 0; i < n; i++ {
			if !r.sc.Scan() {
				fatal("dump ends inside table %s", t.name)
			}
			t.rows = append(t.rows, r.sc.Text())
		}
		return t
	}
	return nil
}

func (r *dumpReader) peek() *tableData {
	if r.head == nil {
		r.head = r.next()
	}
	return r.head
}

// project keeps only the named columns of every row (in the order given), sorted.
func project(t *tableData, keep []string) []string {
	idx := map[string]int{}
	for i, c := range t.cols {
		idx[c] = i
	}
	out := make([]string, len(t.rows))
	for i, row := range t.rows {
		var vals []json.RawMessage
		if err := json.Unmarshal([]byte(row), &vals); err != nil {
			fatal("bad row in %s: %v", t.name, err)
		}
		sel := make([]json.RawMessage, len(keep))
		for j, c := range keep {
			sel[j] = vals[idx[c]]
		}
		b, _ := json.Marshal(sel)
		out[i] = string(b)
	}
	sort.Strings(out)
	return out
}

func short(s string) string {
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}

// diffRows compares two sorted row lists as multisets.
func diffRows(a, b []string) (onlyA, onlyB []string) {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			i++
			j++
		case a[i] < b[j]:
			onlyA = append(onlyA, a[i])
			i++
		default:
			onlyB = append(onlyB, b[j])
			j++
		}
	}
	onlyA = append(onlyA, a[i:]...)
	onlyB = append(onlyB, b[j:]...)
	return
}

func cmdCompare(args []string) {
	var files []string
	ignoreTables := map[string]bool{}
	ignoreCols := map[string]bool{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--ignore":
			i++
			for k := range splitList(args[i]) {
				ignoreTables[k] = true
			}
		case "--ignore-cols":
			i++
			for k := range splitList(args[i]) {
				ignoreCols[k] = true
			}
		default:
			files = append(files, args[i])
		}
	}
	if len(files) != 2 {
		fatal("usage: labtool compare A B [--ignore t1,t2] [--ignore-cols t.c,...]")
	}
	ra, rb := openDump(files[0]), openDump(files[1])
	defer ra.f.Close()
	defer rb.f.Close()
	diffs, tables, rows := 0, 0, 0
	report := func(format string, a ...any) { diffs++; fmt.Printf("DIFF "+format+"\n", a...) }
	for {
		ta, tb := ra.peek(), rb.peek()
		if ta == nil && tb == nil {
			break
		}
		var a, b *tableData
		switch {
		case tb == nil || (ta != nil && ta.name < tb.name):
			a = ra.next()
		case ta == nil || tb.name < ta.name:
			b = rb.next()
		default:
			a, b = ra.next(), rb.next()
		}
		name := ""
		if a != nil {
			name = a.name
		} else {
			name = b.name
		}
		if ignoreTables[name] {
			continue
		}
		if a == nil || b == nil {
			side, t := "first", a
			if a == nil {
				side, t = "second", b
			}
			report("table %s exists only in the %s dump (%d rows)", name, side, t.n)
			continue
		}
		tables++
		// Columns both have, minus the ignored ones.
		inB := map[string]bool{}
		for _, c := range b.cols {
			inB[c] = true
		}
		inA := map[string]bool{}
		for _, c := range a.cols {
			inA[c] = true
		}
		var common []string
		for _, c := range a.cols {
			key := name + "." + c
			switch {
			case ignoreCols[key]:
			case !inB[c]:
				report("%s: column %s only in the first dump", name, c)
			default:
				common = append(common, c)
			}
		}
		for _, c := range b.cols {
			if !inA[c] && !ignoreCols[name+"."+c] {
				report("%s: column %s only in the second dump", name, c)
			}
		}
		var ra2, rb2 []string
		if len(common) == len(a.cols) && len(common) == len(b.cols) {
			ra2, rb2 = a.rows, b.rows
		} else {
			ra2, rb2 = project(a, common), project(b, common)
		}
		rows += len(ra2)
		if len(ra2) != len(rb2) {
			report("%s: %d rows vs %d rows", name, len(ra2), len(rb2))
		}
		onlyA, onlyB := diffRows(ra2, rb2)
		if len(onlyA)+len(onlyB) > 0 {
			report("%s: %d rows only in the first, %d only in the second (columns: %s)", name, len(onlyA), len(onlyB), strings.Join(common, ","))
			for i := 0; i < len(onlyA) && i < 3; i++ {
				fmt.Printf("   first : %s\n", short(onlyA[i]))
			}
			for i := 0; i < len(onlyB) && i < 3; i++ {
				fmt.Printf("   second: %s\n", short(onlyB[i]))
			}
		}
	}
	if diffs > 0 {
		fmt.Printf("DIFFERENT: %d differences (%d tables, %d rows compared)\n", diffs, tables, rows)
		os.Exit(1)
	}
	fmt.Printf("IDENTICAL: %d tables, %d rows compared\n", tables, rows)
}
