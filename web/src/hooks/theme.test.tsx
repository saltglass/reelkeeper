import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { resetAppearanceForTests, themes, useAppearance, useTheme } from "./theme";

const root = document.documentElement;
const saved = () => JSON.parse(localStorage.getItem("reelkeeper.appearance") ?? "null");

describe("appearance", () => {
  beforeEach(() => {
    // jsdom has no matchMedia; pretend the browser prefers dark.
    window.matchMedia = (query: string) =>
      ({
        matches: query.includes("dark"),
        media: query,
        addEventListener: () => {},
        removeEventListener: () => {},
      }) as unknown as MediaQueryList;
    localStorage.clear();
    root.removeAttribute("data-theme");
    root.removeAttribute("style");
    root.classList.remove("dark");
    resetAppearanceForTests();
  });

  it("applies a chosen theme and remembers it", () => {
    const { result } = renderHook(() => useTheme());
    act(() => result.current.setTheme("paper"));
    expect(root.dataset.theme).toBe("paper");
    expect(root.classList.contains("dark")).toBe(false);
    expect(saved().theme).toBe("paper");
  });

  it("marks dark-ground themes with the dark class", () => {
    const { result } = renderHook(() => useTheme());
    act(() => result.current.setTheme("midnight"));
    expect(root.classList.contains("dark")).toBe(true);
  });

  it("follows the browser when nothing is chosen", () => {
    const { result } = renderHook(() => useTheme());
    expect(result.current.theme).toBe("system");
    expect(root.dataset.theme).toBe("dark");
    expect(root.dataset.motion).toBe("lively");
    expect(root.dataset.glow).toBe("on");
  });

  it("ignores unknown saved values", () => {
    localStorage.setItem(
      "reelkeeper.appearance",
      JSON.stringify({ theme: "nonsense", motion: "wild" }),
    );
    const { result } = renderHook(() => useAppearance());
    expect(result.current.appearance.theme).toBe("system");
    expect(result.current.appearance.motion).toBe("lively");
  });

  it("shows queue symbols unless a boolean says otherwise", () => {
    localStorage.setItem("reelkeeper.appearance", JSON.stringify({ queueSymbols: "no" }));
    expect(renderHook(() => useAppearance()).result.current.appearance.queueSymbols).toBe(true);
    resetAppearanceForTests();
    localStorage.setItem("reelkeeper.appearance", JSON.stringify({ queueSymbols: false }));
    expect(renderHook(() => useAppearance()).result.current.appearance.queueSymbols).toBe(false);
  });

  it("keeps a theme chosen before the redesign", () => {
    localStorage.setItem("reelkeeper-theme", "moss");
    const { result } = renderHook(() => useTheme());
    expect(result.current.theme).toBe("moss");
  });

  it("builds a custom theme from one accent and sets its colours on the page", () => {
    const { result } = renderHook(() => useAppearance());
    act(() =>
      result.current.update({ theme: "custom", customAccent: "#f43f5e", customBase: "light" }),
    );
    expect(root.dataset.theme).toBe("light");
    expect(root.style.getPropertyValue("--primary")).toMatch(/^#/);
    expect(saved().customVars.primary).toBe(root.style.getPropertyValue("--primary"));
    // Leaving it removes the colours again.
    act(() => result.current.update({ theme: "dark" }));
    expect(root.style.getPropertyValue("--primary")).toBe("");
  });

  it("applies motion and the glow, shared between every user of the hook", () => {
    const a = renderHook(() => useAppearance());
    const b = renderHook(() => useAppearance());
    act(() => a.result.current.update({ motion: "off", glow: false }));
    expect(root.dataset.motion).toBe("off");
    expect(root.dataset.glow).toBe("off");
    expect(b.result.current.appearance.motion).toBe("off");
  });

  it("offers every theme the stylesheet defines", () => {
    expect(themes.map((t) => t.id)).toEqual([
      "dark",
      "light",
      "midnight",
      "ember",
      "moss",
      "paper",
      "contrast",
    ]);
  });
});
