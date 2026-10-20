import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { NotificationSettingsUpdate } from "@/hooks/notifications";
import { json, renderApp, signedIn } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

const saved = {
  emby: {
    enabled: true,
    url: "http://192.168.1.30:8096",
    apiKeySet: true,
    pathFrom: "/media",
    pathTo: "/Volumes/Media",
  },
  jellyfin: { enabled: false, url: "", apiKeySet: false, pathFrom: "", pathTo: "" },
  plex: { enabled: false, url: "", apiKeySet: false, pathFrom: "", pathTo: "" },
  email: {
    enabled: false,
    host: "",
    port: 587,
    username: "",
    passwordSet: false,
    from: "",
    to: "",
  },
  webhook: { enabled: false, url: "", secretSet: false },
};

function open(testReply?: () => Response) {
  const puts: NotificationSettingsUpdate[] = [];
  const tests: { channel: string; settings: NotificationSettingsUpdate }[] = [];
  renderApp("/settings/notifications", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    "GET /settings/notifications": json(saved),
    "PUT /settings/notifications": async (req) => {
      const body = await req.json();
      puts.push(body);
      if (body.webhook.url === "bad") {
        return json(
          {
            code: "invalid",
            message: "Check the highlighted fields.",
            fields: { "webhook.url": "Enter a full address, like http://192.168.1.30:8096." },
          },
          422,
        );
      }
      return json(saved);
    },
    "POST /settings/notifications/test": async (req) => {
      tests.push(await req.json());
      return testReply ? testReply() : json({ message: "Connected to Emby 4.9.3 (Emby-server)" });
    },
  });
  return { puts, tests };
}

describe("notification settings", () => {
  it("loads the saved values and never shows the secret", async () => {
    open();
    expect(await screen.findByLabelText("Emby address")).toHaveValue("http://192.168.1.30:8096");
    expect(screen.getByLabelText("Reelkeeper's path for Emby")).toHaveValue("/media");
    expect(screen.getByLabelText("Emby's path")).toHaveValue("/Volumes/Media");
    const key = screen.getByLabelText("Emby API key");
    expect(key).toHaveValue("");
    expect(key).toHaveAttribute("placeholder", "Saved — leave empty to keep it");
    expect(screen.getByLabelText("Port")).toHaveValue(587);
  });

  it("saves without a secret when none was typed, and with one when it was", async () => {
    const { puts } = open();
    const url = await screen.findByLabelText("Emby address");
    await userEvent.clear(url);
    await userEvent.type(url, "http://emby:8096");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(puts[0]?.emby).toEqual({
      enabled: true,
      url: "http://emby:8096",
      pathFrom: "/media",
      pathTo: "/Volumes/Media",
    });
    expect(puts[0]?.email.port).toBe(587);
    expect(puts[0]?.email).not.toHaveProperty("password");

    await userEvent.type(screen.getByLabelText("Emby API key"), "new-key");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(puts).toHaveLength(2));
    expect(puts[1]?.emby.apiKey).toBe("new-key");
  });

  it("tests the values as typed and shows the result", async () => {
    const { tests } = open();
    const url = await screen.findByLabelText("Emby address");
    await userEvent.clear(url);
    await userEvent.type(url, "http://other:8096");
    await userEvent.click(screen.getAllByRole("button", { name: "Test" })[0] as HTMLElement);
    expect(await screen.findByText("Connected to Emby 4.9.3 (Emby-server)")).toBeInTheDocument();
    expect(tests[0]?.channel).toBe("emby");
    expect(tests[0]?.settings.emby.url).toBe("http://other:8096");
  });

  it("shows why a test failed", async () => {
    open(() =>
      json({ code: "notification", message: "Couldn't reach Emby: it answered 401." }, 422),
    );
    await userEvent.click(
      (await screen.findAllByRole("button", { name: "Test" }))[0] as HTMLElement,
    );
    expect(await screen.findByText("Couldn't reach Emby: it answered 401.")).toBeInTheDocument();
  });

  it("asks before leaving with unsaved changes", async () => {
    const { puts } = open();
    const url = await screen.findByLabelText("Emby address");
    await userEvent.type(url, "1");
    await userEvent.click(screen.getAllByRole("link", { name: "Movies" })[0] as HTMLElement);
    expect(await screen.findByText("Leave without saving?")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Stay" }));
    expect(screen.getByLabelText("Emby address")).toHaveValue("http://192.168.1.30:80961");
    expect(puts).toHaveLength(0);
  });

  it("has a Jellyfin card of its own, saved and tested apart from Emby", async () => {
    const { puts, tests } = open();
    expect(await screen.findByLabelText("Jellyfin address")).toHaveValue("");
    expect(screen.getByLabelText("Jellyfin API key")).toHaveAttribute("placeholder", "");
    await userEvent.click(screen.getByRole("switch", { name: "Jellyfin enabled" }));
    await userEvent.type(screen.getByLabelText("Jellyfin address"), "http://jellyfin:8096");
    await userEvent.type(screen.getByLabelText("Jellyfin API key"), "jf-key");
    await userEvent.type(screen.getByLabelText("Reelkeeper's path for Jellyfin"), "/media");
    await userEvent.type(screen.getByLabelText("Jellyfin's path"), "/data/media");

    await userEvent.click(screen.getAllByRole("button", { name: "Test" })[1] as HTMLElement);
    await vi.waitFor(() => expect(tests).toHaveLength(1));
    expect(tests[0]?.channel).toBe("jellyfin");
    expect(tests[0]?.settings.jellyfin.url).toBe("http://jellyfin:8096");

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]?.jellyfin).toEqual({
      enabled: true,
      url: "http://jellyfin:8096",
      apiKey: "jf-key",
      pathFrom: "/media",
      pathTo: "/data/media",
    });
    // Emby is sent unchanged, without its stored key.
    expect(puts[0]?.emby).toEqual({
      enabled: true,
      url: "http://192.168.1.30:8096",
      pathFrom: "/media",
      pathTo: "/Volumes/Media",
    });
  });

  it("has a Plex card asking for a token, with Plex's port", async () => {
    const { puts, tests } = open();
    const url = await screen.findByLabelText("Plex address");
    expect(url).toHaveAttribute("placeholder", "http://192.168.1.30:32400");
    expect(screen.getByText(/X-Plex-Token=/)).toBeInTheDocument();
    expect(screen.getByText(/Empty trash automatically after every scan/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("switch", { name: "Plex enabled" }));
    await userEvent.type(url, "http://plex:32400");
    await userEvent.type(screen.getByLabelText("Plex Token"), "plex-token");

    await userEvent.click(screen.getAllByRole("button", { name: "Test" })[2] as HTMLElement);
    await vi.waitFor(() => expect(tests).toHaveLength(1));
    expect(tests[0]?.channel).toBe("plex");

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]?.plex).toEqual({
      enabled: true,
      url: "http://plex:32400",
      apiKey: "plex-token",
      pathFrom: "",
      pathTo: "",
    });
  });

  it("shows field errors from the server", async () => {
    open();
    const url = await screen.findByLabelText("Address");
    await userEvent.type(url, "bad");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(
      await screen.findByText("Enter a full address, like http://192.168.1.30:8096."),
    ).toBeInTheDocument();
  });
});
