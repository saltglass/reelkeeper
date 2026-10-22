// Package download talks to the download client: qBittorrent, Transmission or Deluge.
// Client is what the rest of Reelkeeper uses, so others can be added later.
//
// Reelkeeper only ever touches torrents in its own categories (reelkeeper-movies
// and reelkeeper-tv; in Transmission and Deluge they are labels). Other apps' torrents
// are never listed, changed or removed.
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
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/saltglass/reelkeeper/internal/torrent"
)

// Torrent is one torrent in the download client.
type Torrent struct {
	Hash     string
	Name     string
	Category string
	// State is what the torrent is doing, in Reelkeeper's own words (the
	// client translates its states; see qbitState).
	State    torrent.State
	Progress float64 // 0 to 1
	Size     int64   // bytes selected for download
	Left     int64   // bytes still to download
	// Speed is the download speed in bytes per second.
	Speed int64
	// ETA is the time left; zero when unknown or finished.
	ETA         time.Duration
	SavePath    string
	ContentPath string // the file, or the folder for multi-file torrents
	Ratio       float64
	SeedingTime time.Duration
	AddedOn     time.Time
	CompletedOn *time.Time
	// Seeds is how many seeds the client is connected to right now;
	// SwarmSeeds how many the tracker last said exist (0 when unknown).
	// The tracker's count is often stale and misses seeds found through
	// DHT, so Seeds can be the larger. The indexer's seeder count, which
	// the minimum checks, is like SwarmSeeds.
	Seeds      int
	SwarmSeeds int
	Peers      int
	// Verified is the bytes the client has checked against the torrent's
	// pieces and keeps (qBittorrent's "completed", Transmission's
	// "haveValid"): what a move to another client has to match.
	Verified int64
	// Uploaded is the bytes uploaded since the torrent was added to this
	// client.
	Uploaded int64
	// Tags are the labels on the torrent besides its category: the indexer's
	// name, "requested" and so on. (Deluge has none.)
	Tags []string
	// Message is what the client says is wrong, when it reports an error (only
	// Deluge says).
	Message string
}

// Done reports whether the download has finished.
func (t Torrent) Done() bool { return t.Progress >= 1 }

// Paused reports whether someone stopped the download before it finished.
func (t Torrent) Paused() bool { return t.State == torrent.Paused }

// Stopped reports whether the torrent is stopped, finished or not.
func (t Torrent) Stopped() bool { return t.State == torrent.Paused || t.State == torrent.SeedingPaused }

// Add is a torrent to add: a magnet link or a .torrent file.
type Add struct {
	Magnet  string
	Torrent []byte
	// NZB is an .nzb file's contents, for a Usenet client (which takes
	// nothing else), and Name the name its job gets there.
	NZB  []byte
	Name string
	// Top puts a Usenet job ahead of the client's other jobs (a torrent goes
	// to the top with TopPriority after it is added).
	Top bool
	// Category puts it in one of Reelkeeper's categories.
	Category string
	// Tags are extra labels, such as the indexer's name.
	Tags []string
	// Stopped adds it without starting it.
	Stopped bool
	// SavePath is the folder to download into; empty means the client's
	// default.
	SavePath string
	// FilesIn is set only when moving a torrent from another client: the
	// folder its files are in now, when that isn't SavePath (the folder it
	// belongs in once finished). The new client then looks there for what
	// has been downloaded.
	FilesIn string
}

// ErrLogin means the username or password is wrong.
var ErrLogin = errors.New("qBittorrent refused the username or password")

// QBittorrent is a qBittorrent Web UI API (v2) client.
type QBittorrent struct {
	URL      string // e.g. http://192.168.1.20:8080
	Username string
	Password string

	once   sync.Once
	client *http.Client
	mu     sync.Mutex // one login at a time
}

func (q *QBittorrent) http() *http.Client {
	q.once.Do(func() {
		jar, _ := cookiejar.New(nil)
		q.client = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	})
	return q.client
}

func (q *QBittorrent) base() string { return strings.TrimRight(q.URL, "/") }

// login starts a session. qBittorrent refuses requests whose Referer
// doesn't match it (protection against other web pages using it), so the
// Referer is set to its own address.
func (q *QBittorrent) login(ctx context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	form := url.Values{"username": {q.Username}, "password": {q.Password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.base()+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", q.base())
	resp, err := q.http().Do(req)
	if err != nil {
		return reach(err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	// qBittorrent 5.2 answers 204 (success) or 401 (wrong password);
	// older versions answer 200 with "Ok." or "Fails.".
	switch {
	case resp.StatusCode == http.StatusNoContent:
		return nil
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrLogin
	case resp.StatusCode == http.StatusForbidden:
		return errors.New("qBittorrent has temporarily banned Reelkeeper's address after too many failed logins; wait, or restart qBittorrent")
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("qBittorrent's login answered %s", resp.Status)
	case strings.TrimSpace(string(body)) != "Ok.":
		return ErrLogin
	}
	return nil
}

func reach(err error) error {
	return &unreachableError{msg: "can't reach qBittorrent: " + err.Error(), err: err}
}

// ErrUnreachable matches the error a client gives when it can't be reached
// at all (as opposed to refusing something). A move to another client waits
// and tries again after one of these rather than giving up on the torrent.
var ErrUnreachable = errors.New("the download client can't be reached")

type unreachableError struct {
	msg string
	err error
}

func (e *unreachableError) Error() string        { return e.msg }
func (e *unreachableError) Unwrap() error        { return e.err }
func (e *unreachableError) Is(target error) bool { return target == ErrUnreachable }

// do sends a request, logging in first if the session has expired.
func (q *QBittorrent) do(ctx context.Context, method, path string, body func() (io.Reader, string)) ([]byte, int, error) {
	for attempt := 0; ; attempt++ {
		var r io.Reader
		ctype := ""
		if body != nil {
			r, ctype = body()
		}
		req, err := http.NewRequestWithContext(ctx, method, q.base()+path, r)
		if err != nil {
			return nil, 0, err
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		req.Header.Set("Referer", q.base())
		resp, err := q.http().Do(req)
		if err != nil {
			return nil, 0, reach(err)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, 0, err
		}
		if resp.StatusCode == http.StatusForbidden && attempt == 0 {
			if err := q.login(ctx); err != nil {
				return nil, 0, err
			}
			continue
		}
		return b, resp.StatusCode, nil
	}
}

func form(v url.Values) func() (io.Reader, string) {
	return func() (io.Reader, string) {
		return strings.NewReader(v.Encode()), "application/x-www-form-urlencoded"
	}
}

func expectOK(status int, body []byte, what string) error {
	if status != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("qBittorrent couldn't %s (%d %s)", what, status, msg)
	}
	return nil
}

// Name is what messages call this client.
func (q *QBittorrent) Name() string { return "qBittorrent" }

// Version logs in and returns qBittorrent's version, such as "v5.2.2". It
// is the connection test.
func (q *QBittorrent) Version(ctx context.Context) (string, error) {
	if err := q.login(ctx); err != nil {
		return "", err
	}
	b, status, err := q.do(ctx, http.MethodGet, "/api/v2/app/version", nil)
	if err != nil {
		return "", err
	}
	if err := expectOK(status, b, "say its version"); err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// EnsureCategory creates a category if it doesn't exist. savePath may be
// empty (qBittorrent's default folder). An existing category is left as
// it is: someone may have set its folder on purpose.
func (q *QBittorrent) EnsureCategory(ctx context.Context, name, savePath string) error {
	b, status, err := q.do(ctx, http.MethodGet, "/api/v2/torrents/categories", nil)
	if err != nil {
		return err
	}
	if err := expectOK(status, b, "list its categories"); err != nil {
		return err
	}
	var cats map[string]json.RawMessage
	if err := json.Unmarshal(b, &cats); err != nil {
		return fmt.Errorf("qBittorrent's category list didn't make sense: %w", err)
	}
	if _, ok := cats[name]; ok {
		return nil
	}
	b, status, err = q.do(ctx, http.MethodPost, "/api/v2/torrents/createCategory",
		form(url.Values{"category": {name}, "savePath": {savePath}}))
	if err != nil {
		return err
	}
	return expectOK(status, b, "create the category "+name)
}

// Add adds a torrent and returns its info hash. If qBittorrent already has
// it in the same category, that counts as added; in another category (some
// other app's download) it's an error, and nothing changes.
func (q *QBittorrent) Add(ctx context.Context, a Add) (string, error) {
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
	if existing, err := q.torrents(ctx, url.Values{"hashes": {hash}}); err != nil {
		return "", err
	} else if len(existing) > 0 {
		if existing[0].Category == a.Category {
			return hash, nil
		}
		return "", fmt.Errorf("qBittorrent already has this torrent, in category %q, so Reelkeeper leaves it alone", existing[0].Category)
	}

	body := func() (io.Reader, string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if len(a.Torrent) > 0 {
			fw, _ := mw.CreateFormFile("torrents", hash+".torrent")
			_, _ = fw.Write(a.Torrent)
		} else {
			_ = mw.WriteField("urls", a.Magnet)
		}
		_ = mw.WriteField("category", a.Category)
		if a.SavePath != "" {
			_ = mw.WriteField("savepath", a.SavePath)
		}
		if a.FilesIn != "" {
			// A torrent moving here: say where its files really are, so
			// qBittorrent checks them in place. Without this it may move
			// the other client's file into its own temporary folder.
			// Automatic torrent management would override the folders.
			_ = mw.WriteField("autoTMM", "false")
			if a.FilesIn == a.SavePath {
				_ = mw.WriteField("useDownloadPath", "false")
			} else {
				_ = mw.WriteField("useDownloadPath", "true")
				_ = mw.WriteField("downloadPath", a.FilesIn)
			}
		}
		if len(a.Tags) > 0 {
			_ = mw.WriteField("tags", strings.Join(a.Tags, ","))
		}
		// qBittorrent 5 calls it "stopped"; 4.x called it "paused".
		_ = mw.WriteField("stopped", fmt.Sprint(a.Stopped))
		_ = mw.WriteField("paused", fmt.Sprint(a.Stopped))
		_ = mw.Close()
		return &buf, mw.FormDataContentType()
	}
	b, status, err := q.do(ctx, http.MethodPost, "/api/v2/torrents/add", body)
	if err != nil {
		return "", err
	}
	if err := expectOK(status, b, "add the torrent"); err != nil {
		return "", err
	}
	if strings.TrimSpace(string(b)) == "Fails." {
		return "", errors.New("qBittorrent refused the torrent (it may be invalid)")
	}
	return hash, nil
}

// Torrents lists the torrents in a category. category must not be empty:
// Reelkeeper never lists other apps' torrents.
func (q *QBittorrent) Torrents(ctx context.Context, category string) ([]Torrent, error) {
	if category == "" {
		return nil, errors.New("a category is required")
	}
	return q.torrents(ctx, url.Values{"category": {category}})
}

func (q *QBittorrent) torrents(ctx context.Context, filter url.Values) ([]Torrent, error) {
	b, status, err := q.do(ctx, http.MethodGet, "/api/v2/torrents/info?"+filter.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if err := expectOK(status, b, "list torrents"); err != nil {
		return nil, err
	}
	var raw []struct {
		Hash         string  `json:"hash"`
		Name         string  `json:"name"`
		Category     string  `json:"category"`
		State        string  `json:"state"`
		Progress     float64 `json:"progress"`
		Size         int64   `json:"size"`
		AmountLeft   int64   `json:"amount_left"`
		DLSpeed      int64   `json:"dlspeed"`
		ETA          int64   `json:"eta"`
		SavePath     string  `json:"save_path"`
		ContentPath  string  `json:"content_path"`
		Ratio        float64 `json:"ratio"`
		SeedingTime  int64   `json:"seeding_time"`
		AddedOn      int64   `json:"added_on"`
		CompletionOn int64   `json:"completion_on"`
		NumSeeds     int     `json:"num_seeds"`
		NumComplete  int     `json:"num_complete"`
		NumLeechs    int     `json:"num_leechs"`
		Completed    int64   `json:"completed"`
		Uploaded     int64   `json:"uploaded"`
		Tags         string  `json:"tags"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("qBittorrent's torrent list didn't make sense: %w", err)
	}
	out := make([]Torrent, 0, len(raw))
	for _, r := range raw {
		t := Torrent{
			Hash: strings.ToLower(r.Hash), Name: r.Name, Category: r.Category, State: qbitState(r.State),
			Progress: r.Progress, Size: r.Size, Left: r.AmountLeft, Speed: r.DLSpeed,
			SavePath: r.SavePath, ContentPath: r.ContentPath, Ratio: r.Ratio,
			SeedingTime: time.Duration(r.SeedingTime) * time.Second, AddedOn: time.Unix(r.AddedOn, 0).UTC(),
			Seeds: r.NumSeeds, SwarmSeeds: max(r.NumComplete, 0), Peers: r.NumLeechs,
			Verified: r.Completed, Uploaded: r.Uploaded, Tags: splitTags(r.Tags),
		}
		// qBittorrent reports "unknown" as 8640000 seconds (100 days).
		if r.ETA > 0 && r.ETA < 8640000 {
			t.ETA = time.Duration(r.ETA) * time.Second
		}
		if r.CompletionOn > 0 {
			c := time.Unix(r.CompletionOn, 0).UTC()
			t.CompletedOn = &c
		}
		out = append(out, t)
	}
	return out, nil
}

// splitTags reads qBittorrent's comma-separated tags.
func splitTags(s string) []string {
	var out []string
	for _, tag := range strings.Split(s, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// qbitState translates one of qBittorrent's state names into Reelkeeper's own.
// qBittorrent 4.x and 5.x use different names for the same thing (pausedDL
// and stoppedDL), and both are listed. A name from some future version that
// isn't listed here is torrent.Unknown, which no rule acts on.
func qbitState(s string) torrent.State {
	switch s {
	case "downloading", "forcedDL":
		return torrent.Downloading
	case "stalledDL":
		return torrent.Stalled
	case "metaDL", "forcedMetaDL":
		return torrent.Metadata
	case "queuedDL":
		return torrent.Queued
	case "pausedDL", "stoppedDL":
		return torrent.Paused
	// Checking files before (or while) downloading or seeding. "allocating"
	// is qBittorrent reserving disk space for a new download.
	case "checkingDL", "checkingUP", "checkingResumeData", "allocating":
		return torrent.Checking
	case "moving":
		return torrent.Moving
	// A finished torrent waiting in qBittorrent's queue for its turn to seed
	// ("queuedUP") counts as seeding: nothing in Reelkeeper treats it
	// differently.
	case "uploading", "forcedUP", "queuedUP":
		return torrent.Seeding
	case "stalledUP":
		return torrent.SeedingStalled
	case "pausedUP", "stoppedUP":
		return torrent.SeedingPaused
	case "error":
		return torrent.Error
	case "missingFiles":
		return torrent.MissingFiles
	}
	return torrent.Unknown // including qBittorrent's own "unknown"
}

// Remove deletes torrents, and their downloaded files when deleteFiles is
// set. Only hashes Reelkeeper added (in its categories) may be passed; callers
// check that, and Remove checks it again.
func (q *QBittorrent) Remove(ctx context.Context, category string, hashes []string, deleteFiles bool) error {
	if len(hashes) == 0 {
		return nil
	}
	if err := q.ours(ctx, category, hashes); err != nil {
		return err
	}
	b, status, err := q.do(ctx, http.MethodPost, "/api/v2/torrents/delete",
		form(url.Values{"hashes": {strings.Join(hashes, "|")}, "deleteFiles": {fmt.Sprint(deleteFiles)}}))
	if err != nil {
		return err
	}
	return expectOK(status, b, "remove the torrent")
}

// MoveCategory moves torrents from one of Reelkeeper's categories to another
// without moving their files. Automatic torrent management is turned off for
// them first: with it on, qBittorrent relocates a torrent into its new
// category's folder. Only torrents already in from are touched.
func (q *QBittorrent) MoveCategory(ctx context.Context, from, to string, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	if err := q.ours(ctx, from, hashes); err != nil {
		return err
	}
	h := strings.Join(hashes, "|")
	b, status, err := q.do(ctx, http.MethodPost, "/api/v2/torrents/setAutoManagement",
		form(url.Values{"hashes": {h}, "enable": {"false"}}))
	if err != nil {
		return err
	}
	if err := expectOK(status, b, "turn off automatic management"); err != nil {
		return err
	}
	b, status, err = q.do(ctx, http.MethodPost, "/api/v2/torrents/setCategory",
		form(url.Values{"hashes": {h}, "category": {to}}))
	if err != nil {
		return err
	}
	return expectOK(status, b, "move the torrents to the category "+to)
}

// TopPriority moves these torrents to the top of qBittorrent's download
// queue. Only Reelkeeper's category is touched.
func (q *QBittorrent) TopPriority(ctx context.Context, category string, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	if err := q.ours(ctx, category, hashes); err != nil {
		return err
	}
	b, status, err := q.do(ctx, http.MethodPost, "/api/v2/torrents/topPrio", form(url.Values{"hashes": {strings.Join(hashes, "|")}}))
	if err != nil {
		return err
	}
	// qBittorrent answers 409 when torrent queueing is turned off: then
	// every torrent downloads at once and there's no queue to reorder.
	if status == http.StatusConflict {
		return nil
	}
	return expectOK(status, b, "move the torrents to the top of the queue")
}

// NoShareLimits makes qBittorrent's own seeding limits not apply to these
// torrents (Reelkeeper's cleanup decides when they stop), and starts any that
// those limits had already stopped. Only Reelkeeper's category is touched.
func (q *QBittorrent) NoShareLimits(ctx context.Context, category string, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	if err := q.ours(ctx, category, hashes); err != nil {
		return err
	}
	joined := strings.Join(hashes, "|")
	b, status, err := q.do(ctx, http.MethodPost, "/api/v2/torrents/setShareLimits", form(url.Values{
		"hashes": {joined}, "ratioLimit": {"-1"}, "seedingTimeLimit": {"-1"}, "inactiveSeedingTimeLimit": {"-1"},
		// Required by qBittorrent 5.2 (older versions ignore it); with no
		// limits it never applies.
		"shareLimitAction": {"Default"},
	}))
	if err != nil {
		return err
	}
	if err := expectOK(status, b, "change the torrents' seeding limits"); err != nil {
		return err
	}
	return q.post5or4(ctx, "/api/v2/torrents/start", "/api/v2/torrents/resume", joined, "start the torrents")
}

// Stop pauses these torrents. Only Reelkeeper's category is touched.
func (q *QBittorrent) Stop(ctx context.Context, category string, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	if err := q.ours(ctx, category, hashes); err != nil {
		return err
	}
	return q.post5or4(ctx, "/api/v2/torrents/stop", "/api/v2/torrents/pause", strings.Join(hashes, "|"), "pause the torrents")
}

// Start resumes these torrents. Only Reelkeeper's category is touched.
func (q *QBittorrent) Start(ctx context.Context, category string, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	if err := q.ours(ctx, category, hashes); err != nil {
		return err
	}
	return q.post5or4(ctx, "/api/v2/torrents/start", "/api/v2/torrents/resume", strings.Join(hashes, "|"), "resume the torrents")
}

// post5or4 sends the hashes to qBittorrent 5's endpoint, and to 4.x's
// when 5's doesn't exist (it renamed pause/resume to stop/start). The
// caller has already checked the torrents are Reelkeeper's.
func (q *QBittorrent) post5or4(ctx context.Context, v5, v4, joinedHashes, what string) error {
	b, status, err := q.do(ctx, http.MethodPost, v5, form(url.Values{"hashes": {joinedHashes}}))
	if err == nil && status == http.StatusNotFound {
		b, status, err = q.do(ctx, http.MethodPost, v4, form(url.Values{"hashes": {joinedHashes}}))
	}
	if err != nil {
		return err
	}
	return expectOK(status, b, what)
}

// TrackerMessages returns what the torrent's trackers last said, such as
// "Unregistered torrent". Only Reelkeeper's category is asked about.
func (q *QBittorrent) TrackerMessages(ctx context.Context, category, hash string) ([]string, error) {
	if err := q.ours(ctx, category, []string{hash}); err != nil {
		return nil, err
	}
	b, status, err := q.do(ctx, http.MethodGet, "/api/v2/torrents/trackers?"+url.Values{"hash": {hash}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if err := expectOK(status, b, "list the torrent's trackers"); err != nil {
		return nil, err
	}
	var raw []struct {
		URL string `json:"url"`
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("qBittorrent's tracker list didn't make sense: %w", err)
	}
	var out []string
	for _, t := range raw {
		// qBittorrent lists DHT, PeX and LSD as pseudo-trackers.
		if strings.HasPrefix(t.URL, "** [") || strings.TrimSpace(t.Msg) == "" {
			continue
		}
		out = append(out, t.Msg)
	}
	return out, nil
}

// ours checks that every hash is in the category, so Reelkeeper never changes
// another app's torrent.
func (q *QBittorrent) ours(ctx context.Context, category string, hashes []string) error {
	ts, err := q.Torrents(ctx, category)
	if err != nil {
		return err
	}
	in := map[string]bool{}
	for _, t := range ts {
		in[t.Hash] = true
	}
	for _, h := range hashes {
		if !in[strings.ToLower(h)] {
			return fmt.Errorf("torrent %s isn't in Reelkeeper's category %s, so Reelkeeper won't change it", h, category)
		}
	}
	return nil
}

// Source returns the torrent's .torrent file (qBittorrent 4.5 and newer
// export it, even part-way through a download), or its magnet link when it
// can't: a torrent that is still fetching its details has no file yet.
// The infohash is the same either way.
func (q *QBittorrent) Source(ctx context.Context, category, hash string) (Source, error) {
	if err := q.ours(ctx, category, []string{hash}); err != nil {
		return Source{}, err
	}
	b, status, err := q.do(ctx, http.MethodGet, "/api/v2/torrents/export?"+url.Values{"hash": {hash}}.Encode(), nil)
	if err != nil {
		return Source{}, err
	}
	switch {
	case status == http.StatusOK && len(b) > 0:
		if h, err := InfoHash(b); err != nil || h != strings.ToLower(hash) {
			return Source{}, &NoSourceError{"qBittorrent gave back a file that isn't this torrent's."}
		}
		return Source{Torrent: b}, nil
	case status == http.StatusConflict, status == http.StatusNotFound:
		// 409: no details yet. 404: a qBittorrent older than 4.5, which can't export.
	default:
		return Source{}, expectOK(status, b, "export the torrent")
	}
	b, status, err = q.do(ctx, http.MethodGet, "/api/v2/torrents/info?"+url.Values{"hashes": {hash}}.Encode(), nil)
	if err != nil {
		return Source{}, err
	}
	if err := expectOK(status, b, "list the torrent"); err != nil {
		return Source{}, err
	}
	var raw []struct {
		Magnet string `json:"magnet_uri"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || len(raw) == 0 {
		return Source{}, &NoSourceError{"qBittorrent doesn't list this torrent."}
	}
	if raw[0].Magnet != "" {
		return Source{Magnet: raw[0].Magnet}, nil
	}
	return Source{Magnet: "magnet:?xt=urn:btih:" + strings.ToLower(hash) + "&dn=" + url.QueryEscape(raw[0].Name)}, nil
}

// Layout says where qBittorrent keeps the torrent's files: its file list,
// the folder it belongs in, and the folders its files may be in now (the
// torrent's own, and the temporary folder for unfinished downloads).
func (q *QBittorrent) Layout(ctx context.Context, category, hash string) (Layout, error) {
	if err := q.ours(ctx, category, []string{hash}); err != nil {
		return Layout{}, err
	}
	b, status, err := q.do(ctx, http.MethodGet, "/api/v2/torrents/info?"+url.Values{"hashes": {hash}}.Encode(), nil)
	if err != nil {
		return Layout{}, err
	}
	if err := expectOK(status, b, "list the torrent"); err != nil {
		return Layout{}, err
	}
	var info []struct {
		SavePath     string `json:"save_path"`
		DownloadPath string `json:"download_path"`
		ContentPath  string `json:"content_path"`
	}
	if err := json.Unmarshal(b, &info); err != nil || len(info) == 0 {
		return Layout{}, errors.New("qBittorrent doesn't list this torrent")
	}
	b, status, err = q.do(ctx, http.MethodGet, "/api/v2/torrents/files?"+url.Values{"hash": {hash}}.Encode(), nil)
	if err != nil {
		return Layout{}, err
	}
	var files []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	// A torrent that has no details yet (a magnet link) has no files to list.
	if status != http.StatusConflict && status != http.StatusNotFound {
		if err := expectOK(status, b, "list the torrent's files"); err != nil {
			return Layout{}, err
		}
		if err := json.Unmarshal(b, &files); err != nil {
			return Layout{}, fmt.Errorf("qBittorrent's file list didn't make sense: %w", err)
		}
	}
	l := Layout{Folder: strings.TrimRight(info[0].SavePath, "/")}
	for _, f := range files {
		l.Files = append(l.Files, File{Name: f.Name, Size: f.Size})
	}
	// content_path is where the files really are right now: the torrent's
	// folder, or the file itself. Cut off the torrent's own name to get the
	// folder that holds it.
	if len(files) > 0 {
		first, _, _ := strings.Cut(files[0].Name, "/")
		if dir, ok := strings.CutSuffix(strings.TrimRight(info[0].ContentPath, "/"), "/"+first); ok && dir != "" {
			l.Look = appendFolder(l.Look, dir)
		}
	}
	l.Look = appendFolder(l.Look, l.Folder)
	l.Look = appendFolder(l.Look, info[0].DownloadPath)
	return l, nil
}

// appendFolder adds a folder to a list unless it's empty or already there.
func appendFolder(list []string, dir string) []string {
	dir = strings.TrimRight(dir, "/")
	if dir == "" || slices.Contains(list, dir) {
		return list
	}
	return append(list, dir)
}

// Recheck makes qBittorrent check the stopped torrent's files against its
// pieces. Only Reelkeeper's category is touched.
func (q *QBittorrent) Recheck(ctx context.Context, category string, hash string) error {
	if err := q.ours(ctx, category, []string{hash}); err != nil {
		return err
	}
	b, status, err := q.do(ctx, http.MethodPost, "/api/v2/torrents/recheck", form(url.Values{"hashes": {hash}}))
	if err != nil {
		return err
	}
	return expectOK(status, b, "check the torrent's files")
}

// SetFolder does nothing for qBittorrent: it was told where the unfinished
// files are when the torrent was added, and moves them to the final folder
// itself when the torrent finishes.
func (q *QBittorrent) SetFolder(context.Context, string, string, string) error { return nil }
