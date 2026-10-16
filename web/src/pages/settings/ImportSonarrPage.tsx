import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronDown, ChevronRight } from "lucide-react";
import { type FormEvent, useState } from "react";
import { api, RequestError, unwrap } from "@/api/client";
import type { components } from "@/api/schema";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { profilesQuery } from "@/hooks/library";
import { PageHeader } from "@/pages/PageHeader";

type Report = components["schemas"]["SonarrImportReport"];
type Issue = components["schemas"]["SonarrImportIssue"];
type Wanted = components["schemas"]["SonarrWantedSeries"];

/** The kinds of disagreement, in the order they're shown. */
const groups: { kind: Issue["kind"]; title: string; help: string }[] = [
  {
    kind: "missing-in-reelkeeper",
    title: "In Sonarr but not in Reelkeeper",
    help: "Reelkeeper's scan didn't find it (check Review), or Sonarr has no TMDB number for it. If it's in one of Reelkeeper's TV folders, importing adds it.",
  },
  {
    kind: "no-media-folder",
    title: "Can't be brought over",
    help: "Sonarr keeps it in a folder that isn't one of Reelkeeper's TV folders. Add that folder first (Settings → Media Folders), then import again.",
  },
  {
    kind: "unmatched-episodes",
    title: "Episodes that couldn't be matched",
    help: "Reelkeeper couldn't tell which of its episodes these are, so Sonarr's monitoring for them isn't brought over. Reelkeeper keeps its own choice for them.",
  },
  {
    kind: "numbered-differently",
    title: "Numbered differently",
    help: "Sonarr numbers episodes in TVDB order and Reelkeeper in the order chosen for each series. These were matched by air date instead, so they are still brought over correctly.",
  },
  {
    kind: "files-differ",
    title: "Sonarr has files Reelkeeper doesn't",
    help: "Reelkeeper's scan found no file for these episodes. Check that its TV folders cover them. Until then Reelkeeper will look for them again.",
  },
  {
    kind: "only-in-reelkeeper",
    title: "Only in Reelkeeper",
    help: "Reelkeeper has it, Sonarr doesn't track it. It stays in Reelkeeper.",
  },
];

const plural = (n: number, one: string, many = `${one}s`) =>
  `${n.toLocaleString("en-US")} ${n === 1 ? one : many}`;

/** "S01: 3, S02: 6" */
const seasonSummary = (w: Wanted) =>
  w.seasons.map((s) => `S${String(s.season).padStart(2, "0")}: ${s.episodes}`).join(", ");

const total = (w: Wanted) => w.seasons.reduce((n, s) => n + s.episodes, 0);

/** Bring a Sonarr's monitored episodes, tags, history and blocklist over. */
export function ImportSonarrPage() {
  const qc = useQueryClient();
  const profiles = useQuery(profilesQuery);
  const tvProfiles = profiles.data?.filter((p) => p.appliesTo !== "movie") ?? [];
  const [url, setUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [chosen, setChosen] = useState<number>();
  // HDTV if there is one, until the user picks.
  const profileId = chosen ?? (tvProfiles.find((p) => p.name === "HDTV") ?? tvProfiles[0])?.id;
  const [report, setReport] = useState<Report>();
  const [confirming, setConfirming] = useState(false);

  const body = () => ({ url, apiKey, profileId: profileId ?? 0 });
  const preview = useMutation({
    mutationFn: () => unwrap(api.POST("/migrate/sonarr/preview", { body: body() })),
    onSuccess: setReport,
  });
  const run = useMutation({
    mutationFn: () => unwrap(api.POST("/migrate/sonarr/import", { body: body() })),
    onSuccess: (r) => {
      setReport(r);
      qc.invalidateQueries();
    },
  });
  const busy = preview.isPending || run.isPending;
  const error = preview.error ?? run.error;
  const fieldErr = error instanceof RequestError ? error.fields : {};
  const showBanner = error && Object.keys(fieldErr).length === 0;

  // Changing anything after a preview makes that preview out of date.
  function edit<T>(set: (v: T) => void) {
    return (v: T) => {
      set(v);
      setReport(undefined);
      preview.reset();
      run.reset();
    };
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    run.reset();
    preview.mutate();
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Import from Sonarr"
        description="Brings what Sonarr was monitoring into Reelkeeper, including older episodes it never got, plus tags, history and the blocklist. Sonarr is only read, never changed."
      />
      <Card>
        <CardHeader>
          <CardTitle>Which Sonarr?</CardTitle>
          <CardDescription>
            Reelkeeper finds your files by scanning, so this only adds what Sonarr alone knows.
            Preview first to see what would happen.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit} className="space-y-4">
            <Field id="sonarr-url" label="Sonarr's address" error={fieldErr.url}>
              <Input
                id="sonarr-url"
                value={url}
                placeholder="http://192.168.1.20:8989"
                onChange={(e) => edit(setUrl)(e.target.value)}
              />
            </Field>
            <Field
              id="sonarr-key"
              label="API key"
              help="Sonarr → Settings → General → API Key"
              error={fieldErr.apiKey}
            >
              <Input
                id="sonarr-key"
                type="password"
                autoComplete="off"
                value={apiKey}
                onChange={(e) => edit(setApiKey)(e.target.value)}
              />
            </Field>
            <Field
              id="sonarr-profile"
              label="Bring its series into"
              help="The quality profile its series get in Reelkeeper."
              error={fieldErr.profileId}
            >
              <select
                id="sonarr-profile"
                className="w-full max-w-xs min-h-10 rounded-lg border border-input bg-background px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none"
                value={profileId ?? ""}
                onChange={(e) => edit(setChosen)(Number(e.target.value))}
              >
                {tvProfiles.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            </Field>
            {showBanner && <Alert variant="error">{error.message}</Alert>}
            <div className="flex flex-wrap items-center gap-2">
              <Button type="submit" disabled={busy}>
                Preview
              </Button>
              {report && !report.imported && (
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => setConfirming(true)}
                >
                  Import
                </Button>
              )}
              {busy && (
                <span role="status" className="text-sm text-muted-foreground">
                  Reading Sonarr… (this can take a minute)
                </span>
              )}
            </div>
          </form>
        </CardContent>
      </Card>

      {report && !report.imported && <PreviewView report={report} />}
      {report?.imported && <ResultView report={report} />}

      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent>
          <DialogTitle>Bring it over?</DialogTitle>
          <DialogDescription>
            Reelkeeper will start looking for {plural(report?.newlyWanted ?? 0, "older episode")}{" "}
            that {report?.instance} was monitoring. Nothing is searched right away: the scheduled
            search picks a few each hour, and only while the queue has room (see the queue limit in
            Download Client). This also brings over monitoring, tags, history and the blocklist.{" "}
            {report?.instance} isn't changed, and running it again won't duplicate anything.
          </DialogDescription>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button
              onClick={() => {
                setConfirming(false);
                run.mutate();
              }}
            >
              Import
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function PreviewView({ report: r }: { report: Report }) {
  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle>What was found</CardTitle>
          <CardDescription>
            Sonarr {r.version}
            {r.instance !== "Sonarr" && ` (${r.instance})`}: {plural(r.series, "series", "series")},{" "}
            {plural(r.episodes, "episode")}.{" "}
            {r.newlyWanted > 0
              ? `Reelkeeper will start looking for ${plural(r.newlyWanted, "older episode")} Sonarr was monitoring.`
              : "Reelkeeper already wants everything Sonarr is monitoring that has aired, so nothing older will be searched for."}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-1 text-sm text-muted-foreground">
          {r.toAdd > 0 && (
            <p>
              {plural(r.toAdd, "series", "series")} {r.toAdd === 1 ? "isn't" : "aren't"} in this
              profile yet; the ones in a TV folder Reelkeeper has would be added.
            </p>
          )}
          {r.matchedByDate > 0 && (
            <p>
              {plural(r.matchedByDate, "episode")} {r.matchedByDate === 1 ? "is" : "are"} numbered
              differently in the two apps and matched by air date.
            </p>
          )}
          {r.unmatched > 0 && (
            <p>
              {plural(r.unmatched, "episode")} couldn't be matched, so{" "}
              {r.unmatched === 1 ? "its" : "their"} monitoring isn't brought over.
            </p>
          )}
          {r.turnedOff > 0 && (
            <p>
              {plural(r.turnedOff, "episode")} Reelkeeper wants would be turned off, because Sonarr
              doesn't monitor {r.turnedOff === 1 ? "it" : "them"}.
            </p>
          )}
          <p>
            {plural(r.history, "history record")} and{" "}
            {plural(r.blocklist, "blocklist entry", "blocklist entries")} would be brought over.
          </p>
        </CardContent>
      </Card>

      {r.wanted.length > 0 && <WantedCard wanted={r.wanted} />}

      <Card>
        <CardHeader>
          <CardTitle>Where Sonarr and Reelkeeper differ</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {r.issues.length === 0 ? (
            <Alert variant="success">Sonarr and Reelkeeper agree on every series.</Alert>
          ) : (
            groups.map((g) => (
              <IssueGroup
                key={g.kind}
                group={g}
                issues={r.issues.filter((i) => i.kind === g.kind)}
              />
            ))
          )}
        </CardContent>
      </Card>
    </>
  );
}

/** The episodes Reelkeeper will start looking for, a row per series. */
function WantedCard({ wanted }: { wanted: Wanted[] }) {
  const [open, setOpen] = useState<Set<string>>(new Set());
  const key = (w: Wanted) => `${w.title}-${w.year}`;
  const toggle = (k: string) => {
    const next = new Set(open);
    if (!next.delete(k)) next.add(k);
    setOpen(next);
  };
  const allOpen = open.size === wanted.length;
  return (
    <Card>
      <CardHeader>
        <CardTitle>Older episodes Reelkeeper will look for</CardTitle>
        <CardDescription>
          Aired already, monitored in Sonarr, and without a file in Reelkeeper.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => setOpen(allOpen ? new Set() : new Set(wanted.map(key)))}
        >
          {allOpen ? "Collapse all" : "Expand all"}
        </Button>
        <ul className="rounded-md border divide-y">
          {wanted.map((w) => {
            const isOpen = open.has(key(w));
            return (
              <li key={key(w)}>
                <button
                  type="button"
                  aria-expanded={isOpen}
                  onClick={() => toggle(key(w))}
                  className="flex w-full items-start gap-2 p-3 text-left"
                >
                  <span className="mt-0.5 text-muted-foreground [&_svg]:size-4">
                    {isOpen ? <ChevronDown /> : <ChevronRight />}
                  </span>
                  <span className="min-w-0 flex-1 font-medium break-words">
                    {w.title} ({w.year})
                  </span>
                  <Badge variant="neutral">{plural(total(w), "episode")}</Badge>
                </button>
                {isOpen && (
                  <p className="px-3 pb-3 pl-9 text-sm text-muted-foreground">{seasonSummary(w)}</p>
                )}
              </li>
            );
          })}
        </ul>
      </CardContent>
    </Card>
  );
}

function IssueGroup({ group, issues }: { group: (typeof groups)[number]; issues: Issue[] }) {
  const [open, setOpen] = useState(false);
  if (issues.length === 0) return null;
  return (
    <div className="rounded-md border">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="flex w-full items-start gap-2 p-3 text-left"
      >
        <span className="mt-0.5 text-muted-foreground [&_svg]:size-4">
          {open ? <ChevronDown /> : <ChevronRight />}
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex flex-wrap items-center gap-2 font-medium">
            {group.title} <Badge variant="neutral">{issues.length}</Badge>
          </span>
          <span className="block text-sm text-muted-foreground">{group.help}</span>
        </span>
      </button>
      {open && (
        <ul className="divide-y border-t">
          {issues.map((i) => (
            <li key={`${i.title}-${i.year}-${i.kind}`} className="px-3 py-2 text-sm">
              <p className="font-medium break-words">
                {i.title} ({i.year})
              </p>
              <p className="break-words text-muted-foreground">{i.detail}</p>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function ResultView({ report: r }: { report: Report }) {
  return (
    <Alert variant="success">
      <div className="space-y-2">
        <p>
          Added {plural(r.added, "series", "series")}, changed monitoring on {r.monitorChanged},
          turned {plural(r.episodesTurnedOn, "episode")} on and {r.episodesTurnedOff} off, and
          brought over {plural(r.historyAdded, "history record")} and{" "}
          {plural(r.blocklistAdded, "blocklist entry", "blocklist entries")}.
        </p>
        <p>
          Reelkeeper will now look for {plural(r.newlyWanted, "older episode")}, a few each hour.
        </p>
        <p className="flex gap-4 underline">
          <Link to="/series">Series</Link>
          <Link to="/activity/history">Activity → History</Link>
        </p>
      </div>
    </Alert>
  );
}
