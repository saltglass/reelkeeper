// Package config reads Reelkeeper's bootstrap settings from environment variables.
//
// Only what Reelkeeper needs before it can open its database lives here. Every
// other setting is stored in the database and edited in the UI.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultPort is the HTTP port used when REELKEEPER_PORT is not set.
const DefaultPort = 7676

// Config holds the bootstrap settings.
type Config struct {
	// DB is either a SQLite file path or a postgres:// URL (REELKEEPER_DB).
	// Defaults to reelkeeper.db inside DataDir.
	DB string
	// Port is the HTTP listen port (REELKEEPER_PORT).
	Port int
	// URLBase is the path prefix when served below a sub-path, such as
	// "/reelkeeper". It is empty when served at the root (REELKEEPER_URL_BASE).
	URLBase string
	// DataDir holds the SQLite database, generated keys and backups
	// (REELKEEPER_DATA_DIR). Defaults to ./data.
	DataDir string
	// SecretKey encrypts stored credentials (REELKEEPER_SECRET_KEY). It may be
	// empty here; the server then generates one in DataDir.
	SecretKey string
}

// FromEnv reads the configuration from the process environment.
func FromEnv() (Config, error) {
	return Parse(os.Getenv)
}

// Parse reads the configuration through getenv, so tests can supply a fake
// environment. It returns every problem it finds, not just the first.
func Parse(getenv func(string) string) (Config, error) {
	var errs []error

	cfg := Config{
		Port:      DefaultPort,
		DataDir:   getenv("REELKEEPER_DATA_DIR"),
		DB:        getenv("REELKEEPER_DB"),
		SecretKey: getenv("REELKEEPER_SECRET_KEY"),
	}

	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	if cfg.DB == "" {
		cfg.DB = filepath.Join(cfg.DataDir, "reelkeeper.db")
	}

	if p := getenv("REELKEEPER_PORT"); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			errs = append(errs, fmt.Errorf("REELKEEPER_PORT must be a number from 1 to 65535, got %q", p))
		} else {
			cfg.Port = port
		}
	}

	base, err := normalizeURLBase(getenv("REELKEEPER_URL_BASE"))
	if err != nil {
		errs = append(errs, err)
	}
	cfg.URLBase = base

	return cfg, errors.Join(errs...)
}

// normalizeURLBase turns "reelkeeper", "/reelkeeper" and "/reelkeeper/" into "/reelkeeper", and
// "" or "/" into "". It rejects values that would break routing.
func normalizeURLBase(raw string) (string, error) {
	base := strings.Trim(strings.TrimSpace(raw), "/")
	if base == "" {
		return "", nil
	}
	if strings.ContainsAny(base, "?#\\ ") || strings.Contains(base, "//") || strings.Contains(base, "..") {
		return "", fmt.Errorf("REELKEEPER_URL_BASE must be a plain path such as /reelkeeper, got %q", raw)
	}
	return "/" + base, nil
}
