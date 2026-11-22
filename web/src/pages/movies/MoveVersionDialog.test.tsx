import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const movie = {
  id: 3,
  tmdbId: 170,
  imdbId: "",
  title: "Heat",
  originalTitle: "Heat",
  sortTitle: "heat",
  year: 1995,
  runtime: 170,
  genres: [],
  certification: "",
  overview: "",
  posterPath: "",
  backdropPath: "",
  addedAt: "2026-10-08T00:00:00Z",
  versions: [
    {
      id: 11,
      profileId: 1,
      profileName: "HD",
      rootFolderId: 1,
      rootPath: "/media/kids",
      folder: "Heat (1995)",
      monitored: true,
      upgrades: true,
      files: 1,
      missing: 0,
      size: 8 * 1024 ** 3,
      quality: "Bluray-1080p",
      resolution: 1080,
      hdr: [],
      mediaFiles: [],
    },
  ],
};

const root = (id: number, path: string, mediaType = "movie") => ({
  id,
  path,
  mediaType,
  defaultProfileId: 1,
  manageFiles: true,
  upgradeFound: false,
  titles: 0,
  files: 0,
  missing: 0,
  review: 0,
  size: 0,
  freeSpace: -1,
});

const plan = (over: Record<string, unknown> = {}) => ({
  versionId: 11,
  fromPath: "/media/kids",
  toPath: "/media/movies",
  folder: "Heat (1995)",
  wholeFolder: true,
  files: [
    { path: "Heat (1995)/Heat (1995) [Bluray-1080p].mkv", size: 8 * 1024 ** 3 },
    { path: "Heat (1995)/Heat (1995) [Bluray-1080p].en.srt", size: 1024 },
  ],
  bytes: 8 * 1024 ** 3 + 1024,
  copy: true,
  freeSpace: 500 * 1024 ** 3,
  problems: [],
  ...over,
});

const job = (status: string, message = "") => ({
  id: 41,
  kind: "library.move",
  name: "Move a movie to another media folder",
  status,
  trigger: "manual",
  progress: status === "running" ? 40 : 100,
  message,
  error: "",
  attempts: 1,
  quiet: false,
  createdAt: "2026-11-22T10:00:00Z",
});

function open(routes: Record<string, Response | ((r: Request) => Promise<Response>)> = {}) {
  return renderApp("/movies/3", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /movies/3": json(movie),
    "GET /library/roots": json([
      root(1, "/media/kids"),
      root(2, "/media/movies"),
      root(3, "/media/tv", "series"),
    ]),
    ...routes,
  });
}

describe("moving a movie to another media folder", () => {
  it("shows the plan, moves, and follows the task to the end", async () => {
    const { calls } = open({
      "GET /movies/3/versions/11/move": json(plan()),
      "POST /movies/3/versions/11/move": json(job("queued")),
      "GET /system/tasks": json({
        tasks: [],
        recent: [job("succeeded", "Moved Heat (1995) to /media/movies/Heat (1995).")],
        hiddenRoutine: 0,
      }),
    });
    await userEvent.click(await screen.findByRole("button", { name: /Move…/ }));
    const dialog = await screen.findByRole("dialog");
    // Only other movie folders are offered: not the one it's in, not TV.
    const select = within(dialog).getByRole("combobox", { name: "Move to" });
    expect(
      within(select)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["/media/movies"]);
    expect(await within(dialog).findByText("/media/movies/Heat (1995)")).toBeInTheDocument();
    expect(within(dialog).getByText(/Everything in the movie's folder moves/)).toBeInTheDocument();
    expect(within(dialog).getByText(/different disks.*old copy is deleted/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "GET" && c.url.includes("/move?rootFolderId=2"))).toBe(
      true,
    );

    await userEvent.click(within(dialog).getByRole("button", { name: "Move" }));
    expect(
      await within(dialog).findByText("Moved Heat (1995) to /media/movies/Heat (1995)."),
    ).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST" && c.url.endsWith("/move"));
    expect(await post?.clone().json()).toEqual({ rootFolderId: 2 });
  });

  it("says why a move can't be done, with no Move button", async () => {
    open({
      "GET /movies/3/versions/11/move": json(
        plan({
          problems: [
            'Reelkeeper only reads /media/movies, so it can\'t put files there. Turn on "Reelkeeper manages files here" for it in Settings → Media Folders first.',
          ],
        }),
      ),
    });
    await userEvent.click(await screen.findByRole("button", { name: /Move…/ }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText(/only reads \/media\/movies/)).toBeInTheDocument();
    expect(
      within(dialog).getByRole("link", { name: /Open Settings → Media Folders/ }),
    ).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Move" })).not.toBeInTheDocument();
  });

  it("explains a version with nothing on disk", async () => {
    open({
      "GET /movies/3/versions/11/move": json(plan({ files: [], bytes: 0, copy: false })),
    });
    await userEvent.click(await screen.findByRole("button", { name: /Move…/ }));
    const dialog = await screen.findByRole("dialog");
    expect(
      await within(dialog).findByText(
        /Nothing is on disk yet.*downloads will go to \/media\/movies/,
      ),
    ).toBeInTheDocument();
  });

  it("isn't offered when there's no other movie folder", async () => {
    renderApp("/movies/3", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /movies/3": json(movie),
      "GET /library/roots": json([root(1, "/media/kids"), root(3, "/media/tv", "series")]),
    });
    expect(await screen.findByRole("switch", { name: "Upgrades" })).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: /Move…/ })).not.toBeInTheDocument(),
    );
  });
});
