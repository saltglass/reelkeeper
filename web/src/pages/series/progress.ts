import type { SeriesSummary } from "@/hooks/library";

/**
 * How complete a series is, for the progress bar on the Series page.
 *
 * A series can have several versions (HD, Remux...), each with its own files.
 * Showing one bar per version would crowd a poster, so the bar shows the best
 * version: the one holding the most episodes. "Is anything of this episode on
 * disk?" is what people mean by complete; each version's own count is shown
 * on the series page.
 */
export function seriesProgress(s: Pick<SeriesSummary, "versions" | "airedEpisodes">) {
  const have = Math.max(0, ...s.versions.map((v) => v.episodesWithFiles));
  const total = s.airedEpisodes;
  return {
    have,
    total,
    missing: Math.max(0, total - have),
    percent: total > 0 ? Math.round((Math.min(have, total) / total) * 100) : 0,
  };
}

/** "31 of 38 episodes", or "No episodes aired yet". */
export function progressText(have: number, total: number): string {
  if (total === 0) return "No episodes aired yet";
  return `${have} of ${total} ${total === 1 ? "episode" : "episodes"}`;
}
