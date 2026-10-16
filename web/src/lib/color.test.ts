import { describe, expect, it } from "vitest";
import { accentPresets, buildCustomTheme, contrast, contrastChecks, hexToHsl, hsl } from "./color";

describe("colour maths", () => {
  it("converts between hex and hsl", () => {
    const [h, s, l] = hexToHsl("#8b5cf6");
    expect(hsl(h, s, l)).toBe("#8b5cf6");
    expect(hsl(0, 0, 100)).toBe("#ffffff");
  });

  it("computes WCAG contrast", () => {
    expect(contrast("#000000", "#ffffff")).toBeCloseTo(21, 0);
    expect(contrast("#777777", "#777777")).toBe(1);
  });

  // Every suggested accent, on both bases, meets the same bar as the
  // built-in themes: 4.5:1 for text, 3:1 for the focus ring.
  for (const { name, hex } of accentPresets) {
    for (const base of ["dark", "light"] as const) {
      it(`${name} on ${base} passes every contrast check`, () => {
        const failing = contrastChecks(buildCustomTheme(hex, base)).filter((c) => !c.pass);
        expect(failing).toEqual([]);
      });
    }
  }

  it("still builds a theme from an awkward accent", () => {
    const v = buildCustomTheme("#ffff00", "light");
    expect(v.primary).toMatch(/^#[0-9a-f]{6}$/);
    expect(contrast(v["primary-foreground"], v.primary)).toBeGreaterThanOrEqual(4.5);
  });
});
