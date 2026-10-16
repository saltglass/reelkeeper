import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Play, Square } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api, unwrap } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { type Job, type Task, tasksQuery, tasksWithRoutineQuery } from "@/hooks/tasks";
import { everyLabel, relativeTime, shortDuration } from "@/lib/time";
import { PageHeader } from "@/pages/PageHeader";

const statusText: Record<Job["status"], string> = {
  queued: "Waiting",
  running: "Running",
  succeeded: "Done",
  failed: "Failed",
  cancelled: "Cancelled",
};

const statusVariant = {
  queued: "neutral",
  running: "info",
  succeeded: "success",
  failed: "error",
  cancelled: "warning",
} as const;

const triggerText: Record<Job["trigger"], string> = {
  manual: "started by hand",
  schedule: "on schedule",
  system: "started by Reelkeeper",
  api: "asked for by another app (such as Seerr)",
};

export function JobStatus({ job }: { job: Job }) {
  return <Badge variant={statusVariant[job.status]}>{statusText[job.status]}</Badge>;
}

function duration(job: Job): string | undefined {
  if (!job.startedAt) return undefined;
  const end = job.finishedAt ? new Date(job.finishedAt).getTime() : Date.now();
  return shortDuration((end - new Date(job.startedAt).getTime()) / 1000);
}

export function TasksPage() {
  const [showRoutine, setShowRoutine] = useState(false);
  const { data, error } = useQuery({
    ...(showRoutine ? tasksWithRoutineQuery : tasksQuery),
    refetchInterval: 60_000,
  });
  const qc = useQueryClient();
  // The task that was just started, so its row can show "Started" for a moment.
  const [justRan, setJustRan] = useState<string | null>(null);
  const doneTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(doneTimer.current), []);
  const run = useMutation({
    mutationFn: (kind: string) =>
      unwrap(api.POST("/system/tasks/{kind}/run", { params: { path: { kind } } })),
    onSuccess: (_d, kind) => {
      setJustRan(kind);
      clearTimeout(doneTimer.current);
      doneTimer.current = setTimeout(() => setJustRan(null), 2500);
      return qc.invalidateQueries({ queryKey: tasksQuery.queryKey });
    },
  });
  const cancel = useMutation({
    mutationFn: (id: number) =>
      unwrap(api.POST("/system/jobs/{id}/cancel", { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: tasksQuery.queryKey }),
  });

  if (error) return <Alert variant="error">Couldn't load tasks: {error.message}</Alert>;
  if (!data) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const actionError = run.error ?? cancel.error;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Tasks"
        description="Work Reelkeeper does in the background. Tasks keep going if you close this page, and pick up again after a restart."
      />
      {actionError && <Alert variant="error">{actionError.message}</Alert>}

      <Card>
        <CardHeader>
          <CardTitle>Tasks</CardTitle>
          <CardDescription>
            Scheduled tasks run on their own. Use "Run now" to start one early.
          </CardDescription>
        </CardHeader>
        <CardContent className="p-0 pt-4">
          {data.tasks.map((t, i) => (
            <TaskRow
              key={t.kind}
              index={i}
              justRan={justRan === t.kind}
              task={t}
              starting={run.isPending && run.variables === t.kind}
              onRun={() => run.mutate(t.kind)}
              onCancel={(id) => cancel.mutate(id)}
            />
          ))}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Recent runs</CardTitle>
          <CardDescription>
            The last 50 runs. Older ones are tidied away after 30 days.
          </CardDescription>
        </CardHeader>
        <CardContent className="p-0 pt-4">
          {(data.hiddenRoutine > 0 || showRoutine) && (
            <p className="flex flex-wrap items-center gap-x-2 px-5 pb-3 text-sm text-muted-foreground">
              {showRoutine
                ? "Showing routine checks that found nothing to do."
                : `${data.hiddenRoutine} routine ${data.hiddenRoutine === 1 ? "check" : "checks"} that found nothing to do ${data.hiddenRoutine === 1 ? "is" : "are"} hidden.`}
              <Button variant="link" size="sm" onClick={() => setShowRoutine(!showRoutine)}>
                {showRoutine ? "Hide them" : "Show them"}
              </Button>
            </p>
          )}
          {data.recent.length === 0 ? (
            <p className="px-5 pb-5 text-sm text-muted-foreground">Nothing has run yet.</p>
          ) : (
            <ul className="divide-y">
              {data.recent.map((j, i) => (
                <li
                  key={j.id}
                  className={`animate-rise space-y-1.5 px-5 py-3 ${j.quiet ? "text-muted-foreground" : ""}`}
                  style={{ animationDelay: `calc(min(${i}, 14) * 45ms * var(--m))` }}
                >
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                    <span className="font-medium">{j.name}</span>
                    <JobStatus job={j} />
                    <span className="text-xs text-muted-foreground">
                      {relativeTime(j.createdAt)}, {triggerText[j.trigger]}
                      {duration(j) && `, took ${duration(j)}`}
                      {j.attempts > 1 && `, attempt ${j.attempts}`}
                    </span>
                    {(j.status === "queued" || j.status === "running") && (
                      <Button
                        variant="ghost"
                        size="sm"
                        className="ml-auto"
                        onClick={() => cancel.mutate(j.id)}
                      >
                        <Square /> Cancel
                      </Button>
                    )}
                  </div>
                  {j.status === "running" && (
                    <Progress value={j.progress} label={`${j.name} progress`} />
                  )}
                  {j.error ? (
                    <p className="text-sm text-destructive">{j.error}</p>
                  ) : (
                    j.message && <p className="text-sm text-muted-foreground">{j.message}</p>
                  )}
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

/** How far through its wait the next run is, 0 to 1; undefined when not known. */
function waitShare(task: Task): number | undefined {
  if (!task.nextRunAt || task.everySeconds <= 0) return undefined;
  const left = new Date(task.nextRunAt).getTime() - Date.now();
  return Math.min(1, Math.max(0, 1 - left / (task.everySeconds * 1000)));
}

function Tick() {
  return (
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
  );
}

function TaskRow({
  task,
  index,
  justRan,
  starting,
  onRun,
  onCancel,
}: {
  task: Task;
  index: number;
  justRan: boolean;
  starting: boolean;
  onRun: () => void;
  onCancel: (id: number) => void;
}) {
  const active = task.active;
  const share = waitShare(task);
  return (
    <div
      className="trow2 animate-rise"
      style={{ animationDelay: `calc(min(${index}, 14) * 45ms * var(--m))` }}
    >
      <div className="min-w-0 space-y-1">
        <p className="flex flex-wrap items-center gap-2 font-medium">
          {task.name}
          {justRan && (
            <Badge variant="success">
              <Tick /> Started
            </Badge>
          )}
        </p>
        <p className="text-sm text-muted-foreground">{task.description}</p>
        <p className="pt-1 text-xs text-muted-foreground">
          {everyLabel(task.everySeconds)}
          {task.last && (
            <>
              {" · "}last ran {relativeTime(task.last.finishedAt ?? task.last.createdAt)} (
              {statusText[task.last.status].toLowerCase()})
            </>
          )}
          {task.nextRunAt && <> · next {relativeTime(task.nextRunAt)}</>}
        </p>
        {!active && share !== undefined && (
          <div
            className="nextbar mt-2 h-1 max-w-sm overflow-hidden rounded-full bg-muted"
            role="img"
            aria-label={`${Math.round(share * 100)}% of the wait until the next run has passed`}
          >
            <i className="bg-primary/70" style={{ width: `${share * 100}%` }} />
          </div>
        )}
        {active && (
          <div className="space-y-1 pt-1">
            <Progress
              value={active.status === "running" ? active.progress : 0}
              label={`${task.name} progress`}
            />
            <p className="text-xs text-muted-foreground">
              {active.status === "queued" ? "Waiting to start" : active.message || "Running"}
            </p>
          </div>
        )}
      </div>
      {active ? (
        <Button variant="outline" size="sm" onClick={() => onCancel(active.id)}>
          <Square /> Cancel
        </Button>
      ) : (
        <Button variant="outline" size="sm" disabled={starting} onClick={onRun}>
          {starting ? <span className="spin" aria-hidden="true" /> : <Play />}
          Run now
        </Button>
      )}
    </div>
  );
}
