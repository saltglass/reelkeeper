package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// segmentSize is how many bytes of a file go in one article. Real uploads
// use about 700 KB; smaller articles make more of them, which is what the
// "missing articles" cases need.
const segmentSize = 256 << 10

// group is the one newsgroup everything is "posted" to.
const group = "alt.binaries.reelkeeper.test"

// Release is one made-up release: its files, the articles they were split
// into, and what the indexer says about it. It is stored as release.json.
type Release struct {
	Name     string    `json:"name"`
	Category int       `json:"category"` // Newznab: 2000 movies, 5000 TV
	Size     int64     `json:"size"`     // the files' total, par2 included
	Posted   time.Time `json:"posted"`
	Files    []File    `json:"files"`
	// Missing lists articles that were deleted on purpose (the news server
	// answers "no such article" for them).
	Missing []string `json:"missing,omitempty"`
}

// File is one posted file.
type File struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Segments []Segment `json:"segments"`
}

// Segment is one article.
type Segment struct {
	Number    int    `json:"number"`
	Bytes     int    `json:"bytes"` // the article's encoded size
	MessageID string `json:"messageId"`
}

// MakeOptions describes a release to make.
type MakeOptions struct {
	Name     string
	SizeMB   int
	Category int
	// Missing deletes this many articles of the video, spread through it,
	// after posting. par2 can repair a few; too many make the download fail.
	Missing int
	// NoPar2 leaves out the repair files.
	NoPar2 bool
}

// tvName says a release is TV (S01E02, S01).
var tvName = regexp.MustCompile(`(?i)\bS\d{1,2}(E\d{1,3})?\b`)

func releaseDir(data, name string) string { return filepath.Join(data, "releases", name) }
func articlePath(data, msgID string) string {
	sum := sha1.Sum([]byte(msgID))
	h := hex.EncodeToString(sum[:])
	return filepath.Join(data, "articles", h[:2], h)
}

// makeRelease writes a made-up video (random bytes, named like a real one),
// its par2 repair files, the articles for all of them, release.json and the
// .nzb file.
func makeRelease(data string, o MakeOptions) (*Release, error) {
	if o.Name == "" || strings.ContainsAny(o.Name, `/\`) {
		return nil, errors.New("a release needs a name without slashes")
	}
	if o.SizeMB <= 0 {
		o.SizeMB = 20
	}
	if o.Category == 0 {
		o.Category = 2000
		if tvName.MatchString(o.Name) {
			o.Category = 5000
		}
	}
	dir := releaseDir(data, o.Name)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("release %q already exists (delete %s first)", o.Name, dir)
	}
	work, err := os.MkdirTemp("", "fakenews-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)

	// The "video": random bytes, the same for the same name, so a release
	// made twice is the same release.
	video := o.Name + ".mkv"
	var seed [32]byte
	sum := sha1.Sum([]byte(o.Name))
	copy(seed[:], sum[:])
	buf := make([]byte, o.SizeMB<<20)
	_, _ = rand.NewChaCha8(seed).Read(buf)
	if err := os.WriteFile(filepath.Join(work, video), buf, 0o644); err != nil {
		return nil, err
	}
	names := []string{video}
	if !o.NoPar2 {
		// 10% redundancy, the usual amount.
		cmd := exec.Command("par2", "create", "-q", "-r10", "-n3", o.Name+".par2", video)
		cmd.Dir = work
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("par2 create: %v: %s", err, out)
		}
		pars, _ := filepath.Glob(filepath.Join(work, "*.par2"))
		slices.Sort(pars)
		for _, p := range pars {
			names = append(names, filepath.Base(p))
		}
	}

	r := &Release{Name: o.Name, Category: o.Category, Posted: time.Now().UTC().Truncate(time.Second)}
	for _, n := range names {
		f, err := postFile(data, o.Name, filepath.Join(work, n))
		if err != nil {
			return nil, err
		}
		r.Files = append(r.Files, f)
		r.Size += f.Size
	}
	if o.Missing > 0 {
		segs := r.Files[0].Segments
		if o.Missing > len(segs) {
			o.Missing = len(segs)
		}
		step := float64(len(segs)) / float64(o.Missing)
		for i := range o.Missing {
			id := segs[int(float64(i)*step+step/2)].MessageID
			if err := os.Remove(articlePath(data, id)); err != nil {
				return nil, err
			}
			r.Missing = append(r.Missing, id)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	js, _ := json.MarshalIndent(r, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "release.json"), js, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, o.Name+".nzb"), nzbOf(r), 0o644); err != nil {
		return nil, err
	}
	return r, nil
}

// postFile splits one file into articles and stores them.
func postFile(data, release, path string) (File, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	name := filepath.Base(path)
	f := File{Name: name, Size: int64(len(content))}
	total := (len(content) + segmentSize - 1) / segmentSize
	for i := range total {
		begin := i * segmentSize
		part := content[begin:min(begin+segmentSize, len(content))]
		body := yencPart(name, i+1, total, f.Size, int64(begin), part)
		sum := sha1.Sum([]byte(release + "/" + name + fmt.Sprint(i)))
		id := hex.EncodeToString(sum[:12]) + "@fakenews.reelkeeper.test"
		p := articlePath(data, id)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return File{}, err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return File{}, err
		}
		f.Segments = append(f.Segments, Segment{Number: i + 1, Bytes: len(body), MessageID: id})
	}
	return f, nil
}

// The .nzb format (newzbin's DTD, which every client reads).
type nzbDoc struct {
	XMLName xml.Name  `xml:"nzb"`
	Xmlns   string    `xml:"xmlns,attr"`
	Files   []nzbFile `xml:"file"`
}

type nzbFile struct {
	Poster   string       `xml:"poster,attr"`
	Date     int64        `xml:"date,attr"`
	Subject  string       `xml:"subject,attr"`
	Groups   []string     `xml:"groups>group"`
	Segments []nzbSegment `xml:"segments>segment"`
}

type nzbSegment struct {
	Bytes  int    `xml:"bytes,attr"`
	Number int    `xml:"number,attr"`
	ID     string `xml:",chardata"`
}

func nzbOf(r *Release) []byte {
	doc := nzbDoc{Xmlns: "http://www.newzbin.com/DTD/2003/nzb"}
	for i, f := range r.Files {
		nf := nzbFile{
			Poster: "fakenews <fakenews@reelkeeper.test>", Date: r.Posted.Unix(), Groups: []string{group},
			Subject: fmt.Sprintf(`%s [%d/%d] - "%s" yEnc (1/%d)`, r.Name, i+1, len(r.Files), f.Name, len(f.Segments)),
		}
		for _, s := range f.Segments {
			nf.Segments = append(nf.Segments, nzbSegment{Bytes: s.Bytes, Number: s.Number, ID: s.MessageID})
		}
		doc.Files = append(doc.Files, nf)
	}
	out, _ := xml.MarshalIndent(doc, "", "  ")
	return append([]byte(xml.Header+`<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">`+"\n"), out...)
}

// loadReleases reads every release, newest first.
func loadReleases(data string) ([]*Release, error) {
	dirs, err := filepath.Glob(filepath.Join(data, "releases", "*", "release.json"))
	if err != nil {
		return nil, err
	}
	var out []*Release
	for _, p := range dirs {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var r Release
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, &r)
	}
	slices.SortFunc(out, func(a, b *Release) int { return b.Posted.Compare(a.Posted) })
	return out, nil
}
