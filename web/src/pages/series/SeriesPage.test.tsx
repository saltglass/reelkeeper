import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resetAppearanceForTests } from "@/hooks/theme";
import { json, renderApp, signedIn } from "@/test/app";
import { seriesSummary, seriesVersion } from "@/test/series";

afterEach(() => vi.unstubAllGlobals());

const list = [
  seriesSummary(1, "The Wire", {
    year: 2002,
    airedEpisodes: 60,
    versions: [
      seriesVersion({
        profileName: "Remux",
        profileId: 2,
        quality: "Remux-2160p",
        resolution: 2160,
        hdr: ["HDR10"],
        episodesWithFiles: 31,
      }),
    ],
  }),
  seriesSummary(2, "Andor", {
    year: 2022,
    airedEpisodes: 12,
    problems: 2,
    versions: [seriesVersion({ episodesWithFiles: 12, missing: 1 })],
  }),
  seriesSummary(3, "Severance", {
    year: 2022,
    airedEpisodes: 19,
    versions: [seriesVersion({ files: 0, size: 0, quality: "", episodesWithFiles: 0 })],
  }),
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
  return renderApp("/series", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /series": json(list),
    "GET /library/roots": json([{ id: 5, mediaType: "series" }]),
    "GET /profiles": json([
      { id: 1, name: "HD", slug: "hd", appliesTo: "both" },
      { id: 2, name: "Remux", slug: "remux", appliesTo: "series" },
      { id: 3, name: "Movie only", slug: "mo", appliesTo: "movie" },
    ]),
    "GET /library/review": json(review),
    "GET /settings/labels": json({ parts: labelParts }),
  });
}

const titles = () =>
  screen
    .getAllByRole("link")
    .filter((l) => /\/series\/\d+$/.test(l.getAttribute("href") ?? ""))
    .map((l) => ["Andor", "Severance", "The Wire"].find((t) => l.textContent?.includes(t)));

describe("series page", () => {
  it("lists series by title, with labels and episode progress", async () => {
    open();
    await screen.findByText("Andor");
    expect(titles()).toEqual(["Andor", "Severance", "The Wire"]);
    expect(await screen.findByText("Remux · Remux-2160p · HDR10")).toBeInTheDocument();
    expect(screen.getByText("HD · not on disk")).toBeInTheDocument();
    // The progress is the best version's count against episodes aired.
    expect(screen.getByText("31 of 60 episodes")).toBeInTheDocument();
    expect(screen.getByText("12 of 12 episodes")).toBeInTheDocument();
    expect(screen.getByRole("progressbar", { name: "The Wire episodes on disk" })).toHaveAttribute(
      "aria-valuenow",
      "52",
    );
    expect(screen.getByTitle(/Reelkeeper isn't sure which episode/)).toBeInTheDocument();
  });

  it("puts titles under the episode progress, or hides them when turned off", async () => {
    const { unmount } = open();
    const poster = await screen.findByRole("link", { name: /^Andor \(2022\)/ });
    expect(within(poster).queryByText("Andor")).not.toBeInTheDocument();
    // Below the progress line and above the version labels.
    const caption = screen.getByText("Andor");
    const progress = screen.getByText("12 of 12 episodes");
    const label = await screen.findByTitle("1 file(s) missing");
    expect(
      progress.compareDocumentPosition(caption) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(caption.compareDocumentPosition(label) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    unmount();

    open([], undefined, { posterTitles: false });
    expect(await screen.findByRole("link", { name: /^Andor \(2022\)/ })).toBeInTheDocument();
    expect(screen.queryByText("Andor")).not.toBeInTheDocument();
  });

  it("has Series in the menu right after Movies", async () => {
    open();
    await screen.findByText("Andor");
    const nav = screen.getAllByRole("navigation", { name: "Main" })[0] as HTMLElement;
    const names = within(nav)
      .getAllByRole("link")
      // Without the counts beside some of them ("Series 3").
      .map((l) => l.textContent?.replace(/\d+$/, ""));
    expect(names.indexOf("Series")).toBe(names.indexOf("Movies") + 1);
  });

  it("shows only the label parts chosen in settings", async () => {
    open([], ["quality"]);
    expect(await screen.findByText("Remux-2160p")).toBeInTheDocument();
    expect(screen.queryByText(/Remux · /)).not.toBeInTheDocument();
  });

  it("sorts by episodes missing", async () => {
    open();
    await screen.findByText("Andor");
    await userEvent.selectOptions(screen.getByLabelText("Sort by"), "missing");
    expect(titles()).toEqual(["The Wire", "Severance", "Andor"]);
    await userEvent.selectOptions(screen.getByLabelText("Sort by"), "year");
    expect(titles()[2]).toBe("The Wire");
  });

  it("searches and filters", async () => {
    open();
    await screen.findByText("Andor");
    await userEvent.type(screen.getByLabelText("Search series"), "wire");
    expect(screen.queryByText("Andor")).not.toBeInTheDocument();
    await userEvent.clear(screen.getByLabelText("Search series"));

    const chip = (name: RegExp) => screen.getByRole("button", { name });
    await userEvent.click(chip(/^Missing episodes/));
    expect(titles()).toEqual(["Severance", "The Wire"]);

    await userEvent.click(chip(/^Needs attention/));
    expect(titles()).toEqual(["Andor"]);

    await userEvent.click(chip(/^Not on disk yet/));
    expect(titles()).toEqual(["Severance"]);

    await userEvent.selectOptions(screen.getByLabelText("More filters"), "profile:2");
    expect(titles()).toEqual(["The Wire"]);

    // Profiles for movies only aren't offered.
    expect(screen.queryByRole("option", { name: "Movie only versions" })).not.toBeInTheDocument();
  });

  it("switches to a table", async () => {
    open();
    await screen.findByText("Andor");
    await userEvent.click(screen.getByRole("button", { name: "Table view" }));
    const table = screen.getByRole("table");
    expect(within(table).getByText("31 of 60 episodes")).toBeInTheDocument();
  });

  it("counts only TV folders in the review banner", async () => {
    open([
      { id: 1, mediaType: "movie" },
      { id: 2, mediaType: "series" },
      { id: 3, mediaType: "series" },
    ]);
    expect(await screen.findByText(/2 folders need/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Review now" })).toHaveAttribute("href", "/review");
  });

  it("shows the empty state and a link to add a TV folder", async () => {
    try {
      localStorage.clear();
    } catch {}
    renderApp("/series", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /series": json([]),
      "GET /library/roots": json([{ id: 1, mediaType: "movie" }]),
      "GET /profiles": json([]),
      "GET /library/review": json([]),
      "GET /settings/labels": json({ parts: [] }),
    });
    expect(await screen.findByText("No series yet.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add a TV media folder" })).toBeInTheDocument();
  });
});
