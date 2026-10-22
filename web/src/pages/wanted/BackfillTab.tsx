import { useQuery } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, ChevronRight, X } from "lucide-react";
import { useState } from "react";
import { RequestError } from "@/api/client";
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
import { Progress } from "@/components/ui/progress";
import {
  type BackfillOption,
  type BackfillShow,
  backfillQuery,
  useBackfillOptions,
  useGrabBackfillOption,
  useHideBackfill,
  useMoveBackfill,
  useRemoveBackfill,
} from "@/hooks/backfill";
import { toast } from "@/hooks/toast";
import { formatBytes } from "@/lib/format";
import { cn } from "@/lib/utils";
import { MovieName, PosterThumb, riseStyle } from "@/pages/activity/shared";

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** "14 of 20 episodes", plus what is on its way. */
function progressText(s: BackfillShow) {
  const total = s.found + s.missing;
  const base = `${s.found} of ${plural(total, "episode")}`;
  return s.downloading > 0 ? `${base}; ${s.downloading} downloading` : base;
}

/** Library → Wanted → Backfill: the show being worked on, the line, and the finished shows that need a decision. */
export function BackfillTab() {
  const { data, error } = useQuery(backfillQuery);
  if (error) return <Alert variant="error">Couldn't load Backfill: {error.message}</Alert>;
  if (!data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (!data.now && data.next.length === 0 && data.decision.length === 0) {
    return (
      <Card className="p-6 text-sm text-muted-foreground">
        Nothing queued. Use Add to backfill on a series to fill in its older missing episodes, one
        show at a time.
      </Card>
    );
  }
  return (
    <div className="space-y-6">
      <Section title="Now">
        {data.now ? (
          <NowCard
            show={data.now}
            grabsToday={data.grabsToday}
            grabsPerDay={data.grabsPerDay}
            hasNext={data.next.length > 0}
          />
        ) : (
          <Card className="p-4 text-sm text-muted-foreground">Nothing is being worked on.</Card>
        )}
      </Section>
      {data.next.length > 0 && (
        <Section title="Up next">
          <Card className="overflow-hidden">
            <ul>
              {data.next.map((s, i) => (
                <NextRow
                  key={s.id}
                  show={s}
                  place={i + 1}
                  first={i === 0}
                  last={i === data.next.length - 1}
                />
              ))}
            </ul>
          </Card>
        </Section>
      )}
      {data.decision.length > 0 && (
        <Section title="Needs a decision">
          <p className="mb-2 text-sm text-muted-foreground">
            These shows have everything Reelkeeper can find that fits the profile. The missing
            episodes stay wanted, and Reelkeeper keeps looking quietly.
          </p>
          <Card className="overflow-hidden">
            <ul>
              {data.decision.map((s, i) => (
                <DecisionRow key={s.id} show={s} index={i} />
              ))}
            </ul>
          </Card>
        </Section>
      )}
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section aria-label={title}>
      <h2 className="mb-2 text-lg font-semibold">{title}</h2>
      {children}
    </section>
  );
}

function NowCard({
  show,
  grabsToday,
  grabsPerDay,
  hasNext,
}: {
  show: BackfillShow;
  grabsToday: number;
  grabsPerDay: number;
  hasNext: boolean;
}) {
  const remove = useRemoveBackfill();
  const total = show.found + show.missing;
  return (
    <Card className="animate-rise flex flex-wrap items-start gap-4 p-4">
      <PosterThumb path={show.posterPath || undefined} />
      <div className="min-w-0 flex-1 space-y-2">
        <p className="font-semibold break-words">
          <MovieName
            movie={{ id: show.titleId, title: show.title, year: show.year }}
            mediaType="series"
          />
        </p>
        <p className="text-sm">{progressText(show)}</p>
        {total > 0 && (
          <Progress
            value={Math.round((show.found / total) * 100)}
            label={`${show.title} episodes found`}
          />
        )}
        <p className="text-xs text-muted-foreground">
          Backfill grabs today: {grabsToday} of {grabsPerDay}
        </p>
        {remove.error && <Alert variant="error">{remove.error.message}</Alert>}
      </div>
      <Button
        variant="ghost"
        size="sm"
        disabled={remove.isPending}
        aria-label={`Stop backfilling ${show.title}`}
        title={
          hasNext
            ? "Takes it out of the queue; the next show starts. Its episodes stay on."
            : "Takes it out of the queue. Its episodes stay on."
        }
        onClick={() => remove.mutate(show.id)}
      >
        <X /> Stop
      </Button>
    </Card>
  );
}

function NextRow({
  show,
  place,
  first,
  last,
}: {
  show: BackfillShow;
  place: number;
  first: boolean;
  last: boolean;
}) {
  const move = useMoveBackfill();
  const remove = useRemoveBackfill();
  const err = move.error ?? remove.error;
  return (
    <li
      className="animate-rise flex flex-wrap items-center gap-3 border-b px-4 py-3 last:border-b-0"
      style={riseStyle(place)}
    >
      <span className="w-6 text-sm text-muted-foreground tabular-nums">{place}</span>
      <PosterThumb path={show.posterPath || undefined} className="hidden sm:block" />
      <div className="min-w-0 flex-1">
        <p className="font-semibold break-words">
          <MovieName
            movie={{ id: show.titleId, title: show.title, year: show.year }}
            mediaType="series"
          />
        </p>
        <p className="text-sm text-muted-foreground">{plural(show.missing, "episode")} missing</p>
        {err && <Alert variant="error">{err.message}</Alert>}
      </div>
      <div className="flex items-center gap-1">
        <Button
          variant="tile"
          size="icon"
          aria-label={`Move ${show.title} up`}
          disabled={first || move.isPending}
          onClick={() => move.mutate({ id: show.id, direction: "up" })}
        >
          <ArrowUp />
        </Button>
        <Button
          variant="tile"
          size="icon"
          aria-label={`Move ${show.title} down`}
          disabled={last || move.isPending}
          onClick={() => move.mutate({ id: show.id, direction: "down" })}
        >
          <ArrowDown />
        </Button>
        <Button
          variant="ghost"
          size="sm"
          aria-label={`Remove ${show.title} from the queue`}
          disabled={remove.isPending}
          onClick={() => remove.mutate(show.id)}
        >
          Remove
        </Button>
      </div>
    </li>
  );
}

function DecisionRow({ show, index }: { show: BackfillShow; index: number }) {
  const [open, setOpen] = useState(false);
  const hide = useHideBackfill();
  const options = useBackfillOptions();
  const total = show.found + show.missing;

  function toggle() {
    const next = !open;
    setOpen(next);
    // Searching costs the indexers queries, so only when asked.
    if (next && !options.data && !options.isPending) options.mutate(show.id);
  }

  return (
    <li className="animate-rise border-b px-4 py-3 last:border-b-0" style={riseStyle(index)}>
      <div className="flex flex-wrap items-center gap-3">
        <PosterThumb path={show.posterPath || undefined} className="hidden sm:block" />
        <div className="min-w-0 flex-1">
          <p className="font-semibold break-words">
            <MovieName
              movie={{ id: show.titleId, title: show.title, year: show.year }}
              mediaType="series"
            />
          </p>
          <p className="text-sm text-muted-foreground">
            {show.found} of {plural(total, "episode")}; {show.missing} still wanted
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          aria-expanded={open}
          aria-label={`Show options for ${show.title}`}
          onClick={toggle}
        >
          <ChevronRight
            className={cn(
              "transition-transform duration-[calc(200ms*var(--m))]",
              open && "rotate-90",
            )}
            aria-hidden="true"
          />
          Show options
        </Button>
        <Button
          variant="ghost"
          size="sm"
          aria-label={`Hide ${show.title}`}
          title="Takes it off this list. Its missing episodes stay wanted."
          disabled={hide.isPending}
          onClick={() => hide.mutate(show.id)}
        >
          Hide
        </Button>
      </div>
      {hide.error && <Alert variant="error">{hide.error.message}</Alert>}
      {open && <Options show={show} query={options} />}
    </li>
  );
}

function Options({
  show,
  query,
}: {
  show: BackfillShow;
  query: ReturnType<typeof useBackfillOptions>;
}) {
  const [picked, setPicked] = useState<BackfillOption>();
  const [sent, setSent] = useState<Set<string>>(new Set());
  const grab = useGrabBackfillOption();

  if (query.isPending) {
    return (
      <p role="status" className="mt-3 text-sm text-muted-foreground">
        Searching for releases outside your profile… this can take a minute.
      </p>
    );
  }
  if (query.error) {
    return (
      <div className="mt-3">
        <Alert variant="error">Couldn't look for options: {query.error.message}</Alert>
      </div>
    );
  }
  if (!query.data) return null;

  function confirm() {
    if (!picked) return;
    const token = picked.token;
    grab.mutate(
      { id: show.id, token },
      {
        onSuccess: () => {
          setSent((cur) => new Set(cur).add(token));
          toast("Sent to the download client.", "ok");
          setPicked(undefined);
        },
      },
    );
  }

  const err = grab.error instanceof RequestError ? grab.error : undefined;
  return (
    <div className="mt-3 space-y-4 border-t pt-3">
      {query.data.units.map((u) => (
        <div key={u.label} className="space-y-1.5">
          <h3 className="text-sm font-semibold">
            {u.label.charAt(0).toUpperCase() + u.label.slice(1)}
          </h3>
          {u.options.length === 0 ? (
            <p className="text-sm text-muted-foreground">Nothing else found for this.</p>
          ) : (
            <ul className="space-y-2">
              {u.options.map((o) => (
                <li key={o.token} className="rounded-lg border bg-muted/30 p-3">
                  <p className="font-mono text-xs break-all">{o.title}</p>
                  <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                    <Badge variant="info">
                      {o.usenet ? "Usenet" : plural(o.seeders, "seeder")}
                    </Badge>
                    <span>{formatBytes(o.size)}</span>
                    <span>{o.quality}</span>
                    <span>{o.indexer}</span>
                  </p>
                  <p className="mt-1 text-sm break-words">{o.reasons.join(" ")}</p>
                  <div className="mt-2">
                    {sent.has(o.token) ? (
                      <Badge variant="success">Sent to the download client</Badge>
                    ) : (
                      <Button
                        variant="outline"
                        size="sm"
                        aria-label={`Grab this one: ${o.title}`}
                        onClick={() => {
                          grab.reset();
                          setPicked(o);
                        }}
                      >
                        Grab this one
                      </Button>
                    )}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      ))}
      {query.data.omitted > 0 && (
        <p className="text-xs text-muted-foreground">
          {plural(
            query.data.omitted,
            "more missing season or episode",
            "more missing seasons or episodes",
          )}{" "}
          aren't shown here.
        </p>
      )}
      <Dialog open={Boolean(picked)} onOpenChange={(o) => !o && setPicked(undefined)}>
        <DialogContent>
          <DialogTitle>Download this release even though it's outside your profile?</DialogTitle>
          <DialogDescription className="break-all">{picked?.title}</DialogDescription>
          {picked && <p className="text-sm">{picked.reasons.join(" ")}</p>}
          {err && <Alert variant="error">{err.message}</Alert>}
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button disabled={grab.isPending} onClick={confirm}>
              {grab.isPending ? "Sending…" : "Download it"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
