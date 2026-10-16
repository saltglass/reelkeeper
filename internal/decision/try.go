package decision

import "github.com/saltglass/reelkeeper/internal/parser"

// TryName is the profile page's "test a release name" box: what the
// profile thinks of a name on its own. With no movie to compare against,
// the movie is taken to be the one the name claims, so only the profile's
// own rules are checked. originalLanguage (a Radarr language ID) is the
// language a name without a language tag is taken to be in; runtime
// (minutes) is used for size limits when size is given.
func TryName(p *Profile, name string, size int64, runtime, originalLanguage int) Decision {
	r := parser.Parse(name)
	m := Movie{Title: r.Title, Year: r.Year, Runtime: runtime, OriginalLanguage: originalLanguage}
	return Decide(p, m, Candidate{Name: name, Size: size}, nil)
}
