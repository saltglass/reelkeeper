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

const serverIds = ["emby", "jellyfin", "plex"] as const;
type Server = (typeof serverIds)[number];

type ServerForm = {
  enabled: boolean;
  url: string;
  apiKey: string;
  pathFrom: string;
  pathTo: string;
};

/** The form's values. Keys start empty: the server never sends them. */
type Form = Record<Server, ServerForm>;

function same(a: Form, b: Form) {
  return JSON.stringify(a) === JSON.stringify(b);
}

function toForm(d: NotificationSettings): Form {
  const server = ({ apiKeySet: _, ...s }: NotificationSettings["emby"]) => ({ ...s, apiKey: "" });
  return { emby: server(d.emby), jellyfin: server(d.jellyfin), plex: server(d.plex) };
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

/** What the API takes: a key is sent only if it was typed (otherwise the stored one is kept). */
function toBody(f: Form, saved: NotificationSettings): NotificationSettingsUpdate {
  return {
    ...savedBody(saved),
    emby: serverBody(f.emby),
    jellyfin: serverBody(f.jellyfin),
    plex: serverBody(f.plex),
  };
}

/** What differs between the media server cards. */
const servers: Record<
  Server,
  {
    name: string;
    keyLabel: string;
    keyHelp: string;
    port: number;
    pathExample: string;
    note?: string;
  }
> = {
  emby: {
    name: "Emby",
    keyLabel: "API key",
    keyHelp: "Create one in Emby under Dashboard → Advanced → API Keys.",
    port: 8096,
    pathExample: "/Volumes/Media",
  },
  jellyfin: {
    name: "Jellyfin",
    keyLabel: "API key",
    keyHelp: "Create one in Jellyfin under Dashboard → API Keys.",
    port: 8096,
    pathExample: "/data/media",
  },
  plex: {
    name: "Plex",
    keyLabel: "Token",
    keyHelp:
      "Your Plex token. In Plex Web, open any movie's ⋯ menu → Get Info → View XML, and copy what follows X-Plex-Token= in the new tab's address.",
    port: 32400,
    pathExample: "/data",
    note: 'Reelkeeper asks Plex to scan just the changed folder, in the Plex library that holds it; Test lists your libraries and any media folder none of them holds. A deleted or renamed movie leaves Plex only while its Settings → Library → "Empty trash automatically after every scan" is on (the default).',
  },
};

/** The card for Emby, Jellyfin or Plex: the same fields, told about the same events. */
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
  const { name, keyLabel, keyHelp, port, pathExample, note } = servers[server];
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
        help={`Include the port, like http://192.168.1.30:${port}.`}
        error={errors[`${server}.url`]}
      >
        <Input
          id={`${server}-url`}
          inputMode="url"
          autoComplete="off"
          placeholder={`http://192.168.1.30:${port}`}
          value={form.url}
          onChange={(e) => onChange({ url: e.target.value })}
        />
      </Field>
      <Field
        id={`${server}-key`}
        label={keyLabel}
        help={keyHelp}
        error={errors[`${server}.apiKey`]}
      >
        <Input
          id={`${server}-key`}
          aria-label={`${name} ${keyLabel}`}
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
      {note && <p className="text-sm text-muted-foreground">{note}</p>}
      <TestButton channel={server} body={body} disabled={!form.url} />
    </ChannelCard>
  );
}

export function MediaServersPage() {
  const { data, error } = useQuery(notificationsQuery);
  const save = useSaveNotifications();
  const initial = useMemo(() => data && toForm(data), [data]);
  const { draft: form, setDraft, dirty, reset, commit } = useDraft(initial, same);

  if (error) return <Alert variant="error">Couldn't load settings: {error.message}</Alert>;
  if (!form || !data) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const err = save.error instanceof RequestError ? save.error : undefined;
  const fe = err?.fields ?? {};
  const message = pageError(err, serverIds);
  const body = toBody(form, data);

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate(body, { onSuccess: (saved) => commit(toForm(saved)) });
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Media servers"
        description="Tell Emby, Jellyfin or Plex about new, changed and removed movies and shows, so they show up without a full library scan."
      />
      {/* noValidate: Reelkeeper checks the addresses and shows its own messages;
          the browser's checks would block Save with a popup instead. */}
      <form onSubmit={submit} noValidate className="space-y-6">
        {message && <Alert variant="error">{message}</Alert>}

        {serverIds.map((server) => (
          <MediaServerCard
            key={server}
            server={server}
            form={form[server]}
            keySet={data[server].apiKeySet}
            errors={fe}
            body={body}
            onChange={(patch) => setDraft({ ...form, [server]: { ...form[server], ...patch } })}
          />
        ))}

        <SaveBar dirty={dirty} saving={save.isPending} saved={save.isSuccess} onDiscard={reset} />
      </form>
    </div>
  );
}
