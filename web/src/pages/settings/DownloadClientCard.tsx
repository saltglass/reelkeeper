import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Download } from "lucide-react";
import { type FormEvent, useMemo, useState } from "react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Segmented } from "@/components/ui/segmented";
import {
  clientNames,
  clientProse,
  type DownloadClient,
  type DownloadClientType,
  type DownloadClientUpdate,
  type DownloadMovePreview,
  downloadClientQuery,
  downloadMovePreviewQuery,
  isClientSet,
  useSaveDownloadClient,
  useTestDownloadClient,
} from "@/hooks/downloads";
import { useDraft } from "@/hooks/draft";
import { BuiltinStatusCard } from "./BuiltinStatusCard";
import { BuiltinVpnSection } from "./BuiltinVpnSection";
import { BusyLabel, IconTile } from "./BusyLabel";
import { MoveTorrentsDialog } from "./MoveTorrentsDialog";
import { SaveBar } from "./SaveBar";

type Form = {
  type: DownloadClientType;
  url: string;
  username: string;
  password: string;
  folder: string;
  // The built-in client's settings, as typed (so a half-typed number is kept).
  listenPort: string;
  maxActiveDownloads: string;
  maxActiveSeeds: string;
  downloadLimitKBs: string;
  uploadLimitKBs: string;
  /** The port the VPN provider forwards ("" for none). */
  forwardedPort: string;
  /** A VPN config given but not saved yet ("" for none). It is never loaded back: the server keeps it secret. */
  vpnConfig: string;
};

const builtinKeys = [
  "listenPort",
  "maxActiveDownloads",
  "maxActiveSeeds",
  "downloadLimitKBs",
  "uploadLimitKBs",
  "forwardedPort",
  "vpnConfig",
] as const;

function sameForm(a: Form, b: Form) {
  return (
    a.type === b.type &&
    a.url === b.url &&
    a.username === b.username &&
    a.password === b.password &&
    a.folder === b.folder &&
    builtinKeys.every((k) => a[k] === b[k])
  );
}

/** What the built-in client starts with (the server's defaults). */
const builtinDefaults = {
  listenPort: "6881",
  maxActiveDownloads: "3",
  maxActiveSeeds: "25",
  downloadLimitKBs: "0",
  uploadLimitKBs: "0",
  forwardedPort: "",
  vpnConfig: "",
};

/** The form for a saved client; the built-in fields only mean something when it is the built-in client. */
function formFrom(c: DownloadClient): Form {
  const own = c.type === "builtin";
  return {
    type: c.type,
    url: c.url,
    username: c.username,
    password: "",
    folder: c.folder,
    listenPort: own ? String(c.listenPort) : builtinDefaults.listenPort,
    maxActiveDownloads: own ? String(c.maxActiveDownloads) : builtinDefaults.maxActiveDownloads,
    maxActiveSeeds: own ? String(c.maxActiveSeeds) : builtinDefaults.maxActiveSeeds,
    downloadLimitKBs: own ? String(c.downloadLimitKBs) : builtinDefaults.downloadLimitKBs,
    uploadLimitKBs: own ? String(c.uploadLimitKBs) : builtinDefaults.uploadLimitKBs,
    forwardedPort: own && c.forwardedPort > 0 ? String(c.forwardedPort) : "",
    vpnConfig: "",
  };
}

function toNumber(s: string) {
  const n = Number.parseInt(s, 10);
  return Number.isNaN(n) ? 0 : n;
}

const typeOptions = (Object.keys(clientNames) as DownloadClientType[]).map((value) => ({
  value,
  label: clientNames[value],
  ...(value === "builtin" ? { badge: "Experimental" } : {}),
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
  // The built-in client has no address; this is never shown.
  builtin: { label: "", example: "", help: "" },
};

/** The download client connection (Settings → Download Client). */
export function DownloadClientCard() {
  const { data, error } = useQuery(downloadClientQuery);
  const save = useSaveDownloadClient();
  const test = useTestDownloadClient();
  // The password is never sent to the browser, so the form starts with it empty.
  const initial = useMemo<Form | undefined>(() => data && formFrom(data), [data]);
  const { draft: form, setDraft, dirty, reset, commit } = useDraft(initial, sameForm);
  const qc = useQueryClient();
  // Another kind of client than the saved one: saving moves Reelkeeper's
  // torrents to it. The dialog asks first, with what would move.
  const switching = Boolean(form && data && form.type !== data.type);
  const [asking, setAsking] = useState<DownloadMovePreview>();
  const [leaveSeeding, setLeaveSeeding] = useState(false);
  const [advanced, setAdvanced] = useState(false);

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
  // Reelkeeper's own client has no address or login, but ports and limits.
  const own = form.type === "builtin";
  const address = addressText[form.type];
  // The client being left, if there is one: with none set up, nothing moves.
  const hadClient = isClientSet(data);
  // A VPN is set up (saved, or given and waiting to be saved): the listen
  // port isn't used then, the forwarded port is.
  const withVpn = own && ((data.type === "builtin" && data.hasVpn) || form.vpnConfig !== "");
  // An omitted password means "keep the stored one".
  const body: DownloadClientUpdate = own
    ? {
        type: "builtin",
        url: "",
        username: "",
        folder: form.folder,
        listenPort: toNumber(form.listenPort),
        maxActiveDownloads: toNumber(form.maxActiveDownloads),
        maxActiveSeeds: toNumber(form.maxActiveSeeds),
        downloadLimitKBs: toNumber(form.downloadLimitKBs),
        uploadLimitKBs: toNumber(form.uploadLimitKBs),
        ...(withVpn ? { forwardedPort: toNumber(form.forwardedPort) } : {}),
        ...(form.vpnConfig ? { vpnConfig: form.vpnConfig } : {}),
      }
    : {
        type: form.type,
        url: form.url,
        username: deluge ? "" : form.username,
        folder: form.folder,
        ...(form.password ? { password: form.password } : {}),
      };
  // The speed limits are behind "Show advanced", but an error in one opens it.
  const showAdvanced =
    advanced || Boolean(err?.fields.downloadLimitKBs || err?.fields.uploadLimitKBs);
  const canTest = own ? Boolean(form.folder) : Boolean(form.url);

  function saveNow(leave: boolean) {
    save.mutate(switching && leave ? { ...body, leaveSeeding: true } : body, {
      onSuccess: (saved) => commit(formFrom(saved)),
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
            <CardTitle className="flex flex-wrap items-center gap-2">
              {name}
              {own && <Badge variant="warning">Experimental</Badge>}
            </CardTitle>
            <CardDescription>
              {own
                ? "Reelkeeper downloads torrents itself, with no separate app. Experimental: it works, but it's new. You can add a WireGuard VPN: everything the client does then goes through it, and a kill switch stops every download if it drops."
                : transmission
                  ? "Where Reelkeeper sends the releases it picks. Enter Transmission's RPC address (Transmission 4.0 or newer) and, if you've set one, its login."
                  : deluge
                    ? "Where Reelkeeper sends the releases it picks. Enter the address of Deluge's web UI (Deluge 2.1 or newer) and its password. Turn on the Label plugin in Deluge first."
                    : "Where Reelkeeper sends the releases it picks. Turn on the Web UI in qBittorrent (Options → Web UI) and enter its address and login here."}
            </CardDescription>
          </div>
        </CardHeader>
        <CardContent className="space-y-5">
          {own && data.type === "builtin" && isClientSet(data) && (
            <BuiltinStatusCard canStartWithoutVpn={!data.hasVpn} />
          )}
          <Segmented
            label="Download client"
            options={typeOptions}
            value={form.type}
            onChange={(type) => change({ ...form, type })}
          />
          {err && !Object.keys(err.fields).length && <Alert variant="error">{err.message}</Alert>}
          {!own && (
            <>
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
                    placeholder={
                      data.passwordSet && !switching ? "Saved — leave empty to keep it" : ""
                    }
                    value={form.password}
                    onChange={(e) => change({ ...form, password: e.target.value })}
                  />
                </Field>
              </div>
            </>
          )}
          <Field
            id="dc-folder"
            label="Download folder"
            help={
              own
                ? "Where the built-in client saves its downloads: a full path inside Reelkeeper's container that exists and can be written to. A folder of its own lets Reelkeeper spot leftover files. Leave empty to remove the download client."
                : `Where ${name} saves Reelkeeper's downloads, as ${name} sees it. A folder of its own lets Reelkeeper spot leftover files. Empty: ${name}'s default folder. Only new downloads go there.`
            }
            error={err?.fields.folder}
          >
            <Input
              id="dc-folder"
              type="text"
              placeholder={own ? "/downloads" : "/mnt/downloads/reelkeeper"}
              autoComplete="off"
              value={form.folder}
              onChange={(e) => change({ ...form, folder: e.target.value })}
            />
          </Field>
          {own && (
            <BuiltinVpnSection
              saved={data}
              pending={form.vpnConfig}
              onPending={(vpnConfig) => change({ ...form, vpnConfig })}
              forwardedPort={form.forwardedPort}
              onForwardedPort={(forwardedPort) => change({ ...form, forwardedPort })}
              fieldErrors={err?.fields ?? {}}
            />
          )}
          {own && (
            <>
              <div className="grid gap-5 sm:grid-cols-3">
                {!withVpn && (
                  <Field
                    id="dc-port"
                    label="Listen port"
                    help={`Peers connect to this port. Publish it from the container (e.g. "6881:6881/tcp" and "/udp") so others can reach you; without it, Reelkeeper can still download but finds fewer peers.`}
                    error={err?.fields.listenPort}
                  >
                    <Input
                      id="dc-port"
                      type="number"
                      min="1024"
                      max="65535"
                      className="w-28"
                      value={form.listenPort}
                      onChange={(e) => change({ ...form, listenPort: e.target.value })}
                    />
                  </Field>
                )}
                <Field
                  id="dc-active-downloads"
                  label="Max active downloads"
                  help="How many unfinished torrents download at once. The rest wait in the Queue, in order."
                  error={err?.fields.maxActiveDownloads}
                >
                  <Input
                    id="dc-active-downloads"
                    type="number"
                    min="1"
                    max="50"
                    className="w-28"
                    value={form.maxActiveDownloads}
                    onChange={(e) => change({ ...form, maxActiveDownloads: e.target.value })}
                  />
                </Field>
                <Field
                  id="dc-active-seeds"
                  label="Max active seeding torrents"
                  help="How many finished torrents share at once; the rest wait their turn. 0 means no limit."
                  error={err?.fields.maxActiveSeeds}
                >
                  <Input
                    id="dc-active-seeds"
                    type="number"
                    min="0"
                    max="10000"
                    className="w-28"
                    value={form.maxActiveSeeds}
                    onChange={(e) => change({ ...form, maxActiveSeeds: e.target.value })}
                  />
                </Field>
              </div>
              <div>
                <button
                  type="button"
                  aria-expanded={showAdvanced}
                  onClick={() => setAdvanced(!showAdvanced)}
                  className="flex items-center gap-1 text-sm font-medium text-link hover:underline [&_svg]:size-4"
                >
                  {showAdvanced ? <ChevronDown /> : <ChevronRight />}
                  {showAdvanced ? "Hide advanced" : "Show advanced"}
                </button>
              </div>
              {showAdvanced && (
                <div className="grid gap-5 sm:grid-cols-2">
                  <Field
                    id="dc-down-limit"
                    label="Download speed limit (KB/s)"
                    help="0 means no limit. Changes apply at once."
                    error={err?.fields.downloadLimitKBs}
                  >
                    <Input
                      id="dc-down-limit"
                      type="number"
                      min="0"
                      className="w-32"
                      value={form.downloadLimitKBs}
                      onChange={(e) => change({ ...form, downloadLimitKBs: e.target.value })}
                    />
                  </Field>
                  <Field
                    id="dc-up-limit"
                    label="Upload speed limit (KB/s)"
                    help="0 means no limit. Changes apply at once."
                    error={err?.fields.uploadLimitKBs}
                  >
                    <Input
                      id="dc-up-limit"
                      type="number"
                      min="0"
                      className="w-32"
                      value={form.uploadLimitKBs}
                      onChange={(e) => change({ ...form, uploadLimitKBs: e.target.value })}
                    />
                  </Field>
                </div>
              )}
            </>
          )}
          <p className="text-sm text-muted-foreground">
            {own
              ? "Reelkeeper keeps the built-in client's torrent list in its data folder (in a builtin folder), not in its backups, like an external client's. Torrents it adds are its own: nothing else is ever touched. It never asks your router to open a port."
              : transmission
                ? "Reelkeeper marks its torrents with its own label, reelkeeper-movies (reelkeeper-tv for TV), and never touches other torrents. It turns off Transmission's seeding limits on those torrents one by one: Reelkeeper's cleanup decides when they stop."
                : deluge
                  ? "Reelkeeper marks its torrents with its own label, reelkeeper-movies (reelkeeper-tv for TV), and never touches other torrents. It turns off Deluge's ratio limits and \"move completed\" on those torrents one by one: Reelkeeper's cleanup decides when they stop."
                  : "Reelkeeper uses its own category, reelkeeper-movies, and never touches other torrents."}
          </p>
          <div className="space-y-2" aria-live="polite">
            <Button
              type="button"
              variant="outline"
              disabled={test.isPending || !canTest}
              onClick={() => test.mutate(body)}
            >
              <BusyLabel busy={test.isPending} ok={test.isSuccess}>
                {test.isPending ? "Testing…" : "Test"}
              </BusyLabel>
            </Button>
            {test.data && (
              <Alert variant="success">
                {own
                  ? `The folder ${withVpn ? "is" : "and port are"} fine. ${test.data.version}`
                  : `Connected to ${name} ${test.data.version}`}
              </Alert>
            )}
            {testErr && (
              <Alert variant="error">
                {testErr.fields.url ??
                  testErr.fields.username ??
                  testErr.fields.folder ??
                  testErr.fields.listenPort ??
                  testErr.message}
              </Alert>
            )}
          </div>
          {switching && hadClient && (
            <Alert variant="info">
              Saving moves Reelkeeper's torrents from {clientProse[data.type]} to{" "}
              {clientProse[form.type]}: it checks the files already on disk, so nothing is
              downloaded again, and nothing is deleted.
              {data.type === "builtin" &&
                " Torrents left seeding in the built-in client stop when it is switched off."}
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
