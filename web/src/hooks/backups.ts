import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import type { components } from "@/api/schema";

export type BackupEntry = components["schemas"]["BackupEntry"];
export type BackupList = components["schemas"]["BackupList"];
export type BackupCheck = components["schemas"]["BackupCheck"];
export type RestoreResult = components["schemas"]["RestoreResult"];
export type BackupSettings = components["schemas"]["BackupSettings"];

export const backupsQuery = queryOptions({
  queryKey: ["backups"],
  queryFn: () => unwrap(api.GET("/backups")),
});

export const backupSettingsQuery = queryOptions({
  queryKey: ["settings", "backups"],
  queryFn: () => unwrap(api.GET("/settings/backups")),
});

export function useSaveBackupSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: BackupSettings) => unwrap(api.PUT("/settings/backups", { body })),
    onSuccess: (data) => {
      qc.setQueryData(backupSettingsQuery.queryKey, data);
      // The folder may have changed, so the list is somewhere else now.
      return qc.invalidateQueries({ queryKey: backupsQuery.queryKey });
    },
  });
}

export function useCreateBackup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => unwrap(api.POST("/backups")),
    onSuccess: () => qc.invalidateQueries({ queryKey: backupsQuery.queryKey }),
  });
}

/** Sends the file itself as the request body; the server checks it in full. */
export function useUploadBackup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (file: File) =>
      unwrap(
        api.POST("/backups/upload", {
          // The body is the raw file, not JSON.
          body: file as unknown as string,
          bodySerializer: (b: unknown) => b as BodyInit,
          headers: { "Content-Type": "application/octet-stream" },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: backupsQuery.queryKey }),
  });
}

/**
 * First run only: sends a backup file instead of creating an account. The
 * server checks it in full; a 202 means Reelkeeper is restarting to restore it.
 */
export function useSetupRestore() {
  return useMutation({
    mutationFn: (file: File) =>
      unwrap(
        api.POST("/auth/setup/restore", {
          // The body is the raw file, not JSON.
          body: file as unknown as string,
          bodySerializer: (b: unknown) => b as BodyInit,
          headers: { "Content-Type": "application/octet-stream" },
        }),
      ),
  });
}

export function useDeleteBackup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      unwrap(api.DELETE("/backups/{name}", { params: { path: { name } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: backupsQuery.queryKey }),
  });
}

export function checkBackup(name: string) {
  return unwrap(api.POST("/backups/{name}/check", { params: { path: { name } } }));
}

export function restoreBackup(name: string) {
  return unwrap(api.POST("/backups/{name}/restore", { params: { path: { name } } }));
}

/** How a backup came to be, for its badge. Uploaded is told by its file name. */
export type BackupKind = "scheduled" | "manual" | "safety" | "uploaded";

export function backupKind(b: BackupEntry): BackupKind | undefined {
  if (b.name.endsWith("-uploaded.backup")) return "uploaded";
  if (b.kind === "scheduled" || b.kind === "manual" || b.kind === "safety") return b.kind;
  return undefined;
}

export const kindText: Record<BackupKind, string> = {
  scheduled: "Scheduled",
  manual: "Manual",
  safety: "Safety (before a restore)",
  uploaded: "Uploaded",
};
