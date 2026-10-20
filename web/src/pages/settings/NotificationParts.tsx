import { type ReactNode, useEffect } from "react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import {
  type NotificationSettings,
  type NotificationSettingsUpdate,
  useTestNotification,
} from "@/hooks/notifications";
import { BusyLabel } from "./BusyLabel";

// Media servers and notifications are two pages but one setting in the API:
// each page sends its own cards as edited and the other page's as saved.

export type Channel = "emby" | "jellyfin" | "plex" | "email" | "webhook";

/** The saved settings as an update that changes nothing: secrets left out keep the stored ones. */
export function savedBody(d: NotificationSettings): NotificationSettingsUpdate {
  const server = ({ apiKeySet: _, ...s }: NotificationSettings["emby"]) => s;
  const { passwordSet: _p, ...email } = d.email;
  const { secretSet: _s, ...webhook } = d.webhook;
  return {
    emby: server(d.emby),
    jellyfin: server(d.jellyfin),
    plex: server(d.plex),
    email,
    webhook,
  };
}

/** The save error's message, unless a field on this page shows it already. */
export function pageError(err: RequestError | undefined, channels: readonly Channel[]) {
  if (!err) return undefined;
  const shown = Object.keys(err.fields).some((f) => channels.some((c) => f.startsWith(`${c}.`)));
  return shown ? undefined : err.message;
}

export function TestButton({
  channel,
  body,
  disabled,
}: {
  channel: Channel;
  body: NotificationSettingsUpdate;
  disabled: boolean;
}) {
  const test = useTestNotification();
  const err = test.error instanceof RequestError ? test.error : undefined;
  // Editing the form makes an earlier result stale.
  const { reset } = test;
  const values = JSON.stringify(body);
  // biome-ignore lint/correctness/useExhaustiveDependencies: clear the result when the values change
  useEffect(() => reset(), [values, reset]);
  return (
    <div className="space-y-2" aria-live="polite">
      <Button
        type="button"
        variant="outline"
        disabled={test.isPending || disabled}
        onClick={() => test.mutate({ channel, settings: body })}
      >
        <BusyLabel busy={test.isPending} ok={Boolean(test.data)}>
          {test.isPending ? "Testing…" : "Test"}
        </BusyLabel>
      </Button>
      {test.data && <Alert variant="success">{test.data.message}</Alert>}
      {err && (
        <Alert variant="error">
          {Object.entries(err.fields).find(([field]) => field.startsWith(`${channel}.`))?.[1] ??
            err.message}
        </Alert>
      )}
    </div>
  );
}

export function ChannelCard({
  id,
  title,
  description,
  enabled,
  onEnabled,
  children,
}: {
  id: string;
  title: string;
  description: string;
  enabled: boolean;
  onEnabled: (v: boolean) => void;
  children: ReactNode;
}) {
  return (
    <Card>
      <CardHeader className="flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1 basis-64 space-y-1">
          <CardTitle>{title}</CardTitle>
          <CardDescription>{description}</CardDescription>
        </div>
        <label
          htmlFor={`${id}-enabled`}
          className="flex cursor-pointer items-center gap-2 text-sm font-medium"
        >
          <span className={enabled ? "text-foreground" : "text-muted-foreground"}>
            {enabled ? "On" : "Off"}
          </span>
          <Switch
            id={`${id}-enabled`}
            aria-label={`${title} enabled`}
            checked={enabled}
            onCheckedChange={onEnabled}
          />
        </label>
      </CardHeader>
      <CardContent className="space-y-5">{children}</CardContent>
    </Card>
  );
}
