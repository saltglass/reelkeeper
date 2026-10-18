import type { ReactNode } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import type { ProfileChoice, ProfileOptions, ProfileSimple } from "@/hooks/profiles";
import { cn } from "@/lib/utils";
import { NumberBox, Segmented, selectClass, TagInput } from "./ProfileWidgets";

type Props = {
  options: ProfileOptions;
  value: ProfileSimple;
  onChange: (v: ProfileSimple) => void;
  /** Field errors from the server, keyed "simple.<field>". */
  errors: Record<string, string>;
  /** Profiles for movies show sizes for a 2-hour movie; TV ones per hour. */
  forMovies: boolean;
};

const MOVIE_HOURS = 2;

const groupTitles: Record<string, { title: string; help: string }> = {
  picture: { title: "Picture", help: "What the video looks like." },
  sound: { title: "Sound", help: "What the audio is." },
  release: { title: "Release", help: "Where a release comes from and what it contains." },
  edition: { title: "Edition", help: "Special cuts and versions." },
};

/** A tier can only exist at some resolutions: DVD is SD only, Remux is 1080p and up. */
function rungExists(resolution: number, tier: string) {
  if (tier === "dvd") return resolution === 480;
  if (tier === "remux") return resolution >= 1080;
  return true;
}

function rungLabel(options: ProfileOptions, resolution: number, tier: string) {
  const res = resolution === 480 ? "SD" : `${resolution}p`;
  const name = options.tiers.find((t) => t.id === tier)?.name ?? tier;
  return `${res} ${name}`;
}

/** The "upgrade until" choices: every accepted resolution and source, best first. */
function rungChoices(options: ProfileOptions, v: ProfileSimple) {
  const ids = options.tiers.map((t) => t.id);
  const lo = ids.indexOf(v.lowestSource);
  const hi = ids.indexOf(v.highestSource);
  const out: { resolution: number; source: string }[] = [];
  for (const resolution of [...v.resolutions].sort((a, b) => a - b)) {
    ids.forEach((source, i) => {
      if (i >= lo && i <= hi && rungExists(resolution, source)) out.push({ resolution, source });
    });
  }
  return out.reverse();
}

const rungKey = (r: { resolution: number; source: string }) => `${r.resolution}:${r.source}`;

/** After a change, make sure "upgrade until" is still something accepted. */
function tidy(options: ProfileOptions, v: ProfileSimple): ProfileSimple {
  const choices = rungChoices(options, v);
  if (choices.length === 0 || choices.some((c) => rungKey(c) === rungKey(v.upgradeUntil))) return v;
  return choices[0] ? { ...v, upgradeUntil: choices[0] } : v;
}

function Section({
  title,
  help,
  error,
  children,
}: {
  title: string;
  help: ReactNode;
  error?: string;
  children: ReactNode;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
        <p className="text-sm text-muted-foreground">{help}</p>
      </CardHeader>
      <CardContent className="space-y-4">
        {children}
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
      </CardContent>
    </Card>
  );
}

export function ProfileSimpleEditor({ options, value: v, onChange, errors, forMovies }: Props) {
  const set = (patch: Partial<ProfileSimple>) => onChange(tidy(options, { ...v, ...patch }));
  const tiersBestFirst = [...options.tiers].reverse();
  const choices = rungChoices(options, v);
  const factor = forMovies ? MOVIE_HOURS : 1;
  const sizeUnit = forMovies ? "GB for a 2-hour movie" : "GB per hour of TV";
  const round = (n: number) => Math.round(n * 100) / 100;

  function setSize(res: number, which: "min" | "max", shown: number) {
    const cur = v.sizes?.[String(res)] ?? { min: 0, max: 0 };
    const next = { ...cur, [which]: shown === 0 ? 0 : shown / factor };
    const sizes = { ...v.sizes };
    if (next.min === 0 && next.max === 0) delete sizes[String(res)];
    else sizes[String(res)] = next;
    set({ sizes });
  }

  function setFeature(id: string, c: ProfileChoice | "") {
    const features = { ...v.features };
    if (c === "") delete features[id];
    else features[id] = c;
    set({ features });
  }

  const groups = ["picture", "sound", "release", "edition"];
  return (
    <div className="space-y-6">
      <Section
        title="Resolutions"
        help="Which picture sizes this profile may download. Tick every one you'd accept."
        error={errors["simple.resolutions"]}
      >
        <div className="flex flex-wrap gap-2">
          {options.resolutions.map((r) => (
            <label key={r.value} className="cursor-pointer">
              <input
                type="checkbox"
                className="peer sr-only"
                checked={v.resolutions.includes(r.value)}
                onChange={(e) =>
                  set({
                    resolutions: e.target.checked
                      ? [...v.resolutions, r.value].sort((a, b) => a - b)
                      : v.resolutions.filter((x) => x !== r.value),
                  })
                }
              />
              <span className="inline-flex min-h-9 items-center gap-1.5 rounded-full border border-input bg-card px-3.5 text-sm font-medium transition-[background-color,border-color,color] duration-[calc(200ms*var(--m))] hover:bg-muted peer-checked:border-primary peer-checked:bg-primary/10 peer-checked:text-primary peer-focus-visible:ring-2 peer-focus-visible:ring-ring">
                {r.label}
              </span>
            </label>
          ))}
        </div>
      </Section>

      <Section
        title="Sources"
        help="Where the video comes from, best first: Remux (untouched Blu-ray), Blu-ray encode, WEB-DL (a streaming service's own file), WEBRip (recorded from a stream), DVD, HDTV (recorded from TV). Choose the worst you'd settle for and the best you'd want."
        error={errors["simple.sources"]}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label htmlFor="ps-lowest">Lowest acceptable</Label>
            <select
              id="ps-lowest"
              className={selectClass}
              value={v.lowestSource}
              onChange={(e) => set({ lowestSource: e.target.value })}
            >
              {tiersBestFirst.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="ps-highest">Highest wanted</Label>
            <select
              id="ps-highest"
              className={selectClass}
              value={v.highestSource}
              onChange={(e) => set({ highestSource: e.target.value })}
            >
              {tiersBestFirst.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
          </div>
        </div>
        <div className="flex items-start justify-between gap-4">
          <div className="space-y-1">
            <Label htmlFor="ps-webequal">Count WEB-DL and WEBRip as equal</Label>
            <p className="text-sm text-muted-foreground">
              They look almost the same. When on, neither is an upgrade over the other.
            </p>
          </div>
          <Switch
            id="ps-webequal"
            checked={v.webEqual}
            onCheckedChange={(webEqual) => set({ webEqual })}
          />
        </div>
      </Section>

      <Section
        title="Upgrades"
        help="After a movie is downloaded, Reelkeeper can keep looking for a better release and replace the file."
        error={errors["simple.upgradeUntil"]}
      >
        <div className="flex items-start justify-between gap-4">
          <div className="space-y-1">
            <Label htmlFor="ps-upgrades">Upgrade to better releases</Label>
            <p className="text-sm text-muted-foreground">
              Off means the first release that fits is final.
            </p>
          </div>
          <Switch
            id="ps-upgrades"
            checked={v.upgrades}
            onCheckedChange={(upgrades) => set({ upgrades })}
          />
        </div>
        {v.upgrades && (
          <div className="space-y-1.5">
            <Label htmlFor="ps-until">Upgrade until</Label>
            <select
              id="ps-until"
              className={selectClass}
              value={rungKey(v.upgradeUntil)}
              onChange={(e) => {
                const [resolution, source = ""] = e.target.value.split(":");
                set({ upgradeUntil: { resolution: Number(resolution), source } });
              }}
            >
              {choices.map((c) => (
                <option key={rungKey(c)} value={rungKey(c)}>
                  {rungLabel(options, c.resolution, c.source)}
                </option>
              ))}
            </select>
            <p className="text-sm text-muted-foreground">
              Once a file is this good, Reelkeeper stops looking, even if a release it would rank
              higher comes out later. Only qualities you accept are listed.
            </p>
          </div>
        )}
        <div className="flex items-start justify-between gap-4">
          <div className="space-y-1">
            <Label htmlFor="ps-propers">Prefer PROPER and REPACK releases</Label>
            <p className="text-sm text-muted-foreground">
              A PROPER or REPACK is a fixed copy of a flawed release. When on, it replaces the
              original.
            </p>
          </div>
          <Switch
            id="ps-propers"
            checked={v.preferPropers}
            onCheckedChange={(preferPropers) => set({ preferPropers })}
          />
        </div>
      </Section>

      <Section
        title="Size limits"
        help={`Reject releases that are too small (often poor quality) or too big, for each resolution you accept. Leave a box empty for no limit. ${
          forMovies
            ? "Shown for a 2-hour movie; longer and shorter movies scale to match."
            : "Shown per hour of TV."
        }`}
        error={errors["simple.sizes"]}
      >
        {v.resolutions.length === 0 ? (
          <p className="text-sm text-muted-foreground">Choose a resolution above first.</p>
        ) : (
          <div className="space-y-3">
            {[...v.resolutions]
              .sort((a, b) => a - b)
              .map((res) => {
                const s = v.sizes?.[String(res)];
                const name = options.resolutions.find((r) => r.value === res)?.label ?? `${res}p`;
                return (
                  <div key={res} className="grid items-end gap-2 sm:grid-cols-[1fr_8rem_8rem]">
                    <span className="text-sm font-medium">{name}</span>
                    <div className="space-y-1">
                      <Label htmlFor={`ps-min-${res}`} className="text-xs text-muted-foreground">
                        Smallest ({sizeUnit})
                      </Label>
                      <NumberBox
                        id={`ps-min-${res}`}
                        label={`${res}p smallest, ${sizeUnit}`}
                        value={round((s?.min ?? 0) * factor)}
                        placeholder="No limit"
                        onChange={(n) => setSize(res, "min", n)}
                      />
                    </div>
                    <div className="space-y-1">
                      <Label htmlFor={`ps-max-${res}`} className="text-xs text-muted-foreground">
                        Biggest ({sizeUnit})
                      </Label>
                      <NumberBox
                        id={`ps-max-${res}`}
                        label={`${res}p biggest, ${sizeUnit}`}
                        value={round((s?.max ?? 0) * factor)}
                        placeholder="No limit"
                        onChange={(n) => setSize(res, "max", n)}
                      />
                    </div>
                  </div>
                );
              })}
          </div>
        )}
      </Section>

      <Section
        title="Languages"
        help="Which spoken languages are acceptable. A release name that doesn't mention a language is taken to be in the movie's original language."
        error={errors["simple.languages"]}
      >
        <div className="grid gap-2 sm:grid-cols-3" role="radiogroup" aria-label="Languages">
          {[
            { mode: "any", label: "Any language" },
            { mode: "original", label: "The movie's original language" },
            { mode: "list", label: "Only these" },
          ].map((o) => (
            <label
              key={o.mode}
              className={cn(
                "flex cursor-pointer items-center gap-2 rounded-lg border p-3 text-sm transition-colors duration-[calc(200ms*var(--m))] hover:bg-muted/50",
                v.languages.mode === o.mode && "border-primary bg-accent/50",
              )}
            >
              <input
                type="radio"
                name="ps-lang-mode"
                checked={v.languages.mode === o.mode}
                onChange={() =>
                  set({
                    languages: {
                      mode: o.mode as ProfileSimple["languages"]["mode"],
                      list: o.mode === "list" ? v.languages.list : undefined,
                    },
                  })
                }
              />
              {o.label}
            </label>
          ))}
        </div>
        {v.languages.mode === "list" && (
          <fieldset
            className="grid max-h-56 gap-x-4 gap-y-1 overflow-y-auto rounded-md border p-3 sm:grid-cols-3"
            aria-label="Accepted languages"
          >
            {options.languages.map((l) => (
              <label key={l} className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={v.languages.list?.includes(l) ?? false}
                  onChange={(e) => {
                    const cur = v.languages.list ?? [];
                    set({
                      languages: {
                        mode: "list",
                        list: e.target.checked ? [...cur, l] : cur.filter((x) => x !== l),
                      },
                    });
                  }}
                />
                {l}
              </label>
            ))}
          </fieldset>
        )}
      </Section>

      {groups.map((g) => {
        const feats = options.features.filter((f) => f.group === g);
        if (feats.length === 0) return null;
        return (
          <Section
            key={g}
            title={groupTitles[g]?.title ?? g}
            help={groupTitles[g]?.help ?? ""}
            error={g === "picture" ? errors["simple.features"] : undefined}
          >
            <p className="text-sm text-muted-foreground">
              Prefer ranks matching releases higher, Avoid ranks them lower, Require rejects
              releases without it, and Block rejects releases with it. Prefer and Avoid only choose
              between the releases a search finds; they never start another download by themselves.
            </p>
            <div className="divide-y">
              {feats.map((f) => {
                const choiceOptions: { value: ProfileChoice | ""; label: string }[] = [
                  { value: "", label: "Don't care" },
                  { value: "prefer", label: "Prefer" },
                  { value: "avoid", label: "Avoid" },
                  ...(f.canRequire ? [{ value: "require" as const, label: "Require" }] : []),
                  { value: "block", label: "Block" },
                ];
                return (
                  <div
                    key={f.id}
                    className="flex flex-col gap-2 py-3 first:pt-0 last:pb-0 md:flex-row md:items-center md:justify-between"
                  >
                    <div className="min-w-0 space-y-0.5">
                      <div className="text-sm font-medium">{f.label}</div>
                      {f.help && <p className="text-sm text-muted-foreground">{f.help}</p>}
                    </div>
                    <Segmented
                      label={f.label}
                      value={v.features?.[f.id] ?? ""}
                      options={choiceOptions}
                      onChange={(c) => setFeature(f.id, c)}
                    />
                  </div>
                );
              })}
            </div>
          </Section>
        );
      })}

      <Section
        title="Release groups"
        help="Groups are the people who make a release; their name ends the release name (Movie.2021.1080p.BluRay-GROUP)."
        error={errors["simple.groups"]}
      >
        <div className="space-y-1.5">
          <Label htmlFor="ps-prefer-groups">Prefer these groups</Label>
          <TagInput
            id="ps-prefer-groups"
            value={v.preferGroups ?? []}
            onChange={(preferGroups) => set({ preferGroups })}
            placeholder="Type a group name and press Enter"
          />
          <p className="text-sm text-muted-foreground">
            Their releases rank higher than others of the same quality.
          </p>
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="ps-block-groups">Block these groups</Label>
          <TagInput
            id="ps-block-groups"
            value={v.blockGroups ?? []}
            onChange={(blockGroups) => set({ blockGroups })}
            placeholder="Type a group name and press Enter"
          />
          <p className="text-sm text-muted-foreground">Their releases are never downloaded.</p>
        </div>
      </Section>
    </div>
  );
}
