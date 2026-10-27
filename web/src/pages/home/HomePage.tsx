// Home: is everything OK, and does anything need me? Then what's happening
// now and what's coming up this week. It only reports and links; every fix
// happens on the page it links to.

import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronRight, CircleAlert, CircleCheck, Info, TriangleAlert } from "lucide-react";
import type { ReactNode } from "react";
import { Card } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { downloadClientQuery, queueQuery } from "@/hooks/downloads";
import { grabbingQuery, historyQuery } from "@/hooks/grab";
import { indexersQuery } from "@/hooks/indexers";
import { moviesQuery, reviewQuery, rootsQuery, seriesListQuery } from "@/hooks/library";
import { type Summary, summaryQuery } from "@/hooks/summary";
import { missingTotal, wantedCountsQuery } from "@/hooks/wanted";
import { relativeTime, shortDuration } from "@/lib/time";
import { cn } from "@/lib/utils";
import { PageHeader } from "../PageHeader";

type Tone = "error" | "warning" | "info";

/** One line under "Needs your attention", and where it's fixed. */
export type Problem = { tone: Tone; text: string; to: string; search?: { filter: "attention" } };

type ModeName = "off" | "dryrun" | "on";

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** "A is failing" / "A and B are failing" / "A, B and C are failing". */
function failingText(names: string[]): string {
  const list =
    names.length === 1
      ? names[0]
      : `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
  return `${list} ${names.length === 1 ? "is" : "are"} failing.`;
}

/** What Grabbing being off or in dry run means, for movies, TV or both. */
function modeProblems(movies: ModeName, tv: ModeName): Problem[] {
  const say = (m: ModeName) =>
    m === "off"
      ? "off: Reelkeeper doesn't search or download."
      : "in dry run: Reelkeeper notes what it would grab but downloads nothing.";
  const to = "/settings/download-client";
  if (movies === tv)
    return movies === "on" ? [] : [{ tone: "info", text: `Grabbing is ${say(movies)}`, to }];
  const out: Problem[] = [];
  if (movies !== "on") out.push({ tone: "info", text: `Movie grabbing is ${say(movies)}`, to });
  if (tv !== "on") out.push({ tone: "info", text: `TV grabbing is ${say(tv)}`, to });
  return out;
}

/**
 * Everything that needs attention, worst first. Each is listed only while
 * it's true; undefined inputs (still loading) list nothing.
 */
export function problemsOf(d: {
  queue?: { clientError: string | null; builtinState: string | null; diskShort: string | null };
  summary?: Summary;
  review?: number;
  seriesProblems?: number;
  modes?: { movies: ModeName; tv: ModeName };
}): Problem[] {
  const out: Problem[] = [];
  const client = "/settings/download-client";
  if (d.queue?.clientError) out.push({ tone: "error", text: d.queue.clientError, to: client });
  switch (d.queue?.builtinState) {
    case "down":
      out.push({ tone: "error", text: "The VPN is down, so downloads are stopped.", to: client });
      break;
    case "connecting":
      out.push({
        tone: "error",
        text: "The VPN hasn't connected yet, so downloads wait.",
        to: client,
      });
      break;
    case "held":
      out.push({
        tone: "error",
        text: "The VPN was removed, so the built-in client stays stopped until you choose “Start without VPN”.",
        to: client,
      });
      break;
  }
  if (d.queue?.diskShort) {
    const on = d.modes && (d.modes.movies === "on" || d.modes.tv === "on");
    out.push({
      tone: "warning",
      text: on
        ? `${d.queue.diskShort} Grabs and the hourly search wait until cleanup makes room.`
        : d.queue.diskShort,
      to: "/settings/cleanup",
    });
  }
  const s = d.summary;
  if (s && s.queue.needsAttention > 0) {
    const n = s.queue.needsAttention;
    out.push({
      tone: "warning",
      text: `${plural(n, "download")} finished but couldn't be imported.`,
      to: "/activity/queue",
    });
  }
  if (s?.queue.full) {
    out.push({
      tone: "warning",
      text: `The queue is full (${s.queue.movies + s.queue.tv} of ${s.queue.limit}), so nothing new is grabbed until downloads finish.`,
      to: "/activity/queue",
    });
  }
  if (d.review && d.review > 0) {
    out.push({
      tone: "warning",
      text: `${d.review === 1 ? "1 folder needs" : `${d.review} folders need`} your help identifying ${d.review === 1 ? "it" : "them"}.`,
      to: "/review",
    });
  }
  if (d.seriesProblems && d.seriesProblems > 0) {
    out.push({
      tone: "warning",
      text: `${d.seriesProblems === 1 ? "1 show has" : `${d.seriesProblems} shows have`} files Reelkeeper isn't sure about.`,
      to: "/series",
      search: { filter: "attention" },
    });
  }
  if (s && s.indexers.failing.length > 0) {
    out.push({ tone: "warning", text: failingText(s.indexers.failing), to: "/settings/indexers" });
  }
  if (d.modes) out.push(...modeProblems(d.modes.movies, d.modes.tv));
  return out;
}

const toneIcon: Record<Tone, ReactNode> = {
  error: <CircleAlert className="text-destructive" />,
  warning: <TriangleAlert className="text-warning" />,
  info: <Info className="text-muted-foreground" />,
};

export function HomePage() {
  const summary = useQuery(summaryQuery);
  const queue = useQuery({ ...queueQuery, refetchInterval: 15_000 });
  const review = useQuery(reviewQuery);
  const movies = useQuery(moviesQuery);
  const series = useQuery(seriesListQuery);
  const wanted = useQuery(wantedCountsQuery);
  const grabbing = useQuery(grabbingQuery);
  const roots = useQuery(rootsQuery);
  const indexers = useQuery(indexersQuery);
  const client = useQuery(downloadClientQuery);

  // A new install: what's left to set up, until all three are done.
  const c = client.data;
  const steps =
    roots.data && indexers.data && c
      ? [
          { done: roots.data.length > 0, text: "Add a media folder", to: "/settings/media" },
          { done: indexers.data.length > 0, text: "Add an indexer", to: "/settings/indexers" },
          {
            done: c.type === "builtin" ? c.folder !== "" : c.url !== "",
            text: "Set up a download client",
            to: "/settings/download-client",
          },
        ]
      : undefined;
  const settingUp = steps?.some((s) => !s.done) ?? false;

  const problems = problemsOf({
    queue: queue.data,
    summary: summary.data,
    review: review.data?.length,
    seriesProblems: series.data?.filter((s) => s.problems > 0).length,
    modes: grabbing.data && {
      movies: grabbing.data.mode,
      tv: grabbing.data.tvMode ?? grabbing.data.mode,
    },
  });
  const loaded = !!(summary.data && queue.data && grabbing.data);

  const numbers: { label: string; n?: number; to: string }[] = [
    { label: "Movies", n: movies.data?.length, to: "/movies" },
    { label: "Series", n: series.data?.length, to: "/series" },
    { label: "Wanted", n: wanted.data && missingTotal(wanted.data.counts), to: "/wanted" },
    { label: "Grabbed today", n: summary.data?.grabs.today, to: "/activity/history" },
  ];

  return (
    <div>
      <PageHeader
        title="Home"
        description="How Reelkeeper is doing, and anything that needs you."
      />

      {settingUp && steps ? (
        <Card className="mb-6 animate-rise p-5">
          <h2 className="mb-1 font-semibold">Finish setting up</h2>
          <p className="mb-3 text-sm text-muted-foreground">
            Three steps and Reelkeeper can find, download and organize on its own.
          </p>
          <ul className="space-y-1">
            {steps.map((s) => (
              <li key={s.to}>
                <Link
                  to={s.to}
                  aria-label={`${s.done ? "Done" : "To do"}: ${s.text}`}
                  className="flex min-h-10 items-center gap-3 rounded-md px-2 text-sm hover:bg-muted"
                >
                  {s.done ? (
                    <CircleCheck className="size-5 text-success" aria-hidden="true" />
                  ) : (
                    <span
                      className="size-5 rounded-full border-2 border-input"
                      aria-hidden="true"
                    />
                  )}
                  <span className={cn(s.done && "text-muted-foreground line-through")}>
                    {s.text}
                  </span>
                  <ChevronRight className="ml-auto size-4 text-muted-foreground" />
                </Link>
              </li>
            ))}
          </ul>
        </Card>
      ) : (
        loaded && (
          <Card className="mb-6 animate-rise overflow-hidden">
            {problems.length === 0 ? (
              <p className="flex items-center gap-3 px-5 py-4 text-sm">
                <CircleCheck className="size-5 shrink-0 text-success" />
                All good. Reelkeeper is searching and downloading normally.
              </p>
            ) : (
              <>
                <h2 className="border-b px-5 py-3 text-sm font-semibold">Needs your attention</h2>
                <ul className="divide-y">
                  {problems.map((p) => (
                    <li key={p.text}>
                      <Link
                        to={p.to}
                        search={p.search}
                        className="flex min-h-12 items-center gap-3 px-5 py-3 text-sm hover:bg-muted/50 [&>svg:first-child]:size-5 [&>svg:first-child]:shrink-0"
                      >
                        {toneIcon[p.tone]}
                        <span className="flex-1">{p.text}</span>
                        <ChevronRight className="size-4 shrink-0 text-muted-foreground" />
                      </Link>
                    </li>
                  ))}
                </ul>
              </>
            )}
          </Card>
        )
      )}

      <dl className="mb-6 grid grid-cols-2 gap-3 sm:grid-cols-4">
        {numbers.map((x, i) => (
          <Link key={x.label} to={x.to} className="group">
            <Card
              className="animate-rise px-4 py-3.5 transition-colors group-hover:border-input"
              style={{ animationDelay: `calc(${40 + i * 50}ms * var(--m))` }}
            >
              <dt className="text-xs font-medium text-muted-foreground">{x.label}</dt>
              <dd className="mt-1 text-[22px]/7 font-semibold tracking-tight tabular-nums">
                {x.n ?? "—"}
              </dd>
            </Card>
          </Link>
        ))}
      </dl>

      <div className="grid gap-6 lg:grid-cols-2">
        <section aria-labelledby="home-now" className="min-w-0 space-y-6">
          <h2 id="home-now" className="sr-only">
            Happening now
          </h2>
          <Downloading />
          <Recently />
        </section>
        <ComingUp summary={summary.data} />
      </div>
    </div>
  );
}

function Panel({
  title,
  link,
  children,
}: {
  title: string;
  link?: { to: string; text: string };
  children: ReactNode;
}) {
  return (
    <Card className="animate-rise overflow-hidden">
      <div className="flex items-center gap-2 border-b px-5 py-3">
        <h3 className="text-sm font-semibold">{title}</h3>
        {link && (
          <Link to={link.to} className="ml-auto text-sm text-link hover:underline">
            {link.text}
          </Link>
        )}
      </div>
      {children}
    </Card>
  );
}

const empty = (text: string) => <p className="px-5 py-4 text-sm text-muted-foreground">{text}</p>;

/** Up to 5 downloads in progress, with their progress. */
function Downloading() {
  const { data } = useQuery({ ...queueQuery, refetchInterval: 15_000 });
  const items = (data?.items ?? []).filter((i) => i.status === "downloading");
  return (
    <Panel
      title="Downloading"
      link={
        items.length > 0
          ? { to: "/activity/queue", text: `See all ${items.length} in the Queue` }
          : undefined
      }
    >
      {!data ? (
        empty("Loading…")
      ) : items.length === 0 ? (
        empty("Nothing downloading.")
      ) : (
        <ul className="divide-y">
          {items.slice(0, 5).map((it) => {
            const p = it.torrent?.progress ?? 0;
            const eta = it.torrent?.etaSeconds;
            const name = it.movie?.title ?? it.releaseTitle;
            return (
              <li key={it.id} className="space-y-1.5 px-5 py-3">
                <p className="flex items-baseline gap-2 text-sm">
                  <span className="min-w-0 flex-1 truncate font-medium">
                    {name}
                    {it.episodes && (
                      <span className="font-normal text-muted-foreground"> · {it.episodes}</span>
                    )}
                  </span>
                  <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
                    {Math.round(p * 100)}%{eta ? ` · ${shortDuration(eta)} left` : ""}
                  </span>
                </p>
                <Progress value={p * 100} label={`${name} download`} />
              </li>
            );
          })}
        </ul>
      )}
    </Panel>
  );
}

/** The last 6 grabs and imports. */
function Recently() {
  const { data } = useQuery(historyQuery({ events: ["grabbed", "imported"] }, 6));
  const events = data?.events ?? [];
  return (
    <Panel title="Recently" link={{ to: "/activity/history", text: "See History" }}>
      {!data ? (
        empty("Loading…")
      ) : events.length === 0 ? (
        empty("Nothing grabbed or imported yet.")
      ) : (
        <ul className="divide-y">
          {events.map((e) => (
            <li key={e.id} className="flex items-baseline gap-2 px-5 py-2.5 text-sm">
              <span
                className={cn(
                  "w-16 shrink-0 text-xs font-medium",
                  e.event === "imported" ? "text-success" : "text-accent-foreground",
                )}
              >
                {e.event === "imported" ? "Imported" : "Grabbed"}
              </span>
              <span className="min-w-0 flex-1 truncate">
                <TitleLink id={e.titleId} kind={e.mediaType} title={e.title ?? e.sourceTitle} />
              </span>
              <span className="shrink-0 text-xs text-muted-foreground">
                {relativeTime(e.createdAt)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  );
}

function TitleLink({ id, kind, title }: { id?: number | null; kind?: string; title: string }) {
  if (!id) return <>{title}</>;
  return kind === "series" ? (
    <Link to="/series/$id" params={{ id: String(id) }} className="hover:underline">
      {title}
    </Link>
  ) : (
    <Link to="/movies/$id" params={{ id: String(id) }} className="hover:underline">
      {title}
    </Link>
  );
}

/** "Today", "Tomorrow", or "Fri 31 Oct", for a YYYY-MM-DD date. */
export function dayLabel(date: string, now = new Date()): string {
  const iso = (d: Date) =>
    `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  if (date === iso(now)) return "Today";
  const tomorrow = new Date(now);
  tomorrow.setDate(now.getDate() + 1);
  if (date === iso(tomorrow)) return "Tomorrow";
  const [y, m, d] = date.split("-").map(Number);
  return new Date(y ?? 0, (m ?? 1) - 1, d ?? 1).toLocaleDateString("en-GB", {
    weekday: "short",
    day: "numeric",
    month: "short",
  });
}

const releaseKind: Record<string, string> = {
  "in cinemas": "in cinemas",
  digital: "digital release",
  physical: "disc release",
};

/** Movies and episodes due in the next 7 days, by date, at most 10. */
function ComingUp({ summary }: { summary?: Summary }) {
  const rows = summary
    ? [
        ...summary.upcoming.map((m) => ({
          key: `m${m.titleId}${m.kind}`,
          date: m.date,
          kind: "movie" as const,
          id: m.titleId,
          title: `${m.title} (${m.year})`,
          detail: releaseKind[m.kind] ?? m.kind,
        })),
        ...summary.upcomingEpisodes.map((e) => ({
          key: `e${e.titleId}-${e.season}-${e.episode}`,
          date: e.date,
          kind: "series" as const,
          id: e.titleId,
          title: e.title,
          detail: `S${String(e.season).padStart(2, "0")}E${String(e.episode).padStart(2, "0")}${e.name ? ` ${e.name}` : ""}`,
        })),
      ]
        .sort((a, b) => a.date.localeCompare(b.date))
        .slice(0, 10)
    : [];
  return (
    <section aria-labelledby="home-coming" className="min-w-0">
      <Card className="animate-rise overflow-hidden">
        <div className="border-b px-5 py-3">
          <h2 id="home-coming" className="text-sm font-semibold">
            Coming up this week
          </h2>
        </div>
        {!summary ? (
          empty("Loading…")
        ) : rows.length === 0 ? (
          empty("Nothing due in the next 7 days.")
        ) : (
          <ul className="divide-y">
            {rows.map((r) => (
              <li key={r.key} className="flex items-baseline gap-3 px-5 py-2.5 text-sm">
                <span className="w-20 shrink-0 text-xs font-medium text-muted-foreground">
                  {dayLabel(r.date)}
                </span>
                <span className="min-w-0 flex-1 truncate">
                  <TitleLink id={r.id} kind={r.kind} title={r.title} />
                  <span className="text-muted-foreground"> · {r.detail}</span>
                </span>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </section>
  );
}
