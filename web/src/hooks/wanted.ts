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

/** Searches for these movie or series versions now (queued, one after another). */
export function useSearchWanted() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (versionIds: number[]) => {
      // The server takes up to 50 at a time.
      let queued = 0;
      let alreadyQueued = 0;
      for (let i = 0; i < versionIds.length; i += 50) {
        const r = await unwrap(
          api.POST("/wanted/search", { body: { versionIds: versionIds.slice(i, i + 50) } }),
        );
        queued += r.queued;
        alreadyQueued += r.alreadyQueued;
      }
      return { queued, alreadyQueued };
    },
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
