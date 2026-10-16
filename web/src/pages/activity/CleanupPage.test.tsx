import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const dune = { id: 3, title: "Dune", year: 2021 };

/** The saved download client; every page's wording follows its name. */
const client = (type: "qbittorrent" | "transmission") => ({
  type,
  url: type === "transmission" ? "http://tm:9091" : "http://qb:8080",
  username: "u",
  passwordSet: true,
  folder: "",
});

const seeding = (waiting: string, extra = {}) => ({
  items: [
    {
      downloadId: 1,
      movie: dune,
      releaseTitle: "Dune.2021.1080p",
      indexer: "A",
      quality: "WEBDL-1080p",
      importedAt: "2026-10-09T10:00:00Z",
      seedRatio: 1,
      seedTimeMinutes: 60,
      torrent: { ratio: 0.4, seedingTimeSeconds: 7200, state: "seeding", size: 100 },
      waiting,
      mediaType: "movie",
      episodes: "",
      ...extra,
    },
  ],
});

function event(id: number, action: string, rule: string, extra = {}) {
  return {
    id,
    createdAt: "2026-10-09T10:00:00Z",
    movie: dune,
    releaseTitle: `Release ${id}`,
    action,
    rule,
    message: `Because ${rule}`,
    ...extra,
  };
}

function open(
  mode: string,
  opts: {
    waiting?: string;
    seedingExtra?: object;
    orphans?: unknown;
    events?: unknown;
    more?: unknown;
    client?: ReturnType<typeof client>;
  } = {},
) {
  const removed: string[] = [];
  renderApp("/activity/cleanup", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /settings/cleanup": json({ mode }),
    "GET /settings/download-client": json(opts.client ?? client("qbittorrent")),
    "GET /cleanup/seeding": json(seeding(opts.waiting ?? "", opts.seedingExtra)),
    "GET /cleanup/events": (req) =>
      new URL(req.url).searchParams.has("before")
        ? json(opts.more ?? { events: [], nextBefore: null })
        : json(
            opts.events ?? {
              events: [
                event(4, "would-remove", "stalled"),
                event(3, "removed", "seeded"),
                event(2, "would-empty", "replaced-files", { movie: null, releaseTitle: "" }),
                event(1, "emptied", "unregistered"),
              ],
              nextBefore: null,
            },
          ),
    "GET /cleanup/orphans": json(
      opts.orphans ?? {
        torrents: [
          { hash: "def456", name: "Mystery.Torrent", size: 1024, progress: 0.5, state: "x" },
        ],
        files: ["leftover.mkv"],
        folder: "/mnt/downloads/reelkeeper",
        clientError: null,
      },
    ),
    "POST /cleanup/orphans/torrents/def456/remove": () => {
      removed.push("def456");
      return new Response(null, { status: 204 });
    },
    "POST /cleanup/orphans/files/remove": async (req) => {
      removed.push((await req.json()).name);
      return new Response(null, { status: 204 });
    },
  });
  return removed;
}

describe("cleanup activity page", () => {
  it("says it is a dry run, with a link to the settings", async () => {
    open("dryrun");
    expect(
      await screen.findByText(/Cleanup is in dry run: nothing below was actually removed\./),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Settings → Cleanup" })).toHaveAttribute(
      "href",
      "/settings/cleanup",
    );
  });

  it("says when cleanup is off, and shows no banner when on", async () => {
    open("off");
    expect(
      await screen.findByText(/Cleanup is off: Reelkeeper removes nothing/),
    ).toBeInTheDocument();
  });

  it("shows what a seeding torrent is waiting for, or that it is ready", async () => {
    open("on", { waiting: "a ratio of 1.0 (now 0.4)" });
    expect(await screen.findByText("Waiting for: a ratio of 1.0 (now 0.4)")).toBeInTheDocument();
    expect(screen.getByText("Ratio 0.40 · seeded 2 h")).toBeInTheDocument();
    expect(screen.queryByText(/dry run/)).not.toBeInTheDocument();
  });

  it("links a seeding TV download to the series page with its episodes", async () => {
    open("on", {
      seedingExtra: {
        movie: { id: 9, title: "The Expanse", year: 2015 },
        mediaType: "series",
        episodes: "S02E05",
      },
    });
    const link = await screen.findByRole("link", { name: "The Expanse (2015)" });
    expect(link).toHaveAttribute("href", "/series/9");
    expect(screen.getByText(/S02E05/)).toBeInTheDocument();
  });

  it("names the download client that is set up, not qBittorrent", async () => {
    open("off", {
      client: client("transmission"),
      seedingExtra: { torrent: null },
    });
    expect(
      await screen.findByText(
        "How Reelkeeper tidies Transmission: torrents that finished seeding, stuck downloads, and leftovers it doesn't recognise.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Cleanup is off: Reelkeeper removes nothing from Transmission\./),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/torrents are still in Transmission, and what cleanup/),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Transmission doesn't have this torrent any more."),
    ).toBeInTheDocument();
    expect(screen.getByText(/corner of Transmission that Reelkeeper/)).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/qBittorrent/);
  });

  it("names qBittorrent for a qBittorrent client, and stays neutral with none set up", async () => {
    open("dryrun", { seedingExtra: { torrent: null } });
    expect(
      await screen.findByText("qBittorrent doesn't have this torrent any more."),
    ).toBeInTheDocument();
  });

  it("says 'the download client' when none is set up", async () => {
    open("dryrun", {
      seedingExtra: { torrent: null },
      client: { ...client("qbittorrent"), url: "" },
    });
    expect(
      await screen.findByText("The download client doesn't have this torrent any more."),
    ).toBeInTheDocument();
    expect(screen.getByText(/How Reelkeeper tidies the download client:/)).toBeInTheDocument();
  });

  it("shows Ready to remove when nothing is left to wait for", async () => {
    open("on");
    expect(await screen.findByText("Ready to remove")).toBeInTheDocument();
  });

  it("lists log events with badges and plain rule names", async () => {
    open("dryrun");
    const log = (
      await screen.findByText("Everything cleanup removed, or would have, and why.")
    ).closest("div[data-slot], div") as HTMLElement;
    expect(log).toBeTruthy();
    expect(await screen.findByText("Would remove")).toBeInTheDocument();
    expect(screen.getByText("Removed")).toBeInTheDocument();
    expect(screen.getByText("Would empty")).toBeInTheDocument();
    expect(screen.getByText("Emptied")).toBeInTheDocument();
    expect(screen.getByText("No progress")).toBeInTheDocument();
    expect(screen.getByText("Done seeding")).toBeInTheDocument();
    expect(screen.getByText("Replaced files")).toBeInTheDocument();
    expect(screen.getByText("Deleted on tracker")).toBeInTheDocument();
    expect(screen.getByText("Because stalled")).toBeInTheDocument();
  });

  it("loads more log events", async () => {
    open("on", {
      events: { events: [event(5, "removed", "slow")], nextBefore: 5 },
      more: { events: [event(2, "removed", "metadata")], nextBefore: null },
    });
    await screen.findByText("Too slow");
    await userEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(await screen.findByText("No torrent details")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("removes an orphan torrent only after confirming", async () => {
    const removed = open("dryrun");
    await userEvent.click(await screen.findByRole("button", { name: "Remove Mystery.Torrent" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("Remove from qBittorrent and delete what it downloaded?"),
    ).toBeInTheDocument();
    expect(removed).toEqual([]);
    await userEvent.click(within(dialog).getByRole("button", { name: "Remove" }));
    await vi.waitFor(() => expect(removed).toEqual(["def456"]));
  });

  it("deletes a stray file only after confirming", async () => {
    const removed = open("dryrun");
    await userEvent.click(await screen.findByRole("button", { name: "Delete leftover.mkv" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Delete this file or folder for good?")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
    await vi.waitFor(() => expect(removed).toEqual(["leftover.mkv"]));
  });

  it("explains stray files when there is no download folder", async () => {
    open("dryrun", { orphans: { torrents: [], files: [], folder: null, clientError: null } });
    expect(
      await screen.findByText(/only reported when Reelkeeper has a download folder/),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Settings → Download Client" })).toBeInTheDocument();
  });

  it("shows why qBittorrent couldn't be asked", async () => {
    open("dryrun", {
      orphans: { torrents: [], files: [], folder: null, clientError: "Can't reach qBittorrent." },
    });
    expect(await screen.findByText(/Can't reach qBittorrent\./)).toBeInTheDocument();
  });

  it("asks before removing from the client in use", async () => {
    open("dryrun", { client: client("transmission"), orphans: undefined });
    await userEvent.click(await screen.findByRole("button", { name: "Remove Mystery.Torrent" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("Remove from Transmission and delete what it downloaded?"),
    ).toBeInTheDocument();
  });
});
