import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  api,
  type GeneralSettings,
  type LabelSettings,
  type SecuritySettingsUpdate,
  unwrap,
} from "@/api/client";

export const generalSettingsQuery = queryOptions({
  queryKey: ["settings", "general"],
  queryFn: () => unwrap(api.GET("/settings/general")),
});

export const securitySettingsQuery = queryOptions({
  queryKey: ["settings", "security"],
  queryFn: () => unwrap(api.GET("/settings/security")),
});

export function useSaveGeneral() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: GeneralSettings) => unwrap(api.PUT("/settings/general", { body })),
    onSuccess: (data) => qc.setQueryData(generalSettingsQuery.queryKey, data),
  });
}

export function useSaveSecurity() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: SecuritySettingsUpdate) => unwrap(api.PUT("/settings/security", { body })),
    onSuccess: (data) => {
      qc.setQueryData(securitySettingsQuery.queryKey, data);
      qc.invalidateQueries({ queryKey: ["auth"] });
    },
  });
}

export const labelSettingsQuery = queryOptions({
  queryKey: ["settings", "labels"],
  queryFn: () => unwrap(api.GET("/settings/labels")),
});

export function useSaveLabels() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: LabelSettings) => unwrap(api.PUT("/settings/labels", { body })),
    onSuccess: (data) => qc.setQueryData(labelSettingsQuery.queryKey, data),
  });
}
