import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { api, RequestError, unwrap } from "@/api/client";
import type { components } from "@/api/schema";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";

type Naming = components["schemas"]["NamingSettings"];
type Style = components["schemas"]["MultiEpisodeStyle"];

const query = {
  queryKey: ["settings", "naming"],
  queryFn: () => unwrap(api.GET("/settings/naming")),
};

/** Ready-made schemes. TRaSH's is pasted unchanged from their Radarr guide. */
const presets = [
  {
    name: "Reelkeeper default",
    movieFolder: "{Movie Title} ({Release Year})",
    movieFile:
      "{Movie Title} ({Release Year}) {[Edition Tags]}{[Quality Full]}{[MediaInfo VideoDynamicRangeType]}{-Release Group}",
  },
  {
    name: "Simple",
    movieFolder: "{Movie Title} ({Release Year})",
    movieFile: "{Movie Title} ({Release Year})",
  },
  {
    name: "TRaSH Guides (Emby/Jellyfin/Plex)",
    movieFolder: "{Movie CleanTitle} ({Release Year}) {tmdb-{TmdbId}}",
    movieFile:
      "{Movie CleanTitle} {(Release Year)} {tmdb-{TmdbId}} {edition-{Edition Tags}} {[Custom Formats]}{[Quality Full]}{[MediaInfo 3D]}{[MediaInfo VideoDynamicRangeType]}{[Mediainfo AudioCodec}{ Mediainfo AudioChannels]}{[Mediainfo VideoCodec]}{-Release Group}",
  },
];

/** Ready-made TV schemes (the movie ones above are separate, so each can be picked alone). */
const episodeQuality =
  "{[Quality Full]}{[MediaInfo VideoDynamicRangeType]}[{MediaInfo AudioCodec}{ MediaInfo AudioChannels}]{[MediaInfo VideoCodec]}{-Release Group}";
const tvPresets: { name: string; values: Omit<Naming, "movieFolder" | "movieFile"> }[] = [
  {
    // Must match naming.Defaults() in internal/naming: the names Sonarr
    // gives with TRaSH Guides' scheme, with the TVDB ID in the folder.
    name: "Reelkeeper default (Sonarr with TRaSH Guides' scheme)",
    values: {
      seriesFolder: "{Series TitleYear} {tvdb-{TvdbId}}",
      seasonFolder: "Season {season}",
      episodeFile: `{Series TitleYear} - S{season:00}E{episode:00} - {Episode CleanTitle} ${episodeQuality}`,
      dailyEpisodeFile: `{Series TitleYear} - {Air-Date} - {Episode CleanTitle} ${episodeQuality}`,
      multiEpisodeStyle: "prefixed-range",
    },
  },
  {
    name: "Simple",
    values: {
      seriesFolder: "{Series Title}",
      seasonFolder: "Season {season}",
      episodeFile: "{Series Title} - S{season:00}E{episode:00} - {Episode Title}",
      dailyEpisodeFile: "{Series Title} - {Air-Date} - {Episode Title}",
      multiEpisodeStyle: "prefixed-range",
    },
  },
];

/** Each style written for episodes 1 to 3. */
const multiStyles: { value: Style; name: string; example: string }[] = [
  { value: "prefixed-range", name: "Prefixed range", example: "S01E01-E03" },
  { value: "range", name: "Range", example: "S01E01-03" },
  { value: "extend", name: "Extend", example: "S01E01-02-03" },
  { value: "scene", name: "Scene", example: "S01E01-E02-E03" },
  { value: "repeat", name: "Repeat", example: "S01E01E02E03" },
  { value: "duplicate", name: "Duplicate", example: "S01E01.S01E02.S01E03" },
];

const tvTokens: [string, string][] = [
  ["{Series Title}", "Doctor Who"],
  ["{Series TitleYear}", "Doctor Who (2005)"],
  ["{Series CleanTitle}", "Title without punctuation"],
  ["{Series TitleThe}", "Office, The"],
  ["{Series Year}", "2005"],
  ["{season:00}", "Season number with two digits: 01"],
  ["{season}", "Season number: 1"],
  ["{episode:00}", "Episode number with two digits: 02"],
  ["{Episode Title}", "Rose"],
  ["{Episode CleanTitle}", "Title without punctuation"],
  ["{Air-Date}", "2005-03-26 (daily shows)"],
  ["{tvdb-{TvdbId}}", "{tvdb-78804}"],
  ["{Quality Full}", "WEBDL-1080p Proper"],
  ["{Release Group}", "NTb"],
];

const tokens: [string, string][] = [
  ["{Movie Title}", "The Dark Knight"],
  ["{Movie CleanTitle}", "Title without punctuation: Dune Part Two"],
  ["{Movie TitleThe}", "Dark Knight, The"],
  ["{Movie OriginalTitle}", "Le Fabuleux Destin d'Amélie Poulain"],
  ["{Movie TitleFirstCharacter}", "D (for A–Z folders)"],
  ["{Movie Collection}", "The Dark Knight Collection"],
  ["{Movie Certification}", "PG-13"],
  ["{Release Year}", "2008"],
  ["{TmdbId} / {tmdb-{TmdbId}}", "155 / {tmdb-155}"],
  ["{ImdbId} / {imdb-{ImdbId}}", "tt0468569 / {imdb-tt0468569}"],
  ["{Profile Name}", "The version: HD, Remux"],
  ["{Edition Tags} / {edition-{Edition Tags}}", "IMAX / {edition-IMAX}"],
  ["{Quality Full}", "Bluray-1080p Proper"],
  ["{Quality Title}", "Bluray-1080p"],
  ["{MediaInfo VideoCodec}", "x265, h264"],
  ["{MediaInfo VideoBitDepth}", "10"],
  ["{MediaInfo VideoDynamicRange}", "HDR"],
  ["{MediaInfo VideoDynamicRangeType}", "DV HDR10"],
  ["{MediaInfo AudioCodec}", "TrueHD Atmos"],
  ["{MediaInfo AudioChannels}", "7.1"],
  ["{Release Group}", "FraMeSToR"],
];

/** Folder and file name templates with a live preview (Settings → Media Folders). */
export function NamingCard() {
  const { data } = useQuery(query);
  const qc = useQueryClient();
  const [form, setForm] = useState<Naming | null>(null);
  const [showTokens, setShowTokens] = useState(false);
  useEffect(() => {
    if (data) setForm(data);
  }, [data]);
  const set = (patch: Partial<Naming>) => setForm((f) => (f ? { ...f, ...patch } : f));

  // Preview the unsaved templates shortly after typing stops.
  const [debounced, setDebounced] = useState<Naming | null>(null);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(form), 300);
    return () => clearTimeout(t);
  }, [form]);
  const preview = useQuery({
    queryKey: ["settings", "naming", "preview", debounced],
    queryFn: () => unwrap(api.POST("/settings/naming/preview", { body: debounced as Naming })),
    enabled: debounced !== null,
    placeholderData: (prev) => prev,
  });

  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/settings/naming", { body: form as Naming })),
    onSuccess: (d) => qc.setQueryData(query.queryKey, d),
  });
  const err = save.error instanceof RequestError ? save.error : undefined;
  const dirty = Boolean(data && form && JSON.stringify(data) !== JSON.stringify(form));
  const problems = preview.data?.problems ?? {};
  const fieldError = (k: keyof Naming) => err?.fields[k] ?? problems[k];

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate();
  }

  if (!form) return null;
  const style = multiStyles.find((m) => m.value === form.multiEpisodeStyle);

  return (
    <Card>
      <CardHeader>
        <CardTitle>Naming</CardTitle>
        <CardDescription>
          How Reelkeeper names movie and TV folders and files. Used for what you add, and when you
          choose "Rename files" on a movie or series in a folder Reelkeeper manages. Saving here
          doesn't rename anything by itself.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} className="space-y-6">
          {err && !Object.keys(err.fields).length && <Alert variant="error">{err.message}</Alert>}

          <section className="space-y-5" aria-label="Movie naming">
            <h3 className="text-sm font-semibold">Movies</h3>
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <span className="text-muted-foreground">Start from:</span>
              {presets.map((p) => (
                <Button
                  key={p.name}
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => set({ movieFolder: p.movieFolder, movieFile: p.movieFile })}
                >
                  {p.name}
                </Button>
              ))}
            </div>
            <Field id="naming-folder" label="Movie folder" error={fieldError("movieFolder")}>
              <Input
                id="naming-folder"
                className="font-mono text-xs"
                value={form.movieFolder}
                onChange={(e) => set({ movieFolder: e.target.value })}
              />
            </Field>
            <Field
              id="naming-file"
              label="Movie file"
              help="Without the extension; .mkv or .mp4 stays as it is."
              error={fieldError("movieFile")}
            >
              <Input
                id="naming-file"
                className="font-mono text-xs"
                value={form.movieFile}
                onChange={(e) => set({ movieFile: e.target.value })}
              />
            </Field>
            <Preview title="Movie preview" rows={preview.data?.examples ?? []} />
          </section>

          <section className="space-y-5 border-t pt-5" aria-label="TV naming">
            <h3 className="text-sm font-semibold">TV shows</h3>
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <span className="text-muted-foreground">Start from:</span>
              {tvPresets.map((p) => (
                <Button
                  key={p.name}
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => set(p.values)}
                >
                  {p.name}
                </Button>
              ))}
            </div>
            <Field
              id="naming-series-folder"
              label="Series folder"
              help="One folder per series, such as Doctor Who (2005)."
              error={fieldError("seriesFolder")}
            >
              <Input
                id="naming-series-folder"
                className="font-mono text-xs"
                value={form.seriesFolder}
                onChange={(e) => set({ seriesFolder: e.target.value })}
              />
            </Field>
            <Field
              id="naming-season-folder"
              label="Season folder"
              help='Inside the series folder. Specials always go in "Specials".'
              error={fieldError("seasonFolder")}
            >
              <Input
                id="naming-season-folder"
                className="font-mono text-xs"
                value={form.seasonFolder}
                onChange={(e) => set({ seasonFolder: e.target.value })}
              />
            </Field>
            <Field
              id="naming-episode-file"
              label="Episode file"
              help="Without the extension. Used for standard series."
              error={fieldError("episodeFile")}
            >
              <Input
                id="naming-episode-file"
                className="font-mono text-xs"
                value={form.episodeFile}
                onChange={(e) => set({ episodeFile: e.target.value })}
              />
            </Field>
            <Field
              id="naming-daily-file"
              label="Daily episode file"
              help="For daily shows (talk and news), which are named by air date."
              error={fieldError("dailyEpisodeFile")}
            >
              <Input
                id="naming-daily-file"
                className="font-mono text-xs"
                value={form.dailyEpisodeFile}
                onChange={(e) => set({ dailyEpisodeFile: e.target.value })}
              />
            </Field>
            <Field
              id="naming-multi"
              label="Files with several episodes"
              help={`How one file that holds episodes 1 to 3 is written${style ? `: ${style.example}` : ""}. Applies where the episode file uses {episode:00}.`}
              error={fieldError("multiEpisodeStyle")}
            >
              <select
                id="naming-multi"
                className="w-full max-w-sm min-h-10 rounded-lg border border-input bg-background px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none"
                value={form.multiEpisodeStyle}
                onChange={(e) => set({ multiEpisodeStyle: e.target.value as Style })}
              >
                {multiStyles.map((m) => (
                  <option key={m.value} value={m.value}>
                    {m.name}: {m.example}
                  </option>
                ))}
              </select>
            </Field>
            <Preview title="TV preview" rows={preview.data?.episodeExamples ?? []} />
          </section>

          <div>
            <Button
              type="button"
              variant="link"
              size="sm"
              className="h-auto px-0"
              onClick={() => setShowTokens(!showTokens)}
            >
              {showTokens ? "Hide the token list" : "Show the token list"}
            </Button>
            {showTokens && (
              <div className="mt-2 space-y-4 text-xs">
                <p className="text-muted-foreground">
                  Characters just inside the braces only appear when the token has a value:{" "}
                  <code>{"{[Edition Tags]}"}</code> gives "[IMAX]" or nothing. A dot or underscore
                  in the token name is used between words: <code>{"{Movie.Title}"}</code> gives
                  "The.Dark.Knight". Empty brackets and dangling dashes are tidied away.
                </p>
                <TokenList
                  title="Movie tokens (click to add to the movie file)"
                  tokens={tokens}
                  onAdd={(t) => set({ movieFile: `${form.movieFile}${t}` })}
                />
                <TokenList
                  title="TV tokens (click to add to the episode file)"
                  tokens={tvTokens}
                  onAdd={(t) => set({ episodeFile: `${form.episodeFile}${t}` })}
                />
              </div>
            )}
          </div>

          <div className="flex justify-end">
            <Button type="submit" disabled={save.isPending || !dirty}>
              {save.isPending ? "Saving…" : save.isSuccess && !dirty ? "Saved" : "Save"}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

function Preview({
  title,
  rows,
}: {
  title: string;
  rows: { label: string; folder: string; file: string }[];
}) {
  return (
    <div className="space-y-3 rounded-md border bg-muted/40 p-3">
      <p className="text-sm font-medium">{title}</p>
      {rows.map((ex) => (
        <div key={ex.label} className="space-y-0.5">
          <p className="text-xs text-muted-foreground">{ex.label}</p>
          <p className="font-mono text-xs break-all">
            {ex.folder}/<span className="text-link">{ex.file}</span>
          </p>
        </div>
      ))}
    </div>
  );
}

function TokenList({
  title,
  tokens,
  onAdd,
}: {
  title: string;
  tokens: [string, string][];
  onAdd: (token: string) => void;
}) {
  return (
    <div className="space-y-1">
      <p className="font-medium">{title}</p>
      <dl className="grid gap-x-4 gap-y-1 sm:grid-cols-[auto_1fr]">
        {tokens.map(([t, ex]) => (
          <div key={t} className="contents">
            <dt>
              <button
                type="button"
                className="font-mono text-link hover:underline"
                onClick={() => onAdd(t.split(" / ")[0] ?? t)}
              >
                {t}
              </button>
            </dt>
            <dd className="mb-1 text-muted-foreground sm:mb-0">{ex}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}
