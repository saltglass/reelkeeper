import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resetToastsForTests } from "@/hooks/toast";
import { json, renderApp, signedIn } from "@/test/app";
import { blocked, blocklistOf, daysAgo, stuck } from "@/test/blocklist";

afterEach(() => {
  vi.unstubAllGlobals();
  resetToastsForTests();
});

const ronin = { id: 4, title: "Ronin", year: 1998 };
const show = { id: 7, title: "Some Show", year: 2015 };

// Newest first, as the API sends them: two Dune releases, a Ronin one from a
// second tracker, and a series season that nothing is wanted for any more.
const duneStuck = stuck({ id: 5, createdAt: daysAgo(1) });
const duneMine = blocked({ id: 4, createdAt: daysAgo(3) });
const roninGone = stuck({
  id: 3,
  createdAt: daysAgo(5),
  movie: ronin,
  releaseTitle: "Ronin.1998.1080p.BluRay-DEAD",
  indexer: "Tracker B",
  reason: "Cleanup: The tracker says this torrent no longer exists (deleted or replaced).",
  rule: "unregistered",
  expiresAt: null,
  permanent: true,
});
const showSeason = stuck({
  id: 2,
  createdAt: daysAgo(9),
  movie: show,
  mediaType: "series",
  releaseTitle: "Some.Show.S02.WEB-STUCK",
  indexer: "Tracker B",
  episodes: "season 2 (8 wanted episodes)",
  dormant: true,
});
const radarrOld = blocked({
  id: 1,
  createdAt: daysAgo(30),
  movie: show,
  mediaType: "series",
  releaseTitle: "Some.Show.S01.CAM",
  reason: "Blocklisted in Sonarr: Fake",
  rule: "sonarr",
  indexer: "Tracker A",
  dormant: true,
});

const everything = [duneStuck, duneMine, roninGone, showSeason, radarrOld];

function open(extra: Record<string, (r: Request) => Response> = {}, path = "/system/blocklist") {
  const calls: URL[] = [];
  const view = renderApp(path, {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /blocklist": (req) => {
      const url = new URL(req.url);
      calls.push(url);
      // By default the dormant titles' entries are left out.
      const items =
        url.searchParams.get("include") === "dormant"
          ? everything
          : everything.filter((b) => !b.dormant);
      return json({ items, total: everything.length, dormant: 2, temporary: 3 });
    },
    ...extra,
  });
  return { ...view, blocklistCalls: calls };
}

const groupToggle = (name: string) =>
  screen.findByRole("button", { name: `Show blocked releases for ${name}` });

describe("blocklist page", () => {
  it("groups releases by title, collapsed, newest first", async () => {
    open();
    const dune_ = await groupToggle("Dune (2021)");
    expect(dune_).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByRole("link", { name: "Dune (2021)" })).toHaveAttribute("href", "/movies/3");
    expect(screen.getByRole("link", { name: "Ronin (1998)" })).toHaveAttribute("href", "/movies/4");
    const names = screen
      .getAllByRole("link")
      .map((l) => l.textContent)
      .filter((t) => /\(\d{4}\)$/.test(t ?? ""));
    expect(names).toEqual(["Dune (2021)", "Ronin (1998)"]);
    // Nothing is listed under a title until it is opened.
    expect(screen.queryByText("Dune.2021.CAM-FAKE")).not.toBeInTheDocument();
    expect(screen.getByText("2 releases")).toBeInTheDocument();

    await userEvent.click(dune_);
    expect(dune_).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Dune.2021.CAM-FAKE")).toBeInTheDocument();
    expect(screen.getByText("Dune.2021.1080p.WEB-STUCK")).toBeInTheDocument();
    expect(screen.getByText("Permanent")).toBeInTheDocument();
    expect(screen.getByText("Expires in 27 days")).toBeInTheDocument();
  });

  it("explains an empty blocklist", async () => {
    renderApp("/system/blocklist", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /blocklist": json(blocklistOf([])),
    });
    expect(
      await screen.findByText(
        "Nothing blocked. When a download gets stuck or you remove one from the Queue, its release is listed here so Reelkeeper doesn't grab it again.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
  });

  it("searches by title or release name and opens what it finds", async () => {
    open();
    await groupToggle("Dune (2021)");
    await userEvent.type(screen.getByRole("searchbox", { name: "Search the blocklist" }), "dead");
    // The matching release is shown without having to open its title.
    expect(await screen.findByText("Ronin.1998.1080p.BluRay-DEAD")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Dune (2021)" })).not.toBeInTheDocument();

    await userEvent.clear(screen.getByRole("searchbox"));
    await userEvent.type(screen.getByRole("searchbox"), "dune");
    expect(await screen.findByText("Dune.2021.CAM-FAKE")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Ronin (1998)" })).not.toBeInTheDocument();

    await userEvent.clear(screen.getByRole("searchbox"));
    await userEvent.type(screen.getByRole("searchbox"), "nothing like this");
    expect(await screen.findByText("Nothing matches those filters.")).toBeInTheDocument();
  });

  it("filters by reason", async () => {
    open();
    await groupToggle("Dune (2021)");
    const reason = screen.getByRole("combobox", { name: "Reason" });

    await userEvent.selectOptions(reason, "Tracker deleted it");
    expect(screen.getByRole("link", { name: "Ronin (1998)" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Dune (2021)" })).not.toBeInTheDocument();

    await userEvent.selectOptions(reason, "Removed by you");
    expect(screen.getByRole("link", { name: "Dune (2021)" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Ronin (1998)" })).not.toBeInTheDocument();
    // Only the release you removed counts in its group.
    expect(screen.getByText("1 release")).toBeInTheDocument();

    await userEvent.selectOptions(reason, "Stuck downloads");
    expect(screen.getByRole("link", { name: "Dune (2021)" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Ronin (1998)" })).not.toBeInTheDocument();

    await userEvent.selectOptions(reason, "All reasons");
    expect(screen.getByRole("link", { name: "Ronin (1998)" })).toBeInTheDocument();
  });

  it("filters by indexer", async () => {
    open();
    await groupToggle("Dune (2021)");
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Indexer" }), "Tracker B");
    expect(screen.getByRole("link", { name: "Ronin (1998)" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Dune (2021)" })).not.toBeInTheDocument();
  });

  it("hides titles with nothing wanted until you ask, and counts them", async () => {
    const { blocklistCalls } = open();
    await groupToggle("Dune (2021)");
    expect(screen.queryByRole("link", { name: "Some Show (2015)" })).not.toBeInTheDocument();
    expect(blocklistCalls[0]?.searchParams.get("include")).toBeNull();

    const toggle = screen.getByRole("switch", { name: /Show titles with nothing wanted/ });
    expect(screen.getByText("(2)")).toBeInTheDocument();
    await userEvent.click(toggle);

    const group = await groupToggle("Some Show (2015)");
    expect(screen.getByText("Nothing wanted")).toBeInTheDocument();
    expect(blocklistCalls.at(-1)?.searchParams.get("include")).toBe("dormant");
    // The series' release names its episodes.
    await userEvent.click(group);
    expect(screen.getByText("Episodes: season 2 (8 wanted episodes)")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Some Show (2015)" })).toHaveAttribute(
      "href",
      "/series/7",
    );
    // The titles shown before are still there.
    expect(screen.getByRole("link", { name: "Dune (2021)" })).toBeInTheDocument();
  });

  it("unblocks a release from a group", async () => {
    const { calls } = open({
      "DELETE /blocklist/4": () => new Response(null, { status: 204 }),
    });
    await userEvent.click(await groupToggle("Dune (2021)"));
    await userEvent.click(screen.getByRole("button", { name: "Unblock Dune.2021.CAM-FAKE" }));
    expect(await screen.findByText("Unblocked. Reelkeeper may grab it again.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "DELETE" && c.url.endsWith("/blocklist/4"))).toBe(true);
  });

  it("unblocks every temporary entry after confirming", async () => {
    const posts: URL[] = [];
    open({
      "POST /blocklist/unblock-temporary": (req) => {
        posts.push(new URL(req.url));
        return json({ removed: 3 });
      },
    });
    await groupToggle("Dune (2021)");
    await userEvent.click(
      screen.getByRole("button", { name: "Unblock all temporary entries (3)" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText(/3 releases blocked because a download got stuck/),
    ).toBeInTheDocument();
    expect(posts).toHaveLength(0); // nothing happens until it is confirmed

    // Cancel first: still nothing.
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(posts).toHaveLength(0);

    await userEvent.click(
      screen.getByRole("button", { name: "Unblock all temporary entries (3)" }),
    );
    await userEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", { name: "Unblock them" }),
    );
    expect(await screen.findByText("Unblocked 3 releases.")).toBeInTheDocument();
    expect(posts).toHaveLength(1);
    expect(posts[0]?.searchParams.get("titleId")).toBeNull();
  });

  it("says so when everything blocked belongs to titles with nothing wanted", async () => {
    renderApp("/system/blocklist", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /blocklist": json({ items: [], total: 2, dormant: 2, temporary: 1 }),
    });
    expect(
      await screen.findByText(
        /Everything blocked \(2 releases\) is for titles with nothing wanted/,
      ),
    ).toBeInTheDocument();
  });

  it("is in the sidebar under System, not Activity", async () => {
    open();
    await groupToggle("Dune (2021)");
    const nav = screen.getByRole("navigation", { name: /main|sidebar/i });
    const system = within(nav).getByText("System").parentElement as HTMLElement;
    expect(within(system).getByRole("link", { name: /Blocklist/ })).toHaveAttribute(
      "href",
      "/system/blocklist",
    );
    const activity = within(nav).getByText("Activity").parentElement as HTMLElement;
    expect(within(activity).queryByRole("link", { name: /Blocklist/ })).not.toBeInTheDocument();
  });

  it("sends the old Activity address here", async () => {
    const { router } = open({}, "/activity/blocklist");
    await groupToggle("Dune (2021)");
    expect(router.state.location.pathname).toBe("/system/blocklist");
  });
});
