package download

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/torrent"
)

const sabKey = "0123456789abcdef0123456789abcdef"

// fakeSAB answers like SABnzbd 5.1, from answers recorded from the dev
// stack's SABnzbd (testdata/sabnzbd). Tests can replace an answer by mode.
type fakeSAB struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	answers  map[string]string // by mode, overriding the recorded ones
	requests []url.Values
	uploads  [][]byte
	cats     []string
}

func newFakeSAB(t *testing.T) *fakeSAB {
	f := &fakeSAB{t: t, answers: map[string]string{}, cats: []string{"*", "movies", "tv"}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func recorded(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "sabnzbd", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *fakeSAB) serve(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("apikey") != sabKey {
		_, _ = io.WriteString(w, "API Key Incorrect\n")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, q)
	mode := q.Get("mode")
	if r.Method == http.MethodPost {
		file, _, err := r.FormFile("name")
		if err != nil {
			f.t.Errorf("upload without a name field: %v", err)
		} else {
			b, _ := io.ReadAll(file)
			f.uploads = append(f.uploads, b)
		}
	}
	if a, ok := f.answers[mode]; ok {
		_, _ = io.WriteString(w, a)
		return
	}
	switch mode {
	case "version":
		_, _ = io.WriteString(w, recorded(f.t, "version.json"))
	case "queue":
		if q.Get("name") != "" { // pause, resume, delete
			_, _ = io.WriteString(w, `{"status":true,"nzo_ids":["`+q.Get("value")+`"]}`)
			return
		}
		_, _ = io.WriteString(w, recorded(f.t, "queue.json"))
	case "history":
		if q.Get("name") == "delete" {
			_, _ = io.WriteString(w, `{"status":true}`)
			return
		}
		_, _ = io.WriteString(w, recorded(f.t, "history.json"))
	case "get_cats":
		b := `{"categories":["` + strings.Join(f.cats, `","`) + `"]}`
		_, _ = io.WriteString(w, b)
	case "set_config":
		f.cats = append(f.cats, q.Get("name"))
		_, _ = io.WriteString(w, `{"config":{"categories":[{"name":"`+q.Get("name")+`"}]}}`)
	case "get_config":
		_, _ = io.WriteString(w, recorded(f.t, "get_config_misc.json"))
	case "addfile":
		_, _ = io.WriteString(w, `{"status":true,"nzo_ids":["new-job-id"]}`)
	case "switch":
		_, _ = io.WriteString(w, `{"result":{"position":0,"priority":0}}`)
	default:
		f.t.Errorf("unexpected mode %q", mode)
	}
}

func (f *fakeSAB) client() *SABnzbd { return &SABnzbd{URL: f.srv.URL + "/", APIKey: sabKey} }

func (f *fakeSAB) asked(mode, name string) []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []url.Values
	for _, q := range f.requests {
		if q.Get("mode") == mode && q.Get("name") == name {
			out = append(out, q)
		}
	}
	return out
}

func TestSABnzbdVersionAndLogin(t *testing.T) {
	f := newFakeSAB(t)
	ctx := context.Background()
	if v, err := f.client().Version(ctx); err != nil || v != "5.1.3" {
		t.Fatalf("version = %q, %v", v, err)
	}
	wrong := &SABnzbd{URL: f.srv.URL, APIKey: "nope"}
	if _, err := wrong.Version(ctx); !errors.Is(err, ErrLogin) || !strings.Contains(err.Error(), "API key") {
		t.Errorf("wrong key: %v", err)
	}
	f.answers["version"] = `{"version":"3.7.2"}`
	if _, err := f.client().Version(ctx); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Errorf("old version: %v", err)
	}
	f.answers["version"] = `<html>some other app</html>`
	if _, err := f.client().Version(ctx); err == nil || !strings.Contains(err.Error(), "didn't make sense") {
		t.Errorf("not SABnzbd: %v", err)
	}
	// The key is in every address; a connection error must not repeat it.
	gone := &SABnzbd{URL: "http://127.0.0.1:1", APIKey: sabKey}
	if _, err := gone.Version(ctx); !errors.Is(err, ErrUnreachable) || strings.Contains(err.Error(), sabKey) {
		t.Errorf("unreachable: %v", err)
	}
}

func TestSABnzbdJobs(t *testing.T) {
	f := newFakeSAB(t)
	jobs, err := f.client().Jobs(context.Background(), "reelkeeper-movies")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Torrent{}
	for _, j := range jobs {
		if j.Category != "reelkeeper-movies" {
			t.Errorf("another category's job listed: %+v", j)
		}
		byName[j.Name] = j
	}
	dl := byName["Repairable.Film.2023.1080p.WEB-DL.x264-FAKE"]
	if dl.State != torrent.Downloading || dl.Done() || dl.Speed <= 0 || dl.Left <= 0 || dl.Size <= dl.Left || dl.ETA <= 0 || dl.ContentPath != "" {
		t.Errorf("downloading = %+v", dl)
	}
	done := byName["Made.Up.Film.2024.1080p.WEB-DL.x264-FAKE"]
	if done.State != torrent.Completed || !done.Done() || done.CompletedOn == nil ||
		done.ContentPath != "/mnt/downloads/usenet/reelkeeper-movies/Made.Up.Film.2024.1080p.WEB-DL.x264-FAKE/Made.Up.Film.2024.1080p.WEB-DL.x264-FAKE.mkv" {
		t.Errorf("completed = %+v", done)
	}
	failed := byName["Broken.Film.2022.1080p.WEB-DL.x264-FAKE"]
	if failed.State != torrent.Failed || failed.Done() || !strings.Contains(failed.Message, "cannot be completed") {
		t.Errorf("failed = %+v", failed)
	}
	if len(byName) != 3 {
		t.Errorf("jobs = %v", byName)
	}
	tv, _ := f.client().Jobs(context.Background(), "reelkeeper-tv")
	for _, j := range tv {
		if j.Name == "Harbor.Lights.S01E05.1080p.WEB-DL.x264-FAKE" && (j.State != torrent.Paused || !j.Paused()) {
			t.Errorf("paused = %+v", j)
		}
	}
}

func TestSABnzbdStates(t *testing.T) {
	for status, want := range map[string]torrent.State{
		"Downloading": torrent.Downloading, "Queued": torrent.Queued, "Grabbing": torrent.Queued, "Propagating": torrent.Queued,
		"Paused": torrent.Paused, "Checking": torrent.Checking, "Something new": torrent.Unknown,
	} {
		if got := sabQueueState(status); got != want {
			t.Errorf("queue %q = %q", status, got)
		}
	}
	for status, want := range map[string]torrent.State{
		"Completed": torrent.Completed, "Failed": torrent.Failed, "Verifying": torrent.Repairing, "Repairing": torrent.Repairing,
		"QuickCheck": torrent.Repairing, "Extracting": torrent.Unpacking, "Moving": torrent.Moving, "Queued": torrent.Checking,
		"Running": torrent.Checking, "Something new": torrent.Unknown,
	} {
		if got := sabHistoryState(status); got != want {
			t.Errorf("history %q = %q", status, got)
		}
	}
	// Downloaded but still being repaired isn't done, and says what it's doing.
	j := historyTorrent(sabHistoryJob{ID: "x", Status: "Repairing", ActionLine: "Repairing: 68%", Storage: "/d/x"})
	if j.Done() || j.State != torrent.Repairing || j.Message != "Repairing: 68%" {
		t.Errorf("repairing = %+v", j)
	}
	// A queue job at 100% still has to be post-processed.
	if q := queueTorrent(sabQueueJob{Status: "Downloading", Percentage: "100"}); q.Done() {
		t.Error("a queue job counted as done")
	}
}

func TestSABnzbdAdd(t *testing.T) {
	f := newFakeSAB(t)
	ctx := context.Background()
	nzb := []byte("<nzb></nzb>")
	id, err := f.client().AddNZB(ctx, nzb, "Some.Film.2024.1080p", "reelkeeper-movies", true)
	if err != nil || id != "new-job-id" {
		t.Fatalf("add = %q, %v", id, err)
	}
	q := f.asked("addfile", "")[0]
	if q.Get("cat") != "reelkeeper-movies" || q.Get("nzbname") != "Some.Film.2024.1080p" || q.Get("priority") != "1" || string(f.uploads[0]) != string(nzb) {
		t.Errorf("request = %v, upload %q", q, f.uploads[0])
	}
	if _, err := f.client().AddNZB(ctx, nzb, "Other", "reelkeeper-movies", false); err != nil || f.asked("addfile", "")[1].Get("priority") != "" {
		t.Errorf("normal priority: %v", err)
	}
	f.answers["addfile"] = `{"status":false,"nzo_ids":[]}`
	if _, err := f.client().AddNZB(ctx, nzb, "Dupe", "reelkeeper-movies", false); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("refused: %v", err)
	}
}

func TestSABnzbdEnsureCategory(t *testing.T) {
	f := newFakeSAB(t)
	ctx := context.Background()
	if err := f.client().EnsureCategory(ctx, "reelkeeper-movies"); err != nil {
		t.Fatal(err)
	}
	set := f.asked("set_config", "reelkeeper-movies")
	if len(set) != 1 || set[0].Get("dir") != "reelkeeper-movies" || set[0].Get("pp") != "3" {
		t.Errorf("created with %v", set)
	}
	if err := f.client().EnsureCategory(ctx, "reelkeeper-movies"); err != nil || len(f.asked("set_config", "reelkeeper-movies")) != 1 {
		t.Errorf("an existing category was created again: %v", err)
	}
	// SABnzbd accepted the call but didn't create it.
	f.answers["set_config"] = `{"config":{}}`
	if err := f.client().EnsureCategory(ctx, "reelkeeper-tv"); err == nil || !strings.Contains(err.Error(), "Config → Categories") {
		t.Errorf("not created: %v", err)
	}
}

func TestSABnzbdProblems(t *testing.T) {
	f := newFakeSAB(t)
	ctx := context.Background()
	if w, r, err := f.client().Problems(ctx); err != nil || len(w) != 0 || len(r) != 0 {
		t.Errorf("dev settings: %v %v %v", w, r, err)
	}
	f.answers["get_config"] = `{"config":{"misc":{"no_dupes":2,"no_series_dupes":0,"no_smart_dupes":0,"history_retention_option":"all-delete","history_retention_number":0}}}`
	w, r, err := f.client().Problems(ctx)
	if err != nil || len(w) != 1 || !strings.Contains(w[0], "duplicate detection") || len(r) != 1 || !strings.Contains(r[0], "History Retention") {
		t.Errorf("problems = %v %v %v", w, r, err)
	}
	f.answers["get_config"] = `{"config":{"misc":{"no_dupes":0,"history_retention_option":"number-delete","history_retention_number":200}}}`
	if _, r, _ := f.client().Problems(ctx); len(r) != 0 {
		t.Errorf("keeping 200 jobs refused: %v", r)
	}
}

func TestSABnzbdPauseResumeTop(t *testing.T) {
	f := newFakeSAB(t)
	ctx := context.Background()
	// The recorded queue has the TV job paused and the movie downloading.
	paused, downloading := "fa0b3ad6", "x"
	for _, j := range must(f.client().Jobs(ctx, "reelkeeper-tv")) {
		if j.State == torrent.Paused {
			paused = j.Hash
		}
	}
	for _, j := range must(f.client().Jobs(ctx, "reelkeeper-movies")) {
		if j.State == torrent.Downloading {
			downloading = j.Hash
		}
	}
	if err := f.client().PauseJobs(ctx, "reelkeeper-tv", []string{paused}); err != nil {
		t.Errorf("pause (shown paused): %v", err)
	}
	// SABnzbd says yes, but the recorded queue still shows it downloading.
	if err := f.client().PauseJobs(ctx, "reelkeeper-movies", []string{downloading}); err == nil {
		t.Error("a pause that didn't happen was accepted")
	}
	if err := f.client().ResumeJobs(ctx, "reelkeeper-movies", []string{"no-such-job"}); err == nil || !strings.Contains(err.Error(), "no longer has") {
		t.Errorf("unknown job: %v", err)
	}
	if err := f.client().TopJobs(ctx, "reelkeeper-movies", []string{downloading}); err != nil {
		t.Error(err)
	}
	f.answers["switch"] = `{"result":{"position":-1,"priority":0}}`
	if err := f.client().TopJobs(ctx, "reelkeeper-movies", []string{downloading}); err == nil || !strings.Contains(err.Error(), "higher priority") {
		t.Errorf("couldn't move: %v", err)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestRemoveCompletedFolder(t *testing.T) {
	root := t.TempDir()
	cat := filepath.Join(root, "usenet", "reelkeeper-movies")
	job := filepath.Join(cat, "Some.Film")
	mustMkdir(t, job)
	video := filepath.Join(job, "Some.Film.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(cat, "Another.Film")
	mustMkdir(t, other)

	// SABnzbd's storage can be the video file: the job's folder goes.
	if err := removeCompletedFolder(video, "reelkeeper-movies"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(job); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the job's folder is still there: %v", err)
	}
	for _, keep := range []string{cat, other} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s was deleted: %v", keep, err)
		}
	}
	// Never the category folder itself, nor anything outside one.
	for _, bad := range []string{cat, filepath.Join(root, "usenet", "elsewhere", "Film"), "relative/reelkeeper-movies/x"} {
		if err := removeCompletedFolder(bad, "reelkeeper-movies"); err == nil {
			t.Errorf("%s: no refusal", bad)
		}
	}
	if _, err := os.Stat(cat); err != nil {
		t.Error("the category folder was deleted")
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
}

func TestSABnzbdRemoveCompleted(t *testing.T) {
	f := newFakeSAB(t)
	root := t.TempDir()
	job := filepath.Join(root, "reelkeeper-movies", "Done.Film")
	mustMkdir(t, job)
	// The recorded history, pointed at a folder in the test's own space, and
	// listing the job only until it's deleted.
	f.answers["history"] = `{"history":{"slots":[{"nzo_id":"done-1","name":"Done.Film","category":"reelkeeper-movies","status":"Completed","storage":"` + job + `","bytes":5,"completed":1}]}}`
	f.answers["queue"] = `{"queue":{"slots":[]}}`
	c := f.client()
	srv := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") == "history" && r.URL.Query().Get("name") == "delete" {
			f.mu.Lock()
			f.answers["history"] = `{"history":{"slots":[]}}`
			f.mu.Unlock()
		}
		srv.ServeHTTP(w, r)
	})
	if err := c.RemoveJobs(context.Background(), "reelkeeper-movies", []string{"done-1"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(job); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the completed folder is still there: %v", err)
	}
	del := f.asked("history", "delete")
	if len(del) != 1 || del[0].Get("value") != "done-1" || del[0].Get("del_files") != "1" {
		t.Errorf("history delete = %v", del)
	}
	// Removing a job SABnzbd still lists afterwards is an error.
	f.answers["history"] = `{"history":{"slots":[{"nzo_id":"stuck","name":"Stuck","category":"reelkeeper-movies","status":"Failed","storage":"/x/reelkeeper-movies/Stuck"}]}}`
	f.srv.Config.Handler = srv
	if err := c.RemoveJobs(context.Background(), "reelkeeper-movies", []string{"stuck"}, true); err == nil || !strings.Contains(err.Error(), "still lists") {
		t.Errorf("still listed: %v", err)
	}
}

func TestParseClock(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"0:03:09": 3*time.Minute + 9*time.Second, "1:02:03:04": 26*time.Hour + 3*time.Minute + 4*time.Second, "": 0, "soon": 0,
	} {
		if got := parseClock(in); got != want {
			t.Errorf("%q = %v", in, got)
		}
	}
}
