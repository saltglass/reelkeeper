import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const noTorrentClient = {
  type: "qbittorrent",
  url: "",
  username: "",
  passwordSet: false,
  folder: "",
};

function open(
  usenet: Record<string, unknown> = { type: "sabnzbd", url: "", apiKeySet: false },
  extra: Record<string, Response | ((r: Request) => Response | Promise<Response>)> = {},
) {
  const puts: unknown[] = [];
  const tests: unknown[] = [];
  let saved = usenet;
  renderApp("/settings/download-client", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /settings/download-client": () => json(noTorrentClient),
    "GET /settings/grabbing": () =>
      json({ mode: "dryrun", tvMode: "off", queueLimit: 0, minSeeders: 1 }),
    "GET /settings/usenet-client": () => json(saved),
    "PUT /settings/usenet-client": async (req) => {
      const body = await req.json();
      puts.push(body);
      saved = { type: "sabnzbd", url: body.url, apiKeySet: body.url !== "" };
      return json(saved);
    },
    "POST /settings/usenet-client/test": async (req) => {
      tests.push(await req.json());
      return json({
        version: "5.1.3",
        warnings: [
          "SABnzbd's duplicate detection is on (Config → Switches → Duplicate Detection).",
        ],
      });
    },
    ...extra,
  });
  return { puts, tests };
}

const card = async () =>
  within((await screen.findByText("Usenet: SABnzbd")).closest("form") as HTMLElement);

describe("Usenet client settings", () => {
  it("saves SABnzbd's address and key, and never shows the key", async () => {
    const { puts } = open();
    const c = await card();
    await userEvent.type(c.getByLabelText("Address"), "http://sab:8080");
    await userEvent.type(c.getByLabelText("API key"), "sabkey");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual({ type: "sabnzbd", url: "http://sab:8080", apiKey: "sabkey" });
    // Saved: the key field is empty again, and says one is kept.
    expect(await c.findByPlaceholderText("Saved — leave empty to keep it")).toHaveValue("");
  });

  it("leaves the key out to keep the saved one", async () => {
    const { puts } = open({ type: "sabnzbd", url: "http://sab:8080", apiKeySet: true });
    const c = await card();
    const url = c.getByLabelText("Address");
    await userEvent.clear(url);
    await userEvent.type(url, "http://sabnzbd:8080");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual({ type: "sabnzbd", url: "http://sabnzbd:8080" });
  });

  it("tests the connection and shows what's worth changing in SABnzbd", async () => {
    const { tests } = open({ type: "sabnzbd", url: "http://sab:8080", apiKeySet: true });
    const c = await card();
    await userEvent.click(c.getByRole("button", { name: "Test" }));
    expect(await c.findByText("Connected to SABnzbd 5.1.3")).toBeInTheDocument();
    expect(c.getByText(/duplicate detection is on/)).toBeInTheDocument();
    expect(tests[0]).toEqual({ type: "sabnzbd", url: "http://sab:8080" });
  });

  it("shows why a test failed", async () => {
    open(
      { type: "sabnzbd", url: "http://sab:8080", apiKeySet: true },
      {
        "POST /settings/usenet-client/test": () =>
          json({ code: "usenet_client", message: "SABnzbd refused the API key.", fields: {} }, 422),
      },
    );
    const c = await card();
    await userEvent.click(c.getByRole("button", { name: "Test" }));
    expect(await c.findByText("SABnzbd refused the API key.")).toBeInTheDocument();
  });

  it("lets grabbing go On with only a Usenet client", async () => {
    open({ type: "sabnzbd", url: "http://sab:8080", apiKeySet: true });
    const movies = within(await screen.findByRole("group", { name: "Movies" }));
    await waitFor(() => expect(movies.getByRole("radio", { name: /^On/ })).toBeEnabled());
    expect(screen.queryByText("Set up a download client above first.")).not.toBeInTheDocument();
  });
});
