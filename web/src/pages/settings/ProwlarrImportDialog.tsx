import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { api, RequestError, unwrap } from "@/api/client";
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
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  type ProwlarrCandidate,
  type ProwlarrImportResult,
  type ProwlarrPreview,
  solverQuery,
  useInvalidateIndexers,
} from "@/hooks/indexers";

const kindNames: Record<string, string> = {
  cardigann: "Tracker",
  torznab: "Torznab feed",
  newznab: "Newznab feed",
  knaben: "Knaben",
};

/** Copy indexers over from a running Prowlarr. Preview first, then import. */
export function ProwlarrImportDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const invalidateIndexers = useInvalidateIndexers();
  const solver = useQuery(solverQuery);
  const [url, setUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [preview, setPreview] = useState<ProwlarrPreview>();
  const [chosen, setChosen] = useState<Set<number>>(new Set());
  const [useSolver, setUseSolver] = useState(true);
  const [result, setResult] = useState<ProwlarrImportResult>();

  const read = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/indexers/import/prowlarr/preview", { body: { url, apiKey } })),
    onSuccess: (p) => {
      setPreview(p);
      setChosen(
        new Set(p.indexers.filter((c) => c.supported && !c.exists).map((c) => c.prowlarrId)),
      );
    },
  });
  const offerSolver = Boolean(preview?.solverUrl) && solver.data !== undefined && !solver.data.url;
  const run = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/indexers/import/prowlarr", {
          body: { url, apiKey, ids: [...chosen], useSolverSettings: offerSolver && useSolver },
        }),
      ),
    onSuccess: (r) => {
      setResult(r);
      invalidateIndexers();
      qc.invalidateQueries({ queryKey: ["settings", "solver"] });
    },
  });
  const readErr = read.error instanceof RequestError ? read.error : undefined;

  function submit(e: FormEvent) {
    e.preventDefault();
    read.mutate();
  }
  function toggle(id: number, on: boolean) {
    const next = new Set(chosen);
    if (on) next.add(id);
    else next.delete(id);
    setChosen(next);
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] max-w-xl overflow-y-auto">
        <DialogTitle>Import from Prowlarr</DialogTitle>
        <DialogDescription>
          Copies your indexers from a running Prowlarr so you don't have to set each one up again.
          Prowlarr is only read, never changed.
        </DialogDescription>

        {result ? (
          <ResultView result={result} preview={preview} onClose={onClose} />
        ) : !preview ? (
          <form onSubmit={submit} className="space-y-4">
            {read.error && !Object.keys(readErr?.fields ?? {}).length && (
              <Alert variant="error">{read.error.message}</Alert>
            )}
            <Field id="prowlarr-url" label="Prowlarr's address" error={readErr?.fields.url}>
              <Input
                id="prowlarr-url"
                value={url}
                placeholder="http://192.168.1.21:9696"
                onChange={(e) => setUrl(e.target.value)}
              />
            </Field>
            <Field
              id="prowlarr-key"
              label="API key"
              help="In Prowlarr: Settings → General → API Key."
              error={readErr?.fields.apiKey}
            >
              <Input
                id="prowlarr-key"
                type="password"
                autoComplete="off"
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
              />
            </Field>
            <div className="flex justify-end gap-2">
              <DialogClose asChild>
                <Button type="button" variant="ghost">
                  Cancel
                </Button>
              </DialogClose>
              <Button type="submit" disabled={!url.trim() || !apiKey.trim() || read.isPending}>
                {read.isPending ? "Reading…" : "Preview"}
              </Button>
            </div>
          </form>
        ) : (
          <div className="space-y-4">
            {preview.indexers.length === 0 && (
              <Alert variant="info">Prowlarr has no indexers to import.</Alert>
            )}
            <ul className="divide-y rounded-md border">
              {preview.indexers.map((c) => (
                <CandidateRow
                  key={c.prowlarrId}
                  c={c}
                  checked={chosen.has(c.prowlarrId)}
                  onChange={(on) => toggle(c.prowlarrId, on)}
                />
              ))}
            </ul>
            {offerSolver && (
              <label className="flex items-start gap-2 text-sm">
                <input
                  type="checkbox"
                  className="mt-0.5 accent-primary"
                  checked={useSolver}
                  onChange={(e) => setUseSolver(e.target.checked)}
                />
                Also set Reelkeeper's Cloudflare solver to {preview.solverUrl}
              </label>
            )}
            {run.error && <Alert variant="error">{run.error.message}</Alert>}
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="ghost"
                className="mr-auto"
                onClick={() => {
                  setPreview(undefined);
                  run.reset();
                }}
              >
                Back
              </Button>
              <DialogClose asChild>
                <Button type="button" variant="ghost">
                  Cancel
                </Button>
              </DialogClose>
              <Button disabled={chosen.size === 0 || run.isPending} onClick={() => run.mutate()}>
                {run.isPending
                  ? "Importing…"
                  : `Import ${chosen.size} ${chosen.size === 1 ? "indexer" : "indexers"}`}
              </Button>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function CandidateRow({
  c,
  checked,
  onChange,
}: {
  c: ProwlarrCandidate;
  checked: boolean;
  onChange: (on: boolean) => void;
}) {
  const disabled = !c.supported || c.exists;
  const id = `prowlarr-${c.prowlarrId}`;
  return (
    <li className="flex items-start gap-3 p-3">
      <input
        id={id}
        type="checkbox"
        className="mt-1 accent-primary"
        checked={checked && !disabled}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <label htmlFor={id} className="text-sm font-medium break-words">
            {c.name}
          </label>
          {c.kind && <Badge variant="neutral">{kindNames[c.kind] ?? c.kind}</Badge>}
          {c.useSolver && <Badge variant="info">Uses the solver</Badge>}
          {!c.enabled && <Badge variant="neutral">Off in Prowlarr</Badge>}
        </div>
        {!c.supported && (
          <p className="text-sm text-muted-foreground">
            Can't be imported{c.reason ? `: ${c.reason}` : "."}
          </p>
        )}
        {c.supported && c.exists && (
          <p className="text-sm text-muted-foreground">
            Skipped: Reelkeeper already has an indexer with this name.
          </p>
        )}
        {c.notes.map((n) => (
          <p key={n} className="text-sm text-muted-foreground">
            {n}
          </p>
        ))}
        {c.missingSecrets.length > 0 && (
          <p className="text-sm text-warning">
            Prowlarr doesn't share secrets: enter {c.missingSecrets.join(", ")} after importing.
          </p>
        )}
      </div>
    </li>
  );
}

function ResultView({
  result,
  preview,
  onClose,
}: {
  result: ProwlarrImportResult;
  preview?: ProwlarrPreview;
  onClose: () => void;
}) {
  const needSecrets = result.created
    .map((ix) => ({
      name: ix.name,
      secrets: preview?.indexers.find((c) => c.name === ix.name)?.missingSecrets ?? [],
    }))
    .filter((x) => x.secrets.length > 0);
  const nameOf = (id: number) =>
    preview?.indexers.find((c) => c.prowlarrId === id)?.name ?? `#${id}`;
  return (
    <div className="space-y-4">
      <Alert variant={result.created.length > 0 ? "success" : "info"}>
        <p>
          {result.created.length === 1
            ? "Imported 1 indexer."
            : `Imported ${result.created.length} indexers.`}
        </p>
      </Alert>
      {result.created.length > 0 && (
        <ul className="list-disc pl-5 text-sm">
          {result.created.map((ix) => (
            <li key={ix.id}>{ix.name}</li>
          ))}
        </ul>
      )}
      {needSecrets.length > 0 && (
        <Alert variant="warning">
          <p>
            Open Edit on these and enter the missing secrets, then press Test:{" "}
            {needSecrets.map((x) => `${x.name} (${x.secrets.join(", ")})`).join("; ")}.
          </p>
        </Alert>
      )}
      {result.skipped.length > 0 && (
        <div className="space-y-1">
          <p className="text-sm font-medium">Skipped</p>
          <ul className="list-disc pl-5 text-sm text-muted-foreground">
            {result.skipped.map((s) => (
              <li key={s.prowlarrId}>
                {nameOf(s.prowlarrId)}: {s.reason}
              </li>
            ))}
          </ul>
        </div>
      )}
      <div className="flex justify-end">
        <Button onClick={onClose}>Done</Button>
      </div>
    </div>
  );
}
