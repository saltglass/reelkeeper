import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Download } from "lucide-react";
import { type FormEvent, useMemo, useState } from "react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Segmented } from "@/components/ui/segmented";
import {
  clientNames,
  type DownloadClientType,
  type DownloadMovePreview,
  downloadClientQuery,
  downloadMovePreviewQuery,
  useSaveDownloadClient,
  useTestDownloadClient,
} from "@/hooks/downloads";
import { useDraft } from "@/hooks/draft";
import { BusyLabel, IconTile } from "./BusyLabel";
import { MoveTorrentsDialog } from "./MoveTorrentsDialog";
import { SaveBar } from "./SaveBar";

type Form = {
  type: DownloadClientType;
  url: string;
  username: string;
  password: string;
  folder: string;
};

function sameForm(a: Form, b: Form) {
  return (
    a.type === b.type &&
    a.url === b.url &&
    a.username === b.username &&
    a.password === b.password &&
    a.folder === b.folder
  );
}

const typeOptions = (Object.keys(clientNames) as DownloadClientType[]).map((value) => ({
  value,
  label: clientNames[value],
}));

/** What each kind of client calls its address, with an example. */
const addressText: Record<DownloadClientType, { label: string; example: string; help: string }> = {
  qbittorrent: {
    label: "Web UI address",
    example: "http://192.168.1.10:8080",
    help: "Include the port. Leave empty to remove the download client.",
  },
  transmission: {
    label: "RPC address",
    example: "http://192.168.1.10:9091",
    help: "Include the port. Needs Transmission 4.0 or newer. Leave empty to remove the download client.",
  },
  deluge: {
    label: "Web UI address",
    example: "http://192.168.1.10:8112",
    help: "Include the port. Needs Deluge 2.1 or newer with its web UI (deluge-web) running, and the Label plugin turned on (Preferences → Plugins). Leave empty to remove the download client.",
  },
};

/** The download client connection (Settings → Download Client). */
export function DownloadClientCard() {
  const { data, error } = useQuery(downloadClientQuery);
  const save = useSaveDownloadClient();
  const test = useTestDownloadClient();
  // The password is never sent to the browser, so the form starts with it empty.
  const initial = useMemo<Form | undefined>(
    () =>
      data && {
        type: data.type,
        url: data.url,
        username: data.username,
        password: "",
        folder: data.folder,
      },
    [data],
  );
  const { draft: form, setDraft, dirty, reset, commit } = useDraft(initial, sameForm);
  const qc = useQueryClient();
  // Another kind of client than the saved one: saving moves Reelkeeper's
  // torrents to it. The dialog asks first, with what would move.
  const switching = Boolean(form && data && form.type !== data.type);
  const [asking, setAsking] = useState<DownloadMovePreview>();
  const [leaveSeeding, setLeaveSeeding] = useState(false);

  if (error) return <Alert variant="error">Couldn't load settings: {error.message}</Alert>;
  if (!form || !data) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const err = save.error instanceof RequestError ? save.error : undefined;
  const testErr = test.error instanceof RequestError ? test.error : undefined;
  const change = (next: Form) => {
    setDraft(next);
    test.reset();
  };
  const name = clientNames[form.type];
  const transmission = form.type === "transmission";
  // Deluge's web UI has a password and no username.
  const deluge = form.type === "deluge";
  const address = addressText[form.type];
  // The client being left, if there is one: with none set up, nothing moves.
  const hadClient = Boolean(data.url);
  // An omitted password means "keep the stored one".
  const body = {
    type: form.type,
    url: form.url,
    username: deluge ? "" : form.username,
    folder: form.folder,
    ...(form.password ? { password: form.password } : {}),
  };

  function saveNow(leave: boolean) {
    save.mutate(switching && leave ? { ...body, leaveSeeding: true } : body, {
      onSuccess: (saved) =>
        commit({
          type: saved.type,
          url: saved.url,
          username: saved.username,
          password: "",
          folder: saved.folder,
        }),
      onSettled: () => setAsking(undefined),
    });
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!(switching && hadClient)) return saveNow(false);
    // Ask what would move; with nothing to move the type just changes.
    try {
      const preview = await qc.fetchQuery(downloadMovePreviewQuery);
      if (preview.total === 0) return saveNow(false);
      setLeaveSeeding(false);
      setAsking(preview);
    } catch {
      // Couldn't ask: the server decides (and says why if it can't go ahead).
      saveNow(false);
    }
  }

  return (
    <form onSubmit={submit}>
      <Card>
        <CardHeader className="flex flex-row items-start gap-3">
          <IconTile>
            <Download />
          </IconTile>
          <div className="min-w-0 space-y-1">
            <CardTitle>{name}</CardTitle>
            <CardDescription>
              {transmission
                ? "Where Reelkeeper sends the releases it picks. Enter Transmission's RPC address (Transmission 4.0 or newer) and, if you've set one, its login."
                : deluge
                  ? "Where Reelkeeper sends the releases it picks. Enter the address of Deluge's web UI (Deluge 2.1 or newer) and its password. Turn on the Label plugin in Deluge first."
                  : "Where Reelkeeper sends the releases it picks. Turn on the Web UI in qBittorrent (Options → Web UI) and enter its address and login here."}
            </CardDescription>
          </div>
        </CardHeader>
        <CardContent className="space-y-5">
          <Segmented
            label="Download client"
            options={typeOptions}
            value={form.type}
            onChange={(type) => change({ ...form, type })}
          />
          {err && !Object.keys(err.fields).length && <Alert variant="error">{err.message}</Alert>}
          <Field
            id="dc-url"
            label={address.label}
            help={address.help}
            error={err?.fields.url ?? err?.fields.type}
          >
            <Input
              id="dc-url"
              type="text"
              inputMode="url"
              placeholder={address.example}
              autoComplete="off"
              value={form.url}
              onChange={(e) => change({ ...form, url: e.target.value })}
            />
          </Field>
          <div className={deluge ? undefined : "grid gap-5 sm:grid-cols-2"}>
            {!deluge && (
              <Field
                id="dc-username"
                label="Username"
                help={
                  transmission
                    ? "Optional: leave empty if Transmission's RPC login is off."
                    : undefined
                }
                error={err?.fields.username}
              >
                <Input
                  id="dc-username"
                  autoComplete="off"
                  value={form.username}
                  onChange={(e) => change({ ...form, username: e.target.value })}
                />
              </Field>
            )}
            <Field
              id="dc-password"
              label="Password"
              help={deluge ? "The password of Deluge's web UI. It has no username." : undefined}
              error={err?.fields.password}
            >
              <Input
                id="dc-password"
                type="password"
                autoComplete="new-password"
                placeholder={data.passwordSet && !switching ? "Saved — leave empty to keep it" : ""}
                value={form.password}
                onChange={(e) => change({ ...form, password: e.target.value })}
              />
            </Field>
          </div>
          <Field
            id="dc-folder"
            label="Download folder"
            help={`Where ${name} saves Reelkeeper's downloads, as ${name} sees it. A folder of its own lets Reelkeeper spot leftover files. Empty: ${name}'s default folder. Only new downloads go there.`}
            error={err?.fields.folder}
          >
            <Input
              id="dc-folder"
              type="text"
              placeholder="/mnt/downloads/reelkeeper"
              autoComplete="off"
              value={form.folder}
              onChange={(e) => change({ ...form, folder: e.target.value })}
            />
          </Field>
          <p className="text-sm text-muted-foreground">
            {transmission
              ? "Reelkeeper marks its torrents with its own label, reelkeeper-movies (reelkeeper-tv for TV), and never touches other torrents. It turns off Transmission's seeding limits on those torrents one by one: Reelkeeper's cleanup decides when they stop."
              : deluge
                ? "Reelkeeper marks its torrents with its own label, reelkeeper-movies (reelkeeper-tv for TV), and never touches other torrents. It turns off Deluge's ratio limits and \"move completed\" on those torrents one by one: Reelkeeper's cleanup decides when they stop."
                : "Reelkeeper uses its own category, reelkeeper-movies, and never touches other torrents."}
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
            {test.data && (
              <Alert variant="success">
                Connected to {name} {test.data.version}
              </Alert>
            )}
            {testErr && (
              <Alert variant="error">
                {testErr.fields.url ?? testErr.fields.username ?? testErr.message}
              </Alert>
            )}
          </div>
          {switching && hadClient && (
            <Alert variant="info">
              Saving moves Reelkeeper's torrents from {clientNames[data.type]} to {name}: it checks
              the files already on disk, so nothing is downloaded again, and nothing is deleted.
            </Alert>
          )}
        </CardContent>
      </Card>
      <SaveBar dirty={dirty} saving={save.isPending} saved={save.isSuccess} onDiscard={reset} />
      {asking && (
        <MoveTorrentsDialog
          open
          from={data.type}
          to={form.type}
          preview={asking}
          leaveSeeding={leaveSeeding}
          saving={save.isPending}
          onLeaveSeedingChange={setLeaveSeeding}
          onConfirm={() => saveNow(leaveSeeding)}
          onCancel={() => setAsking(undefined)}
        />
      )}
    </form>
  );
}
