import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

/** The page has two forms, but the floating save bar only shows for the one being edited. */
const movies = () => within(screen.getByRole("group", { name: "Movies" }));
const tv = () => within(screen.getByRole("group", { name: "TV" }));

function saveButton(_form: number) {
  return screen.getByRole("button", { name: "Save" });
}

function open(
  mode = "dryrun",
  extra: Record<string, Response | ((r: Request) => Response | Promise<Response>)> = {},
  client: Record<string, unknown> = {
    type: "qbittorrent",
    url: "",
    username: "",
    passwordSet: false,
    folder: "",
  },
) {
  const puts: unknown[] = [];
  let saved = { mode, tvMode: "off", queueLimit: 0, minSeeders: 1 };
  const view = renderApp("/settings/download-client", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /settings/download-client": () => json(client),
    "GET /settings/grabbing": () => json(saved),
    "PUT /settings/grabbing": async (req) => {
      const body = await req.json();
      puts.push(body);
      if (body.minSeeders > 100) {
        return json(
          {
            code: "invalid",
            message: "Check the highlighted fields.",
            fields: { minSeeders: "Use 0 to 100 seeders." },
          },
          422,
        );
      }
      saved = body;
      return json(body);
    },
    ...extra,
  });
  return { ...view, puts };
}

describe("download client settings", () => {
  it("loads the saved mode, and On can't be chosen until a client is saved", async () => {
    open();
    await screen.findByRole("group", { name: "Movies" });
    expect(movies().getByRole("radio", { name: /Dry run/ })).toBeChecked();
    expect(tv().getByRole("radio", { name: /^Off/ })).toBeChecked();
    expect(movies().getByRole("radio", { name: /^On/ })).toBeDisabled();
    expect(tv().getByRole("radio", { name: /^On/ })).toBeDisabled();
  });

  it("saves a changed mode, minimum seeders and queue limit", async () => {
    const { puts } = open();
    await screen.findByRole("group", { name: "Movies" });
    await userEvent.click(movies().getByRole("radio", { name: /^Off/ }));
    await userEvent.click(tv().getByRole("radio", { name: /Dry run/ }));
    const seeders = screen.getByLabelText("Minimum seeders");
    await userEvent.clear(seeders);
    await userEvent.type(seeders, "5");
    // The queue limit is in the main section, not behind "Show advanced".
    expect(screen.queryByRole("button", { name: "Show advanced" })).not.toBeInTheDocument();
    expect(screen.getByLabelText("Queue limit")).toHaveValue(0);
    expect(
      screen.getByText(
        "Grabbing pauses while Activity → Queue holds this many downloads; it resumes as they finish. Seerr requests still go through. 0 means no limit.",
      ),
    ).toBeInTheDocument();
    await userEvent.clear(screen.getByLabelText("Queue limit"));
    await userEvent.type(screen.getByLabelText("Queue limit"), "3");
    await userEvent.click(saveButton(1));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(puts[0]).toEqual({ mode: "off", tvMode: "dryrun", queueLimit: 3, minSeeders: 5 });
  });

  it("shows field errors from the server", async () => {
    open();
    const seeders = await screen.findByLabelText("Minimum seeders");
    await userEvent.clear(seeders);
    await userEvent.type(seeders, "500");
    await userEvent.click(saveButton(1));
    expect(await screen.findByText("Use 0 to 100 seeders.")).toBeInTheDocument();
  });

  it("shows the queue limit's field error", async () => {
    open("dryrun", {
      "PUT /settings/grabbing": () =>
        json(
          {
            code: "invalid",
            message: "Check.",
            fields: { queueLimit: "Use 0 (no limit) to 1000 downloads." },
          },
          422,
        ),
    });
    const limit = await screen.findByLabelText("Queue limit");
    await userEvent.clear(limit);
    await userEvent.type(limit, "5000");
    await userEvent.click(saveButton(1));
    expect(await screen.findByText("Use 0 (no limit) to 1000 downloads.")).toBeInTheDocument();
  });

  it("shows the tvMode field error", async () => {
    open("dryrun", {
      "PUT /settings/grabbing": () =>
        json(
          {
            code: "invalid",
            message: "Check.",
            fields: { tvMode: "Set up a download client first." },
          },
          422,
        ),
    });
    await screen.findByRole("group", { name: "TV" });
    await userEvent.click(tv().getByRole("radio", { name: /Dry run/ }));
    await userEvent.click(saveButton(1));
    expect(await screen.findByText("Set up a download client first.")).toBeInTheDocument();
  });

  it("allows On once a download client is saved", async () => {
    open(
      "dryrun",
      {},
      {
        type: "qbittorrent",
        url: "http://qb:8080",
        username: "admin",
        passwordSet: true,
        folder: "",
      },
    );
    await screen.findByRole("group", { name: "Movies" });
    expect(movies().getByRole("radio", { name: /^On/ })).toBeEnabled();
    expect(tv().getByRole("radio", { name: /^On/ })).toBeEnabled();
    expect(screen.queryByText("Set up the download client above first.")).not.toBeInTheDocument();
  });

  it("keeps On selectable when it is already saved", async () => {
    open("on");
    await screen.findByRole("group", { name: "Movies" });
    expect(movies().getByRole("radio", { name: /^On/ })).toBeEnabled();
  });
});

describe("download client card (qBittorrent)", () => {
  const saved = {
    type: "qbittorrent",
    url: "http://qb:8080",
    username: "admin",
    passwordSet: true,
    folder: "",
  };

  it("loads the saved connection and never shows the password", async () => {
    open("dryrun", {}, saved);
    expect(await screen.findByLabelText("Web UI address")).toHaveValue("http://qb:8080");
    expect(screen.getByLabelText("Username")).toHaveValue("admin");
    const pw = screen.getByLabelText("Password");
    expect(pw).toHaveValue("");
    expect(pw).toHaveAttribute("type", "password");
    expect(pw).toHaveAttribute("placeholder", "Saved — leave empty to keep it");
    expect(
      screen.getByText(
        "Reelkeeper uses its own category, reelkeeper-movies, and never touches other torrents.",
      ),
    ).toBeInTheDocument();
  });

  it("tests the values as typed", async () => {
    const tests: unknown[] = [];
    open(
      "dryrun",
      {
        "POST /settings/download-client/test": async (req) => {
          const body = await req.json();
          tests.push(body);
          return body.password === "bad"
            ? json(
                {
                  code: "download_client",
                  message: "qBittorrent refused the username or password.",
                },
                422,
              )
            : json({ version: "v5.2.2" });
        },
      },
      saved,
    );
    await userEvent.clear(await screen.findByLabelText("Web UI address"));
    await userEvent.type(screen.getByLabelText("Web UI address"), "http://other:8080");
    await userEvent.click(screen.getByRole("button", { name: "Test" }));
    expect(await screen.findByText("Connected to qBittorrent v5.2.2")).toBeInTheDocument();
    // An empty password is left out, so the stored one is used.
    expect(tests[0]).toEqual({
      type: "qbittorrent",
      url: "http://other:8080",
      username: "admin",
      folder: "",
    });

    await userEvent.type(screen.getByLabelText("Password"), "bad");
    await userEvent.click(screen.getByRole("button", { name: "Test" }));
    expect(
      await screen.findByText("qBittorrent refused the username or password."),
    ).toBeInTheDocument();
    expect(tests[1]).toEqual({
      type: "qbittorrent",
      url: "http://other:8080",
      username: "admin",
      password: "bad",
      folder: "",
    });
  });

  it("saves without a password when none is typed, then enables On", async () => {
    const puts: unknown[] = [];
    let client: Record<string, unknown> = {
      type: "qbittorrent",
      url: "",
      username: "",
      passwordSet: false,
      folder: "",
    };
    const base = open(
      "dryrun",
      {
        "GET /settings/download-client": () => json(client),
        "PUT /settings/download-client": async (req) => {
          const body = await req.json();
          puts.push(body);
          client = {
            type: body.type,
            url: body.url,
            username: body.username,
            passwordSet: true,
            folder: body.folder,
          };
          return json(client);
        },
      },
      client,
    );
    expect((await screen.findAllByRole("radio", { name: /^On/ }))[0]).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Web UI address"), "http://qb:8080");
    await userEvent.type(screen.getByLabelText("Username"), "admin");
    await userEvent.type(screen.getByLabelText("Password"), "secret");
    await userEvent.click(saveButton(0));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(puts[0]).toEqual({
      type: "qbittorrent",
      url: "http://qb:8080",
      username: "admin",
      password: "secret",
      folder: "",
    });
    expect((await screen.findAllByRole("radio", { name: /^On/ }))[0]).toBeEnabled();

    // Change only the username: the password is left out and so kept.
    await userEvent.clear(screen.getByLabelText("Username"));
    await userEvent.type(screen.getByLabelText("Username"), "dan");
    await userEvent.click(saveButton(0));
    await vi.waitFor(() => expect(puts).toHaveLength(2));
    expect(puts[1]).toEqual({
      type: "qbittorrent",
      url: "http://qb:8080",
      username: "dan",
      folder: "",
    });
    base.unmount();
  });

  it("loads and saves the download folder", async () => {
    const puts: Record<string, unknown>[] = [];
    open(
      "dryrun",
      {
        "PUT /settings/download-client": async (req) => {
          const body = await req.json();
          puts.push(body);
          return json({
            type: body.type,
            url: body.url,
            username: body.username,
            passwordSet: true,
            folder: body.folder,
          });
        },
      },
      { ...saved, folder: "/mnt/downloads/reelkeeper" },
    );
    const folder = await screen.findByLabelText("Download folder");
    expect(folder).toHaveValue("/mnt/downloads/reelkeeper");
    expect(folder).toHaveAttribute("placeholder", "/mnt/downloads/reelkeeper");
    await userEvent.clear(folder);
    await userEvent.type(folder, "/data/dl");
    await userEvent.click(saveButton(0));
    await vi.waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual({
      type: "qbittorrent",
      url: "http://qb:8080",
      username: "admin",
      folder: "/data/dl",
    });
  });

  it("shows field errors from the server", async () => {
    open("dryrun", {
      "PUT /settings/download-client": json(
        {
          code: "invalid",
          message: "Check the highlighted fields.",
          fields: { url: "Enter qBittorrent's Web UI address, like http://192.168.1.20:8080." },
        },
        422,
      ),
    });
    await userEvent.type(await screen.findByLabelText("Web UI address"), "nonsense");
    await userEvent.click(saveButton(0));
    expect(
      await screen.findByText("Enter qBittorrent's Web UI address, like http://192.168.1.20:8080."),
    ).toBeInTheDocument();
  });
});

describe("choosing the download client", () => {
  const qb = {
    type: "qbittorrent",
    url: "http://qb:8080",
    username: "admin",
    passwordSet: true,
    folder: "",
  };
  const transmissionButton = async () => {
    await screen.findByRole("group", { name: "Download client" });
    return choice().getByRole("button", { name: "Transmission" });
  };
  const choice = () => within(screen.getByRole("group", { name: "Download client" }));

  it("offers qBittorrent and Transmission, with the saved one chosen", async () => {
    open("dryrun", {}, qb);
    await screen.findByLabelText("Web UI address");
    expect(choice().getByRole("button", { name: "qBittorrent" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(choice().getByRole("button", { name: "Transmission" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
    expect(choice().getByRole("button", { name: "Deluge" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
    expect(screen.getByPlaceholderText("http://192.168.1.10:8080")).toBeInTheDocument();
  });

  const delugeButton = async () => {
    await screen.findByRole("group", { name: "Download client" });
    return choice().getByRole("button", { name: "Deluge" });
  };

  it("words the labels and help for Deluge, which has a password and no username", async () => {
    open("dryrun", {}, { ...qb, username: "admin" });
    await userEvent.click(await delugeButton());
    expect(screen.getByLabelText("Web UI address")).toHaveAttribute(
      "placeholder",
      "http://192.168.1.10:8112",
    );
    expect(screen.getByText(/Needs Deluge 2\.1 or newer/, { selector: "p" })).toBeInTheDocument();
    expect(screen.getByText(/the Label plugin turned on/)).toBeInTheDocument();
    expect(
      screen.getByText("The password of Deluge's web UI. It has no username."),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Username")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Password")).toBeInTheDocument();
    expect(
      screen.getByText(/Where Deluge saves Reelkeeper's downloads, as Deluge sees it/),
    ).toBeInTheDocument();
    expect(screen.getByText(/turns off Deluge's ratio limits/)).toBeInTheDocument();
    expect(screen.queryByText(/Reelkeeper uses its own category/)).not.toBeInTheDocument();
    // Back to qBittorrent: its username field returns, with what was typed.
    await userEvent.click(choice().getByRole("button", { name: "qBittorrent" }));
    expect(screen.getByLabelText("Username")).toHaveValue("admin");
  });

  it("tests and saves Deluge with a password only", async () => {
    const tests: unknown[] = [];
    const puts: unknown[] = [];
    open(
      "dryrun",
      {
        "POST /settings/download-client/test": async (req) => {
          tests.push(await req.json());
          return json({ version: "2.2.0" });
        },
        "PUT /settings/download-client": async (req) => {
          const body = await req.json();
          puts.push(body);
          return json({ ...body, passwordSet: true, folder: "" });
        },
      },
      // A username left over from qBittorrent is not sent to Deluge.
      { ...qb, username: "admin", url: "" },
    );
    await userEvent.click(await delugeButton());
    await userEvent.type(screen.getByLabelText("Web UI address"), "http://deluge:8112");
    await userEvent.type(screen.getByLabelText("Password"), "pw");
    await userEvent.click(screen.getByRole("button", { name: "Test" }));
    expect(await screen.findByText("Connected to Deluge 2.2.0")).toBeInTheDocument();
    expect(tests[0]).toEqual({
      type: "deluge",
      url: "http://deluge:8112",
      username: "",
      folder: "",
      password: "pw",
    });
    await userEvent.click(saveButton(0));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(puts[0]).toEqual({
      type: "deluge",
      url: "http://deluge:8112",
      username: "",
      folder: "",
      password: "pw",
    });
  });

  it("words the labels and help for Transmission", async () => {
    open("dryrun");
    await userEvent.click(await transmissionButton());
    const url = screen.getByLabelText("RPC address");
    expect(url).toHaveAttribute("placeholder", "http://192.168.1.10:9091");
    expect(screen.queryByLabelText("Web UI address")).not.toBeInTheDocument();
    expect(
      screen.getByText(/Needs Transmission 4\.0 or newer\./, { selector: "p" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Optional: leave empty if Transmission's RPC login is off."),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Where Transmission saves Reelkeeper's downloads, as Transmission sees it/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Reelkeeper marks its torrents with its own label/),
    ).toBeInTheDocument();
    expect(screen.getByText(/turns off Transmission's seeding limits/)).toBeInTheDocument();
    expect(screen.queryByText(/Reelkeeper uses its own category/)).not.toBeInTheDocument();
    // Back to qBittorrent: its wording returns.
    await userEvent.click(choice().getByRole("button", { name: "qBittorrent" }));
    expect(screen.getByLabelText("Web UI address")).toBeInTheDocument();
    expect(screen.getByText(/Reelkeeper uses its own category/)).toBeInTheDocument();
  });

  it("tests and saves Transmission without a username", async () => {
    const tests: unknown[] = [];
    const puts: unknown[] = [];
    open("dryrun", {
      "POST /settings/download-client/test": async (req) => {
        tests.push(await req.json());
        return json({ version: "4.0.6 (38c164933e)" });
      },
      "PUT /settings/download-client": async (req) => {
        const body = await req.json();
        puts.push(body);
        return json({ ...body, passwordSet: false, folder: "" });
      },
    });
    await userEvent.click(await transmissionButton());
    await userEvent.type(screen.getByLabelText("RPC address"), "http://tm:9091");
    await userEvent.click(screen.getByRole("button", { name: "Test" }));
    expect(
      await screen.findByText("Connected to Transmission 4.0.6 (38c164933e)"),
    ).toBeInTheDocument();
    expect(tests[0]).toEqual({
      type: "transmission",
      url: "http://tm:9091",
      username: "",
      folder: "",
    });
    await userEvent.click(saveButton(0));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(puts[0]).toEqual({
      type: "transmission",
      url: "http://tm:9091",
      username: "",
      folder: "",
    });
  });

  /** The preview the dialog asks about, and what saving sends. */
  const preview = (p: Partial<Record<string, unknown>> = {}) => ({
    total: 5,
    downloading: 2,
    seeding: 3,
    cannotMove: [],
    ...p,
  });

  function switching(
    p: Record<string, unknown>,
    extra: Record<string, Response | ((r: Request) => Response | Promise<Response>)> = {},
  ) {
    const puts: Record<string, unknown>[] = [];
    const view = open(
      "dryrun",
      {
        "GET /settings/download-client/move-preview": () => json(p),
        "PUT /settings/download-client": async (req) => {
          const body = await req.json();
          puts.push(body);
          return json({ ...body, passwordSet: false, folder: "" });
        },
        ...extra,
      },
      qb,
    );
    return { ...view, puts };
  }

  it("asks before moving torrents to the other client, and Move them saves", async () => {
    const { puts } = switching(preview());
    await userEvent.click(await transmissionButton());
    // Saving moves the torrents: the card says so.
    expect(
      await screen.findByText(/Saving moves Reelkeeper's torrents from qBittorrent/),
    ).toBeInTheDocument();
    await userEvent.click(saveButton(0));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText("Move 5 torrents to Transmission?")).toBeInTheDocument();
    expect(
      dialog.getByText(
        "2 are still downloading. Files already downloaded are checked, not downloaded again; nothing is deleted.",
      ),
    ).toBeInTheDocument();
    expect(
      dialog.getByRole("checkbox", { name: /Leave the seeding ones in qBittorrent/ }),
    ).not.toBeChecked();
    // Nothing is sent until the user says so.
    expect(puts).toEqual([]);
    expect(dialog.getByRole("button", { name: "Move them" })).toHaveFocus();
    await userEvent.click(dialog.getByRole("button", { name: "Move them" }));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(puts[0]).toEqual({
      type: "transmission",
      url: "http://qb:8080",
      username: "admin",
      folder: "",
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("Cancel changes nothing", async () => {
    const { puts } = switching(preview());
    await userEvent.click(await transmissionButton());
    await userEvent.click(saveButton(0));
    const dialog = within(await screen.findByRole("dialog"));
    await userEvent.click(dialog.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(puts).toEqual([]);
  });

  it("leaving the seeding ones is a choice, sent with the save", async () => {
    const { puts } = switching(preview());
    await userEvent.click(await transmissionButton());
    await userEvent.click(saveButton(0));
    const dialog = within(await screen.findByRole("dialog"));
    await userEvent.click(
      dialog.getByRole("checkbox", { name: /Leave the seeding ones in qBittorrent/ }),
    );
    // Only the 2 downloads in the Queue move now.
    expect(dialog.getByText("Move 2 torrents to Transmission?")).toBeInTheDocument();
    await userEvent.click(dialog.getByRole("button", { name: "Move them" }));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(puts[0]).toEqual({
      type: "transmission",
      url: "http://qb:8080",
      username: "admin",
      folder: "",
      leaveSeeding: true,
    });
  });

  it("says 'torrent' for one, and offers to switch when only seeding ones are left behind", async () => {
    switching(preview({ total: 1, downloading: 1, seeding: 0 }));
    await userEvent.click(await transmissionButton());
    await userEvent.click(saveButton(0));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText("Move 1 torrent to Transmission?")).toBeInTheDocument();
    expect(dialog.getByText(/^1 is still downloading\./)).toBeInTheDocument();
    // No seeding ones: no choice to make.
    expect(dialog.queryByRole("checkbox")).not.toBeInTheDocument();
  });

  it("with only seeding torrents and 'leave' ticked there is nothing to move", async () => {
    const { puts } = switching(preview({ total: 2, downloading: 0, seeding: 2 }));
    await userEvent.click(await transmissionButton());
    await userEvent.click(saveButton(0));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText("Move 2 torrents to Transmission?")).toBeInTheDocument();
    await userEvent.click(dialog.getByRole("checkbox", { name: /Leave the seeding ones/ }));
    expect(dialog.getByText("Switch to Transmission?")).toBeInTheDocument();
    expect(
      dialog.getByText(/Reelkeeper will stop tracking the 2 torrents seeding in qBittorrent/),
    ).toBeInTheDocument();
    await userEvent.click(dialog.getByRole("button", { name: "Switch" }));
    await screen.findByText("Saved");
    expect(puts[0]).toMatchObject({ leaveSeeding: true });
  });

  it("lists the torrents that can't move, with why", async () => {
    switching(
      preview({
        cannotMove: [
          { release: "Old.Film.2019.1080p", reason: "It is a private torrent.", seeding: false },
          { release: "Seeding.Film.2018", reason: "It is a private torrent.", seeding: true },
        ],
      }),
    );
    await userEvent.click(await transmissionButton());
    await userEvent.click(saveButton(0));
    const dialog = within(await screen.findByRole("dialog"));
    // 5 to move, less the 2 that can't.
    expect(dialog.getByText("Move 3 torrents to Transmission?")).toBeInTheDocument();
    expect(dialog.getByText("These can't move and stay where they are:")).toBeInTheDocument();
    expect(dialog.getByText("Old.Film.2019.1080p")).toBeInTheDocument();
    expect(
      dialog.getByText(/It is stopped in qBittorrent and Reelkeeper searches for it again\./),
    ).toBeInTheDocument();
    expect(dialog.getByText(/It keeps seeding in qBittorrent, untracked\./)).toBeInTheDocument();
    // Leaving the seeding ones hides the seeding one from the list.
    await userEvent.click(dialog.getByRole("checkbox", { name: /Leave the seeding ones/ }));
    expect(dialog.queryByText("Seeding.Film.2018")).not.toBeInTheDocument();
    expect(dialog.getByText("Old.Film.2019.1080p")).toBeInTheDocument();
    expect(dialog.getByText("This one can't move and stays where it is:")).toBeInTheDocument();
    expect(dialog.getByText("Move 1 torrent to Transmission?")).toBeInTheDocument();
    expect(dialog.getByText(/^1 is still downloading\./)).toBeInTheDocument();
  });

  it("says when none of them can move", async () => {
    switching(
      preview({
        total: 1,
        downloading: 1,
        seeding: 0,
        cannotMove: [
          { release: "Old.Film.2019.1080p", reason: "It is a private torrent.", seeding: false },
        ],
      }),
    );
    await userEvent.click(await transmissionButton());
    await userEvent.click(saveButton(0));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText("Switch to Transmission?")).toBeInTheDocument();
    expect(dialog.getByText(/None of Reelkeeper's torrents can move\./)).toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "Switch" })).toBeInTheDocument();
  });

  it("says so when the old client couldn't be asked", async () => {
    switching(preview({ clientError: "Can't reach qBittorrent: refused." }));
    await userEvent.click(await transmissionButton());
    await userEvent.click(saveButton(0));
    expect(
      await screen.findByText(
        /Reelkeeper couldn't ask qBittorrent which torrents can move: Can't reach qBittorrent: refused\./,
      ),
    ).toBeInTheDocument();
  });

  it("saves straight away when there is nothing to move", async () => {
    const { puts } = switching(preview({ total: 0, downloading: 0, seeding: 0 }));
    await userEvent.click(await transmissionButton());
    await userEvent.click(saveButton(0));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(puts[0]).toEqual({
      type: "transmission",
      url: "http://qb:8080",
      username: "admin",
      folder: "",
    });
  });

  it("doesn't ask when the same kind of client is saved", async () => {
    const asked: string[] = [];
    open(
      "dryrun",
      {
        "GET /settings/download-client/move-preview": () => {
          asked.push("preview");
          return json(preview());
        },
        "PUT /settings/download-client": async (req) =>
          json({ ...(await req.json()), passwordSet: true, folder: "" }),
      },
      qb,
    );
    await userEvent.clear(await screen.findByLabelText("Username"));
    await userEvent.type(screen.getByLabelText("Username"), "dan");
    await userEvent.click(saveButton(0));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(asked).toEqual([]);
  });

  it("shows the server's refusal after the dialog closes", async () => {
    switching(preview(), {
      "PUT /settings/download-client": json(
        {
          code: "download_client",
          message:
            "Reelkeeper can't move the torrents: Transmission can't be used yet (Transmission refused the username or password).",
        },
        422,
      ),
    });
    await userEvent.click(await transmissionButton());
    await userEvent.click(saveButton(0));
    const dialog = within(await screen.findByRole("dialog"));
    await userEvent.click(dialog.getByRole("button", { name: "Move them" }));
    expect(await screen.findByText(/Transmission can't be used yet/)).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});

describe("the built-in client", () => {
  const empty = { type: "qbittorrent", url: "", username: "", passwordSet: false, folder: "" };
  const own = {
    type: "builtin",
    url: "",
    username: "",
    passwordSet: false,
    folder: "/downloads",
    listenPort: 51413,
    maxActiveDownloads: 4,
    maxActiveSeeds: 0,
    downloadLimitKBs: 750,
    uploadLimitKBs: 0,
  };
  const choice = () => within(screen.getByRole("group", { name: "Download client" }));
  const builtinButton = async () => {
    await screen.findByRole("group", { name: "Download client" });
    return choice().getByRole("button", { name: /^Built-in/ });
  };

  it("is a fourth choice, marked Experimental", async () => {
    open("dryrun", {}, empty);
    const button = await builtinButton();
    expect(within(button).getByText("Experimental")).toBeInTheDocument();
    // The others have no badge.
    expect(
      within(choice().getByRole("button", { name: "Deluge" })).queryByText("Experimental"),
    ).not.toBeInTheDocument();
    expect(button).toHaveAttribute("aria-pressed", "false");
  });

  it("hides the address and login, and shows the folder, port and queue sizes", async () => {
    open("dryrun", {}, empty);
    await userEvent.click(await builtinButton());
    expect(screen.queryByLabelText("Web UI address")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Username")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
    expect(
      screen.getByText(
        "Reelkeeper downloads torrents itself, with no separate app. Experimental: it works, but it's new. A VPN option comes next.",
      ),
    ).toBeInTheDocument();
    // The starting values.
    expect(screen.getByLabelText("Download folder")).toHaveValue("");
    expect(screen.getByLabelText("Listen port")).toHaveValue(6881);
    expect(screen.getByLabelText("Max active downloads")).toHaveValue(3);
    expect(screen.getByLabelText("Max active seeding torrents")).toHaveValue(25);
    expect(screen.getByText(/Publish it from the container/)).toBeInTheDocument();
    expect(
      screen.getByText(/without it, Reelkeeper can still download but finds fewer peers/),
    ).toBeInTheDocument();
    // The speed limits are behind "Show advanced".
    expect(screen.queryByLabelText("Download speed limit (KB/s)")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Show advanced" }));
    expect(screen.getByLabelText("Download speed limit (KB/s)")).toHaveValue(0);
    expect(screen.getByLabelText("Upload speed limit (KB/s)")).toHaveValue(0);
    await userEvent.click(screen.getByRole("button", { name: "Hide advanced" }));
    expect(screen.queryByLabelText("Upload speed limit (KB/s)")).not.toBeInTheDocument();
    // Back to another client: its address and login return.
    await userEvent.click(choice().getByRole("button", { name: "qBittorrent" }));
    expect(screen.getByLabelText("Web UI address")).toBeInTheDocument();
    expect(screen.queryByLabelText("Listen port")).not.toBeInTheDocument();
  });

  it("tests and saves with the folder, port and limits, and no address or login", async () => {
    const tests: unknown[] = [];
    const puts: unknown[] = [];
    open(
      "dryrun",
      {
        "POST /settings/download-client/test": async (req) => {
          tests.push(await req.json());
          return json({ version: "Built-in (anacrolix/torrent v1.61.0)" });
        },
        "PUT /settings/download-client": async (req) => {
          const body = await req.json();
          puts.push(body);
          return json({ ...own, ...body, passwordSet: false });
        },
      },
      empty,
    );
    await userEvent.click(await builtinButton());
    // Nothing to test until there is a folder.
    expect(screen.getByRole("button", { name: "Test" })).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Download folder"), "/downloads");
    await userEvent.clear(screen.getByLabelText("Listen port"));
    await userEvent.type(screen.getByLabelText("Listen port"), "51413");
    await userEvent.clear(screen.getByLabelText("Max active seeding torrents"));
    await userEvent.type(screen.getByLabelText("Max active seeding torrents"), "10");
    await userEvent.click(screen.getByRole("button", { name: "Show advanced" }));
    await userEvent.clear(screen.getByLabelText("Download speed limit (KB/s)"));
    await userEvent.type(screen.getByLabelText("Download speed limit (KB/s)"), "750");

    await userEvent.click(screen.getByRole("button", { name: "Test" }));
    expect(
      await screen.findByText(
        /The folder and port are fine\. Built-in \(anacrolix\/torrent v1\.61\.0\)/,
      ),
    ).toBeInTheDocument();
    const want = {
      type: "builtin",
      url: "",
      username: "",
      folder: "/downloads",
      listenPort: 51413,
      maxActiveDownloads: 3,
      maxActiveSeeds: 10,
      downloadLimitKBs: 750,
      uploadLimitKBs: 0,
    };
    expect(tests[0]).toEqual(want);

    await userEvent.click(saveButton(0));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    // No move dialog: there was no download client before.
    expect(puts[0]).toEqual(want);
    // Saved, the built-in client counts as set up: grabbing can be turned On.
    expect((await screen.findAllByRole("radio", { name: /^On/ }))[0]).toBeEnabled();
  });

  it("loads what was saved and shows the server's field errors", async () => {
    open(
      "dryrun",
      {
        "PUT /settings/download-client": json(
          {
            code: "invalid",
            message: "Check the highlighted fields.",
            fields: {
              folder: "/nope doesn't exist inside Reelkeeper's container.",
              listenPort: "Enter a port from 1024 to 65535.",
              uploadLimitKBs: "Enter a speed in KB/s, or 0 for no limit.",
            },
          },
          422,
        ),
        "POST /settings/download-client/test": json(
          { code: "download_client", message: "The folder can't be written to." },
          422,
        ),
      },
      own,
    );
    expect(await screen.findByLabelText("Download folder")).toHaveValue("/downloads");
    expect(screen.getByLabelText("Listen port")).toHaveValue(51413);
    expect(screen.getByLabelText("Max active downloads")).toHaveValue(4);
    expect(screen.getByLabelText("Max active seeding torrents")).toHaveValue(0);
    expect(
      within(screen.getByRole("group", { name: "Download client" })).getByRole("button", {
        name: /^Built-in/,
      }),
    ).toHaveAttribute("aria-pressed", "true");
    // An error under the speed limits opens them.
    await userEvent.type(screen.getByLabelText("Download folder"), "x");
    await userEvent.click(saveButton(0));
    expect(await screen.findByText("Enter a port from 1024 to 65535.")).toBeInTheDocument();
    expect(screen.getByText(/doesn't exist inside Reelkeeper's container/)).toBeInTheDocument();
    expect(screen.getByText("Enter a speed in KB/s, or 0 for no limit.")).toBeInTheDocument();
    expect(screen.getByLabelText("Upload speed limit (KB/s)")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Test" }));
    expect(await screen.findByText("The folder can't be written to.")).toBeInTheDocument();
  });

  it("asks before moving torrents away from it, without offering to leave seeding ones in it", async () => {
    open(
      "dryrun",
      {
        "GET /settings/download-client/move-preview": () =>
          json({ total: 2, downloading: 1, seeding: 1, cannotMove: [] }),
        "PUT /settings/download-client": async (req) =>
          json({ ...(await req.json()), passwordSet: false, folder: "" }),
      },
      own,
    );
    await screen.findByLabelText("Listen port");
    await userEvent.click(choice().getByRole("button", { name: "Transmission" }));
    expect(
      await screen.findByText(/Saving moves Reelkeeper's torrents from Built-in to Transmission/),
    ).toBeInTheDocument();
    expect(screen.getByText(/stop when it is switched off/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("RPC address"), "http://tm:9091");
    await userEvent.click(saveButton(0));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText("Move 2 torrents to Transmission?")).toBeInTheDocument();
    // The built-in client stops when it is switched off, so there is no
    // finishing to do there: its seeding torrents always move.
    expect(dialog.queryByRole("checkbox")).not.toBeInTheDocument();
  });
});
