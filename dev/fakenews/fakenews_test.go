package main

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestYEncRoundTrip(t *testing.T) {
	// Every byte value, including the ones that must be escaped, at every
	// position in a line.
	data := make([]byte, 0, 4096)
	for i := range 4096 {
		data = append(data, byte(i))
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 20000 {
		data = append(data, byte(rng.Uint32()))
	}
	enc := yencEncode(data)
	for line := range strings.Lines(enc) {
		line = strings.TrimSuffix(line, "\r\n")
		if len(line) > lineLength+1 {
			t.Fatalf("line of %d bytes", len(line))
		}
		if strings.ContainsAny(line, "\x00\r\n") {
			t.Fatalf("unescaped byte in %q", line)
		}
		if line[0] == '.' || line[0] == ' ' || line[0] == '\t' {
			t.Fatalf("line starts with %q", line[0])
		}
	}
	if got := yencDecode(enc); !bytes.Equal(got, data) {
		t.Fatalf("round trip changed the data (%d bytes in, %d out)", len(data), len(got))
	}
}

// A news server answers BODY with the article (dot-stuffed) and 430 for a
// missing one.
func TestNewsServer(t *testing.T) {
	data := t.TempDir()
	body := "=ybegin part=1 total=1 line=128 size=3 name=x\r\n.starts with a dot\r\n=yend size=3 part=1 pcrc32=0\r\n"
	p := articlePath(data, "abc@test")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &newsServer{data: data, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	client, server := net.Pipe()
	go s.handle(server)
	r := bufio.NewReader(client)
	read := func() string { l, _ := r.ReadString('\n'); return l }
	if l := read(); !strings.HasPrefix(l, "200") {
		t.Fatalf("greeting %q", l)
	}
	io.WriteString(client, "BODY <abc@test>\r\n")
	if l := read(); !strings.HasPrefix(l, "222") {
		t.Fatalf("BODY answered %q", l)
	}
	var got []string
	for {
		l := read()
		if l == ".\r\n" {
			break
		}
		got = append(got, l)
	}
	if got[1] != "..starts with a dot\r\n" {
		t.Fatalf("line not dot-stuffed: %q", got[1])
	}
	io.WriteString(client, "BODY <nope@test>\r\n")
	if l := read(); !strings.HasPrefix(l, "430") {
		t.Fatalf("missing article answered %q", l)
	}
	io.WriteString(client, "QUIT\r\n")
	if l := read(); !strings.HasPrefix(l, "205") {
		t.Fatalf("QUIT answered %q", l)
	}
}

func TestMatches(t *testing.T) {
	for _, c := range []struct {
		name  string
		terms []string
		want  bool
	}{
		{"Harbor.Lights.S01E04.1080p.WEB-DL-GRP", words("harbor lights s01e04"), true},
		{"Harbor.Lights.S01E04.1080p.WEB-DL-GRP", words("harbor lights s01e05"), false},
		{"Made.Up.Film.2024.1080p.WEB-DL-GRP", nil, true},
	} {
		if got := matches(c.name, c.terms); got != c.want {
			t.Errorf("matches(%q, %v) = %v", c.name, c.terms, got)
		}
	}
}
