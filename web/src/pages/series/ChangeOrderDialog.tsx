import { useMutation, useQuery } from "@tanstack/react-query";
import { Check } from "lucide-react";
import { useState } from "react";
import { api, unwrap } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { type EpisodeOrder, useInvalidateLibrary } from "@/hooks/library";
import { cn } from "@/lib/utils";

/**
 * Lets the owner pick which episode numbering a series' files follow (TMDB's
 * aired order, DVD order...). Asking TMDB for the list can take a few
 * seconds. Switching only re-links files to episodes; nothing is renamed or
 * moved.
 */
export function ChangeOrderDialog({
  seriesId,
  open,
  onOpenChange,
}: {
  seriesId: number;
  open: boolean;
  onOpenChange: (o: boolean) => void;
}) {
  const invalidate = useInvalidateLibrary();
  const [picked, setPicked] = useState<EpisodeOrder | null>(null);
  const orders = useQuery({
    queryKey: ["series", seriesId, "orders"],
    queryFn: () => unwrap(api.GET("/series/{id}/orders", { params: { path: { id: seriesId } } })),
    enabled: open,
    staleTime: 0,
    gcTime: 0,
  });
  const change = useMutation({
    mutationFn: (episodeOrder: string) =>
      unwrap(
        api.PUT("/series/{id}/order", {
          params: { path: { id: seriesId } },
          body: { episodeOrder },
        }),
      ),
    onSuccess: () => {
      invalidate();
      close(false);
    },
  });

  function close(o: boolean) {
    onOpenChange(o);
    if (!o) {
      setPicked(null);
      change.reset();
    }
  }

  // The order that fits the most files. Not flagged when every order fits
  // equally well, because then it tells you nothing.
  const list = orders.data ?? [];
  const most = Math.max(0, ...list.map((o) => o.fits));
  const allSame = list.every((o) => o.fits === list[0]?.fits);
  const isBest = (o: EpisodeOrder) => !allSame && most > 0 && o.fits === most;
  // Most series have only TMDB's aired order: then there's nothing to pick,
  // and the dialog says so instead of showing one greyed-out choice.
  const onlyOne = orders.isSuccess && list.length <= 1;
  const current = list.find((o) => o.current);
  const currentTotal = current ? current.fits + current.problems : 0;

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="max-w-2xl">
        <DialogTitle>Episode order</DialogTitle>
        <DialogDescription>
          Shows disagree about how episodes are numbered: a DVD box set can differ from the order
          they aired in. Pick the numbering your files follow.
        </DialogDescription>

        {orders.isLoading && (
          <p className="text-sm text-muted-foreground">
            Asking TMDB which orders this series has… this can take a few seconds.
          </p>
        )}
        {orders.error && (
          <Alert variant="error">Couldn't load the episode orders: {orders.error.message}</Alert>
        )}

        {onlyOne && (
          <Alert variant={current && current.problems > 0 ? "warning" : "success"}>
            TMDB has no other episode orders for this series, so there's nothing to switch to.
            {current &&
              currentTotal > 0 &&
              (current.problems === 0
                ? ` All ${currentTotal} ${currentTotal === 1 ? "file fits" : "files fit"} TMDB's aired order.`
                : ` ${current.fits} of ${currentTotal} files fit TMDB's aired order; check the notes on the others.`)}
          </Alert>
        )}
        {!onlyOne && orders.isSuccess && !picked && (
          <p className="text-sm text-muted-foreground">Pick the order your files follow.</p>
        )}

        <ul className={cn("max-h-[50vh] space-y-2 overflow-y-auto", onlyOne && "hidden")}>
          {list.map((o) => {
            const total = o.fits + o.problems;
            const selected = picked?.id === o.id;
            return (
              <li key={o.id || "aired"}>
                <button
                  type="button"
                  aria-pressed={selected}
                  disabled={o.current}
                  onClick={() => setPicked(o)}
                  className={cn(
                    "w-full space-y-1 rounded-lg border p-3 text-left transition-[border-color,background-color] duration-[calc(200ms*var(--m))] hover:border-primary/50 hover:bg-muted disabled:cursor-default disabled:hover:bg-transparent",
                    o.current && "border-primary/50 bg-primary/5",
                    selected && "border-primary ring-1 ring-primary",
                  )}
                >
                  <span className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-medium">{o.name}</span>
                    {o.type && <Badge>{o.type}</Badge>}
                    {o.current && <Badge variant="info">Current</Badge>}
                    {isBest(o) && (
                      <Badge variant="success">
                        <Check className="size-3" /> Best fit
                      </Badge>
                    )}
                  </span>
                  {o.description && (
                    <span className="block text-xs text-muted-foreground">{o.description}</span>
                  )}
                  <span className="block text-xs text-muted-foreground">
                    {o.episodes} episodes ·{" "}
                    {total === 0
                      ? "no files to compare"
                      : `fits ${o.fits} of ${total} ${total === 1 ? "file" : "files"}`}
                  </span>
                </button>
              </li>
            );
          })}
        </ul>

        {picked && (
          <Alert variant="info">
            <p className="space-y-1">
              Switch to <strong>{picked.name}</strong>? Reelkeeper will link your files to episodes
              using their numbers in this order.{" "}
              {picked.problems > 0
                ? `${picked.problems} ${picked.problems === 1 ? "file" : "files"} won't fit cleanly and will get a note. `
                : "Every file fits. "}
              No files are renamed or moved, and you can switch back any time.
            </p>
          </Alert>
        )}
        {change.error && <Alert variant="error">{change.error.message}</Alert>}

        <div className="flex justify-end gap-2">
          <DialogClose asChild>
            <Button variant={onlyOne ? "default" : "ghost"}>{onlyOne ? "Close" : "Cancel"}</Button>
          </DialogClose>
          {!onlyOne && (
            <Button
              disabled={!picked || change.isPending}
              onClick={() => picked && change.mutate(picked.id)}
            >
              {change.isPending ? "Switching…" : "Use this order"}
            </Button>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
