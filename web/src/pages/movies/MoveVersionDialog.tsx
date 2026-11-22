import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowDown, FolderInput } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api, unwrap } from "@/api/client";
import type { components } from "@/api/schema";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { Progress } from "@/components/ui/progress";
import { rootsQuery, useInvalidateLibrary } from "@/hooks/library";
import { type Job, type TasksOverview, tasksQuery } from "@/hooks/tasks";
import { formatBytes } from "@/lib/format";

const selectClass =
  "min-h-10 w-full rounded-lg border border-input bg-card px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none";

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

/**
 * "Move…" for one movie version: moves its files to another movie media
 * folder (say, out of the kids' folder). Shows the plan first: what moves,
 * whether it's a rename (same disk) or a copy (the old copy is deleted
 * after), and anything that stops it. The move itself runs as a task, so
 * the dialog follows it until it's done. Hidden when there's no other movie
 * folder to move to.
 */
export function MoveVersion({
  movieId,
  versionId,
  rootFolderId,
  profileName,
}: {
  movieId: number;
  versionId: number;
  /** The media folder the version is in now. */
  rootFolderId: number;
  profileName: string;
}) {
  const [open, setOpen] = useState(false);
  const roots = useQuery(rootsQuery);
  const others = (roots.data ?? []).filter((r) => r.mediaType === "movie" && r.id !== rootFolderId);
  if (others.length === 0) return null;
  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
        <FolderInput /> Move…
      </Button>
      {open && (
        <MoveDialog
          movieId={movieId}
          versionId={versionId}
          profileName={profileName}
          choices={others}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  );
}

function MoveDialog({
  movieId,
  versionId,
  profileName,
  choices,
  onClose,
}: {
  movieId: number;
  versionId: number;
  profileName: string;
  choices: { id: number; path: string }[];
  onClose: () => void;
}) {
  const invalidate = useInvalidateLibrary();
  const [target, setTarget] = useState(choices[0]?.id ?? 0);
  const path = { id: movieId, versionId };
  const plan = useQuery({
    // Not under ["movies"]: the library refresh after the move must leave
    // the plan as it was (what was moved), not re-plan from the new folder.
    queryKey: ["move-plan", movieId, versionId, target],
    queryFn: () =>
      unwrap(
        api.GET("/movies/{id}/versions/{versionId}/move", {
          params: { path, query: { rootFolderId: target } },
        }),
      ),
    staleTime: 0,
  });
  const move = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/movies/{id}/versions/{versionId}/move", {
          params: { path },
          body: { rootFolderId: target },
        }),
      ),
  });

  // The move runs as a task; live events keep the task list current, and
  // a slow poll covers a dropped event stream.
  const jobId = move.data?.id;
  const tasks = useQuery({
    ...tasksQuery,
    enabled: jobId !== undefined,
    refetchInterval: (q) => {
      const j = findJob(q.state.data, jobId);
      return j && (j.status === "succeeded" || j.status === "failed" || j.status === "cancelled")
        ? false
        : 3000;
    },
  });
  const job = findJob(tasks.data, jobId) ?? move.data;
  const done = job?.status === "succeeded";
  const failed = job?.status === "failed" || job?.status === "cancelled";
  // The movie page shows the new folder once the move is done.
  // Once: the invalidate function is new on every render.
  const finished = done || failed;
  const refreshed = useRef(false);
  useEffect(() => {
    if (finished && !refreshed.current) {
      refreshed.current = true;
      invalidate();
    }
  }, [finished, invalidate]);

  const p = plan.data;
  const started = move.isSuccess;
  const dest = choices.find((c) => c.id === target)?.path ?? "";

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-2xl">
        <DialogTitle className="pr-6">Move to another media folder</DialogTitle>
        <DialogDescription>
          Moves the {profileName} version's files. The movie's folder keeps its name, and nothing is
          ever overwritten. Its quality profile, monitoring and history stay as they are.
        </DialogDescription>

        <label className="block space-y-1 text-sm">
          <span className="font-medium">Move to</span>
          <select
            className={selectClass}
            value={target}
            disabled={started}
            onChange={(e) => setTarget(Number(e.target.value))}
          >
            {choices.map((c) => (
              <option key={c.id} value={c.id}>
                {c.path}
              </option>
            ))}
          </select>
        </label>

        {plan.error && <Alert variant="error">{plan.error.message}</Alert>}
        {plan.isLoading && <p className="text-sm text-muted-foreground">Working it out…</p>}

        {p && (
          <div className="space-y-3">
            <div className="space-y-1 rounded-md border p-2 text-xs">
              <p className="font-mono break-all text-muted-foreground">
                {joinPath(p.fromPath, p.folder)}
              </p>
              <ArrowDown className="size-3 text-muted-foreground" />
              <p className="font-mono break-all">{joinPath(p.toPath, p.folder)}</p>
            </div>
            <p className="text-sm">{whatMoves(p)}</p>
            {p.files.length > 0 && (
              <p className="text-sm text-muted-foreground">
                {p.copy
                  ? `These folders are on different disks, so the files are copied, each copy checked, and then the old copy is deleted.${p.freeSpace >= 0 ? ` ${formatBytes(p.freeSpace)} free in ${p.toPath}.` : ""}`
                  : "Both folders are on the same disk, so the files are renamed into place: instant, nothing copied."}
              </p>
            )}
            {p.files.length > 0 && (
              <details className="text-xs">
                <summary className="cursor-pointer text-muted-foreground">
                  Show the {plural(p.files.length, "file")}
                </summary>
                <ul className="mt-1 max-h-48 space-y-0.5 overflow-y-auto font-mono break-all">
                  {p.files.map((f) => (
                    <li key={f.path}>
                      {f.path}{" "}
                      <span className="text-muted-foreground">({formatBytes(f.size)})</span>
                    </li>
                  ))}
                </ul>
              </details>
            )}
            {p.problems.length > 0 && !started && (
              <Alert variant="warning">
                <p>It can't be moved to {dest} yet:</p>
                <ul className="mt-1 list-disc space-y-1 pl-5">
                  {p.problems.map((x) => (
                    <li key={x}>{x}</li>
                  ))}
                </ul>
                {p.problems.some((x) => x.includes("Settings → Media Folders")) && (
                  <Link to="/settings/media" className="mt-1 inline-block underline">
                    Open Settings → Media Folders
                  </Link>
                )}
              </Alert>
            )}
          </div>
        )}

        {move.error && <Alert variant="error">{move.error.message}</Alert>}
        {started && job && !done && !failed && (
          <div className="space-y-1">
            <Progress value={job.status === "running" ? job.progress : 0} label="Move progress" />
            <p role="status" className="text-xs text-muted-foreground">
              {job.status === "queued" ? "Waiting to start" : job.message || "Moving…"}
            </p>
            <p className="text-xs text-muted-foreground">
              You can close this; the move carries on (System → Tasks).
            </p>
          </div>
        )}
        {done && <Alert variant="success">{job.message}</Alert>}
        {failed && <Alert variant="error">{job.error || "The move was cancelled."}</Alert>}

        <div className="flex justify-end gap-2">
          <DialogClose asChild>
            <Button variant={started || !canMove(p) ? "default" : "ghost"}>
              {started || !canMove(p) ? "Close" : "Cancel"}
            </Button>
          </DialogClose>
          {!started && canMove(p) && (
            <Button disabled={move.isPending} onClick={() => move.mutate()}>
              {move.isPending ? "Starting…" : "Move"}
            </Button>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

type Plan = components["schemas"]["MovePlan"];

const canMove = (p: Plan | undefined) => p !== undefined && p.problems.length === 0;

function findJob(o: TasksOverview | undefined, id: number | undefined): Job | undefined {
  if (!o || id === undefined) return undefined;
  return (
    o.recent.find((j) => j.id === id) ??
    o.tasks.map((t) => t.active).find((j): j is Job => j?.id === id)
  );
}

function joinPath(root: string, folder: string) {
  return folder ? `${root}/${folder}` : root;
}

/** What the move takes along, in a sentence. */
function whatMoves(p: Plan) {
  const size = formatBytes(p.bytes);
  if (p.files.length === 0)
    return `Nothing is on disk yet, so only the folder changes: downloads will go to ${p.toPath}.`;
  if (p.wholeFolder)
    return `Everything in the movie's folder moves, subtitles, artwork and extras included: ${plural(p.files.length, "file")}, ${size}.`;
  return `This version shares its folder with another version, so only its video and the files named after it move: ${plural(p.files.length, "file")}, ${size}.`;
}
