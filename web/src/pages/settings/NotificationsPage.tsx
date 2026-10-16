import { useQuery } from "@tanstack/react-query";
import { type FormEvent, type ReactNode, useEffect, useMemo } from "react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { useDraft } from "@/hooks/draft";
import {
  type NotificationSettingsUpdate,
  notificationsQuery,
  useSaveNotifications,
  useTestNotification,
} from "@/hooks/notifications";
import { PageHeader } from "@/pages/PageHeader";
import { BusyLabel } from "./BusyLabel";
import { SaveBar } from "./SaveBar";

/** The form's values. Secrets start empty: the server never sends them. */
type Form = {
  emby: { enabled: boolean; url: string; apiKey: string; pathFrom: string; pathTo: string };
  email: {
    enabled: boolean;
    host: string;
    port: string;
    username: string;
    password: string;
    from: string;
    to: string;
  };
  webhook: { enabled: boolean; url: string; secret: string };
};

function same(a: Form, b: Form) {
  return JSON.stringify(a) === JSON.stringify(b);
}

/** What the API takes: a secret is sent only if it was typed (otherwise the stored one is kept). */
function toBody(f: Form): NotificationSettingsUpdate {
  const port = Number.parseInt(f.email.port, 10);
  return {
    emby: {
      enabled: f.emby.enabled,
      url: f.emby.url,
      pathFrom: f.emby.pathFrom,
      pathTo: f.emby.pathTo,
      ...(f.emby.apiKey ? { apiKey: f.emby.apiKey } : {}),
    },
    email: {
      enabled: f.email.enabled,
      host: f.email.host,
      port: Number.isNaN(port) ? 0 : port,
      username: f.email.username,
      from: f.email.from,
      to: f.email.to,
      ...(f.email.password ? { password: f.email.password } : {}),
    },
    webhook: {
      enabled: f.webhook.enabled,
      url: f.webhook.url,
      ...(f.webhook.secret ? { secret: f.webhook.secret } : {}),
    },
  };
}

function TestButton({
  channel,
  body,
  disabled,
}: {
  channel: "emby" | "email" | "webhook";
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

function ChannelCard({
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

export function NotificationsPage() {
  const { data, error } = useQuery(notificationsQuery);
  const save = useSaveNotifications();
  const initial = useMemo<Form | undefined>(
    () =>
      data && {
        emby: {
          enabled: data.emby.enabled,
          url: data.emby.url,
          apiKey: "",
          pathFrom: data.emby.pathFrom,
          pathTo: data.emby.pathTo,
        },
        email: {
          enabled: data.email.enabled,
          host: data.email.host,
          port: String(data.email.port),
          username: data.email.username,
          password: "",
          from: data.email.from,
          to: data.email.to,
        },
        webhook: { enabled: data.webhook.enabled, url: data.webhook.url, secret: "" },
      },
    [data],
  );
  const { draft: form, setDraft, dirty, reset, commit } = useDraft(initial, same);

  if (error) return <Alert variant="error">Couldn't load settings: {error.message}</Alert>;
  if (!form || !data) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const err = save.error instanceof RequestError ? save.error : undefined;
  const fe = err?.fields ?? {};
  const body = toBody(form);

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate(body, {
      onSuccess: (saved) =>
        commit({
          emby: { ...saved.emby, apiKey: "" },
          email: { ...saved.email, port: String(saved.email.port), password: "" },
          webhook: { ...saved.webhook, secret: "" },
        }),
    });
  }

  const set = <K extends keyof Form>(k: K, patch: Partial<Form[K]>) =>
    setDraft({ ...form, [k]: { ...form[k], ...patch } });

  return (
    <div className="space-y-6">
      <PageHeader
        title="Notifications"
        description="Tell Emby about changes, and get told about problems."
      />
      {/* noValidate: Reelkeeper checks the addresses and shows its own messages;
          the browser's checks would block Save with a popup instead. */}
      <form onSubmit={submit} noValidate className="space-y-6">
        {err && !Object.keys(fe).length && <Alert variant="error">{err.message}</Alert>}

        <ChannelCard
          id="emby"
          title="Emby"
          description="Reelkeeper tells Emby about each new or changed movie folder, so it appears without a full library scan."
          enabled={form.emby.enabled}
          onEnabled={(v) => set("emby", { enabled: v })}
        >
          <Field
            id="emby-url"
            label="Emby address"
            help="Include the port, like http://192.168.1.30:8096."
            error={fe["emby.url"]}
          >
            <Input
              id="emby-url"
              inputMode="url"
              autoComplete="off"
              placeholder="http://192.168.1.30:8096"
              value={form.emby.url}
              onChange={(e) => set("emby", { url: e.target.value })}
            />
          </Field>
          <Field
            id="emby-key"
            label="API key"
            help="Create one in Emby under Dashboard → Advanced → API Keys."
            error={fe["emby.apiKey"]}
          >
            <Input
              id="emby-key"
              type="password"
              autoComplete="new-password"
              placeholder={data.emby.apiKeySet ? "Saved — leave empty to keep it" : ""}
              value={form.emby.apiKey}
              onChange={(e) => set("emby", { apiKey: e.target.value })}
            />
          </Field>
          <div className="grid gap-5 sm:grid-cols-2">
            <Field id="emby-path-from" label="Reelkeeper's path" error={fe["emby.pathFrom"]}>
              <Input
                id="emby-path-from"
                autoComplete="off"
                placeholder="/media"
                value={form.emby.pathFrom}
                onChange={(e) => set("emby", { pathFrom: e.target.value })}
              />
            </Field>
            <Field id="emby-path-to" label="Emby's path" error={fe["emby.pathTo"]}>
              <Input
                id="emby-path-to"
                autoComplete="off"
                placeholder="/Volumes/Media"
                value={form.emby.pathTo}
                onChange={(e) => set("emby", { pathTo: e.target.value })}
              />
            </Field>
          </div>
          <p className="text-sm text-muted-foreground">
            If Emby sees your media under a different path (for example on a Mac), Reelkeeper's
            /media becomes Emby's /Volumes/Media. Leave both empty if the paths are the same.
          </p>
          <TestButton channel="emby" body={body} disabled={!form.emby.url} />
        </ChannelCard>

        <ChannelCard
          id="email"
          title="Email"
          description="Only problems are emailed: failed imports, stuck downloads Reelkeeper removed, an indexer failing for a day, the download client unreachable."
          enabled={form.email.enabled}
          onEnabled={(v) => set("email", { enabled: v })}
        >
          <div className="grid gap-5 sm:grid-cols-[1fr_8rem]">
            <Field id="email-host" label="Mail server" error={fe["email.host"]}>
              <Input
                id="email-host"
                autoComplete="off"
                placeholder="smtp.example.com"
                value={form.email.host}
                onChange={(e) => set("email", { host: e.target.value })}
              />
            </Field>
            <Field id="email-port" label="Port" error={fe["email.port"]}>
              <Input
                id="email-port"
                type="number"
                min="1"
                value={form.email.port}
                onChange={(e) => set("email", { port: e.target.value })}
              />
            </Field>
          </div>
          <p className="-mt-3 text-sm text-muted-foreground">
            Port 465 uses a secure connection from the start; other ports (587 is usual) switch to
            one before logging in. Reelkeeper never sends your password without one.
          </p>
          <Field id="email-username" label="Username" error={fe["email.username"]}>
            <Input
              id="email-username"
              autoComplete="off"
              value={form.email.username}
              onChange={(e) => set("email", { username: e.target.value })}
            />
          </Field>
          <Field id="email-password" label="Password" error={fe["email.password"]}>
            <Input
              id="email-password"
              type="password"
              autoComplete="new-password"
              placeholder={data.email.passwordSet ? "Saved — leave empty to keep it" : ""}
              value={form.email.password}
              onChange={(e) => set("email", { password: e.target.value })}
            />
          </Field>
          <div className="grid gap-5 sm:grid-cols-2">
            <Field id="email-from" label="From" error={fe["email.from"]}>
              <Input
                id="email-from"
                type="email"
                autoComplete="off"
                placeholder="reelkeeper@example.com"
                value={form.email.from}
                onChange={(e) => set("email", { from: e.target.value })}
              />
            </Field>
            <Field id="email-to" label="Send to" error={fe["email.to"]}>
              <Input
                id="email-to"
                type="email"
                autoComplete="off"
                placeholder="you@example.com"
                value={form.email.to}
                onChange={(e) => set("email", { to: e.target.value })}
              />
            </Field>
          </div>
          <TestButton channel="email" body={body} disabled={!form.email.host || !form.email.to} />
        </ChannelCard>

        <ChannelCard
          id="webhook"
          title="Webhook"
          description="Every event, as JSON, for other tools."
          enabled={form.webhook.enabled}
          onEnabled={(v) => set("webhook", { enabled: v })}
        >
          <Field
            id="webhook-url"
            label="Address"
            help="Reelkeeper sends a POST with the event as JSON."
            error={fe["webhook.url"]}
          >
            <Input
              id="webhook-url"
              inputMode="url"
              autoComplete="off"
              placeholder="https://example.com/reelkeeper"
              value={form.webhook.url}
              onChange={(e) => set("webhook", { url: e.target.value })}
            />
          </Field>
          <Field
            id="webhook-secret"
            label="Secret (optional)"
            help="Sent with every request as the X-Reelkeeper-Secret header, so the receiver can tell it came from Reelkeeper."
            error={fe["webhook.secret"]}
          >
            <Input
              id="webhook-secret"
              type="password"
              autoComplete="new-password"
              placeholder={data.webhook.secretSet ? "Saved — leave empty to keep it" : ""}
              value={form.webhook.secret}
              onChange={(e) => set("webhook", { secret: e.target.value })}
            />
          </Field>
          <TestButton channel="webhook" body={body} disabled={!form.webhook.url} />
        </ChannelCard>

        <SaveBar dirty={dirty} saving={save.isPending} saved={save.isSuccess} onDiscard={reset} />
      </form>
    </div>
  );
}
