package download

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saltglass/reelkeeper/internal/torrent"
)

// Deluge is a client for Deluge 2.x, over its web UI's JSON-RPC (POST /json).
//
// Reelkeeper talks to Deluge's web UI, which talks to the Deluge daemon. The
// web UI logs in with a password (Deluge has no usernames there) and hands out
// a session cookie. It also has to be connected to a daemon; if it isn't, and
// exactly one is configured, Reelkeeper connects it (connect).
//
// Deluge answers every call with HTTP 200. A problem comes as an "error"
// object in the body, so every call checks for one. Error code 1 means the
// session is gone: Reelkeeper logs in again and repeats the call.
//
// Deluge has no categories. Reelkeeper's category (reelkeeper-movies or
// reelkeeper-tv) is a label, from Deluge's Label plugin, which has to be
// turned on. A torrent has one label and no tags. A torrent is Reelkeeper's in
// a category only if it carries that label. Every call that changes a torrent
// checks that first, so another app's torrents are never listed, changed or
// removed.
type Deluge struct {
	URL      string // Deluge's web UI address, e.g. http://192.168.1.20:8112
	Password string

	once   sync.Once
	client *http.Client

	// mu keeps one login (and daemon connection) going at a time, and guards
	// the rest.
	mu sync.Mutex
	// ready is set once the session is logged in and connected to a daemon.
	ready bool
	id    int
	// savePaths are the folders EnsureCategory was told about, by category.
	savePaths map[string]string
	// rechecks are the checks Recheck started that haven't finished (see
	// Recheck and checkingAfterRecheck), by hash.
	rechecks map[string]recheck
	// now is the time (tests swap it).
	now func() time.Time
}

// minDelugeMajor and minDelugeMinor are the oldest Deluge Reelkeeper supports:
// 2.1, the first with the web UI answers this client reads.
const (
	minDelugeMajor = 2
	minDelugeMinor = 1
)

func (d *Deluge) http() *http.Client {
	d.once.Do(func() {
		jar, _ := cookiejar.New(nil)
		d.client = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	})
	return d.client
}

// endpoint is the JSON-RPC address. Deluge's web UI serves it at /json, so a
// bare address (the way people copy it from a browser) gets that added.
func (d *Deluge) endpoint() string {
	base := strings.TrimRight(d.URL, "/")
	if strings.HasSuffix(base, "/json") {
		return base
	}
	return base + "/json"
}

func delugeReach(err error) error {
	return &unreachableError{msg: "can't reach Deluge: " + err.Error(), err: err}
}

// rpcError is the "error" object Deluge answers a call with.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// notAuthenticated is Deluge's code 1: the session is missing or expired.
const notAuthenticated = 1

// unknownMethod says whether Deluge answered that it doesn't have the method:
// what a daemon call gets while the web UI isn't connected to a daemon, or a
// Label call while the plugin is off.
func (e *rpcError) unknownMethod() bool {
	return strings.Contains(e.Message, "Unknown method")
}

// text is the error's message without the Python traceback Deluge wraps it
// in: "Failure: [Failure instance: ... <class 'deluge.error.WrappedException'>:
// Unknown Label\nTraceback ..." becomes "Unknown Label".
func (e *rpcError) text() string {
	msg := e.Message
	if i := strings.Index(msg, ">: "); i >= 0 && strings.Contains(msg[:i], "<class ") {
		msg = msg[i+3:]
	}
	msg, _, _ = strings.Cut(msg, "\n")
	msg = strings.TrimSpace(msg)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	if msg == "" {
		return "error code " + strconv.Itoa(e.Code)
	}
	return msg
}

// rpc sends one call and returns its "result". A problem Deluge reports comes
// back as the *rpcError; anything else (the address, the network, an answer
// that isn't Deluge's) is the error.
func (d *Deluge) rpc(ctx context.Context, method string, params ...any) (json.RawMessage, *rpcError, error) {
	if params == nil {
		params = []any{}
	}
	d.mu.Lock()
	d.id++
	id := d.id
	d.mu.Unlock()
	body, err := json.Marshal(map[string]any{"method": method, "params": params, "id": id})
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http().Do(req)
	if err != nil {
		return nil, nil, delugeReach(err)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	_ = resp.Body.Close()
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(b))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, nil, fmt.Errorf("Deluge answered %d %s (is %s Deluge's web UI address?)", resp.StatusCode, msg, d.URL) //nolint:staticcheck // a proper name
	}
	var ans struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(b, &ans); err != nil {
		return nil, nil, fmt.Errorf("Deluge's answer didn't make sense (is %s Deluge's web UI address?)", d.URL) //nolint:staticcheck // a proper name
	}
	return ans.Result, ans.Error, nil
}

// errNotConnected is what Reelkeeper says when Deluge's web UI has no daemon
// and Reelkeeper can't choose one.
var errNotConnected = errors.New("Deluge's web UI isn't connected to a daemon: open it and connect once") //nolint:staticcheck // a proper name

// establish logs in and makes sure the web UI is connected to its daemon.
// Callers hold no lock; it takes one.
func (d *Deluge) establish(ctx context.Context) error {
	d.mu.Lock()
	ready := d.ready
	d.mu.Unlock()
	if ready {
		return nil
	}
	if err := d.login(ctx); err != nil {
		return err
	}
	if err := d.connect(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	d.ready = true
	d.mu.Unlock()
	return nil
}

// forget drops the session, so the next call logs in again.
func (d *Deluge) forget() {
	d.mu.Lock()
	d.ready = false
	d.mu.Unlock()
}

// login starts a session. Deluge answers true or false.
func (d *Deluge) login(ctx context.Context) error {
	res, rerr, err := d.rpc(ctx, "auth.login", d.Password)
	if err != nil {
		return err
	}
	if rerr != nil {
		return fmt.Errorf("Deluge couldn't log Reelkeeper in: %s", rerr.text()) //nolint:staticcheck // a proper name
	}
	var ok bool
	if json.Unmarshal(res, &ok) != nil || !ok {
		return passwordError{}
	}
	return nil
}

// passwordError is "wrong password" for Deluge, whose web UI has no username.
// It matches ErrLogin, like the other clients' login errors.
type passwordError struct{}

func (passwordError) Error() string        { return "Deluge refused the password" }
func (passwordError) Is(target error) bool { return target == ErrLogin }

// connected asks whether the web UI is connected to a daemon.
func (d *Deluge) connected(ctx context.Context) (bool, error) {
	res, rerr, err := d.rpc(ctx, "web.connected")
	if err != nil {
		return false, err
	}
	if rerr != nil {
		return false, fmt.Errorf("Deluge couldn't say whether its web UI is connected to a daemon: %s", rerr.text()) //nolint:staticcheck // a proper name
	}
	var ok bool
	if err := json.Unmarshal(res, &ok); err != nil {
		return false, errors.New("Deluge's answer to web.connected didn't make sense") //nolint:staticcheck // a proper name
	}
	return ok, nil
}

// connect connects the web UI to its daemon when it isn't already, if it has
// exactly one configured. A web UI connected to nothing answers every daemon
// call with "Unknown method", or not at all.
func (d *Deluge) connect(ctx context.Context) error {
	if ok, err := d.connected(ctx); err != nil || ok {
		return err
	}
	res, rerr, err := d.rpc(ctx, "web.get_hosts")
	if err != nil {
		return err
	}
	var hosts [][]any
	if rerr != nil || json.Unmarshal(res, &hosts) != nil || len(hosts) != 1 || len(hosts[0]) == 0 {
		return errNotConnected
	}
	id, _ := hosts[0][0].(string)
	if id == "" {
		return errNotConnected
	}
	if _, rerr, err := d.rpc(ctx, "web.connect", id); err != nil {
		return err
	} else if rerr != nil {
		return errNotConnected
	}
	// A host id that doesn't work fails without saying so: ask again.
	if ok, err := d.connected(ctx); err != nil {
		return err
	} else if !ok {
		return errNotConnected
	}
	return nil
}

// call sends one call, after making sure the session is logged in and
// connected, and decodes its result into out (when out isn't nil). what says,
// in plain words, what the call was for, such as "list torrents": it's used
// in the error message.
//
// A session that has gone (code 1), or a daemon call the web UI no longer
// answers, logs in and connects again, and the call is sent again, twice at
// most.
func (d *Deluge) call(ctx context.Context, what, method string, out any, params ...any) error {
	for attempt := 0; ; attempt++ {
		if err := d.establish(ctx); err != nil {
			return err
		}
		res, rerr, err := d.rpc(ctx, method, params...)
		if err != nil {
			// A web UI that has lost its daemon can leave a daemon call
			// unanswered until it times out: log in and connect afresh next time.
			d.forget()
			return err
		}
		if rerr != nil {
			if attempt < 2 && (rerr.Code == notAuthenticated || (rerr.unknownMethod() && !strings.HasPrefix(method, "web."))) {
				d.forget()
				continue
			}
			if rerr.unknownMethod() && strings.HasPrefix(method, "label.") {
				return fmt.Errorf("Deluge couldn't %s: its Label plugin seems to be off", what) //nolint:staticcheck // a proper name
			}
			return &delugeError{what: what, msg: rerr.text(), code: rerr.Code}
		}
		if out == nil || len(res) == 0 || string(res) == "null" {
			return nil
		}
		if err := json.Unmarshal(res, out); err != nil {
			return fmt.Errorf("Deluge's answer to %s didn't make sense: %w", what, err) //nolint:staticcheck // a proper name
		}
		return nil
	}
}

// delugeError is a call Deluge refused.
type delugeError struct {
	what, msg string
	code      int
}

func (e *delugeError) Error() string {
	return "Deluge couldn't " + e.what + ": " + e.msg
}

// Name is what messages call this client.
func (d *Deluge) Name() string { return "Deluge" }

// Version returns Deluge's version, such as "2.2.0". It is the connection
// test: it logs in, connects the web UI to its daemon if that is needed, and
// refuses a Deluge older than 2.1 or one with the Label plugin off, since
// Reelkeeper keeps its downloads apart with labels.
func (d *Deluge) Version(ctx context.Context) (string, error) {
	var v string
	if err := d.call(ctx, "say its version", "daemon.get_version", &v); err != nil {
		return "", err
	}
	var major, minor int
	parts := strings.SplitN(v, ".", 3)
	if len(parts) >= 2 {
		major, _ = strconv.Atoi(parts[0])
		minor, _ = strconv.Atoi(parts[1])
	}
	if major < minDelugeMajor || (major == minDelugeMajor && minor < minDelugeMinor) {
		return "", fmt.Errorf("Deluge %s is too old: Reelkeeper needs 2.1 or newer", v) //nolint:staticcheck // a proper name
	}
	var plugins []string
	if err := d.call(ctx, "list its plugins", "core.get_enabled_plugins", &plugins); err != nil {
		return "", err
	}
	if !slices.Contains(plugins, "Label") {
		return "", errors.New("Deluge's Label plugin is turned off: Reelkeeper needs it to keep its torrents apart from yours. Turn it on in Deluge (Preferences, Plugins, Label)") //nolint:staticcheck // a proper name
	}
	return v, nil
}

// EnsureCategory makes sure the label exists in Deluge's Label plugin, and
// remembers the folder for the category, for Add to use when it isn't given
// one. (qBittorrent's category carries its folder; Deluge's label has none.)
// A folder remembered earlier is kept.
func (d *Deluge) EnsureCategory(ctx context.Context, name, savePath string) error {
	if savePath != "" {
		d.mu.Lock()
		if d.savePaths == nil {
			d.savePaths = map[string]string{}
		}
		if _, ok := d.savePaths[name]; !ok {
			d.savePaths[name] = savePath
		}
		d.mu.Unlock()
	}
	return d.ensureLabel(ctx, name)
}

// ensureLabel adds the label when Deluge doesn't have it.
func (d *Deluge) ensureLabel(ctx context.Context, name string) error {
	var labels []string
	if err := d.call(ctx, "list its labels", "label.get_labels", &labels); err != nil {
		return err
	}
	if slices.Contains(labels, name) {
		return nil
	}
	err := d.call(ctx, "add the label "+name, "label.add", nil, name)
	var de *delugeError
	if errors.As(err, &de) && strings.Contains(strings.ToLower(de.msg), "already exists") {
		return nil // added by someone a moment ago
	}
	return err
}

// delugeTorrent is the part of Deluge's torrent status Reelkeeper reads.
type delugeTorrent struct {
	Name         string  `json:"name"`
	Label        string  `json:"label"`
	State        string  `json:"state"`
	Paused       bool    `json:"paused"`
	Message      string  `json:"message"`
	IsFinished   bool    `json:"is_finished"`
	Private      bool    `json:"private"`
	Progress     float64 `json:"progress"` // 0 to 100
	TotalWanted  int64   `json:"total_wanted"`
	TotalDone    int64   `json:"total_done"`
	Remaining    int64   `json:"total_remaining"`
	DownRate     int64   `json:"download_payload_rate"`
	ETA          float64 `json:"eta"`
	SavePath     string  `json:"save_path"`
	Ratio        float64 `json:"ratio"`
	SeedingTime  float64 `json:"seeding_time"`
	TimeAdded    float64 `json:"time_added"`
	CompletedAt  float64 `json:"completed_time"`
	NumSeeds     int     `json:"num_seeds"`
	NumPeers     int     `json:"num_peers"`
	TotalSeeds   int     `json:"total_seeds"`
	TotalUpload  int64   `json:"total_uploaded"`
	StopAtRatio  bool    `json:"stop_at_ratio"`
	RemoveRatio  bool    `json:"remove_at_ratio"`
	MoveComplete bool    `json:"move_completed"`
	Files        []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	} `json:"files"`
	Trackers []struct {
		Message   string `json:"message"`
		Endpoints []struct {
			Message string `json:"message"`
		} `json:"endpoints"`
	} `json:"trackers"`
	TrackerStatus string `json:"tracker_status"`
}

// delugeTorrentFields are the fields Torrents asks for: only what it uses.
var delugeTorrentFields = []string{
	"name", "label", "state", "paused", "message", "is_finished", "progress", "total_wanted", "total_done",
	"total_remaining", "download_payload_rate", "eta", "save_path", "ratio", "seeding_time", "time_added",
	"completed_time", "num_seeds", "num_peers", "total_seeds", "total_uploaded",
}

// get asks for torrents: the ones with these hashes, or all that match the
// filter when ids is nil. It returns them by hash. A hash Deluge doesn't have
// is left out.
func (d *Deluge) get(ctx context.Context, filter map[string]any, fields []string) (map[string]delugeTorrent, error) {
	if filter == nil {
		filter = map[string]any{}
	}
	var out map[string]delugeTorrent
	if err := d.call(ctx, "list torrents", "core.get_torrents_status", &out, filter, fields); err != nil {
		return nil, err
	}
	return out, nil
}

// byIDs asks for these torrents.
func (d *Deluge) byIDs(ctx context.Context, hashes []string, fields []string) (map[string]delugeTorrent, error) {
	ids := make([]string, len(hashes))
	for i, h := range hashes {
		ids[i] = strings.ToLower(h)
	}
	return d.get(ctx, map[string]any{"id": ids}, fields)
}

// Torrents lists the torrents with the category's label. category must not be
// empty: Reelkeeper never lists other apps' torrents.
func (d *Deluge) Torrents(ctx context.Context, category string) ([]Torrent, error) {
	if category == "" {
		return nil, errors.New("a category is required")
	}
	raw, err := d.get(ctx, map[string]any{"label": category}, delugeTorrentFields)
	if err != nil {
		return nil, err
	}
	out := make([]Torrent, 0, len(raw))
	for hash, r := range raw {
		if r.Label != category {
			continue
		}
		hash = strings.ToLower(hash)
		t := r.torrent(hash, category)
		if st, ok := d.checkingAfterRecheck(hash, r); ok {
			t.State = st
		}
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b Torrent) int { return strings.Compare(a.Hash, b.Hash) })
	return out, nil
}

// torrent turns Deluge's status into Reelkeeper's Torrent.
func (r delugeTorrent) torrent(hash, category string) Torrent {
	tr := Torrent{
		Hash: hash, Name: r.Name, Category: category, State: delugeState(r),
		Progress: r.Progress / 100, Size: r.TotalWanted, Left: r.Remaining, Speed: r.DownRate,
		SavePath: r.SavePath, ContentPath: path.Join(r.SavePath, r.Name),
		SeedingTime: time.Duration(r.SeedingTime) * time.Second, AddedOn: time.Unix(int64(r.TimeAdded), 0).UTC(),
		Seeds: r.NumSeeds, Peers: r.NumPeers, Verified: r.TotalDone, Uploaded: r.TotalUpload,
	}
	if r.delugeDone() {
		tr.Progress = 1
	}
	// A torrent in error says nothing useful about how far it got, and must
	// never count as finished (it would be imported).
	if r.State == "Error" {
		tr.Message = r.Message
		tr.Progress = min(tr.Progress, math.Nextafter(1, 0))
	}
	// Deluge says 0 for "not known" as well as for "finished".
	if r.ETA > 0 {
		tr.ETA = time.Duration(r.ETA) * time.Second
	}
	// Deluge says -1 for a ratio it can't work out yet.
	if r.Ratio > 0 {
		tr.Ratio = r.Ratio
	}
	if r.CompletedAt > 0 {
		c := time.Unix(int64(r.CompletedAt), 0).UTC()
		tr.CompletedOn = &c
	}
	// How many seeds the tracker says there are (-1 means it hasn't said).
	tr.SwarmSeeds = max(r.TotalSeeds, 0)
	return tr
}

// delugeDone says whether Deluge has all the torrent's data.
func (r delugeTorrent) delugeDone() bool {
	return (r.IsFinished && r.TotalWanted > 0) || r.Progress >= 100
}

// delugeState translates a Deluge torrent's state into Reelkeeper's own
// states. Each mapping means what the matching one in qbitState means:
//
//   - Error is Error, like qBittorrent's "error" (its message tells why).
//   - Checking, and Allocating (which has never been seen, but means the
//     client is preparing its files): Checking.
//   - Moving: Moving, like "moving".
//   - Queued (waiting in the client's queue): Queued when it hasn't finished,
//     Seeding when it has, like "queuedDL" and "queuedUP".
//   - Paused: SeedingPaused when it had finished, Paused when not, like
//     "pausedUP" and "pausedDL".
//   - Downloading: Metadata while it is still getting the torrent's details
//     (a magnet link has no files yet, so nothing is wanted), Stalled when
//     no data is arriving ("stalledDL"), otherwise Downloading. Deluge has no
//     stalled state of its own.
//   - Seeding: Seeding.
//
// Anything else is Unknown, which no rule acts on.
func delugeState(r delugeTorrent) torrent.State {
	done := r.delugeDone()
	switch r.State {
	case "Error":
		return torrent.Error
	case "Checking", "Allocating":
		return torrent.Checking
	case "Moving":
		return torrent.Moving
	case "Queued":
		if done {
			return torrent.Seeding
		}
		return torrent.Queued
	case "Paused":
		if done {
			return torrent.SeedingPaused
		}
		return torrent.Paused
	case "Downloading":
		switch {
		case r.TotalWanted == 0:
			return torrent.Metadata
		case r.DownRate == 0:
			return torrent.Stalled
		}
		return torrent.Downloading
	case "Seeding":
		return torrent.Seeding
	}
	return torrent.Unknown
}

// recheck is a check Recheck started, tracked until Deluge is done with it.
type recheck struct {
	at time.Time
	// seen is set once the check has been seen going on.
	seen bool
}

// recheckGrace is how long Torrents reports a torrent as checking after
// Recheck, if it never sees the check go on: a check of a tiny torrent may be
// over between two looks.
const recheckGrace = 3 * time.Second

func (d *Deluge) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// checkingAfterRecheck says what a torrent Recheck was called for is doing,
// when its check has not finished. Deluge's states can't say it:
//
//   - force_recheck on a paused torrent resumes it for the length of the
//     check and pauses it again after ("paused" is false meanwhile), and the
//     "state" lags behind: "Paused" at first, then "Checking", often
//     "Seeding" or "Downloading" for a moment, and "Checking" may never be
//     seen at all.
//   - The check is over only when the state is "Paused" AND paused is true.
//
// So a torrent with a check under way is Checking until both are true (once
// the check has been seen, or recheckGrace has passed). Then it is tracked no
// more, and its state is read as usual (verified bytes: total_done). ok is
// false when there is nothing to say.
func (d *Deluge) checkingAfterRecheck(hash string, r delugeTorrent) (torrent.State, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rc, ok := d.rechecks[hash]
	if !ok {
		return "", false
	}
	if r.State == "Error" {
		delete(d.rechecks, hash)
		return "", false
	}
	if r.State == "Checking" || !r.Paused {
		rc.seen = true
		d.rechecks[hash] = rc
		return torrent.Checking, true
	}
	if r.State == "Paused" && (rc.seen || d.clock().Sub(rc.at) >= recheckGrace) {
		delete(d.rechecks, hash)
		return "", false
	}
	return torrent.Checking, true
}

// Add adds a torrent and returns its info hash. If Deluge already has it with
// the category's label, that counts as added; without the label (some other
// app's download) it's an error, and nothing changes.
//
// The torrent is added paused, labelled, and given its options before it
// starts (unless the caller wants it stopped): a new torrent copies the
// user's global Deluge settings, and a global "remove at ratio" would remove
// Reelkeeper's torrent from Deluge. See lock for the order.
func (d *Deluge) Add(ctx context.Context, a Add) (string, error) {
	var hash string
	var err error
	switch {
	case len(a.Torrent) > 0:
		hash, err = InfoHash(a.Torrent)
	case a.Magnet != "":
		hash, err = MagnetHash(a.Magnet)
	default:
		err = errors.New("nothing to add: no magnet link or torrent file")
	}
	if err != nil {
		return "", err
	}
	if has, err := d.alreadyHas(ctx, hash, a.Category); err != nil || has {
		return hash, err
	}

	d.mu.Lock()
	dir := d.savePaths[a.Category]
	d.mu.Unlock()
	if a.SavePath != "" {
		dir = a.SavePath
	}
	if a.FilesIn != "" {
		// A torrent moving here: Deluge checks the folder its files are in
		// now; SetFolder then moves them to the final folder.
		dir = a.FilesIn
	}
	opts := map[string]any{"add_paused": true, "stop_at_ratio": false, "remove_at_ratio": false, "move_completed": false}
	if dir != "" {
		opts["download_location"] = dir
	}
	var added string
	if len(a.Torrent) > 0 {
		err = d.call(ctx, "add the torrent", "core.add_torrent_file", &added, "reelkeeper.torrent", base64.StdEncoding.EncodeToString(a.Torrent), opts)
	} else {
		err = d.call(ctx, "add the torrent", "core.add_torrent_magnet", &added, a.Magnet, opts)
	}
	var de *delugeError
	if errors.As(err, &de) && strings.Contains(de.msg, "already in session") {
		// Added by someone else a moment ago: the same check as before.
		_, err := d.alreadyHas(ctx, hash, a.Category)
		return hash, err
	}
	if err != nil {
		return "", err
	}
	if added = strings.ToLower(added); added != "" {
		hash = added
	}
	// It is Reelkeeper's own, just added: if it can't be labelled or set up it
	// is removed again (nothing was downloaded; it is paused).
	if err := d.lock(ctx, a.Category, hash); err != nil {
		_ = d.call(ctx, "remove the torrent", "core.remove_torrent", nil, hash, false)
		return "", err
	}
	if !a.Stopped {
		if err := d.call(ctx, "start the torrent", "core.resume_torrents", nil, []string{hash}); err != nil {
			return "", err
		}
	}
	return hash, nil
}

// lock gives a torrent the category's label and makes sure no seeding limit or
// "move completed" of Deluge's applies to it, and checks both took. The order
// matters: the Label plugin applies the label's own options when a label is
// set, so the label goes first and Reelkeeper's options after it. Deluge
// ignores option names it doesn't know, so the options are read back.
func (d *Deluge) lock(ctx context.Context, category, hash string) error {
	if err := d.call(ctx, "label the torrent "+category, "label.set_torrent", nil, hash, category); err != nil {
		return err
	}
	return d.setOptions(ctx, category, []string{hash})
}

// setOptions turns off Deluge's own ratio limits and "move completed" on
// these torrents, and reads the options back.
func (d *Deluge) setOptions(ctx context.Context, category string, hashes []string) error {
	err := d.call(ctx, "change the torrents' seeding limits", "core.set_torrent_options", nil,
		hashes, map[string]any{"stop_at_ratio": false, "remove_at_ratio": false, "move_completed": false})
	if err != nil {
		return err
	}
	got, err := d.byIDs(ctx, hashes, []string{"label", "stop_at_ratio", "remove_at_ratio", "move_completed"})
	if err != nil {
		return err
	}
	for _, h := range hashes {
		r, ok := got[strings.ToLower(h)]
		switch {
		case !ok:
			return fmt.Errorf("Deluge doesn't list torrent %s after it was set up", h) //nolint:staticcheck // a proper name
		case r.Label != category:
			return fmt.Errorf("Deluge didn't keep the label %s on torrent %s", category, h) //nolint:staticcheck // a proper name
		case r.StopAtRatio || r.RemoveRatio || r.MoveComplete:
			return fmt.Errorf("Deluge didn't turn off its seeding limits and \"move completed\" on torrent %s", h) //nolint:staticcheck // a proper name
		}
	}
	return nil
}

// alreadyHas says whether Deluge has the torrent already, as one of
// Reelkeeper's in the category. A torrent it has without the label is an
// error, so Reelkeeper leaves it alone.
func (d *Deluge) alreadyHas(ctx context.Context, hash, category string) (bool, error) {
	existing, err := d.byIDs(ctx, []string{hash}, []string{"label"})
	if err != nil {
		return false, err
	}
	r, ok := existing[strings.ToLower(hash)]
	if !ok {
		return false, nil
	}
	if r.Label == category {
		return true, nil
	}
	return false, fmt.Errorf("Deluge already has this torrent, without the label %q, so Reelkeeper leaves it alone", category)
}

// ours checks that every hash has the category's label, so Reelkeeper never
// changes another app's torrent. It returns what it read of each, by hash.
func (d *Deluge) ours(ctx context.Context, category string, hashes []string, fields ...string) (map[string]delugeTorrent, error) {
	got, err := d.byIDs(ctx, hashes, append([]string{"label"}, fields...))
	if err != nil {
		return nil, err
	}
	for _, h := range hashes {
		r, ok := got[strings.ToLower(h)]
		switch {
		case !ok:
			return nil, fmt.Errorf("Deluge doesn't list torrent %s", h) //nolint:staticcheck // a proper name
		case r.Label != category:
			return nil, fmt.Errorf("torrent %s doesn't have Reelkeeper's label %s, so Reelkeeper won't change it", h, category)
		}
	}
	return got, nil
}

// act runs a method on these torrents, after checking they're Reelkeeper's.
// The method takes the list of ids.
func (d *Deluge) act(ctx context.Context, category string, hashes []string, what, method string) error {
	if len(hashes) == 0 {
		return nil
	}
	if _, err := d.ours(ctx, category, hashes); err != nil {
		return err
	}
	return d.call(ctx, what, method, nil, lowerAll(hashes))
}

func lowerAll(hashes []string) []string {
	out := make([]string, len(hashes))
	for i, h := range hashes {
		out[i] = strings.ToLower(h)
	}
	return out
}

// Remove deletes torrents, and their downloaded files when deleteFiles is
// set. Only hashes with Reelkeeper's label may be passed; callers check
// that, and Remove checks it again.
func (d *Deluge) Remove(ctx context.Context, category string, hashes []string, deleteFiles bool) error {
	if len(hashes) == 0 {
		return nil
	}
	if _, err := d.ours(ctx, category, hashes); err != nil {
		return err
	}
	for _, h := range lowerAll(hashes) {
		if err := d.call(ctx, "remove the torrent", "core.remove_torrent", nil, h, deleteFiles); err != nil {
			return err
		}
	}
	return nil
}

// MoveCategory gives torrents the label to in place of from, without moving
// their files. Only torrents with the from label are touched.
func (d *Deluge) MoveCategory(ctx context.Context, from, to string, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	if _, err := d.ours(ctx, from, hashes); err != nil {
		return err
	}
	if err := d.ensureLabel(ctx, to); err != nil {
		return err
	}
	for _, h := range lowerAll(hashes) {
		if err := d.lock(ctx, to, h); err != nil {
			return err
		}
	}
	return nil
}

// TopPriority moves these torrents to the top of Deluge's download queue. It
// only matters while Deluge's queue is limiting how many torrents are active;
// with room for all, this changes nothing. Only Reelkeeper's torrents are
// touched.
func (d *Deluge) TopPriority(ctx context.Context, category string, hashes []string) error {
	return d.act(ctx, category, hashes, "move the torrents to the top of the queue", "core.queue_top")
}

// NoShareLimits makes Deluge's seeding limits and "move completed" not apply to
// these torrents (Reelkeeper's cleanup decides when they stop), and starts any
// that those limits had already stopped. Deluge's limits are per torrent
// (stop at ratio, remove at ratio); there is no per-torrent seed-time limit,
// and the global seed-time settings stop nothing on their own. Only
// Reelkeeper's torrents are touched.
func (d *Deluge) NoShareLimits(ctx context.Context, category string, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	if _, err := d.ours(ctx, category, hashes); err != nil {
		return err
	}
	if err := d.setOptions(ctx, category, hashes); err != nil {
		return err
	}
	return d.call(ctx, "start the torrents", "core.resume_torrents", nil, lowerAll(hashes))
}

// Stop pauses these torrents. Only Reelkeeper's torrents are touched.
func (d *Deluge) Stop(ctx context.Context, category string, hashes []string) error {
	return d.act(ctx, category, hashes, "pause the torrents", "core.pause_torrents")
}

// Start resumes these torrents. Only Reelkeeper's torrents are touched.
func (d *Deluge) Start(ctx context.Context, category string, hashes []string) error {
	return d.act(ctx, category, hashes, "resume the torrents", "core.resume_torrents")
}

// TrackerMessages returns what the torrent's trackers last said, such as
// "Unregistered torrent". Only Reelkeeper's torrents are asked about.
func (d *Deluge) TrackerMessages(ctx context.Context, category, hash string) ([]string, error) {
	got, err := d.ours(ctx, category, []string{hash}, "trackers", "tracker_status")
	if err != nil {
		return nil, err
	}
	r := got[strings.ToLower(hash)]
	var out []string
	add := func(m string) {
		m = strings.TrimSpace(m)
		for _, p := range []string{"Error:", "Warning:"} {
			m = strings.TrimSpace(strings.TrimPrefix(m, p))
		}
		if m != "" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	// Deluge's one-line summary says "Announce OK" when all is well, and
	// "Error: ..." or "Warning: ..." when not.
	if s := strings.TrimSpace(r.TrackerStatus); strings.HasPrefix(s, "Error:") || strings.HasPrefix(s, "Warning:") {
		add(s)
	}
	for _, tr := range r.Trackers {
		add(tr.Message)
		for _, e := range tr.Endpoints {
			add(e.Message)
		}
	}
	return out, nil
}

// Source returns the torrent's magnet link. Deluge can't hand back a .torrent
// file through its API, and a private torrent's magnet link is no use to
// another client (its peers never send a private torrent's details), so for
// those it says so. (Every Deluge torrent was grabbed by Reelkeeper 1.4 or
// later, which keeps its own copy; this is only the fallback.)
func (d *Deluge) Source(ctx context.Context, category, hash string) (Source, error) {
	got, err := d.ours(ctx, category, []string{hash}, "private")
	if err != nil {
		return Source{}, err
	}
	if got[strings.ToLower(hash)].Private {
		return Source{}, &NoSourceError{"It is a private torrent, Deluge can't give back its .torrent file, and Reelkeeper has no copy of it."}
	}
	var magnet string
	if err := d.call(ctx, "read the torrent's magnet link", "core.get_magnet_uri", &magnet, strings.ToLower(hash)); err != nil {
		return Source{}, err
	}
	if magnet == "" {
		return Source{}, &NoSourceError{"Deluge didn't give a magnet link for this torrent."}
	}
	return Source{Magnet: magnet}, nil
}

// Layout says where Deluge keeps the torrent's files: its file list and the
// folder they are in. Deluge keeps unfinished files under their plain names,
// at full length, in that same folder (there is no "incomplete" folder unless
// "move completed" is on, which it is not for Reelkeeper's torrents), so that
// is the only place to look.
func (d *Deluge) Layout(ctx context.Context, category, hash string) (Layout, error) {
	got, err := d.ours(ctx, category, []string{hash}, "save_path", "files")
	if err != nil {
		return Layout{}, err
	}
	r := got[strings.ToLower(hash)]
	l := Layout{Folder: strings.TrimRight(r.SavePath, "/")}
	for _, f := range r.Files {
		l.Files = append(l.Files, File{Name: f.Path, Size: f.Size})
	}
	l.Look = appendFolder(l.Look, l.Folder)
	return l, nil
}

// Recheck makes Deluge verify the stopped torrent's files. Only Reelkeeper's
// torrents are touched. Deluge resumes a paused torrent for the length of the
// check and pauses it again, so the torrent is tracked until that is over
// (see checkingAfterRecheck).
func (d *Deluge) Recheck(ctx context.Context, category string, hash string) error {
	hash = strings.ToLower(hash)
	got, err := d.ours(ctx, category, []string{hash}, "paused")
	if err != nil {
		return err
	}
	if !got[hash].Paused {
		return fmt.Errorf("Deluge can only check a paused torrent, and %s isn't paused", hash)
	}
	d.mu.Lock()
	if d.rechecks == nil {
		d.rechecks = map[string]recheck{}
	}
	d.rechecks[hash] = recheck{at: d.clock()}
	d.mu.Unlock()
	if err := d.call(ctx, "check the torrent's files", "core.force_recheck", nil, []string{hash}); err != nil {
		d.mu.Lock()
		delete(d.rechecks, hash)
		d.mu.Unlock()
		return err
	}
	return nil
}

// moveStorageWait is how long SetFolder waits for Deluge to move the files.
const moveStorageWait = 30 * time.Minute

// SetFolder moves the torrent's files to the folder it belongs in, when it
// isn't there already. Deluge moves the files itself (nothing is deleted),
// partial ones included, and shows the state "Moving" while it does; this
// returns once that is over.
func (d *Deluge) SetFolder(ctx context.Context, category, hash, folder string) error {
	hash = strings.ToLower(hash)
	got, err := d.ours(ctx, category, []string{hash}, "save_path")
	if err != nil {
		return err
	}
	folder = strings.TrimRight(folder, "/")
	if strings.TrimRight(got[hash].SavePath, "/") == folder {
		return nil
	}
	if err := d.call(ctx, "move the torrent's files", "core.move_storage", nil, []string{hash}, folder); err != nil {
		return err
	}
	deadline := d.clock().Add(moveStorageWait)
	for {
		r, err := d.ours(ctx, category, []string{hash}, "save_path", "state", "message")
		if err != nil {
			return err
		}
		now := r[hash]
		if now.State != "Moving" {
			if strings.TrimRight(now.SavePath, "/") != folder {
				return fmt.Errorf("Deluge didn't move the torrent's files to %s (it says: %s)", folder, now.Message) //nolint:staticcheck // a proper name
			}
			return nil
		}
		if !d.clock().Before(deadline) {
			return fmt.Errorf("Deluge is still moving the torrent's files after %s", moveStorageWait) //nolint:staticcheck // a proper name
		}
		if err := sleep(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
}

var _ Client = (*Deluge)(nil)
