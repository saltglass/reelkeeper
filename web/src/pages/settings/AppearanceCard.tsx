import { Check } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Segmented } from "@/components/ui/segmented";
import { Switch } from "@/components/ui/switch";
import { type Motion, type Theme, themes, useAppearance } from "@/hooks/theme";
import { accentPresets, buildCustomTheme, contrastChecks } from "@/lib/color";
import { cn } from "@/lib/utils";

const motionChoices: { value: Motion; label: string; help: string }[] = [
  {
    value: "lively",
    label: "Lively",
    help: "Pages glide in, posters lift and shine, progress shimmers.",
  },
  {
    value: "subtle",
    label: "Subtle",
    help: "Faster, quieter transitions. Ambient effects pause.",
  },
  {
    value: "off",
    label: "Off",
    help: "No motion at all. Reelkeeper also does this when your system asks for reduced motion.",
  },
];

const orange = "#f0a04b";

type Swatch = {
  id: Theme;
  name: string;
  /** CSS backgrounds for the preview: the sheet, a strip of film, the accent
   * button and the edge print. */
  ground: string;
  film: string;
  chip: string;
  edge: string;
};

/**
 * Settings → General → Appearance: theme, the Custom accent, motion, poster
 * titles and symbols on the Queue buttons. Everything applies at once
 * and is remembered in this browser only (hooks/theme.ts), so there's no Save button.
 */
export function AppearanceCard() {
  const { appearance, update, resolved } = useAppearance();
  const custom = buildCustomTheme(appearance.customAccent, appearance.customBase);

  const swatches: Swatch[] = [
    {
      id: "system",
      name: "Match system",
      ground: "linear-gradient(135deg, #1b1a19 0 50%, #efebe3 50% 100%)",
      film: "#0b0b0a",
      chip: "#f0a04b",
      edge: "#e08a2e",
    },
    ...themes.map((t) => ({
      id: t.id as Theme,
      name: t.name,
      ground: t.swatch[0],
      film: t.swatch[1],
      chip: t.swatch[2],
      edge: t.swatch[3],
    })),
    {
      id: "custom",
      name: "Custom",
      ground: appearance.customBase === "light" ? "#efebe3" : "#1b1a19",
      film: "#0b0b0a",
      chip: custom.primary,
      edge: custom.edge,
    },
  ];

  const checks = contrastChecks(custom, appearance.customBase);
  const allPass = checks.every((c) => c.pass);
  const motion = motionChoices.find((m) => m.value === appearance.motion) ?? motionChoices[0];
  const resolvedName = themes.find((t) => t.id === resolved)?.name ?? "Reelkeeper Dark";
  const accent = appearance.customAccent.toLowerCase();

  return (
    <Card className="animate-rise">
      <CardHeader className="flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1 space-y-1">
          <CardTitle>Appearance</CardTitle>
          <CardDescription>
            Pick a theme and how lively Reelkeeper feels. Changes apply right away, just for you.
          </CardDescription>
        </div>
        <Badge>Saved automatically</Badge>
      </CardHeader>
      <CardContent>
        <div className="grid grid-cols-[repeat(auto-fill,minmax(132px,1fr))] gap-3">
          {swatches.map((t) => {
            const on = appearance.theme === t.id;
            return (
              <button
                key={t.id}
                type="button"
                aria-pressed={on}
                onClick={() => update({ theme: t.id })}
                className={cn(
                  "grease cursor-pointer border bg-background p-2 text-left hover:border-input",
                  on && "border-primary",
                )}
              >
                <span
                  className="relative block h-16 overflow-hidden"
                  style={{ background: t.ground }}
                  aria-hidden="true"
                >
                  <span
                    className="absolute top-2 left-2 h-2.5 w-7"
                    style={{ background: t.chip }}
                  />
                  <span
                    className="absolute right-2 bottom-2 left-2 h-7"
                    style={{
                      background: `repeating-linear-gradient(90deg, transparent 0 4px, #33312e 4px 8px) 0 2px / 100% 3px no-repeat, repeating-linear-gradient(90deg, transparent 0 4px, #33312e 4px 8px) 0 calc(100% - 2px) / 100% 3px no-repeat, ${t.film}`,
                    }}
                  />
                  <span
                    className="absolute bottom-[19px] left-3.5 h-1 w-10"
                    style={{ background: t.edge }}
                  />
                </span>
                <span className="mt-2 flex items-center justify-between gap-1.5 px-0.5 text-xs/4 font-medium">
                  <span>{t.name}</span>
                  {on && (
                    <Check
                      className="animate-pop-in size-3.5 text-primary"
                      strokeWidth={3}
                      aria-hidden="true"
                    />
                  )}
                </span>
              </button>
            );
          })}
        </div>

        {appearance.theme === "system" && (
          <p className="animate-crumb-in text-sm text-muted-foreground">
            Following your computer's appearance, which is {resolved === "light" ? "light" : "dark"}{" "}
            right now, so Reelkeeper is using {resolvedName}. It switches by itself when your system
            does.
          </p>
        )}

        {appearance.theme === "custom" && (
          <div className="animate-rise flex flex-col gap-4 border border-input bg-background p-4">
            <div className="flex flex-wrap items-center gap-3.5">
              <label className="relative size-12 flex-none cursor-pointer">
                <input
                  type="color"
                  aria-label="Accent colour"
                  value={appearance.customAccent}
                  onChange={(e) => update({ customAccent: e.target.value })}
                  className="peer absolute inset-0 size-full cursor-pointer opacity-0"
                />
                <span
                  className="block size-12 ring-1 ring-border ring-offset-[3px] ring-offset-card transition-[transform,background-color] duration-[calc(320ms*var(--m))] [transition-timing-function:cubic-bezier(.34,1.56,.64,1)] peer-hover:scale-[1.08] peer-focus-visible:outline-2 peer-focus-visible:outline-offset-4 peer-focus-visible:outline-ring"
                  style={{ background: appearance.customAccent }}
                />
              </label>
              <div>
                <div className="text-sm font-medium">Accent</div>
                <div className="font-mono text-sm text-muted-foreground uppercase">
                  {appearance.customAccent}
                </div>
              </div>
              {/* biome-ignore lint/a11y/useSemanticElements: a plain row of round buttons, not a form fieldset */}
              <div
                className="ml-auto flex flex-wrap gap-2"
                role="group"
                aria-label="Suggested accents"
              >
                {accentPresets.map((p) => (
                  <button
                    key={p.hex}
                    type="button"
                    aria-label={p.name}
                    aria-pressed={p.hex === accent}
                    onClick={() => update({ customAccent: p.hex })}
                    className={cn(
                      "size-8 cursor-pointer  border-2 border-transparent transition-[transform,box-shadow] duration-[calc(300ms*var(--m))] [transition-timing-function:cubic-bezier(.34,1.56,.64,1)] hover:scale-[1.15]",
                      p.hex === accent &&
                        "ring-2 ring-foreground ring-offset-2 ring-offset-background",
                    )}
                    style={{ background: p.hex }}
                  />
                ))}
              </div>
            </div>

            <div className="flex flex-wrap items-center gap-3.5">
              <span className="min-w-12 text-sm font-medium">Base</span>
              <Segmented
                label="Base"
                value={appearance.customBase}
                onChange={(customBase) => update({ customBase })}
                options={[
                  { value: "dark", label: "Dark" },
                  { value: "light", label: "Light" },
                ]}
              />
              <span className="text-sm text-muted-foreground">
                Your accent replaces the orange: buttons, links, edge print and the focus ring.
                Reelkeeper adjusts it until every pair is easy to read.
              </span>
            </div>

            <div className="grid grid-cols-[repeat(auto-fill,minmax(150px,1fr))] gap-2.5">
              {(
                [
                  ["Buttons", custom.primary],
                  ["Links and edge print", custom.link],
                  ["Logo", custom.logo],
                ] as const
              ).map(([name, hex], i) => (
                <div
                  key={name}
                  className="animate-rise flex items-center gap-2.5 rounded-lg border bg-card p-2"
                  style={{ animationDelay: `calc(${i * 40}ms * var(--m))` }}
                >
                  <i
                    className="size-7 flex-none rounded-md ring-1 ring-inset ring-[rgba(127,127,127,.25)]"
                    style={{ background: hex }}
                  />
                  <div className="min-w-0">
                    <div className="text-xs/4 font-medium">{name}</div>
                    <div className="font-mono text-xs text-muted-foreground uppercase">{hex}</div>
                  </div>
                </div>
              ))}
            </div>

            <div>
              <div className="mb-2 text-sm font-medium">Contrast check</div>
              <ul className="flex flex-wrap gap-2">
                {checks.map((c) => (
                  <li key={c.label}>
                    <Badge variant={c.pass ? "success" : "warning"} className="px-2.5 py-1">
                      <span className="size-1.5 bg-current" aria-hidden="true" />
                      {c.label} {c.ratio.toFixed(1)}:1
                      <span className="sr-only">{c.pass ? " passes" : " falls short"}</span>
                    </Badge>
                  </li>
                ))}
              </ul>
              <p className="mt-2 text-sm text-muted-foreground">
                {allPass
                  ? "Every pair meets the same contrast bar as the built-in themes."
                  : "Some pairs fall short. Reelkeeper still applies the theme, but try a deeper or brighter accent."}
              </p>
            </div>

            <div className="flex justify-end">
              <Button
                type="button"
                variant="ghost"
                onClick={() => update({ customAccent: orange })}
              >
                Reset to orange
              </Button>
            </div>
          </div>
        )}

        <div className="space-y-2">
          <span className="text-sm font-medium">Motion</span>
          <div className="flex flex-col gap-2">
            <Segmented
              label="Motion"
              value={appearance.motion}
              onChange={(m) => update({ motion: m })}
              options={motionChoices.map(({ value, label }) => ({ value, label }))}
            />
            <p key={motion?.value} className="animate-crumb-in text-sm text-muted-foreground">
              {motion?.help}
            </p>
          </div>
        </div>

        <div className="flex items-center gap-4">
          <Switch
            id="poster-titles"
            checked={appearance.posterTitles}
            onCheckedChange={(posterTitles) => update({ posterTitles })}
            aria-describedby="poster-titles-help"
          />
          <div>
            <label htmlFor="poster-titles" className="cursor-pointer text-sm font-medium">
              Titles under posters
            </label>
            <p id="poster-titles-help" className="text-sm text-muted-foreground">
              Show each title and year below its poster in Movies and Series. Turn off for a cleaner
              grid; the poster usually shows the name anyway.
            </p>
          </div>
        </div>

        <div className="flex items-center gap-4">
          <Switch
            id="queue-symbols"
            checked={appearance.queueSymbols}
            onCheckedChange={(queueSymbols) => update({ queueSymbols })}
            aria-describedby="queue-symbols-help"
          />
          <div>
            <label htmlFor="queue-symbols" className="cursor-pointer text-sm font-medium">
              Symbols on Queue buttons
            </label>
            <p id="queue-symbols-help" className="text-sm text-muted-foreground">
              Show Retry import, Pause, Resume, Move to top and Remove on Activity → Queue as
              symbols. Turn off to show them as words.
            </p>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
