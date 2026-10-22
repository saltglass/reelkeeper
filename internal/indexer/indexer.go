// Package indexer manages the indexers Reelkeeper searches: tracker definitions
// (package cardigann), Torznab/Newznab feeds and Knaben. It stores their
// settings (secrets encrypted), builds a ready client for each, keeps each
// one's session, and tracks health: an indexer that fails is left alone
// for a while, longer each time, so a broken tracker is never hammered.
//
// Searching fans out to every suitable indexer at once and gathers what
// they found, with a plain-English error for any that failed.
package indexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/uptrace/bun"

	"github.com/saltglass/reelkeeper/internal/indexer/cardigann"
	"github.com/saltglass/reelkeeper/internal/indexer/catalog"
	"github.com/saltglass/reelkeeper/internal/indexer/core"
	"github.com/saltglass/reelkeeper/internal/indexer/knaben"
	"github.com/saltglass/reelkeeper/internal/indexer/newznab"
	"github.com/saltglass/reelkeeper/internal/indexer/solver"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/validation"
)

// Kinds of indexer.
const (
	KindCardigann = "cardigann"
	KindTorznab   = "torznab"
	KindNewznab   = "newznab"
	KindKnaben    = "knaben"
)

// The protocols a release downloads over. A release's comes from its
// indexer's kind: Newznab feeds are Usenet, everything else torrents.
const (
	ProtocolTorrent = "torrent"
	ProtocolUsenet  = "usenet"
)

// Indexer is one configured indexer, as stored.
type Indexer struct {
	bun.BaseModel `bun:"table:indexers"`

	ID                  int64      `bun:"id,pk,autoincrement"`
	Name                string     `bun:"name"`
	Kind                string     `bun:"kind"`
	Definition          string     `bun:"definition"`
	BaseURL             string     `bun:"base_url"`
	Enabled             bool       `bun:"enabled"`
	Priority            int        `bun:"priority"`
	UseSolver           bool       `bun:"use_solver"`
	Settings            string     `bun:"settings"` // JSON map of non-secret values
	Secrets             string     `bun:"secrets"`  // sealed JSON map of secret values
	Categories          string     `bun:"categories"`
	SeedRatio           *float64   `bun:"seed_ratio"`
	SeedTimeMinutes     *int       `bun:"seed_time_minutes"`
	PackSeedTimeMinutes *int       `bun:"pack_seed_time_minutes"`
	QueryLimit          *int       `bun:"query_limit"`
	GrabLimit           *int       `bun:"grab_limit"`
	Failures            int        `bun:"failures"`
	RetryAfter          *time.Time `bun:"retry_after"`
	LastError           string     `bun:"last_error"`
	LastSuccessAt       *time.Time `bun:"last_success_at"`
	LastFailureAt       *time.Time `bun:"last_failure_at"`
	CreatedAt           time.Time  `bun:"created_at"`
	UpdatedAt           time.Time  `bun:"updated_at"`
}

// Usage is one indexer's activity on one day.
type Usage struct {
	bun.BaseModel `bun:"table:indexer_usage"`

	IndexerID int64  `bun:"indexer_id,pk"`
	Day       string `bun:"day,pk"` // 2026-10-08, UTC
	Queries   int    `bun:"queries"`
	Grabs     int    `bun:"grabs"`
	Failures  int    `bun:"failures"`
	TotalMS   int64  `bun:"total_ms"`
}

// Errors the API layer maps.
var (
	ErrNotFound = errors.New("not found")
)

// Service manages indexers.
type Service struct {
	db       *store.Store
	box      *secrets.Box
	catalog  *catalog.Catalog
	settings func() settings.Solver
	logger   *slog.Logger
	now      func() time.Time
	// newSession builds an indexer's connection (tests replace it).
	newSession func(ix *Indexer, delay time.Duration, sol *solver.Client) *session
	// notify tells someone when an indexer has failed for a day.
	notify func(ctx context.Context, ev notify.Event)

	mu      sync.Mutex
	clients map[int64]*cached
	// draftTests are recent tests of unsaved settings, by draftKey.
	draftTests map[string]draftTest
	// lastFeed is when each solver-backed indexer's feed was last read.
	lastFeed map[int64]time.Time
	// searchable says whether indexers of a protocol are searched now
	// (SetSearchable): not while nothing could download what they find. nil
	// means every protocol is (tests).
	searchable func(protocol string) bool
}

type cached struct {
	updatedAt time.Time
	client    core.Client
}

// New returns the indexer service.
func New(db *store.Store, box *secrets.Box, cat *catalog.Catalog, solverSettings func() settings.Solver, logger *slog.Logger) *Service {
	return &Service{
		db: db, box: box, catalog: cat, settings: solverSettings, logger: logger, now: time.Now,
		newSession: func(ix *Indexer, delay time.Duration, sol *solver.Client) *session {
			ua := browserUserAgent
			if ix.Kind != KindCardigann {
				ua = "Reelkeeper"
			}
			return newSession(delay, ua, sol)
		},
		clients:    map[int64]*cached{},
		draftTests: map[string]draftTest{},
		lastFeed:   map[int64]time.Time{},
	}
}

// Catalog returns the definitions catalogue.
func (s *Service) Catalog() *catalog.Catalog { return s.catalog }

// --- settings and secrets -------------------------------------------------

// Names of settings that hold secrets, besides password fields.
var secretNames = []string{"cookie", "apikey", "passkey", "rsskey", "pid", "pin", "token", "authkey", "api_key", "2fa", "otp"}

// IsSecret says whether a definition setting holds a secret, which is
// stored encrypted and never sent back to the browser.
func IsSecret(s cardigann.Setting) bool {
	return s.Type == "password" || slices.Contains(secretNames, strings.ToLower(s.Name))
}

func decodeMap(s string) map[string]string {
	out := map[string]string{}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func encodeMap(m map[string]string) string {
	if m == nil {
		m = map[string]string{}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// secretsOf opens an indexer's secrets.
func (s *Service) secretsOf(ix *Indexer) (map[string]string, error) {
	if ix.Secrets == "" {
		return map[string]string{}, nil
	}
	plain, err := s.box.Open(ix.Secrets)
	if err != nil {
		return nil, fmt.Errorf("%s's saved passwords can't be read (was REELKEEPER_SECRET_KEY changed?); enter them again", ix.Name)
	}
	return decodeMap(plain), nil
}

// --- reading -----------------------------------------------------------------

// List returns every indexer, by priority then name.
func (s *Service) List(ctx context.Context) ([]Indexer, error) {
	var out []Indexer
	err := s.db.DB.NewSelect().Model(&out).OrderExpr("priority, LOWER(name)").Scan(ctx)
	return out, err
}

// Get returns one indexer.
func (s *Service) Get(ctx context.Context, id int64) (*Indexer, error) {
	ix := new(Indexer)
	err := s.db.DB.NewSelect().Model(ix).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return ix, err
}

// SecretNames lists which secrets an indexer has stored (not their values).
func (s *Service) SecretNames(ix *Indexer) []string {
	sec, err := s.secretsOf(ix)
	if err != nil {
		return nil
	}
	var out []string
	for k, v := range sec {
		if v != "" {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// Health is an indexer's state in plain words.
type Health struct {
	Status string // ok, failing, disabled, unknown
	// Message explains a problem: the last error and when Reelkeeper will try
	// again.
	Message    string
	RetryAfter *time.Time
}

// HealthOf describes an indexer's health.
func (s *Service) HealthOf(ix *Indexer) Health {
	now := s.now()
	switch {
	case !ix.Enabled:
		return Health{Status: "disabled", Message: "Turned off; Reelkeeper doesn't search it."}
	case ix.RetryAfter != nil && ix.RetryAfter.After(now):
		return Health{
			Status: "failing", RetryAfter: ix.RetryAfter,
			Message: fmt.Sprintf("%s Reelkeeper will try again after %s.", sentence(ix.LastError), ix.RetryAfter.Local().Format("15:04 on Jan 2")),
		}
	case ix.Failures > 0:
		return Health{Status: "failing", Message: sentence(ix.LastError)}
	case ix.LastSuccessAt != nil:
		return Health{Status: "ok"}
	}
	return Health{Status: "unknown", Message: "Not used yet. Test it to check it works."}
}

// Stats are an indexer's recent activity.
type Stats struct {
	QueriesToday int
	GrabsToday   int
	Queries30d   int
	Failures30d  int
	AvgMS        int // average response time over 30 days
}

// StatsOf returns every indexer's recent activity, by indexer ID.
func (s *Service) StatsOf(ctx context.Context) (map[int64]Stats, error) {
	today := s.day(s.now())
	since := s.day(s.now().AddDate(0, 0, -30))
	var rows []Usage
	if err := s.db.DB.NewSelect().Model(&rows).Where("day >= ?", since).Scan(ctx); err != nil {
		return nil, err
	}
	out := map[int64]Stats{}
	ms := map[int64]int64{}
	for _, u := range rows {
		st := out[u.IndexerID]
		st.Queries30d += u.Queries
		st.Failures30d += u.Failures
		ms[u.IndexerID] += u.TotalMS
		if u.Day == today {
			st.QueriesToday, st.GrabsToday = u.Queries, u.Grabs
		}
		out[u.IndexerID] = st
	}
	for id, st := range out {
		if st.Queries30d > 0 {
			st.AvgMS = int(ms[id] / int64(st.Queries30d))
			out[id] = st
		}
	}
	return out, nil
}

func (s *Service) day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// --- writing -----------------------------------------------------------------

// Input is what the user sets for an indexer.
type Input struct {
	Name       string
	Kind       string
	Definition string
	BaseURL    string
	Enabled    bool
	Priority   int
	UseSolver  bool
	// Settings holds every setting's value. A secret left out (or empty
	// when one is stored) keeps the stored value, so the browser never
	// needs to see secrets to save other changes.
	Settings            map[string]string
	Categories          []int
	SeedRatio           *float64
	SeedTimeMinutes     *int
	PackSeedTimeMinutes *int
	QueryLimit          *int
	GrabLimit           *int
}

// Default categories: movies and TV.
var defaultCategories = []int{newznab.Movies, newznab.TV}

// validate checks an input and splits its settings into plain values and
// secrets (merged with the stored ones when editing). The settings come back
// even alongside validation errors, so a test of unsaved settings can still
// use them when only the name is wrong (blank, or already taken).
func (s *Service) validate(ctx context.Context, in *Input, existing *Indexer) (plain, secret map[string]string, err error) {
	var errs validation.Errors
	in.Name = strings.TrimSpace(in.Name)
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	switch {
	case in.Name == "":
		errs.Add("name", "Give the indexer a name.")
	case utf8.RuneCountInString(in.Name) > 60:
		errs.Add("name", "Keep the name to 60 characters or fewer.")
	default:
		q := s.db.DB.NewSelect().Model((*Indexer)(nil)).Where("LOWER(name) = LOWER(?)", in.Name)
		if existing != nil {
			q = q.Where("id <> ?", existing.ID)
		}
		if n, err := q.Count(ctx); err != nil {
			return nil, nil, err
		} else if n > 0 {
			errs.Add("name", "There's already an indexer with that name.")
		}
	}
	if in.Priority == 0 {
		in.Priority = 25
	}
	if in.Priority < 1 || in.Priority > 50 {
		errs.Add("priority", "Use 1 (searched first, preferred) to 50.")
	}
	if in.BaseURL != "" {
		if u, err := url.Parse(in.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs.Add("baseUrl", "Enter a full address starting with https://.")
		}
	}
	for _, c := range in.Categories {
		if newznab.Name(c) == "" {
			errs.Add("categories", fmt.Sprintf("%d isn't a known category.", c))
			break
		}
	}
	if in.SeedRatio != nil && (*in.SeedRatio < 0 || *in.SeedRatio > 100) {
		errs.Add("seedRatio", "Use a ratio from 0 to 100, such as 1.0.")
	}
	for field, v := range map[string]*int{"seedTimeMinutes": in.SeedTimeMinutes, "packSeedTimeMinutes": in.PackSeedTimeMinutes} {
		if v != nil && *v < 0 {
			errs.Add(field, "Use 0 or more minutes.")
		}
	}
	for field, v := range map[string]*int{"queryLimit": in.QueryLimit, "grabLimit": in.GrabLimit} {
		if v != nil && *v < 1 {
			errs.Add(field, "Use 1 or more, or leave it empty for no limit.")
		}
	}

	stored := map[string]string{}
	if existing != nil {
		if stored, err = s.secretsOf(existing); err != nil {
			stored = map[string]string{} // unreadable: the user re-enters them
		}
	}
	plain, secret = map[string]string{}, map[string]string{}
	keepSecret := func(name string) {
		if v, ok := in.Settings[name]; ok && v != "" {
			secret[name] = v
		} else if v := stored[name]; v != "" {
			secret[name] = v
		}
	}

	switch in.Kind {
	case KindCardigann:
		def, err := s.catalog.Get(in.Definition)
		if err != nil {
			errs.Add("definition", "Choose a tracker from the list.")
			break
		}
		for _, st := range def.Settings {
			if strings.HasPrefix(st.Type, "info") {
				continue
			}
			if IsSecret(st) {
				keepSecret(st.Name)
				continue
			}
			v, ok := in.Settings[st.Name]
			if !ok {
				v = fmt.Sprint(st.SettingDefault())
				if st.SettingDefault() == nil {
					v = ""
				}
			}
			switch st.Type {
			case "checkbox":
				on := strings.EqualFold(v, "true") || v == cardigann.CheckboxOn
				v = fmt.Sprint(on)
			case "select":
				if _, ok := st.Options.Get(v); !ok && len(st.Options) > 0 {
					errs.Add("settings."+st.Name, fmt.Sprintf("Choose one of the options for %s.", st.Label))
				}
			}
			plain[st.Name] = v
		}
	case KindTorznab, KindNewznab:
		if in.BaseURL == "" {
			errs.Add("baseUrl", "Enter the feed's address, as its site or Prowlarr/Jackett shows it.")
		}
		keepSecret("apiKey")
	case KindKnaben:
	default:
		errs.Add("kind", "Choose what kind of indexer this is.")
	}
	if errs != nil {
		return plain, secret, errs
	}
	return plain, secret, nil
}

// Create adds an indexer.
func (s *Service) Create(ctx context.Context, in Input) (*Indexer, error) {
	plain, secret, err := s.validate(ctx, &in, nil)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	ix := &Indexer{CreatedAt: now}
	if err := s.apply(ix, in, plain, secret, now); err != nil {
		return nil, err
	}
	s.applyDraftTest(ix, draftKey(0, in, plain, secret))
	if _, err := s.db.DB.NewInsert().Model(ix).Exec(ctx); err != nil {
		return nil, err
	}
	s.logger.Info("indexer added", "name", ix.Name, "kind", ix.Kind, "definition", ix.Definition)
	return ix, nil
}

// Update changes an indexer's settings. Health is kept, except that a
// changed setup gets a fresh chance (no waiting out an old backoff).
func (s *Service) Update(ctx context.Context, id int64, in Input) (*Indexer, error) {
	ix, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	plain, secret, err := s.validate(ctx, &in, ix)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	if err := s.apply(ix, in, plain, secret, now); err != nil {
		return nil, err
	}
	ix.RetryAfter = nil
	s.applyDraftTest(ix, draftKey(id, in, plain, secret))
	if _, err := s.db.DB.NewUpdate().Model(ix).WherePK().Exec(ctx); err != nil {
		return nil, err
	}
	s.forget(id)
	return ix, nil
}

func (s *Service) apply(ix *Indexer, in Input, plain, secret map[string]string, now time.Time) error {
	ix.Name, ix.Kind, ix.Definition, ix.BaseURL = in.Name, in.Kind, in.Definition, in.BaseURL
	if in.Kind != KindCardigann {
		ix.Definition = ""
	}
	ix.Enabled, ix.Priority, ix.UseSolver = in.Enabled, in.Priority, in.UseSolver
	ix.Settings = encodeMap(plain)
	ix.Secrets = ""
	if len(secret) > 0 {
		sealed, err := s.box.Seal(encodeMap(secret))
		if err != nil {
			return err
		}
		ix.Secrets = sealed
	}
	cats := in.Categories
	if len(cats) == 0 {
		cats = defaultCategories
	}
	b, _ := json.Marshal(cats)
	ix.Categories = string(b)
	ix.SeedRatio, ix.SeedTimeMinutes, ix.PackSeedTimeMinutes = in.SeedRatio, in.SeedTimeMinutes, in.PackSeedTimeMinutes
	ix.QueryLimit, ix.GrabLimit = in.QueryLimit, in.GrabLimit
	ix.UpdatedAt = now
	return nil
}

// Delete removes an indexer.
func (s *Service) Delete(ctx context.Context, id int64) error {
	ix, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.db.DB.NewDelete().Model(ix).WherePK().Exec(ctx); err != nil {
		return err
	}
	s.forget(id)
	s.logger.Info("indexer removed", "name", ix.Name)
	return nil
}

// CategoryList decodes an indexer's categories.
func (ix *Indexer) CategoryList() []int {
	var out []int
	_ = json.Unmarshal([]byte(ix.Categories), &out)
	if len(out) == 0 {
		return defaultCategories
	}
	return out
}

// Protocol is what the indexer's releases download over: ProtocolUsenet for
// a Newznab feed, ProtocolTorrent for everything else.
func (ix *Indexer) Protocol() string {
	if ix.Kind == KindNewznab {
		return ProtocolUsenet
	}
	return ProtocolTorrent
}

// SettingsMap decodes an indexer's non-secret settings.
func (ix *Indexer) SettingsMap() map[string]string { return decodeMap(ix.Settings) }

// --- clients -----------------------------------------------------------------

// ErrNoSolver means an indexer wants the solver but none is set up.
var ErrNoSolver = errors.New("this indexer uses the Cloudflare solver, but no solver is set up; add its address under Settings → Indexers")

// client returns a ready client for an indexer, reusing its session (and
// login) while its settings are unchanged.
func (s *Service) client(ix *Indexer) (core.Client, error) {
	s.mu.Lock()
	if c, ok := s.clients[ix.ID]; ok && c.updatedAt.Equal(ix.UpdatedAt) && ix.ID != 0 {
		s.mu.Unlock()
		return c.client, nil
	}
	s.mu.Unlock()
	c, err := s.build(ix, nil)
	if err != nil {
		return nil, err
	}
	if ix.ID != 0 {
		s.mu.Lock()
		s.clients[ix.ID] = &cached{updatedAt: ix.UpdatedAt, client: c}
		s.mu.Unlock()
	}
	return c, nil
}

// build makes a client. secret overrides the stored secrets (for testing
// unsaved settings).
func (s *Service) build(ix *Indexer, secret map[string]string) (core.Client, error) {
	if secret == nil {
		var err error
		if secret, err = s.secretsOf(ix); err != nil {
			return nil, err
		}
	}
	var sol *solver.Client
	if ix.UseSolver {
		cfg := s.settings()
		if cfg.URL == "" {
			return nil, ErrNoSolver
		}
		sol = &solver.Client{URL: cfg.URL, Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second}
	}
	delay := 2 * time.Second
	switch ix.Kind {
	case KindCardigann:
		def, err := s.catalog.Get(ix.Definition)
		if err != nil {
			return nil, fmt.Errorf("the %q tracker definition isn't available (it may have been removed upstream)", ix.Definition)
		}
		if def.RequestDelay > 0 {
			delay = time.Duration(def.RequestDelay * float64(time.Second))
		}
		vals := ix.SettingsMap()
		for k, v := range secret {
			vals[k] = v
		}
		return &cardigann.Engine{Def: def, Settings: vals, BaseURL: ix.BaseURL, HTTP: s.newSession(ix, delay, sol)}, nil
	case KindTorznab, KindNewznab:
		return &newznab.Client{URL: ix.BaseURL, APIKey: secret["apiKey"], HTTP: s.newSession(ix, delay, sol)}, nil
	case KindKnaben:
		return &knaben.Client{API: ix.BaseURL, HTTP: s.newSession(ix, delay, sol)}, nil
	}
	return nil, fmt.Errorf("unknown kind of indexer %q", ix.Kind)
}

func (s *Service) forget(id int64) {
	s.mu.Lock()
	delete(s.clients, id)
	s.mu.Unlock()
}

// --- health and usage --------------------------------------------------------

// backoff is how long to leave an indexer alone after its nth failure in
// a row.
func backoff(n int) time.Duration {
	steps := []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour}
	if n < 1 {
		n = 1
	}
	if n > len(steps) {
		n = len(steps)
	}
	return steps[n-1]
}

// What a request to an indexer was, for its daily counts.
type usageKind int

const (
	usageQuery usageKind = iota // a search (or a test, which searches)
	usageGrab                   // downloading a release's .torrent or magnet
)

// SetNotifier connects notifications (an indexer failing for a day).
func (s *Service) SetNotifier(fn func(ctx context.Context, ev notify.Event)) { s.notify = fn }

// SetSearchable connects the check for whether a protocol's indexers are
// searched (see Service.searchable).
func (s *Service) SetSearchable(fn func(protocol string) bool) { s.searchable = fn }

// Searchable says whether indexers of a protocol (ProtocolTorrent or
// ProtocolUsenet) are searched now.
func (s *Service) Searchable(protocol string) bool {
	return s.searchable == nil || s.searchable(protocol)
}

// record notes the outcome of a request to an indexer.
func (s *Service) record(ctx context.Context, ix *Indexer, took time.Duration, failure error, kind usageKind) {
	now := s.now().UTC()
	u := &Usage{IndexerID: ix.ID, Day: s.day(now), TotalMS: took.Milliseconds()}
	switch kind {
	case usageQuery:
		u.Queries = 1
	case usageGrab:
		u.Grabs = 1
	}
	if failure != nil {
		u.Failures = 1
	}
	if _, err := s.db.DB.NewInsert().Model(u).
		On("CONFLICT (indexer_id, day) DO UPDATE").
		Set("queries = ?TableAlias.queries + EXCLUDED.queries").
		Set("grabs = ?TableAlias.grabs + EXCLUDED.grabs").
		Set("failures = ?TableAlias.failures + EXCLUDED.failures").
		Set("total_ms = ?TableAlias.total_ms + EXCLUDED.total_ms").
		Exec(ctx); err != nil {
		s.logger.Warn("couldn't record indexer usage", "indexer", ix.Name, "error", err)
	}

	q := s.db.DB.NewUpdate().Model((*Indexer)(nil)).Where("id = ?", ix.ID)
	if failure == nil {
		ix.Failures, ix.RetryAfter, ix.LastError, ix.LastSuccessAt = 0, nil, "", &now
		q = q.Set("failures = 0").Set("retry_after = NULL").Set("last_error = ''").Set("last_success_at = ?", now)
	} else {
		ix.Failures++
		retry := now.Add(backoff(ix.Failures))
		ix.RetryAfter, ix.LastError, ix.LastFailureAt = &retry, failure.Error(), &now
		q = q.Set("failures = failures + 1").Set("retry_after = ?", retry).Set("last_error = ?", failure.Error()).Set("last_failure_at = ?", now)
		s.logger.Warn("indexer failed", "indexer", ix.Name, "error", failure, "failures_in_a_row", ix.Failures, "retry_after", retry)
		// A day without a single success is worth telling someone about.
		if s.notify != nil && ix.LastSuccessAt != nil && now.Sub(*ix.LastSuccessAt) >= 24*time.Hour {
			s.notify(ctx, notify.Event{Kind: notify.KindIndexerFailing, Title: ix.Name,
				Message: fmt.Sprintf("%s hasn't worked since %s. Last error: %s", ix.Name,
					ix.LastSuccessAt.Local().Format("Mon 2 Jan 15:04"), failure)})
		}
	}
	if _, err := q.Exec(ctx); err != nil {
		s.logger.Warn("couldn't record indexer health", "indexer", ix.Name, "error", err)
	}
}

// overLimit says whether an indexer has used its daily searches.
func (s *Service) overLimit(ctx context.Context, ix *Indexer) bool {
	if ix.QueryLimit == nil {
		return false
	}
	u := new(Usage)
	err := s.db.DB.NewSelect().Model(u).Where("indexer_id = ? AND day = ?", ix.ID, s.day(s.now())).Scan(ctx)
	return err == nil && u.Queries >= *ix.QueryLimit
}

// --- testing and searching ---------------------------------------------------

// Test checks an indexer works, and records the outcome in its health.
func (s *Service) Test(ctx context.Context, id int64) error {
	ix, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	s.forget(id) // a fresh session and login
	c, err := s.client(ix)
	if err != nil {
		return err
	}
	start := time.Now()
	err = c.Test(ctx)
	s.record(ctx, ix, time.Since(start), err, usageQuery)
	return err
}

// TestInput checks unsaved settings work, without saving anything. id is
// the indexer being edited (0 for a new one), whose stored secrets fill in
// any left empty.
func (s *Service) TestInput(ctx context.Context, id int64, in Input) error {
	var existing *Indexer
	if id != 0 {
		var err error
		if existing, err = s.Get(ctx, id); err != nil {
			return err
		}
	}
	plain, secret, err := s.validate(ctx, &in, existing)
	if err != nil {
		var verrs validation.Errors
		// The name doesn't matter for a test (empty, or already used).
		if errors.As(err, &verrs) && len(verrs) == 1 && verrs["name"] != "" {
			err = nil
		}
		if err != nil {
			return err
		}
	}
	ix := &Indexer{}
	if err := s.apply(ix, in, plain, map[string]string{}, s.now()); err != nil {
		return err
	}
	c, err := s.build(ix, secret)
	if err != nil {
		return err
	}
	err = c.Test(ctx)
	if ctx.Err() == nil {
		// Not when the person gave up waiting: that isn't an outcome.
		s.rememberDraftTest(draftKey(id, in, plain, secret), err)
	}
	return err
}

// Release is a release found on an indexer.
type Release struct {
	core.Release
	IndexerID int64
	Indexer   string
	Priority  int
	// Protocol is ProtocolTorrent or ProtocolUsenet (the indexer's). Empty
	// means a torrent.
	Protocol string
}

// Usenet reports whether the release downloads over Usenet.
func (r Release) Usenet() bool { return r.Protocol == ProtocolUsenet }

// IndexerError is one indexer's failure during a search.
type IndexerError struct {
	IndexerID int64
	Indexer   string
	Message   string
	// Skipped means it wasn't asked: it's resting after a failure.
	Skipped bool
}

// SearchOptions narrow a search.
type SearchOptions struct {
	// IndexerIDs limits the search to these indexers, and searches them
	// even while they're backing off. Empty means every suitable one.
	IndexerIDs []int64
	// Feed marks a read of the newest releases (RSS sync) rather than a
	// search. Indexers behind the Cloudflare solver are read at most once
	// every SolverFeedEvery, since each page costs the solver a browser
	// visit and some sites make one search into several pages.
	Feed bool
}

// SolverFeedEvery is how often RSS sync may read a solver-backed indexer.
// Every 15 minutes kept a single-browser TRAWL solving ~380 pages a day
// for one site, until it ran out of memory and hung (2026-10-10).
const SolverFeedEvery = time.Hour

// Search asks every suitable indexer at once and gathers what they found.
// An indexer is suitable when it's enabled, not backing off, under its
// daily limit, and searches at least one of the query's categories.
func (s *Service) Search(ctx context.Context, q core.Query, opt SearchOptions) ([]Release, []IndexerError, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	var picked []Indexer
	var skipped []IndexerError
	for _, ix := range all {
		targeted := slices.Contains(opt.IndexerIDs, ix.ID)
		if len(opt.IndexerIDs) > 0 && !targeted {
			continue
		}
		if !ix.Enabled && !targeted {
			continue
		}
		if !targeted && ix.RetryAfter != nil && ix.RetryAfter.After(now) {
			// Reported, so a search that found nothing says why.
			skipped = append(skipped, IndexerError{IndexerID: ix.ID, Indexer: ix.Name, Skipped: true,
				Message: "resting after an error until " + ix.RetryAfter.Local().Format("15:04")})
			continue
		}
		if len(q.Categories) > 0 && !overlaps(ix.CategoryList(), q.Categories) {
			continue
		}
		if !s.Searchable(ix.Protocol()) {
			continue
		}
		if s.overLimit(ctx, &ix) {
			continue
		}
		if opt.Feed && ix.UseSolver && !targeted && !s.feedDue(ix.ID, now) {
			continue
		}
		picked = append(picked, ix)
	}

	var (
		mu       sync.Mutex
		releases []Release
		failures []IndexerError
		wg       sync.WaitGroup
	)
	for i := range picked {
		ix := &picked[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			iq := q
			if len(iq.Categories) == 0 {
				iq.Categories = ix.CategoryList()
			} else {
				iq.Categories = intersect(iq.Categories, ix.CategoryList())
			}
			c, err := s.client(ix)
			var found []core.Release
			start := time.Now()
			if err == nil {
				sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
				found, err = c.Search(sctx, iq)
				cancel()
			}
			s.record(ctx, ix, time.Since(start), err, usageQuery)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, IndexerError{IndexerID: ix.ID, Indexer: ix.Name, Message: Sentence(err)})
				return
			}
			for _, r := range found {
				releases = append(releases, Release{Release: r, IndexerID: ix.ID, Indexer: ix.Name, Priority: ix.Priority, Protocol: ix.Protocol()})
			}
		}()
	}
	wg.Wait()
	return releases, append(failures, skipped...), nil
}

// feedDue says whether a solver-backed indexer's feed may be read now, and
// if so counts this as its read (failed reads count too, so a struggling
// solver isn't asked more often).
func (s *Service) feedDue(id int64, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.lastFeed[id]; ok && now.Sub(last) < SolverFeedEvery {
		return false
	}
	s.lastFeed[id] = now
	return true
}

// overlaps: does an indexer's category list cover any wanted category?
func overlaps(have, want []int) bool {
	for _, w := range want {
		for _, h := range have {
			if newznab.Within(w, []int{h}) || newznab.Within(h, []int{w}) {
				return true
			}
		}
	}
	return false
}

// intersect narrows wanted categories to what the indexer searches.
func intersect(want, have []int) []int {
	var out []int
	for _, w := range want {
		for _, h := range have {
			switch {
			case newznab.Within(w, []int{h}):
				out = append(out, w)
			case newznab.Within(h, []int{w}):
				out = append(out, h)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Sentence turns an error into a sentence for people: capitalised, with a
// full stop. Go errors are lower case so they can be wrapped.
func Sentence(err error) string { return sentence(err.Error()) }

func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(r)) + s[size:]
	if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "!") && !strings.HasSuffix(s, "?") {
		s += "."
	}
	return s
}
