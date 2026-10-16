import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { moviesQuery, seriesListQuery } from "@/hooks/library";
import { tmdbImage } from "@/lib/images";
import { cn } from "@/lib/utils";

/** How long a removed row plays its leaving animation before it is dropped. */
const leaveMs = 450;

/**
 * Rows that were just removed. The server forgets them straight away, so the
 * page keeps a copy for a moment to let it slide out (see .row-leave in
 * styles/activity.css). `merge` puts the leaving rows back where they were.
 */
export function useLeaving<T extends { id: number }>() {
  const [leaving, setLeaving] = useState<{ item: T; index: number }[]>([]);
  const timers = useRef<number[]>([]);
  useEffect(() => {
    const t = timers.current;
    return () => {
      for (const id of t) window.clearTimeout(id);
    };
  }, []);

  const leave = useCallback((item: T, index: number) => {
    setLeaving((cur) => [...cur.filter((l) => l.item.id !== item.id), { item, index }]);
    timers.current.push(
      window.setTimeout(
        () => setLeaving((cur) => cur.filter((l) => l.item.id !== item.id)),
        leaveMs,
      ),
    );
  }, []);

  function merge(items: T[]): { item: T; leaving: boolean }[] {
    const out = items.map((item) => ({ item, leaving: false }));
    for (const l of [...leaving].sort((a, b) => a.index - b.index)) {
      if (out.some((o) => o.item.id === l.item.id)) continue;
      out.splice(Math.min(l.index, out.length), 0, { item: l.item, leaving: true });
    }
    return out;
  }
  return { leave, merge };
}

export type MediaType = "movie" | "series";

/** The key a poster is stored under in usePosters(). */
export const posterKey = (mediaType: MediaType | undefined, id: number) =>
  `${mediaType ?? "movie"}:${id}`;

/** Each title's poster path, from the same lists the Movies and Series pages show. Look up with posterKey. */
export function usePosters() {
  const movies = useQuery(moviesQuery);
  const series = useQuery(seriesListQuery);
  const map = new Map<string, string>();
  for (const m of movies.data ?? [])
    if (m.posterPath) map.set(posterKey("movie", m.id), m.posterPath);
  for (const m of series.data ?? [])
    if (m.posterPath) map.set(posterKey("series", m.id), m.posterPath);
  return map;
}

/** A small poster, or an empty tile when the movie has none. */
export function PosterThumb({ path, className }: { path?: string; className?: string }) {
  return (
    <div
      className={cn(
        "relative h-[66px] w-11 shrink-0 overflow-hidden rounded-md border bg-muted",
        className,
      )}
    >
      {path && (
        <img
          src={tmdbImage(path, "w185")}
          alt=""
          loading="lazy"
          className="absolute inset-0 size-full object-cover"
        />
      )}
    </div>
  );
}

/**
 * The title's name and year, linking to its page (or "Removed title"). Series
 * link to the series page and, for TV downloads, show which episodes follow.
 */
export function MovieName({
  movie,
  mediaType = "movie",
  episodes,
  className,
}: {
  movie: { id: number; title: string; year: number } | null | undefined;
  mediaType?: MediaType;
  /** For TV downloads: "S02E05" or "season 2 (8 wanted episodes)". */
  episodes?: string;
  className?: string;
}) {
  if (!movie) return <span>Removed title</span>;
  const text = `${movie.title}${movie.year ? ` (${movie.year})` : ""}`;
  const params = { id: String(movie.id) };
  return (
    <>
      {mediaType === "series" ? (
        <Link
          to="/series/$id"
          params={params}
          className={cn("text-link hover:underline", className)}
        >
          {text}
        </Link>
      ) : (
        <Link
          to="/movies/$id"
          params={params}
          className={cn("text-link hover:underline", className)}
        >
          {text}
        </Link>
      )}
      {episodes && <span className="font-normal text-muted-foreground"> · {episodes}</span>}
    </>
  );
}

/** The staggered entrance of the prototype's lists. */
export function riseStyle(i: number) {
  return { animationDelay: `calc(${Math.min(i, 14)} * 45ms * var(--m))` };
}
