import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { SecuritySettings } from "@/api/client";
import { json, renderApp, signedIn } from "@/test/app";
import { listErrors, toForm, toUpdate } from "./SecuritySettingsPage";

afterEach(() => vi.unstubAllGlobals());

const stored: SecuritySettings = {
  localLoginEnabled: true,
  bypassNetworks: ["192.168.1.0/24"],
  trustedProxies: ["172.16.0.2/32"],
  sessionDays: 30,
  apiKey: "0123456789abcdef0123456789abcdef",
  unreadableCredentials: [],
  oidc: {
    enabled: false,
    issuerUrl: "",
    clientId: "",
    clientSecretSet: true,
    scopes: ["openid", "profile", "email"],
    groupsClaim: "groups",
    requiredGroup: "",
    buttonLabel: "Sign in with SSO",
    redirectUrl: "https://reelkeeper.example/api/v1/auth/oidc/callback",
  },
};

describe("security form mapping", () => {
  it("round-trips without changing anything", () => {
    const u = toUpdate(toForm(stored));
    expect(u.bypassNetworks).toEqual(stored.bypassNetworks);
    expect(u.trustedProxies).toEqual(stored.trustedProxies);
    expect(u.oidc.scopes).toEqual(stored.oidc.scopes);
    // An untouched secret field keeps the stored secret.
    expect(u.oidc.clientSecret).toBeUndefined();
  });

  it("splits lists on new lines and commas and drops blanks", () => {
    const f = toForm(stored);
    f.bypass = " 10.0.0.1 \n\n192.168.1.0/24, 10.0.0.2\n";
    expect(toUpdate(f).bypassNetworks).toEqual(["10.0.0.1", "192.168.1.0/24", "10.0.0.2"]);
  });

  it("sends a new secret, or an empty one to remove it", () => {
    const f = toForm(stored);
    f.oidc.clientSecret = "new";
    expect(toUpdate(f).oidc.clientSecret).toBe("new");
    f.oidc.clearSecret = true;
    expect(toUpdate(f).oidc.clientSecret).toBe("");
  });

  it("explains list errors by entry", () => {
    const msg = listErrors(
      { "bypassNetworks.1": "includes the trusted proxy 172.16.0.2/32." },
      "bypassNetworks",
      ["192.168.1.0/24", "172.16.0.0/24"],
    );
    expect(msg).toBe("172.16.0.0/24: includes the trusted proxy 172.16.0.2/32.");
  });
});

describe("security page", () => {
  it("won't offer turning off password sign-in before SSO has worked", async () => {
    renderApp("/settings/security", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /settings/security": json(stored),
    });
    const toggle = await screen.findByRole("switch", {
      name: "Allow signing in with a username and password",
    });
    expect(toggle).toBeDisabled();
    expect(screen.getByText(/sign in with it once/)).toBeInTheDocument();
  });

  it("shows server validation messages next to the field", async () => {
    renderApp("/settings/security", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /settings/security": json(stored),
      "PUT /settings/security": json(
        {
          code: "invalid",
          message: "Some fields need attention.",
          fields: { "bypassNetworks.0": "includes the trusted proxy 172.16.0.2/32." },
        },
        422,
      ),
    });
    const bypass = await screen.findByLabelText("Trusted networks");
    await userEvent.clear(bypass);
    await userEvent.type(bypass, "172.16.0.0/24");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(
      await screen.findByText("172.16.0.0/24: includes the trusted proxy 172.16.0.2/32."),
    ).toBeInTheDocument();
  });

  it("asks before replacing the API key", async () => {
    renderApp("/settings/security", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
      "GET /settings/security": json(stored),
    });
    await userEvent.click(await screen.findByRole("button", { name: "Generate new key" }));
    expect(await screen.findByText(/stops working immediately/)).toBeInTheDocument();
  });
});
