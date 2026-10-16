import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resetToastsForTests } from "@/hooks/toast";
import { json, renderApp, signedIn } from "@/test/app";
import { blocked, blocklistOf, stuck } from "@/test/blocklist";
import { seriesDetail } from "@/test/series";

afterEach(() => {
  vi.unstubAllGlobals();
  resetToastsForTests();
});

const movie = {
  id: 3,
  tmdbId: 438631,
  imdbId: "",
  title: "Dune",
  originalTitle: "Dune",
  sortTitle: "dune",
  year: 2021,
  runtime: 155,
  genres: [],
  certification: "PG-13",
  overview: "",
  posterPath: "",
  backdropPath: "",
  addedAt: "2026-10-08T00:00:00Z",
  versions: [],
};

function openMovie(list: ReturnType<typeof blocklistOf>, extra: Record<string, never> = {}) {
  let current = list;
  const view = renderApp("/movies/3", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /movies/3": json(movie),
    "GET /blocklist": () => json(current),
    "DELETE /blocklist/2": () => {
      current = blocklistOf(current.items.filter((b) => b.id !== 2));
      return new Response(null, { status: 204 });
    },
    ...extra,
  });
  return view;
}

describe("blocked releases on a movie's page", () => {
  it("asks for this movie's entries, dormant ones too", async () => {
    const { calls } = openMovie(blocklistOf([stuck()]));
    await screen.findByRole("button", { name: /Blocked releases \(1\)/ });
    const call = calls.find((c) => new URL(c.url).pathname.endsWith("/blocklist"));
    const url = new URL(call?.url ?? "http://x");
    expect(url.searchParams.get("titleId")).toBe("3");
    expect(url.searchParams.get("include")).toBe("dormant");
  });

  it("is hidden when nothing is blocked", async () => {
    const { calls } = openMovie(blocklistOf([]));
    await screen.findByRole("heading", { name: "Dune" });
    await waitFor(() => expect(calls.some((c) => c.url.includes("/blocklist"))).toBe(true));
    expect(screen.queryByText(/Blocked releases/)).not.toBeInTheDocument();
  });

  it("is folded until opened, then shows each release in plain words", async () => {
    openMovie(blocklistOf([blocked(), stuck()]));
    const toggle = await screen.findByRole("button", { name: /Blocked releases \(2\)/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Dune.2021.CAM-FAKE")).not.toBeInTheDocument();

    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText(/tried again after 30 days/)).toBeInTheDocument();
    expect(screen.getByText("Dune.2021.CAM-FAKE")).toBeInTheDocument();
    expect(screen.getByText("CAM · Tracker A")).toBeInTheDocument();
    expect(screen.getByText("Removed from the queue by you")).toBeInTheDocument();
    expect(screen.getAllByText("Blocked 3 days ago")).toHaveLength(2);
    expect(screen.getByText("Permanent")).toBeInTheDocument();
    expect(screen.getByText("Expires in 27 days")).toBeInTheDocument();
    expect(screen.getByText("Cleanup: No progress for 2 days.")).toBeInTheDocument();
  });

  it("unblocks a release and says so", async () => {
    const { calls } = openMovie(blocklistOf([blocked(), stuck()]));
    await userEvent.click(await screen.findByRole("button", { name: /Blocked releases \(2\)/ }));
    await userEvent.click(
      screen.getByRole("button", { name: "Unblock Dune.2021.1080p.WEB-STUCK" }),
    );
    expect(await screen.findByText("Unblocked. Reelkeeper may grab it again.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "DELETE" && c.url.endsWith("/blocklist/2"))).toBe(true);
    // The list refreshes: one left.
    await screen.findByRole("button", { name: /Blocked releases \(1\)/ });
    expect(screen.queryByText("Dune.2021.1080p.WEB-STUCK")).not.toBeInTheDocument();
  });
});

describe("blocked releases on a series' page", () => {
  it("shows which episodes a release was for", async () => {
    renderApp("/series/7", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /settings/labels": json({ parts: ["profile", "quality", "hdr"] }),
      "GET /series/7": json(seriesDetail()),
      "GET /blocklist": json(
        blocklistOf([
          stuck({
            id: 9,
            movie: { id: 7, title: "Show", year: 2010 },
            mediaType: "series",
            releaseTitle: "Show.S02.1080p.WEB-STUCK",
            episodes: "season 2 (8 wanted episodes)",
          }),
        ]),
      ),
    });
    await userEvent.click(await screen.findByRole("button", { name: /Blocked releases \(1\)/ }));
    const row = screen.getByText("Show.S02.1080p.WEB-STUCK").closest("li");
    expect(row).not.toBeNull();
    expect(
      within(row as HTMLElement).getByText("Episodes: season 2 (8 wanted episodes)"),
    ).toBeInTheDocument();
  });
});
