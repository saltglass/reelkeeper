import { useQuery } from "@tanstack/react-query";
import { ChevronRight, Search } from "lucide-react";
import { useMemo, useState } from "react";
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
import { Input } from "@/components/ui/input";
import { Segmented } from "@/components/ui/segmented";
import { backfillQuery } from "@/hooks/backfill";
import { toast } from "@/hooks/toast";
import {
  missingTotal,
  SEARCH_LIMIT,
  useSearchWanted,
  useUnmonitorWanted,
  type WantedItem,
  wantedQuery,
} from "@/hooks/wanted";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { MovieName, PosterThumb, riseStyle } from "@/pages/activity/shared";
import { PageHeader } from "@/pages/PageHeader";
import { AddToBackfillButton } from "@/pages/series/AddToBackfillButton";
import { BackfillTab } from "./BackfillTab";

type Tab = "missing" | "upgrades" | "backfill";
type Kind = "all" | "movie" | "tv";
type Sort = "date" | "title" | "searched";

const sortLabels: Record<Sort, string> = {
  date: "Date (newest first)",
  title: "Title",
  searched: "Last searched (longest ago first)",
};

const selectClass =
  "min-h-10 rounded-lg border border-input bg-card px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none";

// The tab and filters are remembered in this browser, like the other lists.
function read<T extends string>(key: string, allowed: readonly T[], fallback: T): T {
  try {
    const v = localStorage.getItem(key);
    return allowed.includes(v as T) ? (v as T) : fallback;
  } catch {
    return fallback;
  }
}
function readText(key: string): string {
  try {
    return localStorage.getItem(key) ?? "";
  } catch {
    return "";
  }
}
function save(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Not remembered; harmless.
  }
}

/** One row's identity: a movie version, or a show version's season. */
const rowKey = (it: WantedItem) =>
  it.kind === "movie" ? `m:${it.versionId}` : `e:${it.versionId}:${it.season ?? 0}`;

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

export function WantedPage() {
  const { data, error } = useQuery(wantedQuery);
  // Only for the tab's count; the tab loads its own list.
  const { data: backfill } = useQuery(backfillQuery);
  const [tab, setTabState] = useState<Tab>(() =>
    read("reelkeeper-wanted-tab", ["missing", "upgrades", "backfill"], "missing"),
  );
  const [kind, setKindState] = useState<Kind>(() =>
    read("reelkeeper-wanted-kind", ["all", "movie", "tv"], "all"),
  );
  const [sort, setSortState] = useState<Sort>(() =>
    read("reelkeeper-wanted-sort", ["date", "title", "searched"], "date"),
  );
  const [profile, setProfileState] = useState(() => readText("reelkeeper-wanted-profile"));
  const [q, setQ] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [confirming, setConfirming] = useState(false);
  const search = useSearchWanted();

  const setTab = (t: Tab) => {
    setTabState(t);
    save("reelkeeper-wanted-tab", t);
    setSelected(new Set());
  };
  const setKind = (k: Kind) => {
    setKindState(k);
    save("reelkeeper-wanted-kind", k);
  };
  const setSort = (s: Sort) => {
    setSortState(s);
    save("reelkeeper-wanted-sort", s);
  };
  const setProfile = (p: string) => {
    setProfileState(p);
    save("reelkeeper-wanted-profile", p);
  };

  const list = tab === "upgrades" ? (data?.upgrades ?? []) : (data?.missing ?? []);
  const profiles = useMemo(() => [...new Set(list.map((i) => i.profile))].sort(), [list]);
  // A remembered profile that no longer has anything wanted filters nothing.
  const activeProfile = profiles.includes(profile) ? profile : "";

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const out = list.filter((it) => {
      if (tab === "missing" && kind !== "all" && (kind === "movie") !== (it.kind === "movie"))
        return false;
      if (activeProfile && it.profile !== activeProfile) return false;
      return !needle || `${it.title} ${it.year}`.toLowerCase().includes(needle);
    });
    const byTitle = (a: WantedItem, b: WantedItem) =>
      a.title.localeCompare(b.title) || (b.season ?? 0) - (a.season ?? 0);
    switch (sort) {
      case "title":
        out.sort(byTitle);
        break;
      case "searched":
        // Never searched first, then the longest ago.
        out.sort(
          (a, b) => (a.lastSearchedAt ?? "").localeCompare(b.lastSearchedAt ?? "") || byTitle(a, b),
        );
        break;
      default:
        out.sort((a, b) => b.date.localeCompare(a.date) || byTitle(a, b));
    }
    return out;
  }, [list, tab, kind, activeProfile, q, sort]);

  if (error) return <Alert variant="error">Couldn't load Wanted: {error.message}</Alert>;

  // Actions apply to the rows you can see and have ticked.
  const chosen = shown.filter((it) => selected.has(rowKey(it)));
  const allChosen = shown.length > 0 && chosen.length === shown.length;
  const counts = data?.counts;

  function toggle(it: WantedItem) {
    const key = rowKey(it);
    setSelected((cur) => {
      const next = new Set(cur);
      if (!next.delete(key)) next.add(key);
      return next;
    });
  }

  async function searchFor(items: WantedItem[]) {
    // The first SEARCH_LIMIT, in the order shown; the rest wait for the hourly search.
    const ids = [...new Set(items.map((i) => i.versionId))];
    const left = Math.max(0, ids.length - SEARCH_LIMIT);
    const later = left > 0 ? ` The other ${left} are left for the hourly search.` : "";
    try {
      const r = await search.mutateAsync(ids.slice(0, SEARCH_LIMIT));
      if (r.queued === 0) {
        toast(`Those searches were already waiting.${later}`, "ok");
      } else {
        toast(
          `${plural(r.queued, "search", "searches")} queued. Results appear in each row as they finish.${later}`,
          "ok",
        );
      }
      setSelected(new Set());
    } catch (e) {
      toast(e instanceof RequestError ? e.message : "Couldn't start the searches.", "problem");
    }
  }

  return (
    <div>
      <PageHeader
        title="Wanted"
        description="What Reelkeeper is still looking for, and why it hasn't found it yet. Movies and episodes that aren't out yet don't appear. Movies with upgrades turned off aren't listed as upgrades."
      />

      <div className="mb-5 flex flex-wrap items-center gap-2.5">
        <Segmented<Tab>
          label="Wanted list"
          value={tab}
          onChange={setTab}
          options={[
            { value: "missing", label: `Missing${counts ? ` (${missingTotal(counts)})` : ""}` },
            {
              value: "upgrades",
              label: `Can be upgraded${counts ? ` (${counts.upgrades})` : ""}`,
            },
            {
              value: "backfill",
              label: `Backfill${backfill ? ` (${(backfill.now ? 1 : 0) + backfill.next.length})` : ""}`,
            },
          ]}
        />
      </div>

      {tab === "backfill" ? (
        <BackfillTab />
      ) : (
        <>
          <div className="mb-5 flex flex-wrap items-center gap-2.5">
            <div className="relative min-w-60 flex-[1_1_260px]">
              <Search className="pointer-events-none absolute top-3 left-3 size-4 text-muted-foreground" />
              <Input
                type="search"
                aria-label="Search wanted"
                placeholder="Search title or year"
                className="pl-9"
                value={q}
                onChange={(e) => setQ(e.target.value)}
              />
            </div>
            {tab === "missing" && (
              <select
                aria-label="Show"
                className={selectClass}
                value={kind}
                onChange={(e) => setKind(e.target.value as Kind)}
              >
                <option value="all">Movies and TV</option>
                <option value="movie">Movies</option>
                <option value="tv">TV</option>
              </select>
            )}
            <select
              aria-label="Profile"
              className={selectClass}
              value={activeProfile}
              onChange={(e) => setProfile(e.target.value)}
            >
              <option value="">All profiles</option>
              {profiles.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
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
          </div>

          {!data ? (
            <p className="text-sm text-muted-foreground">Loading…</p>
          ) : shown.length === 0 ? (
            <Card className="p-6 text-sm text-muted-foreground">
              {list.length > 0
                ? "Nothing matches those filters."
                : tab === "missing"
                  ? "Nothing missing. Everything Reelkeeper monitors that's out has a file."
                  : "No movies below their profile's target quality."}
            </Card>
          ) : (
            <Card className="overflow-hidden">
              <div className="flex items-center gap-3 border-b px-4 py-2.5 text-sm sm:px-5">
                <input
                  type="checkbox"
                  id="wanted-all"
                  className="size-4"
                  checked={allChosen}
                  onChange={() =>
                    setSelected(allChosen ? new Set() : new Set(shown.map((it) => rowKey(it))))
                  }
                />
                <label htmlFor="wanted-all" className="text-muted-foreground">
                  Select all {shown.length} shown
                </label>
              </div>
              <ul>
                {shown.map((it, i) => (
                  <WantedRow
                    key={rowKey(it)}
                    item={it}
                    index={i}
                    checked={selected.has(rowKey(it))}
                    onToggle={() => toggle(it)}
                    onSearch={() => searchFor([it])}
                    searching={search.isPending}
                  />
                ))}
              </ul>
            </Card>
          )}

          {chosen.length > 0 && (
            <Card
              role="region"
              aria-label="Actions for the selected rows"
              className="sticky bottom-4 z-30 mt-4 flex flex-wrap items-center gap-2 px-4 py-3 shadow-lg"
            >
              <b className="mr-auto text-sm tabular-nums">{chosen.length} selected</b>
              <Button
                variant="outline"
                disabled={search.isPending}
                onClick={() => searchFor(chosen)}
              >
                {search.isPending
                  ? "Searching…"
                  : new Set(chosen.map((i) => i.versionId)).size > SEARCH_LIMIT
                    ? `Search first ${SEARCH_LIMIT}`
                    : "Search"}
              </Button>
              <Button variant="outline" onClick={() => setConfirming(true)}>
                Stop monitoring
              </Button>
              <Button variant="ghost" onClick={() => setSelected(new Set())}>
                Clear
              </Button>
            </Card>
          )}
        </>
      )}

      <StopMonitoringDialog
        open={confirming}
        items={chosen}
        onClose={() => setConfirming(false)}
        onDone={() => setSelected(new Set())}
      />
    </div>
  );
}

function stateBadge(it: WantedItem) {
  switch (it.state) {
    case "downloading":
      return <Badge variant="info">Downloading</Badge>;
    case "off":
      return <Badge variant="neutral">Grabbing is off</Badge>;
    case "capped":
      return <Badge variant="warning">Queue full</Badge>;
    case "noSpace":
      return <Badge variant="warning">Disk full</Badge>;
    case "never":
      return <Badge variant="neutral">Not searched yet</Badge>;
    default:
      return <Badge variant="neutral">Waiting</Badge>;
  }
}

function dateLabel(it: WantedItem) {
  return it.kind === "movie" ? `Available ${it.date}` : `Latest aired ${it.date}`;
}

function WantedRow({
  item: it,
  index,
  checked,
  onToggle,
  onSearch,
  searching,
}: {
  item: WantedItem;
  index: number;
  checked: boolean;
  onToggle: () => void;
  onSearch: () => void;
  searching: boolean;
}) {
  const [open, setOpen] = useState(false);
  const isShow = it.kind === "episodes";
  const episodes = it.episodes ?? [];
  const seasonName = it.season === 0 ? "Specials" : `Season ${it.season}`;
  const name = isShow ? `${it.title}, ${seasonName.toLowerCase()}` : it.title;
  return (
    <li
      className="q-row animate-rise grid grid-cols-[auto_minmax(0,1fr)] items-start gap-x-3 gap-y-3 border-b px-4 py-4 last:border-b-0 sm:grid-cols-[auto_2.75rem_minmax(0,1fr)_auto] sm:px-5"
      style={riseStyle(index)}
    >
      <input
        type="checkbox"
        aria-label={`Select ${name}`}
        className="mt-1.5 size-4"
        checked={checked}
        onChange={onToggle}
      />
      <PosterThumb path={it.posterPath || undefined} className="hidden sm:block" />
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-semibold break-words">
            <MovieName
              movie={{ id: it.titleId, title: it.title, year: it.year }}
              mediaType={isShow ? "series" : "movie"}
            />
          </span>
          <Badge>{it.profile}</Badge>
          {stateBadge(it)}
          {it.date && <span className="text-xs text-muted-foreground">{dateLabel(it)}</span>}
        </div>
        {isShow && (
          <div className="mt-1">
            <button
              type="button"
              aria-expanded={open}
              onClick={() => setOpen((o) => !o)}
              className="inline-flex min-h-8 cursor-pointer items-center gap-1 rounded-md text-sm text-link hover:underline"
            >
              <ChevronRight
                className={cn(
                  "size-4 transition-transform duration-[calc(200ms*var(--m))]",
                  open && "rotate-90",
                )}
                aria-hidden="true"
              />
              {seasonName} · {plural(episodes.length, "episode")}
            </button>
            {open && (
              <ul className="mt-1 mb-2 space-y-0.5 border-l pl-4 text-sm">
                {episodes.map((e) => (
                  <li key={e.episodeId} className="flex flex-wrap gap-x-2">
                    <span className="font-mono text-xs/5 text-muted-foreground">
                      S{String(e.season).padStart(2, "0")}E{String(e.number).padStart(2, "0")}
                    </span>
                    <span>{e.name || "Untitled"}</span>
                    <span className="text-xs/5 text-muted-foreground">{e.airDate}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
        {it.quality && it.target && it.score === undefined && (
          <p className="mt-1 text-sm">
            <span className="font-medium">{it.quality}</span>
            <span aria-hidden="true"> → </span>
            <span className="sr-only"> to </span>
            <span className="font-medium">{it.target}</span>
          </p>
        )}
        {/* The quality is already there; only a better format score is wanted. */}
        {it.quality && it.score !== undefined && (
          <>
            <p className="mt-1 text-sm">
              <span className="font-medium">{it.quality}</span>, format score{" "}
              <span className="font-medium">{it.score}</span>
              {it.targetScore !== undefined && (
                <>
                  <span aria-hidden="true"> → </span>
                  <span className="sr-only"> to </span>
                  <span className="font-medium">{it.targetScore}</span>
                </>
              )}
            </p>
            <p className="mt-0.5 text-xs break-words text-muted-foreground">
              {it.formats?.length
                ? `The file has ${it.formats.map((f) => `${f.name} (${f.score > 0 ? "+" : ""}${f.score})`).join(", ")}.`
                : "The file has none of the profile's scored formats."}
            </p>
          </>
        )}
        <p className="mt-1 text-sm break-words">{it.status}</p>
        {(it.lastSearchMessage || it.lastSearchedAt) && (
          <p className="mt-0.5 text-xs break-words text-muted-foreground">
            {it.lastSearchMessage || "Searched."}
            {it.lastSearchedAt && <> · {relativeTime(it.lastSearchedAt)}</>}
          </p>
        )}
      </div>
      <div className="col-span-full flex flex-wrap items-start gap-2 sm:col-span-1">
        <Button
          variant="outline"
          size="sm"
          disabled={searching}
          title={
            isShow
              ? "Searches for all of this show's wanted episodes, not just this season."
              : undefined
          }
          aria-label={`${isShow ? "Search this show" : "Search now"}: ${name}`}
          onClick={onSearch}
        >
          {isShow ? "Search this show" : "Search now"}
        </Button>
        {isShow && <AddToBackfillButton versionId={it.versionId} title={it.title} />}
      </div>
    </li>
  );
}

function StopMonitoringDialog({
  open,
  items,
  onClose,
  onDone,
}: {
  open: boolean;
  items: WantedItem[];
  onClose: () => void;
  onDone: () => void;
}) {
  const stop = useUnmonitorWanted();
  const movies = items.filter((i) => i.kind === "movie");
  const groups = items.filter((i) => i.kind === "episodes");
  const episodeCount = groups.reduce((n, g) => n + (g.episodes?.length ?? 0), 0);
  const what = [
    movies.length > 0 && plural(movies.length, "movie"),
    episodeCount > 0 && plural(episodeCount, "episode"),
  ]
    .filter(Boolean)
    .join(" and ");

  function close() {
    stop.reset();
    onClose();
  }

  function confirm() {
    // One entry per series version, with every chosen season's episodes.
    const byVersion = new Map<number, number[]>();
    for (const g of groups) {
      const ids = byVersion.get(g.versionId) ?? [];
      ids.push(...(g.episodes ?? []).map((e) => e.episodeId));
      byVersion.set(g.versionId, ids);
    }
    stop.mutate(
      {
        movies: movies.map((m) => m.versionId),
        episodes: [...byVersion].map(([versionId, episodeIds]) => ({ versionId, episodeIds })),
      },
      {
        onSuccess: () => {
          toast(`Stopped monitoring ${what}.`, "ok");
          onDone();
          close();
        },
      },
    );
  }

  const err = stop.error instanceof RequestError ? stop.error : undefined;
  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent>
        <DialogTitle>Stop monitoring {what}?</DialogTitle>
        <DialogDescription>
          Reelkeeper stops looking for them. You can turn them back on from their pages.
        </DialogDescription>
        {err && <Alert variant="error">{err.message}</Alert>}
        <div className="flex justify-end gap-2">
          <DialogClose asChild>
            <Button variant="ghost">Cancel</Button>
          </DialogClose>
          <Button variant="destructive" disabled={stop.isPending} onClick={confirm}>
            {stop.isPending ? "Stopping…" : "Stop monitoring"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
