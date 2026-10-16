import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const hd = {
  id: 1,
  name: "HD",
  slug: "hd",
  appliesTo: "both",
  mediaFolders: 2,
  movies: 14,
  series: 0,
};
const shows = { ...hd, id: 2, name: "Shows", slug: "shows", appliesTo: "series" };

const options = {
  presets: [],
  features: [
    { id: "dv", label: "Dolby Vision", group: "picture", canRequire: true },
    { id: "extras", label: "Extras only", group: "release", canRequire: false, help: "Bonus." },
  ],
  tiers: [
    { id: "hdtv", name: "HDTV" },
    { id: "dvd", name: "DVD" },
    { id: "webrip", name: "WEBRip" },
    { id: "webdl", name: "WEB-DL" },
    { id: "bluray", name: "Blu-ray encode" },
    { id: "remux", name: "Remux" },
  ],
  resolutions: [
    { value: 480, label: "SD (480p/576p, DVD)" },
    { value: 720, label: "720p (HD)" },
    { value: 1080, label: "1080p (Full HD)" },
    { value: 2160, label: "2160p (4K)" },
  ],
  languages: ["English", "French"],
  qualities: ["WEBDL-1080p", "Bluray-1080p"],
  choices: ["prefer", "avoid", "require", "block"],
};

const simple = {
  resolutions: [1080],
  lowestSource: "webrip",
  highestSource: "bluray",
  webEqual: true,
  upgrades: true,
  upgradeUntil: { resolution: 1080, source: "bluray" },
  preferPropers: true,
  sizes: { "1080": { min: 0, max: 2.5 } },
  languages: { mode: "original" },
  features: { extras: "block" },
};

const compiled = {
  items: [
    { name: "WEBDL-1080p", allowed: true },
    { name: "Bluray-1080p", allowed: true },
  ],
  upgradesAllowed: true,
  upgradeUntil: "Bluray-1080p",
  upgradeUntilScore: 0,
  minUpgradeStep: 1,
  formats: [
    {
      name: "HDR",
      specifications: [
        {
          name: "HDR10",
          implementation: "ReleaseTitleSpecification",
          negate: false,
          required: false,
          fields: { value: "hdr10" },
        },
      ],
    },
  ],
  scores: { HDR: 500 },
  minScore: 0,
  preferPropers: true,
};

const simpleSettings = { mode: "simple", simple, compiled };

function open(
  id: number,
  extra: Parameters<typeof renderApp>[1] = {},
  settings: unknown = simpleSettings,
) {
  return renderApp(`/settings/profiles/${id}`, {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /profiles": json([hd, shows]),
    "GET /profiles/options": json(options),
    [`GET /profiles/${id}/settings`]: json(settings),
    ...extra,
  });
}

describe("profile settings page", () => {
  it("shows a movie profile's sizes for a 2-hour movie and saves them per hour", async () => {
    const sent: { sizes: Record<string, { min: number; max: number }> }[] = [];
    open(1, {
      "PUT /profiles/1/settings/simple": async (req) => {
        const body = await req.json();
        sent.push(body);
        return json({ ...simpleSettings, simple: body });
      },
    });
    expect(await screen.findByRole("heading", { name: "HD" })).toBeInTheDocument();
    expect(screen.getByText("Simple")).toBeInTheDocument();
    // 2.5 GB per hour is 5 GB for a two-hour movie.
    const max = await screen.findByLabelText("1080p biggest, GB for a 2-hour movie");
    expect(max).toHaveValue(5);
    await userEvent.clear(max);
    await userEvent.type(max, "6");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]?.sizes["1080"]).toEqual({ min: 0, max: 3 });
    expect(await screen.findByText("Saved")).toBeInTheDocument();
  });

  it("shows TV-only sizes per hour of TV", async () => {
    open(2);
    expect(await screen.findByLabelText("1080p biggest, GB per hour of TV")).toHaveValue(2.5);
  });

  it("changes a feature choice and saves it", async () => {
    const sent: { features: Record<string, string> }[] = [];
    open(1, {
      "PUT /profiles/1/settings/simple": async (req) => {
        const body = await req.json();
        sent.push(body);
        return json({ ...simpleSettings, simple: body });
      },
    });
    await userEvent.click(await screen.findByRole("radio", { name: "Dolby Vision: Prefer" }));
    // A feature that can't be required has no Require choice.
    expect(screen.queryByRole("radio", { name: "Extras only: Require" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("radio", { name: "Extras only: Don't care" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]?.features).toEqual({ dv: "prefer" });
  });

  it("shows a validation error next to the setting", async () => {
    open(1, {
      "PUT /profiles/1/settings/simple": json(
        {
          code: "invalid",
          message: "Some fields need attention.",
          fields: { "simple.resolutions": "Choose at least one resolution." },
        },
        422,
      ),
    });
    await userEvent.click(await screen.findByLabelText("1080p (Full HD)"));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Choose at least one resolution.")).toBeInTheDocument();
  });

  it("asks before switching to advanced, then shows the advanced editor", async () => {
    let advanced = false;
    open(1, {
      "GET /profiles/1/settings": () =>
        json(advanced ? { mode: "advanced", simple, compiled } : simpleSettings),
      "POST /profiles/1/settings/switch-to-advanced": () => {
        advanced = true;
        return json({ mode: "advanced", simple, compiled });
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Show advanced" }));
    // The summary shows what the simple settings compile to.
    expect(screen.getByText("Custom formats and their scores")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Edit advanced settings" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText(
        /simple settings no longer apply.*Nothing about what the profile does changes now/,
      ),
    ).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Edit advanced settings" }));
    expect(await screen.findByText(/This profile uses advanced settings/)).toBeInTheDocument();
    expect(screen.getByText("Advanced")).toBeInTheDocument();
    expect(screen.getByLabelText("Score for HDR")).toHaveValue(500);
  });

  it("saves advanced edits and reports a bad import", async () => {
    const sent: { scores: Record<string, number> }[] = [];
    open(
      1,
      {
        "PUT /profiles/1/settings/advanced": async (req) => {
          const body = await req.json();
          sent.push(body);
          return json({ mode: "advanced", simple, compiled: body });
        },
      },
      { mode: "advanced", simple, compiled },
    );
    const score = await screen.findByLabelText("Score for HDR");
    await userEvent.clear(score);
    await userEvent.type(score, "750");
    await userEvent.type(screen.getByLabelText("Import custom format"), "not json");
    await userEvent.click(screen.getByRole("button", { name: "Add format" }));
    expect(await screen.findByText(/isn't valid JSON/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]?.scores).toEqual({ HDR: 750 });
  });

  it("tests a release name and lists the reasons", async () => {
    const sent: unknown[] = [];
    open(1, {
      "POST /profiles/1/test-release": async (req) => {
        sent.push(await req.json());
        return json({
          accepted: false,
          rejections: ["It is 720p, and this profile only accepts 1080p."],
          quality: "Bluray-720p",
          score: 0,
          formats: [],
          upgrade: false,
          parsed: {
            title: "Some Movie",
            year: 2021,
            resolution: 720,
            source: "bluray",
            group: "GRP",
            edition: "",
            languages: [],
            hdr: [],
            audio: [],
            videoCodec: "H.264",
            revision: 0,
          },
        });
      },
    });
    await userEvent.type(
      await screen.findByLabelText("Release name"),
      "Some.Movie.2021.720p.BluRay.x264-GRP",
    );
    await userEvent.type(screen.getByLabelText("Size in GB (optional)"), "4");
    await userEvent.click(screen.getByRole("button", { name: "Test" }));
    expect(await screen.findByText("Would reject")).toBeInTheDocument();
    expect(
      screen.getByText("It is 720p, and this profile only accepts 1080p."),
    ).toBeInTheDocument();
    expect(screen.getByText("Some Movie")).toBeInTheDocument();
    expect(sent).toEqual([{ name: "Some.Movie.2021.720p.BluRay.x264-GRP", sizeGB: 4 }]);
    expect(screen.getByText(/tests the saved settings/)).toBeInTheDocument();
  });
});
