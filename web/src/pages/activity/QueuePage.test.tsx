import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { TorrentState } from "@/hooks/downloads";
import { resetAppearanceForTests } from "@/hooks/theme";
import { json, renderApp, signedIn } from "@/test/app";
import { barKind, isPaused, stateLabel } from "./QueuePage";

afterEach(() => vi.unstubAllGlobals());

const item = {
  id: 7,
  createdAt: "2026-10-09T10:00:00Z",
  movie: { id: 3, title: "Dune", year: 2021 },
  versionId: 11,
  profile: "HD",
  releaseTitle: "Dune.2021.1080p.WEB-DL-GRP",
  indexer: "Tracker A",
  quality: "WEBDL-1080p",
  score: 10,
  size: 5 * 1024 ** 3,
  upgrade: false,
  status: "downloading",
  message: "",
  torrent: {
    state: "downloading",
    progress: 0.42,
    size: 5 * 1024 ** 3,
    left: 3 * 1024 ** 3,
    speed: 2 * 1024 ** 2,
    etaSeconds: 600,
    seeds: 12,
    swarmSeeds: 40,
    peers: 3,
  },
};

function openQueue(
  queue: unknown,
  extra: Record<string, (r: Request) => Response | Promise<Response>> = {},
  queueSymbols?: boolean,
) {
  // Symbols are the default; a test can ask for words instead.
  resetAppearanceForTests();
  localStorage.clear();
  if (queueSymbols !== undefined)
    localStorage.setItem("reelkeeper.appearance", JSON.stringify({ queueSymbols }));
  return renderApp("/activity/queue", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /queue": () => json(queue),
    ...extra,
  });
}

const open = (
  queue: unknown,
  extra: Record<string, (r: Request) => Response | Promise<Response>> = {},
) => openQueue(queue, extra);

describe("queue page", () => {
  it("puts every torrent state in plain English, naming the download client", () => {
    const labels: Record<TorrentState, string> = {
      downloading: "Downloading",
      stalled: "Stalled (no peers sending)",
      metadata: "Getting torrent details",
      queued: "Queued in Transmission",
      paused: "Paused",
      checking: "Checking files",
      moving: "Moving files",
      seeding: "Downloaded, waiting to import",
      "seeding-stalled": "Downloaded, waiting to import",
      "seeding-paused": "Downloaded, paused",
      error: "Transmission reports an error",
      "missing-files": "Transmission reports an error",
      unknown: "Unknown",
    };
    for (const [state, label] of Object.entries(labels))
      expect(stateLabel(state as TorrentState, "Transmission")).toBe(label);
    // The built-in client's name reads in a sentence, at its start too.
    expect(stateLabel("queued", "the built-in client")).toBe("Queued in the built-in client");
    expect(stateLabel("error", "the built-in client")).toBe("The built-in client reports an error");
    // Names like qBittorrent keep their capitals; the stand-in starts a sentence with one.
    expect(stateLabel("error", "qBittorrent")).toBe("qBittorrent reports an error");
    expect(stateLabel("error", "the download client")).toBe("The download client reports an error");
    expect(stateLabel("queued", "the download client")).toBe("Queued in the download client");
  });

  it("words the page with the download client's name", async () => {
    const queued = { ...item, id: 1, torrent: { ...item.torrent, state: "queued" } };
    const lost = { ...item, id: 2, torrent: null };
    open({
      items: [queued, lost],
      clientError: "Can't reach Transmission: connection refused.",
      clientName: "Transmission",
    });
    expect(
      await screen.findByText(/What Reelkeeper has sent to Transmission and is waiting for/),
    ).toBeInTheDocument();
    expect(screen.getByText(/Couldn't ask Transmission how things are going/)).toBeInTheDocument();
    expect(screen.getByText("Queued in Transmission")).toBeInTheDocument();
    expect(
      screen.getByText(/Transmission didn't report this torrent just now/),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Move to top" })).toHaveAttribute(
      "title",
      "Move to top (only matters while Transmission queues downloads)",
    );
    await userEvent.click(screen.getAllByRole("button", { name: "Remove" })[0] as HTMLElement);
    expect(
      await screen.findByText("Remove from Transmission and delete what was downloaded?"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/qBittorrent/)).not.toBeInTheDocument();
  });

  it("warns when the queue is full, and links to the limit", async () => {
    const items = Array.from({ length: 3 }, (_, i) => ({ ...item, id: i + 1 }));
    open({ items, clientError: null, clientName: "qBittorrent", limit: 3, full: true });
    const alert = await screen.findByText(/The queue is full: 3 of 3 downloads\./);
    expect(alert).toHaveTextContent(
      "Reelkeeper won't grab anything new until some finish (Seerr requests still go through).",
    );
    expect(within(alert).getByRole("link", { name: "Settings → Download Client" })).toHaveAttribute(
      "href",
      "/settings/download-client",
    );
    // The sidebar says so too.
    expect(await screen.findByText("Full")).toBeInTheDocument();
  });

  it("words the page naturally for the built-in client", async () => {
    open({
      items: [item],
      clientError: null,
      clientName: "the built-in client",
      builtinState: "connected",
    });
    expect(
      await screen.findByText(/What Reelkeeper has sent to the built-in client and is waiting for/),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Remove" }));
    expect(
      await screen.findByText("Remove from the built-in client and delete what was downloaded?"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Built-in/)).not.toBeInTheDocument();
  });

  it("warns while the built-in client's VPN has stopped its downloads", async () => {
    const link = (alert: HTMLElement) =>
      within(alert).getByRole("link", { name: "Settings → Download Client" });
    for (const [state, text] of [
      ["down", "VPN down: the built-in client's downloads are stopped until it reconnects."],
      ["connecting", "VPN connecting: the built-in client's downloads start when it is connected."],
      ["held", "The built-in client's downloads are stopped until you start them without the VPN."],
    ] as const) {
      const view = open({
        items: [],
        clientError: null,
        clientName: "the built-in client",
        builtinState: state,
      });
      const alert = await screen.findByText(text, { exact: false });
      expect(alert).toHaveTextContent(text);
      expect(link(alert)).toHaveAttribute("href", "/settings/download-client");
      view.unmount();
      vi.unstubAllGlobals();
    }
    // Nothing to say while it is connected, unprotected or another client is in use.
    for (const builtinState of ["connected", "unprotected", null]) {
      const view = open({ items: [], clientError: null, clientName: "x", builtinState });
      await screen.findByText(/Nothing is downloading/);
      expect(screen.queryByText(/downloads are stopped/)).not.toBeInTheDocument();
      expect(screen.queryByText(/VPN (down|connecting)/)).not.toBeInTheDocument();
      view.unmount();
      vi.unstubAllGlobals();
    }
  });

  it("shows no warning while the queue has room", async () => {
    open({ items: [item], clientError: null, clientName: "qBittorrent", limit: 3, full: false });
    await screen.findByRole("link", { name: "Dune (2021)" });
    expect(screen.queryByText(/The queue is full/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Full$/)).not.toBeInTheDocument();
  });

  it("shows progress, speed, time left and peers", async () => {
    open({ items: [item], clientError: null, clientName: "qBittorrent" });
    expect(await screen.findByRole("link", { name: "Dune (2021)" })).toBeInTheDocument();
    expect(screen.getByText("Dune.2021.1080p.WEB-DL-GRP")).toBeInTheDocument();
    expect(screen.getByText("WEBDL-1080p · Tracker A")).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "42");
    expect(screen.getByText("42%")).toBeInTheDocument();
    expect(screen.getByText("Downloading")).toBeInTheDocument();
    // (The sidebar shows the total speed too.)
    expect(within(screen.getByRole("main")).getByText("2.0 MB/s")).toBeInTheDocument();
    expect(screen.getByText("10m 00s left")).toBeInTheDocument();
    expect(screen.getByText(/connected to 12 of 40 seeds\s*· 3 peers/)).toBeInTheDocument();
  });

  it("doesn't show a tracker count that's lower than the connected seeds", async () => {
    // Trackers' counts are often stale and miss seeds found through DHT.
    open({
      items: [{ ...item, torrent: { ...item.torrent, seeds: 35, swarmSeeds: 21 } }],
      clientError: null,
      clientName: "qBittorrent",
    });
    expect(await screen.findByText(/connected to 35 seeds\s*· 3 peers/)).toBeInTheDocument();
    expect(screen.queryByText(/of 21/)).not.toBeInTheDocument();
  });

  it("explains an empty queue, and warns when qBittorrent can't be asked", async () => {
    open({
      items: [],
      clientError: "Can't reach qBittorrent: connection refused.",
      clientName: "qBittorrent",
    });
    expect(await screen.findByText(/Nothing is downloading\./)).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "History" })).toHaveLength(2); // nav and this page
    expect(screen.getByText(/Can't reach qBittorrent: connection refused\./)).toBeInTheDocument();
  });

  it("shows why a finished download is waiting", async () => {
    open({
      items: [
        { ...item, message: "Finished, but Reelkeeper can't see it at /mnt/downloads/x yet." },
      ],
      clientError: null,
      clientName: "qBittorrent",
    });
    expect(await screen.findByText(/can't see it at \/mnt\/downloads\/x yet/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Retry import" })).not.toBeInTheDocument();
  });

  it("retries a failed import", async () => {
    let retried = 0;
    let queue = {
      items: [{ ...item, status: "import-failed", message: "Import failed: disk full" }],
      clientError: null,
      clientName: "qBittorrent",
    };
    open(queue, {
      "POST /queue/7/retry-import": () => {
        retried++;
        queue = { items: [], clientError: null, clientName: "qBittorrent" };
        return new Response(null, { status: 204 });
      },
      "GET /queue": () => json(queue),
    });
    expect(await screen.findByText("Import failed: disk full")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry import" }));
    await waitFor(() => expect(retried).toBe(1));
    expect(await screen.findByText(/Nothing is downloading\./)).toBeInTheDocument();
  });

  it("shows why a retry failed", async () => {
    open(
      {
        items: [{ ...item, status: "import-failed", message: "Import failed: disk full" }],
        clientError: null,
        clientName: "qBittorrent",
      },
      {
        "POST /queue/7/retry-import": () =>
          json({ code: "import_failed", message: "Import failed: still no space" }, 422),
      },
    );
    await userEvent.click(await screen.findByRole("button", { name: "Retry import" }));
    expect(await screen.findByText("Import failed: still no space")).toBeInTheDocument();
  });

  it("removes with the chosen options", async () => {
    const posts: unknown[] = [];
    open(
      { items: [item], clientError: null, clientName: "qBittorrent" },
      {
        "POST /queue/7/remove": async (req) => {
          posts.push(await req.json());
          return new Response(null, { status: 204 });
        },
      },
    );
    await userEvent.click(await screen.findByRole("button", { name: "Remove" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("Remove from qBittorrent and delete what was downloaded?"),
    ).toBeInTheDocument();
    const blocklist = within(dialog).getByLabelText(/Blocklist this release/);
    const search = within(dialog).getByLabelText("Search for another release");
    expect(blocklist).toBeChecked();
    expect(search).toBeChecked();
    // Searching again needs the blocklist, or it would grab the same release.
    expect(blocklist).toBeDisabled();
    expect(
      within(dialog).getByText(/Needed so the new search picks a different release/),
    ).toBeInTheDocument();
    // Where to unblock it later.
    expect(within(dialog).getByText(/System → Blocklist/)).toBeInTheDocument();
    await userEvent.click(search);
    expect(blocklist).toBeEnabled();
    await userEvent.click(within(dialog).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toEqual({ blocklist: true, searchAgain: false });
  });

  it("links a TV download to the series page and says which episodes", async () => {
    open({
      items: [
        {
          ...item,
          movie: { id: 9, title: "The Expanse", year: 2015 },
          mediaType: "series",
          episodes: "S02E05",
        },
      ],
      clientError: null,
      clientName: "qBittorrent",
    });
    const link = await screen.findByRole("link", { name: "The Expanse (2015)" });
    expect(link).toHaveAttribute("href", "/series/9");
    expect(screen.getByText(/S02E05/)).toBeInTheDocument();
  });

  it("tells paused torrents from running ones", () => {
    expect(isPaused("paused")).toBe(true);
    expect(isPaused("downloading")).toBe(false);
    expect(isPaused("queued")).toBe(false);
    // A finished torrent that was stopped is seeding territory, not a paused download.
    expect(isPaused("seeding-paused")).toBe(false);
  });
});

// What the Queue page showed when it read qBittorrent's own state names,
// kept as a yardstick: the page now reads Reelkeeper's states, and for every
// state qBittorrent can report the result must be the same.
describe("torrent states keep the old look", () => {
  const oldIsPaused = (state: string) => state === "pausedDL" || state === "stoppedDL";
  function oldStateLabel(state: string): string {
    if (state === "downloading" || state === "forcedDL") return "Downloading";
    if (state === "stalledDL") return "Stalled (no peers sending)";
    if (state === "metaDL" || state === "forcedMetaDL") return "Getting torrent details";
    if (state === "queuedDL") return "Queued in qBittorrent";
    if (oldIsPaused(state)) return "Paused";
    if (["uploading", "stalledUP", "queuedUP", "forcedUP"].includes(state))
      return "Downloaded, waiting to import";
    if (state === "error" || state === "missingFiles") return "qBittorrent reports an error";
    if (state.startsWith("checking") || state === "allocating") return "Checking files";
    return state;
  }
  function oldBarKind(state: string, progress: number) {
    if (state === "metaDL" || state === "forcedMetaDL") return "fetching";
    if (state === "stalledDL" || state === "error" || state === "missingFiles") return "stalled";
    if (progress >= 1 || ["uploading", "stalledUP", "queuedUP", "forcedUP"].includes(state))
      return "done";
    return "active";
  }

  // Every state name qBittorrent 4.x and 5.x reports, with the state the Go
  // client turns it into (qbitState; its own test pins that table).
  const qbitStates: [string, TorrentState][] = [
    ["error", "error"],
    ["missingFiles", "missing-files"],
    ["uploading", "seeding"],
    ["forcedUP", "seeding"],
    ["queuedUP", "seeding"],
    ["stalledUP", "seeding-stalled"],
    ["pausedUP", "seeding-paused"],
    ["stoppedUP", "seeding-paused"],
    ["checkingUP", "checking"],
    ["allocating", "checking"],
    ["downloading", "downloading"],
    ["forcedDL", "downloading"],
    ["stalledDL", "stalled"],
    ["metaDL", "metadata"],
    ["forcedMetaDL", "metadata"],
    ["queuedDL", "queued"],
    ["pausedDL", "paused"],
    ["stoppedDL", "paused"],
    ["checkingDL", "checking"],
    ["checkingResumeData", "checking"],
    ["moving", "moving"],
    ["unknown", "unknown"],
  ];

  // The only labels that changed: the old page printed the raw qBittorrent
  // name for these, which is no use to read.
  const relabelled: Record<string, string> = {
    pausedUP: "Downloaded, paused",
    stoppedUP: "Downloaded, paused",
    moving: "Moving files",
    unknown: "Unknown",
  };

  it.each(qbitStates)("%s", (name, state) => {
    expect(isPaused(state)).toBe(oldIsPaused(name));
    for (const progress of [0, 0.5, 1])
      expect(barKind(state, progress)).toBe(oldBarKind(name, progress));
    if (name in relabelled) {
      expect(oldStateLabel(name)).toBe(name); // the raw name, as before
      expect(stateLabel(state, "qBittorrent")).toBe(relabelled[name]);
    } else {
      expect(stateLabel(state, "qBittorrent")).toBe(oldStateLabel(name));
    }
  });
});

// The same behaviour, whether the buttons show symbols or words.
describe.each([
  { mode: "symbols", queueSymbols: true },
  { mode: "words", queueSymbols: false },
])("queue row controls with $mode", ({ queueSymbols }) => {
  const paused = { ...item, torrent: { ...item.torrent, state: "paused" } };
  const open = (
    queue: unknown,
    extra: Record<string, (r: Request) => Response | Promise<Response>> = {},
  ) => openQueue(queue, extra, queueSymbols);
  // A symbol button is named by its aria-label; a word button by its text.
  const buttons = () =>
    within(screen.getByRole("main"))
      .getAllByRole("button")
      .map((b) => b.getAttribute("aria-label") ?? b.textContent);

  it("offers Pause and Move to top while downloading, in front of Remove", async () => {
    open({ items: [item], clientError: null, clientName: "qBittorrent" });
    await screen.findByRole("link", { name: "Dune (2021)" });
    expect(buttons()).toEqual(["Pause", "Move to top", "Remove"]);
    expect(screen.getByRole("button", { name: "Move to top" })).toHaveAttribute(
      "title",
      queueSymbols
        ? "Move to top (only matters while qBittorrent queues downloads)"
        : "Only matters while qBittorrent queues downloads",
    );
  });

  it("offers Resume, and no Move to top, while paused", async () => {
    open({ items: [paused], clientError: null, clientName: "qBittorrent" });
    expect(await screen.findByText("Paused")).toBeInTheDocument();
    expect(buttons()).toEqual(["Resume", "Remove"]);
  });

  it("offers Pause and Move to top for a queued or stalled download", async () => {
    open({
      items: [
        { ...item, id: 1, torrent: { ...item.torrent, state: "queued" } },
        { ...item, id: 2, torrent: { ...item.torrent, state: "stalled" } },
      ],
      clientError: null,
      clientName: "qBittorrent",
    });
    await screen.findAllByRole("link", { name: "Dune (2021)" });
    expect(buttons()).toEqual(["Pause", "Move to top", "Remove", "Pause", "Move to top", "Remove"]);
  });

  it("offers neither once the torrent has finished downloading", async () => {
    open({
      items: [{ ...item, torrent: { ...item.torrent, state: "seeding", progress: 1, left: 0 } }],
      clientError: null,
      clientName: "qBittorrent",
    });
    await screen.findByText("Downloaded, waiting to import");
    expect(buttons()).toEqual(["Remove"]);
  });

  it("says 'Moving to Transmission' on a row that is on its way, and disables its buttons", async () => {
    open({
      items: [
        { ...item, movingTo: "Transmission" },
        { ...item, id: 8, movingTo: null },
      ],
      clientError: null,
      clientName: "qBittorrent",
    });
    expect(await screen.findByText("Moving to Transmission")).toBeInTheDocument();
    // One badge only, and not the torrent's own state beside it.
    expect(screen.getAllByText("Moving to Transmission")).toHaveLength(1);
    expect(screen.getAllByText("Downloading")).toHaveLength(1);
    // Pause and Move to top aren't offered for it; Remove is there but off.
    expect(buttons()).toEqual(["Remove", "Pause", "Move to top", "Remove"]);
    expect(screen.getAllByRole("button", { name: "Remove" })[0]).toBeDisabled();
    expect(screen.getAllByRole("button", { name: "Remove" })[1]).toBeEnabled();
  });

  it("offers neither when qBittorrent doesn't report the torrent", async () => {
    open({ items: [{ ...item, torrent: null }], clientError: null, clientName: "qBittorrent" });
    await screen.findByText(/qBittorrent didn't report this torrent just now/);
    expect(buttons()).toEqual(["Remove"]);
  });

  it("offers neither for a failed import", async () => {
    open({
      items: [
        {
          ...item,
          status: "import-failed",
          message: "Import failed: disk full",
          torrent: { ...item.torrent, state: "seeding-paused", progress: 1, left: 0 },
        },
      ],
      clientError: null,
      clientName: "qBittorrent",
    });
    await screen.findByText("Import failed: disk full");
    expect(buttons()).toEqual(["Retry import", "Remove"]);
  });

  it("pauses, then shows Resume once the queue says it is paused", async () => {
    let queue = { items: [item], clientError: null, clientName: "qBittorrent" };
    let posts = 0;
    open(queue, {
      "GET /queue": () => json(queue),
      "POST /queue/7/pause": () => {
        posts++;
        queue = { items: [paused], clientError: null, clientName: "qBittorrent" };
        return new Response(null, { status: 204 });
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Pause" }));
    await waitFor(() => expect(posts).toBe(1));
    expect(await screen.findByRole("button", { name: "Resume" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Move to top" })).not.toBeInTheDocument();
  });

  it("resumes", async () => {
    let queue = { items: [paused], clientError: null, clientName: "qBittorrent" };
    let posts = 0;
    open(queue, {
      "GET /queue": () => json(queue),
      "POST /queue/7/resume": () => {
        posts++;
        queue = { items: [item], clientError: null, clientName: "qBittorrent" };
        return new Response(null, { status: 204 });
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Resume" }));
    await waitFor(() => expect(posts).toBe(1));
    expect(await screen.findByRole("button", { name: "Pause" })).toBeInTheDocument();
  });

  it("moves to the top", async () => {
    let posts = 0;
    open(
      { items: [item], clientError: null, clientName: "qBittorrent" },
      {
        "POST /queue/7/top": () => {
          posts++;
          return new Response(null, { status: 204 });
        },
      },
    );
    await userEvent.click(await screen.findByRole("button", { name: "Move to top" }));
    await waitFor(() => expect(posts).toBe(1));
    // Still downloading, so the same buttons remain.
    expect(await screen.findByRole("button", { name: "Move to top" })).toBeEnabled();
  });

  it("shows a pending state and disables the row's other buttons meanwhile", async () => {
    let release: (r: Response) => void = () => {};
    open(
      { items: [item], clientError: null, clientName: "qBittorrent" },
      {
        "POST /queue/7/pause": () => new Promise<Response>((resolve) => (release = resolve)),
      },
    );
    await userEvent.click(await screen.findByRole("button", { name: "Pause" }));
    expect(await screen.findByRole("button", { name: "Pausing…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Move to top" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Remove" })).toBeDisabled();
    release(new Response(null, { status: 204 }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Remove" })).toBeEnabled());
    expect(screen.getByRole("button", { name: "Pause" })).toBeEnabled();
  });

  it("only disables the buttons of the row being acted on", async () => {
    let release: (r: Response) => void = () => {};
    open(
      {
        items: [item, { ...item, id: 8, releaseTitle: "Other.Movie.2020" }],
        clientError: null,
        clientName: "qBittorrent",
      },
      {
        "POST /queue/7/pause": () => new Promise<Response>((resolve) => (release = resolve)),
      },
    );
    const pauses = await screen.findAllByRole("button", { name: "Pause" });
    await userEvent.click(pauses[0] as HTMLElement);
    await screen.findByRole("button", { name: "Pausing…" });
    expect(screen.getByRole("button", { name: "Pause" })).toBeEnabled(); // the other row's
    release(new Response(null, { status: 204 }));
  });

  it("shows an error on the row, and clears it when another action starts", async () => {
    let fail = true;
    open(
      { items: [item], clientError: null, clientName: "qBittorrent" },
      {
        "POST /queue/7/pause": () =>
          json({ code: "pause_failed", message: "QBittorrent no longer has this download." }, 422),
        "POST /queue/7/top": () => {
          fail = false;
          return new Response(null, { status: 204 });
        },
      },
    );
    await userEvent.click(await screen.findByRole("button", { name: "Pause" }));
    expect(await screen.findByText("QBittorrent no longer has this download.")).toBeInTheDocument();
    // The buttons work again.
    expect(screen.getByRole("button", { name: "Pause" })).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "Move to top" }));
    await waitFor(() => expect(fail).toBe(false));
    await waitFor(() =>
      expect(
        screen.queryByText("QBittorrent no longer has this download."),
      ).not.toBeInTheDocument(),
    );
  });
});

describe("queue row buttons as symbols or words", () => {
  const main = () => within(screen.getByRole("main"));

  it("shows symbols by default: named and titled, with no words on them", async () => {
    open({ items: [item], clientError: null, clientName: "qBittorrent" });
    await screen.findByRole("link", { name: "Dune (2021)" });
    for (const name of ["Pause", "Move to top", "Remove"]) {
      const b = screen.getByRole("button", { name });
      expect(b).toHaveAttribute("aria-label", name);
      expect(b).toHaveAttribute("title", expect.stringContaining(name));
      expect(b.textContent).toBe("");
      expect(b.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
    }
    expect(main().queryByText("Pause")).not.toBeInTheDocument();
    expect(main().queryByText("Remove")).not.toBeInTheDocument();
  });

  it("shows a symbol for Resume too", async () => {
    open({
      items: [{ ...item, torrent: { ...item.torrent, state: "paused" } }],
      clientError: null,
      clientName: "qBittorrent",
    });
    const b = await screen.findByRole("button", { name: "Resume" });
    expect(b).toHaveAttribute("title", "Resume");
    expect(b.textContent).toBe("");
    expect(b.querySelector("svg")).toBeInTheDocument();
  });

  it("shows words when symbols are turned off", async () => {
    openQueue({ items: [item], clientError: null, clientName: "qBittorrent" }, {}, false);
    await screen.findByRole("link", { name: "Dune (2021)" });
    expect(main().getByText("Pause")).toBeInTheDocument();
    expect(main().getByText("Move to top")).toBeInTheDocument();
    expect(main().getByText("Remove")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Pause" })).not.toHaveAttribute("aria-label");
    expect(screen.getByRole("button", { name: "Pause" }).querySelector("svg")).toBeNull();
  });

  it("names a working symbol button by what it is doing, and swaps the icon for a spinner", async () => {
    let release: (r: Response) => void = () => {};
    open(
      { items: [item], clientError: null, clientName: "qBittorrent" },
      {
        "POST /queue/7/top": () => new Promise<Response>((resolve) => (release = resolve)),
      },
    );
    await userEvent.click(await screen.findByRole("button", { name: "Move to top" }));
    const moving = await screen.findByRole("button", { name: "Moving…" });
    expect(moving).toBeDisabled();
    expect(moving.querySelector("svg")).toBeNull();
    expect(moving.querySelector(".slib-spin")).toBeInTheDocument();
    release(new Response(null, { status: 204 }));
    expect(await screen.findByRole("button", { name: "Move to top" })).toBeEnabled();
  });

  it("shows Retry import as a symbol too, and words when symbols are off", async () => {
    const failed = {
      items: [{ ...item, status: "import-failed", message: "Import failed: disk full" }],
      clientError: null,
      clientName: "qBittorrent",
    };
    open(failed);
    const retry = await screen.findByRole("button", { name: "Retry import" });
    expect(retry).toHaveAttribute("aria-label", "Retry import");
    expect(retry).toHaveAttribute("title", "Retry import");
    expect(retry.textContent).toBe("");
    expect(retry.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
    cleanup();
    openQueue(failed, {}, false);
    expect(await screen.findByRole("button", { name: "Retry import" })).toHaveTextContent(
      "Retry import",
    );
  });

  it("names a working Retry import symbol 'Retrying…' with a spinner", async () => {
    let release: (r: Response) => void = () => {};
    open(
      {
        items: [{ ...item, status: "import-failed", message: "Import failed: disk full" }],
        clientError: null,
        clientName: "qBittorrent",
      },
      {
        "POST /queue/7/retry-import": () => new Promise<Response>((resolve) => (release = resolve)),
      },
    );
    await userEvent.click(await screen.findByRole("button", { name: "Retry import" }));
    const retrying = await screen.findByRole("button", { name: "Retrying…" });
    expect(retrying).toBeDisabled();
    expect(retrying.querySelector(".slib-spin")).toBeInTheDocument();
    release(new Response(null, { status: 204 }));
  });
});
