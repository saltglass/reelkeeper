import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import {
  ArrowLeft,
  CircleAlert,
  ExternalLink,
  FilePen,
  RefreshCw,
  Search,
  Trash2,
} from "lucide-react";
import { useState } from "react";
import { api, unwrap } from "@/api/client";
import { BlockedReleases } from "@/components/BlockedReleases";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { historyQuery } from "@/hooks/grab";
import {
  type MediaFile,
  type MovieDetail,
  movieQuery,
  useInvalidateLibrary,
} from "@/hooks/library";
import {
  channelLayout,
  formatBytes,
  formatRuntime,
  languageName,
  qualityLabel,
} from "@/lib/format";
import { tmdbImage } from "@/lib/images";
import { relativeTime } from "@/lib/time";
import { HistoryRow } from "@/pages/activity/HistoryRow";
import { RenameDialog } from "@/pages/library/RenameDialog";
import { DeleteMovieDialog } from "./DeleteMovieDialog";

export function MovieDetailPage() {
  const { id } = useParams({ from: "/app/movies/$id" });
  const movieId = Number(id);
  const { data: m, error } = useQuery(movieQuery(movieId));
  const invalidate = useInvalidateLibrary();
  const [renaming, setRenaming] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const refresh = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/movies/{id}/refresh", { params: { path: { id: movieId } } })),
    onSuccess: invalidate,
  });

  if (error) return <Alert variant="error">Couldn't load the movie: {error.message}</Alert>;
  if (!m) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const facts = [
    m.year > 0 && String(m.year),
    formatRuntime(m.runtime),
    m.certification,
    m.genres.join(", "),
  ].filter(Boolean);

  return (
    <div className="space-y-6">
      <Link
        to="/movies"
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="size-4" /> Movies
      </Link>

      <div className="animate-rise relative overflow-hidden rounded-xl border bg-card shadow-md">
        {m.backdropPath && (
          <img
            src={tmdbImage(m.backdropPath, "w1280")}
            alt=""
            className="absolute inset-0 size-full object-cover opacity-20"
          />
        )}
        <div
          aria-hidden="true"
          className="absolute inset-0 bg-gradient-to-r from-card via-card/70 to-transparent"
        />
        <div className="relative flex flex-col gap-5 p-5 sm:flex-row">
          <div className="aspect-[2/3] w-36 shrink-0 overflow-hidden rounded-[10px] border bg-muted shadow-lg sm:w-44">
            {m.posterPath && (
              <img
                src={tmdbImage(m.posterPath, "w342")}
                alt=""
                className="size-full object-cover"
              />
            )}
          </div>
          <div className="min-w-0 space-y-3">
            <div>
              <h1 className="text-2xl font-semibold">{m.title}</h1>
              {m.originalTitle && m.originalTitle !== m.title && (
                <p className="text-sm text-muted-foreground">{m.originalTitle}</p>
              )}
            </div>
            <p className="text-sm text-muted-foreground">{facts.join(" · ")}</p>
            {m.overview && <p className="max-w-prose text-sm leading-relaxed">{m.overview}</p>}
            <ReleaseDates m={m} />
            <div className="flex flex-wrap items-center gap-2 pt-1">
              <Button variant="outline" size="sm" asChild>
                <a
                  href={`https://www.themoviedb.org/movie/${m.tmdbId}`}
                  target="_blank"
                  rel="noreferrer"
                >
                  TMDB <ExternalLink />
                </a>
              </Button>
              {m.imdbId && (
                <Button variant="outline" size="sm" asChild>
                  <a
                    href={`https://www.imdb.com/title/${m.imdbId}`}
                    target="_blank"
                    rel="noreferrer"
                  >
                    IMDb <ExternalLink />
                  </a>
                </Button>
              )}
              <Button
                variant="outline"
                size="sm"
                disabled={refresh.isPending}
                onClick={() => refresh.mutate()}
              >
                <RefreshCw /> {refresh.isPending ? "Refreshing…" : "Refresh details"}
              </Button>
              {m.versions.some((v) => v.mediaFiles.length > 0) && (
                <Button variant="outline" size="sm" onClick={() => setRenaming(true)}>
                  <FilePen /> Rename files
                </Button>
              )}
              <Button
                variant="ghost"
                size="sm"
                className="text-destructive hover:text-destructive"
                onClick={() => setDeleting(true)}
              >
                <Trash2 /> Delete movie
              </Button>
              {m.metadataUpdatedAt && (
                <span className="text-xs text-muted-foreground">
                  Details from TMDB, {relativeTime(m.metadataUpdatedAt)}
                </span>
              )}
            </div>
            {refresh.error && <Alert variant="error">{refresh.error.message}</Alert>}
          </div>
        </div>
      </div>

      {m.versions.map((v) => (
        <VersionCard key={v.id} movieId={m.id} version={v} />
      ))}
      <BlockedReleases titleId={m.id} />
      <MovieHistory movieId={m.id} />
      <RenameDialog movieId={m.id} open={renaming} onOpenChange={setRenaming} />
      <DeleteMovieDialog movie={m} open={deleting} onOpenChange={setDeleting} />
    </div>
  );
}

function ReleaseDates({ m }: { m: MovieDetail }) {
  const dates = [
    ["In cinemas", m.theatricalRelease],
    ["Digital", m.digitalRelease],
    ["Disc", m.physicalRelease],
  ].filter(([, d]) => d);
  if (dates.length === 0) return null;
  return (
    <dl className="flex flex-wrap gap-x-5 gap-y-1 text-xs">
      {dates.map(([label, d]) => (
        <div key={label} className="flex gap-1">
          <dt className="text-muted-foreground">{label}:</dt>
          <dd>{new Date(`${d}T00:00:00`).toLocaleDateString()}</dd>
        </div>
      ))}
    </dl>
  );
}

const UPGRADES_OFF_HELP =
  "Off: Reelkeeper keeps this file and never looks for a better copy. A missing file is still downloaded again.";

function VersionCard({
  movieId,
  version: v,
}: {
  movieId: number;
  version: MovieDetail["versions"][number];
}) {
  const invalidate = useInvalidateLibrary();
  const qc = useQueryClient();
  const [error, setError] = useState<string>();
  const [searched, setSearched] = useState(false);
  const search = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/movies/{id}/versions/{versionId}/search", {
          params: { path: { id: movieId, versionId: v.id } },
        }),
      ),
    onSuccess: () => {
      setSearched(true);
      setError(undefined);
      // The search runs in the background; its History entry appears shortly.
      for (const delay of [3000, 10_000, 30_000]) {
        setTimeout(() => qc.invalidateQueries({ queryKey: ["history"] }), delay);
      }
    },
    onError: (e) => setError(e.message),
  });
  const monitor = useMutation({
    mutationFn: (monitored: boolean) =>
      unwrap(
        api.PATCH("/movies/{id}/versions/{versionId}", {
          params: { path: { id: movieId, versionId: v.id } },
          body: { monitored },
        }),
      ),
    onSuccess: invalidate,
    onError: (e) => setError(e.message),
  });
  const upgrades = useMutation({
    mutationFn: (upgrades: boolean) =>
      unwrap(
        api.PATCH("/movies/{id}/versions/{versionId}", {
          params: { path: { id: movieId, versionId: v.id } },
          body: { upgrades },
        }),
      ),
    onSuccess: invalidate,
    onError: (e) => setError(e.message),
  });
  const remove = useMutation({
    mutationFn: () =>
      unwrap(
        api.DELETE("/movies/{id}/versions/{versionId}", {
          params: { path: { id: movieId, versionId: v.id } },
        }),
      ),
    onSuccess: invalidate,
    onError: (e) => setError(e.message),
  });

  return (
    <Card className="animate-rise">
      <CardHeader className="flex flex-row flex-wrap items-center gap-3">
        <CardTitle className="mr-auto">
          {v.profileName}
          {qualityLabel(v) && (
            <span className="ml-2 text-sm font-normal text-muted-foreground">
              {qualityLabel(v)}
            </span>
          )}
        </CardTitle>
        <Button
          variant="outline"
          size="sm"
          disabled={search.isPending}
          onClick={() => {
            setSearched(false);
            search.mutate();
          }}
        >
          <Search /> {search.isPending ? "Searching…" : "Search now"}
        </Button>
        <label className="flex items-center gap-2 text-sm" htmlFor={`mon-${v.id}`}>
          Monitored
          <Switch
            id={`mon-${v.id}`}
            checked={v.monitored}
            onCheckedChange={(c) => monitor.mutate(c)}
          />
        </label>
        <label
          className="flex items-center gap-2 text-sm"
          htmlFor={`upg-${v.id}`}
          title={UPGRADES_OFF_HELP}
        >
          Upgrades
          <Switch
            id={`upg-${v.id}`}
            checked={v.upgrades}
            onCheckedChange={(c) => upgrades.mutate(c)}
          />
        </label>
      </CardHeader>
      <CardContent>
        <p className="font-mono text-xs break-all text-muted-foreground">
          {v.rootPath}/{v.folder}
        </p>
        {!v.upgrades && <p className="text-sm text-muted-foreground">{UPGRADES_OFF_HELP}</p>}
        {error && <Alert variant="error">{error}</Alert>}
        {searched && (
          <p role="status" className="text-sm text-muted-foreground">
            Searching… results will appear in History below.
          </p>
        )}
        {v.mediaFiles.length === 0 ? (
          <div className="flex flex-wrap items-center gap-3">
            <p className="text-sm text-muted-foreground">Not on disk yet.</p>
            <Button variant="ghost" size="sm" className="ml-auto" onClick={() => remove.mutate()}>
              <Trash2 /> Remove this version
            </Button>
          </div>
        ) : (
          <ul className="space-y-3">
            {v.mediaFiles.map((f) => (
              <FileRow key={f.id} f={f} />
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

function FileRow({ f }: { f: MediaFile }) {
  const [open, setOpen] = useState(false);
  const v = f.video;
  const summary = [
    v && `${v.width}×${v.height}`,
    v?.codec,
    v?.bitDepth && `${v.bitDepth}-bit`,
    ...(v?.hdr ?? []).map((h) => (h === "DV" && v?.dvProfile ? `DV (profile ${v.dvProfile})` : h)),
    f.audio[0] && `${f.audio[0].codec} ${channelLayout(f.audio[0].channels)}`.trim(),
  ].filter(Boolean);

  return (
    <li className="space-y-2 rounded-lg border bg-background p-3.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 flex-1 text-sm font-medium break-all">
          {f.path.split("/").pop()}
        </span>
        {f.missingSince && (
          <Badge variant="warning">
            <CircleAlert className="size-3" /> Missing since{" "}
            {new Date(f.missingSince).toLocaleDateString()}
          </Badge>
        )}
        <Badge>{f.quality || "Unknown quality"}</Badge>
        {f.edition && <Badge>{f.edition}</Badge>}
        <span className="text-xs text-muted-foreground">{formatBytes(f.size)}</span>
      </div>
      <p className="text-xs text-muted-foreground">
        {summary.join(" · ") || "File details couldn't be read."}
        {f.durationSeconds > 0 && ` · ${formatRuntime(Math.round(f.durationSeconds / 60))}`}
        {f.releaseGroup && ` · ${f.releaseGroup}`}
      </p>
      {f.probeError && (
        <p className="text-xs text-warning">Couldn't read the file's details: {f.probeError}</p>
      )}
      {(f.audio.length > 0 || f.subtitles.length > 0) && (
        <Button variant="link" size="sm" className="h-auto px-0" onClick={() => setOpen(!open)}>
          {open
            ? "Hide tracks"
            : `Show ${f.audio.length} audio and ${f.subtitles.length} subtitle tracks`}
        </Button>
      )}
      {open && (
        <div className="grid gap-3 text-xs sm:grid-cols-2">
          <div>
            <p className="mb-1 font-medium">Audio</p>
            <ul className="space-y-0.5 text-muted-foreground">
              {f.audio.map((a, i) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: tracks have no ID and never reorder
                <li key={i}>
                  {[
                    languageName(a.language) || "Unknown language",
                    a.codec,
                    channelLayout(a.channels),
                    a.title,
                    a.default && "default",
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                </li>
              ))}
            </ul>
          </div>
          <div>
            <p className="mb-1 font-medium">Subtitles</p>
            <ul className="space-y-0.5 text-muted-foreground">
              {f.subtitles.length === 0 && <li>None</li>}
              {f.subtitles.map((s, i) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: tracks have no ID and never reorder
                <li key={i}>
                  {[
                    languageName(s.language) || "Unknown language",
                    s.codec,
                    s.forced && "forced",
                    s.title,
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                </li>
              ))}
            </ul>
          </div>
        </div>
      )}
      <p className="font-mono text-[11px] break-all text-muted-foreground/80">{f.path}</p>
    </li>
  );
}

/** This movie's latest searches and grabs. */
function MovieHistory({ movieId }: { movieId: number }) {
  const { data } = useQuery(historyQuery({ titleId: movieId }, 10));
  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-center gap-3">
        <CardTitle className="mr-auto">History</CardTitle>
        <Link to="/activity/history" className="text-sm text-link hover:underline">
          See all in Activity → History
        </Link>
      </CardHeader>
      <CardContent className="px-0">
        {!data ? (
          <p className="px-4 text-sm text-muted-foreground">Loading…</p>
        ) : data.events.length === 0 ? (
          <p className="px-4 text-sm text-muted-foreground">
            No searches yet. Press "Search now" on a version to look for it.
          </p>
        ) : (
          <ul>
            {data.events.map((e) => (
              <HistoryRow key={e.id} event={e} showTitle={false} />
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
