import { useQuery } from "@tanstack/react-query";
import { Newspaper } from "lucide-react";
import { type FormEvent, useMemo } from "react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  type UsenetClient,
  type UsenetClientUpdate,
  usenetClientQuery,
  useSaveUsenetClient,
  useTestUsenetClient,
} from "@/hooks/downloads";
import { useDraft } from "@/hooks/draft";
import { BusyLabel, IconTile } from "./BusyLabel";
import { SaveBar } from "./SaveBar";

type Form = { url: string; apiKey: string };

const sameForm = (a: Form, b: Form) => a.url === b.url && a.apiKey === b.apiKey;

/** The form for the saved settings: the API key is never sent to the browser. */
const formFrom = (u: UsenetClient): Form => ({ url: u.url, apiKey: "" });

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
  // An omitted key means "keep the stored one".
  const body: UsenetClientUpdate = {
    type: "sabnzbd",
    url: form.url,
    ...(form.apiKey ? { apiKey: form.apiKey } : {}),
  };

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
            <CardTitle>Usenet: SABnzbd</CardTitle>
            <CardDescription>
              Optional. With a Usenet client, Reelkeeper also searches your Usenet (Newznab)
              indexers and sends what it picks there. Your quality profile still decides; between
              two equally good releases, Usenet wins, since it's usually faster and leaves nothing
              to seed.
            </CardDescription>
          </div>
        </CardHeader>
        <CardContent className="space-y-5">
          {err && !Object.keys(err.fields).length && <Alert variant="error">{err.message}</Alert>}
          <div className="grid gap-5 sm:grid-cols-2">
            <Field
              id="usenet-url"
              label="Address"
              help="Include the port. Needs SABnzbd 4.0 or newer. Leave empty to remove the Usenet client."
              error={err?.fields.url ?? err?.fields.type}
            >
              <Input
                id="usenet-url"
                type="text"
                inputMode="url"
                placeholder="http://192.168.1.10:8080"
                autoComplete="off"
                value={form.url}
                onChange={(e) => change({ ...form, url: e.target.value })}
              />
            </Field>
            <Field
              id="usenet-api-key"
              label="API key"
              help="In SABnzbd: Config → General → Security."
              error={err?.fields.apiKey}
            >
              <Input
                id="usenet-api-key"
                type="password"
                autoComplete="new-password"
                placeholder={data.apiKeySet ? "Saved — leave empty to keep it" : ""}
                value={form.apiKey}
                onChange={(e) => change({ ...form, apiKey: e.target.value })}
              />
            </Field>
          </div>
          <p className="text-sm text-muted-foreground">
            Reelkeeper creates its own categories in SABnzbd, reelkeeper-movies and reelkeeper-tv,
            and never touches other jobs. SABnzbd's completed folder must be one Reelkeeper can see
            at the same path, like the torrent client's. Once a download is imported, Reelkeeper
            removes it from SABnzbd with its files (cleanup, so dry run applies); a download SABnzbd
            gives up on is blocklisted and searched again.
          </p>
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
            {test.data && <Alert variant="success">Connected to SABnzbd {test.data.version}</Alert>}
            {test.data?.warnings.map((w) => (
              <Alert key={w} variant="warning">
                {w}
              </Alert>
            ))}
            {testErr && (
              <Alert variant="error">
                {testErr.fields.url ?? testErr.fields.apiKey ?? testErr.message}
              </Alert>
            )}
          </div>
        </CardContent>
      </Card>
      <SaveBar dirty={dirty} saving={save.isPending} saved={save.isSuccess} onDiscard={reset} />
    </form>
  );
}
