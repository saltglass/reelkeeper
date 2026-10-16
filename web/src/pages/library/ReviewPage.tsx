import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowLeft, Check, EyeOff, Search } from "lucide-react";
import { useEffect, useState } from "react";
import { api, unwrap } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { type ReviewItem, reviewQuery, useInvalidateLibrary } from "@/hooks/library";
import { tmdbImage } from "@/lib/images";
import { riseStyle } from "@/pages/activity/shared";
import { PageHeader } from "@/pages/PageHeader";

type Choice = { tmdbId: number; title: string; year: number; overview: string; posterPath: string };

export function ReviewPage() {
  const { data, error } = useQuery(reviewQuery);
  if (error) return <Alert variant="error">Couldn't load the review list: {error.message}</Alert>;
  return (
    <div className="space-y-5">
      <div className="flex gap-4 text-sm text-muted-foreground">
        <Link to="/movies" className="inline-flex items-center gap-1 hover:text-foreground">
          <ArrowLeft className="size-4" /> Movies
        </Link>
        <Link to="/series" className="inline-flex items-center gap-1 hover:text-foreground">
          <ArrowLeft className="size-4" /> Series
        </Link>
      </div>
      <PageHeader
        title="Review"
        description="Folders Reelkeeper found but couldn't identify for sure. Pick the right movie or series, or ignore folders that aren't either (home videos, for example)."
      />
      {!data ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : data.length === 0 ? (
        <div className="flex flex-col items-center gap-3 px-6 py-14 text-center text-muted-foreground">
          <span className="relative grid size-28 place-items-center rounded-full bg-accent text-accent-foreground motion-safe:animate-[float_calc(4s*var(--m))_ease-in-out_infinite]">
            <span
              aria-hidden="true"
              className="absolute -inset-2.5 rounded-full border border-dashed border-primary/50"
            />
            <Check className="size-10" />
          </span>
          <Alert variant="success">Nothing to review. Every folder has been identified.</Alert>
        </div>
      ) : (
        data.map((it, i) => <ReviewCard key={it.id} item={it} index={i} />)
      )}
    </div>
  );
}

function ReviewCard({ item, index }: { item: ReviewItem; index: number }) {
  const isSeries = item.mediaType === "series";
  const noun = isSeries ? "series" : "movie";
  const invalidate = useInvalidateLibrary();
  const [searching, setSearching] = useState(false);
  const resolve = useMutation({
    mutationFn: (tmdbId: number) =>
      unwrap(
        api.POST("/library/review/{id}/resolve", {
          params: { path: { id: item.id } },
          body: { tmdbId },
        }),
      ),
    onSuccess: invalidate,
  });
  const ignore = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/library/review/{id}/ignore", { params: { path: { id: item.id } } })),
    onSuccess: invalidate,
  });
  const err = resolve.error ?? ignore.error;

  return (
    <Card className="animate-rise" style={riseStyle(index)}>
      <CardContent className="space-y-4">
        <div className="space-y-1">
          <Badge variant={isSeries ? "info" : "neutral"}>
            {isSeries ? "Series folder" : "Movie folder"}
          </Badge>
          <p className="font-mono text-sm break-all">
            <span className="text-muted-foreground">{item.rootPath}/</span>
            {item.path}
          </p>
          <p className="text-sm text-warning">{item.reason}</p>
          {item.files.length > 1 && (
            <p className="text-xs text-muted-foreground">{item.files.length} video files</p>
          )}
        </div>
        {err && <Alert variant="error">{err.message}</Alert>}

        {item.candidates.length > 0 && (
          <div className="space-y-2">
            <p className="text-sm font-medium">Is it one of these?</p>
            <ChoiceList
              choices={item.candidates}
              busy={resolve.isPending}
              onPick={(id) => resolve.mutate(id)}
            />
          </div>
        )}

        {searching ? (
          <SearchChoice
            series={isSeries}
            initial={[item.titleGuess, item.yearGuess || ""].join(" ").trim()}
            busy={resolve.isPending}
            onPick={(id) => resolve.mutate(id)}
          />
        ) : (
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" size="sm" onClick={() => setSearching(true)}>
              <Search /> {item.candidates.length > 0 ? "None of these: search" : "Search TMDB"}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              disabled={ignore.isPending}
              onClick={() => ignore.mutate()}
            >
              <EyeOff /> Not a {noun}: ignore this folder
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function ChoiceList({
  choices,
  busy,
  onPick,
}: {
  choices: Choice[];
  busy: boolean;
  onPick: (id: number) => void;
}) {
  return (
    <ul className="grid gap-2 sm:grid-cols-2">
      {choices.map((c) => (
        <li
          key={c.tmdbId}
          className="flex gap-3 rounded-lg border bg-background/40 p-2.5 transition-[border-color,transform] duration-[calc(200ms*var(--m))] hover:-translate-y-px hover:border-primary/50"
        >
          <div className="h-21 w-14 shrink-0 overflow-hidden rounded-md border bg-muted">
            {c.posterPath && (
              <img
                src={tmdbImage(c.posterPath, "w185")}
                alt=""
                className="size-full object-cover"
              />
            )}
          </div>
          <div className="flex min-w-0 flex-1 flex-col gap-1">
            <p className="text-sm font-medium">
              {c.title} {c.year > 0 && <span className="text-muted-foreground">({c.year})</span>}
            </p>
            <p className="line-clamp-2 text-xs text-muted-foreground">{c.overview}</p>
            <Button
              size="sm"
              className="mt-auto self-start"
              disabled={busy}
              onClick={() => onPick(c.tmdbId)}
            >
              <Check /> This one
            </Button>
          </div>
        </li>
      ))}
    </ul>
  );
}

function SearchChoice({
  initial,
  series,
  busy,
  onPick,
}: {
  initial: string;
  series: boolean;
  busy: boolean;
  onPick: (id: number) => void;
}) {
  const [q, setQ] = useState(initial);
  const [term, setTerm] = useState(initial);
  useEffect(() => {
    const t = setTimeout(() => setTerm(q.trim()), 400);
    return () => clearTimeout(t);
  }, [q]);
  // Same TMDB search the Add dialogs use, for the kind of folder at hand.
  const results = useQuery({
    queryKey: [series ? "series" : "movies", "lookup", term],
    queryFn: () =>
      series
        ? unwrap(api.GET("/series/lookup", { params: { query: { q: term } } }))
        : unwrap(api.GET("/movies/lookup", { params: { query: { q: term } } })),
    enabled: term.length >= 2,
  });
  return (
    <div className="space-y-2">
      <Input aria-label="Search TMDB" value={q} onChange={(e) => setQ(e.target.value)} autoFocus />
      {results.error && <Alert variant="error">{results.error.message}</Alert>}
      {results.data && results.data.length === 0 && (
        <p className="text-sm text-muted-foreground">Nothing found.</p>
      )}
      {results.data && results.data.length > 0 && (
        <ChoiceList choices={results.data.slice(0, 6)} busy={busy} onPick={onPick} />
      )}
    </div>
  );
}
