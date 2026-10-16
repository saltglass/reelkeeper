import { useCallback, useSyncExternalStore } from "react";
import { buildCustomTheme, type ThemeVars } from "@/lib/color";

/**
 * Appearance: the theme, motion, the colour glow, poster titles and queue
 * button symbols, remembered per browser (Settings → General → Appearance).
 * Changes apply at once, everywhere,
 * because every component reads one shared store.
 *
 * The themes are blocks of colours in index.css, selected with data-theme on
 * <html>. "system" follows the browser between Reelkeeper Dark and Light.
 * "custom" builds a theme from one accent colour (lib/color.ts) and sets its
 * colours on <html> directly, over its base theme. `dark` says whether a
 * theme has a dark ground (it also gets the .dark class). `swatch` is
 * [ground, card, primary, brand-from, brand-to], for the picker's preview.
 *
 * Adding a theme: a [data-theme="id"] block in index.css, an entry here, and
 * the id in the script in web/index.html, which applies the saved choice
 * before the first paint so nothing flashes.
 */
export const themes = [
  {
    id: "dark",
    name: "Reelkeeper Dark",
    dark: true,
    swatch: ["#07060b", "#110f1c", "#8b5cf6", "#8b5cf6", "#22d3ee"],
  },
  {
    id: "light",
    name: "Reelkeeper Light",
    dark: false,
    swatch: ["#f7f7fa", "#ffffff", "#7c3aed", "#8b5cf6", "#22d3ee"],
  },
  {
    id: "midnight",
    name: "Midnight",
    dark: true,
    swatch: ["#050b14", "#0c1626", "#3b82f6", "#3b82f6", "#2dd4bf"],
  },
  {
    id: "ember",
    name: "Ember",
    dark: true,
    swatch: ["#0c0706", "#17100d", "#f97316", "#f97316", "#f43f5e"],
  },
  {
    id: "moss",
    name: "Moss",
    dark: true,
    swatch: ["#060b08", "#0d1710", "#22c55e", "#22c55e", "#a3e635"],
  },
  {
    id: "paper",
    name: "Paper",
    dark: false,
    swatch: ["#faf6f0", "#fffdf9", "#c2410c", "#ea580c", "#ca8a04"],
  },
  {
    id: "contrast",
    name: "High contrast",
    dark: true,
    swatch: ["#000000", "#0a0a0a", "#c4b5fd", "#c4b5fd", "#67e8f9"],
  },
] as const;

export type ThemeId = (typeof themes)[number]["id"];
export type Theme = ThemeId | "system" | "custom";
export type Motion = "lively" | "subtle" | "off";

export type Appearance = {
  theme: Theme;
  motion: Motion;
  /** The soft colour glow behind the top of each page. */
  glow: boolean;
  /** Title and year under each poster in the Movies and Series grids. */
  posterTitles: boolean;
  /** Symbols instead of words on the row buttons of Activity → Queue (Retry import, Pause, Resume, Move to top, Remove). */
  queueSymbols: boolean;
  /** The Custom theme's accent and base. */
  customAccent: string;
  customBase: "dark" | "light";
};

export const defaultAppearance: Appearance = {
  theme: "system",
  motion: "lively",
  glow: true,
  posterTitles: true,
  queueSymbols: true,
  customAccent: "#14b8a6",
  customBase: "dark",
};

const storageKey = "reelkeeper.appearance";
// Before the redesign only the theme was remembered, under this key.
const legacyThemeKeys = ["reelkeeper-theme"];

const isThemeId = (t: unknown): t is ThemeId => themes.some((x) => x.id === t);
const isTheme = (t: unknown): t is Theme => t === "system" || t === "custom" || isThemeId(t);
const isMotion = (m: unknown): m is Motion => m === "lively" || m === "subtle" || m === "off";
const isHex = (s: unknown): s is string => typeof s === "string" && /^#[0-9a-f]{6}$/i.test(s);

function read(): Appearance {
  const a = { ...defaultAppearance };
  try {
    const saved = JSON.parse(
      localStorage.getItem(storageKey) ?? "null",
    ) as Partial<Appearance> | null;
    if (saved && typeof saved === "object") {
      if (isTheme(saved.theme)) a.theme = saved.theme;
      if (isMotion(saved.motion)) a.motion = saved.motion;
      if (typeof saved.glow === "boolean") a.glow = saved.glow;
      if (typeof saved.posterTitles === "boolean") a.posterTitles = saved.posterTitles;
      if (typeof saved.queueSymbols === "boolean") a.queueSymbols = saved.queueSymbols;
      if (isHex(saved.customAccent)) a.customAccent = saved.customAccent;
      if (saved.customBase === "light" || saved.customBase === "dark")
        a.customBase = saved.customBase;
      return a;
    }
    for (const key of legacyThemeKeys) {
      const t = localStorage.getItem(key);
      if (isThemeId(t)) {
        a.theme = t;
        break;
      }
    }
  } catch {
    // Storage can be unavailable (private windows): use the defaults.
  }
  return a;
}

const prefersLight = () =>
  typeof window.matchMedia === "function" &&
  window.matchMedia("(prefers-color-scheme: light)").matches;

/** The built-in theme whose colours are used: "system" and "custom" resolve to one. */
export function resolveTheme(a: Appearance): ThemeId {
  if (a.theme === "custom") return a.customBase;
  if (a.theme === "system") return prefersLight() ? "light" : "dark";
  return a.theme;
}

const customVarNames = Object.keys(buildCustomTheme("#8b5cf6", "dark")) as (keyof ThemeVars)[];

function apply(a: Appearance) {
  const root = document.documentElement;
  const id = resolveTheme(a);
  root.dataset.theme = id;
  root.classList.toggle("dark", themes.find((t) => t.id === id)?.dark ?? true);
  root.dataset.motion = a.motion;
  root.dataset.glow = a.glow ? "on" : "off";
  const vars = a.theme === "custom" ? buildCustomTheme(a.customAccent, a.customBase) : null;
  for (const name of customVarNames) {
    if (vars) root.style.setProperty(`--${name}`, vars[name]);
    else root.style.removeProperty(`--${name}`);
  }
  return vars;
}

function save(a: Appearance, vars: ThemeVars | null) {
  try {
    // The Custom theme's colours are saved too, so index.html can apply them
    // before the first paint without this code.
    localStorage.setItem(storageKey, JSON.stringify({ ...a, customVars: vars ?? undefined }));
    for (const key of legacyThemeKeys) localStorage.removeItem(key);
  } catch {
    // Ignore: the choice just won't be remembered.
  }
}

// The shared store.
let current: Appearance | null = null;
const listeners = new Set<() => void>();

function get(): Appearance {
  if (!current) {
    current = read();
    apply(current);
  }
  return current;
}

function set(patch: Partial<Appearance>) {
  current = { ...get(), ...patch };
  save(current, apply(current));
  for (const l of listeners) l();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  // "Match system" follows the browser while the page is open.
  const mq =
    typeof window.matchMedia === "function"
      ? window.matchMedia("(prefers-color-scheme: light)")
      : null;
  const onChange = () => {
    if (get().theme === "system") {
      apply(get());
      for (const l of listeners) l();
    }
  };
  mq?.addEventListener("change", onChange);
  return () => {
    listeners.delete(listener);
    mq?.removeEventListener("change", onChange);
  };
}

/** The current appearance and a way to change part of it. */
export function useAppearance() {
  const appearance = useSyncExternalStore(subscribe, get, get);
  const update = useCallback((patch: Partial<Appearance>) => set(patch), []);
  return { appearance, update, resolved: resolveTheme(appearance) };
}

/** The chosen theme, or "system" to follow the browser; remembered per browser. */
export function useTheme() {
  const { appearance, update } = useAppearance();
  const setTheme = useCallback((t: Theme) => update({ theme: t }), [update]);
  return { theme: appearance.theme, setTheme };
}

/** For tests: forget the cached choice so the next read comes from storage. */
export function resetAppearanceForTests() {
  current = null;
}
