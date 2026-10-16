import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { MovieSummary } from "@/hooks/library";
import { resetAppearanceForTests } from "@/hooks/theme";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const v = (over: Partial<MovieSummary["versions"][number]> = {}) => ({
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
  hdr: [] as string[],
  videoCodec: "H.264",
  audioCodec: "DTS",
  audioChannels: 6,
  releaseGroup: "GRP",
  edition: "",
  episodesWithFiles: 0,
  ...over,
});
const movie = (id: number, title: string, year: number, versions = [v()]): MovieSummary => ({
  id,
  tmdbId: id,
  imdbId: "",
  title,
  sortTitle: title.toLowerCase().replace(/^the /, ""),
  year,
  runtime: 100,
  genres: [],
  posterPath: `/poster${id}.jpg`,
  addedAt: `2026-10-0${id}T00:00:00Z`,
  versions,
});

const movies = [
  movie(1, "The Dark Knight", 2008, [
    v({
      profileName: "Remux",
      profileId: 2,
      quality: "Remux-2160p",
      resolution: 2160,
      hdr: ["DV", "HDR10"],
    }),
  ]),
  movie(2, "Dune", 2021, [v({ missing: 1 })]),
  movie(3, "1917", 2019, [v({ files: 0, size: 0, quality: "" })]),
];

function open(
  review: unknown[] = [],
  labelParts = ["profile", "quality", "hdr"],
  appearance?: object,
) {
  resetAppearanceForTests();
  try {
    localStorage.clear();
    if (appearance) localStorage.setItem("reelkeeper.appearance", JSON.stringify(appearance));
  } catch {}
  return renderApp("/movies", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /movies": json(movies),
    "GET /library/roots": json([{ id: 1 }]),
    "GET /profiles": json([
      { id: 1, name: "HD", slug: "hd", appliesTo: "both" },
      { id: 2, name: "Remux", slug: "remux", appliesTo: "movie" },
    ]),
    "GET /library/review": json(review),
    "GET /settings/labels": json({ parts: labelParts }),
  });
}

describe("movies page", () => {
  it("lists movies sorted by title, ignoring 'The'", async () => {
    open();
    await screen.findByText("Dune");
    const order = screen
      .getAllByRole("link")
      .filter((l) => /\/movies\/\d+$/.test(l.getAttribute("href") ?? ""))
      .map((l) => ["1917", "The Dark Knight", "Dune"].find((t) => l.textContent?.startsWith(t)));
    expect(order).toEqual(["1917", "The Dark Knight", "Dune"]);
    expect(await screen.findByText("Remux · Remux-2160p · DV · HDR10")).toBeInTheDocument();
    expect(screen.getByText("HD · not on disk")).toBeInTheDocument();
  });

  it("shows only the label parts chosen in settings", async () => {
    open([], ["quality", "audio", "size"]);
    expect(await screen.findByText("Remux-2160p · DTS 5.1 · 5.0 GB")).toBeInTheDocument();
    // Without the profile part, a version with no files still says so.
    expect(screen.getByText("not on disk")).toBeInTheDocument();
  });

  it("shows no labels when nothing is chosen, except warnings", async () => {
    open([], []);
    expect(await screen.findByTitle("1 file(s) missing")).toBeInTheDocument();
    expect(screen.queryByText(/Remux-2160p/)).not.toBeInTheDocument();
  });

  it("puts titles under the posters, or hides them when turned off", async () => {
    const { unmount } = open();
    const poster = await screen.findByRole("link", { name: /^Dune \(2021\)/ });
    // The title is a caption below the poster, not drawn over it.
    expect(within(poster).queryByText("Dune")).not.toBeInTheDocument();
    expect(screen.getByText("Dune")).toBeInTheDocument();
    unmount();

    open([], undefined, { posterTitles: false });
    // Still named for screen readers, but no caption.
    expect(await screen.findByRole("link", { name: /^Dune \(2021\)/ })).toBeInTheDocument();
    expect(screen.queryByText("Dune")).not.toBeInTheDocument();
  });

  it("searches and filters", async () => {
    open();
    await screen.findByText("Dune");
    await userEvent.type(screen.getByLabelText("Search movies"), "dark");
    expect(screen.queryByText("Dune")).not.toBeInTheDocument();
    expect(screen.getByText("The Dark Knight")).toBeInTheDocument();

    await userEvent.clear(screen.getByLabelText("Search movies"));
    await userEvent.click(screen.getByRole("button", { name: /Files missing/ }));
    expect(screen.getByText("Dune")).toBeInTheDocument();
    expect(screen.queryByText("1917")).not.toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("More filters"), "dupes");
    expect(screen.getByText("No movies match.")).toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("More filters"), "profile:2");
    expect(screen.getByText("The Dark Knight")).toBeInTheDocument();
    expect(screen.queryByText("Dune")).not.toBeInTheDocument();
  });

  it("switches to a table and back", async () => {
    open();
    await screen.findByText("Dune");
    await userEvent.click(screen.getByRole("button", { name: "Table view" }));
    const table = screen.getByRole("table");
    expect(within(table).getAllByText("5.0 GB")).toHaveLength(2);
  });

  it("points to the review screen when folders need help", async () => {
    open([
      { id: 9, mediaType: "movie" },
      { id: 10, mediaType: "movie" },
      { id: 11, mediaType: "series" },
    ]);
    expect(await screen.findByText(/2 folders need/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Review now" })).toHaveAttribute("href", "/review");
  });
});
