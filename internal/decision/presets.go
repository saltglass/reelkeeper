package decision

// Preset is a ready-made set of simple settings.
type Preset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Settings    Simple `json:"settings"`
}

// cleanRelease blocks what nobody wants, and prefers releases from
// well-known groups, in every preset.
func cleanRelease(extra map[string]Choice) map[string]Choice {
	m := map[string]Choice{
		"lq": Block, "junk": Block, "br-disk": Block, "extras": Block,
		"3d": Block, "upscaled": Block, "no-group": Avoid, "good-groups": Prefer,
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// Presets are the starting points offered when creating a profile. They're
// general-purpose, loosely following TRaSH Guides; nothing here is copied
// from any one person's setup.
var Presets = []Preset{
	{
		ID: "hd", Name: "HD",
		Description: "1080p from web or Blu-ray, upgraded to a Blu-ray encode when one appears.",
		Settings: Simple{
			Resolutions: []int{1080}, LowestSource: "webrip", HighestSource: "bluray", WebEqual: true,
			Upgrades: true, UpgradeUntil: Rung{1080, "bluray"}, PreferPropers: true,
			Languages: LanguageChoice{Mode: "original"},
			Features:  cleanRelease(map[string]Choice{"x265-hd": Avoid, "av1": Avoid}),
		},
	},
	{
		ID: "remux", Name: "Remux",
		Description: "Untouched Blu-ray video and audio, 1080p or 4K. Large files, best quality.",
		Settings: Simple{
			Resolutions: []int{1080, 2160}, LowestSource: "remux", HighestSource: "remux",
			Upgrades: true, UpgradeUntil: Rung{2160, "remux"}, PreferPropers: true,
			Languages: LanguageChoice{Mode: "original"},
			Features: cleanRelease(map[string]Choice{
				"dv": Prefer, "hdr10plus": Prefer, "hdr": Prefer, "atmos": Prefer, "lossless": Prefer,
				"dv-no-fallback": Block, "hybrid": Prefer,
			}),
		},
	},
	{
		ID: "4k", Name: "4K",
		Description: "2160p from web or Blu-ray, preferring HDR and Dolby Vision. Smaller than a remux.",
		Settings: Simple{
			Resolutions: []int{2160}, LowestSource: "webrip", HighestSource: "bluray", WebEqual: true,
			Upgrades: true, UpgradeUntil: Rung{2160, "bluray"}, PreferPropers: true,
			Languages: LanguageChoice{Mode: "original"},
			Features: cleanRelease(map[string]Choice{
				"dv": Prefer, "hdr10plus": Prefer, "hdr": Prefer, "atmos": Prefer,
				"dv-no-fallback": Block, "sdr-4k": Avoid, "av1": Avoid,
			}),
		},
	},
	{
		ID: "compact", Name: "Compact",
		Description: "720p or 1080p kept small: at most about 5 GB for a two-hour movie. Good for kids' movies and phones.",
		Settings: Simple{
			Resolutions: []int{720, 1080}, LowestSource: "webrip", HighestSource: "bluray", WebEqual: true,
			Upgrades: true, UpgradeUntil: Rung{1080, "webdl"}, PreferPropers: true,
			Sizes:     map[string]SizeRange{"720": {Max: 1.5}, "1080": {Max: 2.5}},
			Languages: LanguageChoice{Mode: "original"},
			Features:  cleanRelease(map[string]Choice{"x265-hd": Prefer, "av1": Avoid}),
		},
	},
}

// PresetByID finds a preset.
func PresetByID(id string) (Preset, bool) {
	for _, p := range Presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}
