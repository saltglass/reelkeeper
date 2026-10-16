package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", cfg.Port, DefaultPort)
	}
	if cfg.DataDir != "data" {
		t.Errorf("DataDir = %q, want data", cfg.DataDir)
	}
	if want := filepath.Join("data", "reelkeeper.db"); cfg.DB != want {
		t.Errorf("DB = %q, want %q", cfg.DB, want)
	}
	if cfg.URLBase != "" || cfg.SecretKey != "" {
		t.Errorf("URLBase/SecretKey should be empty, got %q / %q", cfg.URLBase, cfg.SecretKey)
	}
}

func TestParseAllSet(t *testing.T) {
	cfg, err := Parse(env(map[string]string{
		"REELKEEPER_DB":         "postgres://reelkeeper@db/reelkeeper",
		"REELKEEPER_PORT":       "8080",
		"REELKEEPER_URL_BASE":   "/reelkeeper/",
		"REELKEEPER_DATA_DIR":   "/config",
		"REELKEEPER_SECRET_KEY": "s3cret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{DB: "postgres://reelkeeper@db/reelkeeper", Port: 8080, URLBase: "/reelkeeper", DataDir: "/config", SecretKey: "s3cret"}
	if cfg != want {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

func TestDataDirMovesDefaultDB(t *testing.T) {
	cfg, err := Parse(env(map[string]string{"REELKEEPER_DATA_DIR": "/config"}))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/config", "reelkeeper.db"); cfg.DB != want {
		t.Errorf("DB = %q, want %q", cfg.DB, want)
	}
}

func TestURLBase(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"", "", true},
		{"/", "", true},
		{"reelkeeper", "/reelkeeper", true},
		{"/reelkeeper", "/reelkeeper", true},
		{"/reelkeeper/", "/reelkeeper", true},
		{" /media/reelkeeper ", "/media/reelkeeper", true},
		{"/a?b", "", false},
		{"/a#b", "", false},
		{"/a//b", "", false},
		{"/../etc", "", false},
		{"/a b", "", false},
	}
	for _, c := range cases {
		cfg, err := Parse(env(map[string]string{"REELKEEPER_URL_BASE": c.in}))
		if c.ok && err != nil {
			t.Errorf("%q: unexpected error %v", c.in, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%q: expected an error", c.in)
		}
		if c.ok && cfg.URLBase != c.want {
			t.Errorf("%q: URLBase = %q, want %q", c.in, cfg.URLBase, c.want)
		}
	}
}

func TestBadPort(t *testing.T) {
	for _, p := range []string{"abc", "0", "65536", "-1"} {
		if _, err := Parse(env(map[string]string{"REELKEEPER_PORT": p})); err == nil {
			t.Errorf("REELKEEPER_PORT=%q: expected an error", p)
		}
	}
}

func TestReportsAllErrors(t *testing.T) {
	_, err := Parse(env(map[string]string{"REELKEEPER_PORT": "x", "REELKEEPER_URL_BASE": "/a?b"}))
	if err == nil {
		t.Fatal("expected errors")
	}
	msg := err.Error()
	if !strings.Contains(msg, "REELKEEPER_PORT") || !strings.Contains(msg, "REELKEEPER_URL_BASE") {
		t.Errorf("error should mention both variables, got: %s", msg)
	}
}

// Variables from before the rename (AERIE_*) are ignored.
func TestIgnoresOldVariableNames(t *testing.T) {
	env := map[string]string{"AERIE_DB": "postgres://old", "AERIE_PORT": "8000", "AERIE_DATA_DIR": "/old"}
	cfg, err := Parse(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DB != filepath.Join("data", "reelkeeper.db") || cfg.Port != DefaultPort || cfg.DataDir != "data" {
		t.Fatalf("AERIE_* variables were read: %+v", cfg)
	}
}
