import type { NotificationSettings, NotificationSettingsUpdate } from "@/hooks/notifications";
import { json, renderApp, signedIn } from "@/test/app";

/** Saved settings: Emby and email set up with their secrets stored, the rest off. */
export const saved: NotificationSettings = {
  emby: {
    enabled: true,
    url: "http://192.168.1.30:8096",
    apiKeySet: true,
    pathFrom: "/media",
    pathTo: "/Volumes/Media",
  },
  jellyfin: { enabled: false, url: "", apiKeySet: false, pathFrom: "", pathTo: "" },
  plex: { enabled: false, url: "", apiKeySet: false, pathFrom: "", pathTo: "" },
  email: {
    enabled: true,
    host: "smtp.example.com",
    port: 587,
    username: "me",
    passwordSet: true,
    from: "reelkeeper@example.com",
    to: "me@example.com",
  },
  webhook: { enabled: false, url: "", secretSet: false },
};

/**
 * Opens a page that edits the notification settings (Media servers or
 * Notifications), recording what it saves and tests. A save with the webhook
 * address "bad" or the Emby address "bad" is refused with a field error.
 */
export function openNotifications(path: string, testReply?: () => Response) {
  const puts: NotificationSettingsUpdate[] = [];
  const tests: { channel: string; settings: NotificationSettingsUpdate }[] = [];
  renderApp(path, {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /settings/notifications": json(saved),
    "PUT /settings/notifications": async (req) => {
      const body: NotificationSettingsUpdate = await req.json();
      puts.push(body);
      const field =
        body.webhook.url === "bad" ? "webhook.url" : body.emby.url === "bad" ? "emby.url" : "";
      if (field) {
        return json(
          {
            code: "invalid",
            message: "Check the highlighted fields.",
            fields: { [field]: "Enter a full address, like http://192.168.1.30:8096." },
          },
          422,
        );
      }
      return json(saved);
    },
    "POST /settings/notifications/test": async (req) => {
      tests.push(await req.json());
      return testReply ? testReply() : json({ message: "Connected to Emby 4.9.3 (Emby-server)" });
    },
  });
  return { puts, tests };
}

/** The saved email settings as a save sends them: the stored password is kept, not sent. */
export const emailAsSaved = {
  enabled: true,
  host: "smtp.example.com",
  port: 587,
  username: "me",
  from: "reelkeeper@example.com",
  to: "me@example.com",
};

/** The saved Emby settings as a save sends them: the stored key is kept, not sent. */
export const embyAsSaved = {
  enabled: true,
  url: "http://192.168.1.30:8096",
  pathFrom: "/media",
  pathTo: "/Volumes/Media",
};
