// Selecting several movies or shows on their pages and changing them at once
// (monitoring, upgrades, which episodes to monitor).

import { Check, ChevronDown } from "lucide-react";
import { useMemo, useState } from "react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { type MovieSummary, type SeriesSummary, useBulkEdit } from "@/hooks/library";
import { toast } from "@/hooks/toast";
import { cn } from "@/lib/utils";

/** How many newly wanted titles one change searches at once (the server's limit). */
export const SEARCHES_NOW = 10;

export type BulkKind = "movie" | "series";
type Title = MovieSummary | SeriesSummary;

/** A change to make: one switch, or (series) an episode preset. */
export type BulkChange =
  | { monitored: boolean }
  | { upgrades: boolean }
  | { monitor: "all" | "future" | "none" };

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
const noun = (kind: BulkKind, n: number) => plural(n, kind === "movie" ? "movie" : "show");

/** The selection on a page: which titles are ticked, and whether ticking is on. */
export function useSelection() {
  const [selecting, setSelecting] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  return {
    selecting,
    selected,
    start: () => setSelecting(true),
    stop: () => {
      setSelecting(false);
      setSelected(new Set());
    },
    toggle: (id: number) =>
      setSelected((s) => {
        const next = new Set(s);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        return next;
      }),
    set: (ids: number[]) => setSelected(new Set(ids)),
  };
}
export type Selection = ReturnType<typeof useSelection>;

/** The tick shown on a poster while selecting. */
export function PosterCheck({ checked }: { checked: boolean }) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        "absolute top-2.5 right-2.5 grid size-6 place-items-center rounded-full border-2 border-white shadow-md",
        checked ? "bg-primary text-primary-foreground" : "bg-black/40",
      )}
    >
      {checked && <Check className="size-4" strokeWidth={3} />}
    </span>
  );
}

/**
 * The bar under the list while selecting: how many are ticked, "select all
 * shown", the changes, and Done.
 */
export function SelectionBar({
  kind,
  sel,
  shown,
}: {
  kind: BulkKind;
  sel: Selection;
  shown: Title[];
}) {
  const [change, setChange] = useState<BulkChange | null>(null);
  const chosen = shown.filter((t) => sel.selected.has(t.id));
  const allChosen = shown.length > 0 && chosen.length === shown.length;
  if (!sel.selecting) return null;
  const none = chosen.length === 0;
  return (
    <>
      <Card
        role="region"
        aria-label={`Change the selected ${kind === "movie" ? "movies" : "shows"}`}
        className="sticky bottom-4 z-30 mt-4 flex flex-wrap items-center gap-2 px-4 py-3 shadow-lg"
      >
        <b className="text-sm tabular-nums">{chosen.length} selected</b>
        <Button
          variant="ghost"
          onClick={() => sel.set(allChosen ? [] : shown.map((t) => t.id))}
          className="mr-auto"
        >
          {allChosen ? "Select none" : `Select all ${shown.length} shown`}
        </Button>
        {/* On a phone: the count, "select all" and Done on one row, the changes on the next. */}
        <Button variant="ghost" onClick={sel.stop} className="sm:order-last">
          Done
        </Button>
        <div className="flex w-full flex-wrap gap-2 sm:w-auto">
          <DropdownMenu>
            <DropdownMenuTrigger asChild disabled={none}>
              <Button variant="outline">
                Monitoring <ChevronDown />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={() => setChange({ monitored: true })}>
                Monitor
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={() => setChange({ monitored: false })}>
                Stop monitoring
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          {kind === "movie" ? (
            <DropdownMenu>
              <DropdownMenuTrigger asChild disabled={none}>
                <Button variant="outline">
                  Upgrades <ChevronDown />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={() => setChange({ upgrades: true })}>
                  Turn upgrades on
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={() => setChange({ upgrades: false })}>
                  Turn upgrades off
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          ) : (
            <DropdownMenu>
              <DropdownMenuTrigger asChild disabled={none}>
                <Button variant="outline">
                  Episodes <ChevronDown />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={() => setChange({ monitor: "all" })}>
                  Monitor all episodes
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={() => setChange({ monitor: "future" })}>
                  Monitor future episodes only
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={() => setChange({ monitor: "none" })}>
                  Monitor no episodes
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
      </Card>
      {change && (
        <BulkEditDialog
          kind={kind}
          titles={chosen}
          change={change}
          onClose={() => setChange(null)}
        />
      )}
    </>
  );
}

/** What a change does, in words: the dialog's title and its explanation. */
function describe(kind: BulkKind, n: number, change: BulkChange): [string, string] {
  const what = noun(kind, n);
  if ("monitored" in change) {
    return change.monitored
      ? [`Monitor ${what}?`, "Reelkeeper looks for anything they're missing."]
      : [
          `Stop monitoring ${what}?`,
          "Reelkeeper stops looking for them. Nothing is deleted, and you can turn them back on.",
        ];
  }
  if ("upgrades" in change) {
    return change.upgrades
      ? [
          `Turn upgrades on for ${what}?`,
          "Reelkeeper replaces their files with better releases, up to each profile's “upgrade until” quality.",
        ]
      : [
          `Turn upgrades off for ${what}?`,
          "Reelkeeper keeps the files they have and doesn't look for better copies. A movie whose file goes missing is still downloaded again.",
        ];
  }
  const presets: Record<"all" | "future" | "none", [string, string]> = {
    all: [`Monitor all episodes of ${what}?`, "Every episode is wanted, aired or not."],
    future: [
      `Monitor only future episodes of ${what}?`,
      "Only episodes airing from today are wanted.",
    ],
    none: [
      `Monitor no episodes of ${what}?`,
      "No episodes are wanted until you turn seasons or episodes on.",
    ],
  };
  const preset = presets[change.monitor];
  return [preset[0], `${preset[1]} This replaces any choices made per season or episode.`];
}

/** The confirm step: what will change, which versions, and what gets searched. */
export function BulkEditDialog({
  kind,
  titles,
  change,
  onClose,
}: {
  kind: BulkKind;
  titles: Title[];
  change: BulkChange;
  onClose: () => void;
}) {
  const edit = useBulkEdit();
  const [profile, setProfile] = useState(0); // 0: every version
  // The profiles among the chosen titles' versions, with how many each has.
  const profiles = useMemo(() => {
    const m = new Map<number, { name: string; n: number }>();
    for (const t of titles)
      for (const v of t.versions) {
        const p = m.get(v.profileId) ?? { name: v.profileName, n: 0 };
        p.n++;
        m.set(v.profileId, p);
      }
    return [...m].sort((a, b) => a[1].name.localeCompare(b[1].name));
  }, [titles]);
  const totalVersions = titles.reduce((n, t) => n + t.versions.length, 0);
  const versions = titles.flatMap((t) =>
    t.versions.filter((v) => profile === 0 || v.profileId === profile),
  );
  const changedTitles = titles.filter((t) =>
    t.versions.some((v) => profile === 0 || v.profileId === profile),
  ).length;

  // Already as asked (for a switch), and how many the change may make wanted.
  let already = 0;
  let turnedOn = 0;
  if ("monitored" in change) {
    already = versions.filter((v) => v.monitored === change.monitored).length;
    if (change.monitored) turnedOn = versions.filter((v) => !v.monitored).length;
  } else if ("upgrades" in change) {
    already = versions.filter((v) => v.upgrades === change.upgrades).length;
    if (change.upgrades) turnedOn = versions.filter((v) => v.monitored && !v.upgrades).length;
  } else if (change.monitor !== "none") {
    turnedOn = versions.filter((v) => v.monitored).length;
  }
  const [title, explain] = describe(kind, changedTitles, change);
  // "1 is already monitored." / "All already have upgrades off."
  let alreadyText = "";
  if (already > 0) {
    const all = already === versions.length;
    const one = already === 1 && !all;
    const who = all ? "All" : String(already);
    if ("monitored" in change)
      alreadyText = `${who} ${one ? "is" : "are"} already ${change.monitored ? "monitored" : "not monitored"}.`;
    else if ("upgrades" in change)
      alreadyText = `${who} already ${one ? "has" : "have"} upgrades ${change.upgrades ? "on" : "off"}.`;
  }

  function confirm() {
    const titleIds = titles.map((t) => t.id);
    const profileId = profile === 0 ? undefined : profile;
    edit.mutate(
      kind === "movie"
        ? { kind, body: { titleIds, profileId, ...("monitor" in change ? {} : change) } }
        : { kind, body: { titleIds, profileId, ...("upgrades" in change ? {} : change) } },
      {
        onSuccess: (res) => {
          let text = `Changed ${plural(res.versions, "version")} of ${noun(kind, res.titles)}.`;
          if (res.searched > 0) text += ` Searching for ${res.searched} now`;
          if (res.later > 0)
            text += `${res.searched > 0 ? "; " : " "}${res.later} more by the hourly search`;
          if (res.searched > 0 || res.later > 0) text += ".";
          toast(text, "ok");
          onClose();
        },
      },
    );
  }

  const err = edit.error instanceof RequestError ? edit.error : undefined;
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>{explain}</DialogDescription>
        {profiles.length > 1 && (
          <label className="grid gap-1.5 text-sm">
            <span className="font-medium">Which versions</span>
            <select
              className="min-h-10 rounded-lg border border-input bg-card px-3 text-sm"
              value={profile}
              onChange={(e) => setProfile(Number(e.target.value))}
            >
              <option value={0}>All versions ({totalVersions})</option>
              {profiles.map(([id, p]) => (
                <option key={id} value={id}>
                  Only {p.name} versions ({p.n})
                </option>
              ))}
            </select>
          </label>
        )}
        <ul className="list-disc space-y-1 pl-5 text-sm text-muted-foreground">
          {versions.length !== changedTitles && (
            <li>This changes {plural(versions.length, "version")}.</li>
          )}
          {alreadyText && <li>{alreadyText}</li>}
          {turnedOn > SEARCHES_NOW && (
            <li>
              Up to {SEARCHES_NOW} that are missing something are searched now, one after another;
              the rest by the hourly search, a few each hour.
            </li>
          )}
          {turnedOn > 0 && turnedOn <= SEARCHES_NOW && (
            <li>Any that are missing something are searched now.</li>
          )}
        </ul>
        {err && <Alert variant="error">{err.message}</Alert>}
        <div className="flex justify-end gap-2">
          <DialogClose asChild>
            <Button variant="ghost">Cancel</Button>
          </DialogClose>
          <Button disabled={edit.isPending || versions.length === 0} onClick={confirm}>
            {edit.isPending ? "Saving…" : "Change"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
