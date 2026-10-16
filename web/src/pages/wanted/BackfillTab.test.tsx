import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Backfill, BackfillOptions, BackfillShow } from "@/hooks/backfill";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());
beforeEach(() => {
  try {
    localStorage.clear();
  } catch {}
});

const show = (id: number, title: string, over: Partial<BackfillShow> = {}): BackfillShow => ({
  id,
  versionId: id * 10,
  titleId: id,
  title,
  year: 2006,
  posterPath: `/p${id}.jpg`,
  state: "queued",
  found: 0,
  missing: 6,
  downloading: 0,
  ...over,
});

const queue: Backfill = {
  now: show(1, "The IT Crowd", { state: "active", found: 14, missing: 6, downloading: 3 }),
  next: [show(2, "Black Books"), show(3, "Spaced")],
  decision: [show(4, "Peep Show", { state: "decision", found: 23, missing: 2 })],
  grabsToday: 2,
  grabsPerDay: 5,
};

const emptyQueue: Backfill = { next: [], decision: [], grabsToday: 0, grabsPerDay: 5 };

const options: BackfillOptions = {
  omitted: 0,
  units: [
    {
      label: "S01E05",
      episodeIds: [105],
      options: [
        {
          token: "tok-1",
          title: "Peep.Show.S01E05.720p.HDTV.x264-KILLERS",
          size: 1.5 * 1024 ** 3,
          quality: "HDTV-720p",
          indexer: "Knaben",
          seeders: 90,
          reasons: ["HDTV-720p isn't a quality this profile accepts."],
        },
        {
          token: "tok-2",
          title: "Peep.Show.S01E05.480p.DVDRip.x264-OLD",
          size: 700 * 1024 ** 2,
          quality: "DVD",
          indexer: "Torrentleech",
          seeders: 12,
          reasons: ["DVD isn't a quality this profile accepts."],
        },
      ],
    },
    { label: "S01E06", episodeIds: [106], options: [] },
  ],
};

function open(routes: Parameters<typeof renderApp>[1] = {}, list: Backfill = queue) {
  return renderApp("/wanted", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /wanted": json({
      missing: [],
      upgrades: [],
      counts: { missingMovies: 0, missingEpisodes: 0, upgrades: 0 },
    }),
    "GET /backfill": json(list),
    ...routes,
  });
}

async function openTab() {
  await userEvent.click(await screen.findByRole("button", { name: /^Backfill/ }));
}

describe("Backfill tab", () => {
  it("counts the shows in line on its tab and keeps the Wanted lists as the first tab", async () => {
    open();
    expect(await screen.findByRole("button", { name: "Backfill (3)" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Missing/ })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("says what to do when nothing is queued", async () => {
    open({}, emptyQueue);
    await openTab();
    expect(
      await screen.findByText(
        "Nothing queued. Use Add to backfill on a series to fill in its older missing episodes, one show at a time.",
      ),
    ).toBeInTheDocument();
  });

  it("shows Now, Up next and Needs a decision", async () => {
    open();
    await openTab();
    const now = await screen.findByRole("region", { name: "Now" });
    expect(within(now).getByText("The IT Crowd (2006)")).toBeInTheDocument();
    expect(within(now).getByText("14 of 20 episodes; 3 downloading")).toBeInTheDocument();
    expect(within(now).getByText("Backfill grabs today: 2 of 5")).toBeInTheDocument();
    expect(within(now).getByRole("progressbar")).toHaveAttribute("aria-valuenow", "70");

    const next = screen.getByRole("region", { name: "Up next" });
    const names = within(next)
      .getAllByRole("listitem")
      .map((li) => li.textContent);
    expect(names[0]).toContain("Black Books");
    expect(names[1]).toContain("Spaced");

    const decision = screen.getByRole("region", { name: "Needs a decision" });
    expect(within(decision).getByText("Peep Show (2006)")).toBeInTheDocument();
    expect(within(decision).getByText("23 of 25 episodes; 2 still wanted")).toBeInTheDocument();
  });

  it("moves shows up and down and removes them", async () => {
    const calls: { method: string; path: string; body?: unknown }[] = [];
    const record = (path: string) => async (req: Request) => {
      calls.push({
        method: req.method,
        path,
        body: req.method === "POST" ? await req.json() : undefined,
      });
      return new Response(null, { status: 204 });
    };
    open({
      "POST /backfill/2/move": record("/backfill/2/move"),
      "POST /backfill/3/move": record("/backfill/3/move"),
      "DELETE /backfill/3": record("/backfill/3"),
      "DELETE /backfill/1": record("/backfill/1"),
    });
    await openTab();
    const next = await screen.findByRole("region", { name: "Up next" });
    // The first can't go up and the last can't go down.
    expect(within(next).getByRole("button", { name: "Move Black Books up" })).toBeDisabled();
    expect(within(next).getByRole("button", { name: "Move Spaced down" })).toBeDisabled();

    await userEvent.click(within(next).getByRole("button", { name: "Move Black Books down" }));
    await userEvent.click(within(next).getByRole("button", { name: "Move Spaced up" }));
    await userEvent.click(
      within(next).getByRole("button", { name: "Remove Spaced from the queue" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Stop backfilling The IT Crowd" }));
    await waitFor(() => expect(calls).toHaveLength(4));
    expect(calls).toEqual([
      { method: "POST", path: "/backfill/2/move", body: { direction: "down" } },
      { method: "POST", path: "/backfill/3/move", body: { direction: "up" } },
      { method: "DELETE", path: "/backfill/3", body: undefined },
      { method: "DELETE", path: "/backfill/1", body: undefined },
    ]);
  });

  it("hides a show that needs a decision", async () => {
    const hidden: string[] = [];
    open({
      "POST /backfill/4/hide": () => {
        hidden.push("4");
        return new Response(null, { status: 204 });
      },
    });
    await openTab();
    await userEvent.click(await screen.findByRole("button", { name: "Hide Peep Show" }));
    await waitFor(() => expect(hidden).toEqual(["4"]));
  });

  it("searches for options only when asked, then grabs one after a confirmation", async () => {
    const searches: string[] = [];
    const grabs: unknown[] = [];
    open({
      "GET /backfill/4/options": () => {
        searches.push("4");
        return json(options);
      },
      "POST /backfill/4/grab": async (req) => {
        grabs.push(await req.json());
        return json({ message: "Grabbed it." }, 202);
      },
    });
    await openTab();
    await screen.findByRole("region", { name: "Needs a decision" });
    expect(searches).toEqual([]); // nothing is searched just by looking

    await userEvent.click(screen.getByRole("button", { name: "Show options for Peep Show" }));
    const killers = await screen.findByText("Peep.Show.S01E05.720p.HDTV.x264-KILLERS");
    expect(searches).toEqual(["4"]);
    const card = killers.closest("li") as HTMLElement;
    expect(within(card).getByText("90 seeders")).toBeInTheDocument();
    expect(within(card).getByText("1.5 GB")).toBeInTheDocument();
    expect(within(card).getByText("HDTV-720p")).toBeInTheDocument();
    expect(within(card).getByText("Knaben")).toBeInTheDocument();
    expect(
      within(card).getByText("HDTV-720p isn't a quality this profile accepts."),
    ).toBeInTheDocument();
    expect(screen.getByText("Nothing else found for this.")).toBeInTheDocument();

    // Closing and opening again doesn't search again.
    await userEvent.click(screen.getByRole("button", { name: "Show options for Peep Show" }));
    await userEvent.click(screen.getByRole("button", { name: "Show options for Peep Show" }));
    expect(searches).toEqual(["4"]);

    // Cancelling grabs nothing.
    await userEvent.click(
      screen.getByRole("button", {
        name: "Grab this one: Peep.Show.S01E05.720p.HDTV.x264-KILLERS",
      }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("Download this release even though it's outside your profile?"),
    ).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(grabs).toEqual([]);

    // Confirming sends the token, not the release.
    await userEvent.click(
      screen.getByRole("button", {
        name: "Grab this one: Peep.Show.S01E05.720p.HDTV.x264-KILLERS",
      }),
    );
    await userEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", { name: "Download it" }),
    );
    await waitFor(() => expect(grabs).toEqual([{ token: "tok-1" }]));
    expect(await screen.findByText("Sent to the download client")).toBeInTheDocument();
  });

  it("shows why a grab was refused", async () => {
    open({
      "GET /backfill/4/options": json(options),
      "POST /backfill/4/grab": json(
        { code: "refused", message: "That release is on the blocklist." },
        409,
      ),
    });
    await openTab();
    await userEvent.click(
      await screen.findByRole("button", { name: "Show options for Peep Show" }),
    );
    await userEvent.click(
      await screen.findByRole("button", {
        name: "Grab this one: Peep.Show.S01E05.480p.DVDRip.x264-OLD",
      }),
    );
    await userEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", { name: "Download it" }),
    );
    expect(await screen.findByText("That release is on the blocklist.")).toBeInTheDocument();
  });
});
