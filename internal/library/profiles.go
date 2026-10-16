package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/saltglass/reelkeeper/internal/validation"
)

// Applies-to values. A profile can be for movies, for TV, or for both.
const appliesBoth = "both"

// maxProfileName keeps names short enough for badges and filter menus.
const maxProfileName = 40

// ProfileUsage counts what refers to a profile, so the UI can explain why a
// profile can't be deleted or narrowed without extra calls.
type ProfileUsage struct {
	MediaFolders int // root folders that file their media under this profile
	Movies       int // movie versions using it
	Series       int // series versions using it (TV arrives in M2)
}

// ProfileView is a profile plus its usage.
type ProfileView struct {
	Profile
	Usage ProfileUsage
}

// fits reports whether a profile that applies to appliesTo can be used for
// the media type (movie or series).
func fits(appliesTo, mediaType string) bool {
	return appliesTo == appliesBoth || appliesTo == mediaType
}

// ProfilesWithUsage lists the profiles with how much each one is used.
func (s *Service) ProfilesWithUsage(ctx context.Context) ([]ProfileView, error) {
	ps, err := s.Profiles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ProfileView, 0, len(ps))
	for _, p := range ps {
		u, err := s.profileUsage(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, ProfileView{Profile: p, Usage: u})
	}
	return out, nil
}

// ProfileUsageOf counts what uses one profile.
func (s *Service) ProfileUsageOf(ctx context.Context, id int64) (ProfileUsage, error) {
	return s.profileUsage(ctx, id)
}

func (s *Service) profileUsage(ctx context.Context, id int64) (ProfileUsage, error) {
	var u ProfileUsage
	folders, err := s.db.DB.NewSelect().Model((*RootFolder)(nil)).Where("default_profile_id = ?", id).Count(ctx)
	if err != nil {
		return u, err
	}
	u.MediaFolders = int(folders)
	if u.Movies, err = s.versionsOfType(ctx, id, Movie); err != nil {
		return u, err
	}
	u.Series, err = s.versionsOfType(ctx, id, Series)
	return u, err
}

func (s *Service) profile(ctx context.Context, id int64) (*Profile, error) {
	p := new(Profile)
	err := s.db.DB.NewSelect().Model(p).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// validateProfile checks a name and applies-to, ignoring the profile with
// ID except (0 when creating) when looking for duplicate names. It returns
// the trimmed name.
func (s *Service) validateProfile(ctx context.Context, except int64, name, appliesTo string) (string, error) {
	var errs validation.Errors
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		errs.Add("name", "Give the profile a name, for example HD or Remux.")
	case utf8.RuneCountInString(name) > maxProfileName:
		errs.Add("name", fmt.Sprintf("Keep the name to %d characters or fewer.", maxProfileName))
	default:
		all, err := s.Profiles(ctx)
		if err != nil {
			return "", err
		}
		for _, p := range all {
			if p.ID != except && strings.EqualFold(p.Name, name) {
				errs.Add("name", fmt.Sprintf("There is already a profile called %s.", p.Name))
			}
		}
	}
	switch appliesTo {
	case Movie, Series, appliesBoth:
	default:
		errs.Add("appliesTo", "Choose Movies, TV, or Movies and TV.")
	}
	return name, errs.Err()
}

// CreateProfile adds a profile.
//
// The slug is made once, here, and never changes (not even on rename): later
// it becomes part of Seerr's compat URLs (/compat/radarr/<slug>), and
// changing it would silently break that setup. Users never edit it.
func (s *Service) CreateProfile(ctx context.Context, name, appliesTo string) (*Profile, error) {
	name, err := s.validateProfile(ctx, 0, name, appliesTo)
	if err != nil {
		return nil, err
	}
	all, err := s.Profiles(ctx)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, p := range all {
		taken[p.Slug] = true
	}
	base := slugify(name)
	slug := base
	for n := 2; taken[slug]; n++ {
		slug = fmt.Sprintf("%s-%d", base, n)
	}
	p := &Profile{Name: name, Slug: slug, AppliesTo: appliesTo, CreatedAt: s.now().UTC(),
		Mode: ModeSimple, Simple: "{}", Advanced: "{}"}
	if _, err := s.db.DB.NewInsert().Model(p).Exec(ctx); err != nil {
		return nil, err
	}
	s.logger.Info("quality profile added", "name", name, "slug", slug)
	return p, nil
}

// slugify turns a name into lowercase a-z0-9 words joined by single hyphens.
func slugify(name string) string {
	var b strings.Builder
	pendingHyphen := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingHyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingHyphen = false
			b.WriteRune(r)
		} else {
			pendingHyphen = true
		}
	}
	if b.Len() == 0 {
		return "profile"
	}
	return b.String()
}

// UpdateProfile renames a profile or changes what it applies to. The slug
// stays as it was.
func (s *Service) UpdateProfile(ctx context.Context, id int64, name, appliesTo string) (*Profile, error) {
	p, err := s.profile(ctx, id)
	if err != nil {
		return nil, err
	}
	name, err = s.validateProfile(ctx, id, name, appliesTo)
	if err != nil {
		return nil, err
	}
	// Narrowing must not strand something that already uses the profile.
	var errs validation.Errors
	for _, mt := range []string{Movie, Series} {
		if fits(appliesTo, mt) {
			continue
		}
		folders, err := s.foldersOfType(ctx, id, mt)
		if err != nil {
			return nil, err
		}
		titles, err := s.versionsOfType(ctx, id, mt)
		if err != nil {
			return nil, err
		}
		if folders > 0 || titles > 0 {
			errs.Add("appliesTo", fmt.Sprintf("%s is used by %s, so it has to keep applying to %s. %s",
				p.Name, usageText(folders, titles, mt), mediaWord(mt), howToFree))
		}
	}
	if errs != nil {
		return nil, errs
	}
	p.Name, p.AppliesTo = name, appliesTo
	if _, err := s.db.DB.NewUpdate().Model(p).Column("name", "applies_to").WherePK().Exec(ctx); err != nil {
		return nil, err
	}
	s.onChange() // movie views show the profile name
	return p, nil
}

func (s *Service) foldersOfType(ctx context.Context, profileID int64, mediaType string) (int, error) {
	n, err := s.db.DB.NewSelect().Model((*RootFolder)(nil)).
		Where("default_profile_id = ? AND media_type = ?", profileID, mediaType).Count(ctx)
	return int(n), err
}

func (s *Service) versionsOfType(ctx context.Context, profileID int64, mediaType string) (int, error) {
	n, err := s.db.DB.NewSelect().Model((*Version)(nil)).
		Join("JOIN titles t ON t.id = version.title_id").
		Where("version.profile_id = ? AND t.media_type = ?", profileID, mediaType).Count(ctx)
	return int(n), err
}

// howToFree tells the user how to stop a profile being used: a media
// folder's movies move with it when it switches profile.
const howToFree = "Switch those media folders to another profile under Settings → Media Folders first; their movies move with them."

func mediaWord(mediaType string) string {
	if mediaType == Movie {
		return "movies"
	}
	return "TV"
}

// usageText words a usage like "2 media folders and 14 movies".
func usageText(folders, titles int, mediaType string) string {
	var parts []string
	if folders > 0 {
		parts = append(parts, plural(folders, "media folder", "media folders"))
	}
	if titles > 0 {
		if mediaType == Movie {
			parts = append(parts, plural(titles, "movie", "movies"))
		} else {
			parts = append(parts, plural(titles, "series", "series"))
		}
	}
	return joinWithAnd(parts)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// DeleteProfile removes a profile nothing uses. Nothing is ever moved or
// deleted on the user's behalf, and the last profile always stays.
func (s *Service) DeleteProfile(ctx context.Context, id int64) error {
	p, err := s.profile(ctx, id)
	if err != nil {
		return err
	}
	total, err := s.db.DB.NewSelect().Model((*Profile)(nil)).Count(ctx)
	if err != nil {
		return err
	}
	if total <= 1 {
		return validation.Errors{"profile": "Reelkeeper needs at least one quality profile, so the last one can't be deleted."}
	}
	u, err := s.profileUsage(ctx, id)
	if err != nil {
		return err
	}
	if u.MediaFolders > 0 || u.Movies > 0 || u.Series > 0 {
		parts := []string{}
		if u.MediaFolders > 0 {
			parts = append(parts, plural(u.MediaFolders, "media folder", "media folders"))
		}
		if u.Movies > 0 {
			parts = append(parts, plural(u.Movies, "movie", "movies"))
		}
		if u.Series > 0 {
			parts = append(parts, plural(u.Series, "series", "series"))
		}
		return validation.Errors{"profile": fmt.Sprintf("%s is used by %s. %s", p.Name, joinWithAnd(parts), howToFree)}
	}
	if _, err := s.db.DB.NewDelete().Model((*Profile)(nil)).Where("id = ?", id).Exec(ctx); err != nil {
		return err
	}
	s.logger.Info("quality profile deleted", "name", p.Name)
	return nil
}

func joinWithAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}
