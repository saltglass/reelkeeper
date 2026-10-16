import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, renderApp, signedIn, signedOut } from "@/test/app";

afterEach(() => vi.unstubAllGlobals());

describe("login page", () => {
  it("asks for the first account on a fresh install", async () => {
    renderApp("/login", { "GET /auth/state": json({ ...signedOut, setupRequired: true }) });
    expect(await screen.findByRole("button", { name: "Create account" })).toBeInTheDocument();
    expect(screen.getByText(/Create the administrator account/)).toBeInTheDocument();

    await userEvent.type(screen.getByLabelText("Password"), "a-good-password");
    await userEvent.type(screen.getByLabelText("Confirm password"), "different");
    expect(screen.getByText("The passwords don't match.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create account" })).toBeDisabled();
  });

  it("shows a clear message for a wrong password", async () => {
    renderApp("/login", {
      "GET /auth/state": json(signedOut),
      "POST /auth/login": json(
        { code: "invalid_credentials", message: "Wrong username or password." },
        401,
      ),
    });
    await userEvent.type(await screen.findByLabelText("Username"), "dan");
    await userEvent.type(screen.getByLabelText("Password"), "nope-nope");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Wrong username or password.");
  });

  it("signs in and goes where the user was heading", async () => {
    let authed = false;
    const { router } = renderApp("/login?redirect=%2Fsettings%2Fgeneral", {
      "GET /auth/state": () => json(authed ? signedIn : signedOut),
      "POST /auth/login": () => {
        authed = true;
        return new Response(null, { status: 204 });
      },
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    });
    await userEvent.type(await screen.findByLabelText("Username"), "dan");
    await userEvent.type(screen.getByLabelText("Password"), "a-good-password");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/settings/general"));
  });

  it("offers single sign-on and explains its errors", async () => {
    renderApp("/login?error=sso_denied", {
      "GET /auth/state": json({
        ...signedOut,
        oidcEnabled: true,
        oidcButtonLabel: "Sign in with Authentik",
      }),
    });
    const sso = await screen.findByRole("link", { name: "Sign in with Authentik" });
    expect(sso.getAttribute("href")).toBe("/api/v1/auth/oidc/login?returnTo=%2F");
    expect(screen.getByRole("alert")).toHaveTextContent(/required group/);
  });

  it("passes on the identity provider's own explanation", async () => {
    renderApp(
      "/login?error=sso_failed&detail=The%20identity%20provider%20said%3A%20invalid_request",
      { "GET /auth/state": json({ ...signedOut, oidcEnabled: true }) },
    );
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/didn't work/);
    expect(alert).toHaveTextContent("The identity provider said: invalid_request");
  });

  it("keeps a signed-in user on the page to read a failed link", async () => {
    const { router } = renderApp("/login?error=sso_link_conflict&redirect=%2Fsettings%2Fgeneral", {
      "GET /auth/state": json(signedIn),
      "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
    });
    const next = await screen.findByRole("button", { name: "Continue to Reelkeeper" });
    expect(screen.getByRole("alert")).toHaveTextContent(/already linked/);
    expect(router.state.location.pathname).toBe("/login");
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
    await userEvent.click(next);
    await waitFor(() => expect(router.state.location.pathname).toBe("/settings/general"));
  });

  it("hides the password form when password sign-in is off", async () => {
    renderApp("/login", {
      "GET /auth/state": json({ ...signedOut, localLoginEnabled: false, oidcEnabled: true }),
    });
    expect(await screen.findByRole("link", { name: "Sign in with SSO" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
  });
});

describe("first run: restore from a backup", () => {
  const setup = { ...signedOut, setupRequired: true };
  const backupFile = () => new File(["backup bytes"], "reelkeeper.backup");

  async function openRestore() {
    await userEvent.click(await screen.findByRole("button", { name: "Restore from a backup" }));
    await userEvent.upload(screen.getByLabelText("Backup file"), backupFile());
    await userEvent.click(screen.getByRole("button", { name: "Restore and restart" }));
  }

  it("is offered only on a fresh install", async () => {
    const first = renderApp("/login", { "GET /auth/state": json(setup) });
    expect(
      await screen.findByRole("button", { name: "Restore from a backup" }),
    ).toBeInTheDocument();
    first.unmount();

    renderApp("/login", { "GET /auth/state": json(signedOut) });
    await screen.findByRole("button", { name: "Sign in" });
    expect(screen.queryByRole("button", { name: "Restore from a backup" })).not.toBeInTheDocument();
  });

  it("explains itself and lets you go back to the account form", async () => {
    renderApp("/login", { "GET /auth/state": json(setup) });
    await userEvent.click(await screen.findByRole("button", { name: "Restore from a backup" }));
    expect(screen.getByText(/same REELKEEPER_SECRET_KEY/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restore and restart" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Create account" })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Create an account instead" }));
    expect(screen.getByRole("button", { name: "Create account" })).toBeInTheDocument();
  });

  it("shows the server's reason when the backup is refused", async () => {
    renderApp("/login", {
      "GET /auth/state": json(setup),
      "POST /auth/setup/restore": json(
        { code: "backup", message: "That backup was made with a different REELKEEPER_SECRET_KEY." },
        422,
      ),
    });
    await openRestore();
    expect(await screen.findByRole("alert")).toHaveTextContent(/different REELKEEPER_SECRET_KEY/);
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("sends the file itself and shows the restarting screen", async () => {
    const { calls } = renderApp("/login", {
      "GET /auth/state": json(setup),
      "POST /auth/setup/restore": json({ message: "Restarting to restore." }, 202),
      "GET /ping": new Response("ok"),
    });
    await openRestore();
    expect(await screen.findByRole("alertdialog")).toHaveTextContent("Restarting to restore");
    const sent = calls.find((c) => c.method === "POST");
    expect(sent?.headers.get("Content-Type")).toBe("application/octet-stream");
    expect(await sent?.text()).toBe("backup bytes");
  });

  it("switches to sign-in when an account already exists", async () => {
    let created = false;
    renderApp("/login", {
      "GET /auth/state": () => json(created ? signedOut : setup),
      "POST /auth/setup/restore": () => {
        created = true;
        return json({ code: "conflict", message: "An account exists." }, 409);
      },
    });
    await openRestore();
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.getByText("An account already exists. Sign in instead.")).toBeInTheDocument();
  });
});

describe("protected pages", () => {
  it("send signed-out visitors to the login page and back afterwards", async () => {
    const { router } = renderApp("/settings/security", { "GET /auth/state": json(signedOut) });
    await waitFor(() => expect(router.state.location.pathname).toBe("/login"));
    expect(router.state.location.search).toEqual({
      redirect: "/settings/security",
      error: undefined,
      detail: undefined,
    });
  });
});
