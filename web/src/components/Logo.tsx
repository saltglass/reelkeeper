import { cn } from "@/lib/utils";

/**
 * The Reelkeeper mark: a strip of film that ends as a bookmark, in one
 * colour (--logo, orange in the dark theme). The browser icon is a fixed copy
 * in public/brand/reelkeeper.svg.
 */
export function Logo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 64 64" aria-hidden="true" className={cn("shrink-0", className)}>
      <path fill="var(--logo)" fillRule="evenodd" d={markPath} />
    </svg>
  );
}

/** The mark's outline: the bookmark, its sprocket holes and two frames. */
const markPath =
  "M18 4h28a2 2 0 0 1 2 2v54l-16-11l-16 11V6a2 2 0 0 1 2-2Z " +
  "M21 8h3v4h-3Z M21 15h3v4h-3Z M21 22h3v4h-3Z M21 29h3v4h-3Z M21 36h3v4h-3Z " +
  "M40 8h3v4h-3Z M40 15h3v4h-3Z M40 22h3v4h-3Z M40 29h3v4h-3Z M40 36h3v4h-3Z " +
  "M27 9h10v12h-10Z M27 24h10v12h-10Z";
