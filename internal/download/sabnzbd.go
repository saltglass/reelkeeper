package download

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saltglass/reelkeeper/internal/torrent"
)

// SABnzbd is a SABnzbd client (its HTTP API, /api with an API key).
//
// SABnzbd has categories, like qBittorrent. Reelkeeper creates its own
// (reelkeeper-movies, reelkeeper-tv) and only ever lists, changes or removes
// jobs in them. A job is in SABnzbd's queue while it downloads, then moves to
// its history, where it is post-processed (checked, repaired, unpacked) and
// ends Completed or Failed. Facts about its API that this relies on were
// checked against SABnzbd 5.1 (see the Usenet plan, "CHECKED 2026-10-22").
type SABnzbd struct {
	URL    string // e.g. http://192.168.1.20:8080 (/api is added)
	APIKey string

	once   sync.Once
	client *http.Client
}

// minSABnzbdMajor is the oldest SABnzbd major version Reelkeeper supports.
const minSABnzbdMajor = 4

func (s *SABnzbd) http() *http.Client {
	// Adding a big .nzb can take a while for SABnzbd to parse.
	s.once.Do(func() { s.client = &http.Client{Timeout: 60 * time.Second} })
	return s.client
}

// apiKeyError is "wrong API key". It matches ErrLogin, so callers don't care
// which client said it.
type apiKeyError struct{}

func (apiKeyError) Error() string        { return "SABnzbd refused the API key" }
func (apiKeyError) Is(target error) bool { return target == ErrLogin }

// call sends one API request and decodes the JSON answer into out (when out
// isn't nil). what says, in plain words, what the request was for, such as
// "list its queue": it's used in error messages. body, when not nil, is sent
// as a multipart form (adding an .nzb).
func (s *SABnzbd) call(ctx context.Context, what string, q url.Values, body func() (io.Reader, string), out any) error {
	q.Set("apikey", s.APIKey)
	q.Set("output", "json")
	method, endpoint := http.MethodGet, strings.TrimRight(s.URL, "/")+"/api?"+q.Encode()
	var r io.Reader
	contentType := ""
	if body != nil {
		method = http.MethodPost
		r, contentType = body()
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, r)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := s.http().Do(req)
	if err != nil {
		// Go's error repeats the address, which has the API key in it.
		return &unreachableError{msg: "can't reach SABnzbd: " + redactKey(err.Error(), s.APIKey), err: err}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	text := strings.TrimSpace(string(b))
	switch {
	// A wrong or missing key is answered in plain text with HTTP 200.
	case strings.HasPrefix(text, "API Key Incorrect"), strings.HasPrefix(text, "API Key Required"):
		return apiKeyError{}
	case resp.StatusCode == http.StatusForbidden:
		return errors.New("SABnzbd refused Reelkeeper's address; add Reelkeeper's host name to SABnzbd's host_whitelist (Config → Special)")
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("SABnzbd couldn't %s (%d %s)", what, resp.StatusCode, short(text))
	}
	var failed struct {
		Status *bool  `json:"status"`
		Error  string `json:"error"`
	}
	if json.Unmarshal(b, &failed) != nil {
		return fmt.Errorf("SABnzbd's answer didn't make sense (is %s SABnzbd's address?)", s.URL) //nolint:staticcheck // a proper name
	}
	if failed.Error != "" {
		return fmt.Errorf("SABnzbd couldn't %s: %s", what, failed.Error)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("SABnzbd's answer to %s didn't make sense: %w", what, err) //nolint:staticcheck // a proper name
	}
	return nil
}

func redactKey(msg, key string) string {
	if key == "" {
		return msg
	}
	return strings.ReplaceAll(msg, key, "…")
}

func short(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// Name is what messages call this client.
func (s *SABnzbd) Name() string { return "SABnzbd" }

// Version returns SABnzbd's version, such as "5.1.3", and refuses one older
// than 4.0.
func (s *SABnzbd) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	if err := s.call(ctx, "say its version", url.Values{"mode": {"version"}}, nil, &v); err != nil {
		return "", err
	}
	if v.Version == "" {
		return "", fmt.Errorf("SABnzbd's answer didn't make sense (is %s SABnzbd's address?)", s.URL) //nolint:staticcheck // a proper name
	}
	major, _, _ := strings.Cut(v.Version, ".")
	if n, err := strconv.Atoi(major); err == nil && n < minSABnzbdMajor {
		return "", fmt.Errorf("SABnzbd %s is too old; Reelkeeper needs SABnzbd %d.0 or newer", v.Version, minSABnzbdMajor)
	}
	return v.Version, nil
}

// Problems lists SABnzbd settings that stop Reelkeeper working well, in plain
// words. The connection test shows them; the ones in refusals make it fail.
func (s *SABnzbd) Problems(ctx context.Context) (warnings, refusals []string, err error) {
	var c struct {
		Config struct {
			Misc struct {
				NoDupes        json.Number `json:"no_dupes"`
				NoSeriesDupes  json.Number `json:"no_series_dupes"`
				NoSmartDupes   json.Number `json:"no_smart_dupes"`
				RetentionOpt   string      `json:"history_retention_option"`
				RetentionCount json.Number `json:"history_retention_number"`
			} `json:"misc"`
		} `json:"config"`
	}
	if err := s.call(ctx, "show its settings", url.Values{"mode": {"get_config"}, "section": {"misc"}}, nil, &c); err != nil {
		return nil, nil, err
	}
	m := c.Config.Misc
	on := func(n json.Number) bool { return n.String() != "" && n.String() != "0" }
	if on(m.NoDupes) || on(m.NoSeriesDupes) || on(m.NoSmartDupes) {
		warnings = append(warnings, "SABnzbd's duplicate detection is on (Config → Switches → Duplicate Detection). "+
			"SABnzbd remembers every release it has downloaded, so a release Reelkeeper grabs again (after a failed import, or for "+
			"a second version) would be refused, paused or failed. Turn it off; Reelkeeper already avoids grabbing the same release twice.")
	}
	// SABnzbd can archive or delete finished jobs at once, or keep only a few.
	// Reelkeeper finds out that a job finished from its history.
	// "all-archive" and "all-delete" take every finished job out of the
	// history at once; "number-…" keeps the last N.
	opt := m.RetentionOpt
	keep, _ := strconv.Atoi(m.RetentionCount.String())
	if strings.HasPrefix(opt, "all-") || (strings.HasPrefix(opt, "number-") && keep < 1) {
		refusals = append(refusals, "SABnzbd is set to remove finished jobs from its history straight away (Config → Switches → History Retention). "+
			"Reelkeeper sees that a download finished in SABnzbd's history, so it would never import anything. Choose \"Keep all jobs\", "+
			"or a number of days or jobs; Reelkeeper removes its own jobs once it has imported them.")
	}
	return warnings, refusals, nil
}

// EnsureCategory creates one of Reelkeeper's categories in SABnzbd, if it
// isn't there, with its own folder (the category's name, inside SABnzbd's
// completed folder) and SABnzbd's full post-processing (repair, unpack and
// delete the leftovers). An unknown category isn't an error to SABnzbd, which
// files the job under its default one, so the category is checked again
// afterwards.
func (s *SABnzbd) EnsureCategory(ctx context.Context, name string) error {
	has := func() (bool, error) {
		var cats struct {
			Categories []string `json:"categories"`
		}
		err := s.call(ctx, "list its categories", url.Values{"mode": {"get_cats"}}, nil, &cats)
		return slices.Contains(cats.Categories, name), err
	}
	if ok, err := has(); ok || err != nil {
		return err
	}
	if err := s.call(ctx, "create the "+name+" category", url.Values{
		"mode": {"set_config"}, "section": {"categories"}, "name": {name}, "dir": {name}, "pp": {"3"},
	}, nil, nil); err != nil {
		return err
	}
	if ok, err := has(); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("SABnzbd didn't create the %s category; create it in SABnzbd (Config → Categories) with the folder %s", name, name)
		}
		return err
	}
	return nil
}

// AddNZB adds an .nzb to SABnzbd in a category, under the release's name,
// and returns the job's ID. top gives it high priority, so it downloads
// before Normal jobs (a Seerr request).
func (s *SABnzbd) AddNZB(ctx context.Context, nzb []byte, name, category string, top bool) (string, error) {
	q := url.Values{"mode": {"addfile"}, "cat": {category}, "nzbname": {name}}
	if top {
		q.Set("priority", "1") // High
	}
	var ans struct {
		Status bool     `json:"status"`
		IDs    []string `json:"nzo_ids"`
	}
	err := s.call(ctx, "add the .nzb", q, func() (io.Reader, string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("name", safeFileName(name)+".nzb")
		_, _ = fw.Write(nzb)
		_ = mw.Close()
		return &buf, mw.FormDataContentType()
	}, &ans)
	if err != nil {
		return "", err
	}
	if !ans.Status || len(ans.IDs) == 0 {
		return "", errors.New("SABnzbd refused the .nzb; if its duplicate detection is on, it refuses releases it has downloaded before (Config → Switches)")
	}
	return ans.IDs[0], nil
}

// safeFileName keeps a release name usable as an upload's file name.
func safeFileName(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == '"' || r < ' ' {
			return '_'
		}
		return r
	}, name)
}

type sabQueue struct {
	Queue struct {
		KBPerSec string `json:"kbpersec"`
		// DiskSpace2 is the space free in the completed folder, in GB.
		DiskSpace2 string        `json:"diskspace2"`
		Slots      []sabQueueJob `json:"slots"`
	} `json:"queue"`
}

// FreeSpace is the space free in SABnzbd's completed folder (where jobs are
// unpacked), as SABnzbd reports it; -1 when it doesn't say.
func (s *SABnzbd) FreeSpace(ctx context.Context) (int64, error) {
	var q sabQueue
	if err := s.call(ctx, "list its queue", url.Values{"mode": {"queue"}, "limit": {"1"}}, nil, &q); err != nil {
		return -1, err
	}
	gb, err := strconv.ParseFloat(strings.TrimSpace(q.Queue.DiskSpace2), 64)
	if err != nil {
		return -1, nil
	}
	return int64(gb * (1 << 30)), nil
}

type sabQueueJob struct {
	ID         string   `json:"nzo_id"`
	Name       string   `json:"filename"`
	Category   string   `json:"cat"`
	Status     string   `json:"status"`
	MB         string   `json:"mb"`
	MBLeft     string   `json:"mbleft"`
	Percentage string   `json:"percentage"`
	TimeLeft   string   `json:"timeleft"`
	TimeAdded  int64    `json:"time_added"`
	Labels     []string `json:"labels"`
}

type sabHistory struct {
	History struct {
		Slots []sabHistoryJob `json:"slots"`
	} `json:"history"`
}

type sabHistoryJob struct {
	ID          string `json:"nzo_id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Status      string `json:"status"`
	Storage     string `json:"storage"`
	Bytes       int64  `json:"bytes"`
	Completed   int64  `json:"completed"`
	TimeAdded   int64  `json:"time_added"`
	FailMessage string `json:"fail_message"`
	ActionLine  string `json:"action_line"`
}

// Jobs lists Reelkeeper's jobs in a category: those still in the queue and
// those in the history, as Torrents (Hash is the job's ID), so the Queue,
// Check and cleanup read them the way they read torrents.
func (s *SABnzbd) Jobs(ctx context.Context, category string) ([]Torrent, error) {
	var q sabQueue
	if err := s.call(ctx, "list its queue", url.Values{"mode": {"queue"}, "cat": {category}}, nil, &q); err != nil {
		return nil, err
	}
	var h sabHistory
	if err := s.call(ctx, "list its history", url.Values{"mode": {"history"}, "category": {category}}, nil, &h); err != nil {
		return nil, err
	}
	var out []Torrent
	speed := int64(parseFloat(q.Queue.KBPerSec) * 1024)
	for _, j := range q.Queue.Slots {
		if j.Category != category {
			continue // the filter is SABnzbd's; check it anyway
		}
		t := queueTorrent(j)
		// SABnzbd gives one speed for the whole queue: it belongs to the job
		// downloading first.
		if t.State == torrent.Downloading && speed > 0 {
			t.Speed, speed = speed, 0
		}
		out = append(out, t)
	}
	for _, j := range h.History.Slots {
		if j.Category == category {
			out = append(out, historyTorrent(j))
		}
	}
	return out, nil
}

// queueTorrent describes a job still in SABnzbd's queue. It isn't done, even
// at 100%: it still has to be checked and unpacked in the history.
func queueTorrent(j sabQueueJob) Torrent {
	mb, left := parseFloat(j.MB), parseFloat(j.MBLeft)
	t := Torrent{
		Hash: j.ID, Name: j.Name, Category: j.Category, State: sabQueueState(j.Status),
		Size: int64(mb * (1 << 20)), Left: int64(left * (1 << 20)), Tags: j.Labels,
		Progress: min(parseFloat(j.Percentage)/100, 0.99), ETA: parseClock(j.TimeLeft),
	}
	if j.TimeAdded > 0 {
		t.AddedOn = time.Unix(j.TimeAdded, 0).UTC()
	}
	if slices.Contains(j.Labels, "DUPLICATE") {
		t.Message = "SABnzbd paused it as a duplicate of a release it downloaded before. Turn off its duplicate detection (Config → Switches)."
	}
	return t
}

// historyTorrent describes a job in SABnzbd's history: downloaded, and being
// post-processed, Completed or Failed.
func historyTorrent(j sabHistoryJob) Torrent {
	t := Torrent{
		Hash: j.ID, Name: j.Name, Category: j.Category, State: sabHistoryState(j.Status),
		Size: j.Bytes, Progress: 0.99, ContentPath: j.Storage, SavePath: filepath.Dir(j.Storage),
	}
	if j.TimeAdded > 0 {
		t.AddedOn = time.Unix(j.TimeAdded, 0).UTC()
	}
	switch t.State {
	case torrent.Completed:
		t.Progress, t.Verified = 1, j.Bytes
		if j.Completed > 0 {
			done := time.Unix(j.Completed, 0).UTC()
			t.CompletedOn = &done
		}
	case torrent.Failed:
		t.Message = j.FailMessage
		if t.Message == "" {
			t.Message = "SABnzbd says it failed, without saying why."
		}
	default:
		t.Message = j.ActionLine // "Repairing: 68%"
	}
	return t
}

// sabQueueState maps a queue job's status to Reelkeeper's states.
func sabQueueState(status string) torrent.State {
	switch status {
	case "Downloading":
		return torrent.Downloading
	case "Queued", "Grabbing", "Fetching", "Propagating":
		return torrent.Queued
	case "Paused":
		return torrent.Paused
	case "Checking":
		return torrent.Checking
	}
	return torrent.Unknown
}

// sabHistoryState maps a history job's status to Reelkeeper's states.
func sabHistoryState(status string) torrent.State {
	switch status {
	case "Completed":
		return torrent.Completed
	case "Failed":
		return torrent.Failed
	case "Verifying", "Repairing", "QuickCheck":
		return torrent.Repairing
	case "Extracting":
		return torrent.Unpacking
	case "Moving":
		return torrent.Moving
	case "Queued", "Running", "Fetching":
		// Waiting for post-processing, or running the user's own script.
		return torrent.Checking
	}
	return torrent.Unknown
}

// RemoveJobs removes jobs from a category, from the queue or the history,
// whichever holds them. With deleteFiles, what they downloaded goes too: an
// unfinished or failed job's leftovers are SABnzbd's to delete, but SABnzbd
// leaves a completed job's folder alone, so Reelkeeper deletes that itself
// (removeCompletedFolder). IDs SABnzbd doesn't have are skipped.
func (s *SABnzbd) RemoveJobs(ctx context.Context, category string, ids []string, deleteFiles bool) error {
	jobs, err := s.Jobs(ctx, category)
	if err != nil {
		return err
	}
	del := "0"
	if deleteFiles {
		del = "1"
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
	if len(queued) > 0 {
		if err := s.call(ctx, "remove a job from its queue", url.Values{"mode": {"queue"}, "name": {"delete"}, "value": {strings.Join(queued, ",")}, "del_files": {del}}, nil, nil); err != nil {
			return err
		}
	}
	if len(done) > 0 {
		if err := s.call(ctx, "remove a job from its history", url.Values{"mode": {"history"}, "name": {"delete"}, "value": {strings.Join(done, ",")}, "del_files": {del}}, nil, nil); err != nil {
			return err
		}
	}
	// SABnzbd says yes even to an ID it doesn't have: check.
	return s.gone(ctx, category, append(queued, done...), "remove")
}

// gone checks that SABnzbd no longer lists these jobs.
func (s *SABnzbd) gone(ctx context.Context, category string, ids []string, what string) error {
	if len(ids) == 0 {
		return nil
	}
	jobs, err := s.Jobs(ctx, category)
	if err != nil {
		return err
	}
	for _, t := range jobs {
		if slices.Contains(ids, t.Hash) {
			return fmt.Errorf("SABnzbd said it would %s %s, but still lists it", what, t.Name)
		}
	}
	return nil
}

// PauseJobs pauses jobs in the queue.
func (s *SABnzbd) PauseJobs(ctx context.Context, category string, ids []string) error {
	return s.queueAction(ctx, category, ids, "pause", torrent.Paused)
}

// ResumeJobs resumes paused jobs in the queue.
func (s *SABnzbd) ResumeJobs(ctx context.Context, category string, ids []string) error {
	return s.queueAction(ctx, category, ids, "resume", "")
}

// queueAction pauses or resumes jobs, then checks they changed (SABnzbd says
// yes even to an ID it doesn't have). want is the state to expect, or ""
// for "anything but paused".
func (s *SABnzbd) queueAction(ctx context.Context, category string, ids []string, action string, want torrent.State) error {
	for _, id := range ids {
		if err := s.call(ctx, action+" a job", url.Values{"mode": {"queue"}, "name": {action}, "value": {id}}, nil, nil); err != nil {
			return err
		}
	}
	jobs, err := s.Jobs(ctx, category)
	if err != nil {
		return err
	}
	for _, id := range ids {
		i := slices.IndexFunc(jobs, func(t Torrent) bool { return t.Hash == id })
		switch {
		case i < 0:
			return errors.New("SABnzbd no longer has this download")
		case jobs[i].ContentPath != "":
			return errors.New("this download has already finished downloading in SABnzbd")
		case want != "" && jobs[i].State != want, want == "" && jobs[i].State == torrent.Paused:
			return fmt.Errorf("SABnzbd didn't %s %s", action, jobs[i].Name)
		}
	}
	return nil
}

// TopJobs moves jobs to the top of the queue. SABnzbd keeps jobs in order of
// priority, so a job can only pass others of its own priority; it says so
// (position -1) when it couldn't.
func (s *SABnzbd) TopJobs(ctx context.Context, _ string, ids []string) error {
	for _, id := range ids {
		var ans struct {
			Result struct {
				Position int `json:"position"`
			} `json:"result"`
		}
		if err := s.call(ctx, "move a job to the top", url.Values{"mode": {"switch"}, "value": {id}, "value2": {"0"}}, nil, &ans); err != nil {
			return err
		}
		if ans.Result.Position < 0 {
			return errors.New("SABnzbd couldn't move it to the top: jobs with a higher priority are ahead of it")
		}
	}
	return nil
}

// removeCompletedFolder deletes a completed Usenet job's files. SABnzbd and
// NZBGet put them in <completed folder>/<category>/<job>, and history's
// storage is that folder or, for one file, sometimes the file itself (SABnzbd
// only). Only the job's own folder (the one inside the category folder) is
// ever deleted: never the category folder or anything above it, and nothing
// outside a folder named after the category.
func removeCompletedFolder(storage, category string) error {
	p := filepath.Clean(storage)
	if !filepath.IsAbs(p) {
		return fmt.Errorf("the download client's folder %q isn't a full path; left alone", storage)
	}
	parts := strings.Split(p, string(filepath.Separator))
	i := slices.Index(parts, category)
	if i < 0 || i == len(parts)-1 {
		return fmt.Errorf("the download client's folder %s isn't inside a %s folder, so Reelkeeper left it alone", storage, category)
	}
	job := string(filepath.Separator) + filepath.Join(parts[:i+2]...)
	if err := os.RemoveAll(job); err != nil {
		return fmt.Errorf("couldn't delete SABnzbd's finished download %s: %w", job, err)
	}
	return nil
}

// parseFloat reads one of SABnzbd's numbers, which it sends as strings.
func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// parseClock reads SABnzbd's time left ("0:03:09", or "1:02:03:04" with days).
func parseClock(s string) time.Duration {
	var d time.Duration
	parts := strings.Split(s, ":")
	units := []time.Duration{time.Second, time.Minute, time.Hour, 24 * time.Hour}
	if len(parts) > len(units) {
		return 0
	}
	for i, p := range slices.Backward(parts) {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0
		}
		d += time.Duration(n) * units[len(parts)-1-i]
	}
	return d
}
