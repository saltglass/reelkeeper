import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const status = {
  version: "dev",
  goVersion: "go",
  os: "linux",
  arch: "amd64",
  startedAt: new Date().toISOString(),
  uptimeSeconds: 1,
  urlBase: "",
  dataDir: "/config",
  database: { engine: "sqlite", schemaVersion: 2 },
  memory: { heapBytes: 1, totalBytes: 1 },
};

function open(auth: object) {
  renderApp("/system/status", {
    "GET /auth/state": json(auth),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /system/status": json(status),
  });
  return screen.findByRole("button", { name: "Account menu" });
}

describe("account menu", () => {
  it("offers linking single sign-on to a password account", async () => {
    await userEvent.click(await open({ ...signedIn, oidcEnabled: true }));
    expect(
      await screen.findByRole("menuitem", { name: "Link single sign-on" }),
    ).toBeInTheDocument();
  });

  it("says when single sign-on is already linked", async () => {
    await userEvent.click(
      await open({ ...signedIn, oidcEnabled: true, user: { ...signedIn.user, ssoLinked: true } }),
    );
    expect(await screen.findByText("Single sign-on linked")).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Link single sign-on" })).not.toBeInTheDocument();
  });

  it("doesn't offer linking when single sign-on is off", async () => {
    await userEvent.click(await open(signedIn));
    expect(await screen.findByRole("menuitem", { name: "Sign out" })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Link single sign-on" })).not.toBeInTheDocument();
  });
});

describe("navigation", () => {
  it("lists Indexers right after Quality profiles in Settings", async () => {
    open(signedIn);
    await screen.findAllByRole("link", { name: "Indexers" });
    const labels = screen
      .getAllByRole("link")
      .map((l) => l.textContent)
      .filter((t) =>
        ["Media folders", "Quality profiles", "Indexers", "General"].includes(t ?? ""),
      );
    expect(labels.slice(0, 4)).toEqual([
      "Media folders",
      "Quality profiles",
      "Indexers",
      "General",
    ]);
  });
});

describe("jump to", () => {
  const movies = [
    { id: 7, title: "Dune", year: 2021 },
    { id: 8, title: "Arrival", year: 2016 },
  ];
  function openApp() {
    return renderApp("/system/status", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /system/status": json(status),
      "GET /movies": json(movies),
      "GET /series": json([{ id: 3, title: "Severance", year: 2022 }]),
      "GET /movies/7": json({}, 500),
    });
  }

  it("opens with Ctrl+K and closes with Escape", async () => {
    openApp();
    await screen.findByRole("button", { name: "Account menu" });
    await userEvent.keyboard("{Control>}k{/Control}");
    expect(
      await screen.findByRole("combobox", { name: "Jump to a page or movie" }),
    ).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  });

  it("opens from the top bar button and filters pages and titles", async () => {
    openApp();
    await userEvent.click(await screen.findByRole("button", { name: /Jump to a page or movie/ }));
    const box = await screen.findByRole("combobox");
    await userEvent.type(box, "dun");
    expect(await screen.findByRole("option", { name: /Dune \(2021\)/ })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Arrival/ })).not.toBeInTheDocument();
    await userEvent.clear(box);
    await userEvent.type(box, "indexers");
    expect(await screen.findByRole("option", { name: /Indexers/ })).toBeInTheDocument();
    await userEvent.clear(box);
    await userEvent.type(box, "zzzz");
    expect(await screen.findByText("No page or movie by that name.")).toBeInTheDocument();
  });

  it("goes to a movie on Enter", async () => {
    const { router } = openApp();
    await userEvent.click(await screen.findByRole("button", { name: /Jump to a page or movie/ }));
    await userEvent.type(await screen.findByRole("combobox"), "dune");
    await screen.findByRole("option", { name: /Dune/ });
    await userEvent.keyboard("{Enter}");
    await vi.waitFor(() => expect(router.state.location.pathname).toBe("/movies/7"));
  });
});

describe("sidebar counts", () => {
  it("shows how many series there are, not episodes", async () => {
    renderApp("/system/status", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /system/status": json(status),
      "GET /series": json([
        { id: 1, title: "One", episodeCount: 10 },
        { id: 2, title: "Two", episodeCount: 20 },
      ]),
    });
    const link = await screen.findByRole("link", { name: /^Series\s*2$/ });
    expect(link).toBeInTheDocument();
  });

  it("marks the Queue entry when the queue is full, and only then", async () => {
    const queue = (full: boolean) => ({
      items: [
        { id: 1, status: "downloading", torrent: null },
        { id: 2, status: "downloading", torrent: null },
      ],
      clientError: null,
      limit: full ? 2 : 5,
      full,
    });
    for (const full of [true, false]) {
      const view = renderApp("/system/status", {
        "GET /auth/state": json(signedIn),
        "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
        "GET /system/status": json(status),
        "GET /queue": json(queue(full)),
      });
      const link = await screen.findByRole("link", { name: full ? /^Queue\s*Full/ : /^Queue\s*2/ });
      if (full) expect(link).toHaveTextContent("Full");
      else expect(link).not.toHaveTextContent("Full");
      view.unmount();
    }
  });
});

describe("VPN indicator", () => {
  const openWith = (builtinState: string | null) =>
    renderApp("/system/status", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /system/status": json(status),
      "GET /queue": json({
        items: [],
        clientError: null,
        clientName: "the built-in client",
        limit: 0,
        full: false,
        builtinState,
      }),
    });

  it("shows the built-in client's VPN state under the name, linking to its settings", async () => {
    for (const [state, label, detail] of [
      ["connected", "VPN connected", "traffic goes through the VPN"],
      ["connecting", "VPN connecting", "start when the VPN is connected"],
      ["down", "VPN down", "stopped until the VPN reconnects"],
      ["held", "Downloads held", "start them without it"],
    ]) {
      const view = openWith(state as string);
      const link = await screen.findByRole("link", { name: new RegExp(`^${label}`) });
      expect(link).toHaveAttribute("href", "/settings/download-client");
      expect(link).toHaveAccessibleName(expect.stringContaining(detail as string));
      view.unmount();
    }
  });

  it("is hidden without a VPN, and for the other download clients", async () => {
    for (const state of ["unprotected", null]) {
      const view = openWith(state);
      // The sidebar has loaded the queue (it shows the live card) but no indicator.
      expect(await screen.findByText("Nothing downloading")).toBeInTheDocument();
      expect(screen.queryByText(/^VPN|^Downloads held/)).not.toBeInTheDocument();
      view.unmount();
    }
  });
});
