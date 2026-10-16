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
  titles: 222,
  files: 240,
  missing: 0,
  review: 2,
  size: 5 * 1024 ** 4,
  freeSpace: 1024 ** 4,
};

function open(roots: unknown[] = [root]) {
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
