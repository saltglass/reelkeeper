import { Link } from "@tanstack/react-router";
import {
  ArrowRightLeft,
  ArrowUpToLine,
  Ban,
  Check,
  ChevronDown,
  ChevronRight,
  Download,
  ListOrdered,
  type LucideIcon,
  Pause,
  Play,
  Rss,
  Search,
  Trash2,
  TriangleAlert,
  X,
} from "lucide-react";
import { useState } from "react";
import { Badge } from "@/components/ui/badge";
import type { HistoryEvent, HistoryEventKind, HistoryVerdict } from "@/hooks/grab";
import { formatBytes } from "@/lib/format";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { riseStyle } from "./shared";

export const eventKinds: {
  kind: HistoryEventKind;
  label: string;
  variant: "neutral" | "info" | "success" | "warning" | "error";
}[] = [
  { kind: "searched", label: "Searched", variant: "neutral" },
  { kind: "rss", label: "RSS", variant: "neutral" },
  { kind: "would-grab", label: "Would grab", variant: "info" },
  { kind: "grabbed", label: "Grabbed", variant: "success" },
  { kind: "grab-skipped", label: "Not grabbed", variant: "warning" },
  { kind: "imported", label: "Imported", variant: "success" },
  { kind: "import-failed", label: "Import failed", variant: "error" },
  { kind: "removed", label: "Removed", variant: "warning" },
  { kind: "paused", label: "Paused", variant: "neutral" },
  { kind: "resumed", label: "Resumed", variant: "neutral" },
  { kind: "moved-to-top", label: "Moved to top", variant: "neutral" },
  { kind: "moved", label: "Moved client", variant: "success" },
  { kind: "move-failed", label: "Move failed", variant: "warning" },
  { kind: "backfill", label: "Backfill", variant: "info" },
  { kind: "from-radarr", label: "From Radarr", variant: "info" },
  { kind: "from-sonarr", label: "From Sonarr", variant: "info" },
];

/** The timeline node's icon and colour class for each kind of event. */
const nodes: Record<HistoryEventKind, { icon: LucideIcon; tone: string }> = {
  searched: { icon: Search, tone: "k-search" },
  rss: { icon: Rss, tone: "k-rss" },
  "would-grab": { icon: Download, tone: "k-search" },
  grabbed: { icon: Download, tone: "k-grab" },
  "grab-skipped": { icon: Ban, tone: "k-warn" },
  imported: { icon: Check, tone: "k-import" },
  "import-failed": { icon: TriangleAlert, tone: "k-removed" },
  removed: { icon: Trash2, tone: "k-removed" },
  paused: { icon: Pause, tone: "k-warn" },
  resumed: { icon: Play, tone: "k-search" },
  "moved-to-top": { icon: ArrowUpToLine, tone: "k-search" },
  moved: { icon: ArrowRightLeft, tone: "k-import" },
  "move-failed": { icon: TriangleAlert, tone: "k-warn" },
  backfill: { icon: ListOrdered, tone: "k-search" },
  "from-radarr": { icon: ArrowRightLeft, tone: "k-search" },
  "from-sonarr": { icon: ArrowRightLeft, tone: "k-search" },
};

/**
 * One History event: a summary line that opens to show the details.
 * "timeline" is the History page's look (a node on a line); the default is a
 * plain list row, used on a movie's page.
 */
export function HistoryRow({
  event: e,
  showTitle = true,
  variant = "list",
  index = 0,
}: {
  event: HistoryEvent;
  showTitle?: boolean;
  variant?: "list" | "timeline";
  index?: number;
}) {
  const [open, setOpen] = useState(false);
  const kind = eventKinds.find((k) => k.kind === e.event);
  const releases = e.data.releases ?? [];
  const hasDetails =
    releases.length > 0 || Boolean(e.data.chosen) || (e.data.indexerErrors ?? []).length > 0;
  const exact = new Date(e.createdAt).toLocaleString();

  const toggle = () => hasDetails && setOpen(!open);

  if (variant === "timeline") {
    const node = nodes[e.event];
    const Icon = node?.icon ?? Search;
    return (
      <li
        className="tl-row animate-rise grid grid-cols-[2rem_minmax(0,1fr)] items-start gap-3 sm:grid-cols-[2.5rem_minmax(0,1fr)] sm:gap-3.5"
        style={riseStyle(index)}
      >
        <span className={cn("tl-node size-8 sm:size-10", node?.tone)} aria-hidden="true">
          <Icon className="size-4 sm:size-[18px]" />
        </span>
        <div className="tl-body min-w-0 rounded-lg border bg-card text-card-foreground shadow-xs">
          {/* biome-ignore lint/a11y/noStaticElementInteractions: the chevron button below is the keyboard way to do the same */}
          {/* biome-ignore lint/a11y/useKeyWithClickEvents: see above */}
          <div
            onClick={toggle}
            className={cn("flex items-start gap-2 px-4 py-3", hasDetails && "cursor-pointer")}
          >
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                {showTitle && (
                  <span className="font-semibold break-words">
                    <MovieLink event={e} />
                  </span>
                )}
                {e.profile && <Badge>{e.profile}</Badge>}
                <Badge variant={kind?.variant ?? "neutral"}>{kind?.label ?? e.event}</Badge>
                <time
                  dateTime={e.createdAt}
                  title={exact}
                  className="ml-auto text-xs text-muted-foreground"
                >
                  {relativeTime(e.createdAt)}
                </time>
              </div>
              <p className="mt-1.5 text-sm break-words text-muted-foreground">{e.message}</p>
            </div>
            {hasDetails && (
              <button
                type="button"
                aria-expanded={open}
                aria-label={open ? "Hide details" : "Show details"}
                onClick={(ev) => {
                  ev.stopPropagation();
                  toggle();
                }}
                className="mt-0.5 rounded text-muted-foreground hover:text-foreground [&_svg]:size-4"
              >
                {open ? <ChevronDown /> : <ChevronRight />}
              </button>
            )}
          </div>
          {open && hasDetails && <Details event={e} />}
        </div>
      </li>
    );
  }

  return (
    <li className="border-b last:border-b-0">
      {/* biome-ignore lint/a11y/noStaticElementInteractions: the chevron button below is the keyboard way to do the same */}
      {/* biome-ignore lint/a11y/useKeyWithClickEvents: see above */}
      <div
        onClick={toggle}
        className={cn(
          "flex w-full items-start gap-2 px-4 py-3",
          hasDetails && "cursor-pointer hover:bg-muted/50",
        )}
      >
        <div className="flex min-w-0 flex-1 flex-col gap-1 sm:flex-row sm:items-start sm:gap-3">
          <time
            dateTime={e.createdAt}
            title={exact}
            className="w-28 shrink-0 text-xs text-muted-foreground sm:pt-0.5"
          >
            {relativeTime(e.createdAt)}
          </time>
          <div className="min-w-0 flex-1 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              {showTitle && (
                <span className="font-medium break-words">
                  <MovieLink event={e} />
                </span>
              )}
              {e.profile && <span className="text-xs text-muted-foreground">{e.profile}</span>}
              <Badge variant={kind?.variant ?? "neutral"}>{kind?.label ?? e.event}</Badge>
            </div>
            <p className="text-sm break-words text-muted-foreground">{e.message}</p>
          </div>
        </div>
        {hasDetails && (
          <button
            type="button"
            aria-expanded={open}
            aria-label={open ? "Hide details" : "Show details"}
            onClick={(ev) => {
              ev.stopPropagation();
              toggle();
            }}
            className="mt-0.5 rounded text-muted-foreground hover:text-foreground [&_svg]:size-4"
          >
            {open ? <ChevronDown /> : <ChevronRight />}
          </button>
        )}
      </div>
      {open && hasDetails && <Details event={e} />}
    </li>
  );
}

function Details({ event: e }: { event: HistoryEvent }) {
  const releases = e.data.releases ?? [];
  const errors = e.data.indexerErrors ?? [];
  return (
    <div className="space-y-3 px-4 pb-4">
      {e.data.chosen && (
        <>
          <p className="text-sm font-medium">Chosen release</p>
          <ReleaseTable releases={[e.data.chosen]} />
        </>
      )}
      {releases.length > 0 && (
        <>
          <ReleaseTable releases={releases} />
          {Boolean(e.data.more) && (
            <p className="text-sm text-muted-foreground">+{e.data.more} more not shown</p>
          )}
        </>
      )}
      {errors.length > 0 && (
        <div className="space-y-1">
          <p className="text-sm font-medium text-destructive">Indexers that failed</p>
          <ul className="list-disc pl-5 text-sm text-muted-foreground">
            {errors.map((m) => (
              <li key={m}>{m}</li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

function ReleaseTable({ releases }: { releases: HistoryVerdict[] }) {
  return (
    // Scrolls sideways inside its card on a phone.
    <div className="overflow-x-auto rounded-md border">
      <table className="w-full min-w-[40rem] text-left text-xs">
        <thead className="bg-muted/50 text-muted-foreground">
          <tr>
            <th className="px-2 py-1.5 font-medium">Release</th>
            <th className="px-2 py-1.5 font-medium">Indexer</th>
            <th className="px-2 py-1.5 font-medium">Size</th>
            <th className="px-2 py-1.5 font-medium">Seeders</th>
            <th className="px-2 py-1.5 font-medium">Quality</th>
            <th className="px-2 py-1.5 font-medium">Score</th>
            <th className="px-2 py-1.5 font-medium">Verdict</th>
          </tr>
        </thead>
        <tbody>
          {releases.map((r, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: the same release can appear on two indexers
            <tr key={`${r.indexer}-${r.name}-${i}`} className="border-t align-top">
              <td className="max-w-xs px-2 py-1.5 break-all">
                {r.name}
                {(r.formats ?? []).length > 0 && (
                  <div className="mt-1 flex flex-wrap gap-1">
                    {(r.formats ?? []).map((f) => (
                      <Badge key={f.name} variant="info">
                        {f.name} {f.score > 0 ? `+${f.score}` : f.score}
                      </Badge>
                    ))}
                  </div>
                )}
              </td>
              <td className="px-2 py-1.5">{r.indexer}</td>
              <td className="px-2 py-1.5 whitespace-nowrap">{formatBytes(r.size)}</td>
              <td className="px-2 py-1.5">{r.seeders}</td>
              <td className="px-2 py-1.5">{r.quality}</td>
              <td className="px-2 py-1.5">{r.score}</td>
              <td className="px-2 py-1.5">
                <span
                  className={cn(
                    "inline-flex items-center gap-1 font-medium [&_svg]:size-3.5",
                    r.accepted ? "text-success" : "text-destructive",
                  )}
                >
                  {r.accepted ? <Check aria-hidden="true" /> : <X aria-hidden="true" />}
                  {r.accepted ? (r.upgrade ? "Accepted (upgrade)" : "Accepted") : "Rejected"}
                </span>
                {(r.rejections ?? []).length > 0 && (
                  <ul className="mt-1 list-disc pl-4 text-muted-foreground">
                    {(r.rejections ?? []).map((reason) => (
                      <li key={reason}>{reason}</li>
                    ))}
                  </ul>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** The title's name and year, linking to its movie or series page. */
export function MovieLink({ event: e }: { event: HistoryEvent }) {
  if (!e.titleId || !e.title) return <span>Removed title</span>;
  const params = { id: String(e.titleId) };
  const label = `${e.title}${e.year ? ` (${e.year})` : ""}`;
  const cls = "text-link hover:underline";
  const stop = (ev: { stopPropagation: () => void }) => ev.stopPropagation();
  return e.mediaType === "series" ? (
    <Link to="/series/$id" params={params} onClick={stop} className={cls}>
      {label}
    </Link>
  ) : (
    <Link to="/movies/$id" params={params} onClick={stop} className={cls}>
      {label}
    </Link>
  );
}
