import type { Blocklist, BlocklistItem } from "@/hooks/downloads";

const day = 86_400_000;
export const daysAgo = (n: number) => new Date(Date.now() - n * day).toISOString();
export const inDays = (n: number) => new Date(Date.now() + n * day + 60_000).toISOString();

/** A blocklist entry: a movie release you removed yourself (permanent) unless overridden. */
export const blocked = (over: Partial<BlocklistItem> = {}): BlocklistItem => ({
  id: 1,
  createdAt: daysAgo(3),
  movie: { id: 3, title: "Dune", year: 2021 },
  mediaType: "movie",
  episodes: "",
  releaseTitle: "Dune.2021.CAM-FAKE",
  indexer: "Tracker A",
  quality: "CAM",
  reason: "Removed from the queue by you",
  rule: "removed",
  expiresAt: null,
  permanent: true,
  dormant: false,
  ...over,
});

/** A temporary entry from cleanup that expires in 27 days. */
export const stuck = (over: Partial<BlocklistItem> = {}): BlocklistItem =>
  blocked({
    id: 2,
    releaseTitle: "Dune.2021.1080p.WEB-STUCK",
    quality: "WEBDL-1080p",
    reason: "Cleanup: No progress for 2 days.",
    rule: "stalled",
    expiresAt: inDays(27),
    permanent: false,
    ...over,
  });

/** The API's answer, with the counts worked out from the items given (and `all` for what's hidden). */
export const blocklistOf = (items: BlocklistItem[], over: Partial<Blocklist> = {}): Blocklist => ({
  items,
  total: items.length,
  dormant: items.filter((b) => b.dormant).length,
  temporary: items.filter((b) => !b.permanent).length,
  ...over,
});
