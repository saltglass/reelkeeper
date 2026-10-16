import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const file = {
  id: 7,
  path: "28 Days Later (2002)/28.Days.Later.2002.1080p.BluRay.x265-GRP.mkv",
  size: 8 * 1024 ** 3,
  quality: "Bluray-1080p",
  resolution: 1080,
  source: "bluray",
  edition: "",
  releaseGroup: "GRP",
  durationSeconds: 6780,
  probeError: "",
  video: { codec: "H.265", width: 1920, height: 1036, bitDepth: 10, hdr: [] },
  audio: [{ codec: "DTS", channels: 6, language: "eng", default: true }],
  subtitles: [{ codec: "SRT", language: "eng", forced: false }],
};

const movie = {
  id: 3,
  tmdbId: 170,
  imdbId: "tt0289043",
  title: "28 Days Later",
  originalTitle: "28 Days Later",
  sortTitle: "28 days later",
  year: 2002,
  runtime: 113,
  genres: ["Horror", "Thriller"],
  certification: "R",
  overview: "Twenty-eight days after a killer virus…",
  posterPath: "",
  backdropPath: "",
  addedAt: "2026-10-08T00:00:00Z",
  versions: [
    {
      id: 11,
      profileId: 1,
      profileName: "HD",
      rootFolderId: 1,
      rootPath: "/media/movies",
      folder: "28 Days Later (2002)",
      monitored: true,
      upgrades: true,
      files: 1,
      missing: 0,
      size: file.size,
      quality: "Bluray-1080p",
      resolution: 1080,
      hdr: [],
      mediaFiles: [file],
    },
  ],
};

const plan = (managed: boolean, done = false) => [
  {
    versionId: 11,
    profileName: "HD",
    rootPath: "/media/movies",
    managed,
    files: [
      {
        fileId: 7,
        from: file.path,
        to: "28 Days Later (2002)/28 Days Later (2002) [Bluray-1080p]-GRP.mkv",
        extras: [
          {
            from: "28 Days Later (2002)/28.Days.Later.2002.1080p.BluRay.x265-GRP.en.srt",
            to: "x.en.srt",
          },
        ],
        problem: "",
        done,
      },
    ],
  },
];

function open(managed: boolean) {
  return renderApp("/movies/3", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /movies/3": json(movie),
    "GET /movies/3/rename": json(plan(managed)),
    "POST /movies/3/rename": json(plan(managed, true)),
  });
}

describe("movie page", () => {
  it("shows the movie, its version and what's in the file", async () => {
    open(false);
    expect(await screen.findByRole("heading", { name: "28 Days Later" })).toBeInTheDocument();
    expect(screen.getByText("2002 · 1h 53m · R · Horror, Thriller")).toBeInTheDocument();
    expect(screen.getByText(/1920×1036 · H\.265 · 10-bit · DTS 5\.1/)).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: /Show 1 audio and 1 subtitle tracks/ }),
    );
    expect(screen.getByText("English · DTS · 5.1 · default")).toBeInTheDocument();
  });

  it("previews a rename and won't do it in a read-only folder", async () => {
    open(false);
    await userEvent.click(await screen.findByRole("button", { name: /Rename files/ }));
    const dialog = await screen.findByRole("dialog");
    expect(
      await within(dialog).findByText(/28 Days Later \(2002\) \[Bluray-1080p\]-GRP\.mkv/),
    ).toBeInTheDocument();
    expect(within(dialog).getByText(/Also renamed: 28\.Days.*en\.srt/)).toBeInTheDocument();
    expect(
      within(dialog).getByText(/This is a preview: Reelkeeper only reads \/media\/movies/),
    ).toBeInTheDocument();
    // No greyed-out button that looks broken: just Close.
    expect(within(dialog).queryByRole("button", { name: /^Rename \d/ })).not.toBeInTheDocument();
    expect(within(dialog).getAllByRole("button", { name: "Close" }).length).toBeGreaterThan(0);
  });

  it("renames in a managed folder and shows the outcome", async () => {
    const { calls } = open(true);
    await userEvent.click(await screen.findByRole("button", { name: /Rename files/ }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(await within(dialog).findByRole("button", { name: "Rename 1 file" }));
    expect(await within(dialog).findByText("Renamed")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST" && c.url.endsWith("/movies/3/rename"))).toBe(true);
  });

  it("searches now, says where the results will show, and lists the movie's history", async () => {
    const { calls } = renderApp("/movies/3", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /movies/3": json(movie),
      "GET /history": json({
        events: [
          {
            id: 1,
            createdAt: "2026-10-09T10:00:00Z",
            titleId: 3,
            versionId: 11,
            title: "28 Days Later",
            year: 2002,
            profile: "HD",
            event: "searched",
            sourceTitle: "",
            indexer: "",
            quality: "",
            score: 0,
            message: "Found nothing acceptable.",
            data: {},
          },
        ],
        nextBefore: null,
      }),
      "POST /movies/3/versions/11/search": json({ id: 1, kind: "grab.search", status: "queued" }),
    });
    await userEvent.click(await screen.findByRole("button", { name: /Search now/ }));
    expect(
      await screen.findByText("Searching… results will appear in History below."),
    ).toBeInTheDocument();
    expect(
      calls.some((c) => c.method === "POST" && c.url.endsWith("/movies/3/versions/11/search")),
    ).toBe(true);
    expect(await screen.findByText("Found nothing acceptable.")).toBeInTheDocument();
    expect(calls.some((c) => c.url.includes("titleId=3") && c.url.includes("limit=10"))).toBe(true);
    expect(screen.getByRole("link", { name: /See all in Activity/ })).toHaveAttribute(
      "href",
      "/activity/history",
    );
  });

  it("switches a version's upgrades without touching monitoring, and back", async () => {
    const bodies: unknown[] = [];
    renderApp("/movies/3", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /movies/3": json(movie),
      "PATCH /movies/3/versions/11": async (req) => {
        bodies.push(await req.json());
        return new Response(null, { status: 204 });
      },
    });
    const upgrades = await screen.findByRole("switch", { name: "Upgrades" });
    expect(upgrades).toBeChecked();
    await userEvent.click(upgrades);
    await userEvent.click(screen.getByRole("switch", { name: "Monitored" }));
    await waitFor(() => expect(bodies).toEqual([{ upgrades: false }, { monitored: false }]));
  });

  it("says what turned-off upgrades mean", async () => {
    const off = { ...movie, versions: [{ ...movie.versions[0], upgrades: false }] };
    renderApp("/movies/3", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /movies/3": json(off),
    });
    expect(await screen.findByRole("switch", { name: "Upgrades" })).not.toBeChecked();
    expect(
      screen.getByText(
        "Off: Reelkeeper keeps this file and never looks for a better copy. A missing file is still downloaded again.",
      ),
    ).toBeInTheDocument();
  });

  describe("deleting", () => {
    const roots = (manageFiles: boolean) => [
      { id: 1, path: "/media/movies", mediaType: "movie", defaultProfileId: 1, manageFiles },
    ];
    function openDelete(manageFiles: boolean, client = "qbittorrent", queued = false) {
      return renderApp("/movies/3", {
        "GET /settings/download-client": json({
          type: client,
          url: "http://client:1234",
          username: "u",
          passwordSet: true,
          folder: "",
        }),
        "GET /auth/state": json(signedIn),
        "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
        "GET /movies/3": json(movie),
        "GET /movies": json([]),
        "GET /library/roots": json(roots(manageFiles)),
        "GET /library/review": json([]),
        "GET /profiles": json([]),
        "GET /history": json({ events: [], nextBefore: null }),
        "GET /queue": json({
          items: queued ? [{ id: 1, movie: { id: 3, title: "28 Days Later", year: 2002 } }] : [],
          clientError: null,
        }),
        "DELETE /movies/3": json({ deleted: [], skipped: ["/media/movies/28 Days Later (2002)"] }),
      });
    }

    it("keeps the files by default, then goes to Movies with a note", async () => {
      const { calls, router } = openDelete(true);
      await userEvent.click(await screen.findByRole("button", { name: /Delete movie/ }));
      const dialog = await screen.findByRole("dialog");
      expect(
        within(dialog).getByRole("heading", { name: "Delete 28 Days Later (2002)?" }),
      ).toBeInTheDocument();
      expect(within(dialog).getByRole("radio", { name: /Keep the files/ })).toBeChecked();
      await userEvent.click(within(dialog).getByRole("button", { name: "Delete movie" }));
      await vi.waitFor(() => expect(router.state.location.pathname).toBe("/movies"));
      const del = calls.find((c) => c.method === "DELETE");
      expect(del?.url).toMatch(/\/movies\/3\?deleteFiles=false$/);
      expect(await screen.findByText(/Deleted 28 Days Later \(2002\)\./)).toBeInTheDocument();
    });

    it("lists the files and turns red when deleting them too", async () => {
      const { calls } = openDelete(true);
      await userEvent.click(await screen.findByRole("button", { name: /Delete movie/ }));
      const dialog = await screen.findByRole("dialog");
      await userEvent.click(await within(dialog).findByRole("radio", { name: /Also delete/ }));
      expect(
        within(dialog).getByText(/28\.Days\.Later\.2002\.1080p\.BluRay\.x265-GRP\.mkv/),
      ).toBeInTheDocument();
      await userEvent.click(within(dialog).getByRole("button", { name: "Delete movie and files" }));
      await vi.waitFor(() =>
        expect(calls.find((c) => c.method === "DELETE")?.url).toMatch(/deleteFiles=true$/),
      );
    });

    it("says which download client its Queue downloads are removed from", async () => {
      openDelete(true, "transmission", true);
      await userEvent.click(await screen.findByRole("button", { name: /Delete movie/ }));
      const dialog = await screen.findByRole("dialog");
      expect(
        await within(dialog).findByText(
          "Its downloads in the Queue are removed from Transmission too.",
        ),
      ).toBeInTheDocument();
    });

    it("won't offer to delete files in a read-only folder", async () => {
      openDelete(false);
      await userEvent.click(await screen.findByRole("button", { name: /Delete movie/ }));
      const dialog = await screen.findByRole("dialog");
      expect(
        await within(dialog).findByText(/\/media\/movies is read-only to Reelkeeper/),
      ).toBeInTheDocument();
      expect(within(dialog).getByRole("radio", { name: /Also delete/ })).toBeDisabled();
    });
  });
});
