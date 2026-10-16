import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";
import { seriesDetail, seriesSummary } from "@/test/series";

afterEach(() => vi.unstubAllGlobals());

function open(roots: unknown[]) {
  try {
    localStorage.clear();
  } catch {}
  const added: unknown[] = [];
  const queries: string[] = [];
  const view = renderApp("/series", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /series": json([seriesSummary(1, "Andor")]),
    "GET /library/review": json([]),
    "GET /settings/labels": json({ parts: [] }),
    "GET /library/roots": json(roots),
    "GET /profiles": json([
      { id: 1, name: "Movie only", slug: "mo", appliesTo: "movie" },
      { id: 2, name: "HD", slug: "hd", appliesTo: "both" },
      { id: 3, name: "Shows", slug: "shows", appliesTo: "series" },
    ]),
    "GET /series/lookup": (req) => {
      queries.push(new URL(req.url).searchParams.get("q") ?? "");
      return json([
        {
          tmdbId: 57243,
          title: "Doctor Who",
          year: 2005,
          overview: "A time lord.",
          posterPath: "",
          inLibrary: false,
        },
        {
          tmdbId: 1,
          title: "Andor",
          year: 2022,
          overview: "",
          posterPath: "",
          inLibrary: true,
          seriesId: 1,
        },
      ]);
    },
    "POST /series": async (req) => {
      added.push(await req.json());
      return json(seriesDetail());
    },
    "GET /series/7": json(seriesDetail()),
  });
  return { ...view, added, queries };
}

const tvRoot = { id: 5, path: "/media/shows", mediaType: "series", defaultProfileId: 2 };
const movieRoot = { id: 1, path: "/media/movies", mediaType: "movie", defaultProfileId: 2 };

describe("add series dialog", () => {
  it("searches TMDB and adds to a TV folder with a series-capable profile", async () => {
    const { added, queries } = open([movieRoot, tvRoot]);
    await userEvent.click(await screen.findByRole("button", { name: "Add series" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Series title"), "doctor who 2005");
    expect(await within(dialog).findByText("Doctor Who")).toBeInTheDocument();
    expect(queries).toContain("doctor who 2005");
    expect(within(dialog).getByText("In your library")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByText("Doctor Who"));

    // Only profiles for series, and only TV folders.
    const profile = await within(dialog).findByLabelText("Quality profile");
    expect(within(profile).queryByRole("option", { name: "Movie only" })).not.toBeInTheDocument();
    expect(within(profile).getByRole("option", { name: "Shows" })).toBeInTheDocument();
    const folder = within(dialog).getByLabelText("Media folder");
    expect(
      within(folder)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["/media/shows"]);

    await userEvent.click(within(dialog).getByRole("button", { name: "Add series" }));
    await waitFor(() =>
      expect(added).toEqual([{ tmdbId: 57243, profileId: 2, rootFolderId: 5, monitored: true }]),
    );
    // Lands on the new series' page.
    expect(await screen.findByRole("heading", { name: "Show" })).toBeInTheDocument();
  });

  it("asks for a TV folder first when there is none", async () => {
    open([movieRoot]);
    await userEvent.click(await screen.findByRole("button", { name: "Add series" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Series title"), "doctor who");
    await userEvent.click(await within(dialog).findByText("Doctor Who"));
    expect(await within(dialog).findByText(/Add a TV folder/)).toBeInTheDocument();
  });
});
