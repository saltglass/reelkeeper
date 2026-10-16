import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { fetchHistory, type HistoryEvent, type HistoryEventKind, historyQuery } from "@/hooks/grab";
import { cn } from "@/lib/utils";
import { PageHeader } from "@/pages/PageHeader";
import { eventKinds, HistoryRow } from "./HistoryRow";

const pageSize = 50;

export function HistoryPage() {
  const [kinds, setKinds] = useState<HistoryEventKind[]>([]);
  const toggle = (k: HistoryEventKind) =>
    setKinds((cur) => (cur.includes(k) ? cur.filter((x) => x !== k) : [...cur, k]));

  return (
    <div>
      <PageHeader
        title="History"
        description="Everything Reelkeeper searched for, found and grabbed, and why. Open a row to see every release it looked at."
      />
      <fieldset className="mb-5 flex flex-wrap gap-2">
        <legend className="sr-only">Show only</legend>
        <Chip active={kinds.length === 0} onClick={() => setKinds([])}>
          All
        </Chip>
        {eventKinds.map((k) => (
          <Chip key={k.kind} active={kinds.includes(k.kind)} onClick={() => toggle(k.kind)}>
            {k.label}
          </Chip>
        ))}
      </fieldset>
      {/* Keyed by the filter so "Load more" pages start over when it changes. */}
      <HistoryList key={kinds.join(",")} events={kinds} />
    </div>
  );
}

function Chip({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: string;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "inline-flex min-h-9 cursor-pointer items-center rounded-full border bg-card px-3.5 text-sm font-medium transition-[background-color,border-color,transform] duration-200 hover:-translate-y-px hover:border-input active:scale-95",
        active && "border-primary/55 bg-accent text-accent-foreground",
      )}
    >
      {children}
    </button>
  );
}

function HistoryList({ events: kinds }: { events: HistoryEventKind[] }) {
  const filter = { events: kinds };
  const { data, error } = useQuery(historyQuery(filter, pageSize));
  // Older pages the user asked for, and where the next one starts.
  const [older, setOlder] = useState<HistoryEvent[]>([]);
  const [cursor, setCursor] = useState<number | null | undefined>(undefined);
  const [loading, setLoading] = useState(false);
  const [moreError, setMoreError] = useState<string>();

  if (error) return <Alert variant="error">Couldn't load History: {error.message}</Alert>;
  if (!data) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const next = cursor === undefined ? data.nextBefore : cursor;
  const shown = [...data.events, ...older];

  async function loadMore() {
    if (next == null) return;
    setLoading(true);
    setMoreError(undefined);
    try {
      const page = await fetchHistory(filter, pageSize, next);
      setOlder((o) => [...o, ...page.events]);
      setCursor(page.nextBefore);
    } catch (e) {
      setMoreError(e instanceof Error ? e.message : "Couldn't load more.");
    } finally {
      setLoading(false);
    }
  }

  if (shown.length === 0) {
    return (
      <Card className="p-6 text-sm text-muted-foreground">
        Nothing here yet. Searches show up when Reelkeeper looks for a movie, or when you press
        "Search now" on a movie's page.
      </Card>
    );
  }
  return (
    <div className="space-y-4">
      <ul className="tl flex flex-col gap-2.5">
        {shown.map((e, i) => (
          <HistoryRow key={e.id} event={e} variant="timeline" index={i} />
        ))}
      </ul>
      {moreError && <Alert variant="error">{moreError}</Alert>}
      {next != null && (
        <div className="text-center">
          <Button variant="outline" disabled={loading} onClick={loadMore}>
            {loading ? "Loading…" : "Load more"}
          </Button>
        </div>
      )}
    </div>
  );
}
