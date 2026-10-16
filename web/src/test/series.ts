import type { EpisodeDetail, SeriesDetail, SeriesSummary } from "@/hooks/library";

/** Version summary fields shared by the series fixtures. */
export const seriesVersion = (over: Partial<SeriesSummary["versions"][number]> = {}) => ({
  id: 1,
  profileId: 1,
  profileName: "HD",
  rootFolderId: 5,
  rootPath: "/media/shows",
  folder: "Show",
  monitored: true,
  files: 3,
  missing: 0,
  size: 6 * 1024 ** 3,
  quality: "WEBDL-1080p",
  resolution: 1080,
  hdr: [] as string[],
  videoCodec: "H.264",
  audioCodec: "AAC",
  audioChannels: 2,
  releaseGroup: "NTb",
  edition: "",
  episodesWithFiles: 3,
  ...over,
});

export const seriesSummary = (
  id: number,
  title: string,
  over: Partial<SeriesSummary> = {},
): SeriesSummary => ({
  id,
  tmdbId: id,
  tvdbId: id,
  imdbId: "",
  title,
  sortTitle: title.toLowerCase().replace(/^the /, ""),
  year: 2010,
  status: "Ended",
  network: "BBC",
  seriesType: "standard",
  genres: [],
  posterPath: `/p${id}.jpg`,
  seasons: 2,
  airedEpisodes: 10,
  problems: 0,
  addedAt: `2026-10-0${id}T00:00:00Z`,
  versions: [seriesVersion()],
  ...over,
});

export const episode = (
  id: number,
  season: number | undefined,
  number: number | undefined,
  over: Partial<EpisodeDetail> = {},
): EpisodeDetail => ({
  id,
  tmdbId: id,
  season,
  number,
  airedSeason: season ?? 1,
  airedNumber: number ?? id,
  title: `Episode ${id}`,
  overview: "",
  airDate: "2020-01-01",
  runtime: 45,
  stillPath: "",
  episodeType: "standard",
  fileIds: [],
  ...over,
});

export const mediaFile = (id: number, path: string, over: Record<string, unknown> = {}) => ({
  id,
  path,
  size: 2 * 1024 ** 3,
  quality: "WEBDL-1080p",
  resolution: 1080,
  source: "web",
  edition: "",
  releaseGroup: "NTb",
  durationSeconds: 2700,
  video: { codec: "H.264", width: 1920, height: 1080, bitDepth: 8, hdr: [] },
  audio: [{ codec: "AAC", channels: 2, language: "eng", default: true }],
  subtitles: [],
  addedAt: "2026-10-07T00:00:00Z",
  matchNote: "",
  ...over,
});

/**
 * A show with: S01 (E1 has a file, E2 has a file with a note, E3 has none),
 * S02 (E1 and E2 share one file), Specials (one episode with no file), an
 * episode the order leaves out, and a file no episode lists.
 */
export function seriesDetail(over: Partial<SeriesDetail> = {}): SeriesDetail {
  const files = [
    mediaFile(101, "Show/Season 01/Show - S01E01.mkv"),
    mediaFile(102, "Show/Season 01/Show - S01E02.mkv", {
      matchNote: "The file says S01E02 but TMDB's episode 2 is a different length.",
    }),
    mediaFile(103, "Show/Season 02/Show - S02E01-E02.mkv"),
    mediaFile(104, "Show/random.mkv", { matchNote: "" }),
  ];
  return {
    ...seriesSummary(7, "Show"),
    originalTitle: "Show",
    overview: "A show about things.",
    runtime: 45,
    certification: "TV-14",
    firstAirDate: "2010-01-01",
    lastAirDate: "2012-01-01",
    backdropPath: "",
    voteAverage: 8,
    episodeOrder: "",
    episodeOrderName: "",
    seasons: 2,
    airedEpisodes: 5,
    problems: 1,
    addedAt: "2026-10-07T00:00:00Z",
    versions: [
      {
        ...seriesVersion({ files: 4, episodesWithFiles: 3 }),
        mediaFiles: files,
        monitor: "all" as const,
        episodes: [
          { episodeId: 1, monitored: true, state: "has-file" as const },
          { episodeId: 2, monitored: true, state: "has-file" as const },
          { episodeId: 3, monitored: true, state: "not-aired" as const },
          { episodeId: 4, monitored: true, state: "has-file" as const },
          { episodeId: 5, monitored: true, state: "has-file" as const },
          { episodeId: 6, monitored: false, state: "unmonitored" as const },
          { episodeId: 8, monitored: false, state: "unmonitored" as const },
        ],
      },
    ],
    seasonList: [
      {
        number: 1,
        name: "Season 1",
        overview: "",
        airDate: "2010-01-01",
        posterPath: "",
        episodes: [
          episode(1, 1, 1, { title: "Pilot", airDate: "2010-01-01", fileIds: [101] }),
          episode(2, 1, 2, { title: "Second", fileIds: [102] }),
          episode(3, 1, 3, { title: "Third", airDate: "" }),
        ],
      },
      {
        number: 2,
        name: "Season 2",
        overview: "",
        airDate: "2011-01-01",
        posterPath: "",
        episodes: [
          episode(4, 2, 1, { title: "Return", airDate: "2011-01-01", fileIds: [103] }),
          episode(5, 2, 2, { title: "Return II", airDate: "2011-01-08", fileIds: [103] }),
        ],
      },
      {
        number: 0,
        name: "Specials",
        overview: "",
        airDate: "",
        posterPath: "",
        episodes: [episode(6, 0, 1, { title: "Christmas Special", airDate: "2010-12-25" })],
      },
    ],
    unordered: [
      episode(8, undefined, undefined, { title: "Deleted Scene", airedSeason: 1, airedNumber: 9 }),
    ],
    ...over,
  };
}
