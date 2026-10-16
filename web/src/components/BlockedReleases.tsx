import { useQuery } from "@tanstack/react-query";
import { Ban, ChevronRight } from "lucide-react";
import { useState } from "react";
import { RequestError } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { type BlocklistItem, blocklistQuery, useUnblock } from "@/hooks/downloads";
import { toast } from "@/hooks/toast";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";
import { riseStyle } from "@/pages/activity/shared";

/** What the blocklist is for, in one place so every screen says the same. */
export const blocklistHelp =
  "Reelkeeper skips these releases when it searches. Ones blocked because a download got stuck are tried again after 30 days.";

/** One blocked release: what it is, why it's blocked, how long, and an Unblock button. */
export function BlockedRow({ item: b, index }: { item: BlocklistItem; index: number }) {
  const unblock = useUnblock();
  const meta = [b.quality, b.indexer].filter(Boolean).join(" · ");
  return (
    <li
      className="animate-rise grid grid-cols-[minmax(0,1fr)_auto] items-start gap-4 border-b px-4 py-4 last:border-b-0 sm:px-5"
      style={riseStyle(index)}
    >
      <div className="min-w-0 space-y-0.5">
        <p className="font-mono text-xs break-all">{b.releaseTitle}</p>
        {meta && <p className="text-xs text-muted-foreground">{meta}</p>}
        {b.episodes && <p className="text-xs text-muted-foreground">Episodes: {b.episodes}</p>}
        <p className="flex items-start gap-1.5 pt-1.5 text-sm font-medium break-words text-warning">
          <Ban className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
          <span>{b.reason}</span>
        </p>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 pt-1">
          <time
            dateTime={b.createdAt}
            title={new Date(b.createdAt).toLocaleString()}
            className="text-xs text-muted-foreground"
          >
            Blocked {relativeTime(b.createdAt)}
          </time>
          {b.expiresAt ? (
            <Badge variant="info" title={new Date(b.expiresAt).toLocaleString()}>
              Expires {relativeTime(b.expiresAt)}
            </Badge>
          ) : (
            <Badge title="Stays blocked until you unblock it.">Permanent</Badge>
          )}
        </div>
      </div>
      <Button
        variant="outline"
        size="sm"
        aria-label={`Unblock ${b.releaseTitle}`}
        disabled={unblock.isPending}
        onClick={() =>
          unblock.mutate(b.id, {
            onSuccess: () => toast("Unblocked. Reelkeeper may grab it again.", "ok"),
            onError: (e) =>
              toast(e instanceof RequestError ? e.message : "Couldn't unblock it.", "problem"),
          })
        }
      >
        Unblock
      </Button>
    </li>
  );
}

/**
 * A movie's or series' blocked releases, folded away until opened. Not shown at
 * all when nothing is blocked, or if the list can't be loaded.
 */
export function BlockedReleases({ titleId }: { titleId: number }) {
  // Dormant entries too: this title's page shows everything blocked for it.
  const { data } = useQuery(blocklistQuery({ titleId, dormant: true }));
  const [open, setOpen] = useState(false);
  if (!data || data.items.length === 0) return null;
  return (
    <section className="animate-rise">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="inline-flex min-h-9 cursor-pointer items-center gap-1.5 rounded-md text-base font-semibold hover:text-link"
      >
        <ChevronRight
          className={cn(
            "size-4 transition-transform duration-[calc(200ms*var(--m))]",
            open && "rotate-90",
          )}
          aria-hidden="true"
        />
        Blocked releases ({data.items.length})
      </button>
      {open && (
        <Card className="animate-rise mt-2 overflow-hidden">
          <p className="border-b px-4 py-3 text-sm text-muted-foreground sm:px-5">
            {blocklistHelp}
          </p>
          <ul>
            {data.items.map((b, i) => (
              <BlockedRow key={b.id} item={b} index={i} />
            ))}
          </ul>
        </Card>
      )}
    </section>
  );
}
