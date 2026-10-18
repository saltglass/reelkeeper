import { fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn } from "@/test/app";
import { ago, statusText } from "./BuiltinStatusCard";

afterEach(() => vi.unstubAllGlobals());

// A config as a provider hands it out, with made-up keys. The page must never
// show the private key, and only ever sends the file text when saving.
const PRIVATE = "kM3jv0xqT9sL2uYw8aGc5dRz1bNf7HeVoPiXtQ4yJ6A=";
const configText = `[Interface]\nPrivateKey = ${PRIVATE}\nAddress = 10.2.0.2/32\nDNS = 10.2.0.1\n\n[Peer]\nPublicKey = SERVERKEYSERVERKEYSERVERKEYSERVERKEYSERV=\nAllowedIPs = 0.0.0.0/0\nEndpoint = 203.0.113.9:51820\n`;

const summary = {
  endpoint: "203.0.113.9:51820",
  tunnelAddresses: ["10.2.0.2/32"],
  dns: ["10.2.0.1"],
  dnsDefault: false,
  publicKey: "PUBLICKEYPUBLICKEYPUBLICKEYPUBLICKEYPUBL=",
  keepalive: 25,
  mtu: 1420,
  hasPresharedKey: false,
  warnings: [],
  notes: [],
};

const base = {
  type: "builtin",
  url: "",
  username: "",
  passwordSet: false,
  folder: "/downloads",
  listenPort: 6881,
  maxActiveDownloads: 3,
  maxActiveSeeds: 25,
  downloadLimitKBs: 0,
  uploadLimitKBs: 0,
  forwardedPort: 0,
  hasVpn: false,
  vpnHeld: false,
  vpn: null,
  vpnProblem: null,
};

const withVpn = { ...base, hasVpn: true, vpn: summary, forwardedPort: 51413 };

const noStatus = json({ code: "not_found", message: "no" }, 404);

type Handler = Response | ((r: Request) => Response | Promise<Response>);

function open(client: Record<string, unknown>, extra: Record<string, Handler> = {}) {
  return renderApp("/settings/download-client", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /settings/download-client": () => json(client),
    "GET /settings/grabbing": json({ mode: "off", tvMode: "off", queueLimit: 0, minSeeders: 1 }),
    "GET /settings/download-client/builtin/status": noStatus,
    ...extra,
  });
}

const vpnCheck = (calls: string[] = []): Record<string, Handler> => ({
  "POST /settings/download-client/vpn/check": async (req) => {
    const body = await req.json();
    calls.push(body.config);
    return body.config.includes("PrivateKey")
      ? json(summary)
      : json(
          {
            code: "invalid",
            message: "That isn't a WireGuard config Reelkeeper can use.",
            fields: {
              config:
                "This doesn't look like a WireGuard config: it has no [Interface] section. Download the WireGuard (not OpenVPN) file from your VPN provider.",
            },
          },
          422,
        );
  },
});

const saveButton = () => screen.getByRole("button", { name: "Save" });

describe("the VPN section", () => {
  it("offers a drop zone, upload and paste with no VPN, and the listen port instead of a forwarded port", async () => {
    open(base);
    expect(await screen.findByText("Drop a .conf file here")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Upload file" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Paste config" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "VPN (WireGuard)" })).toBeInTheDocument();
    // Unprotected: the listen port, no forwarded port.
    expect(screen.getByLabelText("Listen port")).toBeInTheDocument();
    expect(screen.queryByLabelText("Forwarded port")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Replace" })).not.toBeInTheDocument();
  });

  it("shows nothing of the VPN section for the other clients", async () => {
    open({ ...base, type: "qbittorrent", url: "http://qb:8080", username: "a", folder: "" });
    await screen.findByLabelText("Web UI address");
    expect(screen.queryByText("VPN (WireGuard)")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Forwarded port")).not.toBeInTheDocument();
  });

  it("checks a pasted config, shows what is in it (no private key), and saves it with the form", async () => {
    const checks: string[] = [];
    const puts: Record<string, unknown>[] = [];
    let saved: Record<string, unknown> = base;
    const view = open(saved, {
      ...vpnCheck(checks),
      "GET /settings/download-client": () => json(saved),
      "PUT /settings/download-client": async (req) => {
        const body = await req.json();
        puts.push(body);
        saved = { ...base, hasVpn: true, vpn: summary, forwardedPort: body.forwardedPort };
        return json(saved);
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Paste config" }));
    // Nothing to use until something is pasted.
    expect(screen.getByRole("button", { name: "Use this config" })).toBeDisabled();
    await userEvent.click(screen.getByLabelText("WireGuard config"));
    await userEvent.paste(configText);
    await userEvent.click(screen.getByRole("button", { name: "Use this config" }));
    expect(await screen.findByText("203.0.113.9:51820")).toBeInTheDocument();
    expect(checks).toEqual([configText]);
    expect(screen.getByText("10.2.0.2/32")).toBeInTheDocument();
    expect(screen.getByText("10.2.0.1")).toBeInTheDocument();
    expect(screen.getByText("PUBLICKEYPUBLICKEYPUBLICKEYPUBLICKEYPUBL=")).toBeInTheDocument();
    expect(screen.getByText(/New config, not saved yet/)).toBeInTheDocument();
    // The private key is nowhere on the page.
    expect(document.body.textContent).not.toContain(PRIVATE);
    expect(document.body.innerHTML).not.toContain(PRIVATE);
    // With a config the forwarded port replaces the listen port.
    expect(screen.queryByLabelText("Listen port")).not.toBeInTheDocument();
    const port = screen.getByLabelText("Forwarded port");
    expect(port).toHaveValue(null);
    expect(
      screen.getByText(
        "If your VPN provider gives you a forwarded port, enter it so peers can connect to you. Leave empty for outgoing connections only.",
      ),
    ).toBeInTheDocument();
    await userEvent.type(port, "51413");
    // Nothing is sent until the form is saved.
    expect(puts).toEqual([]);
    await userEvent.click(saveButton());
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(puts[0]).toEqual({
      type: "builtin",
      url: "",
      username: "",
      folder: "/downloads",
      listenPort: 6881,
      maxActiveDownloads: 3,
      maxActiveSeeds: 25,
      downloadLimitKBs: 0,
      uploadLimitKBs: 0,
      forwardedPort: 51413,
      vpnConfig: configText,
    });
    // Saved: the "not saved yet" note goes, the summary stays, the config is
    // never sent again unless replaced.
    expect(screen.queryByText(/New config, not saved yet/)).not.toBeInTheDocument();
    expect(screen.getByText("203.0.113.9:51820")).toBeInTheDocument();
    await userEvent.clear(screen.getByLabelText("Forwarded port"));
    await userEvent.type(screen.getByLabelText("Forwarded port"), "60000");
    await userEvent.click(saveButton());
    await vi.waitFor(() => expect(puts).toHaveLength(2));
    expect(puts[1]).not.toHaveProperty("vpnConfig");
    expect(puts[1]).toHaveProperty("forwardedPort", 60000);
    view.unmount();
  });

  it("takes an uploaded file, and shows why a file that isn't a WireGuard config is refused", async () => {
    const checks: string[] = [];
    open(base, vpnCheck(checks));
    await screen.findByText("Drop a .conf file here");
    const input = screen.getByLabelText("WireGuard config file");
    await userEvent.upload(input, new File(["client\ndev tun\n"], "client.conf"));
    expect(await screen.findByText(/it has no \[Interface\] section/)).toBeInTheDocument();
    // Still no VPN, so no save bar for it.
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
    await userEvent.upload(input, new File([configText], "wg0.conf"));
    expect(await screen.findByText("203.0.113.9:51820")).toBeInTheDocument();
    expect(checks).toEqual(["client\ndev tun\n", configText]);
    expect(screen.queryByText(/it has no \[Interface\] section/)).not.toBeInTheDocument();
    expect(saveButton()).toBeInTheDocument();
  });

  it("takes a file dropped on the zone", async () => {
    const checks: string[] = [];
    open(base, vpnCheck(checks));
    const zone = await screen.findByTestId("vpn-drop");
    fireEvent.dragOver(zone);
    fireEvent.drop(zone, { dataTransfer: { files: [new File([configText], "wg0.conf")] } });
    expect(await screen.findByText("203.0.113.9:51820")).toBeInTheDocument();
    expect(checks).toEqual([configText]);
  });

  it("lists the warnings and notes of a config", async () => {
    open(base, {
      "POST /settings/download-client/vpn/check": json({
        ...summary,
        warnings: [
          "This config only sends some traffic through the VPN; Reelkeeper sends all of the built-in client's traffic through it anyway.",
        ],
        notes: ["Ignored PostUp: Reelkeeper keeps the VPN inside the app."],
        dnsDefault: true,
        dns: ["1.1.1.1"],
      }),
    });
    await userEvent.click(await screen.findByRole("button", { name: "Paste config" }));
    await userEvent.click(screen.getByLabelText("WireGuard config"));
    await userEvent.paste(configText);
    await userEvent.click(screen.getByRole("button", { name: "Use this config" }));
    expect(await screen.findByText(/only sends some traffic through the VPN/)).toBeInTheDocument();
    expect(screen.getByText(/Ignored PostUp/)).toBeInTheDocument();
    expect(
      screen.getByText(/the file names none, so this one is asked, through the VPN/),
    ).toBeInTheDocument();
  });

  it("shows a saved VPN with its forwarded port, and no listen port", async () => {
    open(withVpn);
    expect(await screen.findByText("203.0.113.9:51820")).toBeInTheDocument();
    expect(screen.getByLabelText("Forwarded port")).toHaveValue(51413);
    expect(screen.queryByLabelText("Listen port")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Replace" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove" })).toBeInTheDocument();
    // No drop zone until Replace.
    expect(screen.queryByText("Drop a .conf file here")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Replace" }));
    expect(screen.getByText("Drop a .conf file here")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Replace" }));
    expect(screen.queryByText("Drop a .conf file here")).not.toBeInTheDocument();
  });

  it("replaces the saved VPN with a new config, which can be discarded", async () => {
    open(withVpn, vpnCheck());
    await userEvent.click(await screen.findByRole("button", { name: "Replace" }));
    await userEvent.upload(
      screen.getByLabelText("WireGuard config file"),
      new File([configText], "new.conf"),
    );
    expect(await screen.findByText(/New config, not saved yet/)).toBeInTheDocument();
    expect(saveButton()).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Discard new config" }));
    expect(screen.queryByText(/New config, not saved yet/)).not.toBeInTheDocument();
    expect(screen.getByText("203.0.113.9:51820")).toBeInTheDocument();
  });

  it("asks before removing the VPN, and says what happens", async () => {
    const puts: Record<string, unknown>[] = [];
    open(withVpn, {
      "PUT /settings/download-client": async (req) => {
        const body = await req.json();
        puts.push(body);
        return json({ ...base, vpnHeld: true });
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Remove" }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText("Remove the VPN?")).toBeInTheDocument();
    expect(dialog.getByText(/Peers will see your real IP address/)).toBeInTheDocument();
    expect(
      dialog.getByText(/downloads stop until you press "Start without VPN"/),
    ).toBeInTheDocument();
    // Cancelling sends nothing.
    await userEvent.click(dialog.getByRole("button", { name: "Cancel" }));
    expect(puts).toEqual([]);
    await userEvent.click(screen.getByRole("button", { name: "Remove" }));
    await userEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", { name: "Remove VPN" }),
    );
    await vi.waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual({
      type: "builtin",
      url: "",
      username: "",
      folder: "/downloads",
      vpnConfig: "",
      confirmUnprotected: true,
    });
    // The VPN is gone: the drop zone and the listen port are back.
    expect(await screen.findByText("Drop a .conf file here")).toBeInTheDocument();
    expect(screen.getByLabelText("Listen port")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("shows why a saved VPN can't be used", async () => {
    open({
      ...base,
      hasVpn: true,
      vpnProblem:
        "The saved VPN config can't be read, probably because Reelkeeper's secret key changed. Upload the VPN config again.",
    });
    expect(await screen.findByText(/The saved VPN config can't be read/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Replace" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove" })).toBeInTheDocument();
  });

  it("tells the server's forwarded port and confirmation errors where they belong", async () => {
    open(withVpn, {
      "PUT /settings/download-client": json(
        {
          code: "invalid",
          message: "Some fields need attention.",
          fields: {
            forwardedPort:
              "Enter the port your VPN provider forwards to you (1 to 65535), or 0 for none.",
          },
        },
        422,
      ),
    });
    const port = await screen.findByLabelText("Forwarded port");
    await userEvent.clear(port);
    await userEvent.type(port, "9");
    await userEvent.click(saveButton());
    expect(
      await screen.findByText(/Enter the port your VPN provider forwards to you/),
    ).toBeInTheDocument();
  });
});

const status = (over: Record<string, unknown> = {}) => ({
  state: "connected",
  message: "",
  since: "2026-10-17T20:00:00Z",
  lastHandshake: new Date(Date.now() - 23_000).toISOString(),
  endpoint: "203.0.113.9:51820",
  tunnelAddresses: ["10.2.0.2/32"],
  dns: ["10.2.0.1"],
  dnsDefault: false,
  rxBytes: 1000,
  txBytes: 500,
  forwardedPort: 0,
  publicIP: { ip: "198.51.100.9", country: "US" },
  publicIPError: "",
  vpnIP: { ip: "203.0.113.7", country: "NL" },
  vpnIPError: "",
  leak: false,
  checkedAt: "2026-10-17T20:00:00Z",
  ...over,
});

describe("the VPN status card", () => {
  const card = () => screen.findByRole("region", { name: "VPN status" });

  it("is green and says which address peers see while connected", async () => {
    open(withVpn, { "GET /settings/download-client/builtin/status": () => json(status()) });
    const c = await card();
    expect(c).toHaveAttribute("data-state", "connected");
    expect(within(c).getByText("VPN connected: peers see 203.0.113.7 (NL)")).toBeInTheDocument();
    expect(c).toHaveTextContent("Tunnel address 10.2.0.2/32");
    expect(c).toHaveTextContent("Last handshake 23 s ago");
    expect(c).toHaveTextContent("Endpoint 203.0.113.9:51820");
    expect(within(c).getByText("Your real IP: 198.51.100.9")).toBeInTheDocument();
    expect(within(c).getByRole("button", { name: "Check again" })).toBeInTheDocument();
    expect(within(c).queryByRole("button", { name: "Start without VPN" })).not.toBeInTheDocument();
  });

  it("is red and shows the real address when there is no VPN", async () => {
    open(base, {
      "GET /settings/download-client/builtin/status": () =>
        json(
          status({
            state: "unprotected",
            endpoint: "",
            tunnelAddresses: [],
            dns: [],
            lastHandshake: null,
            vpnIP: null,
          }),
        ),
    });
    const c = await card();
    expect(c).toHaveAttribute("data-state", "unprotected");
    expect(
      within(c).getByText("Unprotected: peers see your real IP (198.51.100.9)"),
    ).toBeInTheDocument();
    expect(c).not.toHaveTextContent("Tunnel address");
    expect(c).not.toHaveTextContent("Your real IP:");
  });

  it("is amber while connecting, and while down", async () => {
    let current = status({ state: "connecting", vpnIP: null, lastHandshake: null });
    open(withVpn, { "GET /settings/download-client/builtin/status": () => json(current) });
    let c = await card();
    expect(within(c).getByText("Connecting…")).toBeInTheDocument();
    expect(c).toHaveAttribute("data-state", "connecting");
    expect(c).not.toHaveTextContent("Last handshake");
    // Down: the poll picks it up (here, "Check again" does the same fetch).
    current = status({
      state: "down",
      vpnIP: null,
      since: new Date(Date.now() - 5 * 60_000).toISOString(),
    });
    await userEvent.click(within(c).getByRole("button", { name: "Check again" }));
    c = await card();
    await vi.waitFor(() => expect(c).toHaveAttribute("data-state", "down"));
    expect(c).toHaveTextContent("VPN down: downloads stopped (since 5 minutes ago)");
  });

  it("is red when the VPN shows the real address", async () => {
    open(withVpn, {
      "GET /settings/download-client/builtin/status": () =>
        json(status({ state: "down", leak: true, vpnIP: { ip: "198.51.100.9", country: "US" } })),
    });
    const c = await card();
    expect(
      within(c).getByText("Peers would see your real IP: the VPN isn't hiding it"),
    ).toBeInTheDocument();
    expect(c).toHaveTextContent("Downloads are stopped");
  });

  it("offers Start without VPN when held, and starts", async () => {
    const posts: string[] = [];
    let held = true;
    open(
      { ...base, vpnHeld: true },
      {
        "GET /settings/download-client/builtin/status": () =>
          json(
            held
              ? status({
                  state: "held",
                  message: "Downloads are stopped until you start them without the VPN.",
                  endpoint: "",
                  tunnelAddresses: [],
                  vpnIP: null,
                  lastHandshake: null,
                })
              : status({
                  state: "unprotected",
                  endpoint: "",
                  tunnelAddresses: [],
                  vpnIP: null,
                  lastHandshake: null,
                }),
          ),
        "POST /settings/download-client/builtin/start-unprotected": () => {
          posts.push("start");
          held = false;
          return json({ ...base, vpnHeld: false });
        },
      },
    );
    const c = await card();
    expect(c).toHaveAttribute("data-state", "held");
    expect(c).toHaveTextContent("Downloads are stopped until you start them without the VPN.");
    await userEvent.click(within(c).getByRole("button", { name: "Start without VPN" }));
    await vi.waitFor(() => expect(posts).toEqual(["start"]));
    await vi.waitFor(() =>
      expect(screen.getByRole("region", { name: "VPN status" })).toHaveAttribute(
        "data-state",
        "unprotected",
      ),
    );
    expect(screen.queryByRole("button", { name: "Start without VPN" })).not.toBeInTheDocument();
  });

  it("doesn't offer Start without VPN while a VPN (even an unusable one) is set up", async () => {
    open(
      { ...base, hasVpn: true, vpnProblem: "The saved VPN config can't be read." },
      {
        "GET /settings/download-client/builtin/status": () =>
          json(
            status({
              state: "held",
              message: "The saved VPN config can't be read.",
              endpoint: "",
              tunnelAddresses: [],
              vpnIP: null,
            }),
          ),
      },
    );
    const c = await card();
    expect(c).toHaveAttribute("data-state", "held");
    expect(within(c).queryByRole("button", { name: "Start without VPN" })).not.toBeInTheDocument();
  });

  it("Check again asks for a fresh lookup", async () => {
    const urls: string[] = [];
    open(withVpn, {
      "GET /settings/download-client/builtin/status": (req) => {
        urls.push(new URL(req.url).search);
        return json(status());
      },
    });
    const c = await card();
    await userEvent.click(within(c).getByRole("button", { name: "Check again" }));
    await vi.waitFor(() => expect(urls).toContain("?refresh=true"));
    expect(urls[0]).toBe("");
  });

  it("is not shown for another client, or before the built-in client is saved", async () => {
    open({ ...base, folder: "" });
    await screen.findByText("Drop a .conf file here");
    expect(screen.queryByRole("region", { name: "VPN status" })).not.toBeInTheDocument();
  });

  it("words each state, and the times", () => {
    expect(
      statusText(status({ state: "connected", vpnIP: null, vpnIPError: "" }) as never).detail,
    ).toBe("Checking which IP peers see…");
    expect(
      statusText(status({ state: "connected", vpnIP: null, vpnIPError: "Timed out." }) as never)
        .detail,
    ).toBe("Couldn't check which IP peers see: Timed out.");
    expect(
      statusText(
        status({ state: "unprotected", publicIP: null, publicIPError: "Timed out." }) as never,
      ),
    ).toMatchObject({
      tone: "red",
      headline: "Unprotected: peers see your real IP",
      detail: "Timed out.",
    });
    const now = Date.parse("2026-10-17T12:00:00Z");
    expect(ago("2026-10-17T11:59:37Z", now)).toBe("23 s ago");
    expect(ago("2026-10-17T11:55:00Z", now)).toBe("5 min ago");
    expect(ago("2026-10-17T09:00:00Z", now)).toBe("3 h ago");
  });
});
