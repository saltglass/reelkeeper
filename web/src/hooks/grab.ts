import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { type ApiError, api, unwrap } from "@/api/client";
import type { components } from "@/api/schema";

export type GrabbingSettings = components["schemas"]["GrabbingSettings"];
export type HistoryEvent = components["schemas"]["HistoryEvent"];
export type HistoryEventKind = components["schemas"]["HistoryEventKind"];
export type HistoryVerdict = components["schemas"]["HistoryVerdict"];
export type { ApiError };

export const grabbingQuery = queryOptions({
  queryKey: ["settings", "grabbing"],
  queryFn: () => unwrap(api.GET("/settings/grabbing")),
});

export function useSaveGrabbing() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: GrabbingSettings) => unwrap(api.PUT("/settings/grabbing", { body })),
    onSuccess: (data) => qc.setQueryData(grabbingQuery.queryKey, data),
  });
}

export type HistoryFilter = { titleId?: number; events?: HistoryEventKind[] };

/** One page of History, newest first. `before` is the previous page's nextBefore. */
export async function fetchHistory(filter: HistoryFilter, limit: number, before?: number) {
  return unwrap(
    api.GET("/history", {
      params: {
        query: {
          titleId: filter.titleId,
          event: filter.events?.length ? filter.events : undefined,
          before,
          limit,
        },
      },
    }),
  );
}

/** The newest page; refetched every 30 seconds while the tab is visible. */
export const historyQuery = (filter: HistoryFilter, limit = 50) =>
  queryOptions({
    queryKey: ["history", filter.titleId ?? null, filter.events ?? [], limit],
    queryFn: () => fetchHistory(filter, limit),
    refetchInterval: 30_000,
  });

export type MovieChoices = components["schemas"]["MovieChoices"];
export type MovieChoice = components["schemas"]["MovieChoice"];
export type PickResult = components["schemas"]["PickResult"];

/**
 * "Choose a release": searches every indexer for one movie version and lists
 * what was found with the profile's verdict. Searches, so it can take a while:
 * asked for on request only, as a mutation.
 */
export function useMovieChoices(movieId: number, versionId: number) {
  return useMutation({
    mutationFn: () =>
      unwrap(
        api.GET("/movies/{id}/versions/{versionId}/releases", {
          params: { path: { id: movieId, versionId } },
        }),
      ),
  });
}

/** Grabs one release from the "Choose a release" list (or records it, in a dry run). */
export function useGrabMovieChoice(movieId: number, versionId: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (token: string) =>
      unwrap(
        api.POST("/movies/{id}/versions/{versionId}/releases/grab", {
          params: { path: { id: movieId, versionId } },
          body: { token },
        }),
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["history"] });
      // The Upgrades switch may have turned off, and the movie is now downloading.
      qc.invalidateQueries({ queryKey: ["movies"] });
      qc.invalidateQueries({ queryKey: ["queue"] });
      qc.invalidateQueries({ queryKey: ["wanted"] });
    },
  });
}
