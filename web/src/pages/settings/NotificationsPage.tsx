import { useQuery } from "@tanstack/react-query";
import { type FormEvent, useMemo } from "react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useDraft } from "@/hooks/draft";
import {
  type NotificationSettings,
  type NotificationSettingsUpdate,
  notificationsQuery,
  useSaveNotifications,
} from "@/hooks/notifications";
import { PageHeader } from "@/pages/PageHeader";
import { ChannelCard, pageError, savedBody, TestButton } from "./NotificationParts";
import { SaveBar } from "./SaveBar";

// Emby, Jellyfin and Plex are on the Media servers page.
const channels = ["email", "webhook"] as const;

/** The form's values. Secrets start empty: the server never sends them. */
type Form = {
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

function toForm(d: NotificationSettings): Form {
  return {
    email: {
      enabled: d.email.enabled,
      host: d.email.host,
      port: String(d.email.port),
      username: d.email.username,
      password: "",
      from: d.email.from,
      to: d.email.to,
    },
    webhook: { enabled: d.webhook.enabled, url: d.webhook.url, secret: "" },
  };
}

/** What the API takes: a secret is sent only if it was typed (otherwise the stored one is kept). */
function toBody(f: Form, saved: NotificationSettings): NotificationSettingsUpdate {
  const port = Number.parseInt(f.email.port, 10);
  return {
    ...savedBody(saved),
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

export function NotificationsPage() {
  const { data, error } = useQuery(notificationsQuery);
  const save = useSaveNotifications();
  const initial = useMemo(() => data && toForm(data), [data]);
  const { draft: form, setDraft, dirty, reset, commit } = useDraft(initial, same);

  if (error) return <Alert variant="error">Couldn't load settings: {error.message}</Alert>;
  if (!form || !data) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const err = save.error instanceof RequestError ? save.error : undefined;
  const fe = err?.fields ?? {};
  const message = pageError(err, channels);
  const body = toBody(form, data);

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate(body, { onSuccess: (saved) => commit(toForm(saved)) });
  }

  const set = <K extends keyof Form>(k: K, patch: Partial<Form[K]>) =>
    setDraft({ ...form, [k]: { ...form[k], ...patch } });

  return (
    <div className="space-y-6">
      <PageHeader
        title="Notifications"
        description="Get told about problems, or send every event to other tools."
      />
      {/* noValidate: Reelkeeper checks the addresses and shows its own messages;
          the browser's checks would block Save with a popup instead. */}
      <form onSubmit={submit} noValidate className="space-y-6">
        {message && <Alert variant="error">{message}</Alert>}

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
