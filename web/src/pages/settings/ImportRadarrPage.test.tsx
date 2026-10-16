import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const report = {
  instance: "Radarr",
  version: "6.4.4",
  movies: 342,
  withFile: 338,
  monitored: 300,
  wanted: 4,
  toAdd: 2,
  history: 157,
  blocklist: 0,
  issues: [
    {
      tmdbId: 1,
      title: "Dune",
      year: 2021,
      kind: "quality-differs",
      detail: "Radarr says WEBDL-1080p; Reelkeeper reads Bluray-1080p.",
    },
    {
      tmdbId: 2,
      title: "Elsewhere",
      year: 2010,
      kind: "no-media-folder",
      detail: "Radarr keeps it in /data.",
    },
  ],
  imported: false,
  added: 0,
  monitorChanged: 0,
  historyAdded: 0,
  blocklistAdded: 0,
};

const profiles = [
  { id: 1, name: "HD", slug: "hd", appliesTo: "both", mediaFolders: 1, movies: 0, series: 0 },
  {
    id: 2,
    name: "Remux",
    slug: "remux",
    appliesTo: "movie",
    mediaFolders: 1,
    movies: 0,
    series: 0,
  },
  { id: 3, name: "TV", slug: "tv", appliesTo: "series", mediaFolders: 1, movies: 0, series: 0 },
];

function open(
  over: Record<string, Response | ((r: Request) => Response | Promise<Response>)> = {},
) {
  return renderApp("/settings/import-radarr", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /profiles": json(profiles),
    "POST /migrate/radarr/preview": json(report),
    ...over,
  });
}

async function fillIn() {
  await userEvent.type(
    await screen.findByLabelText("Radarr's address"),
    "http://192.168.1.20:7979",
  );
  await userEvent.type(screen.getByLabelText("API key"), "secret-key");
}

describe("Import from Radarr", () => {
  it("previews with a summary and issues grouped by kind", async () => {
    const { calls } = open();
    await fillIn();
    const select = screen.getByLabelText("Bring its movies into") as HTMLSelectElement;
    await vi.waitFor(() => expect(select.value).toBe("1"));
    // Series-only profiles aren't offered.
    expect(within(select).queryByText("TV")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));

    expect(
      await screen.findByText(/Radarr \(Radarr 6\.4\.4\) has 342 movies: 338 with files/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/157 history records and 0 blocklist entries would be brought over/),
    ).toBeInTheDocument();
    expect(screen.getByText("Reelkeeper reads a different quality")).toBeInTheDocument();
    expect(screen.getByText("Can't be brought over")).toBeInTheDocument();
    expect(screen.queryByText("Only in Reelkeeper")).not.toBeInTheDocument();
    // Collapsed until opened.
    expect(screen.queryByText("Dune (2021)")).not.toBeInTheDocument();
    await userEvent.click(screen.getByText("Reelkeeper reads a different quality"));
    expect(screen.getByText("Dune (2021)")).toBeInTheDocument();
    expect(
      screen.getByText("Radarr says WEBDL-1080p; Reelkeeper reads Bluray-1080p."),
    ).toBeInTheDocument();

    const sent = await calls.find((c) => c.url.includes("/preview"))?.json();
    expect(sent).toEqual({ url: "http://192.168.1.20:7979", apiKey: "secret-key", profileId: 1 });
  });

  it("says so when Radarr and Reelkeeper agree", async () => {
    open({ "POST /migrate/radarr/preview": json({ ...report, issues: [] }) });
    await fillIn();
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(
      await screen.findByText("Radarr and Reelkeeper agree on every movie."),
    ).toBeInTheDocument();
  });

  it("confirms, imports and shows what was done", async () => {
    const { calls } = open({
      "POST /migrate/radarr/import": json({
        ...report,
        imported: true,
        added: 2,
        monitorChanged: 1,
        historyAdded: 157,
        blocklistAdded: 0,
      }),
    });
    await fillIn();
    expect(screen.queryByRole("button", { name: "Import" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));
    await userEvent.click(await screen.findByRole("button", { name: "Import" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(
      "Bring over 2 wanted movies, monitoring, tags, history and blocklist from Radarr?",
    );
    expect(calls.some((c) => c.url.includes("/migrate/radarr/import"))).toBe(false);
    await userEvent.click(within(dialog).getByRole("button", { name: "Import" }));
    expect(
      await screen.findByText(
        "Added 2 movies, changed monitoring on 1, brought over 157 history records and 0 blocklist entries.",
      ),
    ).toBeInTheDocument();
    const result = screen.getByText(/^Added 2 movies/).closest("div") as HTMLElement;
    expect(within(result).getByRole("link", { name: "Movies" })).toHaveAttribute("href", "/movies");
    expect(within(result).getByRole("link", { name: "Activity → History" })).toBeInTheDocument();
  });

  it("shows Radarr's problem in plain words", async () => {
    open({
      "POST /migrate/radarr/preview": json(
        { code: "radarr", message: "Radarr refused the API key." },
        422,
      ),
    });
    await fillIn();
    await userEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(await screen.findByText("Radarr refused the API key.")).toBeInTheDocument();
  });

  it("is reached from Media Folders", async () => {
    renderApp("/settings/media", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /library/roots": json([]),
      "GET /profiles": json([]),
      "GET /skipped-folders": json([]),
    });
    expect(await screen.findByText("Moving from Radarr?")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Import from Radarr" })).toHaveAttribute(
      "href",
      "/settings/import-radarr",
    );
  });
});
