import { type ReactNode, useEffect, useRef, useState } from "react";

/** How many items a long list draws at a time. */
export const BATCH = 120;

/**
 * Draws a long list a batch at a time: the first BATCH items, then another
 * batch whenever the end comes within three screens (further ahead than the
 * five rows of posters NearScreen loads early). A big library
 * (thousands of posters) drawn all at once needs more memory than iPhone
 * Safari allows a tab, which crashes and reloads it.
 *
 * resetKey changes when the user picks another filter, sort or search; the
 * list then starts again from the first batch. New data for the same view
 * (live updates) keeps what's already drawn, so the page doesn't jump.
 *
 * Put `more` right after the list: it is the invisible marker that asks for
 * the next batch.
 */
export function useInBatches<T>(items: T[], resetKey: string): { visible: T[]; more: ReactNode } {
  const [count, setCount] = useState(BATCH);
  const [key, setKey] = useState(resetKey);
  if (key !== resetKey) {
    setKey(resetKey);
    setCount(BATCH);
  }
  const marker = useRef<HTMLDivElement>(null);
  // Without IntersectionObserver (old browsers, tests) everything is drawn.
  const observable = typeof IntersectionObserver !== "undefined";
  const hasMore = observable && count < items.length;

  // biome-ignore lint/correctness/useExhaustiveDependencies: count is how a new observer starts after each batch (see below)
  useEffect(() => {
    const el = marker.current;
    if (!hasMore || !el) return;
    // A new observer per batch reports straight away, so a screen that still
    // shows the marker after a batch gets the next one too.
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) setCount((c) => c + BATCH);
      },
      { rootMargin: "0px 0px 300% 0px" },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [hasMore, count]);

  return {
    visible: hasMore ? items.slice(0, count) : items,
    more: hasMore ? <div ref={marker} aria-hidden="true" className="h-px" /> : null,
  };
}
