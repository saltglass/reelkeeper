import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";
import { seriesDetail, seriesVersion } from "@/test/series";

afterEach(() => vi.unstubAllGlobals());

const orders = [
  {
    id: "",
    name: "TMDB aired order",
    type: "",
    description: "The order the episodes aired in.",
    episodes: 5,
    fits: 2,
    problems: 2,
    current: true,
  },
  {
    id: "dvd1",
    name: "DVD order",
    type: "DVD",
    description: "As on the box set.",
    episodes: 5,
    fits: 4,
    problems: 0,
    current: false,
  },
];

function open(
  over: Parameters<typeof seriesDetail>[0] = {},
  extra: Parameters<typeof renderApp>[1] = {},
) {
  const puts: unknown[] = [];
  const patches: unknown[] = [];
  const view = renderApp("/series/7", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /settings/labels": json({ parts: ["profile", "quality", "hdr"] }),
    "GET /series/7": json(seriesDetail(over)),
    "GET /series/7/orders": json(orders),
    "PUT /series/7/order": async (req) => {
      puts.push(await req.json());
      return json(seriesDetail({ episodeOrder: "dvd1", episodeOrderName: "DVD order" }));
    },
    "PATCH /series/7": async (req) => {
      patches.push(await req.json());
      return json(seriesDetail({ seriesType: "daily" }));
    },
    ...extra,
  });
  return { ...view, puts, patches };
}

describe("series page", () => {
  it("shows the header and the version card with its episode count", async () => {
    open();
    expect(await screen.findByRole("heading", { name: "Show" })).toBeInTheDocument();
    expect(screen.getByText("2010 · BBC · Ended · TV-14 · 45m episodes")).toBeInTheDocument();
    expect(screen.getByText("3 of 5 episodes")).toBeInTheDocument();
    expect(screen.getByText("Monitored")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Rename files/ })).toBeInTheDocument();
  });

  it("lists seasons with Specials last and the newest season open", async () => {
    open();
    await screen.findByRole("heading", { name: "Show" });
    // Season 2 is the most recent: open. Season 1 is closed.
    expect(screen.getByRole("button", { name: /^Season 2/ })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    expect(screen.getByRole("button", { name: /^Season 1/ })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    const seasonButtons = screen
      .getAllByRole("button")
      .filter((b) => /^(Season \d|Specials)/.test(b.textContent ?? ""))
      .map((b) => b.querySelector("span.font-medium")?.textContent);
    expect(seasonButtons).toEqual(["Season 1", "Season 2", "Specials"]);
  });

  it("shows each episode with its code, air date, files and gaps", async () => {
    open();
    await userEvent.click(await screen.findByRole("button", { name: /^Season 1/ }));
    expect(screen.getByText("S01E01")).toBeInTheDocument();
    expect(screen.getByText("Pilot")).toBeInTheDocument();
    expect(
      screen.getByText(new Date("2010-01-01T00:00:00").toLocaleDateString()),
    ).toBeInTheDocument();
    // E3 has no file and no air date yet.
    const third = screen.getByText("Third").closest("li") as HTMLElement;
    expect(within(third).getByText("Not aired yet")).toBeInTheDocument();
    expect(within(third).getByText("No file")).toBeInTheDocument();
    // E1 shows the file's quality label and size.
    const pilot = screen.getByText("Pilot").closest("li") as HTMLElement;
    expect(within(pilot).getByText("WEBDL-1080p")).toBeInTheDocument();
    expect(within(pilot).getByText("2.0 GB")).toBeInTheDocument();
    // A file holding two episodes says so on each.
    const ret = screen.getByText("Return").closest("li") as HTMLElement;
    expect(within(ret).getByText(/also holds S02E02/)).toBeInTheDocument();
  });

  it("flags files Reelkeeper isn't sure about, in the episode and in Needs attention", async () => {
    open();
    await userEvent.click(await screen.findByRole("button", { name: /^Season 1/ }));
    const note = "The file says S01E02 but TMDB's episode 2 is a different length.";
    // Once in the Needs attention section, once on the episode.
    expect(screen.getAllByText(note)).toHaveLength(2);
    const second = screen.getByText("Second").closest("li") as HTMLElement;
    expect(within(second).getByLabelText("Needs attention")).toBeInTheDocument();
    const section = screen.getByRole("heading", { name: "Needs attention" }).closest("div");
    expect(section).not.toBeNull();
  });

  it("marks missing files", async () => {
    const base = seriesDetail();
    (base.versions[0]?.mediaFiles[0] as { missingSince?: string }).missingSince =
      "2026-10-01T00:00:00Z";
    open({ versions: base.versions });
    await userEvent.click(await screen.findByRole("button", { name: /^Season 1/ }));
    const pilot = screen.getByText("Pilot").closest("li") as HTMLElement;
    expect(within(pilot).getByText("Missing")).toBeInTheDocument();
  });

  it("lists episodes the order leaves out, collapsed", async () => {
    open();
    const toggle = await screen.findByRole("button", { name: /^Not in this episode order/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Deleted Scene")).not.toBeInTheDocument();
    await userEvent.click(toggle);
    expect(screen.getByText("Deleted Scene")).toBeInTheDocument();
    // They have no number in the chosen order, so their aired one is shown.
    expect(screen.getByText("S01E09")).toBeInTheDocument();
  });

  it("lists files that no episode holds", async () => {
    open();
    expect(await screen.findByText("Files Reelkeeper couldn't place")).toBeInTheDocument();
    expect(screen.getByText("random.mkv")).toBeInTheDocument();
    expect(screen.getByText("Reelkeeper couldn't tell which episode this is.")).toBeInTheDocument();
  });

  it("shows the current episode order", async () => {
    open({ episodeOrder: "dvd1", episodeOrderName: "DVD order" });
    expect(await screen.findByText("DVD order")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Change order" })).toBeInTheDocument();
  });

  it("changes the series type under Advanced", async () => {
    const { patches } = open();
    await userEvent.click(await screen.findByRole("button", { name: "Show advanced" }));
    expect(screen.getByText(/named by air date/)).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Series type"), "daily");
    await waitFor(() => expect(patches).toEqual([{ seriesType: "daily" }]));
  });

  it("removes a version with no files", async () => {
    const calls: string[] = [];
    open(
      {
        versions: [
          { ...seriesVersion({ files: 0, size: 0, episodesWithFiles: 0 }), mediaFiles: [] },
        ],
        problems: 0,
      },
      {
        "DELETE /series/7/versions/1": () => {
          calls.push("delete");
          return new Response(null, { status: 204 });
        },
      } as never,
    );
    await userEvent.click(await screen.findByRole("button", { name: /Remove this version/ }));
    await waitFor(() => expect(calls).toEqual(["delete"]));
  });
});

describe("change episode order", () => {
  it("shows a loading state, then each order with how well it fits", async () => {
    // TMDB answers slowly: the dialog says so until it does.
    let answer: () => void = () => {};
    const slow = new Promise<void>((r) => {
      answer = r;
    });
    open({}, { "GET /series/7/orders": () => slow.then(() => json(orders)) } as never);
    await userEvent.click(await screen.findByRole("button", { name: "Change order" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/Asking TMDB/)).toBeInTheDocument();
    answer();
    expect(await within(dialog).findByText("DVD order")).toBeInTheDocument();
    expect(within(dialog).getByText("TMDB aired order")).toBeInTheDocument();
    expect(within(dialog).getByText(/fits 2 of 4 files/)).toBeInTheDocument();
    expect(within(dialog).getByText(/fits 4 of 4 files/)).toBeInTheDocument();
    expect(within(dialog).getByText("Current")).toBeInTheDocument();
    expect(within(dialog).getByText("Best fit")).toBeInTheDocument();
    expect(within(dialog).getByText("As on the box set.")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Use this order" })).toBeDisabled();
  });

  it("explains the switch and calls PUT with the chosen order", async () => {
    const { puts } = open();
    await userEvent.click(await screen.findByRole("button", { name: "Change order" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(await within(dialog).findByRole("button", { name: /DVD order/ }));
    expect(within(dialog).getByText(/No files are renamed or moved/)).toBeInTheDocument();
    expect(within(dialog).getByText(/Every file fits/)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Use this order" }));
    await waitFor(() => expect(puts).toEqual([{ episodeOrder: "dvd1" }]));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("says so when TMDB has no other order to switch to", async () => {
    open({}, {
      "GET /series/7/orders": json([
        {
          id: "",
          name: "Aired order (TMDB's default)",
          type: "",
          description: "",
          episodes: 40,
          fits: 36,
          problems: 0,
          current: true,
        },
      ]),
    } as never);
    await userEvent.click(await screen.findByRole("button", { name: "Change order" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      await within(dialog).findByText(/TMDB has no other episode orders for this series/),
    ).toBeInTheDocument();
    expect(within(dialog).getByText(/All 36 files fit TMDB's aired order/)).toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", { name: "Use this order" }),
    ).not.toBeInTheDocument();
    // The footer's Close (the corner ✕ is also called Close).
    const closes = within(dialog).getAllByRole("button", { name: "Close" });
    await userEvent.click(closes[closes.length - 1] as HTMLElement);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("says when TMDB can't be reached", async () => {
    open({}, {
      "GET /series/7/orders": json({ code: "bad_gateway", message: "TMDB is unreachable." }, 502),
    } as never);
    await userEvent.click(await screen.findByRole("button", { name: "Change order" }));
    expect(await screen.findByText(/TMDB is unreachable/)).toBeInTheDocument();
  });
});

describe("rename series files", () => {
  const plan = (done = false) => [
    {
      versionId: 1,
      profileName: "HD",
      rootPath: "/media/shows",
      managed: true,
      files: [
        {
          fileId: 101,
          from: "Show/Show S01E01.mkv",
          to: "Show/Season 01/Show - S01E01.mkv",
          extras: [],
          problem: "",
          done,
        },
        {
          fileId: 102,
          from: "Show/Show S01E02.mkv",
          to: "",
          extras: [],
          problem: "Reelkeeper isn't sure which episode this is, so it won't rename it.",
          done: false,
        },
      ],
    },
  ];

  it("previews, shows per-file problems and renames only the sure ones", async () => {
    const posts: string[] = [];
    open({}, {
      "GET /series/7/rename": json(plan()),
      "POST /series/7/rename": () => {
        posts.push("rename");
        return json(plan(true));
      },
    } as never);
    await userEvent.click(await screen.findByRole("button", { name: /Rename files/ }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText(/won't rename it/)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Rename 1 file" }));
    expect(await within(dialog).findByText("Renamed")).toBeInTheDocument();
    expect(posts).toEqual(["rename"]);
  });
});

describe("TV monitoring and searching", () => {
  const versionWith = (over: Record<string, unknown> = {}, id = 1, name = "HD") => {
    const v = seriesDetail().versions[0] as Record<string, unknown>;
    return { ...v, id, profileName: name, ...over };
  };

  function openMonitoring(over: Parameters<typeof seriesDetail>[0] = {}) {
    const posts: { url: string; body: unknown }[] = [];
    const record = (name: string) => async (req: Request) => {
      const body = await req.json().catch(() => null);
      posts.push({ url: `${req.method} ${name}`, body });
      return name.endsWith("/search")
        ? json({ id: 1, kind: "search", status: "queued" })
        : new Response(null, { status: 204 });
    };
    open(over, {
      "PATCH /series/7/versions/1": record("/series/7/versions/1"),
      "POST /series/7/versions/1/monitor": record("/series/7/versions/1/monitor"),
      "POST /series/7/versions/2/monitor": record("/series/7/versions/2/monitor"),
      "POST /series/7/versions/1/search": record("/series/7/versions/1/search"),
    } as never);
    return posts;
  }

  it("summarises wanted, downloading and upcoming episodes, leaving out zeros", async () => {
    const episodes = [
      { episodeId: 1, monitored: true, state: "wanted" },
      { episodeId: 2, monitored: true, state: "wanted" },
      { episodeId: 3, monitored: true, state: "wanted" },
      { episodeId: 4, monitored: true, state: "downloading" },
      { episodeId: 5, monitored: true, state: "not-aired" },
      { episodeId: 6, monitored: true, state: "not-aired" },
    ];
    openMonitoring({ versions: [versionWith({ episodes })] as never });
    expect(await screen.findByText("3 wanted · 1 downloading · 2 upcoming")).toBeInTheDocument();
  });

  it("omits the summary parts that are zero", async () => {
    openMonitoring();
    // The default fixture has one upcoming episode and nothing else.
    expect(await screen.findByText("1 upcoming")).toBeInTheDocument();
  });

  it("explains the current monitor choice and sends the preset", async () => {
    const posts = openMonitoring();
    expect(
      await screen.findByText("Every episode, aired or not. Specials only when you turn them on."),
    ).toBeInTheDocument();
    expect(screen.getByText(/replaces any choices you made/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "New episodes" }));
    await waitFor(() =>
      expect(posts).toContainEqual({
        url: "PATCH /series/7/versions/1",
        body: { monitor: "future" },
      }),
    );
  });

  it("dates the New episodes choice", async () => {
    openMonitoring({
      versions: [versionWith({ monitor: "future", monitorFrom: "2026-10-11" })] as never,
    });
    expect(
      await screen.findByText(
        `Episodes airing from ${new Date("2026-10-11T00:00:00").toLocaleDateString()}.`,
      ),
    ).toBeInTheDocument();
  });

  it("starts a search from the version card", async () => {
    const posts = openMonitoring();
    await userEvent.click(await screen.findByRole("button", { name: /Search now/ }));
    await waitFor(() =>
      expect(posts.map((p) => p.url)).toContain("POST /series/7/versions/1/search"),
    );
    expect(await screen.findByText(/Searching… results will appear/)).toBeInTheDocument();
  });

  it("badges each episode's state", async () => {
    openMonitoring();
    await userEvent.click(await screen.findByRole("button", { name: /^Season 1/ }));
    const third = screen.getByText("Third").closest("li") as HTMLElement;
    expect(within(third).getByText("Upcoming")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /^Specials/ }));
    const special = screen.getByText("Christmas Special").closest("li") as HTMLElement;
    expect(within(special).getByText("Not monitored")).toBeInTheDocument();
  });

  it("toggles a single episode", async () => {
    const posts = openMonitoring();
    await userEvent.click(await screen.findByRole("button", { name: /^Season 1/ }));
    await userEvent.click(screen.getByRole("button", { name: "Stop monitoring S01E03" }));
    await waitFor(() =>
      expect(posts).toContainEqual({
        url: "POST /series/7/versions/1/monitor",
        body: { episodeIds: [3], monitored: false },
      }),
    );
  });

  it("toggles a whole season: off when all are on, on when some are off", async () => {
    const posts = openMonitoring();
    await screen.findByRole("heading", { name: "Episodes" });
    await userEvent.click(screen.getByRole("button", { name: "Stop monitoring season 1" }));
    await waitFor(() =>
      expect(posts).toContainEqual({
        url: "POST /series/7/versions/1/monitor",
        body: { seasons: [1], monitored: false },
      }),
    );
    // Specials aren't monitored, so the toggle turns them on.
    await userEvent.click(screen.getByRole("button", { name: "Monitor Specials" }));
    await waitFor(() =>
      expect(posts).toContainEqual({
        url: "POST /series/7/versions/1/monitor",
        body: { seasons: [0], monitored: true },
      }),
    );
  });

  it("has a version picker only with several versions, and acts on the picked one", async () => {
    const posts = openMonitoring({
      versions: [versionWith(), versionWith({}, 2, "Remux")] as never,
    });
    await screen.findByRole("heading", { name: "Episodes" });
    await userEvent.click(screen.getByRole("button", { name: "Remux" }));
    await userEvent.click(screen.getByRole("button", { name: "Stop monitoring season 1" }));
    await waitFor(() =>
      expect(posts).toContainEqual({
        url: "POST /series/7/versions/2/monitor",
        body: { seasons: [1], monitored: false },
      }),
    );
  });

  it("shows no picker with one version", async () => {
    openMonitoring();
    await screen.findByRole("heading", { name: "Episodes" });
    expect(screen.queryByRole("group", { name: "Show episodes for" })).not.toBeInTheDocument();
  });
});

describe("Add to backfill", () => {
  const emptyQueue = { next: [], decision: [], grabsToday: 0, grabsPerDay: 5 };
  const queued = (versionId: number) => ({
    id: 9,
    versionId,
    titleId: 7,
    title: "Show",
    year: 2010,
    posterPath: "",
    state: "queued",
    found: 3,
    missing: 2,
    downloading: 0,
  });

  it("adds the version to the queue", async () => {
    const posts: unknown[] = [];
    open(
      {},
      {
        "GET /backfill": json(emptyQueue),
        "POST /backfill": async (req) => {
          posts.push(await req.json());
          return json(queued(1), 201);
        },
      },
    );
    await userEvent.click(await screen.findByRole("button", { name: "Add Show to backfill" }));
    await waitFor(() => expect(posts).toEqual([{ versionId: 1 }]));
  });

  it("says where the version stands once it is in the queue", async () => {
    open(
      {},
      {
        "GET /backfill": json({
          ...emptyQueue,
          now: { ...queued(99), state: "active" },
          next: [queued(98), queued(1)],
        }),
      },
    );
    expect(await screen.findByText("In backfill (#2)")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /to backfill/ })).not.toBeInTheDocument();
  });

  it("says when it is being worked on", async () => {
    open(
      {},
      {
        "GET /backfill": json({ ...emptyQueue, now: { ...queued(1), state: "active" } }),
      },
    );
    expect(await screen.findByText("Backfilling now")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /to backfill/ })).not.toBeInTheDocument();
  });

  it("is left out when no aired episode is missing", async () => {
    const base = seriesDetail();
    open(
      { versions: base.versions.map((v) => ({ ...v, episodesWithFiles: 5 })) as never },
      {
        "GET /backfill": json(emptyQueue),
      },
    );
    await screen.findByRole("heading", { name: "Show" });
    await screen.findByText("5 of 5 episodes");
    expect(screen.queryByRole("button", { name: /to backfill/ })).not.toBeInTheDocument();
  });
});
