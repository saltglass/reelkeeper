import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { BackupCheck, BackupEntry, BackupList } from "@/hooks/backups";
import { resetToastsForTests } from "@/hooks/toast";
import { json, renderApp, signedIn } from "@/test/app";
import { describeRows, RestartOverlay } from "./RestoreFlow";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  resetToastsForTests();
});

const entry = (over: Partial<BackupEntry>): BackupEntry => ({
  name: "reelkeeper-20261011-033000-scheduled.backup",
  size: 3_200_000,
  createdAt: "2026-10-11T03:30:00Z",
  kind: "scheduled",
  app: "v0.8.1",
  engine: "postgres",
  schemaVersion: 17,
  keyMatches: true,
  ...over,
});

const scheduled = entry({});
const manual = entry({
  name: "reelkeeper-20261010-120000-manual.backup",
  createdAt: "2026-10-10T12:00:00Z",
  kind: "manual",
  size: 2048,
});
const safety = entry({
  name: "reelkeeper-20261009-120000-safety.backup",
  createdAt: "2026-10-09T12:00:00Z",
  kind: "safety",
});
const uploaded = entry({
  name: "reelkeeper-20261008-120000-uploaded.backup",
  createdAt: "2026-10-08T12:00:00Z",
  kind: "manual", // the file's own header; the name says it was uploaded
  keyMatches: false,
});
const broken: BackupEntry = {
  name: "junk.backup",
  size: 12,
  keyMatches: false,
  problem: "This file isn't a Reelkeeper backup",
};

const settings = {
  folder: "",
  scheduled: true,
  time: "03:30",
  keep: 14,
  effectiveFolder: "/data/backups",
};

const check: BackupCheck = {
  createdAt: "2026-10-11T03:30:00Z",
  app: "v0.8.1",
  engine: "postgres",
  schemaVersion: 17,
  kind: "scheduled",
  rows: { titles: 140, history: 2300, indexers: 12, settings: 9 },
  total: 2461,
};

function open(
  list: Partial<BackupList>,
  extra: Record<string, never | ((r: Request) => Response | Promise<Response>)> = {},
) {
  return renderApp("/system/backups", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /settings/backups": json(settings),
    "GET /backups": json({ folder: "/data/backups", backups: [], ...list }),
    ...extra,
  });
}

describe("backups list", () => {
  it("shows each backup with its kind, size and warnings", async () => {
    open({ backups: [scheduled, manual, safety, uploaded, broken] });
    expect(await screen.findByText("Scheduled")).toBeInTheDocument();
    expect(screen.getByText("Manual")).toBeInTheDocument();
    expect(screen.getByText("Safety (before a restore)")).toBeInTheDocument();
    expect(screen.getByText("Uploaded")).toBeInTheDocument();
    expect(screen.getAllByText("3.1 MB", { exact: false })).toHaveLength(3);
    expect(screen.getByText("2.0 KB", { exact: false })).toBeInTheDocument();
    expect(screen.getAllByText("Reelkeeper v0.8.1", { exact: false })).toHaveLength(4);
    // Only the uploaded one (made with another key) is flagged...
    expect(screen.getAllByText("Made with a different secret key")).toHaveLength(1);
    // ...and the broken file says why it can't be used, with no restore.
    expect(screen.getByText("This file isn't a Reelkeeper backup")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restore junk.backup" })).toBeDisabled();
    expect(screen.getByRole("link", { name: `Download ${scheduled.name}` })).toHaveAttribute(
      "href",
      `/api/v1/backups/${scheduled.name}`,
    );
  });

  it("says what to do when there are none", async () => {
    open({ backups: [] });
    expect(
      await screen.findByText("The first daily backup runs at 03:30, or press Back up now.", {
        exact: false,
      }),
    ).toBeInTheDocument();
  });

  it("makes a backup now", async () => {
    let made = 0;
    let backups: BackupEntry[] = [];
    open(
      {},
      {
        "GET /backups": () => json({ folder: "/data/backups", backups }),
        "POST /backups": () => {
          made++;
          backups = [manual];
          return json(manual, 201);
        },
      },
    );
    await userEvent.click(await screen.findByRole("button", { name: "Back up now" }));
    expect(await screen.findByText("Backup made")).toBeInTheDocument();
    expect(made).toBe(1);
    expect(await screen.findByText("Manual")).toBeInTheDocument();
  });

  it("shows the server's message when an upload is refused", async () => {
    let body = "";
    open(
      { backups: [scheduled] },
      {
        "POST /backups/upload": async (req) => {
          body = await req.text();
          return json({ code: "backup", message: "This file isn't a Reelkeeper backup" }, 422);
        },
      },
    );
    const input = await screen.findByLabelText("Backup file");
    await userEvent.upload(input, new File(["hello"], "x.backup"));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "This file isn't a Reelkeeper backup",
    );
    expect(body).toBe("hello");
  });

  it("deletes a backup only after confirming", async () => {
    const deleted: string[] = [];
    open(
      { backups: [scheduled, manual] },
      {
        [`DELETE /backups/${manual.name}`]: () => {
          deleted.push(manual.name);
          return new Response(null, { status: 204 });
        },
      },
    );
    await userEvent.click(await screen.findByRole("button", { name: `Delete ${manual.name}` }));
    const dialog = await screen.findByRole("dialog");
    expect(deleted).toEqual([]);
    expect(within(dialog).getByText(manual.name)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
    expect(await screen.findByText("Backup deleted")).toBeInTheDocument();
    expect(deleted).toEqual([manual.name]);
  });
});

describe("restoring", () => {
  it("shows why a backup can't be restored, with no restore button", async () => {
    open(
      { backups: [uploaded] },
      {
        [`POST /backups/${uploaded.name}/check`]: () =>
          json(
            {
              code: "backup",
              message: "This backup was made with a different REELKEEPER_SECRET_KEY.",
            },
            422,
          ),
      },
    );
    await userEvent.click(await screen.findByRole("button", { name: `Restore ${uploaded.name}` }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("This backup can't be restored")).toBeInTheDocument();
    expect(within(dialog).getByText(/different REELKEEPER_SECRET_KEY/)).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: /Restore and restart/ })).toBeNull();
  });

  it("explains the restore, then restarts only when confirmed", async () => {
    let restored = 0;
    open(
      { backups: [scheduled] },
      {
        [`POST /backups/${scheduled.name}/check`]: () => json(check),
        [`POST /backups/${scheduled.name}/restore`]: () => {
          restored++;
          return json({ message: `Reelkeeper is restarting to restore ${scheduled.name}.` }, 202);
        },
        "GET /ping": () => json({ ok: true }),
      },
    );
    await userEvent.click(await screen.findByRole("button", { name: `Restore ${scheduled.name}` }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Restore this backup?")).toBeInTheDocument();
    expect(
      within(dialog).getByText(
        "It holds 2,461 rows across 4 tables (140 titles, 2,300 history entries, 12 indexers).",
      ),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText(/saves a safety backup of everything as it is now/),
    ).toBeInTheDocument();
    expect(within(dialog).getByText(/Everyone is signed out/)).toBeInTheDocument();

    // Cancel changes nothing.
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(restored).toBe(0);
    expect(screen.queryByRole("dialog")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: `Restore ${scheduled.name}` }));
    await userEvent.click(await screen.findByRole("button", { name: "Restore and restart" }));
    const overlay = await screen.findByRole("alertdialog");
    expect(within(overlay).getByText("Restarting to restore…")).toBeInTheDocument();
    expect(within(overlay).getByText("This usually takes under a minute.")).toBeInTheDocument();
    expect(restored).toBe(1);
  });

  it("shows the server's reason if the restore can't start", async () => {
    open(
      { backups: [scheduled] },
      {
        [`POST /backups/${scheduled.name}/check`]: () => json(check),
        [`POST /backups/${scheduled.name}/restore`]: () =>
          json({ code: "backup", message: "This backup is damaged or incomplete." }, 422),
      },
    );
    await userEvent.click(await screen.findByRole("button", { name: `Restore ${scheduled.name}` }));
    await userEvent.click(await screen.findByRole("button", { name: "Restore and restart" }));
    expect(await screen.findByText("This backup is damaged or incomplete.")).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("describes a backup's rows", () => {
    expect(describeRows({ ...check, rows: { a: 1 }, total: 1 })).toBe("1 row across 1 table");
  });
});

describe("the restart overlay", () => {
  it("waits for Reelkeeper to go down and come back, then opens the Backups page", async () => {
    vi.useFakeTimers();
    const assign = vi.fn();
    vi.stubGlobal("location", { origin: "http://localhost", assign });
    const answers = [true, false, false, true]; // still up, going down, down, back
    const fetched: string[] = [];
    vi.stubGlobal("fetch", async (req: Request) => {
      fetched.push(new URL(req.url).pathname);
      if (!answers.shift()) throw new TypeError("connection refused");
      return new Response("ok");
    });
    render(<RestartOverlay />);
    for (let i = 0; i < 3; i++) await act(() => vi.advanceTimersByTimeAsync(2000));
    // It was up at first and must not be mistaken for "back".
    expect(assign).not.toHaveBeenCalled();
    await act(() => vi.advanceTimersByTimeAsync(2000));
    expect(assign).toHaveBeenCalledWith("http://localhost/system/backups");
    expect(new Set(fetched)).toEqual(new Set(["/ping"]));
  });

  it("goes on after 10 seconds even if it never saw Reelkeeper down", async () => {
    vi.useFakeTimers();
    const assign = vi.fn();
    vi.stubGlobal("location", { origin: "http://localhost", assign });
    vi.stubGlobal("fetch", async () => new Response("ok"));
    render(<RestartOverlay />);
    await act(() => vi.advanceTimersByTimeAsync(8000));
    expect(assign).not.toHaveBeenCalled();
    await act(() => vi.advanceTimersByTimeAsync(2000));
    expect(assign).toHaveBeenCalled();
  });

  it("says so after 3 minutes without an answer, and keeps trying", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("location", { origin: "http://localhost", assign: vi.fn() });
    let calls = 0;
    vi.stubGlobal("fetch", async () => {
      calls++;
      throw new TypeError("down");
    });
    render(<RestartOverlay />);
    await act(() => vi.advanceTimersByTimeAsync(170_000));
    expect(screen.getByText("This usually takes under a minute.")).toBeInTheDocument();
    await act(() => vi.advanceTimersByTimeAsync(12_000));
    expect(screen.getByText(/hasn't come back yet/)).toBeInTheDocument();
    const before = calls;
    await act(() => vi.advanceTimersByTimeAsync(6000));
    expect(calls).toBeGreaterThan(before);
  });
});

describe("last restore", () => {
  it("shows a green banner with an undo button when the safety backup is still there", async () => {
    let checked = false;
    open(
      {
        backups: [scheduled, safety],
        lastRestore: {
          at: "2026-10-11T04:00:00Z",
          backup: scheduled.name,
          ok: true,
          message: "Restored it: 2461 rows.",
          safety: safety.name,
        },
      },
      {
        [`POST /backups/${safety.name}/check`]: () => {
          checked = true;
          return json({ ...check, kind: "safety" });
        },
      },
    );
    expect(
      await screen.findByText(new RegExp(`Restored ${scheduled.name} on`)),
    ).toBeInTheDocument();
    expect(screen.getByText("Restored it: 2461 rows.")).toBeInTheDocument();
    expect(
      screen.getByText(new RegExp(`To undo it, restore ${safety.name} \\(made just before\\)`)),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Undo this restore" }));
    expect(await screen.findByText("Restore this backup?")).toBeInTheDocument();
    expect(checked).toBe(true);
  });

  it("shows a red banner when the restore didn't happen", async () => {
    open({
      backups: [scheduled, safety],
      lastRestore: {
        at: "2026-10-11T04:00:00Z",
        backup: scheduled.name,
        ok: false,
        message: "Nothing was restored: this backup is damaged.",
        safety: safety.name,
      },
    });
    const banner = await screen.findByRole("alert");
    expect(banner).toHaveTextContent(
      `The restore of ${scheduled.name} didn't happen: Nothing was restored: this backup is damaged.`,
    );
    // Nothing changed, so there is nothing to undo.
    expect(screen.queryByRole("button", { name: "Undo this restore" })).toBeNull();
  });
});

describe("backup settings", () => {
  it("shows the data folder as the placeholder and saves changes", async () => {
    const puts: Record<string, unknown>[] = [];
    open(
      { backups: [scheduled] },
      {
        "PUT /settings/backups": async (req) => {
          const body = await req.json();
          puts.push(body);
          return json({ ...body, effectiveFolder: body.folder });
        },
      },
    );
    const folder = await screen.findByLabelText("Folder");
    expect(folder).toHaveAttribute("placeholder", "/data/backups");
    expect(screen.getByRole("switch", { name: "Daily backup" })).toBeChecked();
    expect(screen.getByLabelText("Time")).toHaveValue("03:30");
    expect(screen.getByLabelText("Keep")).toHaveValue(14);

    await userEvent.type(folder, "/mnt/nas/backups");
    const keep = screen.getByLabelText("Keep");
    await userEvent.clear(keep);
    await userEvent.type(keep, "7");
    await userEvent.click(screen.getByRole("switch", { name: "Daily backup" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(puts[0]).toEqual({
      folder: "/mnt/nas/backups",
      scheduled: false,
      time: "03:30",
      keep: 7,
    });
  });

  it("shows field errors from the server", async () => {
    open(
      { backups: [] },
      {
        "PUT /settings/backups": () =>
          json(
            {
              code: "invalid",
              message: "Some fields need attention.",
              fields: {
                folder: "Use a full path, such as /backups, or leave it empty for the data folder.",
              },
            },
            422,
          ),
      },
    );
    await userEvent.type(await screen.findByLabelText("Folder"), "relative");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(
      await screen.findByText(
        "Use a full path, such as /backups, or leave it empty for the data folder.",
      ),
    ).toBeInTheDocument();
  });

  it("explains the secret key", async () => {
    open({ backups: [] });
    expect(await screen.findByText(/sealed with REELKEEPER_SECRET_KEY/)).toBeInTheDocument();
  });
});
