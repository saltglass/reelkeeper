import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import type { components } from "@/api/schema";

export type CleanupSettings = components["schemas"]["CleanupSettings"];
export type CleanupEvent = components["schemas"]["CleanupEvent"];
export type SeedingItem = components["schemas"]["SeedingItem"];
export type Orphans = components["schemas"]["Orphans"];

export const cleanupSettingsQuery = queryOptions({
  queryKey: ["settings", "cleanup"],
  queryFn: () => unwrap(api.GET("/settings/cleanup")),
});

export function useSaveCleanupSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CleanupSettings) => unwrap(api.PUT("/settings/cleanup", { body })),
    onSuccess: (data) => qc.setQueryData(cleanupSettingsQuery.queryKey, data),
  });
}

/** Activity → Cleanup data refetches every 30 seconds while the tab is visible. */
export const cleanupEventsQuery = (limit = 50) =>
  queryOptions({
    queryKey: ["cleanup", "events", limit],
    queryFn: () => fetchCleanupEvents(limit),
    refetchInterval: 30_000,
  });

export function fetchCleanupEvents(limit: number, before?: number) {
  return unwrap(api.GET("/cleanup/events", { params: { query: { limit, before } } }));
}

export const cleanupSeedingQuery = queryOptions({
  queryKey: ["cleanup", "seeding"],
  queryFn: () => unwrap(api.GET("/cleanup/seeding")),
  refetchInterval: 30_000,
});

export const cleanupOrphansQuery = queryOptions({
  queryKey: ["cleanup", "orphans"],
  queryFn: () => unwrap(api.GET("/cleanup/orphans")),
  refetchInterval: 30_000,
});

export function useRemoveOrphanTorrent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (hash: string) =>
      unwrap(api.POST("/cleanup/orphans/torrents/{hash}/remove", { params: { path: { hash } } })),
    onSettled: () => qc.invalidateQueries({ queryKey: cleanupOrphansQuery.queryKey }),
  });
}

export function useRemoveOrphanFile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      unwrap(api.POST("/cleanup/orphans/files/remove", { body: { name } })),
    onSettled: () => qc.invalidateQueries({ queryKey: cleanupOrphansQuery.queryKey }),
  });
}
