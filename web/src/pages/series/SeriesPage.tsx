import { useQuery } from "@tanstack/react-query";
import { Link, useSearch } from "@tanstack/react-router";
import { CircleAlert, LayoutGrid, List, Plus, Search, Tv } from "lucide-react";
import { useMemo, useState } from "react";
import { frameNumber, PosterCaption, PosterName, usePosterTitles } from "@/components/PosterTitle";
import { QualityLabel } from "@/components/QualityLabel";
import { SummaryLine } from "@/components/SummaryLine";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import { useInBatches } from "@/hooks/batches";
import {
  profilesQuery,
  reviewQuery,
  rootsQuery,
  type SeriesSummary,
  seriesListQuery,
} from "@/hooks/library";
import { NearItem, NearScreen } from "@/hooks/nearScreen";
import { labelSettingsQuery } from "@/hooks/settings";
import { defaultLabelParts, formatBytes, versionLabel } from "@/lib/format";
import { tmdbImage } from "@/lib/images";
import { cn } from "@/lib/utils";
import { PosterCheck, type Selection, SelectionBar, useSelection } from "../library/BulkEdit";
import { PageHeader } from "../PageHeader";
import { AddSeriesDialog } from "./AddSeriesDialog";
import { progressText, seriesProgress } from "./progress";

type Sort = "title" | "year" | "added" | "missing";
type Filter = "all" | "missing" | "attention" | "nofile" | `profile:${number}`;

const sortLabels: Record<Sort, string> = {
  title: "Title",
  year: "Year (newest first)",
  added: "Date added (newest first)",
  missing: "Most episodes missing",
};

function read<T extends string>(key: string, fallback: T): T {
  try {
    return (localStorage.getItem(key) as T | null) ?? fallback;
  } catch {
    return fallback;
  }
}
function save(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Not remembered; harmless.
  }
}

const totalSize = (s: SeriesSummary) => s.versions.reduce((n, v) => n + v.size, 0);

export function SeriesPage() {
  const series = useQuery(seriesListQuery);
  const roots = useQuery(rootsQuery);
  const profiles = useQuery(profilesQuery);
  const review = useQuery(reviewQuery);
  const [q, setQ] = useState("");
  const [sort, setSortState] = useState<Sort>(() => read("reelkeeper-series-sort", "title"));
  const linked = useSearch({ from: "/app/series" }).filter;
  const [filter, setFilterState] = useState<Filter>(
    () => linked ?? read("reelkeeper-series-filter", "all"),
  );
  const [view, setViewState] = useState<"posters" | "table">(() =>
    read("reelkeeper-series-view", "posters"),
  );
  const [adding, setAdding] = useState(false);
  const sel = useSelection();

  const setSort = (s: Sort) => {
    setSortState(s);
    save("reelkeeper-series-sort", s);
  };
  const setFilter = (f: Filter) => {
    setFilterState(f);
    save("reelkeeper-series-filter", f);
  };
  const setView = (v: "posters" | "table") => {
    setViewState(v);
    save("reelkeeper-series-view", v);
  };

  const shown = useMemo(() => {
    const list = series.data ?? [];
    const needle = q.trim().toLowerCase();
    const filtered = list.filter((s) => {
      if (needle && !`${s.title} ${s.year}`.toLowerCase().includes(needle)) return false;
      switch (filter) {
        case "all":
          return true;
        case "missing":
          return seriesProgress(s).missing > 0;
        case "attention":
          return s.problems > 0;
        case "nofile":
          return s.versions.some((v) => v.files === 0);
        default: {
          const id = Number(filter.split(":")[1]);
          return s.versions.some((v) => v.profileId === id);
        }
      }
    });
    const sorted = [...filtered];
    switch (sort) {
      case "title":
        sorted.sort((a, b) => a.sortTitle.localeCompare(b.sortTitle) || a.year - b.year);
        break;
      case "year":
        sorted.sort((a, b) => b.year - a.year || a.sortTitle.localeCompare(b.sortTitle));
        break;
      case "added":
        sorted.sort((a, b) => b.addedAt.localeCompare(a.addedAt));
        break;
      case "missing":
        sorted.sort(
          (a, b) =>
            seriesProgress(b).missing - seriesProgress(a).missing ||
            a.sortTitle.localeCompare(b.sortTitle),
        );
        break;
    }
    return sorted;
  }, [series.data, q, sort, filter]);
  const batches = useInBatches(shown, `${q}|${sort}|${filter}|${view}`);

  if (series.error)
    return <Alert variant="error">Couldn't load series: {series.error.message}</Alert>;

  const noTvRoots = roots.data && !roots.data.some((r) => r.mediaType === "series");
  // Movie folders to review are on the Movies page; here only TV ones count.
  const reviewCount = review.data?.filter((r) => r.mediaType === "series").length ?? 0;
  const all = series.data ?? [];
  const complete = all.filter((s) => {
    const p = seriesProgress(s);
    return p.total > 0 && p.missing === 0;
  }).length;
  const counts = {
    all: all.length,
    missing: all.filter((s) => seriesProgress(s).missing > 0).length,
    attention: all.filter((s) => s.problems > 0).length,
    nofile: all.filter((s) => s.versions.some((v) => v.files === 0)).length,
  };
  const chips: { value: Filter; label: string; n: number }[] = [
    { value: "all", label: "All series", n: counts.all },
    { value: "missing", label: "Missing episodes", n: counts.missing },
    { value: "attention", label: "Needs attention", n: counts.attention },
    { value: "nofile", label: "Not on disk yet", n: counts.nofile },
  ];
  const isChip = chips.some((c) => c.value === filter);
  const stats: [string, number][] = [
    ["Series", all.length],
    ["Complete", complete],
    ["Missing episodes", counts.missing],
    ["Needs attention", counts.attention],
  ];

  return (
    <div>
      <PageHeader
        title="Series"
        description="Every show Reelkeeper keeps and watches for."
        actions={
          <Button onClick={() => setAdding(true)}>
            <Plus /> Add series
          </Button>
        }
      />

      <div className="mb-6 space-y-4 empty:mb-0">
        {noTvRoots && (
          <Alert variant="info">
            <p>
              Reelkeeper doesn't know where your TV shows are yet.{" "}
              <Link to="/settings/media" className="font-medium text-link underline">
                Add a TV media folder
              </Link>{" "}
              and it will find and identify them.
            </p>
          </Alert>
        )}
        {reviewCount > 0 && (
          <Alert variant="warning">
            <p>
              {reviewCount === 1 ? "1 folder needs" : `${reviewCount} folders need`} your help:
              Reelkeeper isn't sure which series {reviewCount === 1 ? "it is" : "they are"}.{" "}
              <Link to="/review" className="font-medium text-link underline">
                Review now
              </Link>
            </p>
          </Alert>
        )}
      </div>

      <SummaryLine items={stats.map(([label, n]) => ({ label, n: series.data ? n : undefined }))} />

      <div className="mb-5 flex flex-wrap items-center gap-2.5">
        <div className="relative min-w-60 flex-[1_1_260px]">
          <Search className="pointer-events-none absolute top-3 left-3 size-4 text-muted-foreground" />
          <Input
            type="search"
            aria-label="Search series"
            placeholder="Search title or year"
            className="pl-9"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
        </div>
        <select
          aria-label="Sort by"
          className={selectClass}
          value={sort}
          onChange={(e) => setSort(e.target.value as Sort)}
        >
          {Object.entries(sortLabels).map(([k, label]) => (
            <option key={k} value={k}>
              {label}
            </option>
          ))}
        </select>
        <div className="flex gap-1.5">
          <Button
            variant="tile"
            size="icon"
            aria-label="Poster view"
            aria-pressed={view === "posters"}
            onClick={() => setView("posters")}
          >
            <LayoutGrid />
          </Button>
          <Button
            variant="tile"
            size="icon"
            aria-label="Table view"
            aria-pressed={view === "table"}
            onClick={() => setView("table")}
          >
            <List />
          </Button>
        </div>
        <Button
          variant="outline"
          aria-pressed={sel.selecting}
          onClick={sel.selecting ? sel.stop : sel.start}
        >
          {sel.selecting ? "Done" : "Select"}
        </Button>
      </div>
      <div className="mb-6 flex flex-wrap items-center gap-2">
        {/* biome-ignore lint/a11y/useSemanticElements: a fieldset would add a border and legend */}
        <div role="group" aria-label="Filter series" className="flex flex-wrap gap-2">
          {chips.map((c) => (
            <button
              key={c.value}
              type="button"
              aria-pressed={filter === c.value}
              onClick={() => setFilter(c.value)}
              className={cn(
                "inline-flex min-h-9 cursor-pointer items-center gap-1.5 border px-3.5 text-sm font-medium transition-[background-color,border-color,transform] duration-[calc(200ms*var(--m))] [transition-timing-function:cubic-bezier(.34,1.56,.64,1)] hover:-translate-y-px active:scale-95",
                filter === c.value
                  ? "border-primary/55 bg-accent text-accent-foreground"
                  : "bg-card hover:border-input",
              )}
            >
              {c.label}
              <span
                className={cn(
                  "text-xs tabular-nums",
                  filter === c.value ? "text-accent-foreground" : "text-muted-foreground",
                )}
              >
                {c.n}
              </span>
            </button>
          ))}
        </div>
        <select
          aria-label="More filters"
          className={selectClass}
          value={isChip ? "" : filter}
          onChange={(e) => setFilter((e.target.value || "all") as Filter)}
        >
          <option value="">More filters…</option>
          {profiles.data
            ?.filter((p) => p.appliesTo !== "movie")
            .map((p) => (
              <option key={p.id} value={`profile:${p.id}`}>
                {p.name} versions
              </option>
            ))}
        </select>
      </div>

      {series.isLoading ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : shown.length === 0 ? (
        <div className="flex flex-col items-center gap-3 px-6 py-14 text-center text-muted-foreground">
          <span className="relative grid size-28 place-items-center bg-accent text-accent-foreground motion-safe:animate-[float_calc(4s*var(--m))_ease-in-out_infinite]">
            <span
              aria-hidden="true"
              className="absolute -inset-2.5 border border-dashed border-primary/50"
            />
            <Tv className="size-10" />
          </span>
          <p>{(series.data?.length ?? 0) === 0 ? "No series yet." : "No series match."}</p>
        </div>
      ) : view === "posters" ? (
        <PosterGrid series={batches.visible} sel={sel} />
      ) : (
        <SeriesTable series={batches.visible} sel={sel} />
      )}
      {batches.more}
      <SelectionBar kind="series" sel={sel} shown={shown} />
      <p className="mt-6 text-sm text-muted-foreground">
        {shown.length} of {series.data?.length ?? 0} series
      </p>

      <AddSeriesDialog open={adding} onOpenChange={setAdding} />
    </div>
  );
}

/** Quality labels per version, chosen in Settings → General → Labels, as on Movies. */
function VersionBadges({ s }: { s: SeriesSummary }) {
  // Until the setting loads nothing is shown (rather than briefly the wrong
  // thing); if it can't load, the defaults are used.
  const labels = useQuery(labelSettingsQuery);
  const parts = labels.data?.parts ?? (labels.isError ? defaultLabelParts : undefined);
  if (!parts) return null;
  return (
    <div className="flex flex-wrap gap-x-3 gap-y-1">
      {s.versions.map((v) => {
        const pieces =
          v.files === 0
            ? [parts.includes("profile") ? v.profileName : "", "not on disk"].filter(Boolean)
            : versionLabel(v, parts);
        if (pieces.length === 0 && v.missing === 0) return null;
        return (
          <QualityLabel
            key={v.id}
            variant={v.missing > 0 ? "warning" : v.files === 0 ? "neutral" : "info"}
            title={v.missing > 0 ? `${v.missing} file(s) missing` : undefined}
          >
            {v.missing > 0 && <CircleAlert className="size-3" />}
            {pieces.length > 0 ? (
              pieces.join(" · ")
            ) : (
              <span className="sr-only">Files missing</span>
            )}
          </QualityLabel>
        );
      })}
    </div>
  );
}

function Attention({ s }: { s: SeriesSummary }) {
  if (s.problems === 0) return null;
  return (
    <Badge
      variant="warning"
      title="Reelkeeper isn't sure which episode some files are. Open the series to see which."
    >
      <CircleAlert className="size-3" /> Needs attention
    </Badge>
  );
}

function EpisodeProgress({ s }: { s: SeriesSummary }) {
  const p = seriesProgress(s);
  return (
    <div className="space-y-1">
      <Progress value={p.percent} label={`${s.title} episodes on disk`} />
      <p className="text-xs text-muted-foreground">{progressText(p.have, p.total)}</p>
    </div>
  );
}

/** A poster frame. Keyboard focus and selection draw the grease pencil. */
const posterTile = "grease group relative block aspect-[2/3] bg-muted text-white";

const selectClass =
  "min-h-10 rounded-lg border border-input bg-card px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none";

type Status = { label: string; tone: "ok" | "warn" | "idle" };

/** The one-word state shown on a poster. */
function statusOf(s: SeriesSummary): Status {
  const p = seriesProgress(s);
  if (s.problems > 0) return { label: "Needs attention", tone: "warn" };
  if (p.have === 0) return { label: "Not on disk", tone: "idle" };
  if (p.total > 0 && p.missing === 0) return { label: "Complete", tone: "ok" };
  return { label: `${p.missing} missing`, tone: "warn" };
}

const toneColor: Record<Status["tone"], string> = {
  ok: "text-edge",
  warn: "text-warning",
  idle: "text-muted-foreground",
};

function PosterGrid({ series, sel }: { series: SeriesSummary[]; sel: Selection }) {
  const titles = usePosterTitles();
  return (
    <NearScreen>
      <ul className="grid grid-cols-[repeat(auto-fill,minmax(140px,1fr))] gap-y-5 sm:grid-cols-[repeat(auto-fill,minmax(166px,1fr))]">
        {series.map((s, i) => {
          const status = statusOf(s);
          const p = seriesProgress(s);
          // Nothing on disk yet: not exposed, so the artwork is greyed out.
          const unexposed = status.tone === "idle";
          return (
            <NearItem key={s.id} className="poster-item film-cell flex flex-col gap-2">
              {(near) => {
                const body = (
                  <>
                    {s.posterPath
                      ? near && (
                          <img
                            src={tmdbImage(s.posterPath, "w342")}
                            alt=""
                            className="absolute inset-0 size-full object-cover"
                          />
                        )
                      : null}
                    <PosterName title={s.title} year={s.year} onTile={!titles && !s.posterPath} />
                  </>
                );
                return (
                  <>
                    {sel.selecting ? (
                      <button
                        type="button"
                        aria-pressed={sel.selected.has(s.id)}
                        aria-label={`${s.title} (${s.year})`}
                        onClick={() => sel.toggle(s.id)}
                        data-selected={sel.selected.has(s.id)}
                        className={cn(
                          posterTile,
                          "w-full cursor-pointer text-left",
                          unexposed && "unexposed",
                        )}
                      >
                        {body}
                        <PosterCheck checked={sel.selected.has(s.id)} />
                      </button>
                    ) : (
                      <Link
                        to="/series/$id"
                        params={{ id: String(s.id) }}
                        className={cn(posterTile, unexposed && "unexposed")}
                      >
                        {body}
                      </Link>
                    )}
                    <p className={cn("edge-print truncate", toneColor[status.tone])}>
                      <span className="text-edge">{frameNumber(i)} ▸</span> {s.year || "—"} ·{" "}
                      {status.label}
                    </p>
                    <div className="space-y-1.5">
                      <div className="space-y-1">
                        <Progress value={p.percent} label={`${s.title} episodes on disk`} />
                        <p className="text-xs text-muted-foreground">
                          {progressText(p.have, p.total)}
                        </p>
                      </div>
                      {titles && (
                        <PosterCaption to="/series/$id" id={s.id} title={s.title} year={s.year} />
                      )}
                      <Attention s={s} />
                      <VersionBadges s={s} />
                    </div>
                  </>
                );
              }}
            </NearItem>
          );
        })}
      </ul>
    </NearScreen>
  );
}

function SeriesTable({ series, sel }: { series: SeriesSummary[]; sel: Selection }) {
  return (
    // The table sits on a strip of film; each row starts with its frame number.
    <div className="film overflow-x-auto">
      <table className="w-full text-sm">
        <thead className="label-caps border-b text-left">
          <tr>
            {sel.selecting && (
              <th className="w-10 px-3 py-2">
                <span className="sr-only">Selected</span>
              </th>
            )}
            <th className="w-12 px-3 py-2 font-normal">
              <span className="sr-only">Frame</span>
            </th>
            <th className="px-3 py-2 font-normal">Title</th>
            <th className="px-3 py-2 font-normal">Year</th>
            <th className="px-3 py-2 font-normal">Episodes</th>
            <th className="px-3 py-2 font-normal">Versions</th>
            <th className="hidden px-3 py-2 text-right font-normal sm:table-cell">Size</th>
          </tr>
        </thead>
        <tbody className="divide-y">
          {series.map((s, i) => (
            <tr key={s.id} className="hover:bg-muted/50">
              {sel.selecting && (
                <td className="px-3 py-3">
                  <input
                    type="checkbox"
                    className="size-4"
                    aria-label={`${s.title} (${s.year})`}
                    checked={sel.selected.has(s.id)}
                    onChange={() => sel.toggle(s.id)}
                  />
                </td>
              )}
              <td className="edge-print px-3 py-3 align-top">{frameNumber(i)}</td>
              {/* A selected row gets the grease-pencil loop around its title. */}
              <td
                className="grease grease-row px-3 py-3"
                data-selected={sel.selecting && sel.selected.has(s.id)}
              >
                <Link
                  to="/series/$id"
                  params={{ id: String(s.id) }}
                  className="font-medium hover:underline"
                >
                  {s.title}
                </Link>
                <div className="mt-1">
                  <Attention s={s} />
                </div>
              </td>
              <td className="px-3 py-3 font-mono text-[13px] text-muted-foreground">
                {s.year || "—"}
              </td>
              <td className="min-w-32 px-3 py-3">
                <EpisodeProgress s={s} />
              </td>
              <td className="px-3 py-3">
                <VersionBadges s={s} />
              </td>
              <td className="hidden px-3 py-3 text-right font-mono text-[13px] whitespace-nowrap text-muted-foreground sm:table-cell">
                {totalSize(s) > 0 ? formatBytes(totalSize(s)) : "—"}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
