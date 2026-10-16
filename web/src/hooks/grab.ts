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
