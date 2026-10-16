package download

import "errors"

// Source is what a client needs to be given a torrent it hasn't got: the
// .torrent file, or a magnet link when the file isn't available.
type Source struct {
	// Torrent is the .torrent file's content; empty for a magnet.
	Torrent []byte
	// Magnet is a magnet link; empty for a .torrent file.
	Magnet string
}

// Kind is "torrent" or "magnet", as stored with a download.
func (s Source) Kind() string {
	if len(s.Torrent) > 0 {
		return SourceTorrent
	}
	return SourceMagnet
}

// Empty reports whether there is nothing to give a client.
func (s Source) Empty() bool { return len(s.Torrent) == 0 && s.Magnet == "" }

// The kinds of Source (stored; never rename).
const (
	SourceTorrent = "torrent"
	SourceMagnet  = "magnet"
)

// NoSourceError means a client can't give a torrent's file or magnet link
// back, and says why in plain words (it ends up in History).
type NoSourceError struct{ Reason string }

func (e *NoSourceError) Error() string { return e.Reason }

// ErrNoSource matches any NoSourceError.
var ErrNoSource = errors.New("the client can't give this torrent back")

// Is lets errors.Is(err, ErrNoSource) match.
func (e *NoSourceError) Is(target error) bool { return target == ErrNoSource }

// File is one file of a torrent as a client lists it.
type File struct {
	// Name is its path inside the torrent, as the clients list it: the
	// torrent's folder (for a torrent of several files) and the file's name,
	// with "/" between. It's relative to the folder the torrent is saved in.
	Name string
	Size int64
}

// Layout is where a client keeps a torrent's files.
type Layout struct {
	// Files are the torrent's files.
	Files []File
	// Folder is the folder the client saves the torrent in once it has
	// finished: the files' names are relative to it.
	Folder string
	// Look are the folders the files may be in right now, most likely first:
	// Folder, and wherever the client keeps unfinished files.
	Look []string
}

// unfinishedSuffixes are what a client may add to the name of a file it has
// not finished: Transmission's ".part" and qBittorrent's ".!qB". The plain name
// comes first; Deluge always uses it.
var unfinishedSuffixes = []string{"", ".part", ".!qB"}
