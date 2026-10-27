import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import type { components } from "@/api/schema";

export type WantedList = components["schemas"]["WantedList"];
export type WantedItem = components["schemas"]["WantedItem"];
export type WantedEpisode = components["schemas"]["WantedEpisode"];
export type WantedUnmonitorRequest = components["schemas"]["WantedUnmonitorRequest"];

/** Everything Reelkeeper is still looking for. Refetched every 30 seconds while the tab is visible,
 * and when the live events say the library or a search changed. */
export const wantedQuery = queryOptions({
  queryKey: ["wanted", "list"],
  queryFn: () => unwrap(api.GET("/wanted")),
  refetchInterval: 30_000,
});

/** Just the counts (the sidebar's number): no lists, no History read. */
export const wantedCountsQuery = queryOptions({
  queryKey: ["wanted", "counts"],
  queryFn: () => unwrap(api.GET("/wanted", { params: { query: { countsOnly: true } } })),
  refetchInterval: 60_000,
});

/** Movies plus episodes: the number beside Wanted in the sidebar and on the Missing tab. */
export const missingTotal = (c: WantedList["counts"]) => c.missingMovies + c.missingEpisodes;

/**
 * How many movies or shows one click searches now, so a big selection
 * doesn't flood the indexers or the Cloudflare solver. The rest are left to
 * the hourly search.
 */
export const SEARCH_LIMIT = 10;

/** Searches for these movie or series versions now (queued, one after another). */
export function useSearchWanted() {
  const qc = useQueryClient();
  return useMutation({
    // At most SEARCH_LIMIT at a time (the server refuses more).
    mutationFn: (versionIds: number[]) =>
      unwrap(api.POST("/wanted/search", { body: { versionIds } })),
    onSettled: () => qc.invalidateQueries({ queryKey: ["wanted"] }),
  });
}

/** Stops monitoring movie versions and episodes. */
export function useUnmonitorWanted() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: WantedUnmonitorRequest) => unwrap(api.POST("/wanted/unmonitor", { body })),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ["wanted"] });
      qc.invalidateQueries({ queryKey: ["movies"] });
      qc.invalidateQueries({ queryKey: ["series"] });
    },
  });
}
