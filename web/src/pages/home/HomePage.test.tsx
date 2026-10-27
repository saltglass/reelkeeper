import { screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Summary } from "@/hooks/summary";
import { resetAppearanceForTests } from "@/hooks/theme";
import { json, renderApp, signedIn } from "@/test/app";
import { dayLabel, problemsOf } from "./HomePage";

afterEach(() => vi.unstubAllGlobals());

const summary = (over: Partial<Summary> = {}): Summary => ({
  queue: { movies: 1, tv: 0, needsAttention: 0, limit: 0, full: false },
  upcoming: [],
  upcomingEpisodes: [],
  indexers: { ok: 2, total: 2, failing: [] },
  grabs: { today: 3, limit: 0, mode: "on" },
  ...over,
});

const queue = (over: object = {}) => ({
  items: [],
  clientError: null,
  clientName: "qBittorrent",
  limit: 0,
  full: false,
  builtinState: null,
  diskShort: null,
  ...over,
});

const on = { movies: "on", tv: "on" } as const;

describe("what needs attention", () => {
  it("lists nothing when all is well", () => {
    expect(
      problemsOf({ queue: queue(), summary: summary(), review: 0, seriesProblems: 0, modes: on }),
    ).toEqual([]);
  });

  it("lists each problem, worst first, each linking to its fix", () => {
    const ps = problemsOf({
      queue: queue({
        clientError: "Can't reach qBittorrent.",
        builtinState: "down",
        diskShort: "The download disk has 18.0 GB free.",
      }),
      summary: summary({
        queue: { movies: 8, tv: 2, needsAttention: 2, limit: 10, full: true },
        indexers: { ok: 1, total: 3, failing: ["1337x", "kickasstorrents.to"] },
      }),
      review: 3,
      seriesProblems: 1,
      modes: { movies: "on", tv: "dryrun" },
    });
    expect(ps.map((p) => [p.tone, p.text, p.to])).toEqual([
      ["error", "Can't reach qBittorrent.", "/settings/download-client"],
      ["error", "The VPN is down, so downloads are stopped.", "/settings/download-client"],
      [
        "warning",
        "The download disk has 18.0 GB free. Grabs and the hourly search wait until cleanup makes room.",
        "/settings/cleanup",
      ],
      ["warning", "2 downloads finished but couldn't be imported.", "/activity/queue"],
      [
        "warning",
        "The queue is full (10 of 10), so nothing new is grabbed until downloads finish.",
        "/activity/queue",
      ],
      ["warning", "3 folders need your help identifying them.", "/review"],
      ["warning", "1 show has files Reelkeeper isn't sure about.", "/series"],
      ["warning", "1337x and kickasstorrents.to are failing.", "/settings/indexers"],
      [
        "info",
        "TV grabbing is in dry run: Reelkeeper notes what it would grab but downloads nothing.",
        "/settings/download-client",
      ],
    ]);
    expect(ps[6]?.search).toEqual({ filter: "attention" });
  });

  it("words grabbing once when movies and TV agree", () => {
    expect(problemsOf({ modes: { movies: "off", tv: "off" } }).map((p) => p.text)).toEqual([
      "Grabbing is off: Reelkeeper doesn't search or download.",
    ]);
  });

  it("doesn't promise waiting searches while grabbing is off", () => {
    const [p] = problemsOf({
      queue: queue({ diskShort: "The download disk has 18.0 GB free." }),
      modes: { movies: "dryrun", tv: "off" },
    });
    expect(p?.text).toBe("The download disk has 18.0 GB free.");
  });
});

describe("dates", () => {
  const now = new Date(2026, 9, 27, 12);
  it("says today and tomorrow, then the day", () => {
    expect(dayLabel("2026-10-27", now)).toBe("Today");
    expect(dayLabel("2026-10-28", now)).toBe("Tomorrow");
    expect(dayLabel("2026-10-30", now)).toBe("Fri 30 Oct");
  });
});

function openHome(over: Record<string, Response> = {}) {
  resetAppearanceForTests();
  try {
    localStorage.clear();
  } catch {}
  return renderApp("/", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /summary": json(summary()),
    "GET /queue": json(queue()),
    "GET /library/review": json([]),
    "GET /movies": json([{ id: 1 }, { id: 2 }]),
    "GET /series": json([{ id: 3, problems: 0 }]),
    "GET /wanted": json({
      missing: [],
      upgrades: [],
      counts: { missingMovies: 4, missingEpisodes: 3, upgrades: 0 },
    }),
    "GET /settings/grabbing": json({ mode: "on", tvMode: "on", queueLimit: 0, minSeeders: 1 }),
    "GET /library/roots": json([{ id: 1 }]),
    "GET /indexers": json([{ id: 1 }]),
    "GET /settings/download-client": json({
      type: "qbittorrent",
      url: "http://qb:8080",
      folder: "",
    }),
    "GET /history": json({ events: [], nextBefore: null }),
    ...over,
  });
}

describe("home page", () => {
  it("is where the app opens, with Home in the sidebar", async () => {
    openHome();
    expect(await screen.findByRole("heading", { name: "Home" })).toBeInTheDocument();
    const link = screen.getAllByRole("link", { name: "Home" })[0];
    expect(link).toHaveAttribute("href", "/");
    expect(link).toHaveAttribute("data-status", "active");
    // The other pages aren't marked active too.
    expect(screen.getAllByRole("link", { name: /^Movies/ })[0]).not.toHaveAttribute(
      "data-status",
      "active",
    );
  });

  it("says all is good, with the numbers", async () => {
    openHome();
    expect(
      await screen.findByText(/All good\. Reelkeeper is searching and downloading normally\./),
    ).toBeInTheDocument();
    const main = screen.getByRole("main");
    const wanted = within(main).getByText("Wanted").closest("a");
    expect(wanted).toHaveAttribute("href", "/wanted");
    expect(within(wanted as HTMLElement).getByText("7")).toBeInTheDocument();
    expect(within(main).getByText("Grabbed today").closest("a")).toHaveTextContent("3");
    expect(screen.getByText("Nothing downloading.")).toBeInTheDocument();
    expect(screen.getByText("Nothing grabbed or imported yet.")).toBeInTheDocument();
    expect(screen.getByText("Nothing due in the next 7 days.")).toBeInTheDocument();
  });

  it("lists problems as links to where they're fixed", async () => {
    openHome({
      "GET /queue": json(queue({ diskShort: "The download disk has 18.0 GB free." })),
      "GET /summary": json(summary({ indexers: { ok: 1, total: 2, failing: ["1337x"] } })),
      "GET /series": json([{ id: 3, problems: 2 }]),
    });
    const region = (await screen.findByText("Needs your attention")).parentElement as HTMLElement;
    expect(within(region).getByRole("link", { name: /1337x is failing\./ })).toHaveAttribute(
      "href",
      "/settings/indexers",
    );
    expect(
      within(region).getByRole("link", { name: /The download disk has 18\.0 GB free\./ }),
    ).toHaveAttribute("href", "/settings/cleanup");
    expect(within(region).getByRole("link", { name: /1 show has files/ })).toHaveAttribute(
      "href",
      "/series?filter=attention",
    );
    expect(screen.queryByText(/All good/)).not.toBeInTheDocument();
  });

  it("shows the setup checklist on a new install", async () => {
    openHome({
      "GET /library/roots": json([]),
      "GET /indexers": json([{ id: 1 }]),
      "GET /settings/download-client": json({ type: "builtin", url: "", folder: "" }),
    });
    expect(await screen.findByText("Finish setting up")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /To do: Add a media folder/ })).toHaveAttribute(
      "href",
      "/settings/media",
    );
    expect(screen.getByRole("link", { name: /Done: Add an indexer/ })).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /To do: Set up a download client/ }),
    ).toBeInTheDocument();
    expect(screen.queryByText(/All good/)).not.toBeInTheDocument();
  });

  it("shows downloads, recent grabs and imports, and what's coming up", async () => {
    const today = new Date();
    const iso = `${today.getFullYear()}-${String(today.getMonth() + 1).padStart(2, "0")}-${String(today.getDate()).padStart(2, "0")}`;
    openHome({
      "GET /queue": json(
        queue({
          items: [
            {
              id: 1,
              status: "downloading",
              movie: { id: 9, title: "Glass Meridian", year: 1994 },
              episodes: "",
              releaseTitle: "Glass.Meridian.1994.1080p",
              torrent: { progress: 0.42, etaSeconds: 600 },
            },
            {
              id: 2,
              status: "import-failed",
              movie: { id: 8, title: "Broken", year: 2000 },
              episodes: "",
              releaseTitle: "x",
            },
          ],
        }),
      ),
      "GET /history": json({
        events: [
          {
            id: 5,
            createdAt: new Date(Date.now() - 3600_000).toISOString(),
            titleId: 3,
            mediaType: "series",
            title: "The Night Desk",
            event: "imported",
            sourceTitle: "The.Night.Desk.S01E02",
          },
        ],
        nextBefore: null,
      }),
      "GET /summary": json(
        summary({
          upcoming: [{ titleId: 9, title: "Paper Comets", year: 2026, kind: "digital", date: iso }],
          upcomingEpisodes: [
            {
              titleId: 3,
              title: "The Night Desk",
              season: 1,
              episode: 4,
              name: "Static",
              date: "2099-01-01",
            },
          ],
        }),
      ),
    });
    expect(await screen.findByText("Glass Meridian")).toBeInTheDocument();
    expect(screen.getByText(/42% · 10m 00s left/)).toBeInTheDocument();
    expect(screen.queryByText("Broken")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "See all 1 in the Queue" })).toHaveAttribute(
      "href",
      "/activity/queue",
    );
    const recent = await screen.findByText("Imported");
    expect(
      within(recent.closest("li") as HTMLElement).getByRole("link", { name: "The Night Desk" }),
    ).toHaveAttribute("href", "/series/3");
    expect(screen.getByRole("link", { name: "Paper Comets (2026)" })).toHaveAttribute(
      "href",
      "/movies/9",
    );
    const comet = screen
      .getByRole("link", { name: "Paper Comets (2026)" })
      .closest("li") as HTMLElement;
    expect(within(comet).getByText("Today")).toBeInTheDocument();
    expect(within(comet).getByText(/digital release/)).toBeInTheDocument();
    expect(screen.getByText(/S01E04 Static/)).toBeInTheDocument();
  });
});

describe("the Series link from Home", () => {
  it("opens Series showing only the shows that need attention", async () => {
    resetAppearanceForTests();
    try {
      localStorage.clear();
    } catch {}
    const show = (id: number, title: string, problems: number) => ({
      id,
      tmdbId: id,
      tvdbId: id,
      imdbId: "",
      title,
      sortTitle: title.toLowerCase(),
      year: 2020,
      status: "Ended",
      network: "",
      seriesType: "standard",
      genres: [],
      posterPath: "",
      seasons: 1,
      airedEpisodes: 1,
      problems,
      addedAt: "2026-10-01T00:00:00Z",
      versions: [],
    });
    renderApp("/series?filter=attention", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /series": json([show(1, "Fine Show", 0), show(2, "Odd Files", 1)]),
      "GET /library/roots": json([{ id: 1 }]),
      "GET /profiles": json([]),
      "GET /library/review": json([]),
      "GET /settings/labels": json({ parts: [] }),
    });
    expect(await screen.findByText("Odd Files")).toBeInTheDocument();
    expect(screen.queryByText("Fine Show")).not.toBeInTheDocument();
  });
});
