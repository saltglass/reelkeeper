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
  usenet: Record<string, unknown> = {
    type: "sabnzbd",
    url: "",
    username: "",
    apiKeySet: false,
    passwordSet: false,
  },
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
      const nzbget = body.type === "nzbget";
      saved = {
        type: body.type,
        url: body.url,
        username: body.username ?? "",
        apiKeySet: !nzbget && body.url !== "",
        passwordSet: nzbget && body.url !== "",
      };
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
    const { puts } = open({
      type: "sabnzbd",
      url: "http://sab:8080",
      username: "",
      apiKeySet: true,
      passwordSet: false,
    });
    const c = await card();
    const url = c.getByLabelText("Address");
    await userEvent.clear(url);
    await userEvent.type(url, "http://sabnzbd:8080");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual({ type: "sabnzbd", url: "http://sabnzbd:8080" });
  });

  it("tests the connection and shows what's worth changing in SABnzbd", async () => {
    const { tests } = open({
      type: "sabnzbd",
      url: "http://sab:8080",
      username: "",
      apiKeySet: true,
      passwordSet: false,
    });
    const c = await card();
    await userEvent.click(c.getByRole("button", { name: "Test" }));
    expect(await c.findByText("Connected to SABnzbd 5.1.3")).toBeInTheDocument();
    expect(c.getByText(/duplicate detection is on/)).toBeInTheDocument();
    expect(tests[0]).toEqual({ type: "sabnzbd", url: "http://sab:8080" });
  });

  it("shows why a test failed", async () => {
    open(
      {
        type: "sabnzbd",
        url: "http://sab:8080",
        username: "",
        apiKeySet: true,
        passwordSet: false,
      },
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
    open({
      type: "sabnzbd",
      url: "http://sab:8080",
      username: "",
      apiKeySet: true,
      passwordSet: false,
    });
    const movies = within(await screen.findByRole("group", { name: "Movies" }));
    await waitFor(() => expect(movies.getByRole("radio", { name: /^On/ })).toBeEnabled());
    expect(screen.queryByText("Set up a download client above first.")).not.toBeInTheDocument();
  });

  it("offers SABnzbd and NZBGet, and asks for each one's own login", async () => {
    open();
    const c = await card();
    expect(c.getByRole("button", { name: "SABnzbd", pressed: true })).toBeInTheDocument();
    expect(c.getByLabelText("API key")).toBeInTheDocument();
    expect(c.queryByLabelText("Username")).not.toBeInTheDocument();
    await userEvent.click(c.getByRole("button", { name: "NZBGet" }));
    // The title and the fields follow the choice.
    expect(await screen.findByText("Usenet: NZBGet")).toBeInTheDocument();
    expect(c.getByLabelText("Username")).toBeInTheDocument();
    expect(c.getByLabelText("Password")).toBeInTheDocument();
    expect(c.queryByLabelText("API key")).not.toBeInTheDocument();
    expect(c.getByPlaceholderText("http://192.168.1.10:6789")).toBeInTheDocument();
    expect(c.getByText(/Needs NZBGet 21.0 or newer/)).toBeInTheDocument();
    expect(c.getByText(/Reelkeeper never changes NZBGet's settings/)).toBeInTheDocument();
  });

  it("saves NZBGet's address, username and password, and never shows the password", async () => {
    const { puts } = open();
    const c = await card();
    await userEvent.click(c.getByRole("button", { name: "NZBGet" }));
    await userEvent.type(c.getByLabelText("Address"), "http://nzbget:6789");
    await userEvent.type(c.getByLabelText("Username"), "nzbget");
    await userEvent.type(c.getByLabelText("Password"), "tegbzn6789");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual({
      type: "nzbget",
      url: "http://nzbget:6789",
      username: "nzbget",
      password: "tegbzn6789",
    });
    expect(await c.findByPlaceholderText("Saved — leave empty to keep it")).toHaveValue("");
    expect(screen.getByText("Usenet: NZBGet")).toBeInTheDocument();
  });

  it("leaves the password out to keep the saved one", async () => {
    const { puts } = open({
      type: "nzbget",
      url: "http://nzbget:6789",
      username: "nzbget",
      apiKeySet: false,
      passwordSet: true,
    });
    const c = await within(
      (await screen.findByText("Usenet: NZBGet")).closest("form") as HTMLElement,
    );
    expect(c.getByLabelText("Username")).toHaveValue("nzbget");
    const url = c.getByLabelText("Address");
    await userEvent.clear(url);
    await userEvent.type(url, "http://192.168.1.10:6789");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual({
      type: "nzbget",
      url: "http://192.168.1.10:6789",
      username: "nzbget",
    });
  });

  it("doesn't pretend a saved SABnzbd key carries over to NZBGet", async () => {
    open({
      type: "sabnzbd",
      url: "http://sab:8080",
      username: "",
      apiKeySet: true,
      passwordSet: false,
    });
    const c = await card();
    expect(c.getByPlaceholderText("Saved — leave empty to keep it")).toBeInTheDocument();
    await userEvent.click(c.getByRole("button", { name: "NZBGet" }));
    expect(c.queryByPlaceholderText("Saved — leave empty to keep it")).not.toBeInTheDocument();
    // And back: the saved key is still kept.
    await userEvent.click(c.getByRole("button", { name: "SABnzbd" }));
    expect(c.getByPlaceholderText("Saved — leave empty to keep it")).toBeInTheDocument();
  });

  it("tests NZBGet and names it in the answer and the problems", async () => {
    const tests: unknown[] = [];
    open(
      {
        type: "nzbget",
        url: "http://nzbget:6789",
        username: "nzbget",
        apiKeySet: false,
        passwordSet: true,
      },
      {
        "POST /settings/usenet-client/test": async (req) => {
          tests.push(await req.json());
          return json({ version: "26.3", warnings: ["NZBGet is paused."] });
        },
      },
    );
    const c = await within(
      (await screen.findByText("Usenet: NZBGet")).closest("form") as HTMLElement,
    );
    await userEvent.click(c.getByRole("button", { name: "Test" }));
    expect(await c.findByText("Connected to NZBGet 26.3")).toBeInTheDocument();
    expect(c.getByText("NZBGet is paused.")).toBeInTheDocument();
    // No password typed: the saved one is used.
    expect(tests[0]).toEqual({ type: "nzbget", url: "http://nzbget:6789", username: "nzbget" });
  });

  it("shows what is missing for NZBGet's login", async () => {
    open(undefined, {
      "PUT /settings/usenet-client": () =>
        json(
          {
            code: "invalid",
            message: "Check the highlighted fields.",
            fields: {
              username:
                "Enter NZBGet's username (in NZBGet: Settings → Security → ControlUsername).",
              password:
                "Enter NZBGet's password (in NZBGet: Settings → Security → ControlPassword).",
            },
          },
          422,
        ),
    });
    const c = await card();
    await userEvent.click(c.getByRole("button", { name: "NZBGet" }));
    await userEvent.type(c.getByLabelText("Address"), "http://nzbget:6789");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await c.findByText(/Enter NZBGet's username/)).toBeInTheDocument();
    expect(c.getByText(/Enter NZBGet's password/)).toBeInTheDocument();
  });
});
