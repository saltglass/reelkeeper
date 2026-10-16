import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Download, Pencil, Plus, Trash2 } from "lucide-react";
import { type FormEvent, type ReactNode, useEffect, useState } from "react";
import { api, RequestError, unwrap } from "@/api/client";
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
import { Switch } from "@/components/ui/switch";
import {
  definitionsQuery,
  type Indexer,
  indexersQuery,
  inputFromIndexer,
  kindLabel,
  solverQuery,
  useInvalidateIndexers,
} from "@/hooks/indexers";
import { PageHeader } from "@/pages/PageHeader";
import { IndexerDialog } from "./IndexerDialog";
import { ProwlarrImportDialog } from "./ProwlarrImportDialog";

const typeLabel: Record<string, string> = {
  public: "Public",
  "semi-private": "Semi-private",
  private: "Private",
};

export function IndexersPage() {
  const indexers = useQuery(indexersQuery);
  const [editing, setEditing] = useState<Indexer | "new" | null>(null);
  const [deleting, setDeleting] = useState<Indexer | null>(null);
  const [importing, setImporting] = useState(false);
  const list = indexers.data ?? [];
  return (
    <div className="space-y-6">
      <PageHeader
        title="Indexers"
        description="Indexers are the places Reelkeeper searches for releases: torrent sites (trackers), feeds from tools like Jackett, and public search engines. Reelkeeper searches them by itself when something is requested, so there is no search page here."
      />
      <div className="flex flex-wrap gap-2">
        <Button onClick={() => setEditing("new")}>
          <Plus /> Add indexer
        </Button>
        <Button variant="outline" onClick={() => setImporting(true)}>
          <Download /> Import from Prowlarr
        </Button>
      </div>
      {indexers.error && <Alert variant="error">{indexers.error.message}</Alert>}
      {indexers.isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {indexers.data && list.length === 0 && (
        <Alert variant="info">
          No indexers yet. Use Add indexer to pick a tracker or enter a feed, or Import from
          Prowlarr to bring over the ones you already use.
        </Alert>
      )}
      {list.map((ix, i) => (
        <IndexerCard
          key={ix.id}
          index={i}
          indexer={ix}
          onEdit={() => setEditing(ix)}
          onDelete={() => setDeleting(ix)}
        />
      ))}
      <SolverCard />
      {editing && (
        <IndexerDialog
          key={editing === "new" ? "new" : editing.id}
          indexer={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
        />
      )}
      {deleting && <DeleteDialog indexer={deleting} onClose={() => setDeleting(null)} />}
      {importing && <ProwlarrImportDialog onClose={() => setImporting(false)} />}
    </div>
  );
}

const healthBadge = {
  ok: { variant: "success", label: "Working" },
  failing: { variant: "error", label: "Failing" },
  disabled: { variant: "neutral", label: "Off" },
  unknown: { variant: "neutral", label: "Not tested yet" },
} as const;

/** Average response time: milliseconds under a second, otherwise seconds. */
function avgLabel(ms: number) {
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`;
}

/** Bar colour and a short note for an average response time. */
function latency(ms: number) {
  if (ms <= 0) return { color: "bg-muted-foreground", note: "" };
  if (ms < 800) return { color: "bg-success", note: "Fast" };
  if (ms < 2500) return { color: "bg-warning", note: "A bit slow" };
  return { color: "bg-destructive", note: "Slow" };
}

function Stat({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="rounded-lg border bg-background px-3.5 py-3">
      <div className="text-xs text-muted-foreground">{label}</div>
      {children}
    </div>
  );
}

/** A button label that shows a spinner while it works and a tick when it last succeeded. */
function BusyLabel({ busy, ok, children }: { busy: boolean; ok: boolean; children: string }) {
  return (
    <>
      {busy && <span className="spin" aria-hidden="true" />}
      {!busy && ok && (
        <svg
          className="popin"
          width="14"
          height="14"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="3"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden="true"
        >
          <path d="M20 6 9 17l-5-5" />
        </svg>
      )}
      {children}
    </>
  );
}

function IndexerCard({
  indexer: ix,
  index,
  onEdit,
  onDelete,
}: {
  indexer: Indexer;
  index: number;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const invalidate = useInvalidateIndexers();
  const defs = useQuery(definitionsQuery);
  const solver = useQuery(solverQuery);
  const type = defs.data?.definitions.find((d) => d.id === ix.definition)?.type;
  const toggle = useMutation({
    mutationFn: (enabled: boolean) =>
      unwrap(
        api.PUT("/indexers/{id}", {
          params: { path: { id: ix.id } },
          body: inputFromIndexer(ix, { enabled }),
        }),
      ),
    onSuccess: invalidate,
  });
  const test = useMutation({
    mutationFn: () => unwrap(api.POST("/indexers/{id}/test", { params: { path: { id: ix.id } } })),
    onSettled: invalidate,
  });
  const badge = healthBadge[ix.health.status];
  const s = ix.stats;
  const lat = latency(s.avgMs);
  const solverMissing = ix.useSolver && solver.data !== undefined && !solver.data.url;
  return (
    <Card
      className="animate-rise transition-[border-color,box-shadow] hover:border-primary/40"
      style={{ animationDelay: `calc(min(${index}, 14) * 45ms * var(--m))` }}
    >
      <CardContent className="space-y-3">
        <div className="flex flex-wrap items-start gap-3">
          <div className="mr-auto min-w-0 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="font-medium break-words">{ix.name}</h2>
              <Badge variant={badge.variant}>{badge.label}</Badge>
              {type && <Badge variant="neutral">{typeLabel[type] ?? type}</Badge>}
              {ix.useSolver && <Badge variant="info">Cloudflare solver</Badge>}
            </div>
            <p className="text-sm text-muted-foreground">{kindLabel(ix)}</p>
          </div>
          <Switch
            aria-label={`Enable ${ix.name}`}
            checked={ix.enabled}
            disabled={toggle.isPending}
            onCheckedChange={(on) => toggle.mutate(on)}
          />
        </div>
        <div className="grid grid-cols-2 gap-3 pt-1 sm:grid-cols-4">
          <Stat label="Searches today">
            <div className="text-xl font-semibold tabular-nums">{s.queriesToday}</div>
          </Stat>
          <Stat label="Downloads today">
            <div className="text-xl font-semibold tabular-nums">{s.grabsToday}</div>
          </Stat>
          <Stat label="Average response">
            <div className="text-xl font-semibold tabular-nums">
              {s.avgMs > 0 ? avgLabel(s.avgMs) : "–"}
            </div>
            <div className="minibar mt-2 h-1 overflow-hidden rounded-full bg-muted">
              <i
                className={lat.color}
                style={{
                  width: `${s.avgMs > 0 ? Math.max(6, Math.min(100, (s.avgMs / 3000) * 100)) : 0}%`,
                }}
              />
            </div>
            {lat.note && <div className="mt-1 text-xs text-muted-foreground">{lat.note}</div>}
          </Stat>
          <Stat label="Failures in 30 days">
            <div className="text-xl font-semibold tabular-nums">{s.failures30d}</div>
          </Stat>
        </div>
        {s.queriesToday === 0 && s.grabsToday === 0 && s.avgMs === 0 && s.failures30d === 0 && (
          <p className="text-sm text-muted-foreground">No searches yet</p>
        )}
        {ix.health.status === "failing" && ix.health.message && (
          <Alert variant="error">{ix.health.message}</Alert>
        )}
        {ix.health.status !== "failing" && ix.health.message && (
          <p className="text-sm text-muted-foreground">{ix.health.message}</p>
        )}
        {solverMissing && (
          <Alert variant="warning">
            This indexer uses the Cloudflare solver, but no solver address is set. Add one in the
            Cloudflare solver card below.
          </Alert>
        )}
        {toggle.error && <Alert variant="error">{toggle.error.message}</Alert>}
        {test.error && <Alert variant="error">{test.error.message}</Alert>}
        {test.data && (
          <Alert variant={test.data.ok ? "success" : "error"}>{test.data.message}</Alert>
        )}
        <div className="flex flex-wrap justify-end gap-2">
          <Button
            variant="outline"
            size="sm"
            aria-label={`Test ${ix.name}`}
            disabled={test.isPending}
            onClick={() => test.mutate()}
          >
            <BusyLabel busy={test.isPending} ok={Boolean(test.data?.ok)}>
              {test.isPending ? "Testing…" : "Test"}
            </BusyLabel>
          </Button>
          <Button variant="outline" size="sm" aria-label={`Edit ${ix.name}`} onClick={onEdit}>
            <Pencil /> Edit
          </Button>
          <Button variant="ghost" size="sm" aria-label={`Delete ${ix.name}`} onClick={onDelete}>
            <Trash2 /> Delete
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

function DeleteDialog({ indexer, onClose }: { indexer: Indexer; onClose: () => void }) {
  const invalidate = useInvalidateIndexers();
  const remove = useMutation({
    mutationFn: () =>
      unwrap(api.DELETE("/indexers/{id}", { params: { path: { id: indexer.id } } })),
    onSuccess: () => {
      invalidate();
      onClose();
    },
    onError: invalidate,
  });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogTitle>Delete {indexer.name}?</DialogTitle>
        <DialogDescription>
          Reelkeeper forgets this indexer, its settings and its statistics. Nothing changes on the
          site itself, and you can add it again any time. To keep the settings but stop searching,
          turn it off instead.
        </DialogDescription>
        {remove.error && <Alert variant="error">{remove.error.message}</Alert>}
        <div className="flex justify-end gap-2">
          <DialogClose asChild>
            <Button variant="ghost">Cancel</Button>
          </DialogClose>
          <Button variant="destructive" disabled={remove.isPending} onClick={() => remove.mutate()}>
            Delete indexer
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

/** The one Cloudflare solver all indexers share. */
function SolverCard() {
  const { data } = useQuery(solverQuery);
  const [url, setUrl] = useState("");
  const [timeout, setTimeoutSeconds] = useState("60");
  const [advanced, setAdvanced] = useState(false);
  useEffect(() => {
    if (data) {
      setUrl(data.url);
      setTimeoutSeconds(String(data.timeoutSeconds));
    }
  }, [data]);
  const qc = useQueryClient();

  const save = useMutation({
    mutationFn: () =>
      unwrap(
        api.PUT("/settings/solver", {
          body: { url: url.trim(), timeoutSeconds: Number.parseInt(timeout, 10) || 60 },
        }),
      ),
    onSuccess: (d) => qc.setQueryData(solverQuery.queryKey, d),
  });
  const check = useMutation({
    mutationFn: () => unwrap(api.POST("/settings/solver/check", { body: { url: url.trim() } })),
  });
  const err = save.error instanceof RequestError ? save.error : undefined;
  const showAdvanced = advanced || Boolean(err?.fields.timeoutSeconds);

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate();
  }

  return (
    <Card className="animate-rise" style={{ animationDelay: "calc(140ms * var(--m))" }}>
      <CardHeader>
        <CardTitle>Cloudflare solver</CardTitle>
        <CardDescription>
          Some sites show a "checking your browser" page that Reelkeeper can't pass on its own. A
          solver such as TRAWL or FlareSolverr opens the page in a real browser for it. Enter its
          address once here, then switch on "Use the Cloudflare solver" for each site that needs it.
          Reelkeeper never solves these pages itself, and leaving this empty is fine if no site
          needs it.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} className="space-y-5">
          {err && !Object.keys(err.fields).length && <Alert variant="error">{err.message}</Alert>}
          <Field
            id="solver-url"
            label="Solver address"
            help="For example http://trawl:8191. Empty means no solver."
            error={err?.fields.url}
          >
            <Input
              id="solver-url"
              value={url}
              placeholder="http://trawl:8191"
              onChange={(e) => {
                setUrl(e.target.value);
                check.reset();
              }}
            />
          </Field>
          <div>
            <button
              type="button"
              aria-expanded={showAdvanced}
              onClick={() => setAdvanced(!showAdvanced)}
              className="flex items-center gap-1 text-sm font-medium text-link hover:underline [&_svg]:size-4"
            >
              {showAdvanced ? <ChevronDown /> : <ChevronRight />}
              {showAdvanced ? "Hide advanced" : "Show advanced"}
            </button>
          </div>
          {showAdvanced && (
            <Field
              id="solver-timeout"
              label="Timeout (seconds)"
              help="How long one page may take to solve, from 10 to 300. The default of 60 suits most sites."
              error={err?.fields.timeoutSeconds}
            >
              <Input
                id="solver-timeout"
                type="number"
                min="10"
                max="300"
                className="w-28"
                value={timeout}
                onChange={(e) => setTimeoutSeconds(e.target.value)}
              />
            </Field>
          )}
          {check.error && <Alert variant="error">{check.error.message}</Alert>}
          {check.data && (
            <Alert variant={check.data.ok ? "success" : "error"}>{check.data.message}</Alert>
          )}
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="outline"
              disabled={check.isPending}
              onClick={() => check.mutate()}
            >
              <BusyLabel busy={check.isPending} ok={Boolean(check.data?.ok)}>
                {check.isPending ? "Checking…" : "Check"}
              </BusyLabel>
            </Button>
            <Button type="submit" disabled={save.isPending}>
              {save.isPending ? "Saving…" : save.isSuccess ? "Saved" : "Save"}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
