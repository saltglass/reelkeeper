package api

import (
	"net/http"
	"time"

	"github.com/saltglass/reelkeeper/internal/api/gen"
	"github.com/saltglass/reelkeeper/internal/grab"
)

// upcomingDays and upcomingMax bound the summary's "upcoming" list.
const (
	upcomingDays = 7
	upcomingMax  = 10
)

// GetSummary returns the small snapshot a dashboard polls. It reads only
// Reelkeeper's own records.
func (h *Handlers) GetSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out gen.DashboardSummary

	c, err := h.Downloads.QueueCounts(ctx)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out.Queue.Movies, out.Queue.Tv, out.Queue.NeedsAttention = c.Movies, c.TV, c.NeedsAttention
	g := h.Settings.Grabbing()
	out.Queue.Limit, out.Queue.Full = g.QueueLimit, grab.QueueFull(g, c.Movies+c.TV)

	today := time.Now()
	from := today.Format("2006-01-02")
	to := today.AddDate(0, 0, upcomingDays).Format("2006-01-02")
	ups, err := h.Library.UpcomingMovies(ctx, from, to, upcomingMax)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out.Upcoming = make([]gen.UpcomingMovie, 0, len(ups))
	for _, u := range ups {
		out.Upcoming = append(out.Upcoming, gen.UpcomingMovie{
			TitleId: u.TitleID, Title: u.Title, Year: u.Year, Kind: gen.UpcomingMovieKind(u.Kind), Date: u.Date})
	}
	eps, err := h.Library.UpcomingEpisodes(ctx, from, to, upcomingMax)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out.UpcomingEpisodes = make([]gen.UpcomingEpisode, 0, len(eps))
	for _, e := range eps {
		out.UpcomingEpisodes = append(out.UpcomingEpisodes, gen.UpcomingEpisode{
			TitleId: e.TitleID, Title: e.Title, Season: e.Season, Episode: e.Number, Name: e.Name, Date: e.Date})
	}

	ixs, err := h.Indexers.List(ctx)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	out.Indexers.Failing = []string{}
	for i := range ixs {
		if !ixs[i].Enabled {
			continue
		}
		out.Indexers.Total++
		if h.Indexers.HealthOf(&ixs[i]).Status == "failing" {
			out.Indexers.Failing = append(out.Indexers.Failing, ixs[i].Name)
		}
	}
	out.Indexers.Ok = out.Indexers.Total - len(out.Indexers.Failing)

	n, err := h.Grab.GrabsToday(ctx)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	// The daily limit is gone (see queue.limit); grabs.limit stays at 0 so
	// dashboards written for it keep working.
	out.Grabs.Today, out.Grabs.Limit, out.Grabs.Mode = n, 0, gen.DashboardSummaryGrabsMode(g.Mode)

	writeJSON(w, http.StatusOK, out)
}
