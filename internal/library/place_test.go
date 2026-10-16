package library

import (
	"strings"
	"testing"
)

func ip(n int) *int { return &n }

// firefly is Firefly as TMDB numbers it (aired order), plus the numbers
// its "Intended Order" gives each episode, which is how the files are
// named.
func firefly() (eps []Episode, intended map[int64]epKey) {
	aired := []string{"The Train Job", "Bushwhacked", "Our Mrs. Reynolds", "Jaynestown", "Out of Gas",
		"Shindig", "Safe", "Ariel", "War Stories", "Objects in Space", "Serenity"}
	for i, name := range aired {
		eps = append(eps, Episode{ID: int64(i + 1), AiredSeason: 1, AiredNumber: i + 1, Season: ip(1), Number: ip(i + 1), Name: name})
	}
	for i, name := range []string{"Trash", "The Message", "Heart of Gold"} {
		eps = append(eps, Episode{ID: int64(20 + i), AiredSeason: 0, AiredNumber: i + 1, Season: ip(0), Number: ip(i + 1), Name: name})
	}
	// Intended: Serenity, Train Job, Bushwhacked, Shindig, Safe, Our Mrs.
	// Reynolds, Jaynestown, Out of Gas, Ariel, War Stories, Trash, The
	// Message, Heart of Gold, Objects in Space.
	order := []int64{11, 1, 2, 6, 7, 3, 4, 5, 8, 9, 20, 21, 22, 10}
	intended = map[int64]epKey{}
	for i, id := range order {
		intended[id] = epKey{1, i + 1}
	}
	return eps, intended
}

func TestPlaceInAiredOrder(t *testing.T) {
	eps, _ := firefly()
	idx := indexEpisodes(eps, chosenNumbers, "TMDB's aired order")

	// Numbers and title agree.
	p := idx.place(EpisodeClaim{Season: 1, Episodes: []int{2}, Title: "Bushwhacked"})
	if len(p.Episodes) != 1 || p.Episodes[0] != 2 || p.Note != "" {
		t.Errorf("S01E02 = %+v", p)
	}
	// Named in intended order: S01E01 is "Serenity", which TMDB airs as
	// S01E11. Placed by its numbers, with a note.
	p = idx.place(EpisodeClaim{Season: 1, Episodes: []int{1}, Title: "Serenity"})
	if len(p.Episodes) != 1 || p.Episodes[0] != 1 || !strings.Contains(p.Note, `which is S01E11 in TMDB's aired order, but numbered S01E01 ("The Train Job")`) {
		t.Errorf("S01E01 Serenity = %+v", p)
	}
	// Numbers the order doesn't have.
	p = idx.place(EpisodeClaim{Season: 1, Episodes: []int{14}, Title: "Objects in Space"})
	if len(p.Episodes) != 0 || !strings.HasPrefix(p.Note, "TMDB's aired order has no S01E14.") {
		t.Errorf("S01E14 = %+v", p)
	}
	// Specials are season 0.
	if p = idx.place(EpisodeClaim{Season: 0, Episodes: []int{2}}); len(p.Episodes) != 1 || p.Episodes[0] != 21 {
		t.Errorf("S00E02 = %+v", p)
	}
	// A title that fits nothing (a placeholder, a typo) isn't flagged.
	if p = idx.place(EpisodeClaim{Season: 1, Episodes: []int{3}, Title: "TBA"}); p.Note != "" {
		t.Errorf("placeholder title: %+v", p)
	}
	if p = idx.place(EpisodeClaim{Season: 1, Episodes: []int{3}, Title: "Something Else Entirely"}); p.Note != "" || len(p.Episodes) != 1 {
		t.Errorf("unrelated title: %+v", p)
	}
	// Minor differences still fit ("Our Mrs Reynolds" vs "Our Mrs. Reynolds").
	if p = idx.place(EpisodeClaim{Season: 1, Episodes: []int{3}, Title: "Our Mrs Reynolds"}); p.Note != "" {
		t.Errorf("close title: %+v", p)
	}
}

func TestPlaceInChosenOrder(t *testing.T) {
	eps, intended := firefly()
	idx := indexEpisodes(eps, func(e *Episode) (*int, *int) {
		k, ok := intended[e.ID]
		if !ok {
			return nil, nil
		}
		return ip(k.season), ip(k.number)
	}, "the Intended Order")
	for _, c := range []struct {
		claim EpisodeClaim
		want  int64
	}{
		{EpisodeClaim{Season: 1, Episodes: []int{1}, Title: "Serenity"}, 11},
		{EpisodeClaim{Season: 1, Episodes: []int{14}, Title: "Objects in Space"}, 10},
		{EpisodeClaim{Season: 1, Episodes: []int{11}, Title: "Trash"}, 20},
	} {
		p := idx.place(c.claim)
		if len(p.Episodes) != 1 || p.Episodes[0] != c.want || p.Note != "" {
			t.Errorf("%+v = %+v, want episode %d", c.claim, p, c.want)
		}
	}
}

func TestPlaceMultiEpisodeFiles(t *testing.T) {
	eps := []Episode{
		{ID: 1, Season: ip(1), Number: ip(9), Name: "Critical Mass"},
		{ID: 2, Season: ip(1), Number: ip(10), Name: "Leviathan Wakes"},
	}
	idx := indexEpisodes(eps, chosenNumbers, "TMDB's aired order")
	p := idx.place(EpisodeClaim{Season: 1, Episodes: []int{9, 10}, Title: "Critical Mass and Leviathan Wakes"})
	if len(p.Episodes) != 2 || p.Note != "" {
		t.Errorf("two-part file = %+v", p)
	}
	// Half of it missing: link what exists, say what doesn't.
	p = idx.place(EpisodeClaim{Season: 1, Episodes: []int{10, 11}})
	if len(p.Episodes) != 1 || !strings.Contains(p.Note, "has no S01E11") {
		t.Errorf("partly missing = %+v", p)
	}
}

func TestPlaceWithoutSeasonOrByDate(t *testing.T) {
	one := []Episode{
		{ID: 1, Season: ip(1), Number: ip(7), Name: "Great Plains", AirDate: "2006-04-02"},
		{ID: 2, Season: ip(0), Number: ip(1), Name: "Making Of"},
	}
	idx := indexEpisodes(one, chosenNumbers, "TMDB's aired order")
	// One regular season: "07 Great Plains" needs no season.
	if p := idx.place(EpisodeClaim{Season: -1, Episodes: []int{7}}); len(p.Episodes) != 1 || p.Episodes[0] != 1 {
		t.Errorf("no season, one-season show = %+v", p)
	}
	two := append(one, Episode{ID: 3, Season: ip(2), Number: ip(1), Name: "Other"})
	idx = indexEpisodes(two, chosenNumbers, "TMDB's aired order")
	if p := idx.place(EpisodeClaim{Season: -1, Episodes: []int{7}}); len(p.Episodes) != 0 || !strings.Contains(p.Note, "not the season") {
		t.Errorf("no season, two seasons = %+v", p)
	}

	daily := []Episode{
		{ID: 1, Season: ip(2024), Number: ip(50), Name: "Guest A", AirDate: "2024-03-15"},
		{ID: 2, Season: ip(2024), Number: ip(51), Name: "Guest B", AirDate: "2024-03-16"},
		{ID: 3, Season: ip(2024), Number: ip(52), Name: "Guest C", AirDate: "2024-03-16"},
	}
	idx = indexEpisodes(daily, chosenNumbers, "TMDB's aired order")
	if p := idx.place(EpisodeClaim{Season: -1, AirDate: "2024-03-15"}); len(p.Episodes) != 1 || p.Episodes[0] != 1 {
		t.Errorf("by date = %+v", p)
	}
	if p := idx.place(EpisodeClaim{Season: -1, AirDate: "2024-03-16", Title: "Guest C"}); len(p.Episodes) != 1 || p.Episodes[0] != 3 {
		t.Errorf("two that day, title says = %+v", p)
	}
	if p := idx.place(EpisodeClaim{Season: -1, AirDate: "2024-03-16"}); len(p.Episodes) != 0 || !strings.Contains(p.Note, "2 episodes aired on 2024-03-16") {
		t.Errorf("two that day, no title = %+v", p)
	}
	if p := idx.place(EpisodeClaim{Season: -1, AirDate: "2024-01-01"}); len(p.Episodes) != 0 || p.Note == "" {
		t.Errorf("no such day = %+v", p)
	}
}

func TestPlaceWithoutEpisode(t *testing.T) {
	idx := indexEpisodes(nil, chosenNumbers, "TMDB's aired order")
	if p := idx.place(EpisodeClaim{Season: -1}); p.Note != "Couldn't tell which episode this is from its name." {
		t.Errorf("nothing = %+v", p)
	}
	if p := idx.place(EpisodeClaim{Season: 2}); !strings.Contains(p.Note, "whole season") {
		t.Errorf("season only = %+v", p)
	}
}
