import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import type { components } from "@/api/schema";

export type ProfileOptions = components["schemas"]["ProfileOptions"];
export type ProfileSettings = components["schemas"]["ProfileSettings"];
export type ProfileSimple = components["schemas"]["ProfileSimple"];
export type ProfileCompiled = components["schemas"]["ProfileCompiled"];
export type ProfileChoice = components["schemas"]["ProfileChoice"];
export type ProfileFeature = components["schemas"]["ProfileFeature"];
export type ProfilePreset = components["schemas"]["ProfilePreset"];
export type CustomFormat = components["schemas"]["CustomFormat"];
export type TestReleaseResult = components["schemas"]["TestReleaseResult"];
export type TestReleaseInput = components["schemas"]["TestReleaseInput"];

/** Presets, features, tiers and so on. They only change with a new Reelkeeper. */
export const profileOptionsQuery = queryOptions({
  queryKey: ["profile-options"],
  queryFn: () => unwrap(api.GET("/profiles/options")),
  staleTime: Infinity,
});

export const profileSettingsQuery = (id: number) =>
  queryOptions({
    queryKey: ["library", "profile-settings", id],
    queryFn: () => unwrap(api.GET("/profiles/{id}/settings", { params: { path: { id } } })),
  });

/** Stores the saved settings in the cache, so the page follows them. */
function useStoreSettings(id: number) {
  const qc = useQueryClient();
  return (saved: ProfileSettings) => qc.setQueryData(profileSettingsQuery(id).queryKey, saved);
}

export function useSaveSimple(id: number) {
  const store = useStoreSettings(id);
  return useMutation({
    mutationFn: (body: ProfileSimple) =>
      unwrap(api.PUT("/profiles/{id}/settings/simple", { params: { path: { id } }, body })),
    onSuccess: store,
  });
}

export function useSaveAdvanced(id: number) {
  const store = useStoreSettings(id);
  return useMutation({
    mutationFn: (body: ProfileCompiled) =>
      unwrap(api.PUT("/profiles/{id}/settings/advanced", { params: { path: { id } }, body })),
    onSuccess: store,
  });
}

export function useSwitchToAdvanced(id: number) {
  const store = useStoreSettings(id);
  return useMutation({
    mutationFn: () =>
      unwrap(api.POST("/profiles/{id}/settings/switch-to-advanced", { params: { path: { id } } })),
    onSuccess: store,
  });
}

export function useTestRelease(id: number) {
  return useMutation({
    mutationFn: (body: TestReleaseInput) =>
      unwrap(api.POST("/profiles/{id}/test-release", { params: { path: { id } }, body })),
  });
}
