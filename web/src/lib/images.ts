import { urlBase } from "@/lib/utils";

/** URL of a TMDB image, served (and cached) by Reelkeeper itself. */
export function tmdbImage(
  path: string | undefined,
  size: "w185" | "w342" | "w500" | "w780" | "w1280" = "w342",
) {
  if (!path) return undefined;
  return `${urlBase()}/api/v1/images/tmdb/${size}${path}`;
}
