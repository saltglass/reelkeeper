import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const report = {
  instance: "Sonarr",
  version: "4.0.20",
  series: 59,
  toAdd: 2,
  monitoredSeries: 50,
  episodes: 2000,
  matched: 1900,
  matchedByDate: 12,
  unmatched: 3,
  newlyWanted: 436,
  alreadyWanted: 20,
  turnedOff: 5,
  history: 157,
  blocklist: 1,
  wanted: [
    {
      title: "Slow Horses",
      year: 2022,
      seasons: [
        { season: 1, episodes: 3 },
        { season: 2, episodes: 6 },
      ],
    },
    { title: "Tin Star", year: 2017, seasons: [{ season: 1, episodes: 1 }] },
  ],
  issues: [
    {
      tmdbId: 1,
      title: "Harbor Lights",
      year: 2015,
      kind: "numbered-differently",
      detail: "7 episodes matched by air date: Sonarr and Reelkeeper number them differently.",
    },
    {
      tmdbId: 2,
      title: "Elsewhere",
      year: 2010,
      kind: "no-media-folder",
      detail: "Sonarr keeps it in /data/tv.",
    },
  ],
  imported: false,
  added: 0,
  monitorChanged: 0,
  episodesTurnedOn: 0,
  episodesTurnedOff: 0,
  historyAdded: 0,
  blocklistAdded: 0,
};

const profiles = [
  { id: 1, name: "HD", slug: "hd", appliesTo: "movie", mediaFolders: 1, movies: 0, series: 0 },
  { id: 2, name: "Both", slug: "both", appliesTo: "both", mediaFolders: 1, movies: 0, series: 0 },
  { id: 3, name: "HDTV", slug: "hdtv", appliesTo: "series", mediaFolders: 1, movies: 0, series: 0 },
];

function open(
  over: Record<string, Response | ((r: Request) => Response | Promise<Response>)> = {},
) {
  return renderApp("/settings/import-sonarr", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /profiles": json(profiles),
    "POST /migrate/sonarr/preview": json(report),
    ...over,
  });
}

async function fillIn() {
  await userEvent.type(
    await screen.findByLabelText("Sonarr's address"),
    "http://192.168.1.20:8989",
  );
  await userEvent.type(screen.getByLabelText("API key"), "secret-key");
}

describe("Import from Sonarr", () => {
  it("previews with a summary, the wanted list and issues grouped by kind", async () => {
    const { calls } = open();
    await fillIn();
    const select = screen.getByLabelText("Bring its series into") as HTMLSelectElement;
    // HDTV is picked for you; movie-only profiles aren't offered.
    await vi.waitFor(() => expect(select.value).toBe("3"));
    expect(within(select).queryByText("HD")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));

    expect(
      await screen.findByText(
        /Sonarr 4\.0\.20: 59 series, 2,000 episodes\. Reelkeeper will start looking for 436 older episodes Sonarr was monitoring\./,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/157 history records and 1 blocklist entry would be brought over/),
    ).toBeInTheDocument();
    expect(screen.getByText(/12 episodes are numbered differently/)).toBeInTheDocument();

    // The wanted list: a row per series, opening to show its seasons.
    expect(screen.getByText("Slow Horses (2022)")).toBeInTheDocument();
    expect(screen.getByText("9 episodes")).toBeInTheDocument();
    expect(screen.queryByText("S01: 3, S02: 6")).not.toBeInTheDocument();
    await userEvent.click(screen.getByText("Slow Horses (2022)"));
    expect(screen.getByText("S01: 3, S02: 6")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Expand all" }));
    expect(screen.getByText("S01: 1")).toBeInTheDocument();

    // Issues are grouped under plain headings and collapsed until opened.
    expect(screen.getByText("Numbered differently")).toBeInTheDocument();
    expect(screen.getByText("Can't be brought over")).toBeInTheDocument();
    expect(screen.queryByText("Only in Reelkeeper")).not.toBeInTheDocument();
    expect(screen.queryByText("Harbor Lights (2015)")).not.toBeInTheDocument();
    await userEvent.click(screen.getByText("Numbered differently"));
    expect(screen.getByText("Harbor Lights (2015)")).toBeInTheDocument();
    expect(screen.getByText(/7 episodes matched by air date/)).toBeInTheDocument();

    const sent = await calls.find((c) => c.url.includes("/preview"))?.json();
    expect(sent).toEqual({ url: "http://192.168.1.20:8989", apiKey: "secret-key", profileId: 3 });
  });

  it("says so when there is nothing older to look for and nothing differs", async () => {
    open({
      "POST /migrate/sonarr/preview": json({
        ...report,
        newlyWanted: 0,
        wanted: [],
        issues: [],
        matchedByDate: 0,
      }),
    });
    await fillIn();
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(
      await screen.findByText(/Reelkeeper already wants everything Sonarr is monitoring/),
    ).toBeInTheDocument();
    expect(screen.getByText("Sonarr and Reelkeeper agree on every series.")).toBeInTheDocument();
    expect(screen.queryByText("Older episodes Reelkeeper will look for")).not.toBeInTheDocument();
  });

  it("confirms, imports and shows what was done", async () => {
    const { calls } = open({
      "POST /migrate/sonarr/import": json({
        ...report,
        imported: true,
        added: 2,
        monitorChanged: 1,
        episodesTurnedOn: 440,
        episodesTurnedOff: 5,
        historyAdded: 157,
        blocklistAdded: 1,
      }),
    });
    await fillIn();
    expect(screen.queryByRole("button", { name: "Import" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));
    await userEvent.click(await screen.findByRole("button", { name: "Import" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Reelkeeper will start looking for 436 older episodes");
    expect(dialog).toHaveTextContent("the scheduled search picks a few each hour");
    expect(dialog).toHaveTextContent("only while the queue has room");
    expect(calls.some((c) => c.url.includes("/migrate/sonarr/import"))).toBe(false);
    await userEvent.click(within(dialog).getByRole("button", { name: "Import" }));
    expect(
      await screen.findByText(
        "Added 2 series, changed monitoring on 1, turned 440 episodes on and 5 off, and brought over 157 history records and 1 blocklist entry.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText(/Reelkeeper will now look for 436 older episodes/)).toBeInTheDocument();
    const result = screen.getByText(/^Added 2 series/).closest("div") as HTMLElement;
    expect(within(result).getByRole("link", { name: "Series" })).toHaveAttribute("href", "/series");
    expect(within(result).getByRole("link", { name: "Activity → History" })).toBeInTheDocument();
  });

  it("shows Sonarr's problem in plain words", async () => {
    open({
      "POST /migrate/sonarr/preview": json(
        { code: "sonarr", message: "Sonarr refused the API key." },
        422,
      ),
    });
    await fillIn();
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(await screen.findByText("Sonarr refused the API key.")).toBeInTheDocument();
  });

  it("is reached from Media Folders", async () => {
    renderApp("/settings/media", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /library/roots": json([]),
      "GET /profiles": json([]),
      "GET /skipped-folders": json([]),
    });
    expect(await screen.findByText("Moving from Sonarr?")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Import from Sonarr" })).toHaveAttribute(
      "href",
      "/settings/import-sonarr",
    );
  });
});
