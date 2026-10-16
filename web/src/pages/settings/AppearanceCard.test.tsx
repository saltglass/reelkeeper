import { fireEvent, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import { resetAppearanceForTests } from "@/hooks/theme";
import { json, renderApp, signedIn } from "@/test/app";

function open() {
  localStorage.clear();
  resetAppearanceForTests();
  renderApp("/settings/general", {
    "GET /auth/state": json(signedIn),
    "GET /settings/general": json({ instanceName: "Reelkeeper", logLevel: "info" }),
  });
  return screen.findByRole("heading", { name: "Appearance" });
}

beforeEach(() => {
  localStorage.clear();
});

describe("appearance card", () => {
  it("picks a theme at once and marks it", async () => {
    await open();
    const ember = screen.getByRole("button", { name: /Ember/ });
    await userEvent.click(ember);
    expect(ember).toHaveAttribute("aria-pressed", "true");
    expect(document.documentElement.dataset.theme).toBe("ember");
  });

  it("says what Match system follows, and hides the builder", async () => {
    await open();
    expect(screen.getByText(/Following your computer's appearance/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Accent colour")).not.toBeInTheDocument();
  });

  it("builds a custom theme with contrast checks and resets to violet", async () => {
    await open();
    await userEvent.click(screen.getByRole("button", { name: /^Custom/ }));
    const picker = screen.getByLabelText("Accent colour");
    fireEvent.change(picker, { target: { value: "#f43f5e" } });
    expect(document.documentElement.style.getPropertyValue("--primary")).not.toBe("");
    expect(screen.getAllByText("#f43f5e").length).toBeGreaterThan(0);
    expect(screen.getByText(/Body text \d+\.\d:1/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Light" }));
    expect(document.documentElement.dataset.theme).toBe("light");
    await userEvent.click(screen.getByRole("button", { name: "Reset to violet" }));
    expect(screen.getAllByText("#8b5cf6").length).toBeGreaterThan(0);
  });

  it("changes motion and the glow", async () => {
    await open();
    await userEvent.click(screen.getByRole("button", { name: "Off" }));
    expect(document.documentElement.dataset.motion).toBe("off");
    expect(screen.getByText(/No motion at all/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("switch", { name: "Glow behind pages" }));
    expect(document.documentElement.dataset.glow).toBe("off");
  });

  it("turns titles under posters off and remembers it", async () => {
    await open();
    const sw = screen.getByRole("switch", { name: "Titles under posters" });
    expect(sw).toBeChecked();
    await userEvent.click(sw);
    expect(sw).not.toBeChecked();
    expect(JSON.parse(localStorage.getItem("reelkeeper.appearance") ?? "{}").posterTitles).toBe(
      false,
    );
  });

  it("turns symbols on Queue buttons off and remembers it", async () => {
    await open();
    const sw = screen.getByRole("switch", { name: "Symbols on Queue buttons" });
    expect(sw).toBeChecked();
    await userEvent.click(sw);
    expect(sw).not.toBeChecked();
    expect(JSON.parse(localStorage.getItem("reelkeeper.appearance") ?? "{}").queueSymbols).toBe(
      false,
    );
  });
});
