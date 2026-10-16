// Package decision decides which releases a quality profile accepts, and
// which of the accepted ones is best.
//
// A profile is described two ways:
//
//   - Simple settings (simple.go): plain choices such as "1080p or 2160p",
//     "Blu-ray or better", "block Dolby Vision without HDR fallback". This
//     is what most people edit.
//   - The compiled form (profile.go): Radarr's model of allowed qualities,
//     an "upgrade until" quality, and custom formats with scores. Simple
//     settings compile into it, and "Advanced" edits it directly. Custom
//     formats use Radarr's JSON, so TRaSH Guides formats paste in unchanged.
//
// The engine (decide.go) only ever works on the compiled form, so the two
// views can't disagree about what a profile does. Every rejection carries a
// reason in plain words, for History and the "test a release name" box.
//
// Everything here is a pure function of its inputs: no database, no
// network. Callers load profiles and look up blocklists.
package decision

import (
	"strings"

	"github.com/saltglass/reelkeeper/internal/parser"
)

// Source is where a release came from, in Radarr's numbering (custom
// formats from TRaSH Guides refer to sources by these numbers).
type Source int

// Sources, as Radarr numbers them.
const (
	SourceUnknown Source = iota
	SourceCam
	SourceTeleSync
	SourceTeleCine
	SourceWorkprint
	SourceDVD
	SourceTV
	SourceWebDL
	SourceWebRip
	SourceBluRay
)

// Modifier sets a quality apart from others with the same source and
// resolution, in Radarr's numbering.
type Modifier int

// Modifiers, as Radarr numbers them.
const (
	ModifierNone Modifier = iota
	ModifierRegional
	ModifierScreener
	ModifierRawHD
	ModifierBRDisk
	ModifierRemux
)

// Quality is one of Radarr's fixed qualities, such as Bluray-1080p. IDs and
// names match Radarr's exactly: the compatible API reports them, and
// profiles imported from Radarr refer to them.
type Quality struct {
	ID         int
	Name       string
	Source     Source
	Resolution int
	Modifier   Modifier
}

// Qualities lists every quality, worst first, in Radarr's default order.
// A profile reorders and groups them as it likes; this order is only the
// starting point.
var Qualities = []Quality{
	{0, "Unknown", SourceUnknown, 0, ModifierNone},
	{24, "WORKPRINT", SourceWorkprint, 0, ModifierNone},
	{25, "CAM", SourceCam, 0, ModifierNone},
	{26, "TELESYNC", SourceTeleSync, 0, ModifierNone},
	{27, "TELECINE", SourceTeleCine, 0, ModifierNone},
	{29, "REGIONAL", SourceDVD, 480, ModifierRegional},
	{28, "DVDSCR", SourceDVD, 480, ModifierScreener},
	{1, "SDTV", SourceTV, 480, ModifierNone},
	{2, "DVD", SourceDVD, 0, ModifierNone},
	{23, "DVD-R", SourceDVD, 480, ModifierRemux},
	{8, "WEBDL-480p", SourceWebDL, 480, ModifierNone},
	{12, "WEBRip-480p", SourceWebRip, 480, ModifierNone},
	{20, "Bluray-480p", SourceBluRay, 480, ModifierNone},
	{21, "Bluray-576p", SourceBluRay, 576, ModifierNone},
	{4, "HDTV-720p", SourceTV, 720, ModifierNone},
	{5, "WEBDL-720p", SourceWebDL, 720, ModifierNone},
	{14, "WEBRip-720p", SourceWebRip, 720, ModifierNone},
	{6, "Bluray-720p", SourceBluRay, 720, ModifierNone},
	{9, "HDTV-1080p", SourceTV, 1080, ModifierNone},
	{3, "WEBDL-1080p", SourceWebDL, 1080, ModifierNone},
	{15, "WEBRip-1080p", SourceWebRip, 1080, ModifierNone},
	{7, "Bluray-1080p", SourceBluRay, 1080, ModifierNone},
	{30, "Remux-1080p", SourceBluRay, 1080, ModifierRemux},
	{16, "HDTV-2160p", SourceTV, 2160, ModifierNone},
	{18, "WEBDL-2160p", SourceWebDL, 2160, ModifierNone},
	{17, "WEBRip-2160p", SourceWebRip, 2160, ModifierNone},
	{19, "Bluray-2160p", SourceBluRay, 2160, ModifierNone},
	{31, "Remux-2160p", SourceBluRay, 2160, ModifierRemux},
	{22, "BR-DISK", SourceBluRay, 1080, ModifierBRDisk},
	{10, "Raw-HD", SourceTV, 1080, ModifierRawHD},
}

var qualityByName = func() map[string]Quality {
	m := make(map[string]Quality, len(Qualities))
	for _, q := range Qualities {
		m[strings.ToLower(q.Name)] = q
	}
	return m
}()

// QualityByName finds a quality by its Radarr name, ignoring case.
func QualityByName(name string) (Quality, bool) {
	q, ok := qualityByName[strings.ToLower(name)]
	return q, ok
}

// QualityOf is the quality a parsed name claims. The parser already names
// qualities the way Radarr does; anything it can't place is Unknown.
func QualityOf(r parser.Release) Quality {
	if q, ok := QualityByName(r.Quality()); ok {
		return q
	}
	return Qualities[0]
}
