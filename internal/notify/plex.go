package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/saltglass/reelkeeper/internal/settings"
)

// Plex has its own API, not Emby's. To scan one folder it needs the number
// ("key") of the library, which Plex calls a section, that holds the folder.
// So Reelkeeper asks for the libraries and the folders each covers, picks the
// one holding the changed folder, and asks it to scan just that folder (never
// the whole library):
//
//	GET /library/sections                           the libraries, as JSON
//	GET /library/sections/{key}/refresh?path={dir}  a scan of one folder
//
// The token goes in the X-Plex-Token header, never in the address, so it
// can't end up in a proxy's log. A deleted or renamed movie leaves Plex after
// the scan only while Plex's "Empty trash automatically after every scan" is
// on (its default); the help text says so.

// plexSection is one Plex library and the folders it covers.
type plexSection struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Location []struct {
		Path string `json:"path"`
	} `json:"Location"`
}

func plexGet(ctx context.Context, c settings.MediaServerNotify, path string, query url.Values) ([]byte, error) {
	u := c.URL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, cleanErr(err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.APIKey)
	return do(req)
}

// plexSections lists Plex's libraries.
func plexSections(ctx context.Context, c settings.MediaServerNotify) ([]plexSection, error) {
	b, err := plexGet(ctx, c, "/library/sections", nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		MediaContainer struct {
			Directory []plexSection `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, errors.New("that address answered, but it doesn't look like Plex")
	}
	return r.MediaContainer.Directory, nil
}

// plexSectionFor is the library whose folder holds dir (a path as Plex sees
// it). The longest match wins: a library can sit inside another's folder.
func plexSectionFor(sections []plexSection, dir string) (plexSection, bool) {
	var best plexSection
	bestLen := -1
	for _, s := range sections {
		for _, loc := range s.Location {
			p := strings.TrimRight(loc.Path, "/")
			if (dir == p || strings.HasPrefix(dir, p+"/")) && len(p) > bestLen {
				best, bestLen = s, len(p)
			}
		}
	}
	return best, bestLen >= 0
}

// sendPlex asks Plex to scan the folder an event is about.
func sendPlex(ctx context.Context, c settings.MediaServerNotify, ev Event) error {
	dir := translatePath(folderOf(ev.Path), c.PathFrom, c.PathTo)
	sections, err := plexSections(ctx, c)
	if err != nil {
		return fmt.Errorf("plex: %w", err)
	}
	sec, ok := plexSectionFor(sections, dir)
	if !ok {
		return fmt.Errorf("plex: no Plex library holds %s, so Plex can't be told about it; check the paths in Settings → Notifications", dir)
	}
	if _, err := plexGet(ctx, c, "/library/sections/"+url.PathEscape(sec.Key)+"/refresh", url.Values{"path": {dir}}); err != nil {
		return fmt.Errorf("plex: %w", err)
	}
	return nil
}

// testPlex checks the address and token, lists Plex's libraries, and says
// which of Reelkeeper's media folders (folders, as Reelkeeper sees them) no
// library holds. None held at all is an error: the paths are surely wrong.
// Some not held is only a note, as a folder can be left out of Plex on
// purpose.
func testPlex(ctx context.Context, c settings.MediaServerNotify, folders []string) (string, error) {
	if c.URL == "" {
		return "", errors.New("enter Plex's address first")
	}
	b, err := plexGet(ctx, c, "/", nil)
	if err != nil {
		return "", fmt.Errorf("couldn't reach Plex: %w", err)
	}
	var root struct {
		MediaContainer struct {
			FriendlyName      string `json:"friendlyName"`
			Version           string `json:"version"`
			MachineIdentifier string `json:"machineIdentifier"`
		} `json:"MediaContainer"`
	}
	if json.Unmarshal(b, &root) != nil || root.MediaContainer.Version == "" || root.MediaContainer.MachineIdentifier == "" {
		return "", errors.New("that address answered, but it doesn't look like Plex")
	}
	server := fmt.Sprintf("Plex %s (%s)", root.MediaContainer.Version, root.MediaContainer.FriendlyName)
	sections, err := plexSections(ctx, c)
	if err != nil {
		return "", fmt.Errorf("connected to %s, but couldn't list its libraries: %w", server, err)
	}
	if len(sections) == 0 {
		return "", fmt.Errorf("connected to %s, but it has no libraries yet; add your movie and TV folders to Plex first", server)
	}
	var names, missing []string
	for _, s := range sections {
		names = append(names, s.Title)
	}
	for _, f := range folders {
		if p := translatePath(f, c.PathFrom, c.PathTo); !hasSection(sections, p) {
			missing = append(missing, p)
		}
	}
	switch {
	case len(folders) > 0 && len(missing) == len(folders):
		return "", fmt.Errorf("connected to %s, but none of its libraries holds %s; check the paths (Plex's own path for each media folder)", server, strings.Join(missing, ", "))
	case len(missing) > 0:
		return fmt.Sprintf("Connected to %s: %s. No Plex library holds %s, so Plex won't hear about changes there.", server, strings.Join(names, ", "), strings.Join(missing, ", ")), nil
	}
	return fmt.Sprintf("Connected to %s: %s.", server, strings.Join(names, ", ")), nil
}

func hasSection(sections []plexSection, dir string) bool {
	_, ok := plexSectionFor(sections, dir)
	return ok
}
