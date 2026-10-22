package download

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saltglass/reelkeeper/internal/torrent"
)

const nzbgetUser, nzbgetPass = "reelkeeper", "hunter2-not-real"

func nzbgetRecorded(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "nzbget", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fakeNZBGet answers like NZBGet 26.3, from answers recorded from the dev
// stack's NZBGet (testdata/nzbget). Tests can replace an answer by method.
type fakeNZBGet struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	answers map[string]string // by method, overriding the recorded ones
	calls   []nzbgetCall
}

type nzbgetCall struct {
	Method string
	Params []json.RawMessage
}

func newFakeNZBGet(t *testing.T) *fakeNZBGet {
	nzbgetSettle = time.Millisecond
	f := &fakeNZBGet{t: t, answers: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNZBGet) serve(w http.ResponseWriter, r *http.Request) {
	if u, p, ok := r.BasicAuth(); !ok || u != nzbgetUser || p != nzbgetPass {
		w.Header().Set("WWW-Authenticate", `Basic realm="NZBGet"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Path != "/jsonrpc" {
		http.NotFound(w, r)
		return
	}
	b, _ := io.ReadAll(r.Body)
	var call nzbgetCall
	if err := json.Unmarshal(b, &call); err != nil {
		f.t.Errorf("not JSON-RPC: %s", b)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	if a, ok := f.answers[call.Method]; ok {
		_, _ = io.WriteString(w, a)
		return
	}
	file := map[string]string{
		"version": "version.json", "status": "status.json", "config": "config.json", "listgroups": "listgroups_queued.json",
		"history": "history.json", "append": "append.json", "editqueue": "editqueue_true.json",
	}[call.Method]
	if file == "" {
		f.t.Errorf("unexpected method %q", call.Method)
		return
	}
	_, _ = io.WriteString(w, nzbgetRecorded(f.t, file))
}

func (f *fakeNZBGet) client() *NZBGet {
	return &NZBGet{URL: f.srv.URL + "/", Username: nzbgetUser, Password: nzbgetPass}
}

// answer replaces an answer with the recorded file's contents.
func (f *fakeNZBGet) answer(method, file string) {
	f.mu.Lock()
	f.answers[method] = nzbgetRecorded(f.t, file)
	f.mu.Unlock()
}

func (f *fakeNZBGet) asked(method string) []nzbgetCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []nzbgetCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// editqueues are the editqueue commands sent, as "Command id,id".
func (f *fakeNZBGet) editqueues() []string {
	var out []string
	for _, c := range f.asked("editqueue") {
		var cmd string
		var ids []int
		_ = json.Unmarshal(c.Params[0], &cmd)
		_ = json.Unmarshal(c.Params[2], &ids)
		s := cmd
		for i, id := range ids {
			if i == 0 {
				s += " "
			} else {
				s += ","
			}
			s += strconv.Itoa(id)
		}
		out = append(out, s)
	}
	return out
}

func TestNZBGetVersionAndLogin(t *testing.T) {
	f := newFakeNZBGet(t)
	ctx := context.Background()
	if v, err := f.client().Version(ctx); err != nil || v != "26.3" {
		t.Fatalf("version = %q, %v", v, err)
	}
	wrong := &NZBGet{URL: f.srv.URL, Username: nzbgetUser, Password: "nope"}
	if _, err := wrong.Version(ctx); !errors.Is(err, ErrLogin) || !strings.Contains(err.Error(), "username or password") {
		t.Errorf("wrong password: %v", err)
	}
	// Older versions: 21 still works (the same calls), 20 is refused; a
	// test build's version has more after the number.
	for v, ok := range map[string]bool{"21.1": true, "24.0": true, "21.0-testing-r2470": true, "20.0": false, "13.0": false} {
		f.answers["version"] = `{"version":"1.1","id":1,"result":"` + v + `"}`
		_, err := f.client().Version(ctx)
		if ok != (err == nil) || (!ok && !strings.Contains(err.Error(), "too old")) {
			t.Errorf("version %s: %v", v, err)
		}
	}
	f.answers["version"] = `<html>some other app</html>`
	if _, err := f.client().Version(ctx); err == nil || !strings.Contains(err.Error(), "didn't make sense") {
		t.Errorf("not NZBGet: %v", err)
	}
	// An address that isn't NZBGet's at all.
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	if _, err := (&NZBGet{URL: other.URL, Username: "a", Password: "b"}).Version(ctx); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("not found: %v", err)
	}
	// The password goes in a header, never an address, so nothing in an error
	// can repeat it.
	gone := &NZBGet{URL: "http://127.0.0.1:1", Username: nzbgetUser, Password: nzbgetPass}
	if _, err := gone.Version(ctx); !errors.Is(err, ErrUnreachable) || strings.Contains(err.Error(), nzbgetPass) {
		t.Errorf("unreachable: %v", err)
	}
}

func TestNZBGetJobsQueue(t *testing.T) {
	f := newFakeNZBGet(t)
	ctx := context.Background()
	// The recorded queue: a queued movie job, a paused one, another
	// category's, and a paused one named "x".
	jobs, err := f.client().Jobs(ctx, "reelkeeper-movies")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Torrent{}
	for _, j := range jobs {
		if j.Category != "reelkeeper-movies" {
			t.Errorf("another category's job listed: %+v", j)
		}
		byID[j.Hash] = j
	}
	queued, paused := byID["19"], byID["20"]
	if queued.Name != "Nzbcheck.Clean.2024.1080p.WEB-DL" || queued.State != torrent.Queued || queued.Done() || queued.ContentPath != "" || queued.Left <= 0 || queued.Size <= queued.Left {
		t.Errorf("queued = %+v", queued)
	}
	if paused.State != torrent.Paused || !paused.Paused() {
		t.Errorf("paused = %+v", paused)
	}
	if other, _ := f.client().Jobs(ctx, "other"); len(other) != 1 || other[0].Hash != "21" {
		t.Errorf("other category = %+v", other)
	}

	// Downloading: the speed is NZBGet's one for everything, and the repair
	// files it hasn't fetched aren't counted as left to download.
	f.answer("listgroups", "listgroups_downloading.json")
	f.answer("status", "status_downloading.json")
	jobs, _ = f.client().Jobs(ctx, "reelkeeper-movies")
	var big Torrent
	for _, j := range jobs {
		if j.Hash == "23" {
			big = j
		}
	}
	if big.State != torrent.Downloading || big.Speed != 115290010 || big.ETA <= 0 || big.Progress <= 0 || big.Progress >= 0.5 || big.Done() {
		t.Errorf("downloading = %+v", big)
	}
	if want := int64(573-62) << 20; big.Left < want-(2<<20) || big.Left > want+(2<<20) {
		t.Errorf("left = %d, want about %d", big.Left, want)
	}
	for _, j := range jobs {
		if j.Hash == "24" && (j.Speed != 0 || j.State != torrent.Queued) {
			t.Errorf("the waiting job: %+v", j)
		}
	}
}

// A job is in NZBGet's queue until it has been repaired and unpacked, and
// says what it is doing.
func TestNZBGetPostProcessingStaysInTheQueue(t *testing.T) {
	f := newFakeNZBGet(t)
	ctx := context.Background()
	for file, want := range map[string]struct {
		id    string
		state torrent.State
		msg   string
	}{
		"listgroups_unpacking.json":          {"23", torrent.Unpacking, ""},
		"listgroups_moving.json":             {"23", torrent.Moving, ""},
		"listgroups_verifying_repaired.json": {"24", torrent.Checking, "Verifying repaired file Nzbcheck.Repair.2024.1080p.WEB-DL.mkv"},
	} {
		f.answer("listgroups", file)
		jobs, err := f.client().Jobs(ctx, "reelkeeper-movies")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, j := range jobs {
			if j.Hash != want.id {
				continue
			}
			found = true
			if j.State != want.state || j.Done() || j.ContentPath != "" || j.Left != 0 || j.Progress != 0.99 || j.Message != want.msg {
				t.Errorf("%s: %+v", file, j)
			}
		}
		if !found {
			t.Errorf("%s: job %s not listed", file, want.id)
		}
	}
}

func TestNZBGetJobsHistory(t *testing.T) {
	f := newFakeNZBGet(t)
	f.mu.Lock()
	f.answers["listgroups"] = `{"version":"1.1","id":1,"result":[]}`
	f.mu.Unlock()
	jobs, err := f.client().Jobs(context.Background(), "reelkeeper-movies")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Torrent{}
	for _, j := range jobs {
		byID[j.Hash] = j
	}
	if len(byID) != 5 {
		t.Errorf("jobs = %v", byID)
	}
	done := byID["25"]
	if done.State != torrent.Completed || !done.Done() || done.CompletedOn == nil || done.Message != "" ||
		done.ContentPath != "/mnt/downloads/nzbget/reelkeeper-movies/Nzbcheck.Clean.2024.1080p.WEB-DL" ||
		done.SavePath != "/mnt/downloads/nzbget/reelkeeper-movies" || done.Verified != done.Size || done.Size <= 0 {
		t.Errorf("completed = %+v", done)
	}
	for id, want := range map[string]string{
		"26": "too many articles are missing to repair it (its health is 80.0%, it needs 81.3%)",
		"27": "par2 couldn't repair it (5 of its 50 articles were missing)",
		"28": "duplicate check refused it",
		"30": "couldn't read the .nzb",
	} {
		j := byID[id]
		if j.State != torrent.Failed || j.Done() || !strings.Contains(j.Message, want) || j.ContentPath == "" {
			t.Errorf("job %s = %+v", id, j)
		}
	}
	if _, ok := byID["29"]; ok {
		t.Error("another category's job listed")
	}
}

func TestNZBGetStates(t *testing.T) {
	for status, want := range map[string]torrent.State{
		"DOWNLOADING": torrent.Downloading, "QUEUED": torrent.Queued, "FETCHING": torrent.Queued, "PAUSED": torrent.Paused,
		"PP_QUEUED": torrent.Checking, "LOADING_PARS": torrent.Checking, "VERIFYING_SOURCES": torrent.Checking,
		"VERIFYING_REPAIRED": torrent.Checking, "RENAMING": torrent.Checking, "POST_UNPACK_RENAMING": torrent.Checking,
		"EXECUTING_SCRIPT": torrent.Checking, "PP_FINISHED": torrent.Checking,
		"REPAIRING": torrent.Repairing, "UNPACKING": torrent.Unpacking, "MOVING": torrent.Moving, "SOMETHING_NEW": torrent.Unknown,
	} {
		if got := nzbgetQueueState(status); got != want {
			t.Errorf("queue %q = %q, want %q", status, got, want)
		}
	}
	for status, want := range map[string]torrent.State{
		"SUCCESS/PAR": torrent.Completed, "SUCCESS/UNPACK": torrent.Completed, "SUCCESS/ALL": torrent.Completed,
		"SUCCESS/GOOD": torrent.Completed, "SUCCESS/MARK": torrent.Completed, "WARNING/SCRIPT": torrent.Completed,
		"FAILURE/HEALTH": torrent.Failed, "FAILURE/PAR": torrent.Failed, "FAILURE/UNPACK": torrent.Failed, "FAILURE/SCAN": torrent.Failed,
		"FAILURE/MOVE": torrent.Failed, "FAILURE/SCRIPT": torrent.Failed, "FAILURE/BAD": torrent.Failed,
		"WARNING/PASSWORD": torrent.Failed, "WARNING/DAMAGED": torrent.Failed, "WARNING/REPAIRABLE": torrent.Failed,
		"WARNING/SPACE": torrent.Failed, "WARNING/HEALTH": torrent.Failed,
		"DELETED/COPY": torrent.Failed, "DELETED/DUPE": torrent.Failed, "DELETED/HEALTH": torrent.Failed, "FAILURE/NEWTHING": torrent.Failed,
	} {
		got, ok := nzbgetHistoryTorrent(nzbgetHistoryItem{ID: 1, Status: status, Category: "c", DestDir: "/d/c/x"})
		if !ok || got.State != want {
			t.Errorf("history %q = %q (%v), want %q", status, got.State, ok, want)
		}
		if got.State == torrent.Failed && (got.Message == "" || got.Done()) {
			t.Errorf("history %q: failure without a reason: %+v", status, got)
		}
	}
	// A job someone deleted on NZBGet's own page is gone as far as Reelkeeper
	// is concerned.
	if _, ok := nzbgetHistoryTorrent(nzbgetHistoryItem{Status: "DELETED/MANUAL"}); ok {
		t.Error("a job deleted by hand was listed")
	}
	// A script moved the files: its folder wins.
	if got, _ := nzbgetHistoryTorrent(nzbgetHistoryItem{Status: "SUCCESS/ALL", DestDir: "/a/c/x", FinalDir: "/b/c/x"}); got.ContentPath != "/b/c/x" {
		t.Errorf("final dir = %q", got.ContentPath)
	}
}

func TestNZBGetAdd(t *testing.T) {
	f := newFakeNZBGet(t)
	ctx := context.Background()
	nzb := []byte("<nzb></nzb>")
	id, err := f.client().AddNZB(ctx, nzb, "Some/Film.2024.1080p", "reelkeeper-movies", true)
	if err != nil || id != "22" {
		t.Fatalf("add = %q, %v", id, err)
	}
	p := f.asked("append")[0].Params
	var name, content, cat, dupe string
	var prio int
	var top, paused bool
	for i, dst := range []any{&name, &content, &cat, &prio, &top, &paused, new(string), new(int), &dupe} {
		if err := json.Unmarshal(p[i], dst); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := base64.StdEncoding.DecodeString(content)
	if name != "Some_Film.2024.1080p.nzb" || string(got) != string(nzb) || cat != "reelkeeper-movies" || prio != 50 || !top || paused || dupe != "FORCE" {
		t.Errorf("append = %s %q %s %d %v %v %s", name, got, cat, prio, top, paused, dupe)
	}
	if _, err := f.client().AddNZB(ctx, nzb, "Other", "reelkeeper-tv", false); err != nil {
		t.Fatal(err)
	}
	p = f.asked("append")[1].Params
	_ = json.Unmarshal(p[3], &prio)
	_ = json.Unmarshal(p[4], &top)
	if prio != 0 || top {
		t.Errorf("a normal job was added with priority %d, top %v", prio, top)
	}
	// NZBGet gives no ID when it can't take the job.
	f.mu.Lock()
	f.answers["append"] = `{"version":"1.1","id":1,"result":-1}`
	f.mu.Unlock()
	if _, err := f.client().AddNZB(ctx, nzb, "Bad", "reelkeeper-movies", false); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("refused: %v", err)
	}
}

// NZBGet needs no category entry: Reelkeeper creates nothing, and only
// checks that a category the user did make puts jobs where cleanup looks.
func TestNZBGetEnsureCategory(t *testing.T) {
	f := newFakeNZBGet(t)
	ctx := context.Background()
	if err := f.client().EnsureCategory(ctx, "reelkeeper-movies"); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.asked("saveconfig") {
		t.Errorf("rewrote NZBGet's config: %v", c)
	}
	f.mu.Lock()
	f.answers["config"] = `{"version":"1.1","id":1,"result":[
		{"Name":"Category1.Name","Value":"Movies"},{"Name":"Category1.DestDir","Value":""},
		{"Name":"Category2.Name","Value":"Reelkeeper-Movies"},{"Name":"Category2.DestDir","Value":"/data/films"},
		{"Name":"Category3.Name","Value":"reelkeeper-tv"},{"Name":"Category3.DestDir","Value":"/data/usenet/reelkeeper-tv/"}]}`
	f.mu.Unlock()
	if err := f.client().EnsureCategory(ctx, "reelkeeper-movies"); err == nil || !strings.Contains(err.Error(), "/data/films") || !strings.Contains(err.Error(), "DestDir") {
		t.Errorf("own folder: %v", err)
	}
	// A folder named after the category is fine.
	if err := f.client().EnsureCategory(ctx, "reelkeeper-tv"); err != nil {
		t.Errorf("folder named like the category: %v", err)
	}
	// A restricted login can't read the settings: nothing to check.
	f.answers["config"] = `{"version":"1.1","id":1,"error":{"name":"JSONRPCError","code":1,"message":"Access denied"}}`
	if err := f.client().EnsureCategory(ctx, "reelkeeper-movies"); err != nil {
		t.Errorf("restricted login: %v", err)
	}
}

func TestNZBGetProblems(t *testing.T) {
	f := newFakeNZBGet(t)
	ctx := context.Background()
	if w, r, err := f.client().Problems(ctx); err != nil || len(w) != 0 || len(r) != 0 {
		t.Errorf("dev settings: %v %v %v", w, r, err)
	}
	set := func(opts string) {
		f.mu.Lock()
		f.answers["config"] = `{"version":"1.1","id":1,"result":[` + opts + `]}`
		f.mu.Unlock()
	}
	set(`{"Name":"KeepHistory","Value":"0"},{"Name":"AppendCategoryDir","Value":"no"},{"Name":"HealthCheck","Value":"pause"},` +
		`{"Name":"Category1.Name","Value":"reelkeeper-tv"},{"Name":"Category1.DestDir","Value":"/elsewhere"}`)
	w, r, err := f.client().Problems(ctx)
	if err != nil || len(w) != 1 || !strings.Contains(w[0], "HealthCheck") || len(r) != 3 {
		t.Fatalf("problems = %v %v %v", w, r, err)
	}
	all := strings.Join(r, "\n")
	for _, want := range []string{"KeepHistory", "AppendCategoryDir", "/elsewhere"} {
		if !strings.Contains(all, want) {
			t.Errorf("refusals don't mention %s: %v", want, r)
		}
	}
	// Keeping 30 days (the default) and the folders on is fine; duplicate
	// checking being on is no problem, since Reelkeeper adds jobs with it off.
	set(`{"Name":"KeepHistory","Value":"30"},{"Name":"AppendCategoryDir","Value":"yes"},{"Name":"DupeCheck","Value":"yes"},{"Name":"HealthCheck","Value":"park"}`)
	if w, r, _ := f.client().Problems(ctx); len(w) != 0 || len(r) != 0 {
		t.Errorf("defaults: %v %v", w, r)
	}
	// Paused, or out of quota.
	f.answer("status", "status_paused.json")
	if w, _, _ := f.client().Problems(ctx); len(w) != 1 || !strings.Contains(w[0], "paused") {
		t.Errorf("paused: %v", w)
	}
	// A login that can't read the settings gets a warning, not a refusal.
	f.answers["config"] = `{"version":"1.1","id":1,"error":{"name":"JSONRPCError","code":1,"message":"Access denied"}}`
	if w, r, err := f.client().Problems(ctx); err != nil || len(r) != 0 || len(w) != 2 || !strings.Contains(w[1], "control username") {
		t.Errorf("restricted: %v %v %v", w, r, err)
	}
}

func TestNZBGetFreeSpace(t *testing.T) {
	f := newFakeNZBGet(t)
	ctx := context.Background()
	if got, err := f.client().FreeSpace(ctx); err != nil || got != 3836817408 {
		t.Errorf("free = %d, %v", got, err)
	}
	// The intermediate folder's disk counts too when it is smaller.
	f.answers["status"] = `{"version":"1.1","id":1,"result":{"FreeDiskSpaceLo":0,"FreeDiskSpaceHi":3,"FreeInterDiskSpaceLo":1073741824,"FreeInterDiskSpaceHi":0}}`
	if got, _ := f.client().FreeSpace(ctx); got != 1<<30 {
		t.Errorf("smaller intermediate disk: %d", got)
	}
	// Sizes are in two halves: 3 << 32 is 12 GiB.
	f.answers["status"] = `{"version":"1.1","id":1,"result":{"FreeDiskSpaceLo":0,"FreeDiskSpaceHi":3}}`
	if got, _ := f.client().FreeSpace(ctx); got != 3<<32 {
		t.Errorf("big disk: %d", got)
	}
	// All ones is NZBGet's "I don't know" (a container with no disk info).
	f.answers["status"] = `{"version":"1.1","id":1,"result":{"FreeDiskSpaceLo":4294967295,"FreeDiskSpaceHi":4294967295}}`
	if got, err := f.client().FreeSpace(ctx); err != nil || got != -1 {
		t.Errorf("unknown: %d, %v", got, err)
	}
}

func TestNZBGetPauseResumeTop(t *testing.T) {
	f := newFakeNZBGet(t)
	ctx := context.Background()
	// The recorded queue has job 20 paused and job 19 waiting.
	if err := f.client().PauseJobs(ctx, "reelkeeper-movies", []string{"20"}); err != nil {
		t.Errorf("pause (shown paused): %v", err)
	}
	// NZBGet says yes, but the recorded queue still shows 19 unpaused.
	if err := f.client().PauseJobs(ctx, "reelkeeper-movies", []string{"19"}); err == nil || !strings.Contains(err.Error(), "didn't pause") {
		t.Errorf("a pause that didn't happen was accepted: %v", err)
	}
	if err := f.client().ResumeJobs(ctx, "reelkeeper-movies", []string{"19"}); err != nil {
		t.Errorf("resume: %v", err)
	}
	if err := f.client().ResumeJobs(ctx, "reelkeeper-movies", []string{"20"}); err == nil || !strings.Contains(err.Error(), "didn't resume") {
		t.Errorf("a resume that didn't happen: %v", err)
	}
	// An ID NZBGet doesn't have: it answers false, and Reelkeeper says so.
	f.answer("editqueue", "editqueue_unknown_id.json")
	if err := f.client().ResumeJobs(ctx, "reelkeeper-movies", []string{"999"}); err == nil || !strings.Contains(err.Error(), "no longer has") {
		t.Errorf("unknown job: %v", err)
	}
	// A job that is being unpacked has finished downloading.
	f.answer("listgroups", "listgroups_unpacking.json")
	if err := f.client().PauseJobs(ctx, "reelkeeper-movies", []string{"23"}); err == nil || !strings.Contains(err.Error(), "finished downloading") {
		t.Errorf("pausing a job being unpacked: %v", err)
	}
	want := []string{"GroupPause 20", "GroupPause 19", "GroupResume 19", "GroupResume 20", "GroupResume 999", "GroupPause 23"}
	if got := f.editqueues(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("editqueue = %v", got)
	}
}

func TestNZBGetTop(t *testing.T) {
	f := newFakeNZBGet(t)
	ctx := context.Background()
	// The recorded queue lists 19 first: already on top.
	if err := f.client().TopJobs(ctx, "reelkeeper-movies", []string{"19"}); err != nil {
		t.Error(err)
	}
	// 20 is behind 19, which downloads first at the same priority: NZBGet
	// said yes but didn't move it (here: the recorded queue is unchanged).
	if err := f.client().TopJobs(ctx, "reelkeeper-movies", []string{"20"}); err == nil || !strings.Contains(err.Error(), "couldn't move") {
		t.Errorf("not moved: %v", err)
	}
	// Behind a job that's past downloading doesn't count.
	f.answer("listgroups", "listgroups_downloading.json")
	f.mu.Lock()
	f.answers["listgroups"] = strings.Replace(nzbgetRecorded(t, "listgroups_downloading.json"), `"Status" : "DOWNLOADING"`, `"Status" : "UNPACKING"`, 1)
	f.mu.Unlock()
	if err := f.client().TopJobs(ctx, "reelkeeper-movies", []string{"24"}); err != nil {
		t.Errorf("behind a job being unpacked: %v", err)
	}
	if got := f.editqueues(); got[0] != "GroupMoveTop 19" {
		t.Errorf("editqueue = %v", got)
	}
}

func TestNZBGetRemoveCompleted(t *testing.T) {
	f := newFakeNZBGet(t)
	root := t.TempDir()
	job := filepath.Join(root, "reelkeeper-movies", "Done.Film")
	mustMkdir(t, job)
	history := func(items string) {
		f.mu.Lock()
		f.answers["history"] = `{"version":"1.1","id":1,"result":[` + items + `]}`
		f.mu.Unlock()
	}
	f.mu.Lock()
	f.answers["listgroups"] = `{"version":"1.1","id":1,"result":[]}`
	f.mu.Unlock()
	history(`{"NZBID":7,"Name":"Done.Film","Kind":"NZB","Category":"reelkeeper-movies","Status":"SUCCESS/PAR","DestDir":"` + job + `","FinalDir":""}`)
	c := f.client()
	// The job disappears from NZBGet's history once it is deleted there.
	srv := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		if strings.Contains(string(b), "HistoryFinalDelete") {
			history("")
		}
		srv.ServeHTTP(w, r)
	})
	if err := c.RemoveJobs(context.Background(), "reelkeeper-movies", []string{"7"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(job); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the completed folder is still there: %v", err)
	}
	if got := f.editqueues(); len(got) != 1 || got[0] != "HistoryFinalDelete 7" {
		t.Errorf("editqueue = %v", got)
	}
	// A job NZBGet still lists afterwards is an error.
	f.srv.Config.Handler = srv
	history(`{"NZBID":8,"Name":"Stuck","Kind":"NZB","Category":"reelkeeper-movies","Status":"FAILURE/PAR","DestDir":"/x/intermediate/Stuck.#8"}`)
	if err := c.RemoveJobs(context.Background(), "reelkeeper-movies", []string{"8"}, true); err == nil || !strings.Contains(err.Error(), "still lists") {
		t.Errorf("still listed: %v", err)
	}
	// A folder outside the category's is refused, and nothing is deleted.
	outside := filepath.Join(root, "elsewhere", "Film")
	mustMkdir(t, outside)
	history(`{"NZBID":9,"Name":"Film","Kind":"NZB","Category":"reelkeeper-movies","Status":"SUCCESS/PAR","DestDir":"` + outside + `"}`)
	before := len(f.editqueues())
	if err := c.RemoveJobs(context.Background(), "reelkeeper-movies", []string{"9"}, true); err == nil || !strings.Contains(err.Error(), "left it alone") {
		t.Errorf("outside the category folder: %v", err)
	}
	if _, err := os.Stat(outside); err != nil || len(f.editqueues()) != before {
		t.Errorf("a refused removal still did something: %v", err)
	}
}

func TestNZBGetRemoveQueuedAndKeepFiles(t *testing.T) {
	f := newFakeNZBGet(t)
	ctx := context.Background()
	// Queue jobs go with GroupFinalDelete (no trace in the history); a job
	// in the history asked to keep its files is only hidden, so the
	// duplicate-proof record of what failed stays and nothing is deleted.
	srv := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		if strings.Contains(string(b), "GroupFinalDelete") {
			f.mu.Lock()
			f.answers["listgroups"] = `{"version":"1.1","id":1,"result":[]}`
			f.mu.Unlock()
		}
		if strings.Contains(string(b), `"HistoryDelete"`) {
			f.mu.Lock()
			f.answers["history"] = `{"version":"1.1","id":1,"result":[]}`
			f.mu.Unlock()
		}
		srv.ServeHTTP(w, r)
	})
	if err := f.client().RemoveJobs(ctx, "reelkeeper-movies", []string{"19", "20", "26", "404"}, false); err != nil {
		t.Fatal(err)
	}
	if got := f.editqueues(); strings.Join(got, "|") != "GroupFinalDelete 19,20|HistoryDelete 26" {
		t.Errorf("editqueue = %v", got)
	}
}

func TestNZBGetDeleteTakesAMoment(t *testing.T) {
	f := newFakeNZBGet(t)
	// A job that is downloading is still listed for a moment after it is
	// deleted: the check waits a little before deciding.
	n, gone := 0, `{"version":"1.1","id":1,"result":[]}`
	srv := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		if strings.Contains(string(b), "listgroups") {
			f.mu.Lock()
			n++
			if n >= 4 { // the listing before the delete, then two more
				f.answers["listgroups"] = gone
			}
			f.mu.Unlock()
		}
		srv.ServeHTTP(w, r)
	})
	if err := f.client().RemoveJobs(context.Background(), "reelkeeper-movies", []string{"19"}, true); err != nil {
		t.Errorf("a job that went a moment later: %v", err)
	}
}
