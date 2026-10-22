package download

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// NZBGet is a NZBGet client (its JSON-RPC API at /jsonrpc, with the control
// username and password).
//
// Reelkeeper puts its jobs in its own categories (reelkeeper-movies,
// reelkeeper-tv) and only ever lists, changes or removes jobs in them.
// Unlike SABnzbd, NZBGet keeps a job in its queue while it is checked,
// repaired and unpacked, and moves it to its history only when it has ended
// (success, warning or failure). It files a job whose category it has no
// entry for under DestDir/<category>, so Reelkeeper doesn't create categories
// in NZBGet at all (that would mean rewriting its whole config file and
// restarting it). Facts about its API that this relies on were checked
// against NZBGet 26.3 (and 24.0 and 21.1): see the Usenet plan, "CHECKED
// (NZBGet)", and testdata/nzbget.
type NZBGet struct {
	URL      string // e.g. http://192.168.1.20:6789 (/jsonrpc is added)
	Username string
	Password string

	once   sync.Once
	client *http.Client
}

// minNZBGetMajor is the oldest NZBGet major version Reelkeeper supports.
const minNZBGetMajor = 21

// nzbgetSettle is how long to wait for NZBGet to finish a change (such as
// deleting a job that was downloading) before deciding it didn't happen.
var nzbgetSettle = 300 * time.Millisecond

func (n *NZBGet) http() *http.Client {
	n.once.Do(func() { n.client = &http.Client{Timeout: 60 * time.Second} })
	return n.client
}

// nzbgetLoginError is "wrong username or password". It matches ErrLogin, so callers
// don't care which client said it.
type nzbgetLoginError struct{}

func (nzbgetLoginError) Error() string        { return "NZBGet refused the username or password" }
func (nzbgetLoginError) Is(target error) bool { return target == ErrLogin }

// nzbgetRPCError is an error NZBGet itself answered with (a call it didn't
// understand, or isn't allowed): not a network or login problem.
type nzbgetRPCError struct{ msg string }

func (e nzbgetRPCError) Error() string { return e.msg }

// call sends one JSON-RPC request and decodes its result into out (when out
// isn't nil). what says, in plain words, what the request was for, such as
// "list its queue": it's used in error messages.
func (n *NZBGet) call(ctx context.Context, what, method string, params []any, out any) error {
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(map[string]any{"method": method, "params": params, "id": 1})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(n.URL, "/")+"/jsonrpc", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(n.Username, n.Password)
	resp, err := n.http().Do(req)
	if err != nil {
		return &unreachableError{msg: "can't reach NZBGet: " + err.Error(), err: err}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nzbgetLoginError{}
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("NZBGet couldn't %s (%d %s)", what, resp.StatusCode, short(strings.TrimSpace(string(b))))
	}
	var ans struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &ans) != nil || (ans.Result == nil && ans.Error == nil) {
		return fmt.Errorf("NZBGet's answer didn't make sense (is %s NZBGet's address?)", n.URL) //nolint:staticcheck // a proper name
	}
	if ans.Error != nil {
		return nzbgetRPCError{fmt.Sprintf("NZBGet couldn't %s: %s", what, ans.Error.Message)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(ans.Result, out); err != nil {
		return fmt.Errorf("NZBGet's answer to %s didn't make sense: %w", what, err) //nolint:staticcheck // a proper name
	}
	return nil
}

// Name is what messages call this client.
func (n *NZBGet) Name() string { return "NZBGet" }

// Version returns NZBGet's version, such as "26.3", and refuses one older
// than 21.0.
func (n *NZBGet) Version(ctx context.Context) (string, error) {
	var v string
	if err := n.call(ctx, "say its version", "version", nil, &v); err != nil {
		return "", err
	}
	if v == "" {
		return "", fmt.Errorf("NZBGet's answer didn't make sense (is %s NZBGet's address?)", n.URL) //nolint:staticcheck // a proper name
	}
	major, _, _ := strings.Cut(v, ".")
	if num, err := strconv.Atoi(major); err == nil && num < minNZBGetMajor {
		return "", fmt.Errorf("NZBGet %s is too old; Reelkeeper needs NZBGet %d.0 or newer", v, minNZBGetMajor)
	}
	return v, nil
}

// options reads NZBGet's settings (its nzbget.conf), by name.
func (n *NZBGet) options(ctx context.Context) (map[string]string, error) {
	var list []struct{ Name, Value string }
	if err := n.call(ctx, "show its settings", "config", nil, &list); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(list))
	for _, o := range list {
		m[o.Name] = o.Value
	}
	return m, nil
}

// nzbgetStatus is what NZBGet's "status" call says about the whole client.
type nzbgetStatus struct {
	DownloadRate    int64
	DownloadPaused  bool
	PostPaused      bool
	QuotaReached    bool
	FreeDiskSpaceLo int64
	FreeDiskSpaceHi int64
	// The intermediate folder's disk; newer NZBGets only.
	FreeInterDiskSpaceLo *int64
	FreeInterDiskSpaceHi *int64
}

func (n *NZBGet) status(ctx context.Context) (nzbgetStatus, error) {
	var st nzbgetStatus
	err := n.call(ctx, "show its status", "status", nil, &st)
	return st, err
}

// Problems lists NZBGet settings that stop Reelkeeper working well, in plain
// words. The connection test shows them; the ones in refusals make it fail.
func (n *NZBGet) Problems(ctx context.Context) (warnings, refusals []string, err error) {
	st, err := n.status(ctx)
	if err != nil {
		return nil, nil, err
	}
	if st.DownloadPaused || st.PostPaused {
		warnings = append(warnings, "NZBGet is paused, so nothing downloads or is unpacked until it is resumed (the Resume button at the top of its page).")
	}
	if st.QuotaReached {
		warnings = append(warnings, "NZBGet has reached its download quota, so it isn't downloading (Settings → Download Queue).")
	}
	opts, err := n.options(ctx)
	var denied nzbgetRPCError
	if errors.As(err, &denied) {
		return append(warnings, "NZBGet wouldn't show Reelkeeper its settings, so they weren't checked. Use its control username and password (Settings → Security), not the restricted one."), nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	// NZBGet can forget finished jobs at once. Reelkeeper finds out that a
	// download finished from NZBGet's history.
	if days, err := strconv.Atoi(strings.TrimSpace(opts["KeepHistory"])); err == nil && days < 1 {
		refusals = append(refusals, "NZBGet is set to keep no history (Settings → Download Queue → KeepHistory is 0). "+
			"Reelkeeper sees that a download finished in NZBGet's history, so it would never import anything. Set KeepHistory to a number of days "+
			"(the default is 30); Reelkeeper removes its own jobs once it has imported them.")
	}
	// Finished jobs go in DestDir/<category>/<job>: Reelkeeper only deletes a
	// job's folder when it is inside a folder named after the category.
	if strings.EqualFold(strings.TrimSpace(opts["AppendCategoryDir"]), "no") {
		refusals = append(refusals, "NZBGet is set not to put each category's downloads in its own folder (Settings → Paths → AppendCategoryDir is off). "+
			"Reelkeeper only cleans up finished downloads inside a folder named after its category, so they would pile up. Turn AppendCategoryDir on.")
	}
	for _, c := range []string{settings.MovieCategory, settings.TVCategory} {
		if msg := nzbgetCategoryFolder(opts, c); msg != "" {
			refusals = append(refusals, msg)
		}
	}
	if strings.EqualFold(strings.TrimSpace(opts["HealthCheck"]), "pause") {
		warnings = append(warnings, "NZBGet pauses a download that is too damaged to repair instead of failing it (Settings → Download Queue → HealthCheck is pause), "+
			"so Reelkeeper would wait on it for ever. Choose park or delete.")
	}
	return warnings, refusals, nil
}

// nzbgetCategoryFolder says what's wrong when the user has set up a category
// in NZBGet by Reelkeeper's name with a folder of its own that isn't named
// like it, else "". NZBGet doesn't need a category entry for Reelkeeper's
// ones, so most people have none.
func nzbgetCategoryFolder(opts map[string]string, name string) string {
	for k, v := range opts {
		if !strings.HasPrefix(k, "Category") || !strings.HasSuffix(k, ".Name") || !strings.EqualFold(strings.TrimSpace(v), name) {
			continue
		}
		dir := strings.TrimSpace(opts[strings.TrimSuffix(k, ".Name")+".DestDir"])
		if dir != "" && filepath.Base(filepath.Clean(dir)) != name {
			return fmt.Sprintf("NZBGet has a %s category with its own folder (%s) that isn't named %s. Reelkeeper only cleans up finished downloads "+
				"inside a folder named after the category, so they would pile up. Clear that category's DestDir in NZBGet (Settings → Categories), "+
				"or delete the category: NZBGet doesn't need one.", name, dir, name)
		}
	}
	return ""
}

// EnsureCategory checks that NZBGet will file a Reelkeeper category's jobs
// where Reelkeeper cleans up. NZBGet needs no category entry for that, so
// nothing is created.
func (n *NZBGet) EnsureCategory(ctx context.Context, name string) error {
	opts, err := n.options(ctx)
	var denied nzbgetRPCError
	if errors.As(err, &denied) {
		return nil // the connection test has already said so
	}
	if err != nil {
		return err
	}
	if msg := nzbgetCategoryFolder(opts, name); msg != "" {
		return errors.New(msg)
	}
	return nil
}

// AddNZB adds an .nzb to NZBGet in a category, under the release's name, and
// returns the job's ID. top gives it high priority and puts it first, so it
// downloads before Normal jobs (a Seerr request). Duplicate checking is
// switched off for the job (DupeMode FORCE): NZBGet would otherwise accept a
// release it has seen before and quietly drop it into its history without
// downloading it.
func (n *NZBGet) AddNZB(ctx context.Context, nzb []byte, name, category string, top bool) (string, error) {
	priority := 0
	if top {
		priority = 50 // High
	}
	var id int
	err := n.call(ctx, "add the .nzb", "append", []any{
		safeFileName(name) + ".nzb", base64.StdEncoding.EncodeToString(nzb), category, priority,
		top,   // add to the top of the queue
		false, // add paused
		"",    // duplicate key
		0,     // duplicate score
		"FORCE",
		[]any{}, // post-processing parameters
	}, &id)
	if err != nil {
		return "", err
	}
	if id <= 0 {
		return "", errors.New("NZBGet refused the .nzb")
	}
	return strconv.Itoa(id), nil
}

// FreeSpace is the space free where NZBGet puts what it downloads and
// finished jobs, as NZBGet reports it (the smaller of its destination and
// intermediate folders' disks); -1 when it doesn't say.
func (n *NZBGet) FreeSpace(ctx context.Context) (int64, error) {
	st, err := n.status(ctx)
	if err != nil {
		return -1, err
	}
	free := nzbgetBytes(st.FreeDiskSpaceLo, st.FreeDiskSpaceHi)
	if st.FreeInterDiskSpaceLo != nil && st.FreeInterDiskSpaceHi != nil {
		if inter := nzbgetBytes(*st.FreeInterDiskSpaceLo, *st.FreeInterDiskSpaceHi); inter >= 0 && (free < 0 || inter < free) {
			free = inter
		}
	}
	return free, nil
}

// nzbgetBytes joins one of NZBGet's sizes, sent as two 32-bit halves. All
// ones is how it says "unknown": -1.
func nzbgetBytes(lo, hi int64) int64 {
	if lo == 0xFFFFFFFF && hi == 0xFFFFFFFF {
		return -1
	}
	return hi<<32 | (lo & 0xFFFFFFFF)
}

// nzbgetGroup is a job in NZBGet's queue ("listgroups").
type nzbgetGroup struct {
	ID       int    `json:"NZBID"`
	Name     string `json:"NZBName"`
	Category string
	Status   string
	// Sizes come in two 32-bit halves. Remaining includes Paused: the repair
	// files NZBGet only fetches when it needs them.
	FileSizeLo, FileSizeHi           int64
	RemainingSizeLo, RemainingSizeHi int64
	PausedSizeLo, PausedSizeHi       int64
	MaxPriority                      int
	PostInfoText                     string
}

// nzbgetHistoryItem is a job in NZBGet's history.
type nzbgetHistoryItem struct {
	ID          int `json:"NZBID"`
	Name        string
	Kind        string
	Category    string
	Status      string
	DestDir     string
	FinalDir    string
	HistoryTime int64
	// FileSizeLo and Hi are the job's size in two 32-bit halves.
	FileSizeLo, FileSizeHi int64
	// Health and CriticalHealth are tenths of a percent.
	Health, CriticalHealth        int
	TotalArticles, FailedArticles int
}

// Jobs lists Reelkeeper's jobs in a category: those in the queue (downloading
// or being post-processed) and those in the history, as Torrents (Hash is the
// job's ID), so the Queue, Check and cleanup read them the way they read
// torrents.
func (n *NZBGet) Jobs(ctx context.Context, category string) ([]Torrent, error) {
	var groups []nzbgetGroup
	if err := n.call(ctx, "list its queue", "listgroups", nil, &groups); err != nil {
		return nil, err
	}
	var history []nzbgetHistoryItem
	if err := n.call(ctx, "list its history", "history", nil, &history); err != nil {
		return nil, err
	}
	var out []Torrent
	var speed int64
	for _, g := range groups {
		if g.Category != category {
			continue
		}
		t := nzbgetQueueTorrent(g)
		// NZBGet gives one speed for everything it downloads: it belongs to
		// the first job downloading. Ask for it only when something is.
		if t.State == torrent.Downloading && speed == 0 {
			speed = -1
			if st, err := n.status(ctx); err == nil && st.DownloadRate > 0 {
				speed = st.DownloadRate
				t.Speed = speed
				t.ETA = time.Duration(t.Left/speed) * time.Second
			}
		}
		out = append(out, t)
	}
	for _, j := range history {
		if j.Category != category || j.Kind == "DUP" {
			continue
		}
		if t, ok := nzbgetHistoryTorrent(j); ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// nzbgetQueueTorrent describes a job still in NZBGet's queue. It isn't done,
// even when everything is downloaded: it still has to be checked and
// unpacked.
func nzbgetQueueTorrent(g nzbgetGroup) Torrent {
	size := nzbgetBytes(g.FileSizeLo, g.FileSizeHi)
	paused := nzbgetBytes(g.PausedSizeLo, g.PausedSizeHi)
	left := max(nzbgetBytes(g.RemainingSizeLo, g.RemainingSizeHi)-paused, 0)
	t := Torrent{
		Hash: strconv.Itoa(g.ID), Name: g.Name, Category: g.Category, State: nzbgetQueueState(g.Status),
		Size: size, Left: left,
	}
	if want := size - paused; want > 0 {
		t.Progress = min(max(1-float64(left)/float64(want), 0), 0.99)
	}
	if nzbgetPostProcessing(t.State) && g.PostInfoText != "" && g.PostInfoText != "NONE" {
		t.Message = g.PostInfoText // "Verifying file ..."
	}
	return t
}

// nzbgetHistoryTorrent describes a job in NZBGet's history, which has ended:
// completed or failed. ok is false for one that someone deleted in NZBGet's
// own page, which counts as removed.
func nzbgetHistoryTorrent(j nzbgetHistoryItem) (Torrent, bool) {
	class, detail, _ := strings.Cut(j.Status, "/")
	if class == "DELETED" && detail == "MANUAL" {
		return Torrent{}, false
	}
	// The job's folder is DestDir, unless a script moved it (FinalDir). A
	// failed job's is NZBGet's own working folder.
	dir := j.FinalDir
	if dir == "" {
		dir = j.DestDir
	}
	t := Torrent{
		Hash: strconv.Itoa(j.ID), Name: j.Name, Category: j.Category, Size: nzbgetBytes(j.FileSizeLo, j.FileSizeHi),
		ContentPath: dir, SavePath: filepath.Dir(dir), Progress: 0.99,
	}
	// A post-processing script failing (WARNING/SCRIPT) says nothing about
	// the files.
	if class == "SUCCESS" || j.Status == "WARNING/SCRIPT" {
		t.State, t.Progress, t.Verified = torrent.Completed, 1, t.Size
		if j.HistoryTime > 0 {
			done := time.Unix(j.HistoryTime, 0).UTC()
			t.CompletedOn = &done
		}
		return t, true
	}
	t.State = torrent.Failed
	t.Message = nzbgetFailure(j)
	return t, true
}

// nzbgetPercent writes one of NZBGet's healths (tenths of a percent).
func nzbgetPercent(tenths int) string { return fmt.Sprintf("%.1f%%", float64(tenths)/10) }

// nzbgetFailure says, in plain words, why a job in NZBGet's history failed.
// NZBGet's own log for the job has more, but costs a call per failed job.
func nzbgetFailure(j nzbgetHistoryItem) string {
	missing := fmt.Sprintf("%d of its %d articles were missing", j.FailedArticles, j.TotalArticles)
	switch j.Status {
	case "FAILURE/HEALTH", "DELETED/HEALTH":
		return fmt.Sprintf("NZBGet cancelled it: too many articles are missing to repair it (its health is %s, it needs %s)",
			nzbgetPercent(j.Health), nzbgetPercent(j.CriticalHealth))
	case "FAILURE/PAR", "WARNING/DAMAGED":
		return "It is damaged and par2 couldn't repair it (" + missing + ")"
	case "FAILURE/UNPACK":
		return "NZBGet couldn't unpack it (a damaged or password-protected archive)"
	case "FAILURE/SCAN":
		return "NZBGet couldn't read the .nzb file"
	case "FAILURE/MOVE":
		return "NZBGet couldn't move the finished files into place"
	case "FAILURE/SCRIPT":
		return "One of NZBGet's post-processing scripts failed"
	case "FAILURE/BAD", "DELETED/BAD":
		return "It was marked bad in NZBGet"
	case "WARNING/PASSWORD":
		return "It is password-protected"
	case "WARNING/REPAIRABLE":
		return "It is damaged and can be repaired, but NZBGet's ParCheck is set to manual so it didn't (Settings → Par-Check & Repair)"
	case "WARNING/SPACE":
		return "NZBGet ran out of disk space unpacking it"
	case "WARNING/HEALTH":
		return fmt.Sprintf("It is damaged (its health is %s, it needs %s)", nzbgetPercent(j.Health), nzbgetPercent(j.CriticalHealth))
	case "DELETED/COPY", "DELETED/DUPE":
		return "NZBGet's duplicate check refused it: it has a copy of this release already (Settings → Download Queue → DupeCheck)"
	}
	return fmt.Sprintf("NZBGet says it failed (%s)", j.Status)
}

// nzbgetQueueState maps a queue job's status to Reelkeeper's states.
func nzbgetQueueState(status string) torrent.State {
	switch status {
	case "DOWNLOADING":
		return torrent.Downloading
	case "QUEUED", "FETCHING":
		return torrent.Queued
	case "PAUSED":
		return torrent.Paused
	case "REPAIRING":
		return torrent.Repairing
	case "UNPACKING":
		return torrent.Unpacking
	case "MOVING":
		return torrent.Moving
	case "PP_QUEUED", "LOADING_PARS", "VERIFYING_SOURCES", "VERIFYING_REPAIRED", "RENAMING", "POST_UNPACK_RENAMING",
		"EXECUTING_SCRIPT", "PP_FINISHED":
		// Waiting for post-processing, checking, or running the user's own
		// script.
		return torrent.Checking
	}
	return torrent.Unknown
}

// nzbgetPostProcessing says whether a queue job is past downloading.
func nzbgetPostProcessing(s torrent.State) bool {
	switch s {
	case torrent.Checking, torrent.Repairing, torrent.Unpacking, torrent.Moving:
		return true
	}
	return false
}

// RemoveJobs removes jobs from a category, from the queue or the history,
// whichever holds them. NZBGet deletes what an unfinished or failed job
// downloaded, but never a completed job's folder, so with deleteFiles
// Reelkeeper deletes that itself (removeCompletedFolder). Without it a
// finished job is only hidden from NZBGet's history. IDs NZBGet doesn't have
// are skipped.
func (n *NZBGet) RemoveJobs(ctx context.Context, category string, ids []string, deleteFiles bool) error {
	jobs, err := n.Jobs(ctx, category)
	if err != nil {
		return err
	}
	var queued, done []string
	for _, t := range jobs {
		if !slices.Contains(ids, t.Hash) {
			continue
		}
		if t.ContentPath == "" { // not in the history yet
			queued = append(queued, t.Hash)
			continue
		}
		done = append(done, t.Hash)
		if deleteFiles && t.State == torrent.Completed {
			if err := removeCompletedFolder(t.ContentPath, category); err != nil {
				return err
			}
		}
	}
	if err := n.edit(ctx, "remove a job from its queue", "GroupFinalDelete", queued); err != nil {
		return err
	}
	// HistoryFinalDelete forgets the job and deletes a failed job's leftovers;
	// HistoryDelete only hides it.
	command := "HistoryDelete"
	if deleteFiles {
		command = "HistoryFinalDelete"
	}
	if err := n.edit(ctx, "remove a job from its history", command, done); err != nil {
		return err
	}
	return n.gone(ctx, category, append(queued, done...), "remove")
}

// edit sends one editqueue command for jobs (by ID). NZBGet answers false for
// an ID it doesn't have; that isn't an error here, since the caller checks the
// result by listing again.
func (n *NZBGet) edit(ctx context.Context, what, command string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	nums := make([]int, 0, len(ids))
	for _, id := range ids {
		num, err := strconv.Atoi(id)
		if err != nil {
			return fmt.Errorf("%q isn't one of NZBGet's job numbers", id)
		}
		nums = append(nums, num)
	}
	var ok bool
	return n.call(ctx, what, "editqueue", []any{command, "", nums}, &ok)
}

// gone checks that NZBGet no longer lists these jobs. Deleting a job that is
// downloading can take a moment, so it looks again a few times first.
func (n *NZBGet) gone(ctx context.Context, category string, ids []string, what string) error {
	if len(ids) == 0 {
		return nil
	}
	var still string
	for attempt := range 4 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(nzbgetSettle):
			}
		}
		jobs, err := n.Jobs(ctx, category)
		if err != nil {
			return err
		}
		still = ""
		for _, t := range jobs {
			if slices.Contains(ids, t.Hash) {
				still = t.Name
			}
		}
		if still == "" {
			return nil
		}
	}
	return fmt.Errorf("NZBGet said it would %s %s, but still lists it", what, still)
}

// PauseJobs pauses jobs in the queue.
func (n *NZBGet) PauseJobs(ctx context.Context, category string, ids []string) error {
	return n.queueAction(ctx, category, ids, "GroupPause", "pause", torrent.Paused)
}

// ResumeJobs resumes paused jobs in the queue.
func (n *NZBGet) ResumeJobs(ctx context.Context, category string, ids []string) error {
	return n.queueAction(ctx, category, ids, "GroupResume", "resume", "")
}

// queueAction pauses or resumes jobs, then checks they changed (NZBGet says
// no to an ID it doesn't have, but that is checked too). want is the state to
// expect, or "" for "anything but paused".
func (n *NZBGet) queueAction(ctx context.Context, category string, ids []string, command, verb string, want torrent.State) error {
	if err := n.edit(ctx, verb+" a job", command, ids); err != nil {
		return err
	}
	jobs, err := n.Jobs(ctx, category)
	if err != nil {
		return err
	}
	for _, id := range ids {
		i := slices.IndexFunc(jobs, func(t Torrent) bool { return t.Hash == id })
		switch {
		case i < 0:
			return errors.New("NZBGet no longer has this download")
		case jobs[i].ContentPath != "" || nzbgetPostProcessing(jobs[i].State):
			return errors.New("this download has already finished downloading in NZBGet")
		case want != "" && jobs[i].State != want, want == "" && jobs[i].State == torrent.Paused:
			return fmt.Errorf("NZBGet didn't %s %s", verb, jobs[i].Name)
		}
	}
	return nil
}

// TopJobs moves jobs to the top of the queue. NZBGet downloads a job with a
// higher priority first wherever it is in the queue, so a job can only pass
// others of its own priority or lower. It says yes either way, so the queue
// is looked at afterwards.
func (n *NZBGet) TopJobs(ctx context.Context, _ string, ids []string) error {
	for _, id := range ids {
		if err := n.edit(ctx, "move a job to the top", "GroupMoveTop", []string{id}); err != nil {
			return err
		}
	}
	var groups []nzbgetGroup
	if err := n.call(ctx, "list its queue", "listgroups", nil, &groups); err != nil {
		return err
	}
	for _, id := range ids {
		pos := slices.IndexFunc(groups, func(g nzbgetGroup) bool { return strconv.Itoa(g.ID) == id })
		if pos < 0 {
			return errors.New("NZBGet no longer has this download")
		}
		// Whatever is still ahead should have a higher priority, be moving
		// up too, or be past downloading.
		for _, ahead := range groups[:pos] {
			if ahead.MaxPriority <= groups[pos].MaxPriority && !nzbgetPostProcessing(nzbgetQueueState(ahead.Status)) &&
				!slices.Contains(ids, strconv.Itoa(ahead.ID)) {
				return errors.New("NZBGet couldn't move it to the top: jobs ahead of it download first")
			}
		}
	}
	return nil
}
