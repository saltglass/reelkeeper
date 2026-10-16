package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckPing(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ok.Close()
	if err := checkPing(ok.URL, time.Second); err != nil {
		t.Errorf("healthy server: %v", err)
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer broken.Close()
	if err := checkPing(broken.URL, time.Second); err == nil {
		t.Error("503 should be unhealthy")
	}

	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closed.Close()
	if err := checkPing(closed.URL, time.Second); err == nil {
		t.Error("unreachable server should be unhealthy")
	}
}

func TestRunCommands(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 || strings.TrimSpace(out.String()) != "dev" {
		t.Errorf("version: code %d, out %q", code, out.String())
	}

	out.Reset()
	if code := run([]string{"help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "healthcheck") {
		t.Errorf("help: code %d, out %q", code, out.String())
	}

	errOut.Reset()
	if code := run([]string{"bogus"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "unknown command") {
		t.Errorf("bogus: code %d, err %q", code, errOut.String())
	}
}

func TestHealthcheckRejectsBadConfig(t *testing.T) {
	t.Setenv("REELKEEPER_PORT", "not-a-port")
	var errOut bytes.Buffer
	if code := healthcheck(&errOut); code != 1 || !strings.Contains(errOut.String(), "REELKEEPER_PORT") {
		t.Errorf("code %d, err %q", code, errOut.String())
	}
}

func TestMigrateCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REELKEEPER_DATA_DIR", dir)
	var out, errOut bytes.Buffer

	if code := run([]string{"migrate", "status"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "pending") {
		t.Fatalf("status before: code %d, out %q, err %q", code, out.String(), errOut.String())
	}
	if code := run([]string{"migrate"}, &out, &errOut); code != 0 {
		t.Fatalf("migrate: code %d, err %q", code, errOut.String())
	}
	out.Reset()
	if code := run([]string{"migrate", "status"}, &out, &errOut); code != 0 || strings.Contains(out.String(), "pending") {
		t.Errorf("status after: code %d, out %q", code, out.String())
	}
	if code := run([]string{"migrate", "bogus"}, &out, &errOut); code != 2 {
		t.Errorf("bad args: code %d", code)
	}
}

func TestCheckWritable(t *testing.T) {
	dir := t.TempDir()
	if err := checkWritable(dir); err != nil {
		t.Fatalf("writable folder: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".write-test-*")); len(left) != 0 {
		t.Errorf("left its test file behind: %v", left)
	}

	if os.Getuid() == 0 {
		t.Skip("root can write anywhere, so the unwritable case can't be tried")
	}
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	err := checkWritable(locked)
	if err == nil {
		t.Fatal("an unwritable folder passed")
	}
	for _, want := range []string{"Reelkeeper can't write to its data folder " + locked, "running as user", "docs/install.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}
