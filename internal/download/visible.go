package download

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/saltglass/reelkeeper/internal/notify"
)

// Reelkeeper reads qBittorrent's downloads through a shared folder (often
// an NFS mount of the download disk). If that folder isn't mounted,
// say after a reboot where the download host came up late, every finished
// download looks missing. That's a problem with the folder, not with the
// downloads, so imports wait for it instead of failing one by one
// (2026-10-10: a download failed to import because the mount had timed out
// at boot).

// MissingGrace is how long a finished download Reelkeeper can't see waits
// before its import fails, while the download folder itself looks fine.
// While the whole folder looks unmounted, imports wait as long as it takes.
const MissingGrace = time.Hour

// visibility tracks finished downloads Reelkeeper can't see yet. It lives in
// memory: after a restart, the wait simply starts again.
type visibility struct {
	// missingSince is when each download (by ID) was first seen finished
	// but not on disk.
	missingSince map[int64]time.Time
	// warned is set once the "folder can't be seen" problem has been
	// reported, so it's reported once, not every minute.
	warned bool
}

// onDisk reports whether a path exists. Errors other than "doesn't exist"
// (permissions, say) count as there: the import then explains them.
func onDisk(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

// folderUnseen reports whether the download folder as a whole looks
// unmounted: qBittorrent has data for some of Reelkeeper's torrents, and Reelkeeper
// can see none of it. It returns the folder to name in messages.
func (s *Service) folderUnseen(ts []Torrent) (bool, string) {
	started, dir := 0, ""
	for _, t := range ts {
		if t.ContentPath == "" || t.Progress <= 0 {
			continue
		}
		started++
		dir = filepath.Dir(t.ContentPath)
		if s.exists(t.ContentPath) {
			return false, ""
		}
	}
	return started > 0, dir
}

// waitForFiles decides whether a finished download should wait rather
// than import now, because Reelkeeper can't see its files. It updates the
// queue message while waiting.
func (s *Service) waitForFiles(ctx context.Context, d *Download, t Torrent, folderUnseen bool) bool {
	if s.exists(t.ContentPath) {
		delete(s.vis.missingSince, d.ID)
		return false
	}
	now := s.now()
	since, ok := s.vis.missingSince[d.ID]
	if !ok {
		since = now
		s.vis.missingSince[d.ID] = now
	}
	var msg string
	switch {
	case folderUnseen:
		msg = fmt.Sprintf("Finished, but Reelkeeper can't see any downloads in %s. The download folder probably isn't mounted where Reelkeeper runs; the import waits until it is.", filepath.Dir(t.ContentPath))
	case now.Sub(since) < MissingGrace:
		msg = fmt.Sprintf("Finished, but Reelkeeper can't see it at %s yet. Reelkeeper keeps checking until %s, then gives up.", t.ContentPath, since.Add(MissingGrace).Local().Format("15:04"))
	default:
		delete(s.vis.missingSince, d.ID)
		return false // waited long enough: the import fails and says why
	}
	if d.Message != msg {
		d.Message = msg
		if _, err := s.db.DB.NewUpdate().Model(d).Column("message").WherePK().Exec(ctx); err != nil {
			s.logger.Error("couldn't record a download's message", "release", d.ReleaseTitle, "error", err)
		}
	}
	return true
}

// reportFolder tells people, once, that the download folder can't be seen,
// and resets once it can.
func (s *Service) reportFolder(ctx context.Context, unseen bool, dir string) {
	if !unseen {
		s.vis.warned = false
		return
	}
	if s.vis.warned {
		return
	}
	s.vis.warned = true
	s.logger.Warn("can't see the download folder", "folder", dir)
	s.tell(ctx, notify.Event{Kind: notify.KindDownloadFolderMissing, Title: "Download folder",
		Message: fmt.Sprintf("Reelkeeper can't see %[1]s's downloads in %[2]s: %[1]s has data there, but none of it is visible to Reelkeeper. "+
			"Usually the download folder isn't mounted where Reelkeeper runs. Finished downloads wait in Activity → Queue and import once it's back.", s.ClientName(), dir)})
}
