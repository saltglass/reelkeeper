import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import type { components } from "@/api/schema";

export type Backfill = components["schemas"]["Backfill"];
export type BackfillShow = components["schemas"]["BackfillShow"];
export type BackfillOptions = components["schemas"]["BackfillOptions"];
export type BackfillUnit = components["schemas"]["BackfillUnit"];
export type BackfillOption = components["schemas"]["BackfillOption"];

/** The backfill queue. Refetched every 30 seconds while the tab is visible, and when the Backfill task finishes. */
export const backfillQuery = queryOptions({
  queryKey: ["backfill", "list"],
  queryFn: () => unwrap(api.GET("/backfill")),
  refetchInterval: 30_000,
});

/** Where a series version stands in the queue: not in it, being worked on, or waiting at a place (1 is next). */
export type BackfillStanding =
  | { kind: "none" }
  | { kind: "now" }
  | { kind: "waiting"; place: number };

export function backfillStanding(list: Backfill | undefined, versionId: number): BackfillStanding {
  if (list?.now?.versionId === versionId) return { kind: "now" };
  const i = list?.next.findIndex((s) => s.versionId === versionId) ?? -1;
  return i >= 0 ? { kind: "waiting", place: i + 1 } : { kind: "none" };
}

function useBackfillMutation<V, R>(fn: (v: V) => Promise<R>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ["backfill"] });
      qc.invalidateQueries({ queryKey: ["wanted"] });
      qc.invalidateQueries({ queryKey: ["series"] });
    },
  });
}

/** Adds a series version to the end of the queue. */
export function useAddBackfill() {
  return useBackfillMutation((versionId: number) =>
    unwrap(api.POST("/backfill", { body: { versionId } })),
  );
}

export function useRemoveBackfill() {
  return useBackfillMutation((id: number) =>
    unwrap(api.DELETE("/backfill/{id}", { params: { path: { id } } })),
  );
}

export function useMoveBackfill() {
  return useBackfillMutation((v: { id: number; direction: "up" | "down" }) =>
    unwrap(
      api.POST("/backfill/{id}/move", {
        params: { path: { id: v.id } },
        body: { direction: v.direction },
      }),
    ),
  );
}

export function useHideBackfill() {
  return useBackfillMutation((id: number) =>
    unwrap(api.POST("/backfill/{id}/hide", { params: { path: { id } } })),
  );
}

/** The releases the profile turned down, for one show. Searches, so it can take a while: ask on request only. */
export function useBackfillOptions() {
  return useMutation({
    mutationFn: (id: number) =>
      unwrap(api.GET("/backfill/{id}/options", { params: { path: { id } } })),
  });
}

/** Downloads one of the offered releases anyway. */
export function useGrabBackfillOption() {
  return useBackfillMutation((v: { id: number; token: string }) =>
    unwrap(
      api.POST("/backfill/{id}/grab", { params: { path: { id: v.id } }, body: { token: v.token } }),
    ),
  );
}
