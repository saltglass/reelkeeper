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

type Server = "emby" | "jellyfin";
type Channel = Server | "email" | "webhook";

type ServerForm = {
  enabled: boolean;
  url: string;
  apiKey: string;
  pathFrom: string;
  pathTo: string;
};

/** The form's values. Secrets start empty: the server never sends them. */
type Form = {
  emby: ServerForm;
  jellyfin: ServerForm;
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

function serverBody(f: ServerForm): NotificationSettingsUpdate["emby"] {
  return {
    enabled: f.enabled,
    url: f.url,
    pathFrom: f.pathFrom,
    pathTo: f.pathTo,
    ...(f.apiKey ? { apiKey: f.apiKey } : {}),
  };
}

/** What the API takes: a secret is sent only if it was typed (otherwise the stored one is kept). */
function toBody(f: Form): NotificationSettingsUpdate {
  const port = Number.parseInt(f.email.port, 10);
  return {
    emby: serverBody(f.emby),
    jellyfin: serverBody(f.jellyfin),
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

/** What differs between the Emby and Jellyfin cards. */
const servers: Record<Server, { name: string; keyHelp: string; pathExample: string }> = {
  emby: {
    name: "Emby",
    keyHelp: "Create one in Emby under Dashboard → Advanced → API Keys.",
    pathExample: "/Volumes/Media",
  },
  jellyfin: {
    name: "Jellyfin",
    keyHelp: "Create one in Jellyfin under Dashboard → API Keys.",
    pathExample: "/data/media",
  },
};

/** The card for Emby or Jellyfin: the same fields, told about the same events. */
function MediaServerCard({
  server,
  form,
  keySet,
  errors,
  body,
  onChange,
}: {
  server: Server;
  form: ServerForm;
  keySet: boolean;
  errors: Record<string, string>;
  body: NotificationSettingsUpdate;
  onChange: (patch: Partial<ServerForm>) => void;
}) {
  const { name, keyHelp, pathExample } = servers[server];
  return (
    <ChannelCard
      id={server}
      title={name}
      description={`Reelkeeper tells ${name} about each new or changed movie or show folder, so it appears without a full library scan.`}
      enabled={form.enabled}
      onEnabled={(v) => onChange({ enabled: v })}
    >
      <Field
        id={`${server}-url`}
        label={`${name} address`}
        help="Include the port, like http://192.168.1.30:8096."
        error={errors[`${server}.url`]}
      >
        <Input
          id={`${server}-url`}
          inputMode="url"
          autoComplete="off"
          placeholder="http://192.168.1.30:8096"
          value={form.url}
          onChange={(e) => onChange({ url: e.target.value })}
        />
      </Field>
      <Field id={`${server}-key`} label="API key" help={keyHelp} error={errors[`${server}.apiKey`]}>
        <Input
          id={`${server}-key`}
          aria-label={`${name} API key`}
          type="password"
          autoComplete="new-password"
          placeholder={keySet ? "Saved — leave empty to keep it" : ""}
          value={form.apiKey}
          onChange={(e) => onChange({ apiKey: e.target.value })}
        />
      </Field>
      <div className="grid gap-5 sm:grid-cols-2">
        <Field
          id={`${server}-path-from`}
          label="Reelkeeper's path"
          error={errors[`${server}.pathFrom`]}
        >
          <Input
            id={`${server}-path-from`}
            aria-label={`Reelkeeper's path for ${name}`}
            autoComplete="off"
            placeholder="/media"
            value={form.pathFrom}
            onChange={(e) => onChange({ pathFrom: e.target.value })}
          />
        </Field>
        <Field id={`${server}-path-to`} label={`${name}'s path`} error={errors[`${server}.pathTo`]}>
          <Input
            id={`${server}-path-to`}
            autoComplete="off"
            placeholder={pathExample}
            value={form.pathTo}
            onChange={(e) => onChange({ pathTo: e.target.value })}
          />
        </Field>
      </div>
      <p className="text-sm text-muted-foreground">
        If {name} sees your media under a different path (for example on another computer, or in its
        own container), Reelkeeper's /media becomes {name}'s {pathExample}. Leave both empty if the
        paths are the same.
      </p>
      <TestButton channel={server} body={body} disabled={!form.url} />
    </ChannelCard>
  );
}

export function NotificationsPage() {
  const { data, error } = useQuery(notificationsQuery);
  const save = useSaveNotifications();
  const initial = useMemo<Form | undefined>(
    () =>
      data && {
        emby: { ...data.emby, apiKey: "" },
        jellyfin: { ...data.jellyfin, apiKey: "" },
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
          jellyfin: { ...saved.jellyfin, apiKey: "" },
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
        description="Tell Emby or Jellyfin about changes, and get told about problems."
      />
      {/* noValidate: Reelkeeper checks the addresses and shows its own messages;
          the browser's checks would block Save with a popup instead. */}
      <form onSubmit={submit} noValidate className="space-y-6">
        {err && !Object.keys(fe).length && <Alert variant="error">{err.message}</Alert>}

        {(["emby", "jellyfin"] as const).map((server) => (
          <MediaServerCard
            key={server}
            server={server}
            form={form[server]}
            keySet={data[server].apiKeySet}
            errors={fe}
            body={body}
            onChange={(patch) => set(server, patch)}
          />
        ))}

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
