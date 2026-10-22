package download

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/saltglass/reelkeeper/internal/cleanup"
	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/indexer"
	"github.com/saltglass/reelkeeper/internal/notify"
	"github.com/saltglass/reelkeeper/internal/settings"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// Usenet downloads (2.0) go to a Usenet client (SABnzbd or NZBGet) set up next to the
// torrent client, in Reelkeeper's same two categories. A Usenet download's
// record has Protocol "usenet", the client's job ID in Hash and the client's
// type in Client. Its jobs are read as Torrents, so the Queue, Check
// downloads, imports and cleanup treat both kinds alike where they can;
// where they differ (no seeding, failures the client reports, no moves
// between clients) the Usenet parts are in this file.

// UsenetClient is what Reelkeeper needs from a Usenet download client.
type UsenetClient interface {
	// Name is what messages call the client, such as "SABnzbd" or "NZBGet".
	Name() string
	Version(ctx context.Context) (string, error)
	// Problems are the client's settings that stop Reelkeeper working well:
	// warnings, and refusals that fail the connection test.
	Problems(ctx context.Context) (warnings, refusals []string, err error)
	EnsureCategory(ctx context.Context, name string) error
	// AddNZB adds an .nzb under a name and returns the job's ID. top puts it
	// ahead of the client's other jobs.
	AddNZB(ctx context.Context, nzb []byte, name, category string, top bool) (string, error)
	// Jobs lists the jobs in a category, downloading or finished, with
	// ContentPath set once the client has its files somewhere final.
	Jobs(ctx context.Context, category string) ([]Torrent, error)
	// RemoveJobs removes jobs, with what they downloaded when deleteFiles.
	RemoveJobs(ctx context.Context, category string, ids []string, deleteFiles bool) error
	PauseJobs(ctx context.Context, category string, ids []string) error
	ResumeJobs(ctx context.Context, category string, ids []string) error
	TopJobs(ctx context.Context, category string, ids []string) error
	// FreeSpace is the space free where the client puts finished jobs, or
	// -1 when it doesn't say.
	FreeSpace(ctx context.Context) (int64, error)
}

var (
	_ UsenetClient = (*SABnzbd)(nil)
	_ UsenetClient = (*NZBGet)(nil)
)

// ErrNoUsenetClient means a Usenet download needs a Usenet client and none
// is set up.
var ErrNoUsenetClient = errors.New("no Usenet client is set up (Settings → Download Client, Usenet)")

// errUsenetNoMove is what the move calls answer for a Usenet job: only
// torrents move between clients.
var errUsenetNoMove = errors.New("Usenet downloads don't move between download clients") //nolint:staticcheck // a proper name

// SetUsenet connects the Usenet client settings.
func (s *Service) SetUsenet(cfg func() settings.UsenetClient) { s.usenetCfg = cfg }

// newUsenet builds the Usenet client the settings choose.
func newUsenet(c settings.UsenetClient) UsenetClient {
	if c.ClientType() == settings.UsenetNZBGet {
		return &NZBGet{URL: c.URL, Username: c.Username, Password: c.Password}
	}
	return &SABnzbd{URL: c.URL, APIKey: c.APIKey}
}

// usenetSettings are the Usenet client settings; none when not connected.
func (s *Service) usenetSettings() settings.UsenetClient {
	if s.usenetCfg == nil {
		return settings.UsenetClient{}
	}
	return s.usenetCfg()
}

// UsenetOn reports whether a Usenet client is set up.
func (s *Service) UsenetOn() bool { return s.usenetSettings().Configured() }

// Searchable says whether indexers of a protocol are worth searching: only
// while a client that downloads it is set up. With no client at all, torrent
// indexers still are, so dry run shows what would be grabbed while someone
// tries Reelkeeper out.
func (s *Service) Searchable(protocol string) bool {
	if protocol == indexer.ProtocolUsenet {
		return s.UsenetOn()
	}
	return s.settings().Configured() || !s.UsenetOn()
}

// usenetClient is the Usenet client, as a Client for the code both kinds
// share.
func (s *Service) usenetClient() (Client, error) {
	cfg := s.usenetSettings()
	if !cfg.Configured() {
		return nil, ErrNoUsenetClient
	}
	return usenetAdapter{s.NewUsenet(cfg)}, nil
}

// clientFor is the client that holds a download: the Usenet client for a
// Usenet download, the torrent client otherwise.
func (s *Service) clientFor(d *Download) (Client, error) {
	if d.Usenet() {
		return s.usenetClient()
	}
	return s.client()
}

// clientNameFor names the client that holds a download, for messages.
func (s *Service) clientNameFor(d *Download) string {
	if d.Usenet() {
		return d.UsenetClientName()
	}
	return s.ClientName()
}

// Usenet reports whether the download is a Usenet one.
func (d *Download) Usenet() bool { return d.Protocol == indexer.ProtocolUsenet }

// UsenetClientName names the Usenet client a Usenet download is in
// ("SABnzbd").
func (d *Download) UsenetClientName() string { return settings.UsenetDisplayName(d.Client) }

// TestUsenet checks Usenet client settings (saved or not): it returns the
// version and any warnings, and fails on a refusal.
func (s *Service) TestUsenet(ctx context.Context, cfg settings.UsenetClient) (version string, warnings []string, err error) {
	if !cfg.Configured() {
		return "", nil, ErrNoUsenetClient
	}
	c := s.NewUsenet(cfg)
	if version, err = c.Version(ctx); err != nil {
		return "", nil, err
	}
	warnings, refusals, err := c.Problems(ctx)
	if err != nil {
		return "", nil, err
	}
	if len(refusals) > 0 {
		return "", nil, errors.New(strings.Join(refusals, " "))
	}
	// Listing Reelkeeper's category proves the key works for more than the
	// version.
	if _, err := c.Jobs(ctx, settings.MovieCategory); err != nil {
		return "", nil, err
	}
	return version, warnings, nil
}

// usenetAdapter makes a UsenetClient a Client, for the code Usenet and
// torrent downloads share (Queue, Pause, Resume, Move to top, Remove,
// imports). The calls that only mean something for torrents do nothing
// (seeding limits, tracker messages) or refuse (moves between clients).
type usenetAdapter struct{ u UsenetClient }

func (a usenetAdapter) Name() string                                { return a.u.Name() }
func (a usenetAdapter) Version(ctx context.Context) (string, error) { return a.u.Version(ctx) }
func (a usenetAdapter) EnsureCategory(ctx context.Context, name, _ string) error {
	return a.u.EnsureCategory(ctx, name)
}

func (a usenetAdapter) MoveCategory(context.Context, string, string, []string) error {
	return errUsenetNoMove
}

func (a usenetAdapter) Add(ctx context.Context, add Add) (string, error) {
	if len(add.NZB) == 0 {
		return "", errors.New("a Usenet client needs an .nzb file")
	}
	return a.u.AddNZB(ctx, add.NZB, add.Name, add.Category, add.Top)
}

func (a usenetAdapter) Torrents(ctx context.Context, category string) ([]Torrent, error) {
	return a.u.Jobs(ctx, category)
}

func (a usenetAdapter) Remove(ctx context.Context, category string, ids []string, deleteFiles bool) error {
	return a.u.RemoveJobs(ctx, category, ids, deleteFiles)
}

func (a usenetAdapter) NoShareLimits(context.Context, string, []string) error { return nil }

func (a usenetAdapter) TopPriority(ctx context.Context, category string, ids []string) error {
	return a.u.TopJobs(ctx, category, ids)
}

func (a usenetAdapter) Stop(ctx context.Context, category string, ids []string) error {
	return a.u.PauseJobs(ctx, category, ids)
}

func (a usenetAdapter) Start(ctx context.Context, category string, ids []string) error {
	return a.u.ResumeJobs(ctx, category, ids)
}

func (a usenetAdapter) TrackerMessages(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func (a usenetAdapter) Source(context.Context, string, string) (Source, error) {
	return Source{}, errUsenetNoMove
}

func (a usenetAdapter) Layout(context.Context, string, string) (Layout, error) {
	return Layout{}, errUsenetNoMove
}
func (a usenetAdapter) Recheck(context.Context, string, string) error { return errUsenetNoMove }
func (a usenetAdapter) SetFolder(context.Context, string, string, string) error {
	return errUsenetNoMove
}

// checkUsenet is Check downloads for Usenet downloads: finished jobs are
// imported, jobs gone from the client are marked removed. Failed jobs wait
// for cleanup (the "usenet-failed" rule), which removes, blocklists and
// searches again, so they're only counted here.
func (s *Service) checkUsenet(ctx context.Context, ds []Download) (parts []string, quiet bool, err error) {
	c, err := s.usenetClient()
	if err != nil {
		return nil, false, err
	}
	jobs, err := ourTorrents(ctx, c)
	if err != nil {
		s.usenetFailures++
		if s.usenetFailures == 3 {
			s.tell(ctx, notify.Event{Kind: notify.KindClientUnreachable, Title: c.Name(),
				Message: "Reelkeeper can't reach " + c.Name() + ", so Usenet downloads aren't being checked or imported: " + err.Error()})
		}
		return nil, false, err
	}
	s.usenetFailures = 0
	live := map[string]Torrent{}
	for _, t := range jobs {
		live[t.Hash] = t
	}
	folderUnseen, _ := s.folderUnseen(jobs)
	var imported, failed, importFailed, gone, waiting, unseen int
	for i := range ds {
		d := &ds[i]
		t, ok := live[d.Hash]
		switch {
		case !ok:
			gone++
			s.finish(ctx, d, StatusRemoved, "It was removed from "+c.Name()+" outside Reelkeeper.", grab.EventRemoved)
		case t.State == torrent.Failed:
			failed++
		case !t.Done():
			waiting++
		case s.waitForFiles(ctx, d, t, folderUnseen):
			unseen++
		default:
			if s.importOne(ctx, d, t) {
				imported++
			} else {
				importFailed++
			}
		}
	}
	parts = []string{fmt.Sprintf("%d downloading on Usenet", waiting)}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed in %s (cleanup handles them)", failed, c.Name()))
	}
	if unseen > 0 {
		parts = append(parts, fmt.Sprintf("%d finished but not visible to Reelkeeper yet", unseen))
	}
	if imported > 0 {
		parts = append(parts, fmt.Sprintf("%d imported", imported))
	}
	if importFailed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed to import", importFailed))
	}
	if gone > 0 {
		parts = append(parts, fmt.Sprintf("%d removed outside Reelkeeper", gone))
	}
	return parts, imported+importFailed+gone+unseen == 0, nil
}

// checkUsenetRoom refuses a Usenet grab that would leave less than the space
// kept free where the Usenet client puts finished jobs, counting what jobs in
// progress still need. A job needs about twice its size while it is
// unpacked (the archives and what comes out of them). When the client
// doesn't say how much is free, the grab goes through.
func (s *Service) checkUsenetRoom(ctx context.Context, size int64) error {
	reserve := s.keepFree()
	if reserve <= 0 {
		return nil
	}
	cfg := s.usenetSettings()
	u := s.NewUsenet(cfg)
	free, err := u.FreeSpace(ctx)
	if err != nil || free < 0 {
		return nil
	}
	var pending int64
	if jobs, err := ourTorrents(ctx, usenetAdapter{u}); err == nil {
		for _, t := range jobs {
			if !t.Done() && t.State != torrent.Failed {
				pending += t.Left + t.Size // still to download, plus room to unpack
			}
		}
	}
	if free-pending-2*size >= reserve {
		return nil
	}
	return fmt.Errorf("%w: %s's disk has %s free and Usenet downloads in progress still need %s, so this release (%s, about twice that "+
		"while it unpacks) would leave less than the %s kept free (Settings → Cleanup)",
		grab.ErrNotEnoughSpace, u.Name(), cleanup.Size(free), cleanup.Size(pending), cleanup.Size(size), cleanup.Size(reserve))
}
