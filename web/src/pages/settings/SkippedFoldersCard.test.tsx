import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const skipped = {
  id: 5,
  rootFolderId: 1,
  rootPath: "/media/movies",
  path: "Dune (2021)",
  title: "Dune (2021)",
  createdAt: "2026-10-09T10:00:00Z",
};

function open(list: unknown[]) {
  let current = list;
  const view = renderApp("/settings/media", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /library/roots": json([]),
    "GET /profiles": json([]),
    "GET /skipped-folders": () => json(current),
    "DELETE /skipped-folders/5": () => {
      current = [];
      return new Response(null, { status: 204 });
    },
  });
  return view;
}

describe("skipped folders card", () => {
  it("is hidden when nothing is skipped", async () => {
    open([]);
    await screen.findByRole("heading", { name: "Media folders" });
    expect(screen.queryByText("Skipped folders")).not.toBeInTheDocument();
  });

  it("lists skipped folders and stops skipping after a confirmation", async () => {
    const { calls } = open([skipped]);
    expect(await screen.findByText("Skipped folders")).toBeInTheDocument();
    expect(screen.getByText("/media/movies/Dune (2021)")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Stop skipping" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("The next scan will add what's in this folder back to Reelkeeper."),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    await userEvent.click(within(dialog).getByRole("button", { name: "Stop skipping" }));
    await vi.waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE" && c.url.endsWith("/skipped-folders/5"))).toBe(
        true,
      ),
    );
    await vi.waitFor(() => expect(screen.queryByText("Skipped folders")).not.toBeInTheDocument());
  });
});
