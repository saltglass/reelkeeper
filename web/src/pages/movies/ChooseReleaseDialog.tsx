import { ListChecks, RefreshCw, Search } from "lucide-react";
import { useMemo, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  type MovieChoice,
  type PickResult,
  useGrabMovieChoice,
  useMovieChoices,
} from "@/hooks/grab";
import { toast } from "@/hooks/toast";
import { formatBytes } from "@/lib/format";

// As the Blocklist's filters: sized to its choices, next to the search box.
const selectClass =
  "min-h-10 rounded-lg border border-input bg-card px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none";

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

type Sort = "best" | "seeders" | "largest" | "smallest";
const sorts: Record<Sort, { label: string; compare?: (a: MovieChoice, b: MovieChoice) => number }> =
  {
    best: { label: "Best match first" },
    seeders: {
      label: "Most seeders",
      compare: (a, b) => (b.usenet ? 1 : 0) - (a.usenet ? 1 : 0) || b.seeders - a.seeders,
    },
    largest: { label: "Largest", compare: (a, b) => b.size - a.size },
    smallest: { label: "Smallest", compare: (a, b) => a.size - b.size },
  };

/** What the toast says after a grab. */
export function grabbedText(res: PickResult) {
  if (res.dryRun) return "Dry run: recorded in History, nothing was downloaded.";
  return res.upgradesOff
    ? "Sent to the download client. Upgrades are now off for this version."
    : "Sent to the download client.";
}

/**
 * "Choose a release" for one movie version: a button that searches every
 * indexer and lists what was found, the releases the profile accepts first
 * and then the closest misses with why, so the user can grab one themselves.
 */
export function ChooseRelease({
  movieId,
  versionId,
  profileName,
  upgrades,
}: {
  movieId: number;
  versionId: number;
  profileName: string;
  /** The version's Upgrades switch, so the confirmation says whether it will turn off. */
  upgrades: boolean;
}) {
  const [open, setOpen] = useState(false);
  const list = useMovieChoices(movieId, versionId);

  return (
    <>
      <Button
        variant="outline"
        size="sm"
        onClick={() => {
          setOpen(true);
          list.mutate();
        }}
      >
        <ListChecks /> Choose a release
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="flex max-h-[90dvh] max-w-3xl flex-col gap-4 overflow-hidden">
          <div className="space-y-1 pr-6">
            <DialogTitle>Choose a release</DialogTitle>
            <DialogDescription>
              Everything your indexers found for the {profileName} version. Releases your profile
              accepts come first, then the ones it turned down, closest first, with why.
            </DialogDescription>
          </div>
          {list.isPending ? (
            <p role="status" className="text-sm text-muted-foreground">
              Searching every indexer… this can take a minute.
            </p>
          ) : list.error ? (
            <div className="space-y-3">
              <Alert variant="error">Couldn't search: {list.error.message}</Alert>
              <Button variant="outline" size="sm" onClick={() => list.mutate()}>
                <RefreshCw /> Try again
              </Button>
            </div>
          ) : list.data ? (
            <Choices
              key={list.submittedAt}
              data={list.data}
              movieId={movieId}
              versionId={versionId}
              upgrades={upgrades}
              onSearchAgain={() => list.mutate()}
            />
          ) : null}
        </DialogContent>
      </Dialog>
    </>
  );
}

function Choices({
  data,
  movieId,
  versionId,
  upgrades,
  onSearchAgain,
}: {
  data: NonNullable<ReturnType<typeof useMovieChoices>["data"]>;
  movieId: number;
  versionId: number;
  upgrades: boolean;
  onSearchAgain: () => void;
}) {
  const [q, setQ] = useState("");
  const [sort, setSort] = useState<Sort>("best");
  const [picked, setPicked] = useState<MovieChoice>();
  const [sent, setSent] = useState<Record<string, PickResult>>({});
  const grab = useGrabMovieChoice(movieId, versionId);

  const shown = useMemo(() => {
    const words = q.toLowerCase().split(/\s+/).filter(Boolean);
    const out = data.releases.filter((c) => {
      const text = `${c.title} ${c.quality} ${c.indexer}`.toLowerCase();
      return words.every((w) => text.includes(w));
    });
    const compare = sorts[sort].compare;
    return compare ? [...out].sort(compare) : out;
  }, [data.releases, q, sort]);

  function take(c: MovieChoice) {
    grab.mutate(c.token, {
      onSuccess: (res) => {
        setSent((cur) => ({ ...cur, [c.token]: res }));
        setPicked(undefined);
        toast(grabbedText(res), "ok");
      },
    });
  }

  if (picked) {
    return (
      <div className="space-y-3 overflow-y-auto">
        <h3 className="text-sm font-semibold">
          Download this release even though it's outside your profile?
        </h3>
        <p className="font-mono text-xs break-all">{picked.title}</p>
        <p className="text-sm">{picked.reasons.join(" ")}</p>
        {upgrades && (
          <p className="text-sm text-muted-foreground">
            Reelkeeper will keep this file and stop looking for better copies of this version (its
            Upgrades switch turns off). You can turn Upgrades back on at any time.
          </p>
        )}
        {grab.error && <Alert variant="error">{grab.error.message}</Alert>}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={() => setPicked(undefined)}>
            Back to the list
          </Button>
          <Button disabled={grab.isPending} onClick={() => take(picked)}>
            {grab.isPending ? "Sending…" : "Download it"}
          </Button>
        </div>
      </div>
    );
  }

  const left = [
    data.hidden > 0 &&
      `${plural(data.hidden, "release")} for another movie, on the blocklist, or with no seeders ${data.hidden === 1 ? "isn't" : "aren't"} shown.`,
    data.more > 0 &&
      `${plural(data.more, "more release")} further down the ranking ${data.more === 1 ? "isn't" : "aren't"} shown.`,
  ].filter(Boolean);

  return (
    <>
      <div className="flex flex-wrap items-center gap-2.5">
        <div className="relative min-w-48 flex-[1_1_220px]">
          <Search className="pointer-events-none absolute top-3 left-3 size-4 text-muted-foreground" />
          <Input
            type="search"
            aria-label="Filter releases"
            placeholder="Filter by name, quality or indexer"
            className="pl-9"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
        </div>
        <select
          aria-label="Sort releases"
          className={selectClass}
          value={sort}
          onChange={(e) => setSort(e.target.value as Sort)}
        >
          {Object.entries(sorts).map(([k, s]) => (
            <option key={k} value={k}>
              {s.label}
            </option>
          ))}
        </select>
        <Button variant="ghost" size="sm" onClick={onSearchAgain}>
          <RefreshCw /> Search again
        </Button>
      </div>
      {grab.error && <Alert variant="error">{grab.error.message}</Alert>}
      <div className="-mx-1 min-h-0 flex-1 space-y-3 overflow-y-auto px-1">
        {data.indexerErrors && data.indexerErrors.length > 0 && (
          <Alert variant="warning">
            Some indexers didn't answer, so this list may be incomplete:{" "}
            {data.indexerErrors.join("; ")}
          </Alert>
        )}
        {data.releases.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            Nothing found. Check Settings → Indexers, or try again later.
          </p>
        ) : shown.length === 0 ? (
          <p className="text-sm text-muted-foreground">No release matches the filter.</p>
        ) : (
          <ul className="space-y-2">
            {shown.map((c) => (
              <ChoiceRow
                key={c.token}
                c={c}
                sent={sent[c.token]}
                busy={grab.isPending}
                onGrab={() => {
                  grab.reset();
                  if (c.accepted) take(c);
                  else setPicked(c);
                }}
              />
            ))}
          </ul>
        )}
        {left.length > 0 && <p className="text-xs text-muted-foreground">{left.join(" ")}</p>}
      </div>
    </>
  );
}

function ChoiceRow({
  c,
  sent,
  busy,
  onGrab,
}: {
  c: MovieChoice;
  sent?: PickResult;
  busy: boolean;
  onGrab: () => void;
}) {
  return (
    <li className="rounded-lg border bg-muted/30 p-3">
      <p className="font-mono text-xs break-all">{c.title}</p>
      <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
        {c.accepted && <Badge variant="success">Meets your profile</Badge>}
        <Badge variant="info">{c.usenet ? "Usenet" : plural(c.seeders, "seeder")}</Badge>
        <span>{formatBytes(c.size)}</span>
        <span>{c.quality}</span>
        <span>{c.indexer}</span>
      </p>
      {c.reasons.length > 0 && <p className="mt-1 text-sm break-words">{c.reasons.join(" ")}</p>}
      <div className="mt-2">
        {sent ? (
          <Badge variant="success">
            {sent.dryRun ? "Recorded as a dry run" : "Sent to the download client"}
          </Badge>
        ) : (
          <Button
            variant={c.accepted ? "default" : "outline"}
            size="sm"
            disabled={busy}
            aria-label={`Grab ${c.title}`}
            onClick={onGrab}
          >
            Grab
          </Button>
        )}
      </div>
    </li>
  );
}
