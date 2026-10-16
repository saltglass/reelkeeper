import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const stats = { queriesToday: 12, grabsToday: 0, queries30d: 300, failures30d: 0, avgMs: 340 };

const tracker = {
  id: 1,
  name: "SecretSite",
  kind: "cardigann",
  definition: "secretsite",
  definitionName: "SecretSite",
  baseUrl: "https://secret.example",
  enabled: true,
  priority: 25,
  useSolver: false,
  settings: { username: "dan" },
  secretsSet: ["password"],
  categories: [2000, 5000],
  seedRatio: 1,
  seedTimeMinutes: 4320,
  health: { status: "ok", message: "" },
  stats,
  createdAt: "2026-10-01T00:00:00Z",
};
const broken = {
  ...tracker,
  id: 2,
  name: "BrokenSite",
  definitionName: "BrokenSite",
  definition: "broken",
  useSolver: true,
  health: {
    status: "failing",
    message: "Login failed: wrong password. Reelkeeper tries again in 30 minutes.",
  },
  stats: { ...stats, queriesToday: 1, avgMs: 0, failures30d: 4 },
};

const definitions = {
  count: 2,
  definitions: [
    {
      id: "secretsite",
      name: "SecretSite",
      description: "A private movie tracker",
      type: "private",
      language: "en-US",
      links: ["https://secret.example", "https://secret.mirror"],
      custom: false,
    },
    {
      id: "openpub",
      name: "OpenPub",
      description: "A public site",
      type: "public",
      language: "en-US",
      links: ["https://open.example"],
      custom: false,
    },
  ],
};
const secretDetail = {
  ...definitions.definitions[0],
  categories: [],
  login: "form",
  settings: [
    { name: "username", label: "Username", type: "text", default: "", options: [], secret: false },
    {
      name: "password",
      label: "Password",
      type: "password",
      default: "",
      options: [],
      secret: true,
    },
    {
      name: "sort",
      label: "Sort by",
      type: "select",
      default: "created",
      options: [
        { value: "created", label: "Created" },
        { value: "seeders", label: "Seeders" },
      ],
      secret: false,
    },
    {
      name: "freeleech",
      label: "Freeleech only",
      type: "checkbox",
      default: "false",
      options: [],
      secret: false,
    },
    {
      name: "info_cookie",
      label: "Cookie",
      type: "info",
      default: "Use <b>developer</b> tools",
      options: [],
      secret: false,
    },
    {
      name: "notice",
      label: "Heads up",
      type: "info",
      default:
        'Line one<br>Line two <a href="https://x.example/help">the help page</a><script>alert(1)</script>',
      options: [],
      secret: false,
    },
  ],
};

function open(extra: Parameters<typeof renderApp>[1] = {}) {
  return renderApp("/settings/indexers", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /indexers": json([tracker, broken]),
    "GET /indexers/definitions": json(definitions),
    "GET /indexers/definitions/secretsite": json(secretDetail),
    "GET /settings/solver": json({ url: "http://trawl:8191", timeoutSeconds: 60 }),
    ...extra,
  });
}

describe("indexers page", () => {
  it("lists indexers with health, stats and the failure message", async () => {
    open();
    expect(await screen.findByRole("heading", { name: "SecretSite" })).toBeInTheDocument();
    expect(screen.getByText("Working")).toBeInTheDocument();
    expect(await screen.findByText("Private")).toBeInTheDocument();
    expect(screen.getByText("340 ms")).toBeInTheDocument();
    expect(screen.getByText("12")).toBeInTheDocument();
    expect(screen.getByText("Failing")).toBeInTheDocument();
    expect(
      screen.getByText("Login failed: wrong password. Reelkeeper tries again in 30 minutes."),
    ).toBeInTheDocument();
    expect(screen.getByText("4")).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "Enable SecretSite" })).toBeChecked();
  });

  it("explains the empty state", async () => {
    open({ "GET /indexers": json([]) });
    expect(await screen.findByText(/No indexers yet/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add indexer" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Import from Prowlarr" })).toBeInTheDocument();
  });

  it("warns on an indexer that needs the solver when none is set", async () => {
    open({ "GET /settings/solver": json({ url: "", timeoutSeconds: 60 }) });
    expect(
      await screen.findByText(/uses the Cloudflare solver, but no solver address is set/),
    ).toBeInTheDocument();
  });

  it("turns an indexer off, resending its settings without secrets", async () => {
    const sent: unknown[] = [];
    open({
      "PUT /indexers/1": async (req) => {
        sent.push(await req.json());
        return json({ ...tracker, enabled: false });
      },
    });
    await userEvent.click(await screen.findByRole("switch", { name: "Enable SecretSite" }));
    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]).toMatchObject({
      name: "SecretSite",
      kind: "cardigann",
      definition: "secretsite",
      enabled: false,
      settings: { username: "dan" },
      seedRatio: 1,
      seedTimeMinutes: 4320,
    });
    expect(JSON.stringify(sent[0])).not.toContain("password");
  });

  it("tests a saved indexer and shows the result", async () => {
    open({ "POST /indexers/1/test": json({ ok: true, message: "Found 50 releases." }) });
    await userEvent.click(await screen.findByRole("button", { name: "Test SecretSite" }));
    expect(await screen.findByText("Found 50 releases.")).toBeInTheDocument();
  });

  it("adds a tracker: pick, fill in, test, save", async () => {
    const tested: Record<string, unknown>[] = [];
    const saved: Record<string, unknown>[] = [];
    open({
      "POST /indexers/test": async (req) => {
        tested.push(await req.json());
        return json({ ok: true, message: "Logged in; found 20 releases." });
      },
      "POST /indexers": async (req) => {
        saved.push(await req.json());
        return json({ ...tracker, id: 3 });
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Add indexer" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: /^Tracker/ }));
    // Filter the catalogue by text and by type.
    await userEvent.selectOptions(within(dialog).getByLabelText("Type"), "private");
    expect(within(dialog).queryByText("OpenPub")).not.toBeInTheDocument();
    await userEvent.selectOptions(within(dialog).getByLabelText("Type"), "");
    await userEvent.type(within(dialog).getByLabelText("Search trackers"), "pub");
    expect(within(dialog).queryByText("SecretSite")).not.toBeInTheDocument();
    await userEvent.clear(within(dialog).getByLabelText("Search trackers"));
    await userEvent.click(within(dialog).getByRole("button", { name: /SecretSite/ }));

    // The form comes from the definition; the name starts as the tracker's.
    expect(await within(dialog).findByLabelText("Name")).toHaveValue("SecretSite");
    await userEvent.type(within(dialog).getByLabelText("Username"), "dan");
    await userEvent.type(within(dialog).getByLabelText("Password"), "hunter2");
    await userEvent.selectOptions(within(dialog).getByLabelText("Sort by"), "seeders");
    await userEvent.click(within(dialog).getByRole("switch", { name: "Freeleech only" }));
    // Standard help is in our words; other help is plain text, never HTML.
    expect(within(dialog).getByText(/developer tools \(press F12\)/)).toBeInTheDocument();
    expect(within(dialog).getByText(/Line one/)).toHaveTextContent(
      "Line one Line two the help page (https://x.example/help)alert(1)",
    );
    expect(dialog.querySelector("script")).toBeNull();
    // Seed time is typed in hours.
    await userEvent.type(within(dialog).getByLabelText("Minimum ratio"), "1");
    await userEvent.type(within(dialog).getByLabelText("Minimum seed time (hours)"), "72");

    await userEvent.click(within(dialog).getByRole("button", { name: "Test" }));
    expect(await within(dialog).findByText("Logged in; found 20 releases.")).toBeInTheDocument();
    expect(tested[0]).not.toHaveProperty("id");

    await userEvent.click(within(dialog).getByRole("button", { name: "Add indexer" }));
    await waitFor(() => expect(saved).toHaveLength(1));
    expect(saved[0]).toMatchObject({
      name: "SecretSite",
      kind: "cardigann",
      definition: "secretsite",
      enabled: true,
      useSolver: false,
      priority: 25,
      settings: { username: "dan", password: "hunter2", sort: "seeders", freeleech: "true" },
      seedRatio: 1,
      seedTimeMinutes: 4320,
    });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("shows field errors from the server on their fields", async () => {
    open({
      "POST /indexers": json(
        {
          code: "invalid",
          message: "Some fields need attention.",
          fields: {
            name: "An indexer called that already exists.",
            "settings.username": "Username is required.",
            priority: "Priority must be 1 to 50.",
          },
        },
        422,
      ),
    });
    await userEvent.click(await screen.findByRole("button", { name: "Add indexer" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: /^Tracker/ }));
    await userEvent.click(await within(dialog).findByRole("button", { name: /SecretSite/ }));
    await within(dialog).findByLabelText("Name");
    await userEvent.click(within(dialog).getByRole("button", { name: "Add indexer" }));
    expect(
      await within(dialog).findByText("An indexer called that already exists."),
    ).toBeInTheDocument();
    expect(within(dialog).getByText("Username is required.")).toBeInTheDocument();
    // The advanced section opens by itself to show its error.
    expect(within(dialog).getByText("Priority must be 1 to 50.")).toBeInTheDocument();
  });

  it("adds a Torznab feed with an address and API key", async () => {
    const saved: Record<string, unknown>[] = [];
    open({
      "POST /indexers": async (req) => {
        saved.push(await req.json());
        return json({ ...tracker, id: 4, kind: "torznab" });
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Add indexer" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: /Torznab or Newznab feed/ }));
    await userEvent.type(within(dialog).getByLabelText("Name"), "My Jackett");
    await userEvent.type(within(dialog).getByLabelText("Feed address"), "http://jackett/api");
    await userEvent.type(within(dialog).getByLabelText("API key"), "abc123");
    await userEvent.click(within(dialog).getByRole("button", { name: "Add indexer" }));
    await waitFor(() => expect(saved).toHaveLength(1));
    expect(saved[0]).toMatchObject({
      name: "My Jackett",
      kind: "torznab",
      baseUrl: "http://jackett/api",
      settings: { apiKey: "abc123" },
    });
  });

  it("edits an indexer without sending secrets that weren't retyped", async () => {
    const saved: Record<string, unknown>[] = [];
    const tested: Record<string, unknown>[] = [];
    open({
      "POST /indexers/test": async (req) => {
        tested.push(await req.json());
        return json({ ok: false, message: "Login failed." });
      },
      "PUT /indexers/1": async (req) => {
        saved.push(await req.json());
        return json(tracker);
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Edit SecretSite" }));
    const dialog = await screen.findByRole("dialog");
    const password = await within(dialog).findByLabelText("Password");
    expect(password).toHaveValue("");
    expect(password).toHaveAttribute("placeholder", "Saved; leave empty to keep");
    expect(within(dialog).getByLabelText("Username")).toHaveValue("dan");
    // 4320 minutes shows as 72 hours.
    expect(within(dialog).getByLabelText("Minimum seed time (hours)")).toHaveValue(72);

    await userEvent.click(within(dialog).getByRole("button", { name: "Test" }));
    expect(await within(dialog).findByText("Login failed.")).toBeInTheDocument();
    expect(tested[0]).toMatchObject({ id: 1 });
    expect(tested[0]?.settings).not.toHaveProperty("password");

    await userEvent.clear(within(dialog).getByLabelText("Username"));
    await userEvent.type(within(dialog).getByLabelText("Username"), "dan2");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(saved).toHaveLength(1));
    expect(saved[0]?.settings).toEqual({ username: "dan2", sort: "created", freeleech: "false" });
    expect(saved[0]?.seedTimeMinutes).toBe(4320);
  });

  it("deletes after confirming", async () => {
    let deleted = false;
    open({
      "GET /indexers": () => json(deleted ? [broken] : [tracker, broken]),
      "DELETE /indexers/1": () => {
        deleted = true;
        return new Response(null, { status: 204 });
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Delete SecretSite" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Delete SecretSite?")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete indexer" }));
    await waitFor(() => expect(screen.queryByText("SecretSite")).not.toBeInTheDocument());
    expect(deleted).toBe(true);
  });

  it("checks and saves the Cloudflare solver", async () => {
    const saved: unknown[] = [];
    const checked: unknown[] = [];
    open({
      "POST /settings/solver/check": async (req) => {
        checked.push(await req.json());
        return json({ ok: true, message: "TRAWL answered." });
      },
      "PUT /settings/solver": async (req) => {
        const b = await req.json();
        saved.push(b);
        return json(b);
      },
    });
    const url = await screen.findByLabelText("Solver address");
    await waitFor(() => expect(url).toHaveValue("http://trawl:8191"));
    await userEvent.clear(url);
    await userEvent.type(url, "http://solver:8191");
    // The timeout is an advanced setting.
    expect(screen.queryByLabelText("Timeout (seconds)")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Show advanced" }));
    const timeout = screen.getByLabelText("Timeout (seconds)");
    await userEvent.clear(timeout);
    await userEvent.type(timeout, "90");
    await userEvent.click(screen.getByRole("button", { name: "Check" }));
    expect(await screen.findByText("TRAWL answered.")).toBeInTheDocument();
    expect(checked).toEqual([{ url: "http://solver:8191" }]);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(saved).toEqual([{ url: "http://solver:8191", timeoutSeconds: 90 }]));
  });
});

describe("import from Prowlarr", () => {
  const preview = {
    solverUrl: "http://flaresolverr:8191",
    solverTimeoutSeconds: 60,
    indexers: [
      {
        prowlarrId: 11,
        name: "GoodOne",
        kind: "cardigann",
        definition: "goodone",
        enabled: true,
        useSolver: true,
        supported: true,
        reason: "",
        exists: false,
        missingSecrets: ["password"],
        notes: ["Categories were mapped."],
      },
      {
        prowlarrId: 12,
        name: "Usenety",
        kind: "",
        definition: "",
        enabled: true,
        useSolver: false,
        supported: false,
        reason: "Reelkeeper doesn't support this indexer type.",
        exists: false,
        missingSecrets: [],
        notes: [],
      },
      {
        prowlarrId: 13,
        name: "SecretSite",
        kind: "cardigann",
        definition: "secretsite",
        enabled: true,
        useSolver: false,
        supported: true,
        reason: "",
        exists: true,
        missingSecrets: [],
        notes: [],
      },
    ],
  };

  it("previews, imports the chosen ones and adopts the solver address", async () => {
    const previewBodies: unknown[] = [];
    const imports: Record<string, unknown>[] = [];
    open({
      "GET /settings/solver": json({ url: "", timeoutSeconds: 60 }),
      "POST /indexers/import/prowlarr/preview": async (req) => {
        previewBodies.push(await req.json());
        return json(preview);
      },
      "POST /indexers/import/prowlarr": async (req) => {
        imports.push(await req.json());
        return json({
          created: [{ ...tracker, id: 9, name: "GoodOne" }],
          skipped: [],
        });
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Import from Prowlarr" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(
      within(dialog).getByLabelText("Prowlarr's address"),
      "http://192.168.1.21:9696",
    );
    await userEvent.type(within(dialog).getByLabelText("API key"), "pkey");
    await userEvent.click(within(dialog).getByRole("button", { name: "Preview" }));

    expect(await within(dialog).findByLabelText("GoodOne")).toBeChecked();
    expect(within(dialog).getByLabelText("Usenety")).toBeDisabled();
    expect(
      within(dialog).getByText(/Reelkeeper doesn't support this indexer type/),
    ).toBeInTheDocument();
    expect(within(dialog).getByLabelText("SecretSite")).toBeDisabled();
    expect(within(dialog).getByText(/already has an indexer with this name/)).toBeInTheDocument();
    expect(within(dialog).getByText("Uses the solver")).toBeInTheDocument();
    expect(within(dialog).getByText(/enter password after importing/)).toBeInTheDocument();
    expect(
      within(dialog).getByLabelText(
        "Also set Reelkeeper's Cloudflare solver to http://flaresolverr:8191",
      ),
    ).toBeChecked();

    await userEvent.click(within(dialog).getByRole("button", { name: "Import 1 indexer" }));
    expect(await within(dialog).findByText("Imported 1 indexer.")).toBeInTheDocument();
    expect(previewBodies).toEqual([{ url: "http://192.168.1.21:9696", apiKey: "pkey" }]);
    expect(imports).toEqual([
      { url: "http://192.168.1.21:9696", apiKey: "pkey", ids: [11], useSolverSettings: true },
    ]);
    expect(within(dialog).getByText(/GoodOne \(password\)/)).toBeInTheDocument();
  });

  it("doesn't offer the solver address when Reelkeeper already has one", async () => {
    open({
      "POST /indexers/import/prowlarr/preview": json(preview),
    });
    await userEvent.click(await screen.findByRole("button", { name: "Import from Prowlarr" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Prowlarr's address"), "http://p:9696");
    await userEvent.type(within(dialog).getByLabelText("API key"), "k");
    await userEvent.click(within(dialog).getByRole("button", { name: "Preview" }));
    await within(dialog).findByLabelText("GoodOne");
    expect(
      within(dialog).queryByText(/Also set Reelkeeper's Cloudflare solver/),
    ).not.toBeInTheDocument();
  });

  it("shows why Prowlarr couldn't be read", async () => {
    open({
      "POST /indexers/import/prowlarr/preview": json(
        { code: "bad_gateway", message: "Couldn't reach Prowlarr at that address." },
        502,
      ),
    });
    await userEvent.click(await screen.findByRole("button", { name: "Import from Prowlarr" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Prowlarr's address"), "http://p:9696");
    await userEvent.type(within(dialog).getByLabelText("API key"), "k");
    await userEvent.click(within(dialog).getByRole("button", { name: "Preview" }));
    expect(
      await within(dialog).findByText("Couldn't reach Prowlarr at that address."),
    ).toBeInTheDocument();
  });
});
