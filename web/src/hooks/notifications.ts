import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import type { components } from "@/api/schema";

export type NotificationSettings = components["schemas"]["NotificationSettings"];
export type NotificationSettingsUpdate = components["schemas"]["NotificationSettingsUpdate"];

export const notificationsQuery = queryOptions({
  queryKey: ["settings", "notifications"],
  queryFn: () => unwrap(api.GET("/settings/notifications")),
});

export function useSaveNotifications() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: NotificationSettingsUpdate) =>
      unwrap(api.PUT("/settings/notifications", { body })),
    onSuccess: (data) => qc.setQueryData(notificationsQuery.queryKey, data),
  });
}

export function useTestNotification() {
  return useMutation({
    mutationFn: (v: {
      channel: "emby" | "jellyfin" | "email" | "webhook";
      settings: NotificationSettingsUpdate;
    }) => unwrap(api.POST("/settings/notifications/test", { body: v })),
  });
}
