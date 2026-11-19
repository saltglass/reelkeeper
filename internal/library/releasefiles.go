package library

import (
	"fmt"
	"path"
	"strings"

	"github.com/saltglass/reelkeeper/internal/parser"
)

// ReleaseFile is one file a torrent says it holds, before anything is
// downloaded: its path inside the torrent ("/" between folders) and size.
type ReleaseFile struct {
	Name string
	Size int64
}

// programExtensions are files that run as programs or scripts when opened.
// No real movie or episode release has one. Fakes on public trackers use
// them ("Movie.2024.1080p.mkv.exe", "Setup.exe" next to a password-locked
// archive), so a release holding any of them is refused before it's
// downloaded. Archives aren't here: a release that is nothing but an archive
// is caught by "no video file" instead, and a subtitle .zip next to the
// video is harmless.
var programExtensions = map[string]string{
	// Windows programs, installers and shortcuts.
	".exe": "a Windows program", ".com": "a Windows program", ".scr": "a Windows program",
	".pif": "a Windows program", ".cpl": "a Windows program", ".dll": "a Windows program",
	".msi": "a Windows installer", ".msp": "a Windows installer", ".msix": "a Windows installer",
	".appx": "a Windows installer", ".application": "a Windows installer",
	".lnk": "a Windows shortcut", ".scf": "a Windows shortcut",
	// Windows scripts.
	".bat": "a Windows script", ".cmd": "a Windows script", ".vbs": "a Windows script",
	".vbe": "a Windows script", ".js": "a Windows script", ".jse": "a Windows script",
	".wsf": "a Windows script", ".wsh": "a Windows script", ".ps1": "a Windows script",
	".psm1": "a Windows script", ".hta": "a Windows script", ".reg": "a Windows registry file",
	// macOS, Android and Java programs.
	".app": "a Mac program", ".dmg": "a Mac installer", ".pkg": "a Mac installer",
	".apk": "an Android app", ".jar": "a Java program",
}

// UnfitRelease says why a torrent's files make it not worth downloading, or
// "" when they're fine:
//   - it holds a program or script (the most likely way a fake does harm), or
//   - it has no movie or episode file the import would take (only samples,
//     extras, archives or other files), so it could never be imported.
//
// It judges by names alone, as the import would see them once downloaded
// (hidden files and extras folders don't count as the video, nor do small
// samples), but a program counts wherever it is, hidden or not. An empty
// list (a magnet link whose details haven't arrived) is "".
func UnfitRelease(files []ReleaseFile) string {
	if len(files) == 0 {
		return ""
	}
	if why := ProgramsIn(files); why != "" {
		return why
	}
	var others []string
	for _, f := range files {
		if isPadding(f.Name) {
			continue
		}
		if mainVideoName(f) {
			return ""
		}
		others = append(others, path.Base(f.Name))
	}
	return "it has no movie or episode file in it, only " + listSome(others, 3)
}

// ProgramsIn is the first half of UnfitRelease alone: why the files include
// a program or script, or "" when none does. Usenet releases are judged by
// this only, since their files are usually archives with scrambled names
// that say nothing about whether a video is inside.
func ProgramsIn(files []ReleaseFile) string {
	var programs []string
	kind := ""
	for _, f := range files {
		name := path.Base(f.Name)
		if isPadding(f.Name) {
			continue
		}
		if k, ok := programExtensions[strings.ToLower(path.Ext(name))]; ok {
			if kind == "" {
				kind = k
			}
			programs = append(programs, name)
		}
	}
	switch len(programs) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("it contains %s, %s, not a video", programs[0], kind)
	}
	return fmt.Sprintf("it contains %d programs or scripts (%s first, %s), not just a video", len(programs), programs[0], kind)
}

// isPadding reports whether a file is BitTorrent padding (BEP 47): hybrid
// torrents list ".pad/1234" files that are never written to disk.
func isPadding(name string) bool {
	parts := strings.Split(name, "/")
	return len(parts) >= 2 && parts[len(parts)-2] == ".pad"
}

// mainVideoName reports whether the import would take a file as a movie or
// episode, by name and size alone (videoFile, for a file not yet on disk).
func mainVideoName(f ReleaseFile) bool {
	parts := strings.Split(f.Name, "/")
	for _, dir := range parts[:len(parts)-1] {
		if skipDirs[strings.ToLower(dir)] || strings.HasPrefix(dir, ".") {
			return false
		}
	}
	name := parts[len(parts)-1]
	if !parser.IsVideo(name) || strings.HasPrefix(name, ".") {
		return false
	}
	base := strings.TrimSuffix(name, path.Ext(name))
	if extraSuffix.MatchString(base) {
		return false
	}
	return !sampleWord.MatchString(base) || f.Size >= 500<<20
}

// listSome names the first few items: "a.zip, b.txt and 3 more files".
func listSome(names []string, n int) string {
	if len(names) <= n {
		switch len(names) {
		case 0:
			return "nothing"
		case 1:
			return names[0]
		}
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
	more := len(names) - n
	word := "files"
	if more == 1 {
		word = "file"
	}
	return fmt.Sprintf("%s and %d more %s", strings.Join(names[:n], ", "), more, word)
}
