import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { WantedItem } from "@/hooks/wanted";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());
beforeEach(() => {
  try {
    localStorage.clear();
  } catch {}
});

const movie = (id: number, title: string, over: Partial<WantedItem> = {}): WantedItem => ({
  kind: "movie",
  titleId: id,
  versionId: id * 10,
  title,
  year: 2020,
  posterPath: `/p${id}.jpg`,
  profile: "HD",
  date: "2026-09-01",
  state: "waiting",
  status:
    "Next search in about 2 days. Dry run: Reelkeeper notes what it would grab but downloads nothing.",
  lastSearchedAt: new Date(Date.now() - 24 * 3600_000).toISOString(),
  lastSearchMessage: `Searched for ${title} (2020), HD: 14 releases, 0 acceptable. Most common reason (9 of 14): too large`,
  ...over,
});

const slowHorses: WantedItem = {
  kind: "episodes",
  titleId: 5,
  versionId: 50,
  title: "Slow Horses",
  year: 2022,
  posterPath: "/sh.jpg",
  profile: "HDTV",
  date: "2026-10-08",
  state: "never",
  status: "Not searched yet. In line for the next scheduled search.",
  lastSearchMessage: "",
  season: 6,
  episodes: [
    { episodeId: 501, season: 6, number: 1, name: "Pulse", airDate: "2026-10-01" },
    { episodeId: 502, season: 6, number: 2, name: "", airDate: "2026-10-08" },
  ],
};

const list = {
  missing: [
    slowHorses,
    movie(1, "Ronin"),
    movie(2, "Heat", {
      profile: "Remux",
      date: "2026-08-01",
      state: "downloading",
      status: "Downloading (Activity → Queue).",
      lastSearchedAt: undefined,
      lastSearchMessage: "",
    }),
  ],
  upgrades: [
    movie(3, "Alien", {
      quality: "WEBDL-1080p",
      target: "Bluray-1080p",
      status: "Due now: in line for the next scheduled search.",
    }),
  ],
  counts: { missingMovies: 2, missingEpisodes: 2, upgrades: 1 },
};

function open(routes: Parameters<typeof renderApp>[1] = {}) {
  return renderApp("/wanted", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /wanted": json(list),
    ...routes,
  });
}

const rowTitles = () =>
  screen
    .getAllByRole("listitem")
    .map((li) => ["Slow Horses", "Ronin", "Heat", "Alien"].find((t) => li.textContent?.includes(t)))
    .filter(Boolean);

describe("wanted page", () => {
  it("lists what's missing with why, newest first", async () => {
    open();
    await screen.findByText("Ronin (2020)");
    expect(rowTitles()).toEqual(["Slow Horses", "Ronin", "Heat"]);
    expect(screen.getByRole("button", { name: "Missing (4)" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByRole("button", { name: "Can be upgraded (1)" })).toBeInTheDocument();
    // The status line, and the last search with how long ago.
    expect(screen.getByText(/^Next search in about 2 days\./)).toBeInTheDocument();
    expect(screen.getByText(/14 releases, 0 acceptable.*a day ago|yesterday/)).toBeInTheDocument();
    expect(screen.getByText("Downloading (Activity → Queue).")).toBeInTheDocument();
    expect(
      screen.getByText("Not searched yet. In line for the next scheduled search."),
    ).toBeInTheDocument();
    // Titles link to their pages.
    expect(screen.getByRole("link", { name: "Ronin (2020)" })).toHaveAttribute("href", "/movies/1");
    expect(screen.getByRole("link", { name: "Slow Horses (2022)" })).toHaveAttribute(
      "href",
      "/series/5",
    );
  });

  it("groups episodes by season and opens to the list", async () => {
    open();
    const toggle = await screen.findByRole("button", { name: /Season 6 · 2 episodes/ });
    expect(screen.queryByText("Pulse")).not.toBeInTheDocument();
    await userEvent.click(toggle);
    expect(screen.getByText("Pulse")).toBeInTheDocument();
    expect(screen.getByText("S06E02")).toBeInTheDocument();
    expect(screen.getByText("2026-10-08")).toBeInTheDocument();
  });

  it("filters by movies or TV, profile and search, and remembers the choice", async () => {
    const { unmount } = open();
    await screen.findByText("Ronin (2020)");
    await userEvent.selectOptions(screen.getByLabelText("Show"), "movie");
    expect(rowTitles()).toEqual(["Ronin", "Heat"]);
    await userEvent.selectOptions(screen.getByLabelText("Show"), "tv");
    expect(rowTitles()).toEqual(["Slow Horses"]);
    await userEvent.selectOptions(screen.getByLabelText("Show"), "all");
    await userEvent.selectOptions(screen.getByLabelText("Profile"), "Remux");
    expect(rowTitles()).toEqual(["Heat"]);
    await userEvent.selectOptions(screen.getByLabelText("Profile"), "");
    await userEvent.type(screen.getByLabelText("Search wanted"), "ron");
    expect(rowTitles()).toEqual(["Ronin"]);
    await userEvent.clear(screen.getByLabelText("Search wanted"));
    await userEvent.selectOptions(screen.getByLabelText("Sort by"), "title");
    expect(rowTitles()).toEqual(["Heat", "Ronin", "Slow Horses"]);
    await userEvent.selectOptions(screen.getByLabelText("Show"), "tv");
    unmount();

    open();
    await screen.findByText("Slow Horses (2022)");
    expect(screen.getByLabelText("Show")).toHaveValue("tv");
    expect(screen.getByLabelText("Sort by")).toHaveValue("title");
  });

  it("sorts by last searched with never-searched first", async () => {
    open();
    await screen.findByText("Ronin (2020)");
    await userEvent.selectOptions(screen.getByLabelText("Sort by"), "searched");
    // Slow Horses and Heat were never searched; Ronin was.
    expect(rowTitles()).toEqual(["Heat", "Slow Horses", "Ronin"]);
  });

  it("shows upgrades with the file's quality and the target", async () => {
    open();
    await userEvent.click(await screen.findByRole("button", { name: /Can be upgraded/ }));
    expect(await screen.findByText("Alien (2020)")).toBeInTheDocument();
    expect(rowTitles()).toEqual(["Alien"]);
    expect(screen.getByText("WEBDL-1080p")).toBeInTheDocument();
    expect(screen.getByText("Bluray-1080p")).toBeInTheDocument();
    // The kind filter is for the Missing tab only.
    expect(screen.queryByLabelText("Show")).not.toBeInTheDocument();
  });

  it("labels items held back by a full queue", async () => {
    open({
      "GET /wanted": json({
        ...list,
        missing: [
          movie(1, "Ronin", {
            state: "capped",
            status:
              "The queue is full (Settings → Download Client); searching resumes when downloads finish.",
          }),
        ],
        counts: { missingMovies: 1, missingEpisodes: 0, upgrades: 1 },
      }),
    });
    await screen.findByText("Ronin (2020)");
    expect(screen.getByText("Queue full")).toBeInTheDocument();
    expect(screen.queryByText(/daily limit/i)).not.toBeInTheDocument();
    expect(screen.getByText(/searching resumes when downloads finish/)).toBeInTheDocument();
  });

  it("labels items waiting for space on the download disk", async () => {
    open({
      "GET /wanted": json({
        ...list,
        missing: [
          movie(1, "Ronin", {
            state: "noSpace",
            status:
              "Waiting for space: the download disk has 18.0 GB free. Searching resumes when cleanup makes room.",
          }),
        ],
        counts: { missingMovies: 1, missingEpisodes: 0, upgrades: 1 },
      }),
    });
    await screen.findByText("Ronin (2020)");
    expect(screen.getByText("Disk full")).toBeInTheDocument();
    expect(screen.getByText(/Searching resumes when cleanup makes room/)).toBeInTheDocument();
  });

  it("shows the score gap when the quality is already reached", async () => {
    open({
      "GET /wanted": json({
        ...list,
        upgrades: [
          movie(4, "Tron: Ares", {
            quality: "Remux-2160p",
            target: "Remux-2160p",
            score: 1500,
            targetScore: 2500,
            formats: [
              { name: "HDR10", score: 500 },
              { name: "TrueHD", score: 1000 },
            ],
            status: "Due now: in line for the next scheduled search.",
          }),
          movie(5, "Sunshine", {
            quality: "Remux-2160p",
            target: "Remux-2160p",
            score: 0,
            targetScore: 100,
            formats: [],
            status: "Due now: in line for the next scheduled search.",
          }),
        ],
      }),
    });
    await userEvent.click(await screen.findByRole("button", { name: /Can be upgraded/ }));
    await screen.findByText("Tron: Ares (2020)");
    // Not "Remux-2160p → Remux-2160p": the score is what's short.
    expect(screen.getByText("1500")).toBeInTheDocument();
    expect(screen.getByText("2500")).toBeInTheDocument();
    expect(screen.getByText("The file has HDR10 (+500), TrueHD (+1000).")).toBeInTheDocument();
    expect(
      screen.getByText("The file has none of the profile's scored formats."),
    ).toBeInTheDocument();
  });

  it("says plainly when there's nothing", async () => {
    open({
      "GET /wanted": json({
        missing: [],
        upgrades: [],
        counts: { missingMovies: 0, missingEpisodes: 0, upgrades: 0 },
      }),
    });
    expect(
      await screen.findByText(
        "Nothing missing. Everything Reelkeeper monitors that's out has a file.",
      ),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Can be upgraded/ }));
    expect(screen.getByText("No movies below their profile's target quality.")).toBeInTheDocument();
  });

  it("searches one row now, and a whole show from its row", async () => {
    const bodies: unknown[] = [];
    open({
      "POST /wanted/search": async (req: Request) => {
        bodies.push(await req.json());
        return json({ queued: 1, alreadyQueued: 0 });
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Search now: Ronin" }));
    await waitFor(() => expect(bodies).toEqual([{ versionIds: [10] }]));
    expect(await screen.findByText(/1 search queued/)).toBeInTheDocument();
    const show = screen.getByRole("button", { name: /Search this show/ });
    expect(show).toHaveAttribute(
      "title",
      expect.stringContaining("all of this show's wanted episodes"),
    );
    await userEvent.click(show);
    await waitFor(() => expect(bodies[1]).toEqual({ versionIds: [50] }));
  });

  it("selects rows and searches them all", async () => {
    const bodies: unknown[] = [];
    open({
      "POST /wanted/search": async (req: Request) => {
        bodies.push(await req.json());
        return json({ queued: 2, alreadyQueued: 0 });
      },
    });
    await screen.findByText("Ronin (2020)");
    await userEvent.click(screen.getByRole("checkbox", { name: "Select Ronin" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Select Slow Horses, season 6" }));
    const bar = screen.getByRole("region", { name: "Actions for the selected rows" });
    expect(within(bar).getByText("2 selected")).toBeInTheDocument();
    await userEvent.click(within(bar).getByRole("button", { name: "Search" }));
    await waitFor(() => expect(bodies).toEqual([{ versionIds: [50, 10] }]));
    expect(await screen.findByText(/2 searches queued/)).toBeInTheDocument();
    // The selection clears afterwards.
    await waitFor(() => expect(screen.queryByText("2 selected")).not.toBeInTheDocument());
  });

  it("selects everything shown, and only that", async () => {
    open();
    await screen.findByText("Ronin (2020)");
    await userEvent.selectOptions(screen.getByLabelText("Show"), "movie");
    await userEvent.click(screen.getByRole("checkbox", { name: /Select all 2 shown/ }));
    expect(screen.getByText("2 selected")).toBeInTheDocument();
    // Ticked rows that are filtered out aren't acted on.
    await userEvent.selectOptions(screen.getByLabelText("Show"), "tv");
    expect(screen.queryByText(/selected/)).not.toBeInTheDocument();
  });

  it("asks before it stops monitoring, then does it", async () => {
    const bodies: unknown[] = [];
    open({
      "POST /wanted/unmonitor": async (req: Request) => {
        bodies.push(await req.json());
        return json({ movies: 2, episodes: 2 });
      },
    });
    await screen.findByText("Ronin (2020)");
    await userEvent.click(screen.getByRole("checkbox", { name: /Select all 3 shown/ }));
    await userEvent.click(screen.getByRole("button", { name: "Stop monitoring" }));
    expect(await screen.findByText("Stop monitoring 2 movies and 2 episodes?")).toBeInTheDocument();
    expect(
      screen.getByText(
        "Reelkeeper stops looking for them. You can turn them back on from their pages.",
      ),
    ).toBeInTheDocument();
    expect(bodies).toEqual([]); // nothing until confirmed
    const dialog = screen.getByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Stop monitoring" }));
    await waitFor(() =>
      expect(bodies).toEqual([
        { movies: [10, 20], episodes: [{ versionId: 50, episodeIds: [501, 502] }] },
      ]),
    );
    expect(
      await screen.findByText("Stopped monitoring 2 movies and 2 episodes."),
    ).toBeInTheDocument();
  });

  it("can be cancelled", async () => {
    const { calls } = open();
    await screen.findByText("Ronin (2020)");
    await userEvent.click(screen.getByRole("checkbox", { name: "Select Ronin" }));
    await userEvent.click(screen.getByRole("button", { name: "Stop monitoring" }));
    await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));
    expect(calls.some((c) => c.url.endsWith("/wanted/unmonitor"))).toBe(false);
  });
});

describe("sidebar", () => {
  it("counts missing movies and episodes beside Wanted", async () => {
    open({
      "GET /wanted": (req) =>
        new URL(req.url).searchParams.get("countsOnly")
          ? json({ missing: [], upgrades: [], counts: list.counts })
          : json(list),
    });
    const link = (await screen.findAllByRole("link", { name: /^Wanted/ }))[0];
    await waitFor(() => expect(link).toHaveTextContent("Wanted4"));
    expect(link).toHaveAttribute("href", "/wanted");
  });
});

describe("Add to backfill", () => {
  const empty = { next: [], decision: [], grabsToday: 0, grabsPerDay: 5 };

  it("is offered on a show's group, not on a movie", async () => {
    const posts: unknown[] = [];
    open({
      "GET /backfill": json(empty),
      "POST /backfill": async (req) => {
        posts.push(await req.json());
        return json({}, 201);
      },
    });
    await screen.findByText("Ronin (2020)");
    expect(screen.getAllByRole("button", { name: /to backfill/ })).toHaveLength(1);
    await userEvent.click(screen.getByRole("button", { name: "Add Slow Horses to backfill" }));
    await waitFor(() => expect(posts).toEqual([{ versionId: 50 }]));
  });

  it("says where a queued show stands", async () => {
    open({
      "GET /backfill": json({
        ...empty,
        next: [
          {
            id: 3,
            versionId: 50,
            titleId: 5,
            title: "Slow Horses",
            year: 2022,
            posterPath: "",
            state: "queued",
            found: 0,
            missing: 2,
            downloading: 0,
          },
        ],
      }),
    });
    await screen.findByText("Ronin (2020)");
    expect(await screen.findByText("In backfill (#1)")).toBeInTheDocument();
  });
});
