import type { EpisodeDetail, MediaFile, SeriesDetail } from "@/hooks/library";

export type VersionDetail = SeriesDetail["versions"][number];
export type EpisodeState = NonNullable<VersionDetail["episodes"]>[number];

const pad = (n: number) => String(n).padStart(2, "0");

/** "S01E02". Specials are season 0, so S00E01. */
export function episodeCode(season: number, number: number): string {
  return `S${pad(season)}E${pad(number)}`;
}

/** The code in the chosen episode order, or the aired one for episodes the order leaves out. */
export function codeOf(e: EpisodeDetail): string {
  return e.season !== undefined && e.number !== undefined
    ? episodeCode(e.season, e.number)
    : episodeCode(e.airedSeason, e.airedNumber);
}

export const fileName = (f: MediaFile) => f.path.split("/").pop() ?? f.path;

/** A file with the version (profile) it belongs to. */
export type FileInfo = { file: MediaFile; profileName: string; versionId: number };

/** Every file of every version, by id. */
export function filesById(s: SeriesDetail): Map<number, FileInfo> {
  const m = new Map<number, FileInfo>();
  for (const v of s.versions)
    for (const f of v.mediaFiles)
      m.set(f.id, { file: f, profileName: v.profileName, versionId: v.id });
  return m;
}

/** All episodes the page knows about, in the order and out of it. */
export const allEpisodes = (s: SeriesDetail): EpisodeDetail[] => [
  ...s.seasonList.flatMap((x) => x.episodes),
  ...s.unordered,
];

/** Files that no episode lists: Reelkeeper found them but couldn't place them. */
export function unplacedFiles(s: SeriesDetail): FileInfo[] {
  const placed = new Set(allEpisodes(s).flatMap((e) => e.fileIds));
  return [...filesById(s).values()].filter((i) => !placed.has(i.file.id));
}

/** The files Reelkeeper has a note about (it isn't sure which episode they are). */
export function filesWithNotes(s: SeriesDetail): FileInfo[] {
  return [...filesById(s).values()].filter((i) => Boolean(i.file.matchNote));
}

export const hasAired = (e: EpisodeDetail, today: string) => e.airDate !== "" && e.airDate <= today;

/** Today as "2026-10-08", for comparing with episode air dates. */
export function todayISO(): string {
  const d = new Date();
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** A version's episode states by episode id. */
export function statesById(v: VersionDetail | undefined): Map<number, EpisodeState> {
  return new Map((v?.episodes ?? []).map((s) => [s.episodeId, s]));
}

/** "3 wanted · 1 downloading · 2 upcoming" (parts that are zero are left out), or "". */
export function stateSummary(episodes: EpisodeState[] | undefined): string {
  const count = (state: EpisodeState["state"]) =>
    (episodes ?? []).filter((e) => e.state === state).length;
  return [
    [count("wanted"), "wanted"],
    [count("downloading"), "downloading"],
    [count("not-aired"), "upcoming"],
  ]
    .filter(([n]) => n !== 0)
    .map(([n, label]) => `${n} ${label}`)
    .join(" · ");
}

/** A "YYYY-MM-DD" date written like the other dates on the page. */
export const formatDay = (day: string) => new Date(`${day}T00:00:00`).toLocaleDateString();
