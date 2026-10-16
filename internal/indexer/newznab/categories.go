// Package newznab holds the standard release categories that Torznab,
// Newznab, Jackett and Prowlarr all share, and a client for Torznab and
// Newznab indexers.
package newznab

import "strings"

// Category is a standard category: 2000 "Movies", 2040 "Movies/HD".
type Category struct {
	ID   int
	Name string
}

// Common category IDs.
const (
	Movies = 2000
	TV     = 5000
)

// Categories is the standard list. Subcategories are parent ID + 10, 20...
var Categories = []Category{
	{1000, "Console"}, {1010, "Console/NDS"}, {1020, "Console/PSP"}, {1030, "Console/Wii"},
	{1040, "Console/XBox"}, {1050, "Console/XBox 360"}, {1060, "Console/Wiiware"},
	{1070, "Console/XBox 360 DLC"}, {1080, "Console/PS3"}, {1090, "Console/Other"},
	{1110, "Console/3DS"}, {1120, "Console/PS Vita"}, {1130, "Console/WiiU"},
	{1140, "Console/XBox One"}, {1180, "Console/PS4"},
	{2000, "Movies"}, {2010, "Movies/Foreign"}, {2020, "Movies/Other"}, {2030, "Movies/SD"},
	{2040, "Movies/HD"}, {2045, "Movies/UHD"}, {2050, "Movies/BluRay"}, {2060, "Movies/3D"},
	{2070, "Movies/DVD"}, {2080, "Movies/WEB-DL"},
	{3000, "Audio"}, {3010, "Audio/MP3"}, {3020, "Audio/Video"}, {3030, "Audio/Audiobook"},
	{3040, "Audio/Lossless"}, {3050, "Audio/Other"}, {3060, "Audio/Foreign"},
	{4000, "PC"}, {4010, "PC/0day"}, {4020, "PC/ISO"}, {4030, "PC/Mac"}, {4040, "PC/Mobile-Other"},
	{4050, "PC/Games"}, {4060, "PC/Mobile-iOS"}, {4070, "PC/Mobile-Android"},
	{5000, "TV"}, {5010, "TV/WEB-DL"}, {5020, "TV/Foreign"}, {5030, "TV/SD"}, {5040, "TV/HD"},
	{5045, "TV/UHD"}, {5050, "TV/Other"}, {5060, "TV/Sport"}, {5070, "TV/Anime"},
	{5080, "TV/Documentary"},
	{6000, "XXX"}, {6010, "XXX/DVD"}, {6020, "XXX/WMV"}, {6030, "XXX/XviD"}, {6040, "XXX/x264"},
	{6045, "XXX/UHD"}, {6050, "XXX/Pack"}, {6060, "XXX/ImageSet"}, {6070, "XXX/Other"},
	{6080, "XXX/SD"}, {6090, "XXX/WEB-DL"},
	{7000, "Books"}, {7010, "Books/Mags"}, {7020, "Books/EBook"}, {7030, "Books/Comics"},
	{7040, "Books/Technical"}, {7050, "Books/Other"}, {7060, "Books/Foreign"},
	{8000, "Other"}, {8010, "Other/Misc"}, {8020, "Other/Hashed"},
}

var byName = func() map[string]int {
	m := make(map[string]int, len(Categories))
	for _, c := range Categories {
		m[strings.ToLower(c.Name)] = c.ID
	}
	return m
}()

// ID returns the ID for a category name ("Movies/HD" → 2040), or 0.
func ID(name string) int { return byName[strings.ToLower(strings.TrimSpace(name))] }

// Parent returns a category's top-level category: 2040 → 2000.
func Parent(id int) int { return id / 1000 * 1000 }

// Name returns a category's name, or "".
func Name(id int) string {
	for _, c := range Categories {
		if c.ID == id {
			return c.Name
		}
	}
	return ""
}

// Within reports whether a category is one of the wanted ones or under
// one of them (2040 is within 2000). An empty want list wants everything.
func Within(id int, want []int) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		if id == w || (w%1000 == 0 && Parent(id) == w) {
			return true
		}
	}
	return false
}
