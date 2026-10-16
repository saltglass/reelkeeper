import { QueryClient } from "@tanstack/react-query";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { applyJobEvent, type Job, type TasksOverview, tasksQuery } from "@/hooks/tasks";
import { everyLabel, shortDuration } from "@/lib/time";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const now = new Date().toISOString();
const job = (over: Partial<Job>): Job => ({
  id: 1,
  kind: "jobs.prune",
  name: "Tidy task history",
  status: "succeeded",
  trigger: "schedule",
  progress: 100,
  message: "Removed 3 old runs",
  error: "",
  attempts: 1,
  quiet: false,
  createdAt: now,
  startedAt: now,
  finishedAt: now,
  ...over,
});

const overview: TasksOverview = {
  tasks: [
    {
      kind: "jobs.prune",
      name: "Tidy task history",
      description: "Removes finished task runs older than 30 days.",
      everySeconds: 86400,
      last: job({}),
      nextRunAt: new Date(Date.now() + 86_000_000).toISOString(),
    },
  ],
  hiddenRoutine: 0,
  recent: [
    job({}),
    job({
      id: 2,
      status: "failed",
      error: "The folder /media/movies isn't reachable",
      message: "",
    }),
  ],
};

describe("tasks page", () => {
  it("lists tasks and recent runs, and starts one", async () => {
    let started = false;
    renderApp("/system/tasks", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /system/tasks": json(overview),
      "POST /system/tasks/jobs.prune/run": () => {
        started = true;
        return json({ job: job({ id: 3, status: "queued" }), alreadyActive: false });
      },
    });
    expect(await screen.findByText(/Every day/)).toBeInTheDocument();
    expect(screen.getByText("The folder /media/movies isn't reachable")).toBeInTheDocument();
    const runs = screen.getByText("Recent runs").closest("section") as HTMLElement;
    expect(within(runs).getByText("Failed")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Run now" }));
    expect(started).toBe(true);
  });
});

describe("routine runs", () => {
  it("says how many are hidden, and shows them on request", async () => {
    const quietRun = job({
      id: 7,
      name: "Check downloads",
      message: "4 downloading.",
      quiet: true,
    });
    let asked = "";
    renderApp("/system/tasks", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /system/tasks": (req: Request) => {
        asked = new URL(req.url).search;
        return json(
          asked.includes("routine=true")
            ? { ...overview, recent: [quietRun, ...overview.recent], hiddenRoutine: 0 }
            : { ...overview, hiddenRoutine: 12 },
        );
      },
    });
    expect(
      await screen.findByText(/12 routine checks that found nothing to do are hidden/),
    ).toBeInTheDocument();
    expect(screen.queryByText("4 downloading.")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Show them" }));
    expect(await screen.findByText("4 downloading.")).toBeInTheDocument();
    expect(asked).toContain("routine=true");

    await userEvent.click(screen.getByRole("button", { name: "Hide them" }));
    expect(await screen.findByText(/12 routine checks/)).toBeInTheDocument();
    expect(screen.queryByText("4 downloading.")).not.toBeInTheDocument();
  });
});

describe("live task updates", () => {
  it("moves progress without refetching and records the finish", () => {
    const qc = new QueryClient();
    qc.setQueryData(tasksQuery.queryKey, structuredClone(overview));
    applyJobEvent(
      qc,
      job({ id: 9, status: "running", progress: 40, message: "Scanning", finishedAt: undefined }),
    );
    let data = qc.getQueryData<TasksOverview>(tasksQuery.queryKey);
    expect(data?.recent[0]?.id).toBe(9);
    expect(data?.tasks[0]?.active?.progress).toBe(40);

    applyJobEvent(qc, job({ id: 9, status: "succeeded", progress: 100 }));
    data = qc.getQueryData<TasksOverview>(tasksQuery.queryKey);
    expect(data?.tasks[0]?.active).toBeUndefined();
    expect(data?.tasks[0]?.last?.id).toBe(9);
    expect(data?.recent.filter((j) => j.id === 9)).toHaveLength(1);
  });
});

describe("time labels", () => {
  it("describes schedules and durations", () => {
    expect(everyLabel(0)).toBe("When needed");
    expect(everyLabel(86400)).toBe("Every day");
    expect(everyLabel(6 * 3600)).toBe("Every 6 hours");
    expect(shortDuration(185)).toBe("3m 05s");
  });
});
