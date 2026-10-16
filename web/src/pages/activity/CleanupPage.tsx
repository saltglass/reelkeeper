import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Trash2 } from "lucide-react";
import { type ReactNode, useState } from "react";
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
import {
  type CleanupEvent,
  cleanupEventsQuery,
  cleanupOrphansQuery,
  cleanupSeedingQuery,
  cleanupSettingsQuery,
  fetchCleanupEvents,
  type SeedingItem,
  useRemoveOrphanFile,
  useRemoveOrphanTorrent,
} from "@/hooks/cleanup";
import { atSentenceStart, useClientName } from "@/hooks/downloads";
import { toast } from "@/hooks/toast";
import { formatBytes } from "@/lib/format";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { PageHeader } from "@/pages/PageHeader";
import { MovieName, riseStyle } from "./shared";

const pageSize = 50;

const ruleLabels: Record<string, string> = {
  seeded: "Done seeding",
  stalled: "No progress",
  slow: "Too slow",
  metadata: "No torrent details",
  unregistered: "Deleted on tracker",
  missing: "Files missing",
  "import-failed": "Import failed",
  "replaced-files": "Replaced files",
};

export function ruleLabel(rule: string) {
  return ruleLabels[rule] ?? rule;
}

const actionBadges: Record<
  CleanupEvent["action"],
  { label: string; variant: "error" | "warning" | "neutral" | "info" }
> = {
  removed: { label: "Removed", variant: "error" },
  "would-remove": { label: "Would remove", variant: "warning" },
  emptied: { label: "Emptied", variant: "error" },
  "would-empty": { label: "Would empty", variant: "warning" },
};

/** "3 days", "5 hours", "20 minutes" for the seeded time. */
function seededFor(seconds: number) {
  const m = Math.floor(seconds / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h} h`;
  return `${Math.floor(h / 24)} days`;
}

export function CleanupPage() {
  const client = useClientName();
  return (
    <div className="space-y-6">
      <PageHeader
        title="Seeding & cleanup"
        description={`How Reelkeeper tidies ${client}: torrents that finished seeding, stuck downloads, and leftovers it doesn't recognise.`}
        actions={
          <Button asChild variant="outline">
            <Link to="/settings/cleanup">Cleanup rules</Link>
          </Button>
        }
      />
      <ModeBanner />
      <SeedingCard />
      <LogCard />
      <OrphansCard />
    </div>
  );
}

function ModeBanner() {
  const { data } = useQuery(cleanupSettingsQuery);
  const client = useClientName();
  if (!data || data.mode === "on") return null;
  const settings = (
    <Link to="/settings/cleanup" className="text-link hover:underline">
      Settings → Cleanup
    </Link>
  );
  return (
    <Alert variant="warning">
      {data.mode === "dryrun" ? (
        <>Cleanup is in dry run: nothing below was actually removed. Change this in {settings}.</>
      ) : (
        <>
          Cleanup is off: Reelkeeper removes nothing from {client}. Turn it on in {settings}.
        </>
      )}
    </Alert>
  );
}

/** How far a torrent is toward the seeding goal (ratio or hours, whichever comes first), 0 to 1. */
function goalProgress(
  t: NonNullable<SeedingItem["torrent"]>,
  goal: { seedRatio: number; seedHours: number } | undefined,
): number | undefined {
  if (!goal) return undefined;
  const parts: number[] = [];
  if (goal.seedRatio > 0) parts.push(t.ratio / goal.seedRatio);
  if (goal.seedHours > 0) parts.push(t.seedingTimeSeconds / (goal.seedHours * 3600));
  if (parts.length === 0) return undefined;
  return Math.min(1, Math.max(...parts));
}

function GoalRing({ progress, label }: { progress: number; label: string }) {
  const pct = Math.floor(progress * 100);
  return (
    <svg width="48" height="48" viewBox="0 0 44 44" role="img" aria-label={label}>
      <circle cx="22" cy="22" r="16" fill="none" strokeWidth="4" stroke="var(--muted)" />
      <circle
        className="seed-ring"
        cx="22"
        cy="22"
        r="16"
        fill="none"
        strokeWidth="4"
        strokeLinecap="round"
        pathLength={100}
        strokeDasharray={`${pct} 101`}
        transform="rotate(-90 22 22)"
        stroke={progress >= 1 ? "var(--success)" : "var(--primary)"}
      />
      <text
        x="22"
        y="26"
        textAnchor="middle"
        fontSize="10"
        fontWeight="600"
        fill="var(--foreground)"
      >
        {pct}%
      </text>
    </svg>
  );
}

function SeedingCard() {
  const { data, error } = useQuery(cleanupSeedingQuery);
  const client = useClientName();
  const settings = useQuery(cleanupSettingsQuery);
  const off = settings.data?.mode === "off";
  const goal = settings.data;
  return (
    <Card>
      <CardHeader>
        <CardTitle>Seeding</CardTitle>
        <CardDescription>
          Imported movies whose torrents are still in {client}, and what cleanup waits for before
          removing them.
          {goal && (goal.seedRatio > 0 || goal.seedHours > 0) && (
            <>
              {" "}
              Each ring fills toward your goal:{" "}
              {[
                goal.seedRatio > 0 && `ratio ${goal.seedRatio}`,
                goal.seedHours > 0 && `${goal.seedHours} hours`,
              ]
                .filter(Boolean)
                .join(" or ")}
              , whichever comes first.
            </>
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className="p-0 pt-4">
        {error && <Alert variant="error">Couldn't load this: {error.message}</Alert>}
        {!data && !error && <p className="px-5 pb-5 text-sm text-muted-foreground">Loading…</p>}
        {data &&
          (data.items.length === 0 ? (
            <p className="px-5 pb-5 text-sm text-muted-foreground">
              Nothing is seeding. Torrents of imported movies show up here until cleanup removes
              them.
            </p>
          ) : (
            <ul>
              {data.items.map((it, i) => (
                <SeedingRow key={it.downloadId} item={it} off={off} index={i} goal={goal} />
              ))}
            </ul>
          ))}
      </CardContent>
    </Card>
  );
}

function SeedingRow({
  item: it,
  off,
  index,
  goal,
}: {
  item: SeedingItem;
  off: boolean;
  index: number;
  goal?: { seedRatio: number; seedHours: number };
}) {
  const t = it.torrent;
  const client = useClientName();
  const progress = t ? goalProgress(t, goal) : undefined;
  let status: ReactNode;
  let pill: ReactNode = null;
  if (!t) status = `${atSentenceStart(client)} doesn't have this torrent any more.`;
  else if (it.waiting) {
    status = `Waiting for: ${it.waiting}`;
    pill = <Badge variant="info">Seeding</Badge>;
  } else if (off) status = "Cleanup is off.";
  else pill = <Badge variant="success">Ready to remove</Badge>;
  return (
    <li
      className="animate-rise grid grid-cols-[3rem_minmax(0,1fr)] items-center gap-x-4 gap-y-2 border-t px-5 py-4 sm:grid-cols-[3rem_minmax(0,1fr)_auto]"
      style={riseStyle(index)}
    >
      {progress != null ? (
        <GoalRing
          progress={progress}
          label={`${Math.floor(progress * 100)}% of the way to the seeding goal`}
        />
      ) : (
        <span aria-hidden="true" className="size-12 rounded-full border-4 border-muted" />
      )}
      <div className="min-w-0 space-y-0.5">
        <p className="font-semibold break-words">
          <MovieName movie={it.movie} mediaType={it.mediaType} episodes={it.episodes} />
        </p>
        <p className="font-mono text-xs break-all text-muted-foreground">{it.releaseTitle}</p>
        {t && (
          <p className="text-xs text-muted-foreground">
            Ratio {t.ratio.toFixed(2)} · seeded {seededFor(t.seedingTimeSeconds)}
          </p>
        )}
        {status && <p className="text-sm">{status}</p>}
      </div>
      {pill && <div className="col-start-2 sm:col-start-auto">{pill}</div>}
    </li>
  );
}

function LogCard() {
  const { data, error } = useQuery(cleanupEventsQuery(pageSize));
  const [older, setOlder] = useState<CleanupEvent[]>([]);
  const [cursor, setCursor] = useState<number | null | undefined>(undefined);
  const [loading, setLoading] = useState(false);
  const [moreError, setMoreError] = useState<string>();

  const next = data ? (cursor === undefined ? data.nextBefore : cursor) : null;
  const shown = data ? [...data.events, ...older] : [];

  async function loadMore() {
    if (next == null) return;
    setLoading(true);
    setMoreError(undefined);
    try {
      const page = await fetchCleanupEvents(pageSize, next);
      setOlder((o) => [...o, ...page.events]);
      setCursor(page.nextBefore);
    } catch (e) {
      setMoreError(e instanceof Error ? e.message : "Couldn't load more.");
    } finally {
      setLoading(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Log</CardTitle>
        <CardDescription>Everything cleanup removed, or would have, and why.</CardDescription>
      </CardHeader>
      <CardContent className="p-0 pt-4">
        {error && <Alert variant="error">Couldn't load the log: {error.message}</Alert>}
        {!data && !error && <p className="px-5 pb-5 text-sm text-muted-foreground">Loading…</p>}
        {data && shown.length === 0 && (
          <p className="px-5 pb-5 text-sm text-muted-foreground">
            Nothing yet. Cleanup hasn't removed anything, or found anything it would remove.
          </p>
        )}
        {shown.length > 0 && (
          <ul>
            {shown.map((e, i) => {
              const badge = actionBadges[e.action];
              const dry = e.action.startsWith("would");
              return (
                <li
                  key={e.id}
                  className="animate-rise grid grid-cols-[2.5rem_minmax(0,1fr)] items-start gap-4 border-t px-5 py-4"
                  style={riseStyle(i)}
                >
                  <span
                    className={cn("tl-node size-10", dry ? "k-warn" : "k-removed")}
                    aria-hidden="true"
                  >
                    <Trash2 className="size-4" />
                  </span>
                  <div className="min-w-0 space-y-1">
                    <div className="flex flex-wrap items-center gap-2">
                      {e.movie && (
                        <span className="font-semibold break-words">
                          <MovieName movie={e.movie} />
                        </span>
                      )}
                      <Badge variant={badge.variant}>{badge.label}</Badge>
                      <span className="text-sm font-medium">{ruleLabel(e.rule)}</span>
                      <span
                        className="text-xs text-muted-foreground"
                        title={new Date(e.createdAt).toLocaleString()}
                      >
                        {relativeTime(e.createdAt)}
                      </span>
                    </div>
                    {e.releaseTitle && (
                      <p className="font-mono text-xs break-all text-muted-foreground">
                        {e.releaseTitle}
                      </p>
                    )}
                    <p className="text-sm break-words text-muted-foreground">{e.message}</p>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
        {moreError && <Alert variant="error">{moreError}</Alert>}
        {next != null && (
          <div className="border-t p-4 text-center">
            <Button variant="outline" disabled={loading} onClick={loadMore}>
              {loading ? "Loading…" : "Load more"}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

type Pending = { kind: "torrent"; hash: string; name: string } | { kind: "file"; name: string };

function OrphansCard() {
  const { data, error } = useQuery(cleanupOrphansQuery);
  const client = useClientName();
  const [pending, setPending] = useState<Pending>();
  const removeTorrent = useRemoveOrphanTorrent();
  const removeFile = useRemoveOrphanFile();
  const mutation = pending?.kind === "file" ? removeFile : removeTorrent;
  const err = mutation.error instanceof RequestError ? mutation.error : undefined;

  function close() {
    removeTorrent.reset();
    removeFile.reset();
    setPending(undefined);
  }
  function confirm() {
    if (!pending) return;
    const done = (text: string) => () => {
      toast(text, "ok");
      close();
    };
    if (pending.kind === "torrent")
      removeTorrent.mutate(pending.hash, { onSuccess: done("Torrent removed") });
    else removeFile.mutate(pending.name, { onSuccess: done("File deleted") });
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Not Reelkeeper's</CardTitle>
        <CardDescription>
          Things in Reelkeeper's corner of {client} that Reelkeeper doesn't know about. Reelkeeper
          only reports them; nothing here is removed unless you ask.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4 p-0 pt-4">
        {error && <Alert variant="error">Couldn't load this: {error.message}</Alert>}
        {!data && !error && <p className="px-5 pb-5 text-sm text-muted-foreground">Loading…</p>}
        {data?.clientError && (
          <div className="px-5">
            <Alert variant="warning">
              Couldn't ask {client}: {data.clientError}
            </Alert>
          </div>
        )}
        {data && !data.clientError && (
          <>
            <section aria-label="Torrents Reelkeeper doesn't track">
              <h3 className="px-5 pb-2 text-sm font-medium">Torrents</h3>
              {data.torrents.length === 0 ? (
                <p className="px-5 pb-2 text-sm text-muted-foreground">
                  Every torrent in Reelkeeper's category is one Reelkeeper knows about.
                </p>
              ) : (
                <ul>
                  {data.torrents.map((t) => (
                    <li
                      key={t.hash}
                      className="flex flex-wrap items-center justify-between gap-2 border-t px-5 py-3"
                    >
                      <div className="min-w-0">
                        <p className="text-sm font-medium break-all">{t.name}</p>
                        <p className="text-xs text-muted-foreground">
                          {formatBytes(t.size)} · {Math.floor(t.progress * 100)}% downloaded
                        </p>
                      </div>
                      <Button
                        variant="outline"
                        size="sm"
                        aria-label={`Remove ${t.name}`}
                        onClick={() => setPending({ kind: "torrent", hash: t.hash, name: t.name })}
                      >
                        Remove
                      </Button>
                    </li>
                  ))}
                </ul>
              )}
            </section>
            <section aria-label="Stray files">
              <h3 className="px-5 pb-2 text-sm font-medium">Stray files</h3>
              {data.folder == null ? (
                <p className="px-5 pb-4 text-sm text-muted-foreground">
                  Stray files are only reported when Reelkeeper has a download folder of its own.
                  Set one in{" "}
                  <Link to="/settings/download-client" className="text-link hover:underline">
                    Settings → Download Client
                  </Link>
                  .
                </p>
              ) : data.files.length === 0 ? (
                <p className="px-5 pb-4 text-sm text-muted-foreground">
                  No leftovers in {data.folder}.
                </p>
              ) : (
                <ul>
                  {data.files.map((f) => (
                    <li
                      key={f}
                      className="flex flex-wrap items-center justify-between gap-2 border-t px-5 py-3"
                    >
                      <p className="min-w-0 text-sm break-all">{f}</p>
                      <Button
                        variant="outline"
                        size="sm"
                        aria-label={`Delete ${f}`}
                        onClick={() => setPending({ kind: "file", name: f })}
                      >
                        Delete
                      </Button>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          </>
        )}
      </CardContent>
      <Dialog open={Boolean(pending)} onOpenChange={(open) => !open && close()}>
        <DialogContent>
          <DialogTitle>
            {pending?.kind === "file"
              ? "Delete this file or folder for good?"
              : `Remove from ${client} and delete what it downloaded?`}
          </DialogTitle>
          <DialogDescription className="break-all">{pending?.name}</DialogDescription>
          {err && <Alert variant="error">{err.message}</Alert>}
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button variant="destructive" disabled={mutation.isPending} onClick={confirm}>
              {mutation.isPending ? "Working…" : pending?.kind === "file" ? "Delete" : "Remove"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
