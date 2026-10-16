import { Link } from "@tanstack/react-router";
import { useAppearance } from "@/hooks/theme";

/**
 * Titles for the Movies and Series poster grids. The title and year sit under
 * the poster, not over it, so they never cover the artwork, and Settings →
 * General → Appearance can hide them (posterTitles).
 *
 * PosterName goes inside the poster's link: it names the link for screen
 * readers, and shows on the tile itself only when there's no poster image
 * and the caption is hidden, so a posterless title is never left blank.
 * PosterCaption is the visible line under the poster; it's a second way to
 * click through, hidden from screen readers and the tab order since the
 * poster link already says the same.
 */
export function usePosterTitles() {
  return useAppearance().appearance.posterTitles;
}

export function PosterName({
  title,
  year,
  onTile,
}: {
  title: string;
  year?: number;
  onTile: boolean;
}) {
  if (!onTile) return <span className="sr-only">{year ? `${title} (${year}) ` : `${title} `}</span>;
  return (
    <span className="absolute inset-x-3 bottom-3.5">
      <span className="block text-[15px]/[19px] font-semibold [text-shadow:0_1px_8px_rgba(0,0,0,0.5)]">
        {title}
      </span>
      <span className="mt-[3px] block text-xs/4 opacity-90">{year || "—"}</span>
    </span>
  );
}

export function PosterCaption({
  to,
  id,
  title,
  year,
}: {
  to: "/movies/$id" | "/series/$id";
  id: number;
  title: string;
  year?: number;
}) {
  return (
    <Link
      to={to}
      params={{ id: String(id) }}
      tabIndex={-1}
      aria-hidden="true"
      className="block min-w-0 hover:underline"
    >
      <span className="line-clamp-2 text-sm/5 font-medium">{title}</span>
      <span className="block text-xs/4 text-muted-foreground">{year || "—"}</span>
    </Link>
  );
}
