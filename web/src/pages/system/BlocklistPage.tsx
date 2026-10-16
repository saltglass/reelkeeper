import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { ChevronRight, Search } from "lucide-react";
import { useMemo, useState } from "react";
import { RequestError } from "@/api/client";
import { BlockedRow, blocklistHelp } from "@/components/BlockedReleases";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { type BlocklistItem, blocklistQuery, useUnblockTemporary } from "@/hooks/downloads";
import { toast } from "@/hooks/toast";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { MovieName, riseStyle } from "@/pages/activity/shared";
import { PageHeader } from "@/pages/PageHeader";

/** The Reason filter: which rules (BlocklistItem.rule) each choice covers. */
const reasons = {
  all: { label: "All reasons", rules: undefined },
  stuck: {
    label: "Stuck downloads",
    rules: ["stalled", "slow", "metadata", "missing", "import-failed", "cleanup"],
  },
  removed: { label: "Removed by you", rules: ["removed"] },
  deleted: { label: "Tracker deleted it", rules: ["unregistered"] },
  imported: { label: "Imported from Radarr or Sonarr", rules: ["radarr", "sonarr"] },
} as const;
type Reason = keyof typeof reasons;

const selectClass =
  "min-h-10 rounded-lg border border-input bg-card px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none";

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** One title's blocked releases. Items arrive newest first, so a group's first is its newest. */
type Group = { key: string; first: BlocklistItem; items: BlocklistItem[] };

const groupKey = (b: BlocklistItem) => `${b.mediaType}:${b.movie?.id ?? `gone-${b.id}`}`;

function groupByTitle(items: BlocklistItem[]): Group[] {
  const groups = new Map<string, Group>();
  for (const b of items) {
    const key = groupKey(b);
    const g = groups.get(key);
    if (g) g.items.push(b);
    else groups.set(key, { key, first: b, items: [b] });
  }
  return [...groups.values()];
}

export function BlocklistPage() {
  const [showDormant, setShowDormant] = useState(false);
  // Keep the old list on screen while the dormant switch loads the new one.
  const { data, error } = useQuery({
    ...blocklistQuery({ dormant: showDormant }),
    placeholderData: keepPreviousData,
  });
  const [q, setQ] = useState("");
  const [reason, setReason] = useState<Reason>("all");
  const [indexer, setIndexer] = useState("");
  const [open, setOpen] = useState<Set<string>>(new Set());
  const [confirming, setConfirming] = useState(false);

  const items = data?.items ?? [];
  const indexers = useMemo(
    () => [...new Set(items.map((b) => b.indexer).filter(Boolean))].sort(),
    [items],
  );
  // A remembered choice that no longer matches anything filters nothing.
  const activeIndexer = indexers.includes(indexer) ? indexer : "";
  const searching = q.trim() !== "";

  const groups = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const rules: readonly string[] | undefined = reasons[reason].rules;
    return groupByTitle(
      items.filter((b) => {
        if (rules && !rules.includes(b.rule)) return false;
        if (activeIndexer && b.indexer !== activeIndexer) return false;
        if (!needle) return true;
        return `${b.movie?.title ?? ""} ${b.releaseTitle}`.toLowerCase().includes(needle);
      }),
    );
  }, [items, q, reason, activeIndexer]);

  if (error) return <Alert variant="error">Couldn't load the blocklist: {error.message}</Alert>;

  const toggle = (key: string) =>
    setOpen((cur) => {
      const next = new Set(cur);
      if (!next.delete(key)) next.add(key);
      return next;
    });

  return (
    <div>
      <PageHeader
        title="Blocklist"
        description={`${blocklistHelp} Ones you blocked yourself stay blocked until you unblock them.`}
        actions={
          data &&
          data.temporary > 0 && (
            <Button variant="outline" onClick={() => setConfirming(true)}>
              Unblock all temporary entries ({data.temporary})
            </Button>
          )
        }
      />

      {!data ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : data.total === 0 ? (
        <Card className="p-6 text-sm text-muted-foreground">
          Nothing blocked. When a download gets stuck or you remove one from the Queue, its release
          is listed here so Reelkeeper doesn't grab it again.
        </Card>
      ) : (
        <>
          <div className="mb-5 flex flex-wrap items-center gap-2.5">
            <div className="relative min-w-60 flex-[1_1_260px]">
              <Search className="pointer-events-none absolute top-3 left-3 size-4 text-muted-foreground" />
              <Input
                type="search"
                aria-label="Search the blocklist"
                placeholder="Search title or release name"
                className="pl-9"
                value={q}
                onChange={(e) => setQ(e.target.value)}
              />
            </div>
            <select
              aria-label="Reason"
              className={selectClass}
              value={reason}
              onChange={(e) => setReason(e.target.value as Reason)}
            >
              {Object.entries(reasons).map(([k, r]) => (
                <option key={k} value={k}>
                  {r.label}
                </option>
              ))}
            </select>
            <select
              aria-label="Indexer"
              className={selectClass}
              value={activeIndexer}
              onChange={(e) => setIndexer(e.target.value)}
            >
              <option value="">All indexers</option>
              {indexers.map((name) => (
                <option key={name} value={name}>
                  {name}
                </option>
              ))}
            </select>
            <div className="flex items-center gap-2.5 text-sm">
              <Switch
                id="blocklist-dormant"
                checked={showDormant}
                onCheckedChange={setShowDormant}
              />
              <label htmlFor="blocklist-dormant">
                Show titles with nothing wanted
                <span className="text-muted-foreground"> ({data.dormant})</span>
              </label>
            </div>
          </div>

          {groups.length === 0 ? (
            <Card className="p-6 text-sm text-muted-foreground">
              {items.length > 0
                ? "Nothing matches those filters."
                : `Everything blocked (${plural(data.dormant, "release")}) is for titles with nothing wanted right now. Turn on "Show titles with nothing wanted" to see ${data.dormant === 1 ? "it" : "them"}.`}
            </Card>
          ) : (
            <div className="space-y-3">
              {groups.map((g, i) => (
                <GroupCard
                  key={g.key}
                  group={g}
                  index={i}
                  open={searching || open.has(g.key)}
                  onToggle={() => toggle(g.key)}
                />
              ))}
            </div>
          )}
        </>
      )}

      <UnblockTemporaryDialog
        open={confirming}
        count={data?.temporary ?? 0}
        onClose={() => setConfirming(false)}
      />
    </div>
  );
}

/** A title and its blocked releases, folded away until opened. */
function GroupCard({
  group: g,
  index,
  open,
  onToggle,
}: {
  group: Group;
  index: number;
  open: boolean;
  onToggle: () => void;
}) {
  const first = g.first;
  const name = first.movie ? `${first.movie.title} (${first.movie.year})` : "Removed title";
  const dormant = g.items.every((b) => b.dormant);
  return (
    <Card className="animate-rise overflow-hidden" style={riseStyle(index)}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 sm:px-5">
        <button
          type="button"
          aria-expanded={open}
          aria-label={`${open ? "Hide" : "Show"} blocked releases for ${name}`}
          onClick={onToggle}
          className="inline-flex size-8 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
        >
          <ChevronRight
            className={cn(
              "size-4 transition-transform duration-[calc(200ms*var(--m))]",
              open && "rotate-90",
            )}
            aria-hidden="true"
          />
        </button>
        <span className="font-semibold break-words">
          <MovieName movie={first.movie} mediaType={first.mediaType} />
        </span>
        <Badge>{plural(g.items.length, "release")}</Badge>
        {dormant && (
          <Badge title="Nothing is wanted for this title right now.">Nothing wanted</Badge>
        )}
        <span className="text-xs text-muted-foreground sm:ml-auto">
          Latest block {relativeTime(first.createdAt)}
        </span>
      </div>
      {open && (
        <ul className="border-t">
          {g.items.map((b, i) => (
            <BlockedRow key={b.id} item={b} index={i} />
          ))}
        </ul>
      )}
    </Card>
  );
}

function UnblockTemporaryDialog({
  open,
  count,
  onClose,
}: {
  open: boolean;
  count: number;
  onClose: () => void;
}) {
  const unblock = useUnblockTemporary();
  const err = unblock.error instanceof RequestError ? unblock.error : undefined;
  function close() {
    unblock.reset();
    onClose();
  }
  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent>
        <DialogTitle>Unblock all temporary entries?</DialogTitle>
        <DialogDescription>
          {plural(count, "release")} blocked because a download got stuck will be allowed again, so
          Reelkeeper may grab them in a later search (and they may get stuck again). Entries you
          blocked yourself stay blocked.
        </DialogDescription>
        {err && <Alert variant="error">{err.message}</Alert>}
        <div className="flex justify-end gap-2">
          <DialogClose asChild>
            <Button variant="ghost">Cancel</Button>
          </DialogClose>
          <Button
            disabled={unblock.isPending}
            onClick={() =>
              unblock.mutate(undefined, {
                onSuccess: (r) => {
                  toast(`Unblocked ${plural(r.removed, "release")}.`, "ok");
                  close();
                },
              })
            }
          >
            {unblock.isPending ? "Unblocking…" : "Unblock them"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
