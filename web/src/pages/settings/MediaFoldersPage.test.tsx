import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const tvNaming = {
  seriesFolder: "{Series TitleYear}",
  seasonFolder: "Season {season:00}",
  episodeFile: "{Series TitleYear} - S{season:00}E{episode:00}",
  dailyEpisodeFile: "{Series TitleYear} - {Air-Date}",
  multiEpisodeStyle: "prefixed-range",
};

const root = {
  id: 1,
  path: "/media/movies",
  mediaType: "movie",
  defaultProfileId: 1,
  manageFiles: false,
  upgradeFound: false,
  titles: 222,
  files: 240,
  missing: 0,
  review: 2,
  size: 5 * 1024 ** 4,
  freeSpace: 1024 ** 4,
};

function open(roots: unknown[] = [root], extra: Parameters<typeof renderApp>[1] = {}) {
  const saved: unknown[] = [];
  const added: unknown[] = [];
  const view = renderApp("/settings/media", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /library/roots": json(roots),
    "GET /library/browse": json({ path: "/", folders: [{ name: "emby", path: "/media" }] }),
    "POST /library/roots": async (req) => {
      added.push(await req.json());
      return json({ ...root, id: 2 });
    },
    "GET /profiles": json([
      { id: 1, name: "HD", slug: "hd", appliesTo: "both" },
      { id: 2, name: "Remux", slug: "remux", appliesTo: "movie" },
    ]),
    "PUT /library/roots/1": json(
      {
        code: "validation",
        message: "Some settings need attention.",
        fields: {
          defaultProfileId:
            "1 movie here already has a Remux copy in another folder (1917). A movie can have only one copy per profile, so this folder keeps its current profile.",
        },
      },
      422,
    ),
    "GET /settings/naming": json({
      movieFolder: "{Movie Title} ({Release Year})",
      movieFile: "{Movie Title}",
      ...tvNaming,
    }),
    "POST /settings/naming/preview": async (req) => {
      const body = await req.json();
      return json({
        examples: [
          {
            label: "A remux with everything",
            folder: "The Dark Knight (2008)",
            file: `${body.movieFile}.mkv`,
          },
        ],
        episodeExamples: [
          {
            label: "A multi-episode file",
            folder: "Doctor Who (2005)/Season 01",
            file: `${body.episodeFile} ${body.multiEpisodeStyle}.mkv`,
          },
        ],
        problems: {
          ...(body.movieFile.includes("Titel")
            ? { movieFile: "Reelkeeper doesn't know {Movie Titel}." }
            : {}),
          ...(body.episodeFile.includes("Epsiode")
            ? { episodeFile: "Reelkeeper doesn't know {Epsiode Title}." }
            : {}),
        },
      });
    },
    "PUT /settings/naming": async (req) => {
      const body = await req.json();
      saved.push(body);
      return json(body);
    },
    ...extra,
  });
  return { ...view, saved, added };
}

describe("media folders page", () => {
  it("shows each folder with its counts, read-only by default", async () => {
    open();
    expect(await screen.findByText("/media/movies")).toBeInTheDocument();
    expect(screen.getByText(/222 movies · 240 files · 5\.0 TB · 1\.0 TB free/)).toBeInTheDocument();
    expect(screen.getByText("2 to review")).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "Reelkeeper manages files here" })).not.toBeChecked();
  });

  it("explains a refused profile switch under the dropdown", async () => {
    open();
    await userEvent.selectOptions(
      await screen.findByLabelText("Movies here are the version"),
      "Remux",
    );
    expect(
      await screen.findByText(/1 movie here already has a Remux copy in another folder \(1917\)/),
    ).toBeInTheDocument();
    expect(screen.queryByText("Some settings need attention.")).not.toBeInTheDocument();
  });

  it("asks before letting Reelkeeper manage files", async () => {
    open();
    await userEvent.click(
      await screen.findByRole("switch", { name: "Reelkeeper manages files here" }),
    );
    expect(screen.getByText("Let Reelkeeper manage files in this folder?")).toBeInTheDocument();
  });

  it("previews naming live, flags problems and saves", async () => {
    const { saved } = open();
    const file = await screen.findByLabelText("Movie file");
    await waitFor(() => expect(file).toHaveValue("{Movie Title}"));
    await userEvent.clear(file);
    await userEvent.type(file, "{{Movie Titel}");
    expect(await screen.findByText("Reelkeeper doesn't know {Movie Titel}.")).toBeInTheDocument();

    await userEvent.click(
      within(screen.getByRole("region", { name: "Movie naming" })).getByRole("button", {
        name: "Simple",
      }),
    );
    expect(await screen.findByText("{Movie Title} ({Release Year}).mkv")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(saved).toEqual([
        {
          movieFolder: "{Movie Title} ({Release Year})",
          movieFile: "{Movie Title} ({Release Year})",
          ...tvNaming,
        },
      ]),
    );
  });

  it("labels TV folders as series", async () => {
    open([{ ...root, id: 2, path: "/media/shows", mediaType: "series", titles: 40, review: 0 }]);
    expect(await screen.findByText(/40 series · 240 files/)).toBeInTheDocument();
    const select = screen.getByLabelText("Shows here are the version");
    // A movies-only profile can't hold shows.
    expect(within(select).queryByText("Remux")).not.toBeInTheDocument();
  });

  it("adds a TV folder with a TV-capable profile", async () => {
    const { added } = open();
    await userEvent.click(await screen.findByRole("button", { name: /Add a media folder/ }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).queryByText(/arrive in the next update/)).not.toBeInTheDocument();
    // Movies by default: both profiles are offered.
    expect(within(dialog).getByRole("option", { name: "Remux" })).toBeInTheDocument();
    await userEvent.selectOptions(within(dialog).getByLabelText("What's in it"), "TV shows");
    expect(within(dialog).queryByRole("option", { name: "Remux" })).not.toBeInTheDocument();
    await userEvent.type(within(dialog).getByLabelText("Folder"), "/media/shows");
    await userEvent.click(within(dialog).getByRole("button", { name: "Add and scan" }));
    await waitFor(() =>
      expect(added).toEqual([{ path: "/media/shows", mediaType: "series", defaultProfileId: 1 }]),
    );
  });

  describe("upgrades", () => {
    const upgradeSwitch = "Upgrade movies found here";

    it("hides the switch for TV folders, and in the add dialog once TV is chosen", async () => {
      open([
        root,
        {
          ...root,
          id: 2,
          path: "/media/shows",
          mediaType: "series",
          titles: 40,
          upgradeFound: true,
        },
      ]);
      expect(await screen.findByText("/media/shows")).toBeInTheDocument();
      // One card has it (the movie folder), the TV folder's card doesn't.
      expect(screen.getAllByRole("switch", { name: upgradeSwitch })).toHaveLength(1);

      await userEvent.click(screen.getByRole("button", { name: /Add a media folder/ }));
      const dialog = await screen.findByRole("dialog");
      const addSwitch = "Upgrade movies already in this folder";
      expect(within(dialog).getByRole("switch", { name: addSwitch })).not.toBeChecked();
      await userEvent.selectOptions(within(dialog).getByLabelText("What's in it"), "TV shows");
      expect(within(dialog).queryByRole("switch", { name: addSwitch })).not.toBeInTheDocument();
    });

    it("adds a movie folder with upgrades off unless the switch is turned on", async () => {
      const { added } = open();
      const addFolder = async (path: string, turnOn: boolean) => {
        await userEvent.click(await screen.findByRole("button", { name: /Add a media folder/ }));
        const dialog = await screen.findByRole("dialog");
        await userEvent.type(within(dialog).getByLabelText("Folder"), path);
        if (turnOn) {
          await userEvent.click(
            within(dialog).getByRole("switch", { name: "Upgrade movies already in this folder" }),
          );
        }
        await userEvent.click(within(dialog).getByRole("button", { name: "Add and scan" }));
        await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
      };
      await addFolder("/media/a", false);
      await addFolder("/media/b", true);
      expect(added).toEqual([
        { path: "/media/a", mediaType: "movie", defaultProfileId: 1, upgradeFound: false },
        { path: "/media/b", mediaType: "movie", defaultProfileId: 1, upgradeFound: true },
      ]);
    });

    it("asks first, with the movie count, and sends upgradeFound only then", async () => {
      const puts: unknown[] = [];
      open([root], {
        "PUT /library/roots/1": async (req) => {
          const body = await req.json();
          puts.push(body);
          return json({ ...root, ...body });
        },
      });
      await userEvent.click(await screen.findByRole("switch", { name: upgradeSwitch }));
      let dialog = await screen.findByRole("dialog");
      expect(within(dialog).getByText("Look for better copies of 222 movies?")).toBeInTheDocument();
      expect(
        within(dialog).getByText(
          "Reelkeeper will turn upgrades on for every movie in /media/movies, including ones it downloaded, and look for better copies at its usual pace.",
        ),
      ).toBeInTheDocument();
      // Cancelling changes nothing.
      await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
      expect(puts).toEqual([]);
      expect(screen.getByRole("switch", { name: upgradeSwitch })).not.toBeChecked();

      await userEvent.click(screen.getByRole("switch", { name: upgradeSwitch }));
      dialog = await screen.findByRole("dialog");
      await userEvent.click(
        within(dialog).getByRole("button", { name: "Yes, look for better copies" }),
      );
      await waitFor(() =>
        expect(puts).toEqual([{ defaultProfileId: 1, manageFiles: false, upgradeFound: true }]),
      );
    });

    it("confirms turning upgrades off, with a singular count", async () => {
      const puts: unknown[] = [];
      open([{ ...root, titles: 1, upgradeFound: true }], {
        "PUT /library/roots/1": async (req) => {
          const body = await req.json();
          puts.push(body);
          return json({ ...root, ...body });
        },
      });
      await userEvent.click(await screen.findByRole("switch", { name: upgradeSwitch }));
      const dialog = await screen.findByRole("dialog");
      expect(within(dialog).getByText("Stop upgrading 1 movie?")).toBeInTheDocument();
      expect(
        within(dialog).getByText(/keeps every file in \/media\/movies as it is/),
      ).toBeInTheDocument();
      await userEvent.click(within(dialog).getByRole("button", { name: "Yes, keep the files" }));
      await waitFor(() =>
        expect(puts).toEqual([{ defaultProfileId: 1, manageFiles: false, upgradeFound: false }]),
      );
    });

    it("leaves upgradeFound out when only the profile changes", async () => {
      const puts: unknown[] = [];
      open([root], {
        "PUT /library/roots/1": async (req) => {
          puts.push(await req.json());
          return json(root);
        },
      });
      await userEvent.selectOptions(
        await screen.findByLabelText("Movies here are the version"),
        "Remux",
      );
      await waitFor(() => expect(puts).toEqual([{ defaultProfileId: 2, manageFiles: false }]));
    });
  });

  it("edits the TV templates, previews episodes, flags problems and saves", async () => {
    const { saved } = open();
    const file = await screen.findByLabelText("Episode file");
    await waitFor(() => expect(file).toHaveValue(tvNaming.episodeFile));
    expect(await screen.findByText(/A multi-episode file/)).toBeInTheDocument();

    await userEvent.type(file, "{{Epsiode Title}");
    expect(await screen.findByText("Reelkeeper doesn't know {Epsiode Title}.")).toBeInTheDocument();

    const style = screen.getByLabelText("Files with several episodes");
    expect(within(style).getByRole("option", { name: "Range: S01E01-03" })).toBeInTheDocument();
    await userEvent.selectOptions(style, "scene");
    expect(await screen.findByText(/scene\.mkv/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(saved).toHaveLength(1));
    expect(saved[0]).toMatchObject({
      episodeFile: `${tvNaming.episodeFile}{Epsiode Title}`,
      multiEpisodeStyle: "scene",
    });
  });
});
