import { screen } from "@testing-library/react";
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
const ix = (id: number, name: string, health: object) => ({
  id,
  name,
  kind: "knaben",
  definition: "",
  definitionName: "",
  baseUrl: "",
  enabled: true,
  priority: 25,
  useSolver: false,
  settings: {},
  secretsSet: [],
  categories: [],
  health,
  stats: { queriesToday: 0, grabsToday: 0, queries30d: 0, failures30d: 0, avgMs: 0 },
  createdAt: "2026-10-01T00:00:00Z",
});

function open(list: unknown) {
  renderApp("/system/status", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /system/status": json(status),
    "GET /indexers": json(list),
  });
}

describe("status page indexers", () => {
  it("lists failing indexers with their messages and a link to settings", async () => {
    open([
      ix(1, "FineOne", { status: "ok", message: "" }),
      ix(2, "BadOne", { status: "failing", message: "Timed out. Trying again in 10 minutes." }),
    ]);
    expect(await screen.findByText("1 indexer is failing")).toBeInTheDocument();
    expect(screen.getByText("BadOne")).toBeInTheDocument();
    expect(screen.getByText("Timed out. Trying again in 10 minutes.")).toBeInTheDocument();
    expect(screen.queryByText("FineOne")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Settings → Indexers/ })).toHaveAttribute(
      "href",
      "/settings/indexers",
    );
  });

  it("shows no card when everything works", async () => {
    open([ix(1, "FineOne", { status: "ok", message: "" })]);
    await screen.findByText("About");
    expect(screen.queryByText(/indexers? (is|are) failing/)).not.toBeInTheDocument();
  });
});
