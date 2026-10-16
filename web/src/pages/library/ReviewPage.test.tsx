import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const candidate = (tmdbId: number, title: string, year: number) => ({
  tmdbId,
  title,
  year,
  overview: "",
  posterPath: "",
});

const items = [
  {
    id: 1,
    rootFolderId: 1,
    rootPath: "/media/movies",
    mediaType: "movie",
    path: "Dune",
    titleGuess: "Dune",
    yearGuess: 0,
    reason: "Two movies match this name.",
    candidates: [candidate(10, "Dune", 1984)],
    files: ["Dune.mkv"],
  },
  {
    id: 2,
    rootFolderId: 5,
    rootPath: "/media/shows",
    mediaType: "series",
    path: "Doctor Who",
    titleGuess: "Doctor Who",
    yearGuess: 0,
    reason: "Several series are called Doctor Who.",
    candidates: [candidate(57243, "Doctor Who", 2005)],
    files: ["a.mkv", "b.mkv"],
  },
];

function open(path = "/review") {
  const resolved: { url: string; body: unknown }[] = [];
  const searches: string[] = [];
  const view = renderApp(path, {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /library/review": json(items),
    "GET /series/lookup": (req) => {
      searches.push(`series:${new URL(req.url).searchParams.get("q")}`);
      return json([candidate(1396, "Doctor Who (1963)", 1963)]);
    },
    "GET /movies/lookup": (req) => {
      searches.push(`movies:${new URL(req.url).searchParams.get("q")}`);
      return json([]);
    },
    "POST /library/review/2/resolve": async (req) => {
      resolved.push({ url: req.url, body: await req.json() });
      return json({});
    },
  });
  return { ...view, resolved, searches };
}

describe("review page", () => {
  it("shows movie and series folders, each described for what it is", async () => {
    open();
    expect(await screen.findByText("Two movies match this name.")).toBeInTheDocument();
    expect(screen.getByText("Several series are called Doctor Who.")).toBeInTheDocument();
    expect(screen.getByText("Movie folder")).toBeInTheDocument();
    expect(screen.getByText("Series folder")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Not a movie: ignore/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Not a series: ignore/ })).toBeInTheDocument();
  });

  it("resolves a series folder with a TMDB series ID", async () => {
    const { resolved } = open();
    const card = (await screen.findByText("Several series are called Doctor Who.")).closest(
      "div[class*='space-y-4']",
    ) as HTMLElement;
    await userEvent.click(within(card).getByRole("button", { name: /This one/ }));
    await waitFor(() => expect(resolved).toHaveLength(1));
    expect(resolved[0]?.body).toEqual({ tmdbId: 57243 });
  });

  it("searches TMDB for series, not movies, on a series folder", async () => {
    const { searches } = open();
    const card = (await screen.findByText("Several series are called Doctor Who.")).closest(
      "div[class*='space-y-4']",
    ) as HTMLElement;
    await userEvent.click(within(card).getByRole("button", { name: /None of these: search/ }));
    expect(await within(card).findByText("Doctor Who (1963)")).toBeInTheDocument();
    expect(searches).toContain("series:Doctor Who");
    expect(searches.some((s) => s.startsWith("movies:"))).toBe(false);
  });

  it("keeps the old /movies/review link working", async () => {
    const { router } = open("/movies/review");
    expect(await screen.findByText("Two movies match this name.")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/review");
  });
});
