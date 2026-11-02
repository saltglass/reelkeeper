import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { MovieChoices } from "@/hooks/grab";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const movie = {
  id: 3,
  tmdbId: 613,
  imdbId: "tt0363163",
  title: "Downfall",
  originalTitle: "Der Untergang",
  sortTitle: "downfall",
  year: 2004,
  runtime: 156,
  genres: ["Drama", "History"],
  certification: "R",
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
      rootPath: "/media/movies",
      folder: "Downfall (2004)",
      monitored: true,
      upgrades: true,
      files: 0,
      missing: 0,
      size: 0,
      quality: "",
      resolution: 0,
      hdr: [],
      mediaFiles: [],
    },
  ],
};

const choices: MovieChoices = {
  hidden: 2,
  more: 0,
  indexerErrors: ["Slowtracker: timed out"],
  releases: [
    {
      token: "tok-ok",
      title: "Downfall.2004.1080p.BluRay.x264-GOOD",
      size: 10 * 1024 ** 3,
      quality: "Bluray-1080p",
      indexer: "Knaben",
      seeders: 12,
      score: 10,
      accepted: true,
      reasons: [],
    },
    {
      token: "tok-ger",
      title: "Der.Untergang.2004.GERMAN.1080p.BluRay.x264-GER",
      size: 12 * 1024 ** 3,
      quality: "Bluray-1080p",
      indexer: "Torrentleech",
      seeders: 80,
      score: 0,
      accepted: false,
      reasons: ["It doesn't have English audio, which this profile requires."],
    },
    {
      token: "tok-dvd",
      title: "Downfall.2004.DVDRip.x264-OLD",
      size: 700 * 1024 ** 2,
      quality: "DVD",
      indexer: "Knaben",
      seeders: 3,
      score: 0,
      accepted: false,
      reasons: [
        "DVD isn't a quality this profile accepts.",
        "It doesn't have English audio, which this profile requires.",
      ],
    },
  ],
};

function open(grab: (req: Request) => Response | Promise<Response>) {
  const searches: number[] = [];
  const out = renderApp("/movies/3", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /movies/3": json(movie),
    "GET /movies/3/versions/11/releases": () => {
      searches.push(11);
      return json(choices);
    },
    "POST /movies/3/versions/11/releases/grab": grab,
  });
  return { ...out, searches };
}

async function openList() {
  await userEvent.click(await screen.findByRole("button", { name: /Choose a release/ }));
  const dialog = await screen.findByRole("dialog");
  await within(dialog).findByText("Downfall.2004.1080p.BluRay.x264-GOOD");
  return dialog;
}

function titles(dialog: HTMLElement) {
  return within(dialog)
    .getAllByRole("listitem")
    .map((li) => li.querySelector("p")?.textContent);
}

describe("Choose a release", () => {
  it("searches only when asked and lists accepted releases first, then the misses with why", async () => {
    const { searches } = open(() => json({ message: "", dryRun: false }, 202));
    await screen.findByRole("heading", { name: "Downfall" });
    expect(searches).toEqual([]); // nothing is searched just by looking

    const dialog = await openList();
    expect(searches).toEqual([11]);
    expect(titles(dialog)).toEqual([
      "Downfall.2004.1080p.BluRay.x264-GOOD",
      "Der.Untergang.2004.GERMAN.1080p.BluRay.x264-GER",
      "Downfall.2004.DVDRip.x264-OLD",
    ]);
    const good = within(dialog)
      .getByText("Downfall.2004.1080p.BluRay.x264-GOOD")
      .closest("li") as HTMLElement;
    expect(within(good).getByText("Meets your profile")).toBeInTheDocument();
    const ger = within(dialog)
      .getByText(/GERMAN/)
      .closest("li") as HTMLElement;
    expect(within(ger).queryByText("Meets your profile")).not.toBeInTheDocument();
    expect(within(ger).getByText("80 seeders")).toBeInTheDocument();
    expect(within(ger).getByText("12 GB")).toBeInTheDocument();
    expect(within(ger).getByText(/doesn't have English audio/)).toBeInTheDocument();
    expect(within(dialog).getByText(/Slowtracker: timed out/)).toBeInTheDocument();
    expect(
      within(dialog).getByText(
        /2 releases for another movie, on the blocklist, or with no seeders aren't shown/,
      ),
    ).toBeInTheDocument();
  });

  it("filters and sorts the list", async () => {
    open(() => json({ message: "", dryRun: false }, 202));
    const dialog = await openList();

    await userEvent.type(
      within(dialog).getByRole("searchbox", { name: "Filter releases" }),
      "german",
    );
    expect(titles(dialog)).toEqual(["Der.Untergang.2004.GERMAN.1080p.BluRay.x264-GER"]);
    await userEvent.type(
      within(dialog).getByRole("searchbox", { name: "Filter releases" }),
      " remux",
    );
    expect(within(dialog).getByText("No release matches the filter.")).toBeInTheDocument();
    await userEvent.clear(within(dialog).getByRole("searchbox", { name: "Filter releases" }));

    await userEvent.selectOptions(
      within(dialog).getByRole("combobox", { name: "Sort releases" }),
      "seeders",
    );
    expect(titles(dialog)[0]).toBe("Der.Untergang.2004.GERMAN.1080p.BluRay.x264-GER");
    await userEvent.selectOptions(
      within(dialog).getByRole("combobox", { name: "Sort releases" }),
      "smallest",
    );
    expect(titles(dialog)[0]).toBe("Downfall.2004.DVDRip.x264-OLD");
  });

  it("asks before grabbing a release outside the profile, and says Upgrades will turn off", async () => {
    const grabs: unknown[] = [];
    open(async (req) => {
      grabs.push(await req.json());
      return json({ message: "Grabbed it.", dryRun: false, upgradesOff: true }, 202);
    });
    const dialog = await openList();

    await userEvent.click(within(dialog).getByRole("button", { name: /Grab Der\.Untergang/ }));
    expect(
      within(dialog).getByText("Download this release even though it's outside your profile?"),
    ).toBeInTheDocument();
    expect(within(dialog).getByText(/its\s+Upgrades switch turns off/)).toBeInTheDocument();

    // Going back grabs nothing.
    await userEvent.click(within(dialog).getByRole("button", { name: "Back to the list" }));
    expect(grabs).toEqual([]);

    // Confirming sends the token, not the release.
    await userEvent.click(within(dialog).getByRole("button", { name: /Grab Der\.Untergang/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Download it" }));
    await waitFor(() => expect(grabs).toEqual([{ token: "tok-ger" }]));
    expect(await within(dialog).findByText("Sent to the download client")).toBeInTheDocument();
    expect(
      await screen.findByText(
        "Sent to the download client. Upgrades are now off for this version.",
      ),
    ).toBeInTheDocument();
  });

  it("grabs a release the profile accepts without asking", async () => {
    const grabs: unknown[] = [];
    open(async (req) => {
      grabs.push(await req.json());
      return json({ message: "Grabbed it.", dryRun: false }, 202);
    });
    const dialog = await openList();
    await userEvent.click(
      within(dialog).getByRole("button", { name: /Grab Downfall\.2004\.1080p/ }),
    );
    await waitFor(() => expect(grabs).toEqual([{ token: "tok-ok" }]));
    expect(within(dialog).queryByText(/outside your profile/)).not.toBeInTheDocument();
  });

  it("says when a grab was only a dry run", async () => {
    open(() => json({ message: "Would grab it.", dryRun: true }, 202));
    const dialog = await openList();
    await userEvent.click(
      within(dialog).getByRole("button", { name: /Grab Downfall\.2004\.1080p/ }),
    );
    expect(await within(dialog).findByText("Recorded as a dry run")).toBeInTheDocument();
    expect(
      await screen.findByText("Dry run: recorded in History, nothing was downloaded."),
    ).toBeInTheDocument();
  });

  it("shows why a grab was refused", async () => {
    open(() => json({ code: "refused", message: "That release is on the blocklist." }, 409));
    const dialog = await openList();
    await userEvent.click(within(dialog).getByRole("button", { name: /Grab Der\.Untergang/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Download it" }));
    expect(
      await within(dialog).findByText("That release is on the blocklist."),
    ).toBeInTheDocument();
  });
});
