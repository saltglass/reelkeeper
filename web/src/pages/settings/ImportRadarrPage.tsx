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

type Report = components["schemas"]["RadarrImportReport"];
type Issue = components["schemas"]["RadarrImportIssue"];

/** The kinds of disagreement, in the order they're shown. */
const groups: { kind: Issue["kind"]; title: string; help: string }[] = [
  {
    kind: "missing-in-reelkeeper",
    title: "Radarr has a file Reelkeeper didn't find",
    help: "Usually a folder Reelkeeper couldn't identify (check Review) or a media folder not added to Reelkeeper yet.",
  },
  {
    kind: "no-media-folder",
    title: "Can't be brought over",
    help: "Radarr keeps it in a folder that isn't one of Reelkeeper's media folders. Add that folder first (Settings → Media Folders), then import again.",
  },
  {
    kind: "other-file",
    title: "Different file",
    help: "Radarr and Reelkeeper each have a different file for this movie.",
  },
  {
    kind: "quality-differs",
    title: "Reelkeeper reads a different quality",
    help: "Reelkeeper trusts the file's own headers over its name; this is fine unless the file is mislabelled.",
  },
  {
    kind: "only-in-reelkeeper",
    title: "Only in Reelkeeper",
    help: "Reelkeeper found it, Radarr doesn't track it. It stays in Reelkeeper.",
  },
];

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/** Bring a Radarr's wanted movies, monitoring, tags, history and blocklist over. */
export function ImportRadarrPage() {
  const qc = useQueryClient();
  const profiles = useQuery(profilesQuery);
  const movieProfiles = profiles.data?.filter((p) => p.appliesTo !== "series") ?? [];
  const [url, setUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [chosen, setChosen] = useState<number>();
  // HD if there is one, until the user picks.
  const profileId = chosen ?? (movieProfiles.find((p) => p.name === "HD") ?? movieProfiles[0])?.id;
  const [report, setReport] = useState<Report>();
  const [confirming, setConfirming] = useState(false);

  const body = () => ({ url, apiKey, profileId: profileId ?? 0 });
  const preview = useMutation({
    mutationFn: () => unwrap(api.POST("/migrate/radarr/preview", { body: body() })),
    onSuccess: setReport,
  });
  const run = useMutation({
    mutationFn: () => unwrap(api.POST("/migrate/radarr/import", { body: body() })),
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
        title="Import from Radarr"
        description="Brings a Radarr's wanted movies, monitoring, tags, history and blocklist into Reelkeeper. Radarr is only read, never changed. Run it once for each Radarr."
      />
      <Card>
        <CardHeader>
          <CardTitle>Which Radarr?</CardTitle>
          <CardDescription>
            Reelkeeper finds your files by scanning, so this only adds what Radarr alone knows.
            Preview first to see what would happen.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit} className="space-y-4">
            <Field id="radarr-url" label="Radarr's address" error={fieldErr.url}>
              <Input
                id="radarr-url"
                value={url}
                placeholder="http://192.168.1.20:7979"
                onChange={(e) => edit(setUrl)(e.target.value)}
              />
            </Field>
            <Field
              id="radarr-key"
              label="API key"
              help="Radarr → Settings → General → API Key"
              error={fieldErr.apiKey}
            >
              <Input
                id="radarr-key"
                type="password"
                autoComplete="off"
                value={apiKey}
                onChange={(e) => edit(setApiKey)(e.target.value)}
              />
            </Field>
            <Field
              id="radarr-profile"
              label="Bring its movies into"
              help="The quality profile its movies get in Reelkeeper."
              error={fieldErr.profileId}
            >
              <select
                id="radarr-profile"
                className="w-full max-w-xs min-h-10 rounded-lg border border-input bg-background px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none"
                value={profileId ?? ""}
                onChange={(e) => edit(setChosen)(Number(e.target.value))}
              >
                {movieProfiles.map((p) => (
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
                  Reading Radarr… (this can take a minute)
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
            Bring over {plural(report?.toAdd ?? 0, "wanted movie")}, monitoring, tags, history and
            blocklist from {report?.instance}? Radarr isn't changed. Running it again won't
            duplicate anything.
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
    <Card>
      <CardHeader>
        <CardTitle>What was found</CardTitle>
        <CardDescription>
          {r.instance} (Radarr {r.version}) has {plural(r.movies, "movie")}: {r.withFile} with
          files, {r.wanted} wanted (monitored, no file yet). {plural(r.toAdd, "movie")}{" "}
          {r.toAdd === 1 ? "isn't" : "aren't"} in this profile yet;{" "}
          {plural(r.history, "history record")} and{" "}
          {plural(r.blocklist, "blocklist entry", "blocklist entries")} would be brought over.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {r.issues.length === 0 ? (
          <Alert variant="success">Radarr and Reelkeeper agree on every movie.</Alert>
        ) : (
          groups.map((g) => (
            <IssueGroup key={g.kind} group={g} issues={r.issues.filter((i) => i.kind === g.kind)} />
          ))
        )}
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
            <li key={`${i.tmdbId}-${i.kind}`} className="px-3 py-2 text-sm">
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
          Added {plural(r.added, "movie")}, changed monitoring on {r.monitorChanged}, brought over{" "}
          {plural(r.historyAdded, "history record")} and{" "}
          {plural(r.blocklistAdded, "blocklist entry", "blocklist entries")}.
        </p>
        <p className="flex gap-4 underline">
          <Link to="/movies">Movies</Link>
          <Link to="/activity/history">Activity → History</Link>
        </p>
      </div>
    </Alert>
  );
}
