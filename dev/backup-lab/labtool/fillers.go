//go:build lab

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/saltglass/reelkeeper/internal/secrets"
)

// Hints for columns whose values the app expects to be one of a few words.
var enumHints = map[string][]string{
	"cleanup_events.action":   {"removed", "would-remove", "emptied", "would-empty"},
	"cleanup_events.rule":     {"seeded", "stalled", "slow", "metadata", "unregistered", "missing", "import-failed", "replaced-files", "space"},
	"scan_items.status":       {"needs_review", "ignored"},
	"root_folders.media_type": {"movie", "series"},
	"titles.media_type":       {"movie", "series"},
	"profiles.applies_to":     {"both", "movie", "series"},
	"profiles.mode":           {"simple"},
	"indexers.kind":           {"torznab", "newznab"},
	"jobs.status":             {"succeeded", "failed", "cancelled"},
	"jobs.triggered_by":       {"manual", "schedule", "system", "api"},
	"backfill.state":          {"queued", "decision", "done"},
	"downloads.status":        {"imported", "removed", "import-failed"},
}

var intHints = map[string]func(*seeder) int64{}
var textHints = map[string]func(*seeder, int) string{}

type filler func(s *seeder, t *tableInfo, row rec, i int)

func (s *seeder) loadProfiles() {
	if s.profilesFor["both"] != nil {
		return
	}
	s.profilesFor["both"] = []int64{}
	rows, err := s.d.sql.QueryContext(s.ctx, "SELECT id, applies_to FROM profiles")
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var at string
		if rows.Scan(&id, &at) != nil {
			continue
		}
		for _, mt := range []string{"movie", "series"} {
			if at == "both" || at == mt {
				s.profilesFor[mt] = append(s.profilesFor[mt], id)
			}
		}
	}
}

func (s *seeder) prepareTable(t *tableInfo) {
	switch t.Name {
	case "root_folders", "versions":
		s.loadProfiles()
	}
}

func (s *seeder) remember(t *tableInfo, row rec) {
	id, _ := row["id"].(int64)
	switch t.Name {
	case "titles":
		r := rec{"id": id, "media_type": row["media_type"], "title": row["title"]}
		if row["media_type"] == "series" {
			s.seriesTitles = append(s.seriesTitles, r)
		} else {
			s.moviesTitles = append(s.moviesTitles, r)
		}
		s.keep["titles"] = append(s.keep["titles"], r)
	case "root_folders":
		mt, _ := row["media_type"].(string)
		s.rootsByType[mt] = append(s.rootsByType[mt], rec{"id": id})
	case "versions":
		title := s.byIDTitle(row["title_id"])
		r := rec{"id": id, "title_id": row["title_id"], "media_type": title["media_type"], "root_folder_id": row["root_folder_id"],
			"folder": row["folder"]}
		if title["media_type"] == "series" {
			s.seriesVers = append(s.seriesVers, r)
		} else {
			s.movieVersions = append(s.movieVersions, r)
		}
		s.keep["versions"] = append(s.keep["versions"], r)
	case "episodes":
		tid, _ := row["title_id"].(int64)
		s.episodesByTitle[tid] = append(s.episodesByTitle[tid], id)
	case "media_files":
		if row["__tv"] == true {
			s.tvFiles = append(s.tvFiles, rec{"id": id, "title_id": row["__title"], "version_id": row["version_id"]})
		}
	case "downloads":
		if row["category"] == "reelkeeper-tv" {
			s.downloadsTV = append(s.downloadsTV, rec{"id": id, "title_id": row["title_id"]})
		}
	}
}

func (s *seeder) byIDTitle(v any) rec {
	id, _ := v.(int64)
	if s.byID["titles"] == nil {
		s.byID["titles"] = map[int64]rec{}
		for _, r := range s.keep["titles"] {
			s.byID["titles"][r["id"].(int64)] = r
		}
	}
	if r, ok := s.byID["titles"][id]; ok {
		return r
	}
	// titles added since the index was built
	for _, r := range s.keep["titles"] {
		s.byID["titles"][r["id"].(int64)] = r
	}
	return s.byID["titles"][id]
}

func randRec(s *seeder, l []rec) rec {
	if len(l) == 0 {
		return nil
	}
	return l[s.intn(len(l))]
}

func (s *seeder) jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func (s *seeder) list(n int, f func() string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = f()
	}
	return out
}

var (
	sources      = []string{"Bluray", "WEB-DL", "WEBRip", "HDTV", "Remux", "DVD", ""}
	qualities    = []string{"Bluray-1080p", "WEBDL-1080p", "WEBDL-2160p", "HDTV-720p", "Remux-2160p", "WEBRip-480p", "Unknown"}
	groups       = []string{"FLUX", "NTb", "SPARKS", "CMRG", "TEPES", "", "É-Group", "Ünïcode"}
	resolutions  = []int64{0, 480, 576, 720, 1080, 2160, 4320}
	historyKinds = []struct {
		name string
		w    int
	}{{"searched", 45}, {"rss", 20}, {"would-grab", 5}, {"grabbed", 8}, {"grab-skipped", 5}, {"imported", 7},
		{"import-failed", 3}, {"removed", 3}, {"backfill", 2}, {"from-radarr", 1}, {"from-sonarr", 1}}
)

func (s *seeder) weighted(kinds []struct {
	name string
	w    int
}) string {
	total := 0
	for _, k := range kinds {
		total += k.w
	}
	r := s.intn(total)
	for _, k := range kinds {
		if r < k.w {
			return k.name
		}
		r -= k.w
	}
	return kinds[0].name
}

func (s *seeder) releaseName(i int) string {
	return fmt.Sprintf("%s.%s.%d.%s.%s.x264-%s", strings.ReplaceAll(s.phrase(2), " ", "."), s.word(), 1980+s.intn(46),
		s.pickStr([]string{"1080p", "2160p", "720p"}), s.pickStr([]string{"BluRay", "WEB-DL", "HDTV"}), s.pickStr(groups))
}

func (s *seeder) verdict(i int) map[string]any {
	v := map[string]any{"name": s.releaseName(i), "indexer": fmt.Sprintf("Lab Indexer %d", 1+s.intn(12)), "size": int64(s.intn(60_000_000_000)),
		"seeders": s.intn(2000), "quality": s.pickStr(qualities), "score": s.intn(3000) - 200, "accepted": s.chance(0.5)}
	if s.chance(0.3) {
		v["upgrade"] = true
	}
	if s.chance(0.5) {
		v["rejections"] = s.list(1+s.intn(2), func() string { return "Rejected: " + s.text() })
	}
	if s.chance(0.3) {
		v["formats"] = []any{map[string]any{"name": "x265 (HD)", "score": -10000}, map[string]any{"name": "Repack/Proper", "score": 5}}
	}
	return v
}

var fillers map[string]filler

func init() {
	fillers = map[string]filler{
		"root_folders": func(s *seeder, t *tableInfo, row rec, i int) {
			mt := "movie"
			if i >= 4 {
				mt = "series"
			}
			row["media_type"] = mt
			row["path"] = fmt.Sprintf("/lab/media/%s-%d", mt, i)
			if p := s.profilesFor[mt]; len(p) > 0 {
				row["default_profile_id"] = p[s.intn(len(p))]
			}
			s.set(t, row, "manage_files", false) // the lab never lets the app touch files
			s.set(t, row, "created_at", s.recentTime(900))
			s.set(t, row, "last_scan_at", nil)
		},
		"profiles": func(s *seeder, t *tableInfo, row rec, i int) {
			row["name"] = fmt.Sprintf("Lab Profile %d ✨", i)
			row["slug"] = fmt.Sprintf("lab-profile-%d", i)
			row["applies_to"] = []string{"both", "movie", "series"}[i%3]
			row["created_at"] = s.recentTime(900)
			s.set(t, row, "mode", "simple")
			s.set(t, row, "simple", "{}")
			s.set(t, row, "advanced", "{}")
		},
		"titles": func(s *seeder, t *tableInfo, row rec, i int) {
			series := i%11 == 10
			mt := "movie"
			if series {
				mt = "series"
			}
			title := s.title(i)
			row["media_type"] = mt
			row["tmdb_id"] = int64(1000 + i)
			row["imdb_id"] = fmt.Sprintf("tt%07d", 100000+i)
			if s.chance(0.1) {
				row["imdb_id"] = ""
			}
			row["tvdb_id"] = int64(0)
			if series {
				row["tvdb_id"] = int64(70000 + i)
			}
			row["title"], row["original_title"] = title, title
			if s.chance(0.3) {
				row["original_title"] = s.text()
			}
			row["sort_title"] = strings.ToLower(title)
			row["year"] = int64(1900 + s.intn(131))
			if s.chance(0.01) {
				row["year"] = int64(0)
			}
			row["overview"] = s.text()
			if i == 7 {
				row["overview"] = strings.Repeat("A very long overview with ünïcödé and emoji 🎬. ", 8000) // ~380 KB
			}
			row["runtime"] = int64(s.intn(241))
			row["status"] = s.pickStr([]string{"Released", "Ended", "Returning Series", "Planned", "Canceled", ""})
			row["genres"] = s.jsonOf(s.list(s.intn(5), func() string { return s.pickStr(genres) }))
			row["alt_titles"] = s.jsonOf(s.list(s.intn(4), s.text))
			row["collection"] = s.pickStr([]string{"", "", "", s.word() + " Collection"})
			row["certification"] = s.pickStr([]string{"", "PG", "PG-13", "R", "TV-MA", "TV-14"})
			for _, c := range []string{"release_date", "theatrical_release", "digital_release", "physical_release"} {
				v := s.date()
				if s.chance(0.3) {
					v = ""
				}
				s.set(t, row, c, v)
			}
			row["poster_path"] = fmt.Sprintf("/poster%d.jpg", i)
			row["backdrop_path"] = fmt.Sprintf("/backdrop%d.jpg", i)
			row["popularity"] = s.float()
			row["vote_average"] = math.Round(s.rng.Float64()*100) / 10
			if s.chance(0.04) {
				row["vote_average"] = s.float()
			}
			row["metadata_updated_at"] = s.recentTime(20)
			row["created_at"] = s.recentTime(700)
			s.set(t, row, "original_language", s.pickStr([]string{"en", "fr", "ja", "de", "", "ko"}))
			s.set(t, row, "network", "")
			s.set(t, row, "series_type", "")
			s.set(t, row, "last_air_date", "")
			s.set(t, row, "episode_order", "")
			s.set(t, row, "episode_order_name", "")
			if series {
				s.set(t, row, "network", s.pickStr([]string{"HBO", "BBC One", "Netflix", "NHK", ""}))
				s.set(t, row, "series_type", s.pickStr([]string{"standard", "standard", "daily"}))
				s.set(t, row, "last_air_date", s.date())
			}
		},
		"versions": func(s *seeder, t *tableInfo, row rec, i int) {
			all := s.keep["titles"]
			var title rec
			if i < len(all) {
				title = all[i]
			} else {
				title = all[s.intn(len(all))]
			}
			mt, _ := title["media_type"].(string)
			row["title_id"] = title["id"]
			if p := s.profilesFor[mt]; len(p) > 0 {
				row["profile_id"] = p[s.intn(len(p))]
			}
			if r := randRec(s, s.rootsByType[mt]); r != nil {
				row["root_folder_id"] = r["id"]
			}
			row["folder"] = fmt.Sprintf("%v (%d) {tmdb-%d}", title["title"], 1900+s.intn(130), i)
			row["monitored"] = s.chance(0.8)
			row["created_at"] = s.recentTime(700)
			row["last_search_at"] = nil
			if s.chance(0.6) {
				row["last_search_at"] = s.recentTime(30)
			}
			s.set(t, row, "monitor_from", nil)
			if mt == "series" && s.chance(0.7) {
				s.set(t, row, "monitor_from", s.date())
			}
		},
		"seasons": func(s *seeder, t *tableInfo, row rec, i int) {
			n := len(s.seriesTitles)
			if n == 0 {
				row["__skip"] = true
				return
			}
			row["title_id"] = s.seriesTitles[i%n]["id"]
			season := i / n
			row["season_number"] = int64(season)
			row["name"] = fmt.Sprintf("Season %d", season)
			if season == 0 {
				row["name"] = "Specials"
			}
			row["air_date"] = s.date()
			row["poster_path"] = fmt.Sprintf("/season%d.jpg", i)
		},
		"episodes": func(s *seeder, t *tableInfo, row rec, i int) {
			n := len(s.seriesTitles)
			if n == 0 {
				row["__skip"] = true
				return
			}
			row["title_id"] = s.seriesTitles[i%n]["id"]
			j := i / n
			season, number := int64(j/12), int64(j%12+1)
			row["aired_season"], row["aired_number"] = season, number
			switch {
			case s.chance(0.05):
				row["season"], row["number"] = nil, nil
			case s.chance(0.05):
				row["season"], row["number"] = season+1, number+int64(s.intn(5))
			default:
				row["season"], row["number"] = season, number
			}
			row["tmdb_id"] = int64(500000 + i)
			row["name"] = s.text()
			row["overview"] = s.text()
			row["air_date"] = s.date()
			if s.chance(0.1) {
				row["air_date"] = ""
			}
			row["runtime"] = int64(s.intn(120))
			row["still_path"] = fmt.Sprintf("/still%d.jpg", i)
			row["episode_type"] = s.pickStr([]string{"", "standard", "finale", "mid_season"})
		},
		"media_files": func(s *seeder, t *tableInfo, row rec, i int) {
			nm := min(len(s.movieVersions), s.rowsFor("media_files")/10)
			var v rec
			tv := false
			if i < nm {
				v = s.movieVersions[i]
			} else {
				v = randRec(s, s.seriesVers)
				tv = true
				if v == nil {
					v = randRec(s, s.movieVersions)
					tv = false
				}
			}
			if v == nil {
				row["__skip"] = true
				return
			}
			row["version_id"], row["root_folder_id"] = v["id"], v["root_folder_id"]
			row["path"] = fmt.Sprintf("%v/File %d [%s].mkv", v["folder"], i, s.word())
			size := int64(s.intn(80_000_000_000))
			switch i {
			case 33:
				size = 1 << 60 // (a max-int64 here would make the app's own SUM(size) overflow)
			case 34:
				size = 0
			}
			row["size"] = size
			row["modified_at"] = s.randTime()
			row["resolution"] = resolutions[s.intn(len(resolutions))]
			row["source"] = s.pickStr(sources)
			row["quality"] = s.pickStr(qualities)
			row["edition"] = s.pickStr([]string{"", "", "Director's Cut", "Extended", "IMAX"})
			row["release_group"] = s.pickStr(groups)
			row["release_info"] = s.jsonOf(map[string]any{"title": s.title(i), "year": 1980 + s.intn(45), "resolution": s.pickStr([]string{"1080p", "2160p"}),
				"group": s.pickStr(groups), "languages": s.list(s.intn(3), func() string { return s.pickStr([]string{"English", "French", "Japanese"}) })})
			row["probe_error"] = ""
			if s.chance(0.12) {
				row["media_info"] = "{}"
				row["probe_error"] = s.text()
			} else {
				row["media_info"] = s.jsonOf(map[string]any{"container": s.pickStr([]string{"mkv", "mp4"}), "duration": int64(s.intn(10_000)) * int64(time.Second),
					"video": map[string]any{"codec": s.pickStr([]string{"H.264", "H.265", "AV1"}), "width": 1920, "height": 1080, "bitDepth": 8 + 2*s.intn(2)},
					"audio": []any{map[string]any{"codec": "DD+", "channels": 6, "language": "eng", "default": true}}})
			}
			row["added_at"] = s.recentTime(700)
			row["probed_at"] = nil
			if s.chance(0.9) {
				row["probed_at"] = s.recentTime(300)
			}
			row["missing_since"] = nil
			if s.chance(0.04) {
				row["missing_since"] = s.recentTime(30)
			}
			s.set(t, row, "episode_claim", "{}")
			s.set(t, row, "match_note", "")
			if tv {
				s.set(t, row, "episode_claim", s.jsonOf(map[string]any{"season": s.intn(10), "episodes": []int{1 + s.intn(20)}}))
				if s.chance(0.03) {
					s.set(t, row, "match_note", s.text())
				}
			}
			row["__tv"], row["__title"] = tv, v["title_id"]
		},
		"episode_files": func(s *seeder, t *tableInfo, row rec, i int) {
			if i >= len(s.tvFiles) {
				row["__skip"] = true
				return
			}
			f := s.tvFiles[i]
			eps := s.episodesByTitle[f["title_id"].(int64)]
			if len(eps) == 0 {
				row["__skip"] = true
				return
			}
			row["media_file_id"] = f["id"]
			row["episode_id"] = eps[s.intn(len(eps))]
		},
		"scan_items": func(s *seeder, t *tableInfo, row rec, i int) {
			row["path"] = fmt.Sprintf("/lab/unsorted/Unknown %d ünï", i)
			row["title_guess"] = s.title(i)
			row["year_guess"] = int64(1950 + s.intn(75))
			row["reason"] = s.text()
			cands := make([]any, s.intn(4))
			for k := range cands {
				cands[k] = map[string]any{"tmdbId": 1 + s.intn(900000), "title": s.title(k), "year": 1950 + s.intn(75), "overview": s.text(),
					"posterPath": "/c.jpg", "score": math.Round(s.rng.Float64()*100) / 100, "votes": s.intn(50000)}
			}
			row["candidates"] = s.jsonOf(cands)
			row["files"] = s.jsonOf(s.list(1+s.intn(3), func() string { return fmt.Sprintf("file %s.mkv", s.word()) }))
			row["created_at"] = s.recentTime(300)
			row["updated_at"] = s.recentTime(300)
		},
		"skipped_folders": func(s *seeder, t *tableInfo, row rec, i int) {
			row["path"] = fmt.Sprintf("Skipped Folder %d", i)
			row["title"] = s.title(i)
			row["created_at"] = s.recentTime(300)
		},
		"history": func(s *seeder, t *tableInfo, row rec, i int) {
			ev := s.weighted(historyKinds)
			row["event"] = ev
			if v := randRec(s, s.keep["versions"]); v != nil && !s.chance(0.06) {
				row["title_id"], row["version_id"] = v["title_id"], v["id"]
			} else {
				row["title_id"], row["version_id"] = nil, nil
			}
			row["created_at"] = s.recentTime(700)
			row["source_title"] = s.releaseName(i)
			row["indexer"] = fmt.Sprintf("Lab Indexer %d", 1+s.intn(12))
			row["quality"] = s.pickStr(qualities)
			row["score"] = int64(s.intn(3200) - 200)
			if s.chance(0.002) {
				row["score"] = int64(math.MaxInt32)
			}
			row["message"] = s.text()
			data := map[string]any{}
			switch ev {
			case "searched", "rss":
				data["trigger"] = s.pickStr([]string{"manual", "added", "scheduled", "rss", "backfill", "request"})
				data["releases"] = func() []any {
					out := make([]any, s.intn(4))
					for k := range out {
						out[k] = s.verdict(k)
					}
					return out
				}()
				data["more"] = s.intn(30)
				if s.chance(0.1) {
					data["indexerErrors"] = []string{"Lab Indexer 3: " + s.text()}
				}
			case "grabbed", "would-grab", "grab-skipped":
				data["trigger"] = "scheduled"
				data["chosen"] = s.verdict(i)
			}
			row["data"] = s.jsonOf(data)
		},
		"downloads": func(s *seeder, t *tableInfo, row rec, i int) {
			v := randRec(s, s.keep["versions"])
			tv := false
			if v != nil && !s.chance(0.03) {
				row["title_id"], row["version_id"] = v["title_id"], v["id"]
				tv = v["media_type"] == "series"
			} else {
				row["title_id"], row["version_id"] = nil, nil
			}
			row["created_at"] = s.recentTime(700)
			row["hash"] = s.hash40(i)
			row["category"] = "reelkeeper-movies"
			if tv {
				row["category"] = "reelkeeper-tv"
			}
			row["release_title"] = s.releaseName(i)
			row["indexer"] = fmt.Sprintf("Lab Indexer %d", 1+s.intn(12))
			row["quality"] = s.pickStr(qualities)
			row["score"] = int64(s.intn(3200) - 200)
			size := int64(s.intn(80_000_000_000))
			switch i {
			case 5:
				size = math.MaxInt64
			case 6:
				size = 0
			}
			row["size"] = size
			row["upgrade"] = s.chance(0.2)
			row["seed_ratio"] = nil
			row["seed_time_minutes"] = nil
			if s.chance(0.6) {
				row["seed_ratio"] = []float64{0.5, 1, 1.5, 2, 0.1 + 0.2}[s.intn(5)]
				row["seed_time_minutes"] = int64(s.intn(20000))
			}
			row["status"] = s.pickStr([]string{"imported", "imported", "imported", "imported", "removed", "import-failed"})
			row["message"] = s.text()
			row["completed_at"] = s.recentTime(700)
			row["imported_at"] = nil
			if row["status"] == "imported" {
				row["imported_at"] = s.recentTime(700)
			}
			row["imported_path"] = ""
			if row["status"] == "imported" {
				row["imported_path"] = fmt.Sprintf("Folder %d/File %d.mkv", i, i)
			}
			s.set(t, row, "cleanup_track", "{}")
			if s.chance(0.2) {
				s.set(t, row, "cleanup_track", s.jsonOf(map[string]any{"strikes": s.intn(3), "lastProgress": s.recentTime(5).Format(time.RFC3339Nano)}))
			}
			s.set(t, row, "limits_set", s.chance(0.7))
			s.set(t, row, "episodes_label", "")
			if tv {
				s.set(t, row, "episodes_label", fmt.Sprintf("S%02dE%02d", 1+s.intn(10), 1+s.intn(24)))
			}
		},
		"blocklist": func(s *seeder, t *tableInfo, row rec, i int) {
			reasons := []struct{ reason, rule string }{
				{"Cleanup: The tracker says this torrent no longer exists (it was removed).", "unregistered"},
				{"Cleanup: qBittorrent reports an error or missing files for this torrent.", "missing"},
				{"Cleanup: It has been trying to get the torrent's details for 3 hours.", "metadata"},
				{"Cleanup: No progress for 12 hours.", "stalled"},
				{"Cleanup: Slower than 10 KB/s for 6 hours.", "slow"},
				{"Cleanup: Its import failed 3 times.", "import-failed"},
				{"Cleanup: some other reason", "cleanup"},
				{"Blocklisted in Radarr (record 1234)", "radarr"},
				{"Blocklisted in Sonarr (record 99)", "sonarr"},
				{"Removed from the queue by an admin", "removed"},
				{"", "removed"},
			}
			r := reasons[s.intn(len(reasons))]
			row["reason"] = r.reason
			row["release_title"] = s.releaseName(i)
			row["hash"] = ""
			if s.chance(0.8) {
				row["hash"] = s.hash40(i + 1_000_000)
			}
			row["indexer"] = fmt.Sprintf("Lab Indexer %d", 1+s.intn(12))
			row["quality"] = s.pickStr(qualities)
			created := s.recentTime(700)
			row["created_at"] = created
			s.set(t, row, "rule", r.rule)
			s.set(t, row, "expires_at", nil)
			if strings.HasPrefix(r.reason, "Cleanup: ") && r.rule != "unregistered" {
				s.set(t, row, "expires_at", created.Add(30*24*time.Hour))
			}
		},
		"download_episodes": func(s *seeder, t *tableInfo, row rec, i int) {
			d := randRec(s, s.downloadsTV)
			if d == nil {
				row["__skip"] = true
				return
			}
			eps := s.episodesByTitle[idOf(d["title_id"])]
			if len(eps) == 0 {
				row["__skip"] = true
				return
			}
			row["download_id"], row["episode_id"] = d["id"], eps[s.intn(len(eps))]
		},
		"episode_monitoring": episodeLink,
		"episode_searches":   episodeLink,
		"tags": func(s *seeder, t *tableInfo, row rec, i int) {
			row["label"] = strings.ToLower(fmt.Sprintf("%d - tag ünï %s", i, s.word()))
		},
		"indexers": func(s *seeder, t *tableInfo, row rec, i int) {
			row["name"] = fmt.Sprintf("Lab Indexer %d", i+1)
			if i%5 == 4 {
				row["name"] = fmt.Sprintf("Lab Indexer %d 🕵️ café", i+1)
			}
			row["kind"] = []string{"torznab", "torznab", "newznab"}[i%3]
			s.set(t, row, "definition", "")
			row["base_url"] = fmt.Sprintf("http://indexer%d.invalid/api", i+1)
			row["enabled"] = i%4 != 3
			row["priority"] = int64(1 + s.intn(50))
			row["use_solver"] = s.chance(0.2)
			row["settings"] = s.jsonOf(map[string]string{"note": s.text(), "freeleech": "true"})
			row["secrets"] = ""
			if s.box != nil {
				sealed, err := s.box.Seal(s.jsonOf(map[string]string{"apiKey": fmt.Sprintf("lab-indexer-key-%d", i+1)}))
				if err == nil {
					row["secrets"] = sealed
				}
			}
			row["categories"] = s.jsonOf([]int{2000, 5000})
			row["seed_ratio"], row["seed_time_minutes"], row["pack_seed_time_minutes"] = nil, nil, nil
			if s.chance(0.7) {
				row["seed_ratio"] = []float64{1, 1.5, 2.5, 0.1 + 0.2}[s.intn(4)]
				row["seed_time_minutes"] = int64(60 * s.intn(200))
				row["pack_seed_time_minutes"] = int64(60 * s.intn(300))
			}
			row["query_limit"], row["grab_limit"] = nil, nil
			if s.chance(0.3) {
				row["query_limit"], row["grab_limit"] = int64(100+s.intn(900)), int64(s.intn(50))
			}
			row["failures"] = int64(s.intn(4))
			row["retry_after"] = nil
			row["last_error"] = ""
			if row["failures"].(int64) > 0 {
				row["last_error"] = "lookup indexer.invalid: no such host"
				row["retry_after"] = s.recentTime(1)
			}
			row["last_success_at"] = s.recentTime(30)
			row["last_failure_at"] = nil
			row["created_at"] = s.recentTime(700)
			row["updated_at"] = s.recentTime(30)
		},
		"indexer_usage": func(s *seeder, t *tableInfo, row rec, i int) {
			ids := s.ids["indexers"]
			if len(ids) == 0 {
				row["__skip"] = true
				return
			}
			row["indexer_id"] = ids[i%len(ids)]
			row["day"] = s.now.AddDate(0, 0, -(i / len(ids))).Format("2006-01-02")
			row["queries"], row["grabs"], row["failures"] = int64(s.intn(300)), int64(s.intn(10)), int64(s.intn(5))
			row["total_ms"] = int64(s.intn(3_000_000))
			if i == 1 {
				row["total_ms"] = int64(1 << 60)
			}
		},
		"imported_records": func(s *seeder, t *tableInfo, row rec, i int) {
			srcs := []string{"Radarr/history", "Radarr/blocklist", "Sonarr/history", "Sonarr/blocklist"}
			row["source"], row["external_id"] = srcs[i%4], int64(i/4+1)
		},
		"backfill": func(s *seeder, t *tableInfo, row rec, i int) {
			if i >= len(s.seriesVers) {
				row["__skip"] = true
				return
			}
			row["version_id"] = s.seriesVers[i]["id"]
			row["position"] = int64(i + 1)
			row["state"] = []string{"queued", "decision", "done"}[i%3]
			row["added_at"] = s.recentTime(60)
			row["started_at"], row["finished_at"] = nil, nil
			if i%3 != 0 {
				row["started_at"], row["finished_at"] = s.recentTime(50), s.recentTime(40)
			}
			row["found"], row["missing"] = int64(s.intn(50)), int64(s.intn(50))
			row["hidden"] = s.chance(0.2)
		},
		"jobs": func(s *seeder, t *tableInfo, row rec, i int) {
			kinds := s.kinds
			if len(kinds) == 0 {
				kinds = []string{"library.scan", "download.check", "grab.rss", "indexer.health"}
			}
			row["kind"] = kinds[s.intn(len(kinds))]
			row["args"] = "{}"
			if s.chance(0.3) {
				row["args"] = s.jsonOf(map[string]any{"versionId": s.intn(5000), "note": s.text()})
			}
			row["status"] = s.pickStr([]string{"succeeded", "succeeded", "succeeded", "failed", "cancelled"})
			row["triggered_by"] = s.pickStr([]string{"manual", "schedule", "schedule", "system", "api"})
			row["dedupe_key"] = nil
			row["progress"] = int64(100)
			row["message"] = s.text()
			row["error"] = ""
			if row["status"] == "failed" {
				row["error"] = "Lab: " + s.text()
			}
			row["attempts"] = int64(1 + s.intn(3))
			created := s.recentTime(29)
			row["created_at"], row["started_at"], row["finished_at"] = created, created.Add(time.Second), created.Add(time.Duration(2+s.intn(90))*time.Second)
			s.set(t, row, "quiet", s.chance(0.5))
		},
	}
}

func idOf(v any) int64 {
	n, _ := v.(int64)
	return n
}

func episodeLink(s *seeder, t *tableInfo, row rec, i int) {
	v := randRec(s, s.seriesVers)
	if v == nil {
		row["__skip"] = true
		return
	}
	eps := s.episodesByTitle[idOf(v["title_id"])]
	if len(eps) == 0 {
		row["__skip"] = true
		return
	}
	row["version_id"], row["episode_id"] = v["id"], eps[s.intn(len(eps))]
	s.set(t, row, "searched_at", s.recentTime(60))
}

var _ = secrets.KeyFileName
var _ = context.Background
