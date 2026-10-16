import { ArrowDown, ArrowUp, Check, Copy, Trash2, X } from "lucide-react";
import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input, Textarea } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import type { CustomFormat, ProfileCompiled } from "@/hooks/profiles";
import { NumberBox, selectClass } from "./ProfileWidgets";

type Required = NonNullable<ProfileCompiled["required"]>[number];

/** Server lists can come back as null when empty. */
export function normalizeCompiled(c: ProfileCompiled): ProfileCompiled {
  return {
    ...c,
    items: c.items ?? [],
    formats: c.formats ?? [],
    scores: c.scores ?? {},
    required: c.required ?? [],
    sizes: c.sizes ?? {},
  };
}

const mbPerMin = (n: number) => `${Math.round(n * 100) / 100} MB/min`;

/** Read-only view of what the simple settings compile to. */
export function CompiledSummary({ value: c }: { value: ProfileCompiled }) {
  const v = normalizeCompiled(c);
  const sizes = Object.entries(v.sizes ?? {}).filter(([, s]) => s.min > 0 || s.max > 0);
  const formats = v.formats.filter((f) => (v.scores?.[f.name] ?? 0) !== 0);
  return (
    <div className="space-y-4 text-sm">
      <p className="text-muted-foreground">
        This is what your simple settings turn into. It is the form Radarr uses, so you can see
        exactly what the profile does.
      </p>
      <div>
        <h3 className="font-medium">Qualities, best first</h3>
        <ul className="mt-1 columns-1 gap-6 sm:columns-2">
          {[...v.items].reverse().map((it) => (
            <li key={it.name} className="flex items-center gap-2">
              {it.allowed ? (
                <Check className="size-4 text-success" aria-label="Allowed" />
              ) : (
                <X className="size-4 text-muted-foreground" aria-label="Not allowed" />
              )}
              <span className={it.allowed ? "" : "text-muted-foreground"}>{it.name}</span>
            </li>
          ))}
        </ul>
      </div>
      <p>
        <span className="font-medium">Upgrades:</span>{" "}
        {v.upgradesAllowed ? `until ${v.upgradeUntil}` : "off"}
        {v.preferPropers ? "; PROPER and REPACK preferred" : ""}
      </p>
      {formats.length > 0 && (
        <div>
          <h3 className="font-medium">Custom formats and their scores</h3>
          <ul className="mt-1 columns-1 gap-6 sm:columns-2">
            {formats.map((f) => (
              <li key={f.name} className="flex justify-between gap-2">
                <span>{f.name}</span>
                <span className="tabular-nums text-muted-foreground">{v.scores?.[f.name]}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
      {(v.required?.length ?? 0) > 0 && (
        <div>
          <h3 className="font-medium">A release must have</h3>
          <ul className="mt-1 list-disc pl-5">
            {v.required?.map((r) => (
              <li key={r.label}>
                {r.label} <span className="text-muted-foreground">({r.anyOf.join(" or ")})</span>
              </li>
            ))}
          </ul>
        </div>
      )}
      {sizes.length > 0 && (
        <div>
          <h3 className="font-medium">Size limits</h3>
          <ul className="mt-1 columns-1 gap-6 sm:columns-2">
            {sizes.map(([q, s]) => (
              <li key={q}>
                {q}: {s.min > 0 ? mbPerMin(s.min) : "no minimum"} to{" "}
                {s.max > 0 ? mbPerMin(s.max) : "no maximum"}
              </li>
            ))}
          </ul>
        </div>
      )}
      <p className="text-muted-foreground">
        Minimum total score: {v.minScore}. A better score raises a release only if it adds at least{" "}
        {v.minUpgradeStep}.
      </p>
    </div>
  );
}

function Block({
  title,
  help,
  children,
}: {
  title: string;
  help: string;
  children: React.ReactNode;
}) {
  return (
    <section className="space-y-3">
      <div>
        <h3 className="font-medium">{title}</h3>
        <p className="text-sm text-muted-foreground">{help}</p>
      </div>
      {children}
    </section>
  );
}

function Row({
  id,
  label,
  help,
  children,
}: {
  id: string;
  label: string;
  help: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      <p className="text-sm text-muted-foreground">{help}</p>
    </div>
  );
}

/** Parses pasted Radarr / TRaSH Guides JSON: one format or a list of them. */
export function parseFormats(text: string): CustomFormat[] {
  let data: unknown;
  try {
    data = JSON.parse(text);
  } catch {
    throw new Error(
      "That isn't valid JSON. Paste the whole text copied from TRaSH Guides or Radarr.",
    );
  }
  const list = Array.isArray(data) ? data : [data];
  if (list.length === 0) throw new Error("The list is empty.");
  return list.map((raw, i) => {
    const f = raw as Partial<CustomFormat> | null;
    if (!f || typeof f !== "object" || typeof f.name !== "string" || f.name.trim() === "") {
      throw new Error(`Format ${i + 1} has no name. A custom format needs a "name".`);
    }
    if (!Array.isArray(f.specifications)) {
      throw new Error(`"${f.name}" has no "specifications" list.`);
    }
    return f as CustomFormat;
  });
}

export function AdvancedEditor({
  value: v,
  onChange,
  error,
}: {
  value: ProfileCompiled;
  onChange: (v: ProfileCompiled) => void;
  error?: string;
}) {
  const [importText, setImportText] = useState("");
  const [importError, setImportError] = useState("");
  const [copied, setCopied] = useState("");
  const set = (patch: Partial<ProfileCompiled>) => onChange({ ...v, ...patch });
  const formats = v.formats;
  const scores = v.scores ?? {};
  const required = v.required ?? [];
  const sizes = v.sizes ?? {};
  const allowed = v.items.filter((i) => i.allowed);

  function move(i: number, by: number) {
    const items = [...v.items];
    [items[i], items[i + by]] = [
      items[i + by] as (typeof items)[number],
      items[i] as (typeof items)[number],
    ];
    set({ items });
  }
  function setItem(i: number, allowedNow: boolean) {
    set({ items: v.items.map((it, j) => (j === i ? { ...it, allowed: allowedNow } : it)) });
  }
  function importFormats() {
    try {
      const added = parseFormats(importText);
      const have = new Set(formats.map((f) => f.name.toLowerCase()));
      for (const f of added) {
        if (have.has(f.name.toLowerCase()))
          throw new Error(`You already have a format called "${f.name}".`);
        have.add(f.name.toLowerCase());
      }
      set({ formats: [...formats, ...added] });
      setImportText("");
      setImportError("");
    } catch (e) {
      setImportError((e as Error).message);
    }
  }
  async function copy(f: CustomFormat) {
    try {
      await navigator.clipboard.writeText(JSON.stringify(f, null, 2));
      setCopied(f.name);
    } catch {
      setCopied("");
    }
  }
  function removeFormat(name: string) {
    const rest = { ...scores };
    delete rest[name];
    set({ formats: formats.filter((f) => f.name !== name), scores: rest });
  }
  function setRequired(i: number, patch: Partial<Required>) {
    set({ required: required.map((r, j) => (j === i ? { ...r, ...patch } : r)) });
  }
  function setSize(q: string, which: "min" | "max", n: number) {
    const cur = sizes[q] ?? { min: 0, max: 0 };
    const next = { ...cur, [which]: n };
    const all = { ...sizes };
    if (next.min === 0 && next.max === 0) delete all[q];
    else all[q] = next;
    set({ sizes: all });
  }

  const qualityNames = v.items.flatMap((it) => (it.qualities?.length ? it.qualities : [it.name]));

  return (
    <div className="space-y-8">
      {error && <Alert variant="error">{error}</Alert>}

      <Block
        title="Qualities"
        help="Worst at the bottom, best at the top. Only ticked qualities are downloaded. Use the arrows to change the order."
      >
        <ul className="divide-y rounded-md border">
          {[...v.items]
            .map((it, i) => ({ it, i }))
            .reverse()
            .map(({ it, i }) => (
              <li key={it.name} className="flex items-center gap-3 p-2">
                <input
                  type="checkbox"
                  aria-label={`Allow ${it.name}`}
                  checked={it.allowed}
                  onChange={(e) => setItem(i, e.target.checked)}
                />
                <div className="mr-auto min-w-0">
                  <div className="text-sm">{it.name}</div>
                  {it.qualities && it.qualities.length > 0 && (
                    <div className="text-xs text-muted-foreground">
                      Counts as equal: {it.qualities.join(", ")}
                    </div>
                  )}
                </div>
                <Button
                  type="button"
                  variant="tile"
                  size="icon"
                  aria-label={`Move ${it.name} up`}
                  disabled={i === v.items.length - 1}
                  onClick={() => move(i, 1)}
                >
                  <ArrowUp />
                </Button>
                <Button
                  type="button"
                  variant="tile"
                  size="icon"
                  aria-label={`Move ${it.name} down`}
                  disabled={i === 0}
                  onClick={() => move(i, -1)}
                >
                  <ArrowDown />
                </Button>
              </li>
            ))}
        </ul>
      </Block>

      <Block
        title="Upgrades"
        help="When Reelkeeper should replace a file it already has with a better release."
      >
        <div className="flex items-start justify-between gap-4">
          <div className="space-y-1">
            <Label htmlFor="pa-upgrades">Upgrades allowed</Label>
            <p className="text-sm text-muted-foreground">
              Off means the first release that fits is final.
            </p>
          </div>
          <Switch
            id="pa-upgrades"
            checked={v.upgradesAllowed}
            onCheckedChange={(upgradesAllowed) => set({ upgradesAllowed })}
          />
        </div>
        {v.upgradesAllowed && (
          <Row
            id="pa-until"
            label="Upgrade until"
            help="Quality upgrades stop once a file reaches this quality."
          >
            <select
              id="pa-until"
              className={selectClass}
              value={v.upgradeUntil}
              onChange={(e) => set({ upgradeUntil: e.target.value })}
            >
              {!allowed.some((i) => i.name === v.upgradeUntil) && (
                <option value={v.upgradeUntil}>{v.upgradeUntil || "Choose…"}</option>
              )}
              {[...allowed].reverse().map((i) => (
                <option key={i.name} value={i.name}>
                  {i.name}
                </option>
              ))}
            </select>
          </Row>
        )}
        <Row
          id="pa-until-score"
          label="Upgrade until score"
          help="Once the quality is reached, Reelkeeper keeps upgrading for a better score until it hits this number."
        >
          <NumberBox
            id="pa-until-score"
            label="Upgrade until score"
            allowNegative
            value={v.upgradeUntilScore}
            onChange={(n) => set({ upgradeUntilScore: n })}
          />
        </Row>
        <Row
          id="pa-step"
          label="Minimum score step"
          help="A release must beat the current file's score by at least this much to count as an upgrade on score alone."
        >
          <NumberBox
            id="pa-step"
            label="Minimum score step"
            value={v.minUpgradeStep}
            onChange={(n) => set({ minUpgradeStep: n })}
          />
        </Row>
        <Row
          id="pa-min"
          label="Minimum score"
          help="Releases whose total score is lower than this are rejected."
        >
          <NumberBox
            id="pa-min"
            label="Minimum score"
            allowNegative
            value={v.minScore}
            onChange={(n) => set({ minScore: n })}
          />
        </Row>
        <div className="flex items-start justify-between gap-4">
          <div className="space-y-1">
            <Label htmlFor="pa-propers">Prefer PROPER and REPACK releases</Label>
            <p className="text-sm text-muted-foreground">
              A fixed copy of a flawed release replaces the original.
            </p>
          </div>
          <Switch
            id="pa-propers"
            checked={v.preferPropers}
            onCheckedChange={(preferPropers) => set({ preferPropers })}
          />
        </div>
      </Block>

      <Block
        title="Custom formats"
        help="Rules that recognise things in a release name (a group, HDR, a language) and give it points. Higher totals win. Formats use the same JSON as Radarr and TRaSH Guides."
      >
        {formats.length === 0 ? (
          <p className="text-sm text-muted-foreground">No custom formats yet.</p>
        ) : (
          <div className="overflow-x-auto rounded-md border">
            <table className="w-full text-sm">
              <thead className="text-left text-xs text-muted-foreground">
                <tr>
                  <th className="p-2 font-medium">Name</th>
                  <th className="p-2 font-medium">Score</th>
                  <th className="p-2 font-medium">Conditions</th>
                  <th className="p-2" />
                </tr>
              </thead>
              <tbody className="divide-y">
                {formats.map((f) => (
                  <tr key={f.name}>
                    <td className="p-2 break-words">{f.name}</td>
                    <td className="w-28 p-2">
                      <NumberBox
                        id={`pa-score-${f.name}`}
                        label={`Score for ${f.name}`}
                        allowNegative
                        step="1"
                        value={scores[f.name] ?? 0}
                        onChange={(n) => set({ scores: { ...scores, [f.name]: n } })}
                      />
                    </td>
                    <td className="p-2 tabular-nums">{f.specifications.length}</td>
                    <td className="p-2">
                      <div className="flex justify-end gap-1">
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          onClick={() => copy(f)}
                          aria-label={`Copy JSON of ${f.name}`}
                        >
                          <Copy /> {copied === f.name ? "Copied" : "Copy JSON"}
                        </Button>
                        <Button
                          type="button"
                          variant="tile"
                          size="icon"
                          aria-label={`Delete ${f.name}`}
                          onClick={() => removeFormat(f.name)}
                        >
                          <Trash2 />
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <div className="space-y-2">
          <Label htmlFor="pa-import">Import custom format</Label>
          <Textarea
            id="pa-import"
            rows={4}
            value={importText}
            placeholder="Paste the JSON of one format, or a list of formats"
            onChange={(e) => setImportText(e.target.value)}
          />
          {importError && (
            <p role="alert" className="text-sm text-destructive">
              {importError}
            </p>
          )}
          <p className="text-sm text-muted-foreground">
            Reelkeeper checks the formats when you save. New formats start with a score of 0.
          </p>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={importText.trim() === ""}
            onClick={importFormats}
          >
            Add format
          </Button>
        </div>
      </Block>

      <Block
        title="Required"
        help="Things a release must have, such as HDR. A release is accepted only if it matches at least one of the listed formats for every line."
      >
        {required.length === 0 && (
          <p className="text-sm text-muted-foreground">Nothing is required.</p>
        )}
        {required.map((r, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: requirements have no stable id
          <div key={i} className="space-y-2 rounded-md border p-3">
            <div className="flex items-end gap-2">
              <div className="mr-auto w-full space-y-1.5">
                <Label htmlFor={`pa-req-${i}`}>Name shown in reasons</Label>
                <Input
                  id={`pa-req-${i}`}
                  value={r.label}
                  onChange={(e) => setRequired(i, { label: e.target.value })}
                />
              </div>
              <Button
                type="button"
                variant="tile"
                size="icon"
                aria-label={`Remove requirement ${r.label}`}
                onClick={() => set({ required: required.filter((_, j) => j !== i) })}
              >
                <Trash2 />
              </Button>
            </div>
            <fieldset className="space-y-1">
              <legend className="text-sm font-medium">Any of these formats</legend>
              {formats.length === 0 && (
                <p className="text-sm text-muted-foreground">Add a custom format first.</p>
              )}
              <div className="grid gap-x-4 gap-y-1 sm:grid-cols-2">
                {formats.map((f) => (
                  <label key={f.name} className="flex items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={r.anyOf.includes(f.name)}
                      onChange={(e) =>
                        setRequired(i, {
                          anyOf: e.target.checked
                            ? [...r.anyOf, f.name]
                            : r.anyOf.filter((x) => x !== f.name),
                        })
                      }
                    />
                    {f.name}
                  </label>
                ))}
              </div>
            </fieldset>
          </div>
        ))}
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => set({ required: [...required, { label: "", anyOf: [] }] })}
        >
          Add a requirement
        </Button>
      </Block>

      <Block
        title="Size limits"
        help="Smallest and biggest release for each quality, in megabytes per minute of runtime (a 2-hour movie is 120 minutes). Empty means no limit."
      >
        <div className="overflow-x-auto rounded-md border">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-muted-foreground">
              <tr>
                <th className="p-2 font-medium">Quality</th>
                <th className="p-2 font-medium">Smallest (MB/min)</th>
                <th className="p-2 font-medium">Biggest (MB/min)</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {qualityNames.map((q) => (
                <tr key={q}>
                  <td className="p-2">{q}</td>
                  <td className="w-36 p-2">
                    <NumberBox
                      id={`pa-smin-${q}`}
                      label={`${q} smallest`}
                      value={sizes[q]?.min ?? 0}
                      placeholder="No limit"
                      onChange={(n) => setSize(q, "min", n)}
                    />
                  </td>
                  <td className="w-36 p-2">
                    <NumberBox
                      id={`pa-smax-${q}`}
                      label={`${q} biggest`}
                      value={sizes[q]?.max ?? 0}
                      placeholder="No limit"
                      onChange={(n) => setSize(q, "max", n)}
                    />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Block>
    </div>
  );
}
