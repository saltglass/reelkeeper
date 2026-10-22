import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpToLine, type LucideIcon, Pause, Play, RotateCcw, X } from "lucide-react";
import { useState } from "react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  atSentenceStart,
  type QueueItem,
  queueQuery,
  type TorrentState,
  useMoveDownloadToTop,
  usePauseDownload,
  useRemoveFromQueue,
  useResumeDownload,
  useRetryImport,
} from "@/hooks/downloads";
import { useAppearance } from "@/hooks/theme";
import { toast } from "@/hooks/toast";
import { formatBytes } from "@/lib/format";
import { shortDuration } from "@/lib/time";
import { cn } from "@/lib/utils";
import { PageHeader } from "@/pages/PageHeader";
import { MovieName, PosterThumb, posterKey, riseStyle, useLeaving, usePosters } from "./shared";

/** Whether someone stopped the download before it finished. */
export function isPaused(state: TorrentState): boolean {
  return state === "paused";
}

/** A download's state in plain English; `clientName` is the name of the client holding it. */
export function stateLabel(state: TorrentState, clientName: string): string {
  if (state === "downloading") return "Downloading";
  // Usenet jobs' own states.
  if (state === "repairing") return "Checking and repairing files";
  if (state === "unpacking") return "Unpacking";
  if (state === "completed") return "Downloaded, waiting to import";
  if (state === "failed") return `${atSentenceStart(clientName)} couldn't download it`;
  if (state === "stalled") return "Stalled (no peers sending)";
  if (state === "metadata") return "Getting torrent details";
  if (state === "queued") return `Queued in ${clientName}`;
  if (state === "paused") return "Paused";
  if (state === "seeding" || state === "seeding-stalled") return "Downloaded, waiting to import";
  if (state === "seeding-paused") return "Downloaded, paused";
  if (state === "error" || state === "missing-files")
    return `${atSentenceStart(clientName)} reports an error`;
  if (state === "checking") return "Checking files";
  if (state === "moving") return "Moving files";
  return "Unknown";
}

export type BarKind = "active" | "stalled" | "fetching" | "done";

/** Which look the progress bar gets for a torrent state. */
export function barKind(state: TorrentState, progress: number): BarKind {
  // Repairing and unpacking take as long as they take: no percentage to show.
  if (state === "metadata" || state === "repairing" || state === "unpacking") return "fetching";
  if (state === "stalled" || state === "error" || state === "missing-files" || state === "failed")
    return "stalled";
  if (progress >= 1 || state === "seeding" || state === "seeding-stalled" || state === "completed")
    return "done";
  return "active";
}

/** What the Queue says while the built-in client's downloads are stopped by its VPN. */
const vpnBanners: Record<string, string> = {
  down: "VPN down: the built-in client's downloads are stopped until it reconnects.",
  connecting: "VPN connecting: the built-in client's downloads start when it is connected.",
  held: "The built-in client's downloads are stopped until you start them without the VPN.",
};

export function QueuePage() {
  const { data, error } = useQuery(queueQuery);
  const posters = usePosters();
  const [removing, setRemoving] = useState<{ item: QueueItem; index: number }>();
  const { leave, merge } = useLeaving<QueueItem>();
  const totalSpeed = (data?.items ?? []).reduce((sum, it) => sum + (it.torrent?.speed ?? 0), 0);
  const rows = merge(data?.items ?? []);

  return (
    <div>
      <PageHeader
        title="Queue"
        description={
          data &&
          `What Reelkeeper has sent to ${data.usenetClientName ? `${data.clientName} and ${data.usenetClientName}` : data.clientName} and is waiting for, and downloads that finished but couldn't be imported.`
        }
        actions={
          totalSpeed > 0 && (
            <Badge variant="info" className="px-3 py-1 text-sm tabular-nums">
              <span className="eq" aria-hidden="true">
                <i />
                <i />
                <i />
                <i />
              </span>
              {formatBytes(totalSpeed)}/s down
            </Badge>
          )
        }
      />
      {error && <Alert variant="error">Couldn't load the queue: {error.message}</Alert>}
      {!data && !error && <p className="text-sm text-muted-foreground">Loading…</p>}
      {data && (
        <div className="space-y-4">
          {data.full && (
            <Alert variant="warning">
              <p>
                The queue is full: {data.items.length} of {data.limit} downloads. Reelkeeper won't
                grab anything new until some finish (Seerr requests still go through). Change the
                limit in{" "}
                <Link to="/settings/download-client" className="text-link hover:underline">
                  Settings → Download Client
                </Link>
                .
              </p>
            </Alert>
          )}
          {data.builtinState && data.builtinState in vpnBanners && (
            <Alert variant="warning">
              <p>
                {vpnBanners[data.builtinState]} See{" "}
                <Link to="/settings/download-client" className="text-link hover:underline">
                  Settings → Download Client
                </Link>
                .
              </p>
            </Alert>
          )}
          {data.clientError && (
            <Alert variant="warning">
              Couldn't ask how downloads are going: {data.clientError} The list below shows only
              what Reelkeeper remembers.
            </Alert>
          )}
          {rows.length === 0 ? (
            <Card className="p-6 text-sm text-muted-foreground">
              Nothing is downloading. See{" "}
              <Link to="/activity/history" className="text-link hover:underline">
                History
              </Link>{" "}
              for what Reelkeeper has done lately.
            </Card>
          ) : (
            <Card className="overflow-hidden">
              <ul>
                {rows.map(({ item, leaving }, i) => (
                  <QueueRow
                    key={item.id}
                    item={item}
                    clientName={rowClient(item, data)}
                    index={i}
                    leaving={leaving}
                    poster={
                      item.movie ? posters.get(posterKey(item.mediaType, item.movie.id)) : undefined
                    }
                    onRemove={() => setRemoving({ item, index: i })}
                  />
                ))}
              </ul>
            </Card>
          )}
        </div>
      )}
      <RemoveDialog
        item={removing?.item}
        clientName={removing && data ? rowClient(removing.item, data) : "the download client"}
        onClose={() => setRemoving(undefined)}
        onRemoved={() => removing && leave(removing.item, removing.index)}
      />
    </div>
  );
}

/** The name of the client holding a download: the Usenet client's for a Usenet one. */
function rowClient(item: QueueItem, data: { clientName: string; usenetClientName: string | null }) {
  if (item.protocol === "usenet") return data.usenetClientName ?? "the Usenet client";
  return data.clientName;
}

/** Usenet jobs can be paused, resumed and moved only while still in the client's queue. */
const usenetQueueStates: TorrentState[] = ["downloading", "queued", "paused"];

function QueueRow({
  item: it,
  clientName,
  index,
  leaving,
  poster,
  onRemove,
}: {
  item: QueueItem;
  /** The download client's name, for the wording. */
  clientName: string;
  index: number;
  leaving: boolean;
  poster?: string;
  onRemove: () => void;
}) {
  const { appearance } = useAppearance();
  const symbols = appearance.queueSymbols;
  const retry = useRetryImport();
  const pause = usePauseDownload();
  const resume = useResumeDownload();
  const top = useMoveDownloadToTop();
  const t = it.torrent;
  const percent = t ? Math.floor(t.progress * 100) : 0;
  const failed = it.status === "import-failed";
  const retryError = retry.error instanceof RequestError ? retry.error.message : undefined;
  // Pause, Resume and Move to top are for a download that's still going: not
  // a failed import, and not a torrent that has finished or that the download
  // client didn't report.
  // A download on its way to another client can't be touched until it arrives.
  const moving = it.movingTo != null;
  const usenet = it.protocol === "usenet";
  const canControl =
    it.status === "downloading" &&
    t != null &&
    t.progress < 1 &&
    !moving &&
    (!usenet || usenetQueueStates.includes(t.state));
  const paused = t != null && isPaused(t.state);
  const actions = [pause, resume, top];
  const busy = moving || retry.isPending || actions.some((m) => m.isPending);
  const actionError = actions.map((m) => m.error).find((e) => e instanceof RequestError)?.message;
  /** Runs one action; an earlier action's error is cleared first. */
  function act(m: (typeof actions)[number]) {
    for (const other of actions) other.reset();
    m.mutate(it.id);
  }
  const kind = t ? barKind(t.state, t.progress) : "active";
  const stateVariant =
    kind === "stalled"
      ? "warning"
      : kind === "done"
        ? "success"
        : kind === "fetching" || paused
          ? "neutral"
          : "info";

  return (
    <li
      className={cn(
        "q-row animate-rise grid grid-cols-[minmax(0,1fr)] items-start gap-x-4 gap-y-3 border-b px-4 py-4 last:border-b-0 sm:grid-cols-[2.75rem_minmax(0,1fr)_auto] sm:px-5",
        leaving && "row-leave",
      )}
      style={riseStyle(index)}
    >
      <PosterThumb path={poster} className="hidden sm:block" />
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-semibold break-words">
            <MovieName movie={it.movie} mediaType={it.mediaType} episodes={it.episodes} />
          </span>
          {it.profile && <Badge>{it.profile}</Badge>}
          {failed && <Badge variant="error">Import failed</Badge>}
          {moving && (
            <Badge variant="info">
              <span className="ping-dot" aria-hidden="true" />
              Moving to {it.movingTo}
            </Badge>
          )}
          {t && !failed && !moving && (
            <Badge variant={stateVariant}>
              {(kind === "active" && !paused) || kind === "stalled" ? (
                <span className="ping-dot" aria-hidden="true" />
              ) : null}
              {stateLabel(t.state, clientName)}
            </Badge>
          )}
        </div>
        <p className="mt-0.5 font-mono text-xs break-all text-muted-foreground">
          {it.releaseTitle}
        </p>
        <p className="mt-0.5 text-xs text-muted-foreground">
          {[it.quality, it.indexer, usenet && "Usenet"].filter(Boolean).join(" · ")}
        </p>
        {failed && (
          <p className="mt-2 text-sm break-words text-destructive">{retryError ?? it.message}</p>
        )}
        {actionError && <p className="mt-2 text-sm break-words text-destructive">{actionError}</p>}
        {/* A note while it waits, e.g. finished but the download folder can't be seen. */}
        {!failed && it.message && (
          <p className="mt-2 text-sm break-words text-muted-foreground">{it.message}</p>
        )}
        {/* What the client says: why a Usenet job failed, or how its repair is going. */}
        {t?.message && !failed && (
          <p
            className={cn(
              "mt-2 text-sm break-words",
              t.state === "failed" ? "text-destructive" : "text-muted-foreground",
            )}
          >
            {t.message}
          </p>
        )}
        {t ? (
          <>
            <div
              role="progressbar"
              aria-label={`${it.releaseTitle} progress`}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={percent}
              className="q-bar"
            >
              {kind === "fetching" && <span className="q-indet" />}
              {kind !== "fetching" && (
                <div
                  className={cn(
                    "q-fill",
                    kind === "stalled" && "warn",
                    (kind === "done" || failed) && "done",
                  )}
                  style={{ width: `${percent}%` }}
                />
              )}
            </div>
            <p className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground tabular-nums">
              <span className="font-semibold text-foreground">{percent}%</span>
              {failed && <span>{stateLabel(t.state, clientName)}</span>}
              {t.speed > 0 && <span>{formatBytes(t.speed)}/s</span>}
              {t.etaSeconds != null && <span>{shortDuration(t.etaSeconds)} left</span>}
              {!usenet && (
                <span>
                  {/* The tracker's swarm count is often stale and misses seeds found
                      through DHT, so it can be lower than the connected count. */}
                  {t.swarmSeeds && t.swarmSeeds > t.seeds
                    ? `connected to ${t.seeds} of ${t.swarmSeeds} seeds`
                    : `connected to ${t.seeds} seeds`}{" "}
                  · {t.peers} peers
                </span>
              )}
              <span>{formatBytes(t.size)}</span>
            </p>
          </>
        ) : (
          <p className="mt-2 text-xs text-muted-foreground">
            {atSentenceStart(clientName)} didn't report this {usenet ? "download" : "torrent"} just
            now.
          </p>
        )}
      </div>
      <div className={cn("flex flex-wrap gap-2", !symbols && "sm:flex-col")}>
        {failed && (
          <RowButton
            symbols={symbols}
            icon={RotateCcw}
            label="Retry import"
            pendingLabel="Retrying…"
            pending={retry.isPending}
            disabled={busy}
            onClick={() => retry.mutate(it.id)}
          />
        )}
        {canControl && paused && (
          <RowButton
            symbols={symbols}
            icon={Play}
            label="Resume"
            pendingLabel="Resuming…"
            pending={resume.isPending}
            disabled={busy}
            onClick={() => act(resume)}
          />
        )}
        {canControl && !paused && (
          <>
            <RowButton
              symbols={symbols}
              icon={Pause}
              label="Pause"
              pendingLabel="Pausing…"
              pending={pause.isPending}
              disabled={busy}
              onClick={() => act(pause)}
            />
            <RowButton
              symbols={symbols}
              icon={ArrowUpToLine}
              label="Move to top"
              pendingLabel="Moving…"
              pending={top.isPending}
              title={`Only matters while ${clientName} queues downloads`}
              symbolTitle={`Move to top (only matters while ${clientName} queues downloads)`}
              disabled={busy}
              onClick={() => act(top)}
            />
          </>
        )}
        <RowButton symbols={symbols} icon={X} label="Remove" disabled={busy} onClick={onRemove} />
      </div>
    </li>
  );
}

/**
 * On a desktop in a dark theme the row buttons' 1px border nearly vanished at
 * the rounded corners (on a 1080p screen). 2px: on a normal-density screen browsers
 * round 1.5px down to 1px, so anything between does nothing there.
 */
const rowBorder = "sm:dark:border-2";

/**
 * One of a row's buttons: the word, or (when symbols are on) an icon that
 * keeps the word as its name and tooltip. While its action runs the word
 * becomes `pendingLabel`, and the icon turns into a spinner.
 */
function RowButton({
  symbols,
  icon: Icon,
  label,
  pendingLabel,
  pending = false,
  title,
  symbolTitle = label,
  disabled,
  onClick,
}: {
  symbols: boolean;
  icon: LucideIcon;
  label: string;
  /** Shown while the action runs; Remove has none, since it opens a dialog first. */
  pendingLabel?: string;
  pending?: boolean;
  /** The tooltip when showing words. */
  title?: string;
  /** The tooltip when showing a symbol. */
  symbolTitle?: string;
  disabled: boolean;
  onClick: () => void;
}) {
  if (!symbols)
    return (
      <Button
        variant="outline"
        size="sm"
        className={rowBorder}
        disabled={disabled}
        title={title}
        onClick={onClick}
      >
        {pending ? pendingLabel : label}
      </Button>
    );
  return (
    <Button
      variant="tile"
      size="icon"
      className={rowBorder}
      disabled={disabled}
      aria-label={pending ? pendingLabel : label}
      title={symbolTitle}
      onClick={onClick}
    >
      {pending ? <span className="slib-spin" aria-hidden="true" /> : <Icon aria-hidden="true" />}
    </Button>
  );
}

function RemoveDialog({
  item,
  clientName,
  onClose,
  onRemoved,
}: {
  item?: QueueItem;
  clientName: string;
  onClose: () => void;
  onRemoved: () => void;
}) {
  const remove = useRemoveFromQueue();
  const [blocklist, setBlocklist] = useState(true);
  const [searchAgain, setSearchAgain] = useState(true);
  const err = remove.error instanceof RequestError ? remove.error : undefined;

  function close() {
    remove.reset();
    setBlocklist(true);
    setSearchAgain(true);
    onClose();
  }

  return (
    <Dialog open={Boolean(item)} onOpenChange={(open) => !open && close()}>
      <DialogContent>
        <DialogTitle>Remove from {clientName} and delete what was downloaded?</DialogTitle>
        <DialogDescription className="break-all">{item?.releaseTitle}</DialogDescription>
        <div className="space-y-2">
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              className="mt-1"
              checked={searchAgain}
              onChange={(e) => setSearchAgain(e.target.checked)}
            />
            Search for another release
          </label>
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              className="mt-1"
              checked={blocklist || searchAgain}
              disabled={searchAgain}
              onChange={(e) => setBlocklist(e.target.checked)}
            />
            <span>
              Blocklist this release, so Reelkeeper never grabs it again
              {searchAgain && (
                <span className="block text-muted-foreground">
                  Needed so the new search picks a different release. You can unblock it later under
                  System → Blocklist.
                </span>
              )}
            </span>
          </label>
        </div>
        {err && <Alert variant="error">{err.message}</Alert>}
        <div className="flex justify-end gap-2">
          <DialogClose asChild>
            <Button variant="ghost">Cancel</Button>
          </DialogClose>
          <Button
            variant="destructive"
            disabled={remove.isPending}
            onClick={() =>
              item &&
              remove.mutate(
                { id: item.id, blocklist: blocklist || searchAgain, searchAgain },
                {
                  onSuccess: () => {
                    onRemoved();
                    toast("Removed from the queue", "ok");
                    close();
                  },
                },
              )
            }
          >
            {remove.isPending ? "Removing…" : "Remove"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
