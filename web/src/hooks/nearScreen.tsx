import {
  type CSSProperties,
  createContext,
  type ReactNode,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react";
import { cn } from "@/lib/utils";

/** How many rows above and below the screen have their posters ready. */
export const ROWS_AHEAD = 5;

type Watch = (el: Element, onChange: (near: boolean) => void) => () => void;

const NearContext = createContext<Watch | null>(null);

/**
 * Wrap a poster grid in this and draw its tiles with NearItem. Tiles within
 * ROWS_AHEAD rows of the screen are drawn and their posters loaded before
 * they scroll into view, so scrolling stays smooth. Tiles further away drop
 * their poster, so a library of thousands stays within the memory iPhone
 * Safari allows a tab; scrolling back brings it from the browser's cache.
 */
export function NearScreen({ children }: { children: ReactNode }) {
  // Without IntersectionObserver (old browsers, tests) every tile is "near".
  const [watcher] = useState(() =>
    typeof IntersectionObserver === "undefined" ? null : newWatcher(),
  );
  useEffect(() => () => watcher?.stop(), [watcher]);
  return <NearContext.Provider value={watcher?.watch ?? null}>{children}</NearContext.Provider>;
}

/** A grid tile (`li`) that tells its children whether it is near the screen. */
export function NearItem({
  className,
  style,
  children,
}: {
  className?: string;
  style?: CSSProperties;
  children: (near: boolean) => ReactNode;
}) {
  const ref = useRef<HTMLLIElement>(null);
  const watch = useContext(NearContext);
  const [near, setNear] = useState(watch === null);
  useEffect(() => {
    const el = ref.current;
    if (!watch || !el) return;
    return watch(el, setNear);
  }, [watch]);
  return (
    <li ref={ref} className={cn(className, near && "near")} style={style}>
      {children(near)}
    </li>
  );
}

/**
 * One IntersectionObserver for a whole grid. Its margin is ROWS_AHEAD rows,
 * measured from a drawn tile plus the grid's row gap; it is measured again
 * once the first tiles are drawn and whenever the window changes size (rows
 * get taller or shorter as the columns change).
 */
function newWatcher() {
  const items = new Map<Element, { onChange: (near: boolean) => void; near: boolean }>();
  let io: IntersectionObserver | null = null;
  let measured = false;
  let timer: ReturnType<typeof setTimeout> | undefined;

  const start = () => {
    const first = items.keys().next().value;
    if (!first) return;
    const tile = [...items].find(([, s]) => s.near)?.[0] ?? first;
    const gap = Number.parseFloat(getComputedStyle(tile.parentElement ?? tile).rowGap) || 0;
    const margin = Math.round((tile.getBoundingClientRect().height + gap) * ROWS_AHEAD);
    io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          const s = items.get(e.target);
          if (!s) continue;
          s.near = e.isIntersecting;
          s.onChange(e.isIntersecting);
        }
        // The first measurement may come from a tile not drawn yet.
        if (!measured) {
          measured = true;
          later();
        }
      },
      { rootMargin: `${margin}px 0px` },
    );
    for (const el of items.keys()) io.observe(el);
    window.addEventListener("resize", later);
  };
  const stop = () => {
    clearTimeout(timer);
    io?.disconnect();
    io = null;
    window.removeEventListener("resize", later);
  };
  const restart = () => {
    stop();
    start();
  };
  function later() {
    clearTimeout(timer);
    timer = setTimeout(restart, 200);
  }

  const watch: Watch = (el, onChange) => {
    items.set(el, { onChange, near: false });
    if (io) io.observe(el);
    else start();
    return () => {
      items.delete(el);
      io?.unobserve(el);
      if (items.size === 0) stop();
    };
  };
  return { watch, stop };
}
