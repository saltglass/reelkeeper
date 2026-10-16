/**
 * Colour maths for the Custom theme: from one accent colour, work out a whole
 * theme (surfaces, text, links, focus ring, brand gradient) that still meets
 * the contrast rules of the built-in themes (4.5:1 for text, 3:1 for the
 * focus ring). Ported from a design prototype.
 */

type RGB = [number, number, number];

export function hexToRgb(hex: string): RGB {
  let h = String(hex || "#8b5cf6").replace("#", "");
  if (h.length === 3) h = [...h].map((c) => c + c).join("");
  const n = Number.parseInt(h, 16) || 0;
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}

function rgbToHex(r: number, g: number, b: number): string {
  return `#${[r, g, b]
    .map((v) =>
      Math.round(Math.max(0, Math.min(255, v)))
        .toString(16)
        .padStart(2, "0"),
    )
    .join("")}`;
}

/** Hue (0–360), saturation and lightness (0–100) of a hex colour. */
export function hexToHsl(hex: string): [number, number, number] {
  const [r, g, b] = hexToRgb(hex).map((v) => v / 255) as RGB;
  const mx = Math.max(r, g, b);
  const mn = Math.min(r, g, b);
  let h = 0;
  let s = 0;
  const l = (mx + mn) / 2;
  if (mx !== mn) {
    const d = mx - mn;
    s = l > 0.5 ? d / (2 - mx - mn) : d / (mx + mn);
    h = mx === r ? (g - b) / d + (g < b ? 6 : 0) : mx === g ? (b - r) / d + 2 : (r - g) / d + 4;
    h *= 60;
  }
  return [h, s * 100, l * 100];
}

/** A hex colour from hue, saturation and lightness. */
export function hsl(h: number, s: number, l: number): string {
  const hue = ((h % 360) + 360) % 360;
  const sat = Math.max(0, Math.min(100, s)) / 100;
  const lig = Math.max(0, Math.min(100, l)) / 100;
  const k = (n: number) => (n + hue / 30) % 12;
  const a = sat * Math.min(lig, 1 - lig);
  const f = (n: number) => lig - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));
  return rgbToHex(f(0) * 255, f(8) * 255, f(4) * 255);
}

function luminance(hex: string): number {
  const c = hexToRgb(hex).map((v) => {
    const x = v / 255;
    return x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * (c[0] ?? 0) + 0.7152 * (c[1] ?? 0) + 0.0722 * (c[2] ?? 0);
}

/** WCAG contrast ratio between two colours (1 to 21). */
export function contrast(a: string, b: string): number {
  const x = luminance(a);
  const y = luminance(b);
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
}

/** Starting from (h, s, l), step lightness until the colour reaches target contrast on bg. */
function fitContrast(
  h: number,
  s: number,
  l: number,
  bg: string,
  target: number,
  step: number,
): string {
  let light = l;
  let c = hsl(h, s, light);
  for (let i = 0; i < 70 && contrast(c, bg) < target; i++) {
    light += step;
    if (light <= 0 || light >= 100) break;
    c = hsl(h, s, light);
  }
  return c;
}

/** The theme variables the Custom theme sets, by their CSS names in index.css. */
export type ThemeVars = Record<
  | "background"
  | "foreground"
  | "card"
  | "muted"
  | "muted-foreground"
  | "border"
  | "input"
  | "ring"
  | "primary"
  | "primary-foreground"
  | "accent"
  | "accent-foreground"
  | "destructive"
  | "destructive-foreground"
  | "success"
  | "warning"
  | "link"
  | "brand-from"
  | "brand-to"
  | "shadow-card"
  | "shadow-pop"
  | "glow-alpha",
  string
>;

/** A whole theme from one accent colour, on a dark or light base. */
export function buildCustomTheme(accent: string, base: "dark" | "light"): ThemeVars {
  const [h, s0, l0] = hexToHsl(accent);
  const s = Math.max(45, s0);
  const dark = base === "dark";
  const bg = dark ? hsl(h, 28, 4) : hsl(h, 25, 97.5);
  const card = dark ? hsl(h, 28, 8.5) : "#ffffff";
  const accentFill = dark ? hsl(h, 35, 16) : hsl(h, 90, 95);
  const link = dark
    ? fitContrast(h, Math.min(s, 95), 70, card, 4.5, 2)
    : fitContrast(h, Math.min(s, 85), 44, card, 4.5, -2);

  // Primary stays near the picked colour; its lightness is nudged until its
  // label (white or near-black, whichever reads better) reaches 4.5:1.
  let pl = dark ? Math.min(Math.max(l0, 50), 66) : Math.min(Math.max(l0, 36), 52);
  const ink = hsl(h, 40, 6);
  let primary = hsl(h, s, pl);
  let primaryFg = "#ffffff";
  for (let i = 0; i < 40; i++) {
    primary = hsl(h, s, pl);
    const w = contrast("#ffffff", primary);
    const k = contrast(ink, primary);
    primaryFg = w >= k ? "#ffffff" : ink;
    if (Math.max(w, k) >= 4.5) break;
    pl += w >= k ? -1.5 : 1.5;
  }

  return {
    background: bg,
    foreground: dark ? hsl(h, 30, 94) : hsl(h, 25, 10),
    card,
    muted: dark ? hsl(h, 22, 12.5) : hsl(h, 20, 94.5),
    "muted-foreground": dark
      ? fitContrast(h, 12, 66, card, 4.5, 1.5)
      : fitContrast(h, 8, 44, card, 4.5, -1.5),
    border: dark ? hsl(h, 20, 18) : hsl(h, 18, 89),
    input: dark ? fitContrast(h, 16, 42, card, 3, 2) : fitContrast(h, 8, 60, card, 3, -2),
    ring: link,
    primary,
    "primary-foreground": primaryFg,
    accent: accentFill,
    "accent-foreground": dark
      ? fitContrast(h, 90, 88, accentFill, 4.5, 1.5)
      : fitContrast(h, 70, 32, accentFill, 4.5, -2),
    destructive: dark ? "#f87171" : "#dc2626",
    "destructive-foreground": dark ? "#1a0707" : "#ffffff",
    success: dark ? "#4ade80" : "#15803d",
    warning: dark ? "#fbbf24" : "#b45309",
    link,
    "brand-from": primary,
    "brand-to": hsl(h + 70, 85, dark ? 58 : 50),
    "shadow-card": dark ? "0 1px 2px rgba(0,0,0,.5)" : "0 1px 2px rgba(23,20,31,.06)",
    "shadow-pop": dark ? "0 12px 32px rgba(0,0,0,.6)" : "0 12px 32px rgba(23,20,31,.14)",
    "glow-alpha": dark ? "0.22" : "0.16",
  };
}

/** The pairs the Custom theme is checked on, with the bar each must meet. */
export function contrastChecks(v: ThemeVars) {
  const check = (label: string, a: string, b: string, target: number) => {
    const ratio = contrast(a, b);
    return { label, ratio, target, pass: ratio >= target };
  };
  return [
    check("Body text", v.foreground, v.background, 4.5),
    check("Secondary text", v["muted-foreground"], v.card, 4.5),
    check("Button label", v["primary-foreground"], v.primary, 4.5),
    check("Links", v.link, v.card, 4.5),
    check("Focus ring", v.ring, v.background, 3),
  ];
}

/** Suggested accents for the Custom theme. */
export const accentPresets = [
  { name: "Teal", hex: "#14b8a6" },
  { name: "Rose", hex: "#f43f5e" },
  { name: "Amber", hex: "#f59e0b" },
  { name: "Sky", hex: "#0ea5e9" },
  { name: "Violet", hex: "#8b5cf6" },
  { name: "Fuchsia", hex: "#d946ef" },
  { name: "Lime", hex: "#84cc16" },
] as const;
