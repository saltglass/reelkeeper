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
    const hc = screen.getByRole("button", { name: /High contrast/ });
    await userEvent.click(hc);
    expect(hc).toHaveAttribute("aria-pressed", "true");
    expect(document.documentElement.dataset.theme).toBe("contrast");
  });

  it("says what Match system follows, and hides the builder", async () => {
    await open();
    expect(screen.getByText(/Following your computer's appearance/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Accent colour")).not.toBeInTheDocument();
  });

  it("builds a custom accent with contrast checks and resets to orange", async () => {
    await open();
    await userEvent.click(screen.getByRole("button", { name: /^Custom/ }));
    const picker = screen.getByLabelText("Accent colour");
    fireEvent.change(picker, { target: { value: "#f43f5e" } });
    expect(document.documentElement.style.getPropertyValue("--primary")).not.toBe("");
    expect(screen.getAllByText("#f43f5e").length).toBeGreaterThan(0);
    expect(screen.getByText(/Button label \d+\.\d:1/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Light" }));
    expect(document.documentElement.dataset.theme).toBe("light");
    await userEvent.click(screen.getByRole("button", { name: "Reset to orange" }));
    expect(screen.getAllByText("#f0a04b").length).toBeGreaterThan(0);
  });

  it("changes motion", async () => {
    await open();
    await userEvent.click(screen.getByRole("button", { name: "Off" }));
    expect(document.documentElement.dataset.motion).toBe("off");
    expect(screen.getByText(/No motion at all/)).toBeInTheDocument();
    expect(screen.queryByRole("switch", { name: /Glow/ })).not.toBeInTheDocument();
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
