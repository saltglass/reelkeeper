// Package torrent holds Reelkeeper's own list of download states. Each download
// client reports states in its own words and translates them into these;
// everything else in Reelkeeper (cleanup, the Queue page, the
// Radarr/Sonarr-compatible API) reads only these, so it doesn't depend on
// which client is in use. Usenet jobs (2.0) use the same list: the torrent
// states that mean the same thing (downloading, queued, paused, checking,
// moving), plus a few of their own at the end.
package torrent

// State is what a torrent is doing right now.
type State string

// The states. Every one is also what the API sends to the web app.
const (
	// Downloading: fetching data from peers.
	Downloading State = "downloading"
	// Stalled: wants data, but no peer is sending any.
	Stalled State = "stalled"
	// Metadata: getting the torrent's details (a magnet link has none yet).
	Metadata State = "metadata"
	// Queued: waiting in the client's queue for its turn to download.
	Queued State = "queued"
	// Paused: stopped by someone before it finished.
	Paused State = "paused"
	// Checking: the client is checking or preparing its files.
	Checking State = "checking"
	// Moving: the client is moving its files to another folder.
	Moving State = "moving"
	// Seeding: finished and sharing (or waiting its turn to share).
	Seeding State = "seeding"
	// SeedingStalled: finished, but nobody wants it right now.
	SeedingStalled State = "seeding-stalled"
	// SeedingPaused: finished and stopped.
	SeedingPaused State = "seeding-paused"
	// Error: the client reports a problem with it.
	Error State = "error"
	// MissingFiles: the client can't find its files on disk.
	MissingFiles State = "missing-files"
	// Unknown: the client's state isn't one Reelkeeper recognises.
	Unknown State = "unknown"

	// Usenet jobs only.
	//
	// Repairing: downloaded; the Usenet client is checking the files with
	// their par2 repair files, and repairing them if articles were missing.
	Repairing State = "repairing"
	// Unpacking: downloaded and checked; the files are being unpacked.
	Unpacking State = "unpacking"
	// Completed: finished, with nothing to seed.
	Completed State = "completed"
	// Failed: the Usenet client gave up on it (too many missing articles, a
	// failed repair or unpack, ...); the download's message says why.
	Failed State = "failed"
)
