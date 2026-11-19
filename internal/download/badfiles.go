package download

// Bad files: a release whose files are a program or script, or hold no
// movie or episode file at all, is refused before it's downloaded rather
// than found out at import (2026-11-19). Fakes on public trackers look like
// this ("Movie.2024.1080p.mkv.exe", a password-locked .zip with a "codec"
// installer).
//
//   - A .torrent file lists its files, and Reelkeeper fetches it itself
//     before the download client sees it, so Grab reads the list first. A
//     bad one never reaches the client: it's blocklisted for good and the
//     grab answers grab.ErrBadFiles, so the search moves on to the next
//     acceptable release.
//   - A magnet link has no list until the client has fetched the torrent's
//     details from its peers (seconds, usually). "Check downloads" reads the
//     list once it's there, and a bad one is removed at once with the little
//     it has downloaded, blocklisted for good, and searched for again.
//   - An .nzb (Usenet, 2.0) names each file in its posts' subjects, and
//     Grab reads those too. Only the program rule applies there: a Usenet
//     release is normally a set of archives, often with scrambled names, so
//     "no video" can't be told from the .nzb. A program the poster named
//     ("Setup.exe", "Movie.mkv.exe") is refused like a torrent's.
//
// Always on, no setting: nothing in the library is touched, and what's
// removed is a download that can never be imported. The rules themselves
// are library.UnfitRelease, so they match what the import would take.

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/anacrolix/torrent/metainfo"

	"github.com/saltglass/reelkeeper/internal/grab"
	"github.com/saltglass/reelkeeper/internal/library"
	"github.com/saltglass/reelkeeper/internal/torrent"
)

// BlockBadFiles is the blocklist rule for a release refused for its files.
// Permanent: the files of a torrent never change.
const BlockBadFiles = "bad-files"

// TorrentFiles lists the files a .torrent file holds, with their paths as
// the download clients list them: the torrent's folder and the file's path
// inside it, "/" between, or just the name for a torrent of one file.
func TorrentFiles(b []byte) ([]File, error) {
	mi, err := metainfo.Load(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("not a readable torrent file: %w", err)
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return nil, fmt.Errorf("the torrent file's details are damaged: %w", err)
	}
	if !info.IsDir() {
		return []File{{Name: info.BestName(), Size: info.TotalLength()}}, nil
	}
	var out []File
	for f := range info.UpvertedFilesIter() {
		out = append(out, File{Name: info.BestName() + "/" + strings.Join(f.BestPath(), "/"), Size: f.Length})
	}
	return out, nil
}

// NZBFiles lists the files an .nzb names, with each file's size from its
// segments. A file's name is the quoted part of its subject, as posting
// tools write it (`Movie [01/30] - "Movie.part01.rar" yEnc (1/50)`); a
// subject with no quoted name (an obfuscated post) is left out, so the list
// can be shorter than the release, or empty.
func NZBFiles(b []byte) ([]File, error) {
	var doc struct {
		Files []struct {
			Subject  string `xml:"subject,attr"`
			Segments []struct {
				Bytes int64 `xml:"bytes,attr"`
			} `xml:"segments>segment"`
		} `xml:"file"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("not a readable .nzb file: %w", err)
	}
	var out []File
	for _, f := range doc.Files {
		name := quotedName(f.Subject)
		if name == "" {
			continue
		}
		var size int64
		for _, seg := range f.Segments {
			size += seg.Bytes
		}
		out = append(out, File{Name: name, Size: size})
	}
	return out, nil
}

// quotedName is the first "quoted" part of a post's subject, or "".
func quotedName(subject string) string {
	_, rest, ok := strings.Cut(subject, `"`)
	if !ok {
		return ""
	}
	name, _, ok := strings.Cut(rest, `"`)
	if !ok {
		return ""
	}
	return strings.TrimSpace(name)
}

// unfit is library.UnfitRelease for a client's file list.
func unfit(files []File) string {
	rf := make([]library.ReleaseFile, len(files))
	for i, f := range files {
		rf[i] = library.ReleaseFile{Name: f.Name, Size: f.Size}
	}
	return library.UnfitRelease(rf)
}

// refuseTorrent checks a fetched .torrent's files before it's sent. When
// they're bad, the release is blocklisted for the title and the answer is
// grab.ErrBadFiles with the reason. A file list that can't be read isn't a
// reason to refuse: the client reads the torrent itself, and Check looks at
// its list then. checked says whether the list was read.
func (s *Service) refuseTorrent(ctx context.Context, g grab.Grabbed, titleID int64, b []byte) (checked bool, err error) {
	files, ferr := TorrentFiles(b)
	if ferr != nil {
		s.logger.Warn("couldn't read the files in a torrent before sending it; they'll be checked once the download client has it",
			"release", g.Release.Title, "error", ferr)
		return false, nil
	}
	reason := unfit(files)
	if reason == "" {
		return true, nil
	}
	hash, _ := InfoHash(b)
	if err := s.AddToBlocklist(ctx, BlockedRelease{TitleID: titleID, ReleaseTitle: g.Release.Title, Hash: hash,
		Indexer: g.Release.Indexer, Quality: g.Quality, Reason: badFilesReason(reason), Rule: BlockBadFiles}); err != nil {
		return true, err
	}
	s.logger.Info("refused a release for its files before downloading it", "release", g.Release.Title, "reason", reason)
	return true, fmt.Errorf("%w: %s", grab.ErrBadFiles, reason)
}

// refuseNZB checks the files a fetched .nzb names before it's sent, for
// programs only (see the top of this file). When there is one, the release
// is blocklisted for the title (by name: a Usenet release has no info hash)
// and the answer is grab.ErrBadFiles with the reason. An .nzb that can't be
// read is left to the Usenet client, which reads it itself.
func (s *Service) refuseNZB(ctx context.Context, g grab.Grabbed, titleID int64, b []byte) error {
	files, err := NZBFiles(b)
	if err != nil {
		s.logger.Warn("couldn't read the files in an .nzb before sending it", "release", g.Release.Title, "error", err)
		return nil
	}
	rf := make([]library.ReleaseFile, len(files))
	for i, f := range files {
		rf[i] = library.ReleaseFile{Name: f.Name, Size: f.Size}
	}
	reason := library.ProgramsIn(rf)
	if reason == "" {
		return nil
	}
	if err := s.AddToBlocklist(ctx, BlockedRelease{TitleID: titleID, ReleaseTitle: g.Release.Title,
		Indexer: g.Release.Indexer, Quality: g.Quality, Reason: badFilesReason(reason), Rule: BlockBadFiles}); err != nil {
		return err
	}
	s.logger.Info("refused a Usenet release for its files before downloading it", "release", g.Release.Title, "reason", reason)
	return fmt.Errorf("%w: %s", grab.ErrBadFiles, reason)
}

// badFilesReason is the blocklist's reason for a release with bad files.
func badFilesReason(reason string) string {
	return "Not downloaded: " + reason + "."
}

// checkFiles looks at the files of a download whose list wasn't read at grab
// time (a magnet link, or after a restart), once the client has them. A bad
// one is removed with its data, blocklisted for good, and searched for
// again; the answer is then true. Problems reading the list are left for the
// next check. Called by Check, under syncMu.
func (s *Service) checkFiles(ctx context.Context, c Client, d *Download, t Torrent) bool {
	if s.isChecked(d.Hash) || t.State == torrent.Metadata {
		return false
	}
	lay, err := c.Layout(ctx, d.Category, d.Hash)
	if err != nil {
		s.logger.Debug("couldn't list a download's files yet", "release", d.ReleaseTitle, "error", err)
		return false
	}
	if len(lay.Files) == 0 {
		return false // its details haven't arrived yet
	}
	s.markChecked(d.Hash, true)
	reason := unfit(lay.Files)
	if reason == "" {
		return false
	}
	if err := c.Remove(ctx, d.Category, []string{d.Hash}, true); err != nil {
		s.logger.Warn("couldn't remove a download with bad files; it'll be tried again", "release", d.ReleaseTitle, "error", err)
		s.markChecked(d.Hash, false)
		return false
	}
	if d.TitleID != nil {
		if err := s.AddToBlocklist(ctx, BlockedRelease{TitleID: *d.TitleID, ReleaseTitle: d.ReleaseTitle, Hash: d.Hash,
			Indexer: d.Indexer, Quality: d.Quality, Reason: "Removed: " + reason + ".", Rule: BlockBadFiles}); err != nil {
			s.logger.Error("couldn't blocklist a download removed for its files", "release", d.ReleaseTitle, "error", err)
		}
	}
	msg := "Removed as soon as its files were known: " + reason + ". It's on the blocklist now."
	if d.VersionID != nil && s.searchAgain != nil {
		if err := s.searchAgain(ctx, *d.VersionID); err != nil {
			s.logger.Warn("couldn't start a search to replace a download with bad files", "release", d.ReleaseTitle, "error", err)
		} else {
			msg += " Searching for another release."
		}
	}
	s.logger.Info("removed a download for its files", "release", d.ReleaseTitle, "reason", reason)
	s.finish(ctx, d, StatusRemoved, msg, grab.EventRemoved)
	return true
}

// markChecked records whether a download's files have been checked.
func (s *Service) markChecked(hash string, checked bool) {
	s.checkedMu.Lock()
	defer s.checkedMu.Unlock()
	if checked {
		s.filesChecked[hash] = true
	} else {
		delete(s.filesChecked, hash)
	}
}

func (s *Service) isChecked(hash string) bool {
	s.checkedMu.Lock()
	defer s.checkedMu.Unlock()
	return s.filesChecked[hash]
}

// forgetChecked drops the checked marks of downloads no longer in progress.
func (s *Service) forgetChecked(inProgress []Download) {
	keep := make(map[string]bool, len(inProgress))
	for _, d := range inProgress {
		keep[d.Hash] = true
	}
	s.checkedMu.Lock()
	defer s.checkedMu.Unlock()
	for h := range s.filesChecked {
		if !keep[h] {
			delete(s.filesChecked, h)
		}
	}
}
