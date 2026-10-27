import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { MovieSummary, SeriesSummary } from "@/hooks/library";
import { resetAppearanceForTests } from "@/hooks/theme";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

type Version = MovieSummary["versions"][number];
const v = (over: Partial<Version> = {}): Version => ({
  id: 1,
  profileId: 1,
  profileName: "HD",
  rootFolderId: 1,
  rootPath: "/media/movies",
  folder: "x",
  monitored: true,
  upgrades: true,
  files: 1,
  missing: 0,
  size: 5 * 1024 ** 3,
  quality: "Bluray-1080p",
  resolution: 1080,
  hdr: [],
  videoCodec: "H.264",
  audioCodec: "DTS",
  audioChannels: 6,
  releaseGroup: "GRP",
  edition: "",
  episodesWithFiles: 0,
  ...over,
});
const movie = (id: number, title: string, versions: Version[]): MovieSummary => ({
  id,
  tmdbId: id,
  imdbId: "",
  title,
  sortTitle: title.toLowerCase(),
  year: 2000 + id,
  runtime: 100,
  genres: [],
  posterPath: "",
  addedAt: "2026-10-01T00:00:00Z",
  versions,
});

const movies = [
  movie(1, "Alien", [v({ id: 11, upgrades: false })]),
  movie(2, "Brazil", [v({ id: 12 })]),
  movie(3, "Contact", [v({ id: 13 }), v({ id: 14, profileId: 2, profileName: "Remux" })]),
];

const profiles = [
  { id: 1, name: "HD", slug: "hd", appliesTo: "both" },
  { id: 2, name: "Remux", slug: "remux", appliesTo: "movie" },
];

function base(extra: Record<string, unknown> = {}) {
  resetAppearanceForTests();
  try {
    localStorage.clear();
  } catch {}
  return {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /library/roots": json([{ id: 1 }]),
    "GET /profiles": json(profiles),
    "GET /library/review": json([]),
    "GET /settings/labels": json({ parts: ["profile"] }),
    ...extra,
  } as Parameters<typeof renderApp>[1];
}

function openMovies(result = { titles: 2, versions: 2, searched: 0, later: 0 }) {
  const sent: unknown[] = [];
  renderApp(
    "/movies",
    base({
      "GET /movies": json(movies),
      "POST /movies/bulk": async (req: Request) => {
        sent.push(await req.json());
        return json(result);
      },
    }),
  );
  return sent;
}

const bar = () => screen.getByRole("region", { name: "Change the selected movies" });

describe("selecting movies", () => {
  it("ticks posters instead of opening them, and turns upgrades off for them", async () => {
    const sent = openMovies();
    await userEvent.click(await screen.findByRole("button", { name: "Select" }));
    // While selecting, a poster is a tick box, not a link.
    const alien = screen.getByRole("button", { name: "Alien (2001)" });
    expect(screen.queryByRole("link", { name: /^Alien \(2001\)/ })).not.toBeInTheDocument();
    await userEvent.click(alien);
    await userEvent.click(screen.getByRole("button", { name: "Brazil (2002)" }));
    expect(alien).toHaveAttribute("aria-pressed", "true");
    expect(within(bar()).getByText("2 selected")).toBeInTheDocument();

    await userEvent.click(within(bar()).getByRole("button", { name: /Upgrades/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Turn upgrades off" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Turn upgrades off for 2 movies?")).toBeInTheDocument();
    expect(within(dialog).getByText("1 already has upgrades off.")).toBeInTheDocument();
    // One profile among them: no version question.
    expect(within(dialog).queryByLabelText("Which versions")).not.toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Change" }));
    expect(await screen.findByText("Changed 2 versions of 2 movies.")).toBeInTheDocument();
    expect(sent).toEqual([{ titleIds: [1, 2], upgrades: false }]);
  });

  it("selects everything shown, and asks which versions when profiles differ", async () => {
    const sent = openMovies({ titles: 3, versions: 3, searched: 0, later: 0 });
    await userEvent.click(await screen.findByRole("button", { name: "Select" }));
    await userEvent.click(within(bar()).getByRole("button", { name: "Select all 3 shown" }));
    expect(within(bar()).getByText("3 selected")).toBeInTheDocument();

    await userEvent.click(within(bar()).getByRole("button", { name: /Monitoring/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Stop monitoring" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Stop monitoring 3 movies?")).toBeInTheDocument();
    expect(within(dialog).getByText("This changes 4 versions.")).toBeInTheDocument();
    await userEvent.selectOptions(
      within(dialog).getByLabelText("Which versions"),
      "Only Remux versions (1)",
    );
    expect(within(dialog).getByText("Stop monitoring 1 movie?")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Change" }));
    await screen.findByText(/^Changed/);
    expect(sent).toEqual([{ titleIds: [1, 2, 3], profileId: 2, monitored: false }]);
  });

  it("says when only the first 10 are searched now", async () => {
    const many = Array.from({ length: 12 }, (_, i) =>
      movie(i + 1, `Movie ${String(i + 1).padStart(2, "0")}`, [
        v({ id: 100 + i, monitored: false, files: 0 }),
      ]),
    );
    renderApp(
      "/movies",
      base({
        "GET /movies": json(many),
        "POST /movies/bulk": json({ titles: 12, versions: 12, searched: 10, later: 2 }),
      }),
    );
    await userEvent.click(await screen.findByRole("button", { name: "Select" }));
    await userEvent.click(within(bar()).getByRole("button", { name: "Select all 12 shown" }));
    await userEvent.click(within(bar()).getByRole("button", { name: /Monitoring/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Monitor" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText(/Up to 10 that are missing something are searched now/),
    ).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Change" }));
    expect(
      await screen.findByText(
        "Changed 12 versions of 12 movies. Searching for 10 now; 2 more by the hourly search.",
      ),
    ).toBeInTheDocument();
  });

  it("uses tick boxes in the table, and Done leaves selecting", async () => {
    openMovies();
    await screen.findByText("Alien");
    await userEvent.click(screen.getByRole("button", { name: "Table view" }));
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Select" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Contact (2003)" }));
    expect(within(bar()).getByText("1 selected")).toBeInTheDocument();
    await userEvent.click(within(bar()).getByRole("button", { name: "Done" }));
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("region", { name: "Change the selected movies" }),
    ).not.toBeInTheDocument();
  });

  it("shows the server's reason when it refuses", async () => {
    renderApp(
      "/movies",
      base({
        "GET /movies": json(movies),
        "POST /movies/bulk": json({ code: "not_found", message: "Not found." }, 404),
      }),
    );
    await userEvent.click(await screen.findByRole("button", { name: "Select" }));
    await userEvent.click(screen.getByRole("button", { name: "Alien (2001)" }));
    await userEvent.click(within(bar()).getByRole("button", { name: /Monitoring/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Monitor" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Change" }));
    expect(await within(dialog).findByText("Not found.")).toBeInTheDocument();
  });
});

const show = (id: number, title: string): SeriesSummary => ({
  id,
  tmdbId: id,
  tvdbId: id,
  imdbId: "",
  title,
  sortTitle: title.toLowerCase(),
  year: 2010 + id,
  status: "Ended",
  network: "",
  seriesType: "standard",
  genres: [],
  posterPath: "",
  seasons: 1,
  airedEpisodes: 10,
  problems: 0,
  addedAt: "2026-10-01T00:00:00Z",
  versions: [v({ id: 200 + id, upgrades: true })],
});

describe("selecting shows", () => {
  it("sets which episodes to monitor, with no upgrades choice", async () => {
    const sent: unknown[] = [];
    renderApp(
      "/series",
      base({
        "GET /series": json([show(1, "Firefly"), show(2, "Fargo")]),
        "GET /backfill": json({ items: [] }),
        "POST /series/bulk": async (req: Request) => {
          sent.push(await req.json());
          return json({ titles: 2, versions: 2, searched: 2, later: 0 });
        },
      }),
    );
    await userEvent.click(await screen.findByRole("button", { name: "Select" }));
    const region = screen.getByRole("region", { name: "Change the selected shows" });
    await userEvent.click(within(region).getByRole("button", { name: "Select all 2 shown" }));
    expect(within(region).queryByRole("button", { name: /Upgrades/ })).not.toBeInTheDocument();
    await userEvent.click(within(region).getByRole("button", { name: /Episodes/ }));
    await userEvent.click(
      await screen.findByRole("menuitem", { name: "Monitor future episodes only" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("Monitor only future episodes of 2 shows?"),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText(/replaces any choices made per season or episode/),
    ).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Change" }));
    expect(
      await screen.findByText("Changed 2 versions of 2 shows. Searching for 2 now."),
    ).toBeInTheDocument();
    expect(sent).toEqual([{ titleIds: [2, 1], monitor: "future" }]);
  });
});
