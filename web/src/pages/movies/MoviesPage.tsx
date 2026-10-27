import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { CircleAlert, Film, LayoutGrid, List, Plus, Search } from "lucide-react";
import { useMemo, useState } from "react";
import { PosterCaption, PosterName, usePosterTitles } from "@/components/PosterTitle";
import { QualityLabel } from "@/components/QualityLabel";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { useInBatches } from "@/hooks/batches";
import { queueQuery } from "@/hooks/downloads";
import {
  type MovieSummary,
  moviesQuery,
  profilesQuery,
  reviewQuery,
  rootsQuery,
} from "@/hooks/library";
import { NearItem, NearScreen } from "@/hooks/nearScreen";
import { labelSettingsQuery } from "@/hooks/settings";
import { defaultLabelParts, formatBytes, versionLabel } from "@/lib/format";
import { tmdbImage } from "@/lib/images";
import { cn } from "@/lib/utils";
import { PosterCheck, type Selection, SelectionBar, useSelection } from "../library/BulkEdit";
import { PageHeader } from "../PageHeader";
import { AddMovieDialog } from "./AddMovieDialog";

type Sort = "title" | "year" | "added" | "size";
type Filter =
  | "all"
  | "downloading"
  | "missing"
  | "nofile"
  | "monitored"
  | "dupes"
  | `profile:${number}`;

const sortLabels: Record<Sort, string> = {
  title: "Title",
  year: "Year (newest first)",
  added: "Date added (newest first)",
  size: "Size (largest first)",
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

const totalSize = (m: MovieSummary) => m.versions.reduce((n, v) => n + v.size, 0);

export function MoviesPage() {
  const movies = useQuery(moviesQuery);
  const roots = useQuery(rootsQuery);
  const profiles = useQuery(profilesQuery);
  const review = useQuery(reviewQuery);
  const queue = useQuery(queueQuery);
  const [q, setQ] = useState("");
  const [sort, setSortState] = useState<Sort>(() => read("reelkeeper-movies-sort", "title"));
  const [filter, setFilterState] = useState<Filter>(() => read("reelkeeper-movies-filter", "all"));
  const [view, setViewState] = useState<"posters" | "table">(() =>
    read("reelkeeper-movies-view", "posters"),
  );
  const [adding, setAdding] = useState(false);
  const sel = useSelection();
  const { deleted, kept } = useSearch({ from: "/app/movies" });
  const navigate = useNavigate();

  const setSort = (s: Sort) => {
    setSortState(s);
    save("reelkeeper-movies-sort", s);
  };
  const setFilter = (f: Filter) => {
    setFilterState(f);
    save("reelkeeper-movies-filter", f);
  };
  const setView = (v: "posters" | "table") => {
    setViewState(v);
    save("reelkeeper-movies-view", v);
  };

  // Movie id -> download progress (0 to 1) for movies the queue says are downloading.
  const downloading = useMemo(() => {
    const map = new Map<number, number>();
    for (const item of queue.data?.items ?? []) {
      if (item.status !== "downloading" || !item.movie) continue;
      const p = item.torrent?.progress ?? 0;
      map.set(item.movie.id, Math.max(map.get(item.movie.id) ?? 0, p));
    }
    return map;
  }, [queue.data]);

  const shown = useMemo(() => {
    const list = movies.data ?? [];
    const needle = q.trim().toLowerCase();
    const filtered = list.filter((m) => {
      if (needle && !`${m.title} ${m.year}`.toLowerCase().includes(needle)) return false;
      switch (filter) {
        case "all":
          return true;
        case "missing":
          return m.versions.some((v) => v.missing > 0);
        case "nofile":
          return m.versions.some((v) => v.files === 0);
        case "downloading":
          return downloading.has(m.id);
        case "monitored":
          return m.versions.some((v) => v.monitored);
        case "dupes":
          return m.versions.some((v) => v.files > 1);
        default: {
          const id = Number(filter.split(":")[1]);
          return m.versions.some((v) => v.profileId === id);
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
      case "size":
        sorted.sort((a, b) => totalSize(b) - totalSize(a));
        break;
    }
    return sorted;
  }, [movies.data, q, sort, filter, downloading]);
  const batches = useInBatches(shown, `${q}|${sort}|${filter}|${view}`);

  if (movies.error)
    return <Alert variant="error">Couldn't load movies: {movies.error.message}</Alert>;

  const noRoots = roots.data && roots.data.length === 0;
  // Series folders to review are counted on the Series page. Items without a type are movies.
  const reviewCount = review.data?.filter((r) => r.mediaType !== "series").length ?? 0;
  const all = movies.data ?? [];
  const onDisk = all.filter((m) => m.versions.some((v) => v.files > 0)).length;
  const wanted = all.filter((m) => m.versions.some((v) => v.monitored && v.files === 0)).length;
  const counts = {
    all: all.length,
    downloading: all.filter((m) => downloading.has(m.id)).length,
    monitored: all.filter((m) => m.versions.some((v) => v.monitored)).length,
    missing: all.filter((m) => m.versions.some((v) => v.missing > 0)).length,
    nofile: all.filter((m) => m.versions.some((v) => v.files === 0)).length,
  };
  const chips: { value: Filter; label: string; n: number }[] = [
    { value: "all", label: "All", n: counts.all },
    { value: "downloading", label: "Downloading", n: counts.downloading },
    { value: "monitored", label: "Monitored", n: counts.monitored },
    { value: "missing", label: "Files missing", n: counts.missing },
    { value: "nofile", label: "Not on disk yet", n: counts.nofile },
  ];
  const isChip = chips.some((c) => c.value === filter);
  const stats: [string, number][] = [
    ["Movies", all.length],
    ["On disk", onDisk],
    ["Downloading", counts.downloading],
    ["Wanted", wanted],
  ];

  return (
    <div>
      <PageHeader
        title="Movies"
        description="Everything Reelkeeper keeps and watches for."
        actions={
          <Button onClick={() => setAdding(true)}>
            <Plus /> Add movie
          </Button>
        }
      />

      <div className="space-y-4">
        {deleted && (
          <Alert variant="success" role="status">
            <p className="flex-1">
              Deleted {deleted}.{" "}
              {kept
                ? "Its files were left where they are, and scans will skip its folder. You can undo that under Settings → Media Folders."
                : "Its files were deleted too."}
            </p>
            <button
              type="button"
              className="underline"
              onClick={() => navigate({ to: "/movies", search: {}, replace: true })}
            >
              Dismiss
            </button>
          </Alert>
        )}
        {noRoots && (
          <Alert variant="info">
            <p>
              Reelkeeper doesn't know where your movies are yet.{" "}
              <Link to="/settings/media" className="font-medium text-link underline">
                Add a media folder
              </Link>{" "}
              and it will find and identify them.
            </p>
          </Alert>
        )}
        {reviewCount > 0 && (
          <Alert variant="warning">
            <p>
              {reviewCount === 1 ? "1 folder needs" : `${reviewCount} folders need`} your help:
              Reelkeeper isn't sure which movie {reviewCount === 1 ? "it is" : "they are"}.{" "}
              <Link to="/review" className="font-medium text-link underline">
                Review now
              </Link>
            </p>
          </Alert>
        )}
      </div>

      <dl className="mb-6 grid grid-cols-2 gap-3 sm:grid-cols-4">
        {stats.map(([label, n], i) => (
          <Card
            key={label}
            className="animate-rise px-4 py-3.5"
            style={{ animationDelay: `calc(${40 + i * 50}ms * var(--m))` }}
          >
            <dt className="text-xs font-medium text-muted-foreground">{label}</dt>
            <dd className="mt-1 text-[22px]/7 font-semibold tracking-tight tabular-nums">
              {movies.data ? n : "—"}
            </dd>
          </Card>
        ))}
      </dl>

      <div className="mb-5 flex flex-wrap items-center gap-2.5">
        <div className="relative min-w-60 flex-[1_1_260px]">
          <Search className="pointer-events-none absolute top-3 left-3 size-4 text-muted-foreground" />
          <Input
            type="search"
            aria-label="Search movies"
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
        <div role="group" aria-label="Filter movies" className="flex flex-wrap gap-2">
          {chips.map((c) => (
            <button
              key={c.value}
              type="button"
              aria-pressed={filter === c.value}
              onClick={() => setFilter(c.value)}
              className={cn(
                "inline-flex min-h-9 cursor-pointer items-center gap-1.5 rounded-full border px-3.5 text-sm font-medium transition-[background-color,border-color,transform] duration-[calc(200ms*var(--m))] [transition-timing-function:cubic-bezier(.34,1.56,.64,1)] hover:-translate-y-px active:scale-95",
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
          <option value="dupes">More than one copy</option>
          {profiles.data
            ?.filter((p) => p.appliesTo !== "series")
            .map((p) => (
              <option key={p.id} value={`profile:${p.id}`}>
                {p.name} versions
              </option>
            ))}
        </select>
      </div>

      {movies.isLoading ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : shown.length === 0 ? (
        <div className="flex flex-col items-center gap-2 px-6 py-14 text-center text-muted-foreground">
          <Film className="size-10" />
          <p>{all.length === 0 ? "No movies yet." : "No movies match."}</p>
        </div>
      ) : view === "posters" ? (
        <PosterGrid movies={batches.visible} downloading={downloading} sel={sel} />
      ) : (
        <MovieTable movies={batches.visible} downloading={downloading} sel={sel} />
      )}
      {batches.more}
      <SelectionBar kind="movie" sel={sel} shown={shown} />
      <p className="mt-6 text-sm text-muted-foreground">
        {shown.length} of {all.length} movies
      </p>

      <AddMovieDialog open={adding} onOpenChange={setAdding} />
    </div>
  );
}

const posterTile =
  "poster-tile group relative block aspect-[2/3] overflow-hidden rounded-[10px] bg-card text-white shadow-md focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring";

const selectClass =
  "min-h-10 rounded-lg border border-input bg-card px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none";

function VersionBadges({ m }: { m: MovieSummary }) {
  // What labels show is chosen in Settings → General. Until that loads,
  // nothing is shown (rather than briefly the wrong thing); if it can't
  // load, the defaults are used.
  const labels = useQuery(labelSettingsQuery);
  const parts = labels.data?.parts ?? (labels.isError ? defaultLabelParts : undefined);
  if (!parts) return null;
  return (
    <div className="flex flex-wrap gap-1">
      {m.versions.map((v) => {
        const pieces =
          v.files === 0
            ? [parts.includes("profile") ? v.profileName : "", "not on disk"].filter(Boolean)
            : versionLabel(v, parts);
        // Nothing chosen and nothing wrong: no label at all.
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

type Status = { label: string; tone: "ok" | "warn" | "live" | "idle" };

/** The one-word state shown on a poster. */
function statusOf(m: MovieSummary, progress: number | undefined): Status {
  if (progress !== undefined)
    return { label: `Downloading ${Math.round(progress * 100)}%`, tone: "live" };
  if (m.versions.some((v) => v.missing > 0)) return { label: "Files missing", tone: "warn" };
  if (m.versions.length > 0 && m.versions.every((v) => v.files > 0))
    return { label: "On disk", tone: "ok" };
  if (m.versions.some((v) => v.files > 0)) return { label: "Partly on disk", tone: "ok" };
  return m.versions.some((v) => v.monitored)
    ? { label: "Wanted", tone: "idle" }
    : { label: "Not on disk", tone: "idle" };
}

const toneColor: Record<Status["tone"], string> = {
  ok: "text-[#4ade80]",
  warn: "text-[#fbbf24]",
  live: "text-[#22d3ee]",
  idle: "text-white/70",
};

function PosterGrid({
  movies,
  downloading,
  sel,
}: {
  movies: MovieSummary[];
  downloading: Map<number, number>;
  sel: Selection;
}) {
  const titles = usePosterTitles();
  return (
    <NearScreen>
      <ul className="grid grid-cols-[repeat(auto-fill,minmax(130px,1fr))] gap-x-5 gap-y-6 sm:grid-cols-[repeat(auto-fill,minmax(156px,1fr))]">
        {movies.map((m, i) => {
          const progress = downloading.get(m.id);
          const status = statusOf(m, progress);
          return (
            <NearItem
              key={m.id}
              className="poster-item animate-rise space-y-2"
              style={{ animationDelay: `calc(min(${i}, 14) * 45ms * var(--m))` }}
            >
              {(near) => {
                const body = (
                  <>
                    {m.posterPath ? (
                      near && (
                        <img
                          src={tmdbImage(m.posterPath, "w342")}
                          alt=""
                          className="absolute inset-0 size-full object-cover transition-transform duration-[calc(900ms*var(--m))] group-hover:scale-[1.08]"
                        />
                      )
                    ) : (
                      <span
                        aria-hidden="true"
                        className="absolute inset-0 bg-gradient-to-br from-primary/60 to-[#22d3ee]/40"
                      />
                    )}
                    <span
                      aria-hidden="true"
                      className={cn(
                        "absolute inset-0 bg-gradient-to-t",
                        m.posterPath
                          ? "from-black/30 via-transparent to-black/35"
                          : "from-black/85 via-black/10 to-black/30",
                      )}
                    />
                    <PosterName title={m.title} year={m.year} onTile={!titles && !m.posterPath} />
                    <span className="absolute top-2.5 left-2.5 inline-flex items-center gap-1.5 rounded-full bg-[rgba(7,6,11,0.75)] px-2.5 py-[3px] text-xs/4 font-medium">
                      <span
                        className={cn(toneColor[status.tone], status.tone === "live" && "ping-dot")}
                      >
                        {status.tone !== "live" && (
                          <span className="block size-[7px] rounded-full bg-current" />
                        )}
                      </span>
                      {status.label}
                    </span>
                    {progress !== undefined && (
                      <span
                        role="progressbar"
                        aria-label={`${m.title} download`}
                        aria-valuemin={0}
                        aria-valuemax={100}
                        aria-valuenow={Math.round(progress * 100)}
                        className="absolute inset-x-0 bottom-0 h-1 bg-white/20"
                      >
                        <i
                          className="block h-full bg-white shadow-[0_0_12px_rgba(255,255,255,0.8)] transition-[width] duration-[calc(900ms*var(--m))] ease-linear"
                          style={{ width: `${Math.round(progress * 100)}%` }}
                        />
                      </span>
                    )}
                  </>
                );
                return (
                  <>
                    {sel.selecting ? (
                      <button
                        type="button"
                        aria-pressed={sel.selected.has(m.id)}
                        aria-label={`${m.title} (${m.year})`}
                        onClick={() => sel.toggle(m.id)}
                        className={cn(
                          posterTile,
                          "w-full cursor-pointer text-left",
                          sel.selected.has(m.id) && "outline-3 outline-offset-2 outline-primary",
                        )}
                      >
                        {body}
                        <PosterCheck checked={sel.selected.has(m.id)} />
                      </button>
                    ) : (
                      <Link to="/movies/$id" params={{ id: String(m.id) }} className={posterTile}>
                        {body}
                      </Link>
                    )}
                    {titles && (
                      <PosterCaption to="/movies/$id" id={m.id} title={m.title} year={m.year} />
                    )}
                    <VersionBadges m={m} />
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

function MovieTable({
  movies,
  downloading,
  sel,
}: {
  movies: MovieSummary[];
  downloading: Map<number, number>;
  sel: Selection;
}) {
  return (
    <div className="animate-rise overflow-x-auto rounded-lg border bg-card">
      <table className="w-full text-sm">
        <thead className="border-b text-left text-xs text-muted-foreground">
          <tr>
            {sel.selecting && (
              <th className="w-10 px-3 py-2">
                <span className="sr-only">Selected</span>
              </th>
            )}
            <th className="px-3 py-2 font-medium">Title</th>
            <th className="px-3 py-2 font-medium">Year</th>
            <th className="px-3 py-2 font-medium">Versions</th>
            <th className="hidden px-3 py-2 text-right font-medium sm:table-cell">Size</th>
          </tr>
        </thead>
        <tbody className="divide-y">
          {movies.map((m) => (
            <tr key={m.id} className="hover:bg-muted/50">
              {sel.selecting && (
                <td className="px-3 py-2">
                  <input
                    type="checkbox"
                    className="size-4"
                    aria-label={`${m.title} (${m.year})`}
                    checked={sel.selected.has(m.id)}
                    onChange={() => sel.toggle(m.id)}
                  />
                </td>
              )}
              <td className="px-3 py-2">
                <Link
                  to="/movies/$id"
                  params={{ id: String(m.id) }}
                  className="font-medium hover:underline"
                >
                  {m.title}
                </Link>
                {downloading.has(m.id) && (
                  <Badge variant="info" className="ml-2">
                    Downloading {Math.round((downloading.get(m.id) ?? 0) * 100)}%
                  </Badge>
                )}
              </td>
              <td className="px-3 py-2 text-muted-foreground">{m.year || "—"}</td>
              <td className="px-3 py-2">
                <VersionBadges m={m} />
              </td>
              <td className="hidden px-3 py-2 text-right whitespace-nowrap text-muted-foreground sm:table-cell">
                {totalSize(m) > 0 ? formatBytes(totalSize(m)) : "—"}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
