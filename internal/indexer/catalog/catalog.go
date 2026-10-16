// Package catalog keeps the tracker definitions Reelkeeper can use: the
// community's (Prowlarr's definitions repository, downloaded at run time
// and refreshed daily, never part of Reelkeeper's code) plus any the user adds.
//
// On disk, in the data folder:
//
//	definitions/community/*.yml   the latest download (replaced as a whole)
//	definitions/custom/*.yml      the user's own; they win over community
//	                              definitions with the same id
//
// Only a small index is kept in memory; a full definition is read from
// disk when an indexer needs it.
package catalog

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/saltglass/reelkeeper/internal/indexer/cardigann"
)

// Source is where the community definitions come from: the repository as
// one archive. Schema v11 is the current format (see its VERSIONS file).
const (
	Source        = "https://codeload.github.com/Prowlarr/Indexers/tar.gz/refs/heads/master"
	SchemaVersion = "v11"
)

// Summary describes a definition for the "add an indexer" list.
type Summary struct {
	ID          string
	Name        string
	Description string
	Type        string // public, semi-private, private
	Language    string
	Links       []string
	Custom      bool // the user's own
}

// Catalog is the set of available definitions.
type Catalog struct {
	dir    string
	source string
	http   *http.Client
	logger *slog.Logger

	mu        sync.RWMutex
	index     map[string]entry
	updatedAt time.Time
	cache     map[string]*cardigann.Definition
}

type entry struct {
	Summary
	file string
}

// New opens the catalog in dataDir and reads what's on disk.
func New(dataDir string, logger *slog.Logger) (*Catalog, error) {
	c := &Catalog{
		dir: filepath.Join(dataDir, "definitions"), source: Source, logger: logger,
		http: &http.Client{Timeout: 2 * time.Minute},
	}
	for _, sub := range []string{"community", "custom"} {
		if err := os.MkdirAll(filepath.Join(c.dir, sub), 0o750); err != nil {
			return nil, fmt.Errorf("create %s: %w", filepath.Join(c.dir, sub), err)
		}
	}
	if err := c.reload(); err != nil {
		return nil, err
	}
	return c, nil
}

// SetSource changes where definitions are downloaded from (tests use a
// fake server).
func (c *Catalog) SetSource(url string) { c.source = url }

// reload rebuilds the index from disk.
func (c *Catalog) reload() error {
	index := map[string]entry{}
	for _, sub := range []string{"community", "custom"} {
		files, err := filepath.Glob(filepath.Join(c.dir, sub, "*.yml"))
		if err != nil {
			return err
		}
		for _, f := range files {
			s, err := summarize(f)
			if err != nil {
				c.logger.Warn("skipping a tracker definition that can't be read", "file", f, "error", err)
				continue
			}
			s.Custom = sub == "custom"
			index[s.ID] = entry{Summary: s, file: f} // custom ones come second and win
		}
	}
	var updated time.Time
	if b, err := os.ReadFile(filepath.Join(c.dir, "community", ".updated")); err == nil {
		updated, _ = time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	}
	c.mu.Lock()
	c.index, c.updatedAt, c.cache = index, updated, map[string]*cardigann.Definition{}
	c.mu.Unlock()
	return nil
}

// summarize reads just a definition's description fields.
func summarize(file string) (Summary, error) {
	b, err := os.ReadFile(file) //nolint:gosec // a file Reelkeeper listed in its own definitions folder
	if err != nil {
		return Summary{}, err
	}
	var s struct {
		ID          string   `yaml:"id"`
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Type        string   `yaml:"type"`
		Language    string   `yaml:"language"`
		Links       []string `yaml:"links"`
	}
	if err := yaml.Unmarshal(cardigann.PrepareYAML(b), &s); err != nil {
		return Summary{}, err
	}
	if s.ID == "" || s.Name == "" {
		return Summary{}, errors.New("no id or name")
	}
	return Summary{ID: s.ID, Name: s.Name, Description: s.Description, Type: s.Type, Language: s.Language, Links: s.Links}, nil
}

// List returns every available definition, sorted by name.
func (c *Catalog) List() []Summary {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Summary, 0, len(c.index))
	for _, e := range c.index {
		out = append(out, e.Summary)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Len is how many definitions are available.
func (c *Catalog) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.index)
}

// UpdatedAt is when the community definitions were last downloaded.
func (c *Catalog) UpdatedAt() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.updatedAt
}

// ErrUnknown means no definition has that id.
var ErrUnknown = errors.New("no tracker definition with that id")

// Get returns a full definition.
func (c *Catalog) Get(id string) (*cardigann.Definition, error) {
	c.mu.RLock()
	d, ok := c.cache[id]
	e, known := c.index[id]
	c.mu.RUnlock()
	if ok {
		return d, nil
	}
	if !known {
		return nil, ErrUnknown
	}
	b, err := os.ReadFile(e.file)
	if err != nil {
		return nil, err
	}
	d, err = cardigann.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("the %s definition can't be read: %w", id, err)
	}
	c.mu.Lock()
	c.cache[id] = d
	c.mu.Unlock()
	return d, nil
}

// Update downloads the community definitions and replaces the old set if
// the download is good. On any problem the old set stays.
func (c *Catalog) Update(ctx context.Context) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.source, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("can't download tracker definitions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("can't download tracker definitions: HTTP %d", resp.StatusCode)
	}

	tmp, err := os.MkdirTemp(c.dir, ".download-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp) //nolint:errcheck // best effort; a leftover is harmless

	n, err := extract(resp.Body, tmp)
	if err != nil {
		return 0, err
	}
	if n < 50 {
		// A healthy download has hundreds; fewer means something's off
		// upstream. Keep what we have.
		return 0, fmt.Errorf("the download had only %d definitions; keeping the current set", n)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".updated"), []byte(time.Now().UTC().Format(time.RFC3339)), 0o600); err != nil {
		return 0, err
	}
	community := filepath.Join(c.dir, "community")
	old := community + ".old"
	_ = os.RemoveAll(old)
	if err := os.Rename(community, old); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	if err := os.Rename(tmp, community); err != nil {
		_ = os.Rename(old, community) // put the old set back
		return 0, err
	}
	_ = os.RemoveAll(old)
	if err := c.reload(); err != nil {
		return 0, err
	}
	return n, nil
}

// Limits on the download, so a bad archive can't fill the disk.
const (
	maxFile  = 2 << 20
	maxTotal = 100 << 20
)

// extract writes the archive's definitions/<SchemaVersion>/*.yml files
// into dir, checking that each one parses.
func extract(r io.Reader, dir string) (int, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, fmt.Errorf("the download isn't a gzip archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	n, total := 0, int64(0)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("the archive is damaged: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		// "<repo>-<branch>/definitions/v11/1337x.yml"
		parts := strings.Split(path.Clean(h.Name), "/")
		if len(parts) != 4 || parts[1] != "definitions" || parts[2] != SchemaVersion || !strings.HasSuffix(parts[3], ".yml") {
			continue
		}
		name := parts[3]
		if strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") || h.Size > maxFile {
			continue
		}
		total += h.Size
		if total > maxTotal {
			return 0, errors.New("the archive is far larger than expected")
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxFile))
		if err != nil {
			return 0, err
		}
		if _, err := cardigann.Parse(b); err != nil {
			continue // a definition Reelkeeper can't read is left out
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}
