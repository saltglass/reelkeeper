import { useQuery } from "@tanstack/react-query";
import { Newspaper } from "lucide-react";
import { type FormEvent, useMemo } from "react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Segmented } from "@/components/ui/segmented";
import {
  type UsenetClient,
  type UsenetClientType,
  type UsenetClientUpdate,
  usenetClientQuery,
  useSaveUsenetClient,
  useTestUsenetClient,
} from "@/hooks/downloads";
import { useDraft } from "@/hooks/draft";
import { BusyLabel, IconTile } from "./BusyLabel";
import { SaveBar } from "./SaveBar";

type Form = {
  type: UsenetClientType;
  url: string;
  /** NZBGet's login. */
  username: string;
  password: string;
  /** SABnzbd's API key. */
  apiKey: string;
};

const sameForm = (a: Form, b: Form) =>
  a.type === b.type &&
  a.url === b.url &&
  a.username === b.username &&
  a.password === b.password &&
  a.apiKey === b.apiKey;

/** The form for the saved settings: the API key and password are never sent to the browser. */
const formFrom = (u: UsenetClient): Form => ({
  type: u.type,
  url: u.url,
  username: u.username,
  password: "",
  apiKey: "",
});

const clientNames: Record<UsenetClientType, string> = { sabnzbd: "SABnzbd", nzbget: "NZBGet" };
const typeOptions = (Object.keys(clientNames) as UsenetClientType[]).map((value) => ({
  value,
  label: clientNames[value],
}));

/** What each Usenet client's address looks like and needs, and where to find its login. */
const clientText: Record<
  UsenetClientType,
  { example: string; addressHelp: string; loginHelp: string; notes: string }
> = {
  sabnzbd: {
    example: "http://192.168.1.10:8080",
    addressHelp:
      "Include the port. Needs SABnzbd 4.0 or newer. Leave empty to remove the Usenet client.",
    loginHelp: "In SABnzbd: Config → General → Security.",
    notes:
      "Reelkeeper creates its own categories in SABnzbd, reelkeeper-movies and reelkeeper-tv, and never touches other jobs. SABnzbd's completed folder must be one Reelkeeper can see at the same path, like the torrent client's. Once a download is imported, Reelkeeper removes it from SABnzbd with its files (cleanup, so dry run applies); a download SABnzbd gives up on is blocklisted and searched again.",
  },
  nzbget: {
    example: "http://192.168.1.10:6789",
    addressHelp:
      "Include the port. Needs NZBGet 21.0 or newer. Leave empty to remove the Usenet client.",
    loginHelp: "In NZBGet: Settings → Security (ControlUsername and ControlPassword).",
    notes:
      "Reelkeeper's downloads use the categories reelkeeper-movies and reelkeeper-tv, which NZBGet files in their own folders under its DestDir (nothing to set up, and Reelkeeper never changes NZBGet's settings). NZBGet's DestDir must be a folder Reelkeeper can see at the same path, like the torrent client's. Once a download is imported, Reelkeeper removes it from NZBGet with its files (cleanup, so dry run applies); a download NZBGet gives up on is blocklisted and searched again.",
  },
};

/**
 * The Usenet download client (Settings → Download Client). It is separate from
 * the torrent client: either or both can be set up, and Usenet indexers are
 * searched only while this one is.
 */
export function UsenetClientCard() {
  const { data, error } = useQuery(usenetClientQuery);
  const save = useSaveUsenetClient();
  const test = useTestUsenetClient();
  const initial = useMemo<Form | undefined>(() => data && formFrom(data), [data]);
  const { draft: form, setDraft, dirty, reset, commit } = useDraft(initial, sameForm);

  if (error) return <Alert variant="error">Couldn't load settings: {error.message}</Alert>;
  if (!form || !data) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const err = save.error instanceof RequestError ? save.error : undefined;
  const testErr = test.error instanceof RequestError ? test.error : undefined;
  const change = (next: Form) => {
    setDraft(next);
    test.reset();
  };
  const name = clientNames[form.type];
  const text = clientText[form.type];
  // A stored key or password is only kept for the client it was saved for.
  const kept = data.type === form.type;
  // An omitted key or password means "keep the stored one".
  const body: UsenetClientUpdate =
    form.type === "nzbget"
      ? {
          type: "nzbget",
          url: form.url,
          username: form.username,
          ...(form.password ? { password: form.password } : {}),
        }
      : { type: "sabnzbd", url: form.url, ...(form.apiKey ? { apiKey: form.apiKey } : {}) };

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate(body, { onSuccess: (saved) => commit(formFrom(saved)) });
  }

  return (
    <form onSubmit={submit}>
      <Card>
        <CardHeader className="flex flex-row items-start gap-3">
          <IconTile>
            <Newspaper />
          </IconTile>
          <div className="min-w-0 space-y-1">
            <CardTitle>Usenet: {name}</CardTitle>
            <CardDescription>
              Optional. With a Usenet client, Reelkeeper also searches your Usenet (Newznab)
              indexers and sends what it picks there. Your quality profile still decides; between
              two equally good releases, Usenet wins, since it's usually faster and leaves nothing
              to seed.
            </CardDescription>
          </div>
        </CardHeader>
        <CardContent className="space-y-5">
          <Segmented
            label="Usenet client"
            options={typeOptions}
            value={form.type}
            onChange={(type) => change({ ...form, type })}
          />
          {err && !Object.keys(err.fields).length && <Alert variant="error">{err.message}</Alert>}
          <div className="grid gap-5 sm:grid-cols-2">
            <Field
              id="usenet-url"
              label="Address"
              help={text.addressHelp}
              error={err?.fields.url ?? err?.fields.type}
            >
              <Input
                id="usenet-url"
                type="text"
                inputMode="url"
                placeholder={text.example}
                autoComplete="off"
                value={form.url}
                onChange={(e) => change({ ...form, url: e.target.value })}
              />
            </Field>
            {form.type === "sabnzbd" ? (
              <Field
                id="usenet-api-key"
                label="API key"
                help={text.loginHelp}
                error={err?.fields.apiKey}
              >
                <Input
                  id="usenet-api-key"
                  type="password"
                  autoComplete="new-password"
                  placeholder={kept && data.apiKeySet ? "Saved — leave empty to keep it" : ""}
                  value={form.apiKey}
                  onChange={(e) => change({ ...form, apiKey: e.target.value })}
                />
              </Field>
            ) : (
              <>
                <Field
                  id="usenet-username"
                  label="Username"
                  help={text.loginHelp}
                  error={err?.fields.username}
                >
                  <Input
                    id="usenet-username"
                    type="text"
                    autoComplete="off"
                    placeholder="nzbget"
                    value={form.username}
                    onChange={(e) => change({ ...form, username: e.target.value })}
                  />
                </Field>
                <Field id="usenet-password" label="Password" error={err?.fields.password}>
                  <Input
                    id="usenet-password"
                    type="password"
                    autoComplete="new-password"
                    placeholder={kept && data.passwordSet ? "Saved — leave empty to keep it" : ""}
                    value={form.password}
                    onChange={(e) => change({ ...form, password: e.target.value })}
                  />
                </Field>
              </>
            )}
          </div>
          <p className="text-sm text-muted-foreground">{text.notes}</p>
          <div className="space-y-2" aria-live="polite">
            <Button
              type="button"
              variant="outline"
              disabled={test.isPending || !form.url}
              onClick={() => test.mutate(body)}
            >
              <BusyLabel busy={test.isPending} ok={test.isSuccess}>
                {test.isPending ? "Testing…" : "Test"}
              </BusyLabel>
            </Button>
            {test.data && (
              <Alert variant="success">
                Connected to {name} {test.data.version}
              </Alert>
            )}
            {test.data?.warnings.map((w) => (
              <Alert key={w} variant="warning">
                {w}
              </Alert>
            ))}
            {testErr && (
              <Alert variant="error">
                {testErr.fields.url ??
                  testErr.fields.apiKey ??
                  testErr.fields.username ??
                  testErr.fields.password ??
                  testErr.message}
              </Alert>
            )}
          </div>
        </CardContent>
      </Card>
      <SaveBar dirty={dirty} saving={save.isPending} saved={save.isSuccess} onDiscard={reset} />
    </form>
  );
}
