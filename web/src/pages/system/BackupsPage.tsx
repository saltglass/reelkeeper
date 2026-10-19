import { useQuery } from "@tanstack/react-query";
import { Download, RotateCcw, Trash2, Upload } from "lucide-react";
import { type ChangeEvent, type FormEvent, useRef, useState } from "react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, SwitchRow } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import {
  type BackupEntry,
  type BackupSettings,
  backupKind,
  backupSettingsQuery,
  backupsQuery,
  checkBackup,
  kindText,
  type RestoreResult,
  useCreateBackup,
  useDeleteBackup,
  useSaveBackupSettings,
  useUploadBackup,
} from "@/hooks/backups";
import { useDraft } from "@/hooks/draft";
import { toast } from "@/hooks/toast";
import { formatBytes } from "@/lib/format";
import { urlBase } from "@/lib/utils";
import { PageHeader } from "@/pages/PageHeader";
import { SaveBar } from "@/pages/settings/SaveBar";
import { type Flow, RestoreDialogs } from "./RestoreFlow";

const kindVariant = {
  scheduled: "info",
  manual: "neutral",
  safety: "warning",
  uploaded: "neutral",
} as const;

function sameSettings(a: BackupSettings, b: BackupSettings) {
  return (
    a.folder === b.folder && a.scheduled === b.scheduled && a.time === b.time && a.keep === b.keep
  );
}

export function BackupsPage() {
  const list = useQuery(backupsQuery);
  const settings = useQuery(backupSettingsQuery);
  const create = useCreateBackup();
  const upload = useUploadBackup();
  const del = useDeleteBackup();
  const fileInput = useRef<HTMLInputElement>(null);
  const [flow, setFlow] = useState<Flow>();
  const [deleting, setDeleting] = useState<BackupEntry>();

  async function startRestore(name: string) {
    setFlow({ name, step: "checking" });
    try {
      setFlow({ name, step: "ready", check: await checkBackup(name) });
    } catch (e) {
      if (e instanceof RequestError && e.status === 422) {
        setFlow({ name, step: "refused", message: e.message });
        return;
      }
      setFlow(undefined);
      toast(e instanceof Error ? e.message : "The backup couldn't be checked.", "problem");
    }
  }

  function chosen(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    e.target.value = ""; // so choosing the same file again works
    if (!file) return;
    create.reset();
    upload.mutate(file, { onSuccess: (b) => toast(`Uploaded ${b.name}`) });
  }

  if (list.error) return <Alert variant="error">Couldn't load backups: {list.error.message}</Alert>;
  if (!list.data) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const { backups, lastRestore, folder } = list.data;
  const actionError = create.error ?? upload.error ?? del.error;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Backups"
        description="Copies of Reelkeeper's own data: library, settings, indexers, history. Your media files aren't part of them."
      />

      {lastRestore && (
        <RestoreBanner
          result={lastRestore}
          canUndo={Boolean(
            lastRestore.safety && backups.some((b) => b.name === lastRestore.safety),
          )}
          busy={flow?.step === "checking"}
          onUndo={(name) => startRestore(name)}
        />
      )}
      {actionError && <Alert variant="error">{actionError.message}</Alert>}

      <Card>
        <CardHeader className="flex flex-wrap items-start gap-3">
          <div className="min-w-0 flex-[1_1_260px] space-y-1">
            <CardTitle>Backups</CardTitle>
            <CardDescription>
              Kept in <span className="font-mono break-all">{folder}</span>
            </CardDescription>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              disabled={upload.isPending}
              onClick={() => fileInput.current?.click()}
            >
              {upload.isPending ? <span className="spin" aria-hidden="true" /> : <Upload />}
              Upload a backup
            </Button>
            <input
              ref={fileInput}
              type="file"
              accept=".backup"
              aria-label="Backup file"
              className="sr-only"
              tabIndex={-1}
              onChange={chosen}
            />
            <Button
              disabled={create.isPending}
              onClick={() => {
                upload.reset();
                create.mutate(undefined, { onSuccess: () => toast("Backup made") });
              }}
            >
              {create.isPending && <span className="spin" aria-hidden="true" />}
              Back up now
            </Button>
          </div>
        </CardHeader>
        <CardContent className="p-0 pt-4">
          {backups.length === 0 ? (
            <p className="px-5 pb-5 text-sm text-muted-foreground">
              No backups yet.{" "}
              {settings.data?.scheduled
                ? `The first daily backup runs at ${settings.data.time}, or press Back up now.`
                : "Daily backups are off; press Back up now."}
            </p>
          ) : (
            <ul className="divide-y border-t">
              {backups.map((b) => (
                <BackupRow
                  key={b.name}
                  backup={b}
                  checking={flow?.step === "checking" && flow.name === b.name}
                  onRestore={() => startRestore(b.name)}
                  onDelete={() => setDeleting(b)}
                />
              ))}
            </ul>
          )}
        </CardContent>
      </Card>

      {settings.data ? (
        <SettingsCard data={settings.data} />
      ) : settings.error ? (
        <Alert variant="error">Couldn't load the backup settings: {settings.error.message}</Alert>
      ) : null}

      <Card>
        <CardHeader>
          <CardTitle>Secret key</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm">
            Backups keep stored passwords and keys (indexer logins, the download client, email,
            Emby, Jellyfin) sealed with REELKEEPER_SECRET_KEY. Keep a copy of that key somewhere
            safe. Without it, a backup still restores your library, settings and history, but those
            passwords have to be entered again. A backup made with a different key can't be restored
            until Reelkeeper runs with that key.
          </p>
        </CardContent>
      </Card>

      <Dialog open={Boolean(deleting)} onOpenChange={(o) => !o && setDeleting(undefined)}>
        <DialogContent>
          <DialogTitle>Delete this backup?</DialogTitle>
          <DialogDescription>
            <span className="font-mono text-xs break-all">{deleting?.name}</span>
            <br />
            The file is removed from the backup folder. This can't be undone.
          </DialogDescription>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button
              variant="destructive"
              onClick={() => {
                if (deleting) {
                  del.mutate(deleting.name, { onSuccess: () => toast("Backup deleted") });
                }
                setDeleting(undefined);
              }}
            >
              Delete
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      <RestoreDialogs
        flow={flow}
        entry={backups.find((b) => b.name === flow?.name)}
        onClose={() => setFlow(undefined)}
        onRestarting={(name) => setFlow({ name, step: "restarting" })}
      />
    </div>
  );
}

function RestoreBanner({
  result,
  canUndo,
  busy,
  onUndo,
}: {
  result: RestoreResult;
  canUndo: boolean;
  busy: boolean;
  onUndo: (safety: string) => void;
}) {
  const when = new Date(result.at).toLocaleString();
  return (
    <Alert variant={result.ok ? "success" : "error"}>
      {result.ok ? (
        <p className="font-medium">
          Restored {result.backup} on {when}.
        </p>
      ) : (
        <p className="font-medium">
          The restore of {result.backup} didn't happen: {result.message}
        </p>
      )}
      {result.ok && result.message && <p>{result.message}</p>}
      {/* A restore that didn't happen changed nothing, so there is nothing to undo. */}
      {result.ok && result.safety && canUndo && (
        <p className="flex flex-wrap items-center gap-x-3 gap-y-1">
          To undo it, restore {result.safety} (made just before).
          <Button
            variant="outline"
            size="sm"
            disabled={busy}
            onClick={() => onUndo(result.safety as string)}
          >
            <RotateCcw /> Undo this restore
          </Button>
        </p>
      )}
    </Alert>
  );
}

function BackupRow({
  backup,
  checking,
  onRestore,
  onDelete,
}: {
  backup: BackupEntry;
  checking: boolean;
  onRestore: () => void;
  onDelete: () => void;
}) {
  const kind = backupKind(backup);
  const href = `${urlBase()}/api/v1/backups/${encodeURIComponent(backup.name)}`;
  return (
    <li className="flex flex-wrap items-center gap-x-4 gap-y-2 px-5 py-3">
      <div className="min-w-0 flex-[1_1_260px] space-y-1">
        <p className="flex flex-wrap items-center gap-2">
          <span className="font-medium">
            {backup.createdAt ? new Date(backup.createdAt).toLocaleString() : backup.name}
          </span>
          {kind && <Badge variant={kindVariant[kind]}>{kindText[kind]}</Badge>}
          {!backup.problem && !backup.keyMatches && (
            <Badge variant="warning">Made with a different secret key</Badge>
          )}
          {backup.problem && <Badge variant="error">{backup.problem}</Badge>}
        </p>
        <p className="text-xs text-muted-foreground">
          {backup.app && <>Reelkeeper {backup.app} · </>}
          {formatBytes(backup.size)}
          {backup.createdAt && <span className="font-mono"> · {backup.name}</span>}
        </p>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button asChild variant="outline" size="sm">
          <a href={href} download={backup.name} aria-label={`Download ${backup.name}`}>
            <Download /> Download
          </a>
        </Button>
        <Button
          variant="outline"
          size="sm"
          disabled={checking || Boolean(backup.problem)}
          aria-label={`Restore ${backup.name}`}
          onClick={onRestore}
        >
          {checking ? <span className="spin" aria-hidden="true" /> : <RotateCcw />}
          Restore…
        </Button>
        <Button variant="ghost" size="sm" aria-label={`Delete ${backup.name}`} onClick={onDelete}>
          <Trash2 /> Delete
        </Button>
      </div>
    </li>
  );
}

function toInt(s: string) {
  const n = Number.parseInt(s, 10);
  return Number.isNaN(n) ? 0 : n;
}

function SettingsCard({ data }: { data: BackupSettings }) {
  const save = useSaveBackupSettings();
  const { draft: form, setDraft, dirty, reset, commit } = useDraft(data, sameSettings);
  if (!form) return null;

  const err = save.error instanceof RequestError ? save.error : undefined;
  const fe = err?.fields ?? {};
  const set = (patch: Partial<BackupSettings>) => setDraft({ ...form, ...patch });

  function submit(e: FormEvent) {
    e.preventDefault();
    if (!form) return;
    const body = {
      folder: form.folder,
      scheduled: form.scheduled,
      time: form.time,
      keep: form.keep,
    };
    save.mutate(body, { onSuccess: commit });
  }

  return (
    <form onSubmit={submit}>
      <Card>
        <CardHeader>
          <CardTitle>Settings</CardTitle>
          <CardDescription>
            When Reelkeeper backs up on its own, how many it keeps, and where they go.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {err && Object.keys(fe).length === 0 && <Alert variant="error">{err.message}</Alert>}
          <SwitchRow
            id="backup-scheduled"
            label="Daily backup"
            help="Reelkeeper makes a backup once a day, at the time below."
            error={fe.scheduled}
          >
            <Switch
              id="backup-scheduled"
              checked={form.scheduled}
              onCheckedChange={(v) => set({ scheduled: v })}
            />
          </SwitchRow>
          <div className="grid gap-5 sm:grid-cols-2">
            <Field
              id="backup-time"
              label="Time"
              help="Local time, on a 24-hour clock."
              error={fe.time}
            >
              <Input
                id="backup-time"
                type="time"
                className="w-36"
                value={form.time}
                onChange={(e) => set({ time: e.target.value })}
              />
            </Field>
            <Field
              id="backup-keep"
              label="Keep"
              help="Scheduled and manual backups past this number are deleted, oldest first. Safety and uploaded backups aren't counted."
              error={fe.keep}
            >
              <div className="flex items-center gap-2">
                <Input
                  id="backup-keep"
                  type="number"
                  min="1"
                  max="365"
                  className="w-28"
                  value={form.keep}
                  onChange={(e) => set({ keep: toInt(e.target.value) })}
                />
                <span className="text-sm text-muted-foreground">backups</span>
              </div>
            </Field>
          </div>
          <Field
            id="backup-folder"
            label="Folder"
            help="Leave empty to use Reelkeeper's data folder. In Docker, mount the folder you want into the container."
            error={fe.folder}
          >
            <Input
              id="backup-folder"
              className="font-mono"
              placeholder={data.effectiveFolder}
              value={form.folder}
              onChange={(e) => set({ folder: e.target.value })}
            />
          </Field>
        </CardContent>
      </Card>
      <SaveBar dirty={dirty} saving={save.isPending} saved={save.isSuccess} onDiscard={reset} />
    </form>
  );
}
