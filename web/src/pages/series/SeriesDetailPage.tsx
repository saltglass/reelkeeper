import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import {
  ArrowLeft,
  Bookmark,
  BookmarkCheck,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  ExternalLink,
  FilePen,
  ListOrdered,
  RefreshCw,
  Search,
  Trash2,
  TriangleAlert,
} from "lucide-react";
import { type ReactNode, useState } from "react";
import { api, unwrap } from "@/api/client";
import { BlockedReleases } from "@/components/BlockedReleases";
import { QualityLabel } from "@/components/QualityLabel";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Progress } from "@/components/ui/progress";
import { Segmented } from "@/components/ui/segmented";
import { Switch } from "@/components/ui/switch";
import {
  type EpisodeDetail,
  type MediaFile,
  type SeriesDetail,
  seriesQuery,
  useInvalidateLibrary,
} from "@/hooks/library";
import { labelSettingsQuery } from "@/hooks/settings";
import {
  defaultLabelParts,
  formatBytes,
  formatRuntime,
  qualityLabel,
  versionLabel,
} from "@/lib/format";
import { tmdbImage } from "@/lib/images";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { riseStyle } from "@/pages/activity/shared";
import { RenameDialog } from "@/pages/library/RenameDialog";
import { AddToBackfillButton } from "./AddToBackfillButton";
import { ChangeOrderDialog } from "./ChangeOrderDialog";
import {
  codeOf,
  type EpisodeState,
  type FileInfo,
  fileName,
  filesById,
  filesWithNotes,
  formatDay,
  hasAired,
  stateSummary,
  statesById,
  todayISO,
  unplacedFiles,
  type VersionDetail,
} from "./episodes";
import { progressText } from "./progress";

export function SeriesDetailPage() {
  const { id } = useParams({ from: "/app/series/$id" });
  const seriesId = Number(id);
  const { data: s, error } = useQuery(seriesQuery(seriesId));
  const invalidate = useInvalidateLibrary();
  const [renaming, setRenaming] = useState(false);
  const [ordering, setOrdering] = useState(false);
  const refresh = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/series/{id}/refresh", { params: { path: { id: seriesId } } })),
    onSuccess: invalidate,
  });

  if (error) return <Alert variant="error">Couldn't load the series: {error.message}</Alert>;
  if (!s) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const facts = [
    s.year > 0 && String(s.year),
    s.network,
    s.status,
    s.certification,
    formatRuntime(s.runtime) && `${formatRuntime(s.runtime)} episodes`,
    s.genres.join(", "),
  ].filter(Boolean);
  const hasFiles = s.versions.some((v) => v.mediaFiles.length > 0);
  const notes = filesWithNotes(s);
  const unplaced = unplacedFiles(s);

  return (
    <div className="space-y-6">
      <Link
        to="/series"
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="size-4" /> Series
      </Link>

      <div className="animate-rise relative overflow-hidden rounded-xl border bg-card shadow-md">
        {s.backdropPath && (
          <img
            src={tmdbImage(s.backdropPath, "w1280")}
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
            {s.posterPath && (
              <img
                src={tmdbImage(s.posterPath, "w342")}
                alt=""
                className="size-full object-cover"
              />
            )}
          </div>
          <div className="min-w-0 space-y-3">
            <div>
              <h1 className="text-2xl font-semibold">{s.title}</h1>
              {s.originalTitle && s.originalTitle !== s.title && (
                <p className="text-sm text-muted-foreground">{s.originalTitle}</p>
              )}
            </div>
            <p className="text-sm text-muted-foreground">{facts.join(" · ")}</p>
            {s.overview && <p className="max-w-prose text-sm leading-relaxed">{s.overview}</p>}
            <div className="flex flex-wrap items-center gap-2 pt-1">
              <Button variant="outline" size="sm" asChild>
                <a
                  href={`https://www.themoviedb.org/tv/${s.tmdbId}`}
                  target="_blank"
                  rel="noreferrer"
                >
                  TMDB <ExternalLink />
                </a>
              </Button>
              {s.imdbId && (
                <Button variant="outline" size="sm" asChild>
                  <a
                    href={`https://www.imdb.com/title/${s.imdbId}`}
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
              {hasFiles && (
                <Button variant="outline" size="sm" onClick={() => setRenaming(true)}>
                  <FilePen /> Rename files
                </Button>
              )}
              {s.metadataUpdatedAt && (
                <span className="text-xs text-muted-foreground">
                  Details from TMDB, {relativeTime(s.metadataUpdatedAt)}
                </span>
              )}
            </div>
            {refresh.error && <Alert variant="error">{refresh.error.message}</Alert>}
          </div>
        </div>
      </div>

      {s.versions.map((v, i) => (
        <VersionCard
          key={v.id}
          index={i}
          seriesId={s.id}
          seriesTitle={s.title}
          version={v}
          airedEpisodes={s.airedEpisodes}
          onlyVersion={s.versions.length === 1}
        />
      ))}

      {notes.length > 0 && (
        <Card className="animate-rise border-warning/40">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <TriangleAlert className="size-4 text-warning" /> Needs attention
            </CardTitle>
            <p className="text-sm text-muted-foreground">
              Reelkeeper isn't sure which episode these files are. Nothing has been renamed or
              changed. Often the files follow a different episode order: try{" "}
              <button
                type="button"
                className="text-link underline"
                onClick={() => setOrdering(true)}
              >
                changing the order
              </button>
              .
            </p>
          </CardHeader>
          <CardContent>
            <ul className="space-y-2">
              {notes.map((i) => (
                <li
                  key={i.file.id}
                  className="space-y-0.5 rounded-lg border border-warning/30 bg-warning/5 p-2.5"
                >
                  <p className="text-sm font-medium break-all">{fileName(i.file)}</p>
                  <p className="text-xs text-warning">{i.file.matchNote}</p>
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}

      <div className="animate-rise flex flex-wrap items-center gap-3 rounded-lg border bg-card p-3 shadow-xs">
        <ListOrdered className="size-4 text-muted-foreground" />
        <p className="mr-auto text-sm">
          <span className="text-muted-foreground">Episode order: </span>
          <span className="font-medium">
            {s.episodeOrder === "" ? "TMDB's aired order" : s.episodeOrderName || s.episodeOrder}
          </span>
        </p>
        <Button variant="outline" size="sm" onClick={() => setOrdering(true)}>
          Change order
        </Button>
      </div>

      <Episodes series={s} />

      {unplaced.length > 0 && <Unplaced files={unplaced} />}

      <BlockedReleases titleId={s.id} />

      <Advanced series={s} />

      <RenameDialog kind="series" movieId={s.id} open={renaming} onOpenChange={setRenaming} />
      <ChangeOrderDialog seriesId={s.id} open={ordering} onOpenChange={setOrdering} />
    </div>
  );
}

function VersionCard({
  index,
  seriesId,
  seriesTitle,
  version: v,
  airedEpisodes,
  onlyVersion,
}: {
  index: number;
  seriesId: number;
  seriesTitle: string;
  version: VersionDetail;
  airedEpisodes: number;
  onlyVersion: boolean;
}) {
  const invalidate = useInvalidateLibrary();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [error, setError] = useState<string>();
  const [searched, setSearched] = useState(false);
  const search = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/series/{id}/versions/{versionId}/search", {
          params: { path: { id: seriesId, versionId: v.id } },
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
  const preset = useMutation({
    mutationFn: (monitor: "all" | "future" | "none") =>
      unwrap(
        api.PATCH("/series/{id}/versions/{versionId}", {
          params: { path: { id: seriesId, versionId: v.id } },
          body: { monitor },
        }),
      ),
    onSuccess: () => {
      setError(undefined);
      invalidate();
    },
    onError: (e) => setError(e.message),
  });
  const summary = stateSummary(v.episodes);
  const monitor = useMutation({
    mutationFn: (monitored: boolean) =>
      unwrap(
        api.PATCH("/series/{id}/versions/{versionId}", {
          params: { path: { id: seriesId, versionId: v.id } },
          body: { monitored },
        }),
      ),
    onSuccess: invalidate,
    onError: (e) => setError(e.message),
  });
  const remove = useMutation({
    mutationFn: () =>
      unwrap(
        api.DELETE("/series/{id}/versions/{versionId}", {
          params: { path: { id: seriesId, versionId: v.id } },
        }),
      ),
    onSuccess: () => {
      invalidate();
      // Removing the last version removes the series itself.
      if (onlyVersion) navigate({ to: "/series" });
    },
    onError: (e) => setError(e.message),
  });

  return (
    <Card className="animate-rise" style={riseStyle(index + 1)}>
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
        <AddToBackfillButton
          versionId={v.id}
          title={seriesTitle}
          hasMissing={v.monitored && airedEpisodes > v.episodesWithFiles}
        />
        <label className="flex items-center gap-2 text-sm" htmlFor={`mon-${v.id}`}>
          Monitored
          <Switch
            id={`mon-${v.id}`}
            checked={v.monitored}
            onCheckedChange={(c) => monitor.mutate(c)}
          />
        </label>
      </CardHeader>
      <CardContent>
        <p className="font-mono text-xs break-all text-muted-foreground">
          {v.rootPath}/{v.folder}
        </p>
        <p className="text-sm">
          {progressText(v.episodesWithFiles, airedEpisodes)}
          <span className="text-muted-foreground">
            {" · "}
            {v.files} {v.files === 1 ? "file" : "files"}
            {v.size > 0 && ` · ${formatBytes(v.size)}`}
          </span>
        </p>
        {airedEpisodes > 0 && (
          <Progress
            value={Math.round((Math.min(v.episodesWithFiles, airedEpisodes) / airedEpisodes) * 100)}
            label={`${v.profileName} episodes on disk`}
          />
        )}
        {summary && <p className="text-sm font-medium">{summary}</p>}
        {v.monitor && (
          <div className="space-y-1.5">
            <p className="text-sm font-medium">Monitor</p>
            <Segmented
              label={`Monitor, ${v.profileName}`}
              value={v.monitor}
              options={[
                { value: "all", label: "All" },
                { value: "future", label: "New episodes" },
                { value: "none", label: "None" },
              ]}
              onChange={(m) => m !== v.monitor && !preset.isPending && preset.mutate(m)}
            />
            <p className="text-sm text-muted-foreground">
              {v.monitor === "all" &&
                "Every episode, aired or not. Specials only when you turn them on."}
              {v.monitor === "future" &&
                (v.monitorFrom
                  ? `Episodes airing from ${formatDay(v.monitorFrom)}.`
                  : "Episodes that haven't aired yet.")}
              {v.monitor === "none" && "Nothing until you turn on a season or episode."}
            </p>
            <p className="text-xs text-muted-foreground">
              Changing this replaces any choices you made for single seasons or episodes.
            </p>
          </div>
        )}
        {searched && (
          <p role="status" className="text-sm text-muted-foreground">
            Searching… results will appear in Activity → History.
          </p>
        )}
        {v.missing > 0 && (
          <Badge variant="warning" className="w-fit">
            <CircleAlert className="size-3" /> {v.missing}{" "}
            {v.missing === 1 ? "file is" : "files are"} missing from the disk
          </Badge>
        )}
        {error && <Alert variant="error">{error}</Alert>}
        {v.mediaFiles.length === 0 && (
          <div className="flex flex-wrap items-center gap-3">
            <p className="text-sm text-muted-foreground">Not on disk yet.</p>
            <Button
              variant="ghost"
              size="sm"
              className="ml-auto"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              <Trash2 /> Remove this version
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

/** Seasons as collapsible sections, plus episodes the chosen order leaves out. */
function Episodes({ series: s }: { series: SeriesDetail }) {
  const today = todayISO();
  const files = filesById(s);
  // A file holding several episodes is mentioned on each of them.
  const heldBy = new Map<number, EpisodeDetail[]>();
  for (const e of [...s.seasonList.flatMap((x) => x.episodes), ...s.unordered])
    for (const id of e.fileIds) heldBy.set(id, [...(heldBy.get(id) ?? []), e]);

  // Open the most recent regular season that has aired something, else the first.
  const regular = s.seasonList.filter((x) => x.number > 0);
  const current =
    [...regular].reverse().find((x) => x.episodes.some((e) => hasAired(e, today)))?.number ??
    regular[0]?.number ??
    s.seasonList[0]?.number;

  const [pickedId, setPickedId] = useState<number>();
  const qc = useQueryClient();
  const [error, setError] = useState<string>();
  const version = s.versions.find((v) => v.id === pickedId) ?? s.versions[0];
  const states = statesById(version);
  const setMonitored = useMutation({
    mutationFn: (body: { seasons?: number[]; episodeIds?: number[]; monitored: boolean }) =>
      unwrap(
        api.POST("/series/{id}/versions/{versionId}/monitor", {
          params: { path: { id: s.id, versionId: version?.id ?? 0 } },
          body,
        }),
      ),
    onSuccess: () => {
      setError(undefined);
      qc.invalidateQueries({ queryKey: ["series"] });
    },
    onError: (e) => setError(e.message),
  });
  const monitoring = version?.episodes
    ? {
        states,
        pending: setMonitored.isPending,
        episode: (id: number, monitored: boolean) =>
          setMonitored.mutate({ episodeIds: [id], monitored }),
      }
    : undefined;

  if (s.seasonList.length === 0 && s.unordered.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        TMDB doesn't list any episodes for this series yet. Try "Refresh details".
      </p>
    );
  }

  return (
    <section className="space-y-3" aria-label="Episodes">
      <h2 className="text-lg font-semibold">Episodes</h2>
      {s.versions.length > 1 && (
        <Segmented
          label="Show episodes for"
          value={String(version?.id)}
          options={s.versions.map((v) => ({ value: String(v.id), label: v.profileName }))}
          onChange={(id) => setPickedId(Number(id))}
        />
      )}
      {error && <Alert variant="error">{error}</Alert>}
      {s.seasonList.map((season) => (
        <SeasonSection
          key={season.number}
          title={season.number === 0 ? "Specials" : `Season ${season.number}`}
          episodes={season.episodes}
          files={files}
          today={today}
          defaultOpen={season.number === current}
          seasonToggle={
            monitoring && {
              label: season.number === 0 ? "Specials" : `season ${season.number}`,
              on:
                season.episodes.length > 0 &&
                season.episodes.every((e) => monitoring.states.get(e.id)?.monitored),
              pending: monitoring.pending,
              onChange: (monitored) => setMonitored.mutate({ seasons: [season.number], monitored }),
            }
          }
        >
          <EpisodeList
            episodes={season.episodes}
            files={files}
            heldBy={heldBy}
            today={today}
            monitoring={monitoring}
          />
        </SeasonSection>
      ))}
      {s.unordered.length > 0 && (
        <SeasonSection
          title="Not in this episode order"
          note="TMDB has these episodes, but the order you chose leaves them out."
          episodes={s.unordered}
          files={files}
          today={today}
          defaultOpen={false}
          hideProgress
        >
          <EpisodeList
            episodes={s.unordered}
            files={files}
            heldBy={heldBy}
            today={today}
            monitoring={monitoring}
          />
        </SeasonSection>
      )}
    </section>
  );
}

const hasPresentFile = (e: EpisodeDetail, files: Map<number, FileInfo>) =>
  e.fileIds.some((id) => files.get(id) && !files.get(id)?.file.missingSince);

function SeasonSection({
  title,
  note,
  episodes,
  files,
  today,
  defaultOpen,
  hideProgress,
  seasonToggle,
  children,
}: {
  title: string;
  note?: string;
  episodes: EpisodeDetail[];
  files: Map<number, FileInfo>;
  today: string;
  defaultOpen: boolean;
  hideProgress?: boolean;
  seasonToggle?: {
    label: string;
    on: boolean;
    pending: boolean;
    onChange: (monitored: boolean) => void;
  };
  children: ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const aired = episodes.filter((e) => hasAired(e, today));
  // "Have" counts episodes (aired or not) with at least one file that's on
  // disk; a file marked missing doesn't count, as in the version's count.
  const have = episodes.filter((e) => hasPresentFile(e, files)).length;
  const Icon = open ? ChevronDown : ChevronRight;
  return (
    <div className="overflow-hidden rounded-lg border bg-card shadow-xs">
      <div className="flex items-center transition-colors hover:bg-muted/50">
        <button
          type="button"
          aria-expanded={open}
          className="flex min-w-0 flex-1 cursor-pointer items-center gap-2 px-3 py-3 text-left"
          onClick={() => setOpen(!open)}
        >
          <Icon className="size-4 shrink-0 text-muted-foreground" />
          <span className="mr-auto font-medium">{title}</span>
          <span
            className={cn(
              "rounded-full border px-2.5 py-0.5 text-xs tabular-nums",
              !hideProgress && have > 0 && have >= aired.length && aired.length > 0
                ? "border-success/40 bg-success/10 text-success"
                : "text-muted-foreground",
            )}
          >
            {hideProgress
              ? `${episodes.length} ${episodes.length === 1 ? "episode" : "episodes"}`
              : `${have} of ${aired.length > 0 ? aired.length : episodes.length} ${episodes.length === 1 ? "episode" : "episodes"}`}
          </span>
        </button>
        {seasonToggle && (
          <MonitorToggle
            on={seasonToggle.on}
            disabled={seasonToggle.pending}
            code={seasonToggle.label}
            className="mr-2"
            onChange={seasonToggle.onChange}
          />
        )}
      </div>
      {open && (
        <div className="animate-rise border-t">
          {note && <p className="px-3 pt-2 text-xs text-muted-foreground">{note}</p>}
          {children}
        </div>
      )}
    </div>
  );
}

function EpisodeList({
  episodes,
  files,
  heldBy,
  today,
  monitoring,
}: {
  episodes: EpisodeDetail[];
  files: Map<number, FileInfo>;
  heldBy: Map<number, EpisodeDetail[]>;
  today: string;
  monitoring?: {
    states: Map<number, EpisodeState>;
    pending: boolean;
    episode: (id: number, monitored: boolean) => void;
  };
}) {
  return (
    <ul className="divide-y">
      {episodes.map((e) => {
        const present = hasPresentFile(e, files);
        const st = monitoring?.states.get(e.id);
        return (
          <li key={e.id} className="space-y-1.5 px-3 py-2.5 transition-colors hover:bg-muted/40">
            <div className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5">
              <span className="font-mono text-xs text-muted-foreground">{codeOf(e)}</span>
              <span className={cn("min-w-0 flex-1 text-sm font-medium", !present && "font-normal")}>
                {e.title || "Untitled"}
              </span>
              <span className="text-xs text-muted-foreground">
                {e.airDate === ""
                  ? "Not aired yet"
                  : hasAired(e, today)
                    ? new Date(`${e.airDate}T00:00:00`).toLocaleDateString()
                    : `Airs ${new Date(`${e.airDate}T00:00:00`).toLocaleDateString()}`}
              </span>
              {st && <StateBadge state={st.state} />}
              {monitoring && (
                <MonitorToggle
                  on={Boolean(st?.monitored)}
                  disabled={monitoring.pending}
                  code={codeOf(e)}
                  onChange={(m) => monitoring.episode(e.id, m)}
                />
              )}
            </div>
            {e.fileIds.length === 0 ? (
              <p className="text-xs text-muted-foreground">No file</p>
            ) : (
              <ul className="space-y-1">
                {e.fileIds.map((id) => {
                  const info = files.get(id);
                  if (!info) return null;
                  const others = (heldBy.get(id) ?? []).filter((x) => x.id !== e.id);
                  return <EpisodeFile key={id} info={info} also={others} />;
                })}
              </ul>
            )}
          </li>
        );
      })}
    </ul>
  );
}

/** Downloading, Wanted and Upcoming get a badge; Unmonitored is muted text. */
function StateBadge({ state }: { state: EpisodeState["state"] }) {
  if (state === "downloading") return <Badge variant="info">Downloading</Badge>;
  if (state === "wanted") return <Badge variant="warning">Wanted</Badge>;
  if (state === "not-aired") return <Badge>Upcoming</Badge>;
  if (state === "unmonitored")
    return <span className="text-xs text-muted-foreground/70">Not monitored</span>;
  return null;
}

/** A bookmark button that turns monitoring of an episode or season on and off. */
function MonitorToggle({
  on,
  code,
  disabled,
  className,
  onChange,
}: {
  on: boolean;
  /** What it controls, for the screen-reader label, e.g. "S02E05" or "season 2". */
  code: string;
  disabled?: boolean;
  className?: string;
  onChange: (monitored: boolean) => void;
}) {
  const Icon = on ? BookmarkCheck : Bookmark;
  return (
    <button
      type="button"
      aria-label={on ? `Stop monitoring ${code}` : `Monitor ${code}`}
      aria-pressed={on}
      title={on ? "Monitored" : "Not monitored"}
      disabled={disabled}
      onClick={() => onChange(!on)}
      className={cn(
        "inline-flex size-8 shrink-0 cursor-pointer items-center justify-center rounded-md hover:bg-muted disabled:opacity-50 [&_svg]:size-4",
        on ? "text-primary" : "text-muted-foreground",
        className,
      )}
    >
      <Icon />
    </button>
  );
}

/** Label facts for a single file, so labels read like the version labels. */
function fileFacts(f: MediaFile, profileName: string) {
  return {
    profileName,
    quality: f.quality,
    resolution: f.resolution,
    hdr: f.video?.hdr ?? [],
    videoCodec: f.video?.codec ?? "",
    audioCodec: f.audio[0]?.codec ?? "",
    audioChannels: f.audio[0]?.channels ?? 0,
    releaseGroup: f.releaseGroup,
    edition: f.edition,
    size: f.size,
  };
}

function EpisodeFile({ info, also }: { info: FileInfo; also: EpisodeDetail[] }) {
  const { file: f } = info;
  const labels = useQuery(labelSettingsQuery);
  const parts = labels.data?.parts ?? (labels.isError ? defaultLabelParts : undefined);
  // The profile is left out: the card above already says which version this is.
  const pieces = parts
    ? versionLabel(
        fileFacts(f, info.profileName),
        parts.filter((p) => p !== "profile" && p !== "size"),
      )
    : [];
  return (
    <li className="space-y-0.5 text-xs">
      <div className="flex flex-wrap items-center gap-2">
        {pieces.length > 0 && <QualityLabel variant="info">{pieces.join(" · ")}</QualityLabel>}
        <span className="text-muted-foreground">{formatBytes(f.size)}</span>
        {f.missingSince && (
          <Badge variant="warning">
            <CircleAlert className="size-3" /> Missing
          </Badge>
        )}
        {f.matchNote && (
          <TriangleAlert className="size-4 shrink-0 text-warning" aria-label="Needs attention" />
        )}
        {also.length > 0 && (
          <span className="text-muted-foreground">also holds {also.map(codeOf).join(", ")}</span>
        )}
        <span className="min-w-0 break-all text-muted-foreground/80">{fileName(f)}</span>
      </div>
      {f.matchNote && <p className="text-warning">{f.matchNote}</p>}
    </li>
  );
}

function Unplaced({ files }: { files: FileInfo[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Files Reelkeeper couldn't place</CardTitle>
        <p className="text-sm text-muted-foreground">
          These files are in this series' folder, but no episode matches them. They stay where they
          are and won't be renamed.
        </p>
      </CardHeader>
      <CardContent>
        <ul className="space-y-2">
          {files.map((i) => (
            <li
              key={i.file.id}
              className="space-y-0.5 rounded-lg border border-warning/30 bg-warning/5 p-2.5"
            >
              <p className="text-sm font-medium break-all">{fileName(i.file)}</p>
              <p className="text-xs text-warning">
                {i.file.matchNote || "Reelkeeper couldn't tell which episode this is."}
              </p>
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  );
}

/** Rarely needed settings, out of the way. */
function Advanced({ series: s }: { series: SeriesDetail }) {
  const [open, setOpen] = useState(false);
  const invalidate = useInvalidateLibrary();
  const save = useMutation({
    mutationFn: (seriesType: "standard" | "daily") =>
      unwrap(api.PATCH("/series/{id}", { params: { path: { id: s.id } }, body: { seriesType } })),
    onSuccess: invalidate,
  });
  return (
    <div>
      <Button
        variant="link"
        size="sm"
        className="h-auto px-0"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        {open ? "Hide advanced" : "Show advanced"}
      </Button>
      {open && (
        <div className="animate-rise mt-2 space-y-3 rounded-lg border bg-card p-4 shadow-xs">
          {save.error && <Alert variant="error">{save.error.message}</Alert>}
          <Field
            id="series-type"
            label="Series type"
            help="Standard shows are named by season and episode (S01E02). Choose Daily for talk and news shows that air every day and are named by air date. Changing this doesn't rename anything by itself."
          >
            <select
              id="series-type"
              className="w-full max-w-xs min-h-10 rounded-lg border border-input bg-background px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none"
              value={s.seriesType}
              disabled={save.isPending}
              onChange={(e) => save.mutate(e.target.value as "standard" | "daily")}
            >
              <option value="standard">Standard (season and episode)</option>
              <option value="daily">Daily (by air date)</option>
            </select>
          </Field>
        </div>
      )}
    </div>
  );
}
