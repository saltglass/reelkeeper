import { type QueryClient, queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import type { components } from "@/api/schema";

export type Job = components["schemas"]["Job"];
export type Task = components["schemas"]["Task"];
export type TasksOverview = components["schemas"]["TasksOverview"];

export const tasksQuery = queryOptions({
  queryKey: ["system", "tasks"],
  queryFn: () => unwrap(api.GET("/system/tasks")),
});

/** The same overview, with the routine runs that found nothing to do included. */
export const tasksWithRoutineQuery = queryOptions({
  queryKey: ["system", "tasks", "routine"],
  queryFn: () => unwrap(api.GET("/system/tasks", { params: { query: { routine: true } } })),
});

const finished = (j: Job) =>
  j.status === "succeeded" || j.status === "failed" || j.status === "cancelled";

/**
 * Applies a task.updated event to the cached overview, so progress moves
 * without refetching. A finished run also changes the next scheduled time,
 * which only the server knows, so that triggers one refetch.
 */
export function applyJobEvent(qc: QueryClient, job: Job) {
  const data = qc.getQueryData<TasksOverview>(tasksQuery.queryKey);
  if (!data) return;
  // Routine runs aren't listed in this (default) view; the refetch after
  // the run finishes updates the "hidden" count.
  const recent = data.recent.some((j) => j.id === job.id)
    ? data.recent.map((j) => (j.id === job.id ? job : j))
    : job.quiet
      ? data.recent
      : [job, ...data.recent].slice(0, 50);
  const tasks = data.tasks.map((t) => {
    if (t.kind !== job.kind) return t;
    if (finished(job))
      return { ...t, last: job, active: t.active?.id === job.id ? undefined : t.active };
    return { ...t, active: job, nextRunAt: undefined };
  });
  qc.setQueryData<TasksOverview>(tasksQuery.queryKey, { ...data, tasks, recent });
  if (finished(job)) qc.invalidateQueries({ queryKey: tasksQuery.queryKey });
}
