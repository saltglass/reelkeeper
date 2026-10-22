// Command reelkeeper is the Reelkeeper server and its maintenance subcommands.
//
//	reelkeeper serve        run the web server (the default when no subcommand is given)
//	reelkeeper migrate      apply pending database migrations, then exit
//	reelkeeper migrate status  list migrations and whether each has run
//	reelkeeper reset-auth   turn password sign-in back on and print a new password
//	reelkeeper probe FILE   show what a video file's name claims and what it contains
//	reelkeeper healthcheck  exit 0 if the local server answers /ping, 1 otherwise
//	reelkeeper version      print the version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/saltglass/reelkeeper/internal/api"
	"github.com/saltglass/reelkeeper/internal/auth"
	"github.com/saltglass/reelkeeper/internal/backup"
	"github.com/saltglass/reelkeeper/internal/buildinfo"
	"github.com/saltglass/reelkeeper/internal/compat/radarr"
	"github.com/saltglass/reelkeeper/internal/compat/sonarr"
	"github.com/saltglass/reelkeeper/internal/config"
	"github.com/saltglass/reelkeeper/internal/download"
	"github.com/saltglass/reelkeeper/internal/events"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/indexer/catalog"
	"github.com/saltglass/reelkeeper/internal/jobs"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/mediainfo"
	"github.com/saltglass/reelkeeper/internal/metadata"
	importer "github.com/saltglass/reelkeeper/internal/migrate"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/parser"
	"github.com/saltglass/reelkeeper/internal/secrets"
	"github.com/saltglass/reelkeeper/internal/server"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/store"
	"github.com/saltglass/reelkeeper/internal/ui"
)

const usage = `Usage: reelkeeper <command>

Commands:
  serve           Run the web server (default); applies migrations first
  migrate         Apply pending database migrations, then exit
  migrate status  List migrations and whether each has run
  reset-auth      Locked out? Turns password sign-in back on, gives the first
                  account a new random password (printed once), ends all sessions
  probe FILE...   Show what a video file's name claims and what it contains
  healthcheck     Exit 0 if the local server answers /ping, 1 otherwise
  version         Print the version

Bootstrap settings come from environment variables:
  REELKEEPER_DB, REELKEEPER_PORT, REELKEEPER_URL_BASE, REELKEEPER_DATA_DIR, REELKEEPER_SECRET_KEY

Recovery: starting with REELKEEPER_RESET_AUTH=true turns password sign-in back on
(without changing any password). Remove it again afterwards.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches to a subcommand and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}

	switch cmd {
	case "serve":
		return serve(stderr)
	case "migrate":
		return migrate(args[1:], stdout, stderr)
	case "reset-auth":
		return resetAuth(stdout, stderr)
	case "probe":
		return probe(args[1:], stdout, stderr)
	case "healthcheck":
		return healthcheck(stderr)
	case "version", "--version", "-v":
		_, _ = fmt.Fprintln(stdout, buildinfo.Version)
		return 0
	case "help", "--help", "-h":
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

func loadConfig(stderr io.Writer) (config.Config, bool) {
	cfg, err := config.FromEnv()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "configuration error:\n%v\n", err)
		return cfg, false
	}
	return cfg, true
}

func serve(stderr io.Writer) int {
	cfg, ok := loadConfig(stderr)
	if !ok {
		return 1
	}
	// The level follows Settings → General; info until settings load.
	var level slog.LevelVar
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: &level}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := openApp(ctx, cfg, logger, true)
	if err != nil {
		logger.Error("startup failed", "error", err)
		return 1
	}
	defer func() { _ = app.db.Close() }()

	level.Set(app.settings.General().SlogLevel())
	broker := events.NewBroker()
	app.settings.OnChange(func(group string) {
		if group == settings.GroupGeneral {
			level.Set(app.settings.General().SlogLevel())
		}
		broker.Publish(events.Event{Type: events.SettingsChanged, Data: map[string]string{"group": group}})
	})

	// The built-in download client's engine runs while it is the download
	// client, or the one a move is leaving; it follows the settings.
	syncBuiltin := func() { app.builtin.Apply(app.settings.DownloadClient(), app.settings.DownloadMove()) }
	syncBuiltin()
	// Closed on every way out, after the tasks that use it have stopped, and
	// before the database closes: it saves its records and closes its files.
	defer app.builtin.Close()
	app.settings.OnChange(func(group string) {
		if group == settings.GroupDownloadClient || group == settings.GroupDownloadMove {
			syncBuiltin()
		}
	})

	if v := os.Getenv("REELKEEPER_RESET_AUTH"); v == "1" || strings.EqualFold(v, "true") {
		if err := app.settings.EnableLocalLogin(ctx); err != nil {
			logger.Error("REELKEEPER_RESET_AUTH: can't turn password sign-in back on", "error", err)
			return 1
		}
		logger.Warn("REELKEEPER_RESET_AUTH is set: password sign-in has been turned back on. Remove the variable once you're back in.")
	}

	// Everything Reelkeeper notifies about also pops up in open browsers.
	app.notify.SetLive(func(ev notify.Event) {
		broker.Publish(events.Event{Type: events.Notice, Data: map[string]any{
			"kind": ev.Kind, "title": ev.Title, "message": ev.Message, "problem": notify.IsProblem(ev.Kind),
		}})
	})

	tasks := jobs.New(app.db, logger)
	registerTasks(tasks, app)
	// Backups: a restore asks for a clean restart (stop), and the container
	// starts again and restores before anything else runs.
	backups := backup.New(backup.Config{
		DB: app.db, Settings: app.settings.Backups, DataDir: cfg.DataDir, App: buildinfo.Version, Logger: logger,
		KeyCheck: func(ctx context.Context) (string, error) { return settings.ReadKeyCheck(ctx, app.db) },
		KeyOpens: func(sealed string) bool { return settings.KeyOpens(app.box, sealed) },
		Restart:  stop,
	})
	// Nothing is writing a backup yet, so whatever temporary files a killed
	// run left behind can go.
	backups.RemoveLeftovers()
	backups.RegisterTasks(tasks)
	tasks.OnChange(func(j jobs.Job) {
		broker.Publish(events.Event{Type: events.TaskUpdated, Data: api.ToJob(tasks, j)})
	})
	app.library.OnChange(func() {
		broker.Publish(events.Event{Type: events.LibraryChanged, Data: map[string]any{}})
	})

	apiHandler := api.Router(api.Deps{
		Store:     app.db,
		Settings:  app.settings,
		Auth:      app.auth,
		Events:    broker,
		Jobs:      tasks,
		Library:   app.library,
		Indexers:  app.indexers,
		Grab:      app.grab,
		History:   app.history,
		Downloads: app.downloads,
		Notify:    app.notify,
		Migrate:   importer.New(app.db, app.library, app.history, app.downloads.AddToBlocklist),
		Backups:   backups,
		Logger:    logger,
		URLBase:   cfg.URLBase,
		DataDir:   cfg.DataDir,
		StartedAt: time.Now(),
	})

	// The Radarr- and Sonarr-compatible APIs, for Seerr and the Dashboard.
	search := func(ctx context.Context, versionID int64) error {
		_, _, err := grab.QueueSearch(ctx, tasks, versionID, grab.TriggerRequest)
		return err
	}
	checkDownloads := func(ctx context.Context) error {
		_, _, err := tasks.RunForAPI(ctx, download.CheckKind)
		return err
	}
	radarrHandler := radarr.Router(radarr.Deps{
		Library: app.library, Downloads: app.downloads, URLBase: cfg.URLBase, Logger: logger,
		Search: search, CheckDownloads: checkDownloads,
	})
	sonarrHandler := sonarr.Router(sonarr.Deps{
		Library: app.library, Downloads: app.downloads, URLBase: cfg.URLBase, Logger: logger,
		Search: search, CheckDownloads: checkDownloads,
	})
	compatHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/sonarr/") {
			sonarrHandler.ServeHTTP(w, r)
			return
		}
		radarrHandler.ServeHTTP(w, r)
	})

	// Every request's context derives from baseCtx, which is cancelled when
	// shutdown starts, so long-lived event streams end instead of holding
	// the shutdown up.
	baseCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	srv := &http.Server{
		Addr:        net.JoinHostPort("", strconv.Itoa(cfg.Port)),
		BaseContext: func(net.Listener) context.Context { return baseCtx },
		Handler: server.New(server.Options{
			URLBase:    cfg.URLBase,
			Logger:     logger,
			Identify:   app.auth.Identify,
			API:        apiHandler,
			Compat:     http.StripPrefix(cfg.URLBase+"/compat", compatHandler),
			UI:         ui.Handler(cfg.URLBase),
			ClientAddr: app.auth.ClientAddr,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	// Background tasks stop with the server: they share baseCtx.
	tasksDone := make(chan struct{})
	go func() {
		defer close(tasksDone)
		if err := tasks.Start(baseCtx); err != nil {
			logger.Error("background tasks stopped", "error", err)
		}
	}()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("reelkeeper started", "version", buildinfo.Version, "port", cfg.Port, "url_base", cfg.URLBase)

	select {
	case err := <-errCh:
		logger.Error("server stopped", "error", err)
		return 1
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	cancelRequests()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("shutdown failed", "error", err)
		return 1
	}
	select {
	case <-tasksDone:
	case <-shutdownCtx.Done():
		logger.Warn("background tasks didn't stop in time")
	}
	app.notify.Wait() // let notifications already on their way finish
	return 0
}

// registerTasks adds every background task. Each feature registers its own
// here, so System → Tasks lists them all.
func registerTasks(m *jobs.Manager, app *appServices) {
	app.library.RegisterTasks(m)
	app.indexers.RegisterTasks(m)
	app.grab.RegisterTasks(m)
	app.downloads.SetCleanup(app.settings.Cleanup, func(ctx context.Context, versionID int64) error {
		_, _, err := grab.QueueSearch(ctx, m, versionID, grab.TriggerManual)
		return err
	}, app.library)
	app.downloads.RegisterTasks(m)
	m.Register(jobs.Task{
		Kind:        "auth.prune-sessions",
		Name:        "Remove expired sign-ins",
		Description: "Deletes sign-in sessions that have expired. Expired sessions already can't be used; this keeps the database tidy.",
		Every:       24 * time.Hour,
		Run: func(ctx context.Context, r *jobs.Run) error {
			n, err := app.auth.PruneSessions(ctx)
			if err == nil {
				r.Progress(100, fmt.Sprintf("Removed %d expired sign-ins", n))
			}
			return err
		},
	})
}

// appServices are the long-lived services built at startup.
type appServices struct {
	box       *secrets.Box
	db        *store.Store
	settings  *settings.Service
	auth      *auth.Service
	library   *library.Service
	indexers  *indexer.Service
	history   *grab.History
	grab      *grab.Service
	downloads *download.Service
	notify    *notify.Service
	// builtin runs the built-in download client's engine (nil unless serving).
	builtin *download.BuiltinHost
}

// openApp opens the database, loads the secret key and settings, and
// builds the services shared by serve and reset-auth. When serving, a
// restore requested from System → Backups runs first, before settings load
// or any task starts.
func openApp(ctx context.Context, cfg config.Config, logger *slog.Logger, serving bool) (*appServices, error) {
	db, err := openDatabase(ctx, cfg, logger)
	if err != nil {
		return nil, err
	}
	key, generated, err := secrets.LoadKey(cfg.SecretKey, cfg.DataDir)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if generated {
		logger.Warn("REELKEEPER_SECRET_KEY is not set, so a key was generated in the data folder. "+
			"Back it up: without it, stored credentials can't be read.",
			"file", filepath.Join(cfg.DataDir, secrets.KeyFileName))
	}
	box, err := secrets.New(key)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if serving {
		backup.RunPending(ctx, db, cfg.DataDir, buildinfo.Version,
			func(ctx context.Context) (string, error) { return settings.ReadKeyCheck(ctx, db) },
			func(sealed string) bool { return settings.KeyOpens(box, sealed) }, logger)
	}
	st, err := settings.New(ctx, db, box, logger)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	tmdb := metadata.New(
		func() string { return st.Metadata().TMDBAPIKey },
		func() string { return st.Metadata().Language },
	)
	lib := library.New(db, tmdb, logger)
	lib.SetNaming(st.Naming)
	cat, err := catalog.New(cfg.DataDir, logger)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	idx := indexer.New(db, box, cat, st.Solver, logger)
	// No download client until 0.4.1: grabbing "On" records why nothing
	// was sent.
	hist := grab.NewHistory(db)
	gr := grab.New(lib, idx, st.Grabbing, hist, nil, logger)
	dl := download.NewService(db, st.DownloadClient, idx, lib, hist, logger)
	dl.SetSecrets(box)
	dl.SetMoves(st)
	// Usenet (2.0): the Usenet client sits next to the torrent client, and
	// each protocol's indexers are searched only while a client for it is.
	dl.SetUsenet(st.UsenetClient)
	idx.SetSearchable(dl.Searchable)
	// The built-in client's engine runs only while serving (serve starts it).
	var builtinHost *download.BuiltinHost
	if serving {
		builtinHost = download.NewBuiltinHost(cfg.DataDir, logger)
		dl.SetBuiltin(builtinHost)
	}
	gr.SetDownloads(dl, dl)
	dl.SetWantedTitles(gr.WantedTitleIDs)
	nt := notify.New(st.Notifications, logger)
	nt.SetMediaFolders(func(ctx context.Context) ([]string, error) {
		rfs, err := lib.RootFolders(ctx)
		paths := make([]string, len(rfs))
		for i, rf := range rfs {
			paths[i] = rf.Path
		}
		return paths, err
	})
	dl.SetNotifier(nt.Notify)
	gr.SetNotifier(nt.Notify)
	idx.SetNotifier(nt.Notify)
	return &appServices{box: box, notify: nt, db: db, settings: st, auth: auth.New(db, st, logger), library: lib, indexers: idx,
		history: hist, grab: gr, downloads: dl, builtin: builtinHost}, nil
}

func resetAuth(stdout, stderr io.Writer) int {
	cfg, ok := loadConfig(stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	app, err := openApp(ctx, cfg, logger, false)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "reset-auth failed: %v\n", err)
		return 1
	}
	defer func() { _ = app.db.Close() }()

	username, password, err := app.auth.ResetAuth(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "reset-auth failed: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, `Password sign-in is on and every session has ended.

  Username: %s
  Password: %s

Sign in with these, then change the password under your account menu.
This password is not shown again.
`, username, password)
	return 0
}

// checkWritable makes sure Reelkeeper can write to its data folder, which
// holds the secret key, backups and downloaded definitions, and the database
// itself for SQLite. Left alone, an unwritable folder (typically a fresh
// Docker named volume, owned by root) shows up much later as SQLite's
// "unable to open database file (14)", which says nothing about the cause.
func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return &unwritableError{dir: dir, uid: os.Getuid(), gid: os.Getgid(), err: err}
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	return nil
}

// unwritableError is checkWritable's error. It is a type, not an
// fmt.Errorf, because the message is a sentence for the person running
// Reelkeeper and starts with the app's name.
type unwritableError struct {
	dir      string
	uid, gid int
	err      error
}

func (e *unwritableError) Error() string {
	return fmt.Sprintf("Reelkeeper can't write to its data folder %s (running as user %d:%d). "+
		"Make the folder writable by that user; for Docker, see docs/install.md. (%v)", e.dir, e.uid, e.gid, e.err)
}

func (e *unwritableError) Unwrap() error { return e.err }

// openDatabase connects to the configured database and brings its schema up
// to date.
func openDatabase(ctx context.Context, cfg config.Config, logger *slog.Logger) (*store.Store, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data folder %s: %w", cfg.DataDir, err)
	}
	if err := checkWritable(cfg.DataDir); err != nil {
		return nil, err
	}
	db, err := store.Open(ctx, cfg.DB)
	if err != nil {
		return nil, err
	}
	res, err := db.Migrate(ctx, filepath.Join(cfg.DataDir, "backups"))
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if res.Applied > 0 {
		logger.Info("database upgraded", "engine", db.Engine, "from", res.From, "to", res.To, "backup", res.Backup)
	} else {
		logger.Info("database ready", "engine", db.Engine, "schema_version", res.To)
	}
	return db, nil
}

func migrate(args []string, stdout, stderr io.Writer) int {
	cfg, ok := loadConfig(stderr)
	if !ok {
		return 1
	}
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(stderr, nil))

	switch {
	case len(args) == 0:
		db, err := openDatabase(ctx, cfg, logger)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "migrate failed: %v\n", err)
			return 1
		}
		_ = db.Close()
		return 0

	case len(args) == 1 && args[0] == "status":
		db, err := store.Open(ctx, cfg.DB)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		defer func() { _ = db.Close() }()
		status, err := db.MigrationStatus(ctx)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "Database: %s\n", db.Engine)
		for _, m := range status {
			state := "pending"
			if m.Applied {
				state = "applied " + m.AppliedAt.Local().Format(time.DateTime)
			}
			_, _ = fmt.Fprintf(stdout, "  %-40s %s\n", m.Name, state)
		}
		return 0

	default:
		_, _ = fmt.Fprintf(stderr, "usage: reelkeeper migrate [status]\n")
		return 2
	}
}

// probe prints, for each file, what its name says (parser) and what the
// file contains (mediainfo), as JSON: a troubleshooting aid.
func probe(paths []string, stdout, stderr io.Writer) int {
	if len(paths) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: reelkeeper probe FILE...")
		return 2
	}
	code := 0
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	for _, p := range paths {
		out := struct {
			File  string          `json:"file"`
			Name  parser.Release  `json:"name"`
			Media *mediainfo.Info `json:"media,omitempty"`
			Error string          `json:"error,omitempty"`
		}{File: p, Name: parser.Parse(filepath.Base(p))}
		info, err := mediainfo.Read(p)
		if err != nil {
			out.Error = err.Error()
			code = 1
		}
		out.Media = info
		_ = enc.Encode(out)
	}
	return code
}

// healthcheck exists because the container image has no curl or shell.
func healthcheck(stderr io.Writer) int {
	cfg, ok := loadConfig(stderr)
	if !ok {
		return 1
	}
	url := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)) + "/ping"
	if err := checkPing(url, 5*time.Second); err != nil {
		_, _ = fmt.Fprintf(stderr, "unhealthy: %v\n", err)
		return 1
	}
	return 0
}

func checkPing(url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", url, resp.Status)
	}
	return nil
}
