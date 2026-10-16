import { useEffect, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  type BackupCheck,
  type BackupEntry,
  backupKind,
  kindText,
  restoreBackup,
} from "@/hooks/backups";
import { urlBase } from "@/lib/utils";

/** Where the restore flow is. Nothing here changes anything until "restarting". */
export type Flow =
  | { name: string; step: "checking" }
  | { name: string; step: "refused"; message: string }
  | { name: string; step: "ready"; check: BackupCheck }
  | { name: string; step: "restarting" };

const familiar: { table: string; one: string; many: string }[] = [
  { table: "titles", one: "title", many: "titles" },
  { table: "history", one: "history entry", many: "history entries" },
  { table: "indexers", one: "indexer", many: "indexers" },
];

function plural(n: number, one: string, many: string) {
  return `${n.toLocaleString()} ${n === 1 ? one : many}`;
}

/** "12,345 rows across 26 tables (140 titles, 2,300 history entries, 12 indexers)". */
export function describeRows(check: BackupCheck): string {
  const tables = Object.keys(check.rows).length;
  const main = `${plural(check.total, "row", "rows")} across ${plural(tables, "table", "tables")}`;
  const parts = familiar
    .filter((f) => check.rows[f.table] !== undefined)
    .map((f) => plural(check.rows[f.table] as number, f.one, f.many));
  return parts.length ? `${main} (${parts.join(", ")})` : main;
}

/**
 * The dialogs of the restore flow. A refused backup shows why and offers no
 * restore button. A good one explains what will happen, in plain words,
 * before the "Restore and restart" button.
 */
export function RestoreDialogs({
  flow,
  entry,
  onClose,
  onRestarting,
}: {
  flow: Flow | undefined;
  /** The list's entry for the backup, if it is still listed. */
  entry: BackupEntry | undefined;
  onClose: () => void;
  onRestarting: (name: string) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  // A new flow starts clean.
  const name = flow?.name;
  // biome-ignore lint/correctness/useExhaustiveDependencies: reset when another backup is picked
  useEffect(() => {
    setBusy(false);
    setError(undefined);
  }, [name]);

  async function restore(n: string) {
    setBusy(true);
    setError(undefined);
    try {
      await restoreBackup(n);
      onRestarting(n);
    } catch (e) {
      setBusy(false);
      setError(e instanceof Error ? e.message : "The restore couldn't be started.");
    }
  }

  const kind = entry ? backupKind(entry) : undefined;

  return (
    <>
      <Dialog open={flow?.step === "refused"} onOpenChange={(o) => !o && onClose()}>
        <DialogContent>
          <DialogTitle>This backup can't be restored</DialogTitle>
          <DialogDescription>{flow?.step === "refused" ? flow.message : ""}</DialogDescription>
          <p className="text-sm text-muted-foreground">Nothing was changed.</p>
          <div className="flex justify-end">
            <DialogClose asChild>
              <Button variant="outline">Close</Button>
            </DialogClose>
          </div>
        </DialogContent>
      </Dialog>

      <Dialog open={flow?.step === "ready"} onOpenChange={(o) => !o && !busy && onClose()}>
        <DialogContent>
          <DialogTitle>Restore this backup?</DialogTitle>
          {flow?.step === "ready" && (
            <>
              <DialogDescription asChild>
                <div className="space-y-1">
                  <p className="font-mono text-xs break-all">{flow.name}</p>
                  <p>
                    Made {new Date(flow.check.createdAt).toLocaleString()} by Reelkeeper{" "}
                    {flow.check.app}
                    {kind ? ` (${kindText[kind].toLowerCase()})` : ""}.
                  </p>
                  <p>It holds {describeRows(flow.check)}.</p>
                </div>
              </DialogDescription>
              <p className="text-sm">
                Reelkeeper first saves a safety backup of everything as it is now. Then it restarts
                and replaces all its data — library, settings, indexers, history — with this backup.
                Your media files aren't touched. Everyone is signed out.
              </p>
              {error && <Alert variant="error">{error}</Alert>}
              <div className="flex flex-wrap justify-end gap-2">
                <Button variant="ghost" disabled={busy} onClick={onClose}>
                  Cancel
                </Button>
                <Button variant="destructive" disabled={busy} onClick={() => restore(flow.name)}>
                  {busy && <span className="spin" aria-hidden="true" />}
                  Restore and restart
                </Button>
              </div>
            </>
          )}
        </DialogContent>
      </Dialog>

      {flow?.step === "restarting" && <RestartOverlay />}
    </>
  );
}

const pollEvery = 2_000;
/** If it never answers "no" (it restarted between two polls), accept "yes" after this. */
const goingDownWithin = 10_000;
const slowAfter = 180_000;

/** Is Reelkeeper answering? /ping makes no external calls and needs no sign-in. */
async function answers(): Promise<boolean> {
  try {
    const req = new Request(`${window.location.origin}${urlBase()}/ping`, { cache: "no-store" });
    const res = await fetch(req, { signal: AbortSignal.timeout?.(3_000) });
    return res.ok;
  } catch {
    return false;
  }
}

/**
 * Covers the page while Reelkeeper restarts to restore. It waits for Reelkeeper
 * to go down (or 10 seconds to pass), then for it to answer again, and then
 * opens the Backups page, which asks for a sign-in first. `destination` is
 * where to go instead, as a path under the URL base (the first-run screen
 * uses "/", which lands on the sign-in page).
 */
export function RestartOverlay({ destination = "/system/backups" }: { destination?: string }) {
  const [slow, setSlow] = useState(false);
  useEffect(() => {
    const started = Date.now();
    let sawDown = false;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    async function tick() {
      const up = await answers();
      if (stopped) return;
      const elapsed = Date.now() - started;
      if (!up) sawDown = true;
      if (up && (sawDown || elapsed >= goingDownWithin)) {
        window.location.assign(`${window.location.origin}${urlBase()}${destination}`);
        return;
      }
      if (elapsed >= slowAfter) setSlow(true);
      timer = setTimeout(tick, pollEvery);
    }
    timer = setTimeout(tick, pollEvery);
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [destination]);

  return (
    <div
      role="alertdialog"
      aria-modal="true"
      aria-labelledby="restart-title"
      aria-describedby="restart-text"
      className="fixed inset-0 z-[70] grid place-items-center bg-background/95 p-6 backdrop-blur-sm"
    >
      <div className="max-w-md space-y-3 text-center">
        <span className="spin spin-lg mx-auto text-primary" aria-hidden="true" />
        <h2 id="restart-title" className="text-lg font-semibold">
          Restarting to restore…
        </h2>
        <p id="restart-text" className="text-sm text-muted-foreground" aria-live="polite">
          {slow
            ? "Reelkeeper hasn't come back yet. If it doesn't, start it again; the restore runs as it starts."
            : "This usually takes under a minute."}
        </p>
      </div>
    </div>
  );
}
