package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func def(id, name string) string {
	return fmt.Sprintf(`---
id: %s
name: %s
description: "A made-up tracker, with a JSON-style \/ escape"
type: public
language: en-US
links: [https://%s.example/]
caps:
  categorymappings:
    - {id: 1, cat: Movies}
search:
  path: browse
  rows: {selector: tr}
  fields:
    title: {selector: a}
    download: {selector: a, attribute: href}
`, id, name, id)
}

// archive builds a repository archive like GitHub's.
func archive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestUpdateAndList(t *testing.T) {
	files := map[string]string{
		"Indexers-master/definitions/v11/broken.yml": "id: [",
		"Indexers-master/definitions/v10/old.yml":    def("old", "Old Schema"),
		"Indexers-master/README.md":                  "readme",
	}
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("tracker%02d", i)
		files["Indexers-master/definitions/v11/"+id+".yml"] = def(id, fmt.Sprintf("Tracker %02d", i))
	}
	body := archive(t, files)
	serve := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !serve {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	c, err := New(dir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	c.SetSource(srv.URL)
	if c.Len() != 0 {
		t.Fatal("a new catalog starts empty")
	}
	n, err := c.Update(context.Background())
	if err != nil || n != 60 {
		t.Fatalf("update = %d, %v", n, err)
	}
	if c.Len() != 60 || c.UpdatedAt().IsZero() {
		t.Errorf("len %d, updated %v", c.Len(), c.UpdatedAt())
	}
	if l := c.List(); l[0].Name != "Tracker 00" || l[0].Type != "public" {
		t.Errorf("first = %+v", l[0])
	}
	d, err := c.Get("tracker07")
	if err != nil || d.Name != "Tracker 07" {
		t.Errorf("get = %+v, %v", d, err)
	}
	if _, err := c.Get("old"); err == nil {
		t.Error("only the current schema is used")
	}

	// A failed download keeps what's there.
	serve = false
	if _, err := c.Update(context.Background()); err == nil || c.Len() != 60 {
		t.Errorf("failed update: %v, len %d", err, c.Len())
	}

	// The user's own definitions win, and survive reopening.
	if err := os.WriteFile(filepath.Join(dir, "definitions", "custom", "mine.yml"), []byte(def("tracker07", "My Tracker 07")), 0o600); err != nil {
		t.Fatal(err)
	}
	c2, err := New(dir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	d, _ = c2.Get("tracker07")
	if c2.Len() != 60 || d.Name != "My Tracker 07" {
		t.Errorf("custom: len %d, %+v", c2.Len(), d)
	}
}
