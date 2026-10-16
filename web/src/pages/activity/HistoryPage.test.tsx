import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const base = {
  titleId: 3,
  versionId: 11,
  title: "28 Days Later",
  year: 2002,
  profile: "HD",
  sourceTitle: "",
  indexer: "",
  quality: "",
  score: 0,
  data: {},
};

const search = {
  ...base,
  id: 12,
  createdAt: "2026-10-09T10:00:00Z",
  event: "searched",
  message: "Found 2 releases; 1 acceptable.",
  data: {
    trigger: "manual",
    more: 4,
    indexerErrors: ["Tracker X timed out"],
    releases: [
      {
        name: "28.Days.Later.2002.1080p.BluRay-GRP",
        indexer: "Tracker A",
        size: 8 * 1024 ** 3,
        seeders: 20,
        quality: "Bluray-1080p",
        score: 150,
        accepted: true,
        formats: [{ name: "x265", score: 50 }],
      },
      {
        name: "28.Days.Later.2002.480p.DVDRip",
        indexer: "Tracker B",
        size: 700 * 1024 ** 2,
        seeders: 2,
        quality: "DVD",
        score: 0,
        accepted: false,
        rejections: ["Below the lowest quality this profile accepts"],
      },
    ],
  },
};
const wouldGrab = {
  ...base,
  id: 11,
  createdAt: "2026-10-09T10:00:01Z",
  event: "would-grab",
  message: "Would grab 28.Days.Later",
};
const older = {
  ...base,
  id: 5,
  createdAt: "2026-10-08T10:00:00Z",
  event: "rss",
  message: "Old RSS event",
};

function open() {
  return renderApp("/activity/history", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /history": (req) => {
      const u = new URL(req.url);
      if (u.searchParams.get("before")) return json({ events: [older], nextBefore: null });
      const kinds = u.searchParams.getAll("event");
      if (kinds.includes("would-grab")) return json({ events: [wouldGrab], nextBefore: null });
      return json({ events: [search, wouldGrab], nextBefore: 11 });
    },
  });
}

describe("History page", () => {
  it("lists events with a badge and the message, linking to the movie", async () => {
    open();
    expect(await screen.findByText("Found 2 releases; 1 acceptable.")).toBeInTheDocument();
    // One chip and one badge.
    expect(screen.getAllByText("Searched")).toHaveLength(2);
    expect(screen.getAllByRole("link", { name: "28 Days Later (2002)" })[0]).toHaveAttribute(
      "href",
      "/movies/3",
    );
  });

  it("opens a search to show each release and why it was rejected", async () => {
    open();
    const row = (await screen.findByText("Found 2 releases; 1 acceptable.")).closest("li");
    expect(row).not.toBeNull();
    await userEvent.click(within(row as HTMLElement).getByRole("button", { name: "Show details" }));
    expect(screen.getByText("28.Days.Later.2002.1080p.BluRay-GRP")).toBeInTheDocument();
    expect(screen.getByText("8.0 GB")).toBeInTheDocument();
    expect(screen.getByText("x265 +50")).toBeInTheDocument();
    expect(screen.getByText("Accepted")).toBeInTheDocument();
    expect(screen.getByText("Rejected")).toBeInTheDocument();
    expect(screen.getByText("Below the lowest quality this profile accepts")).toBeInTheDocument();
    expect(screen.getByText("+4 more not shown")).toBeInTheDocument();
    expect(screen.getByText("Tracker X timed out")).toBeInTheDocument();
  });

  it("loads more with the next-page cursor", async () => {
    const { calls } = open();
    await userEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByText("Old RSS event")).toBeInTheDocument();
    expect(calls.some((c) => new URL(c.url).searchParams.get("before") === "11")).toBe(true);
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("filters by event kind", async () => {
    open();
    await screen.findByText("Found 2 releases; 1 acceptable.");
    await userEvent.click(screen.getByRole("button", { name: "Would grab" }));
    await vi.waitFor(() =>
      expect(screen.queryByText("Found 2 releases; 1 acceptable.")).not.toBeInTheDocument(),
    );
    expect(screen.getByText("Would grab 28.Days.Later")).toBeInTheDocument();
  });

  it("has chips for the download events, and shows their message without a release table", async () => {
    const importedEvent = {
      ...base,
      id: 20,
      createdAt: "2026-10-09T11:00:00Z",
      event: "imported",
      message: "Imported as Dune (2021)/Dune (2021).mkv.",
    };
    const { calls } = renderApp("/activity/history", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /history": (req) => {
        const kinds = new URL(req.url).searchParams.getAll("event");
        return json({
          events: kinds.includes("imported") ? [importedEvent] : [search],
          nextBefore: null,
        });
      },
    });
    await screen.findByText("Found 2 releases; 1 acceptable.");
    for (const name of ["Imported", "Import failed", "Removed", "From Radarr", "From Sonarr"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
    await userEvent.click(screen.getByRole("button", { name: "Imported" }));
    expect(await screen.findByText("Imported as Dune (2021)/Dune (2021).mkv.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Show details" })).not.toBeInTheDocument();
    expect(
      calls.some((c) => new URL(c.url).searchParams.getAll("event").includes("imported")),
    ).toBe(true);
  });

  it("links a series event to the series page", async () => {
    renderApp("/activity/history", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /history": json({
        events: [
          {
            ...wouldGrab,
            id: 20,
            titleId: 9,
            title: "The Expanse",
            year: 2015,
            mediaType: "series",
            message: "Would grab The.Expanse.S02E05",
          },
        ],
        nextBefore: null,
      }),
    });
    expect(await screen.findByText("Would grab The.Expanse.S02E05")).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "The Expanse (2015)" })[0]).toHaveAttribute(
      "href",
      "/series/9",
    );
  });
});
