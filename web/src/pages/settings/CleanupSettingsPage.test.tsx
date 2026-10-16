import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const defaults = {
  mode: "dryrun",
  seedRatio: 1,
  seedHours: 24,
  stalledHours: 6,
  slowKBps: 50,
  slowHours: 12,
  metadataHours: 2,
  importFailedDays: 3,
  unregistered: true,
  missingFiles: true,
  emptyReplacedDays: 30,
};

function open(reject = false, type: "qbittorrent" | "transmission" = "qbittorrent") {
  const puts: Record<string, unknown>[] = [];
  renderApp("/settings/cleanup", {
    "GET /settings/download-client": json({
      type,
      url: "http://client:1234",
      username: "u",
      passwordSet: true,
      folder: "",
    }),
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /settings/cleanup": json(defaults),
    "PUT /settings/cleanup": async (req) => {
      const body = await req.json();
      puts.push(body);
      if (reject) {
        return json(
          {
            code: "invalid",
            message: "Some fields need attention.",
            fields: { slowHours: "Set both the speed and how long, or neither." },
          },
          422,
        );
      }
      return json(body);
    },
  });
  return puts;
}

describe("cleanup settings", () => {
  it("names the download client that is set up", async () => {
    open(false, "transmission");
    expect(
      await screen.findByText(
        "How Reelkeeper tidies Transmission after itself: finished torrents, stuck downloads and leftovers.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "24 = 1 day, 168 = 1 week. Time spent waiting in Transmission's queue counts.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("Transmission lost the downloaded files.")).toBeInTheDocument();
    expect(
      screen.getByText("Transmission can't find out what's in the torrent."),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/it is removed from Transmission with what it downloaded/),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Off" }));
    expect(
      screen.getByText("Reelkeeper never removes anything from Transmission."),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "On" }));
    expect(
      screen.getByText(/Reelkeeper removes torrents from Transmission, and what they downloaded/),
    ).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/qBittorrent/);
  });

  it("loads the saved settings", async () => {
    open();
    expect(await screen.findByRole("button", { name: "Dry run" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByLabelText("Ratio")).toHaveValue(1);
    expect(screen.getByLabelText("Hours since it finished")).toHaveValue(24);
    expect(screen.getByLabelText("No progress, hours")).toHaveValue(6);
    expect(screen.getByLabelText("Empty them after (days)")).toHaveValue(30);
    expect(screen.getByRole("switch", { name: "Deleted on the tracker" })).toBeChecked();
  });

  it("saves changes", async () => {
    const puts = open();
    await userEvent.click(await screen.findByRole("button", { name: "On" }));
    const hours = screen.getByLabelText("Hours since it finished");
    await userEvent.clear(hours);
    await userEvent.type(hours, "72");
    await userEvent.click(screen.getByRole("switch", { name: "Files missing" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(puts[0]).toEqual({ ...defaults, mode: "on", seedHours: 72, missingFiles: false });
  });

  it("shows field errors from the server", async () => {
    open(true);
    const hours = await screen.findByLabelText("Slow for, hours");
    await userEvent.clear(hours);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(
      await screen.findByText("Set both the speed and how long, or neither."),
    ).toBeInTheDocument();
  });
});
