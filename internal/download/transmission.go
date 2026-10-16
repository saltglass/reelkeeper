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
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// Transmission is a Transmission RPC client (its JSON-over-HTTP API).
//
// Transmission has no categories. Reelkeeper's category (reelkeeper-movies
// or reelkeeper-tv) is a label on the torrent, and a torrent is Reelkeeper's
// in a category only if it carries that label. Every call that changes a
// torrent checks that first, so another app's torrents are never listed,
// changed or removed.
type Transmission struct {
	URL      string // e.g. http://192.168.1.20:9091 (the /transmission/rpc path is added)
	Username string // empty when Transmission's RPC login is off
	Password string

	once   sync.Once
	client *http.Client

	mu sync.Mutex
	// sessionID is the X-Transmission-Session-Id Transmission last gave us.
	sessionID string
	// savePaths are the folders EnsureCategory was told about, by category.
	savePaths map[string]string
}

// minRPCVersion is the RPC version of Transmission 4.0, the first with labels.
const minRPCVersion = 17

func (t *Transmission) http() *http.Client {
	t.once.Do(func() { t.client = &http.Client{Timeout: 30 * time.Second} })
	return t.client
}

// endpoint is the RPC address. Transmission serves it at /transmission/rpc,
// so a bare address (the way people copy it from a browser) gets that added;
// an address with a path of its own is used as it is.
func (t *Transmission) endpoint() string {
	base := strings.TrimRight(t.URL, "/")
	if u, err := url.Parse(base); err == nil && u.Path != "" {
		return base
	}
	return base + "/transmission/rpc"
}

func transmissionReach(err error) error {
	return &unreachableError{msg: "can't reach Transmission: " + err.Error(), err: err}
}

// loginError is "wrong username or password" for a named client. It matches
// ErrLogin, so callers don't care which client said it.
type loginError struct{ client string }

func (e loginError) Error() string        { return e.client + " refused the username or password" }
func (e loginError) Is(target error) bool { return target == ErrLogin }

// call sends one RPC request and decodes the "arguments" of the answer into
// out (when out isn't nil). what says, in plain words, what the request was
// for, such as "list torrents": it's used in the error message.
//
// Transmission guards against other web pages using it with a session id: a
// request without the right one is answered 409 with the id to use, so the
// request is sent again with it. The id is kept for the next calls.
func (t *Transmission) call(ctx context.Context, what, method string, args map[string]any, out any) error {
	reqBody, err := json.Marshal(map[string]any{"method": method, "arguments": args})
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint(), bytes.NewReader(reqBody))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		t.mu.Lock()
		if t.sessionID != "" {
			req.Header.Set("X-Transmission-Session-Id", t.sessionID)
		}
		t.mu.Unlock()
		if t.Username != "" {
			req.SetBasicAuth(t.Username, t.Password)
		}
		resp, err := t.http().Do(req)
		if err != nil {
			return transmissionReach(err)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		switch resp.StatusCode {
		case http.StatusConflict:
			id := resp.Header.Get("X-Transmission-Session-Id")
			if id == "" || attempt > 0 {
				return fmt.Errorf("Transmission couldn't %s: it kept asking for a new session id", what)
			}
			t.mu.Lock()
			t.sessionID = id
			t.mu.Unlock()
			continue
		case http.StatusUnauthorized:
			return loginError{"Transmission"}
		case http.StatusForbidden:
			return fmt.Errorf("Transmission refused Reelkeeper's address; add it to Transmission's rpc-whitelist, or turn the whitelist off")
		case http.StatusOK:
		default:
			msg := strings.TrimSpace(string(b))
			if len(msg) > 200 {
				msg = msg[:200]
			}
			return fmt.Errorf("Transmission couldn't %s (%d %s)", what, resp.StatusCode, msg)
		}
		var ans struct {
			Result    string          `json:"result"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(b, &ans); err != nil || ans.Result == "" {
			return fmt.Errorf("Transmission's answer didn't make sense (is %s Transmission's RPC address?)", t.endpoint()) //nolint:staticcheck // a proper name
		}
		if ans.Result != "success" {
			return fmt.Errorf("Transmission couldn't %s: %s", what, ans.Result)
		}
		if out == nil || len(ans.Arguments) == 0 {
			return nil
		}
		if err := json.Unmarshal(ans.Arguments, out); err != nil {
			return fmt.Errorf("Transmission's answer to %s didn't make sense: %w", what, err) //nolint:staticcheck // a proper name
		}
		return nil
	}
}

// Name is what messages call this client.
func (t *Transmission) Name() string { return "Transmission" }

// Version returns Transmission's version, such as "4.0.6 (38c164933e)". It is
// the connection test, and refuses a Transmission older than 4.0: Reelkeeper
// keeps its downloads apart with labels, which 4.0 added.
func (t *Transmission) Version(ctx context.Context) (string, error) {
	var s struct {
		Version    string `json:"version"`
		RPCVersion int    `json:"rpc-version"`
	}
	err := t.call(ctx, "say its version", "session-get", map[string]any{"fields": []string{"version", "rpc-version"}}, &s)
	if err != nil {
		return "", err
	}
	if s.RPCVersion < minRPCVersion {
		// "3.00 (bb6b5a062e)": the name without the build.
		v, _, _ := strings.Cut(s.Version, " ")
		return "", fmt.Errorf("Transmission %s is too old: Reelkeeper needs 4.0 or newer, for labels", v)
	}
	return s.Version, nil
}

// EnsureCategory needs no call: a label exists as soon as a torrent has it.
// It only remembers the folder for the category, for Add to use when it isn't
// given one. (qBittorrent's category carries its folder; Transmission's label
// has none.) A folder remembered earlier is kept.
func (t *Transmission) EnsureCategory(_ context.Context, name, savePath string) error {
	if savePath == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.savePaths == nil {
		t.savePaths = map[string]string{}
	}
	if _, ok := t.savePaths[name]; !ok {
		t.savePaths[name] = savePath
	}
	return nil
}

// rawTorrent is the part of Transmission's torrent-get answer Reelkeeper reads.
type rawTorrent struct {
	HashString              string       `json:"hashString"`
	Name                    string       `json:"name"`
	Labels                  []string     `json:"labels"`
	Status                  int          `json:"status"`
	Error                   int          `json:"error"`
	ErrorString             string       `json:"errorString"`
	IsStalled               bool         `json:"isStalled"`
	MetadataPercentComplete float64      `json:"metadataPercentComplete"`
	PercentDone             float64      `json:"percentDone"`
	SizeWhenDone            int64        `json:"sizeWhenDone"`
	LeftUntilDone           int64        `json:"leftUntilDone"`
	RateDownload            int64        `json:"rateDownload"`
	ETA                     int64        `json:"eta"`
	DownloadDir             string       `json:"downloadDir"`
	UploadRatio             float64      `json:"uploadRatio"`
	SecondsSeeding          int64        `json:"secondsSeeding"`
	AddedDate               int64        `json:"addedDate"`
	DoneDate                int64        `json:"doneDate"`
	PeersSendingToUs        int          `json:"peersSendingToUs"`
	PeersConnected          int          `json:"peersConnected"`
	TrackerStats            []rawTracker `json:"trackerStats"`
	HaveValid               int64        `json:"haveValid"`
	UploadedEver            int64        `json:"uploadedEver"`
}

type rawTracker struct {
	SeederCount        int    `json:"seederCount"`
	LastAnnounceResult string `json:"lastAnnounceResult"`
	LastScrapeResult   string `json:"lastScrapeResult"`
}

// torrentFields are the fields Torrents asks for: only what it uses.
var torrentFields = []string{
	"hashString", "name", "labels", "status", "error", "errorString", "isStalled", "metadataPercentComplete",
	"percentDone", "sizeWhenDone", "leftUntilDone", "rateDownload", "eta", "downloadDir", "uploadRatio",
	"secondsSeeding", "addedDate", "doneDate", "peersSendingToUs", "peersConnected", "trackerStats", "haveValid", "uploadedEver",
}

// get asks for torrents: the ones with these hashes, or all of them when
// ids is nil. Callers keep only the ones with Reelkeeper's label.
func (t *Transmission) get(ctx context.Context, ids []string, fields []string) ([]rawTorrent, error) {
	args := map[string]any{"fields": fields}
	if ids != nil {
		args["ids"] = ids
	}
	var ans struct {
		Torrents []rawTorrent `json:"torrents"`
	}
	if err := t.call(ctx, "list torrents", "torrent-get", args, &ans); err != nil {
		return nil, err
	}
	return ans.Torrents, nil
}

// Torrents lists the torrents with the category's label. category must not be
// empty: Reelkeeper never lists other apps' torrents.
func (t *Transmission) Torrents(ctx context.Context, category string) ([]Torrent, error) {
	if category == "" {
		return nil, errors.New("a category is required")
	}
	raw, err := t.get(ctx, nil, torrentFields)
	if err != nil {
		return nil, err
	}
	out := make([]Torrent, 0, len(raw))
	for _, r := range raw {
		if !slices.Contains(r.Labels, category) {
			continue
		}
		out = append(out, r.torrent(category))
	}
	return out, nil
}

// torrent turns Transmission's row into Reelkeeper's Torrent.
func (r rawTorrent) torrent(category string) Torrent {
	tr := Torrent{
		Hash: strings.ToLower(r.HashString), Name: r.Name, Category: category, State: transmissionState(r),
		Progress: r.PercentDone, Size: r.SizeWhenDone, Left: r.LeftUntilDone, Speed: r.RateDownload,
		SavePath: r.DownloadDir, ContentPath: path.Join(r.DownloadDir, r.Name),
		SeedingTime: time.Duration(r.SecondsSeeding) * time.Second, AddedOn: time.Unix(r.AddedDate, 0).UTC(),
		Seeds: r.PeersSendingToUs, Peers: r.PeersConnected, Verified: r.HaveValid, Uploaded: r.UploadedEver,
	}
	// Transmission has no categories: its labels are the category and the
	// tags.
	for _, l := range r.Labels {
		if !slices.Contains(settings.Categories, l) {
			tr.Tags = append(tr.Tags, l)
		}
	}
	// The moment its last piece arrives, Transmission still shows the torrent
	// as downloading, for a short while, with everything downloaded. Only
	// when it changes to seeding is the download complete: its files have
	// then been moved from Transmission's "incomplete" folder, if it uses
	// one, to the download folder, which is where Reelkeeper imports from.
	// Until then the torrent isn't reported as finished.
	if r.Status == 4 && tr.Progress >= 1 {
		tr.Progress = math.Nextafter(1, 0)
	}
	// Transmission says -1 (not known) or -2 (can't be known) for the time
	// left, and -1 for a ratio it can't work out yet.
	if r.ETA > 0 {
		tr.ETA = time.Duration(r.ETA) * time.Second
	}
	if r.UploadRatio > 0 {
		tr.Ratio = r.UploadRatio
	}
	if r.DoneDate > 0 {
		c := time.Unix(r.DoneDate, 0).UTC()
		tr.CompletedOn = &c
	}
	// The most seeds any tracker says there are (a negative count means the
	// tracker hasn't said).
	for _, ts := range r.TrackerStats {
		tr.SwarmSeeds = max(tr.SwarmSeeds, ts.SeederCount)
	}
	return tr
}

// transmissionState translates a Transmission torrent's status into Reelkeeper's
// own states. Each mapping means what the matching one in qbitState means:
//
//   - A local error (error 3: Transmission itself can't read or write the
//     files) is Error, like qBittorrent's "error". When the message says the
//     data is gone ("No data found! Ensure your drives are connected or
//     use "Set Location"...") it is MissingFiles, like "missingFiles".
//     Errors 1 and 2 are only what a tracker said (a warning or an error);
//     the torrent keeps working, so they don't make it Error. The "unregistered"
//     rule reads those messages through TrackerMessages instead.
//   - Status 1 and 2 (waiting to check, checking): Checking, like
//     "checkingDL" and "checkingUP".
//   - Status 3 (waiting in the download queue): Queued, like "queuedDL".
//   - Status 5 (waiting in the seed queue): Seeding, like "queuedUP", which
//     qBittorrent also counts as seeding.
//   - Status 0 (stopped): SeedingPaused when it had finished, Paused when not,
//     like "pausedUP"/"stoppedUP" and "pausedDL"/"stoppedDL".
//   - Status 4 (downloading): Metadata while it is still getting the
//     torrent's details ("metaDL"), Stalled when Transmission's "is stalled"
//     flag is set ("stalledDL"), otherwise Downloading. Transmission has no
//     stalled state; its flag comes on after the torrent has been idle for
//     "queue-stalled-minutes" (30 by default), so it is slower to say so
//     than qBittorrent, and cleanup waits longer, never acts sooner.
//   - Status 6 (seeding): SeedingStalled when the flag is set ("stalledUP"),
//     otherwise Seeding ("uploading").
//
// Anything else is Unknown, which no rule acts on.
func transmissionState(r rawTorrent) torrent.State {
	if r.Error == 3 {
		if strings.Contains(strings.ToLower(r.ErrorString), "no data found") {
			return torrent.MissingFiles
		}
		return torrent.Error
	}
	switch r.Status {
	case 1, 2:
		return torrent.Checking
	case 3:
		return torrent.Queued
	case 5:
		return torrent.Seeding
	case 0:
		if r.PercentDone >= 1 {
			return torrent.SeedingPaused
		}
		return torrent.Paused
	case 4:
		switch {
		case r.MetadataPercentComplete < 1:
			return torrent.Metadata
		case r.IsStalled:
			return torrent.Stalled
		}
		return torrent.Downloading
	case 6:
		if r.IsStalled {
			return torrent.SeedingStalled
		}
		return torrent.Seeding
	}
	return torrent.Unknown
}

// Add adds a torrent and returns its info hash. If Transmission already has
// it with the category's label, that counts as added; without the label (some
// other app's download) it's an error, and nothing changes.
func (t *Transmission) Add(ctx context.Context, a Add) (string, error) {
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
	if has, err := t.alreadyHas(ctx, hash, a.Category); err != nil || has {
		return hash, err
	}

	t.mu.Lock()
	dir := t.savePaths[a.Category]
	t.mu.Unlock()
	if a.SavePath != "" {
		dir = a.SavePath
	}
	args := map[string]any{"paused": a.Stopped, "labels": labelsFor(a)}
	if len(a.Torrent) > 0 {
		args["metainfo"] = base64.StdEncoding.EncodeToString(a.Torrent)
	} else {
		args["filename"] = a.Magnet
	}
	if a.FilesIn != "" {
		// A torrent moving here: Transmission checks the folder its files
		// are in now; SetFolder then moves them to the final folder.
		dir = a.FilesIn
	}
	if dir != "" {
		args["download-dir"] = dir
	}
	var ans struct {
		Added *struct {
			HashString string `json:"hashString"`
		} `json:"torrent-added"`
		Duplicate *struct {
			HashString string `json:"hashString"`
		} `json:"torrent-duplicate"`
	}
	if err := t.call(ctx, "add the torrent", "torrent-add", args, &ans); err != nil {
		return "", err
	}
	switch {
	case ans.Added != nil && ans.Added.HashString != "":
		return strings.ToLower(ans.Added.HashString), nil
	case ans.Duplicate != nil && ans.Duplicate.HashString != "":
		// Added by someone else a moment ago: the same check as before.
		h := strings.ToLower(ans.Duplicate.HashString)
		if _, err := t.alreadyHas(ctx, h, a.Category); err != nil {
			return "", err
		}
		return h, nil
	}
	return "", errors.New("Transmission refused the torrent (it may be invalid)")
}

// alreadyHas says whether Transmission has the torrent already, as one of
// Reelkeeper's in the category. A torrent it has without the label is an
// error, so Reelkeeper leaves it alone.
func (t *Transmission) alreadyHas(ctx context.Context, hash, category string) (bool, error) {
	existing, err := t.get(ctx, []string{hash}, []string{"hashString", "labels"})
	if err != nil || len(existing) == 0 {
		return false, err
	}
	if slices.Contains(existing[0].Labels, category) {
		return true, nil
	}
	return false, fmt.Errorf("Transmission already has this torrent, without the label %q, so Reelkeeper leaves it alone", category)
}

// labelsFor is the category followed by the tags, as Transmission labels:
// no commas (they separate labels in its own screens), no blanks, no repeats.
func labelsFor(a Add) []string {
	out := []string{a.Category}
	for _, tag := range a.Tags {
		tag = strings.TrimSpace(strings.ReplaceAll(tag, ",", " "))
		if tag != "" && !slices.Contains(out, tag) {
			out = append(out, tag)
		}
	}
	return out
}

// ours checks that every hash has the category's label, so Reelkeeper never
// changes another app's torrent. It returns the torrents' labels, by hash.
func (t *Transmission) ours(ctx context.Context, category string, hashes []string) (map[string][]string, error) {
	rows, err := t.get(ctx, hashes, []string{"hashString", "labels"})
	if err != nil {
		return nil, err
	}
	labels := map[string][]string{}
	for _, r := range rows {
		if slices.Contains(r.Labels, category) {
			labels[strings.ToLower(r.HashString)] = r.Labels
		}
	}
	for _, h := range hashes {
		if _, ok := labels[strings.ToLower(h)]; !ok {
			return nil, fmt.Errorf("torrent %s doesn't have Reelkeeper's label %s, so Reelkeeper won't change it", h, category)
		}
	}
	return labels, nil
}

// act runs a method on these torrents, after checking they're Reelkeeper's.
func (t *Transmission) act(ctx context.Context, category string, hashes []string, what, method string) error {
	if len(hashes) == 0 {
		return nil
	}
	if _, err := t.ours(ctx, category, hashes); err != nil {
		return err
	}
	return t.call(ctx, what, method, map[string]any{"ids": hashes}, nil)
}

// Remove deletes torrents, and their downloaded files when deleteFiles is
// set. Only hashes with Reelkeeper's label may be passed; callers check
// that, and Remove checks it again.
func (t *Transmission) Remove(ctx context.Context, category string, hashes []string, deleteFiles bool) error {
	if len(hashes) == 0 {
		return nil
	}
	if _, err := t.ours(ctx, category, hashes); err != nil {
		return err
	}
	return t.call(ctx, "remove the torrent", "torrent-remove", map[string]any{"ids": hashes, "delete-local-data": deleteFiles}, nil)
}

// MoveCategory gives torrents the label to in place of from, keeping their
// other labels, without moving their files. Only torrents with the from
// label are touched.
func (t *Transmission) MoveCategory(ctx context.Context, from, to string, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	labels, err := t.ours(ctx, from, hashes)
	if err != nil {
		return err
	}
	for _, h := range hashes {
		var next []string
		for _, l := range labels[strings.ToLower(h)] {
			if l == from {
				l = to
			}
			if !slices.Contains(next, l) {
				next = append(next, l)
			}
		}
		if err := t.call(ctx, "move the torrent to the label "+to, "torrent-set", map[string]any{"ids": []string{h}, "labels": next}, nil); err != nil {
			return err
		}
	}
	return nil
}

// TopPriority moves these torrents to the top of Transmission's download
// queue. It only matters while Transmission's queue is turned on (its
// "download queue size" setting); with it off, torrents all download at once
// and this changes nothing. Only Reelkeeper's torrents are touched.
func (t *Transmission) TopPriority(ctx context.Context, category string, hashes []string) error {
	return t.act(ctx, category, hashes, "move the torrents to the top of the queue", "queue-move-top")
}

// NoShareLimits makes Transmission's seeding limits not apply to these
// torrents (Reelkeeper's cleanup decides when they stop), and starts any that
// those limits had already stopped. Transmission's limits are per torrent:
// mode 2 is "unlimited", for both the ratio and the idle time. Only
// Reelkeeper's torrents are touched.
func (t *Transmission) NoShareLimits(ctx context.Context, category string, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	if _, err := t.ours(ctx, category, hashes); err != nil {
		return err
	}
	err := t.call(ctx, "change the torrents' seeding limits", "torrent-set",
		map[string]any{"ids": hashes, "seedRatioMode": 2, "seedIdleMode": 2}, nil)
	if err != nil {
		return err
	}
	return t.call(ctx, "start the torrents", "torrent-start", map[string]any{"ids": hashes}, nil)
}

// Stop pauses these torrents. Only Reelkeeper's torrents are touched.
func (t *Transmission) Stop(ctx context.Context, category string, hashes []string) error {
	return t.act(ctx, category, hashes, "pause the torrents", "torrent-stop")
}

// Start resumes these torrents. Only Reelkeeper's torrents are touched.
func (t *Transmission) Start(ctx context.Context, category string, hashes []string) error {
	return t.act(ctx, category, hashes, "resume the torrents", "torrent-start")
}

// TrackerMessages returns what the torrent's trackers last said, such as
// "Unregistered torrent". Only Reelkeeper's torrents are asked about.
func (t *Transmission) TrackerMessages(ctx context.Context, category, hash string) ([]string, error) {
	if _, err := t.ours(ctx, category, []string{hash}); err != nil {
		return nil, err
	}
	rows, err := t.get(ctx, []string{hash}, []string{"hashString", "trackerStats"})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		for _, ts := range r.TrackerStats {
			for _, m := range []string{ts.LastAnnounceResult, ts.LastScrapeResult} {
				// Transmission says "Success" when all is well.
				if m = strings.TrimSpace(m); m != "" && !strings.EqualFold(m, "success") {
					out = append(out, m)
				}
			}
		}
	}
	return out, nil
}

// Source returns the torrent's magnet link. Transmission can't hand back a
// .torrent file through its API, and a private torrent's magnet link is no
// use to another client (its peers never send a private torrent's details),
// so for those it says so.
func (t *Transmission) Source(ctx context.Context, category, hash string) (Source, error) {
	if _, err := t.ours(ctx, category, []string{hash}); err != nil {
		return Source{}, err
	}
	var ans struct {
		Torrents []struct {
			IsPrivate  bool   `json:"isPrivate"`
			MagnetLink string `json:"magnetLink"`
		} `json:"torrents"`
	}
	err := t.call(ctx, "read the torrent's magnet link", "torrent-get",
		map[string]any{"ids": []string{hash}, "fields": []string{"isPrivate", "magnetLink"}}, &ans)
	if err != nil {
		return Source{}, err
	}
	if len(ans.Torrents) == 0 {
		return Source{}, &NoSourceError{"Transmission doesn't list this torrent."}
	}
	if ans.Torrents[0].IsPrivate {
		return Source{}, &NoSourceError{"It is a private torrent, Transmission can't give back its .torrent file, and Reelkeeper didn't keep a copy (it was grabbed before Reelkeeper 1.3)."}
	}
	return Source{Magnet: ans.Torrents[0].MagnetLink}, nil
}

// Layout says where Transmission keeps the torrent's files: its file list,
// the folder it belongs in, and the folders its files may be in now (the
// download folder and, when it is used, the incomplete folder, where
// unfinished files carry a ".part" ending).
func (t *Transmission) Layout(ctx context.Context, category, hash string) (Layout, error) {
	if _, err := t.ours(ctx, category, []string{hash}); err != nil {
		return Layout{}, err
	}
	var ans struct {
		Torrents []struct {
			DownloadDir string `json:"downloadDir"`
			Files       []struct {
				Name   string `json:"name"`
				Length int64  `json:"length"`
			} `json:"files"`
		} `json:"torrents"`
	}
	err := t.call(ctx, "list the torrent's files", "torrent-get",
		map[string]any{"ids": []string{hash}, "fields": []string{"downloadDir", "files"}}, &ans)
	if err != nil {
		return Layout{}, err
	}
	if len(ans.Torrents) == 0 {
		return Layout{}, errors.New("Transmission doesn't list this torrent") //nolint:staticcheck // a proper name
	}
	var sess struct {
		IncompleteEnabled bool   `json:"incomplete-dir-enabled"`
		IncompleteDir     string `json:"incomplete-dir"`
		DownloadDir       string `json:"download-dir"`
	}
	if err := t.call(ctx, "read its folders", "session-get",
		map[string]any{"fields": []string{"incomplete-dir-enabled", "incomplete-dir", "download-dir"}}, &sess); err != nil {
		return Layout{}, err
	}
	l := Layout{Folder: strings.TrimRight(ans.Torrents[0].DownloadDir, "/")}
	for _, f := range ans.Torrents[0].Files {
		l.Files = append(l.Files, File{Name: f.Name, Size: f.Length})
	}
	l.Look = appendFolder(l.Look, l.Folder)
	if sess.IncompleteEnabled {
		l.Look = appendFolder(l.Look, sess.IncompleteDir)
	}
	l.Look = appendFolder(l.Look, sess.DownloadDir)
	return l, nil
}

// Recheck makes Transmission verify the stopped torrent's files. Only
// Reelkeeper's torrents are touched.
func (t *Transmission) Recheck(ctx context.Context, category string, hash string) error {
	return t.act(ctx, category, []string{hash}, "check the torrent's files", "torrent-verify")
}

// SetFolder moves the torrent's files to the folder it belongs in, when it
// isn't there already. Transmission moves the files itself (nothing is
// deleted), partial ones included.
func (t *Transmission) SetFolder(ctx context.Context, category, hash, folder string) error {
	if _, err := t.ours(ctx, category, []string{hash}); err != nil {
		return err
	}
	rows, err := t.get(ctx, []string{hash}, []string{"hashString", "downloadDir"})
	if err != nil {
		return err
	}
	if len(rows) == 1 && strings.TrimRight(rows[0].DownloadDir, "/") == strings.TrimRight(folder, "/") {
		return nil
	}
	return t.call(ctx, "move the torrent's files", "torrent-set-location",
		map[string]any{"ids": []string{hash}, "location": folder, "move": true}, nil)
}

var _ Client = (*Transmission)(nil)
