import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import type { components } from "@/api/schema";
import { grabbingQuery } from "./grab";

export type DownloadClient = components["schemas"]["DownloadClient"];
export type DownloadClientType = components["schemas"]["DownloadClientType"];
export type DownloadClientUpdate = components["schemas"]["DownloadClientUpdate"];
export type QueueItem = components["schemas"]["QueueItem"];
export type TorrentState = components["schemas"]["TorrentState"];
export type BlocklistItem = components["schemas"]["BlocklistItem"];
export type Blocklist = components["schemas"]["Blocklist"];
export type DownloadMovePreview = components["schemas"]["DownloadMovePreview"];
export type BuiltinStatus = components["schemas"]["BuiltinStatus"];
export type VpnSummary = components["schemas"]["VpnSummary"];

/** What changing the download client would move (asked just before saving a change of type). */
export const downloadMovePreviewQuery = queryOptions({
  queryKey: ["settings", "download-client", "move-preview"],
  queryFn: () => unwrap(api.GET("/settings/download-client/move-preview")),
  // Counts change as downloads finish: always ask afresh.
  staleTime: 0,
  gcTime: 0,
});

export const downloadClientQuery = queryOptions({
  queryKey: ["settings", "download-client"],
  queryFn: () => unwrap(api.GET("/settings/download-client")),
});

/** What each kind of download client is called as a choice or a title. */
export const clientNames: Record<DownloadClientType, string> = {
  qbittorrent: "qBittorrent",
  transmission: "Transmission",
  deluge: "Deluge",
  builtin: "Built-in",
};

/**
 * What each kind of client is called inside a sentence: "Built-in" would read
 * oddly ("sent to Built-in"), so the built-in client is "the built-in client".
 * Use atSentenceStart where the name begins a sentence.
 */
export const clientProse: Record<DownloadClientType, string> = {
  ...clientNames,
  builtin: "the built-in client",
};

/**
 * Whether a download client is set up: the built-in client has no address, so
 * it is set up once it has a download folder.
 */
export function isClientSet(c: Pick<DownloadClient, "type" | "url" | "folder">): boolean {
  return c.type === "builtin" ? c.folder !== "" : c.url !== "";
}

/**
 * The configured download client's name for page text, such as "Transmission".
 * "the download client" stands in while it loads or when none is set up, so no
 * page names the wrong client. It reads the saved Download Client settings
 * (which Settings → Download Client has cached, and which never change on
 * their own) rather than the Queue, which is fetched every 5 seconds and only
 * on its own page. Use atSentenceStart where the name begins a sentence.
 */
export function useClientName(): string {
  const { data } = useQuery(downloadClientQuery);
  return data && isClientSet(data) ? clientProse[data.type] : "the download client";
}

/**
 * A client name to start a sentence with: the stand-in "the download client"
 * gets a capital, a name like qBittorrent stays as it is.
 */
export function atSentenceStart(clientName: string): string {
  return clientName.startsWith("the ") ? `The${clientName.slice(3)}` : clientName;
}

export function useSaveDownloadClient() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: DownloadClientUpdate) =>
      unwrap(api.PUT("/settings/download-client", { body })),
    onSuccess: (data) => {
      qc.setQueryData(downloadClientQuery.queryKey, data);
      // A new or removed VPN changes the status card.
      qc.invalidateQueries({ queryKey: builtinStatusQuery.queryKey });
      // Removing the client also drops Grabbing out of "On".
      qc.invalidateQueries({ queryKey: grabbingQuery.queryKey });
      // A change of client starts moving torrents: the Queue and Tasks show it.
      qc.invalidateQueries({ queryKey: queueQuery.queryKey });
      qc.invalidateQueries({ queryKey: ["system", "tasks"] });
      qc.invalidateQueries({ queryKey: ["history"] });
    },
  });
}

/** The built-in client's VPN and the address peers see; fetched every 10 seconds while the tab is visible. */
export const builtinStatusQuery = queryOptions({
  queryKey: ["settings", "download-client", "builtin-status"],
  queryFn: () => unwrap(api.GET("/settings/download-client/builtin/status")),
  refetchInterval: 10_000,
  // A status that fails (the client isn't the built-in one any more) isn't retried in a loop.
  retry: false,
});

/** "Check again": looks the addresses up afresh (the server allows it once in 30 seconds). */
export function useCheckAgain() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () =>
      unwrap(
        api.GET("/settings/download-client/builtin/status", {
          params: { query: { refresh: true } },
        }),
      ),
    onSuccess: (data) => qc.setQueryData(builtinStatusQuery.queryKey, data),
  });
}

/** Reads a WireGuard config on the server without saving it. */
export function useCheckVpnConfig() {
  return useMutation({
    mutationFn: (config: string) =>
      unwrap(api.POST("/settings/download-client/vpn/check", { body: { config } })),
  });
}

/** "Start without VPN": lets the built-in client's downloads go on after the VPN was removed. */
export function useStartWithoutVpn() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => unwrap(api.POST("/settings/download-client/builtin/start-unprotected")),
    onSuccess: (data) => {
      qc.setQueryData(downloadClientQuery.queryKey, data);
      qc.invalidateQueries({ queryKey: builtinStatusQuery.queryKey });
      qc.invalidateQueries({ queryKey: queueQuery.queryKey });
    },
  });
}

export function useTestDownloadClient() {
  return useMutation({
    mutationFn: (body: DownloadClientUpdate) =>
      unwrap(api.POST("/settings/download-client/test", { body })),
  });
}

/** Reelkeeper's unfinished downloads; refetched every 5 seconds while the tab is visible. */
export const queueQuery = queryOptions({
  queryKey: ["queue"],
  queryFn: () => unwrap(api.GET("/queue")),
  refetchInterval: 5_000,
});

export function useRetryImport() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) =>
      unwrap(api.POST("/queue/{id}/retry-import", { params: { path: { id } } })),
    onSettled: () => qc.invalidateQueries({ queryKey: queueQuery.queryKey }),
  });
}

/** Pauses a download in the download client. History records it, so it is refreshed too. */
export function usePauseDownload() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => unwrap(api.POST("/queue/{id}/pause", { params: { path: { id } } })),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ["history"] });
      return qc.invalidateQueries({ queryKey: queueQuery.queryKey });
    },
  });
}

/** Starts a paused download again. */
export function useResumeDownload() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) =>
      unwrap(api.POST("/queue/{id}/resume", { params: { path: { id } } })),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ["history"] });
      return qc.invalidateQueries({ queryKey: queueQuery.queryKey });
    },
  });
}

/** Puts a download first in the download client's queue. */
export function useMoveDownloadToTop() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => unwrap(api.POST("/queue/{id}/top", { params: { path: { id } } })),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ["history"] });
      return qc.invalidateQueries({ queryKey: queueQuery.queryKey });
    },
  });
}

/** Every blocklist query shares this key prefix, so one invalidate refreshes them all. */
const blocklistKey = ["blocklist"] as const;

export function useRemoveFromQueue() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: number; blocklist: boolean; searchAgain: boolean }) =>
      unwrap(
        api.POST("/queue/{id}/remove", {
          params: { path: { id: v.id } },
          body: { blocklist: v.blocklist, searchAgain: v.searchAgain },
        }),
      ),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: queueQuery.queryKey });
      qc.invalidateQueries({ queryKey: blocklistKey });
      qc.invalidateQueries({ queryKey: ["history"] });
    },
  });
}

/**
 * Releases Reelkeeper skips: all titles' or one title's. Entries of titles with
 * nothing wanted (dormant) are left out unless `dormant` is set.
 */
export const blocklistQuery = (opts: { titleId?: number; dormant?: boolean } = {}) =>
  queryOptions({
    queryKey: [...blocklistKey, opts.titleId ?? "all", opts.dormant ? "dormant" : "active"],
    queryFn: () =>
      unwrap(
        api.GET("/blocklist", {
          params: {
            query: { titleId: opts.titleId, include: opts.dormant ? "dormant" : undefined },
          },
        }),
      ),
  });

export function useUnblock() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE("/blocklist/{id}", { params: { path: { id } } })),
    onSettled: () => qc.invalidateQueries({ queryKey: blocklistKey }),
  });
}

/** Unblocks every temporary entry (of one title, if given). Resolves to how many. */
export function useUnblockTemporary() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (titleId?: number) =>
      unwrap(api.POST("/blocklist/unblock-temporary", { params: { query: { titleId } } })),
    onSettled: () => qc.invalidateQueries({ queryKey: blocklistKey }),
  });
}
