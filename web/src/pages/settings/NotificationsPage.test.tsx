import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json } from "@/test/app";
import { emailAsSaved, embyAsSaved, openNotifications } from "@/test/notifications";

afterEach(() => vi.unstubAllGlobals());

const open = (testReply?: () => Response) =>
  openNotifications("/settings/notifications", testReply);

describe("notification settings", () => {
  it("loads email and webhook, never the password, and leaves media servers to their page", async () => {
    open();
    expect(await screen.findByLabelText("Mail server")).toHaveValue("smtp.example.com");
    expect(screen.getByLabelText("Port")).toHaveValue(587);
    const password = screen.getByLabelText("Password");
    expect(password).toHaveValue("");
    expect(password).toHaveAttribute("placeholder", "Saved — leave empty to keep it");
    expect(screen.queryByLabelText("Emby address")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Plex address")).not.toBeInTheDocument();
  });

  it("saves without a password when none was typed, and keeps the media servers as they were", async () => {
    const { puts } = open();
    const host = await screen.findByLabelText("Mail server");
    await userEvent.clear(host);
    await userEvent.type(host, "mail.example.com");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(puts[0]?.email).toEqual({ ...emailAsSaved, host: "mail.example.com" });
    expect(puts[0]?.emby).toEqual(embyAsSaved);
    expect(puts[0]?.plex).toEqual({ enabled: false, url: "", pathFrom: "", pathTo: "" });

    await userEvent.type(screen.getByLabelText("Password"), "new-password");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await vi.waitFor(() => expect(puts).toHaveLength(2));
    expect(puts[1]?.email.password).toBe("new-password");
  });

  it("tests the values as typed", async () => {
    const { tests } = open(() => json({ message: "Test email sent to me@example.com" }));
    const to = await screen.findByLabelText("Send to");
    await userEvent.clear(to);
    await userEvent.type(to, "other@example.com");
    await userEvent.click(screen.getAllByRole("button", { name: "Test" })[0] as HTMLElement);
    expect(await screen.findByText("Test email sent to me@example.com")).toBeInTheDocument();
    expect(tests[0]?.channel).toBe("email");
    expect(tests[0]?.settings.email.to).toBe("other@example.com");
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
