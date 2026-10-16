import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Search } from "lucide-react";
import { Dialog as DialogPrimitive } from "radix-ui";
import { useEffect, useMemo, useRef, useState } from "react";
import { nav } from "@/components/AppShell";
import { moviesQuery, seriesListQuery } from "@/hooks/library";
import { cn } from "@/lib/utils";

type Target =
  | { kind: "page"; key: string; label: string; section: string; to: string }
  | { kind: "movie" | "series"; key: string; label: string; section: string; id: number };

const maxResults = 30;

/**
 * "Jump to": a search box over every page and every movie and series in the
 * library. Opens with Ctrl+K / Cmd+K or the top bar's search button (which
 * sends the "reelkeeper:jump-to" event, see openJumpTo in AppShell).
 */
export function JumpTo() {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const [active, setActive] = useState(0);
  const navigate = useNavigate();
  const listRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const show = () => {
      setQ("");
      setActive(0);
      setOpen(true);
    };
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        show();
      }
    };
    window.addEventListener("reelkeeper:jump-to", show);
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("reelkeeper:jump-to", show);
      window.removeEventListener("keydown", onKey);
    };
  }, []);

  const { data: movies } = useQuery({ ...moviesQuery, enabled: open });
  const { data: series } = useQuery({ ...seriesListQuery, enabled: open });

  const results = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const has = (s: string) => s.toLowerCase().includes(needle);
    const pages: Target[] = nav.flatMap((s) =>
      s.items
        .filter((i) => has(`${s.title} ${i.label}`))
        .map((i) => ({
          kind: "page" as const,
          key: `p:${i.to}`,
          label: i.label,
          section: s.title,
          to: i.to,
        })),
    );
    if (!needle) return pages.slice(0, maxResults);
    const titles: Target[] = [
      ...(movies ?? [])
        .filter((m) => has(m.title))
        .map((m) => ({
          kind: "movie" as const,
          key: `m:${m.id}`,
          label: `${m.title} (${m.year})`,
          section: "Movie",
          id: m.id,
        })),
      ...(series ?? [])
        .filter((s) => has(s.title))
        .map((s) => ({
          kind: "series" as const,
          key: `s:${s.id}`,
          label: `${s.title} (${s.year})`,
          section: "Series",
          id: s.id,
        })),
    ];
    return [...pages, ...titles].slice(0, maxResults);
  }, [q, movies, series]);

  // Keep the highlighted row in view while arrowing through a long list.
  // biome-ignore lint/correctness/useExhaustiveDependencies: runs when the highlight moves
  useEffect(() => {
    listRef.current
      ?.querySelector('[aria-selected="true"]')
      ?.scrollIntoView?.({ block: "nearest" });
  }, [active]);

  function go(t: Target | undefined) {
    if (!t) return;
    setOpen(false);
    if (t.kind === "page") navigate({ to: t.to });
    else if (t.kind === "movie") navigate({ to: "/movies/$id", params: { id: String(t.id) } });
    else navigate({ to: "/series/$id", params: { id: String(t.id) } });
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!results.length) return;
      const step = e.key === "ArrowDown" ? 1 : -1;
      setActive((a) => (a + step + results.length) % results.length);
    } else if (e.key === "Enter") {
      e.preventDefault();
      go(results[Math.min(active, results.length - 1)]);
    }
  }

  const current = results[Math.min(active, results.length - 1)];

  return (
    <DialogPrimitive.Root open={open} onOpenChange={setOpen}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="animate-fade-in fixed inset-0 z-50 bg-[rgba(5,4,10,.55)] backdrop-blur-[6px]" />
        <DialogPrimitive.Content
          aria-describedby={undefined}
          className="animate-pal-in fixed top-24 left-1/2 z-50 w-[calc(100%-2rem)] max-w-[580px] -translate-x-1/2 overflow-hidden rounded-[14px] border bg-card shadow-md ring-1 ring-primary/20"
        >
          <DialogPrimitive.Title className="sr-only">Jump to</DialogPrimitive.Title>
          <div className="flex items-center gap-2.5 border-b px-4 text-muted-foreground">
            <Search className="size-[18px] shrink-0" aria-hidden="true" />
            <input
              // biome-ignore lint/a11y/noAutofocus: the box exists to be typed in at once
              autoFocus
              role="combobox"
              aria-expanded="true"
              aria-controls="jump-to-list"
              aria-activedescendant={current ? `jump-${current.key}` : undefined}
              aria-label="Jump to a page or movie"
              placeholder="Jump to a page or movie"
              value={q}
              onChange={(e) => {
                setQ(e.target.value);
                setActive(0);
              }}
              onKeyDown={onKeyDown}
              className="min-h-14 flex-1 border-0 bg-transparent text-base text-foreground outline-none placeholder:text-muted-foreground"
            />
            <kbd className="rounded border px-1.5 text-xs">esc</kbd>
          </div>
          <div
            ref={listRef}
            id="jump-to-list"
            role="listbox"
            aria-label="Results"
            className="max-h-[360px] overflow-auto p-2"
          >
            {results.map((t, i) => (
              <div
                key={t.key}
                id={`jump-${t.key}`}
                role="option"
                aria-selected={i === active}
                tabIndex={-1}
                onClick={() => go(t)}
                onKeyDown={(e) => e.key === "Enter" && go(t)}
                onMouseMove={() => i !== active && setActive(i)}
                className={cn(
                  "flex min-h-11 w-full cursor-pointer items-center gap-2.5 rounded-lg px-3 text-left font-medium transition-colors duration-[calc(150ms*var(--m))]",
                  i === active && "bg-accent text-accent-foreground",
                )}
              >
                <span className="truncate">{t.label}</span>
                <span
                  className={cn(
                    "ml-auto shrink-0 text-xs font-normal",
                    i === active ? "text-accent-foreground" : "text-muted-foreground",
                  )}
                >
                  {t.section}
                </span>
              </div>
            ))}
            {results.length === 0 && (
              <p className="p-6 text-center text-sm text-muted-foreground">
                No page or movie by that name.
              </p>
            )}
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
