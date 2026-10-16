import { useMutation, useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Globe, Rss, Search } from "lucide-react";
import { type FormEvent, type ReactNode, useState } from "react";
import { api, RequestError, unwrap } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, SwitchRow } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import {
  type DefinitionDetail,
  type DefinitionSetting,
  type DefinitionSummary,
  definitionQuery,
  definitionsQuery,
  type Indexer,
  type IndexerInput,
  type IndexerKind,
  plainInfoText,
  solverQuery,
  useInvalidateIndexers,
} from "@/hooks/indexers";

const selectClass =
  "w-full min-h-10 rounded-lg border border-input bg-background px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none";

/** Standard categories. Reelkeeper only looks for movies and TV. */
const categoryChoices: { id: number; label: string; sub?: boolean }[] = [
  { id: 2000, label: "Movies (all)" },
  { id: 2030, label: "Movies SD", sub: true },
  { id: 2040, label: "Movies HD", sub: true },
  { id: 2045, label: "Movies UHD", sub: true },
  { id: 2050, label: "Movies Blu-ray", sub: true },
  { id: 2060, label: "Movies 3D", sub: true },
  { id: 2070, label: "Movies web", sub: true },
  { id: 2010, label: "Movies foreign", sub: true },
  { id: 2020, label: "Movies other", sub: true },
  { id: 5000, label: "TV (all)" },
  { id: 5030, label: "TV SD", sub: true },
  { id: 5040, label: "TV HD", sub: true },
  { id: 5045, label: "TV UHD", sub: true },
  { id: 5060, label: "TV sport", sub: true },
  { id: 5070, label: "TV anime", sub: true },
  { id: 5080, label: "TV documentary", sub: true },
  { id: 5020, label: "TV foreign", sub: true },
  { id: 5050, label: "TV other", sub: true },
];

const typeLabel: Record<string, string> = {
  public: "Public",
  "semi-private": "Semi-private",
  private: "Private",
};

/** Keys of the server's field errors that live under "Show advanced". */
const advancedFields = [
  "baseUrl",
  "priority",
  "categories",
  "queryLimit",
  "grabLimit",
  "packSeedTimeMinutes",
];

const cookieHelp =
  'This site needs your login cookie. Sign in to the site in your browser, open its developer tools (press F12), go to the Network tab, reload the page and click the first request. Under Request Headers, copy the whole value after "Cookie:" and paste it here.';
const solverHelp =
  "This site may use Cloudflare protection: turn on 'Use the Cloudflare solver' below.";
const userAgentHelp =
  'Use the same browser\'s user agent that the cookie came from (the "User-Agent" line next to the cookie in the developer tools).';

function hoursText(minutes?: number) {
  return minutes ? String(Math.round((minutes / 60) * 100) / 100) : "";
}
function minutesFromHours(s: string) {
  const n = Number.parseFloat(s);
  return s.trim() === "" || Number.isNaN(n) ? undefined : Math.round(n * 60);
}
function intOrUndefined(s: string) {
  const n = Number.parseInt(s, 10);
  return s.trim() === "" || Number.isNaN(n) ? undefined : n;
}

/** Add a new indexer, or edit an existing one. */
export function IndexerDialog({ indexer, onClose }: { indexer?: Indexer; onClose: () => void }) {
  // Which kind of indexer is being added: unset until chosen.
  const [kind, setKind] = useState<IndexerKind | undefined>(indexer?.kind);
  const [defId, setDefId] = useState<string | undefined>(indexer?.definition || undefined);
  const [picked, setPicked] = useState<DefinitionSummary | undefined>();

  let title = "Add an indexer";
  let description: ReactNode =
    "An indexer is a place Reelkeeper searches for releases. What kind do you want to add?";
  let body: ReactNode;
  if (indexer) {
    title = `Edit ${indexer.name}`;
    description = "Secrets such as passwords are stored safely and never shown again.";
  } else if (kind === "cardigann" && !defId) {
    title = "Add a tracker";
    description =
      "Pick the site from the list. Reelkeeper already knows how to talk to each of them.";
  } else if (kind) {
    title = "Add an indexer";
    description = "Fill in the details, test them, then save.";
  }

  if (!kind) {
    body = (
      <KindChooser
        onChoose={(k) => {
          setKind(k);
        }}
      />
    );
  } else if (kind === "cardigann" && !defId) {
    body = (
      <DefinitionPicker
        onBack={() => setKind(undefined)}
        onPick={(d) => {
          setPicked(d);
          setDefId(d.id);
        }}
      />
    );
  } else if (kind === "cardigann" && defId) {
    body = (
      <TrackerLoader
        key={defId}
        defId={defId}
        indexer={indexer}
        summary={picked}
        onBack={indexer ? undefined : () => setDefId(undefined)}
        onClose={onClose}
      />
    );
  } else {
    body = (
      <IndexerForm
        indexer={indexer}
        kind={kind}
        onBack={indexer ? undefined : () => setKind(undefined)}
        onClose={onClose}
      />
    );
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90svh] max-w-xl overflow-y-auto">
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>{description}</DialogDescription>
        {body}
      </DialogContent>
    </Dialog>
  );
}

function KindChooser({ onChoose }: { onChoose: (k: IndexerKind) => void }) {
  const choices: { kind: IndexerKind; icon: ReactNode; name: string; text: string }[] = [
    {
      kind: "cardigann",
      icon: <Globe />,
      name: "Tracker",
      text: "A torrent site from Reelkeeper's list of hundreds, public or private. You'll need your login for private ones.",
    },
    {
      kind: "torznab",
      icon: <Rss />,
      name: "Torznab or Newznab feed",
      text: "A web address plus an API key, for example from Jackett or another indexer manager.",
    },
    {
      kind: "knaben",
      icon: <Search />,
      name: "Knaben",
      text: "A public search engine that covers many torrent sites. No settings needed.",
    },
  ];
  return (
    <div className="space-y-2">
      {choices.map((c) => (
        <button
          key={c.kind}
          type="button"
          onClick={() => onChoose(c.kind)}
          className="flex w-full items-start gap-3 rounded-md border bg-card p-3 text-left hover:bg-muted [&_svg]:mt-0.5 [&_svg]:size-4 [&_svg]:shrink-0"
        >
          {c.icon}
          <span className="space-y-0.5">
            <span className="block text-sm font-medium">{c.name}</span>
            <span className="block text-sm text-muted-foreground">{c.text}</span>
          </span>
        </button>
      ))}
    </div>
  );
}

function DefinitionPicker({
  onPick,
  onBack,
}: {
  onPick: (d: DefinitionSummary) => void;
  onBack: () => void;
}) {
  const defs = useQuery(definitionsQuery);
  const [text, setText] = useState("");
  const [type, setType] = useState("");
  const needle = text.trim().toLowerCase();
  const matches = (defs.data?.definitions ?? []).filter(
    (d) =>
      (!type || d.type === type) &&
      (!needle ||
        d.name.toLowerCase().includes(needle) ||
        d.description.toLowerCase().includes(needle)),
  );
  const shown = matches.slice(0, 100);
  return (
    <div className="space-y-3">
      <div className="flex flex-col gap-2 sm:flex-row">
        <Input
          aria-label="Search trackers"
          placeholder="Search trackers…"
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
        <select
          aria-label="Type"
          className={`${selectClass} sm:w-44`}
          value={type}
          onChange={(e) => setType(e.target.value)}
        >
          <option value="">All types</option>
          <option value="public">Public</option>
          <option value="semi-private">Semi-private</option>
          <option value="private">Private</option>
        </select>
      </div>
      <p className="text-xs text-muted-foreground">
        Public sites need no account. Private sites need an account, and often an invitation.
      </p>
      {defs.error && <Alert variant="error">{defs.error.message}</Alert>}
      {defs.isLoading && <p className="text-sm text-muted-foreground">Loading trackers…</p>}
      {defs.data && defs.data.count === 0 && (
        <Alert variant="warning">
          Reelkeeper hasn't downloaded the tracker list yet. It does that once a day; try again in a
          few minutes.
        </Alert>
      )}
      {defs.data && defs.data.count > 0 && (
        <ul className="max-h-72 divide-y overflow-y-auto rounded-md border">
          {shown.length === 0 && (
            <li className="px-3 py-2 text-sm text-muted-foreground">No tracker matches.</li>
          )}
          {shown.map((d) => (
            <li key={d.id}>
              <button
                type="button"
                onClick={() => onPick(d)}
                className="flex w-full items-start gap-2 px-3 py-2 text-left hover:bg-muted"
              >
                <span className="min-w-0 flex-1 space-y-0.5">
                  <span className="block text-sm font-medium break-words">{d.name}</span>
                  {d.description && (
                    <span className="line-clamp-2 block text-xs text-muted-foreground">
                      {d.description}
                    </span>
                  )}
                </span>
                <Badge variant={d.type === "public" ? "success" : "neutral"}>
                  {typeLabel[d.type] ?? d.type}
                </Badge>
              </button>
            </li>
          ))}
          {matches.length > shown.length && (
            <li className="px-3 py-2 text-xs text-muted-foreground">
              {matches.length - shown.length} more. Type in the search box to narrow the list.
            </li>
          )}
        </ul>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onBack}>
          Back
        </Button>
      </div>
    </div>
  );
}

function TrackerLoader({
  defId,
  indexer,
  summary,
  onBack,
  onClose,
}: {
  defId: string;
  indexer?: Indexer;
  summary?: DefinitionSummary;
  onBack?: () => void;
  onClose: () => void;
}) {
  const detail = useQuery(definitionQuery(defId));
  if (detail.error) {
    return (
      <div className="space-y-3">
        <Alert variant="error">
          Couldn't load this tracker's settings: {detail.error.message}
          {indexer && " Its definition may have been removed upstream."}
        </Alert>
        <div className="flex justify-end gap-2">
          {onBack && (
            <Button variant="ghost" onClick={onBack}>
              Back
            </Button>
          )}
          <DialogClose asChild>
            <Button variant="outline">Close</Button>
          </DialogClose>
        </div>
      </div>
    );
  }
  if (!detail.data) {
    return (
      <p className="text-sm text-muted-foreground">
        Loading {summary?.name ?? "the tracker"}'s settings…
      </p>
    );
  }
  return (
    <IndexerForm
      indexer={indexer}
      kind="cardigann"
      detail={detail.data}
      onBack={onBack}
      onClose={onClose}
    />
  );
}

type FormState = {
  name: string;
  feedKind: IndexerKind;
  baseUrl: string;
  enabled: boolean;
  useSolver: boolean;
  settings: Record<string, string>;
  categories: number[];
  seedRatio: string;
  seedHours: string;
  packSeedHours: string;
  priority: string;
  queryLimit: string;
  grabLimit: string;
};

function fieldSettings(detail?: DefinitionDetail) {
  return (detail?.settings ?? []).filter((s) => s.type !== "info");
}

function initialState(kind: IndexerKind, indexer?: Indexer, detail?: DefinitionDetail): FormState {
  const settings: Record<string, string> = {};
  for (const s of fieldSettings(detail)) {
    // Secrets always start empty; the stored value is never sent to the browser.
    settings[s.name] = s.secret ? "" : (indexer?.settings[s.name] ?? s.default);
  }
  if (kind === "torznab" || kind === "newznab") settings.apiKey = "";
  return {
    name: indexer?.name ?? detail?.name ?? (kind === "knaben" ? "Knaben" : ""),
    feedKind: kind,
    baseUrl: indexer?.baseUrl ?? "",
    enabled: indexer?.enabled ?? true,
    useSolver: indexer?.useSolver ?? false,
    settings,
    categories: indexer?.categories?.length ? indexer.categories : [2000, 5000],
    seedRatio: indexer?.seedRatio ? String(indexer.seedRatio) : "",
    seedHours: hoursText(indexer?.seedTimeMinutes),
    packSeedHours: hoursText(indexer?.packSeedTimeMinutes),
    priority: String(indexer?.priority ?? 25),
    queryLimit: indexer?.queryLimit ? String(indexer.queryLimit) : "",
    grabLimit: indexer?.grabLimit ? String(indexer.grabLimit) : "",
  };
}

function IndexerForm({
  indexer,
  kind,
  detail,
  onBack,
  onClose,
}: {
  indexer?: Indexer;
  kind: IndexerKind;
  detail?: DefinitionDetail;
  onBack?: () => void;
  onClose: () => void;
}) {
  const invalidate = useInvalidateIndexers();
  const solver = useQuery(solverQuery);
  const [f, setF] = useState(() => initialState(kind, indexer, detail));
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const isCardigann = kind === "cardigann";
  const isFeed = kind === "torznab" || kind === "newznab";
  const set = <K extends keyof FormState>(key: K, value: FormState[K]) =>
    setF((s) => ({ ...s, [key]: value }));
  const setSetting = (name: string, value: string) =>
    setF((s) => ({ ...s, settings: { ...s.settings, [name]: value } }));

  const secretsSet = indexer?.secretsSet ?? [];
  const secretNames = new Set([
    ...fieldSettings(detail)
      .filter((s) => s.secret || s.type === "password")
      .map((s) => s.name),
    ...(isFeed ? ["apiKey"] : []),
  ]);

  function buildInput(): IndexerInput {
    const settings: Record<string, string> = {};
    for (const [name, value] of Object.entries(f.settings)) {
      // Only send a secret if something was typed: empty means "keep the stored one".
      if (secretNames.has(name) && value === "") continue;
      settings[name] = value;
    }
    return {
      name: f.name.trim(),
      kind: isFeed ? f.feedKind : kind,
      definition: isCardigann ? detail?.id : undefined,
      baseUrl: f.baseUrl.trim(),
      enabled: f.enabled,
      priority: intOrUndefined(f.priority) ?? 25,
      useSolver: kind === "knaben" ? false : f.useSolver,
      settings,
      categories: f.categories,
      seedRatio: f.seedRatio.trim() === "" ? undefined : Number.parseFloat(f.seedRatio),
      seedTimeMinutes: minutesFromHours(f.seedHours),
      packSeedTimeMinutes: minutesFromHours(f.packSeedHours),
      queryLimit: intOrUndefined(f.queryLimit),
      grabLimit: intOrUndefined(f.grabLimit),
    };
  }

  const test = useMutation({
    mutationFn: () => {
      const body = { ...buildInput(), ...(indexer ? { id: indexer.id } : {}) };
      return unwrap(api.POST("/indexers/test", { body }));
    },
  });
  const save = useMutation({
    mutationFn: () => {
      const body = buildInput();
      return unwrap(
        indexer
          ? api.PUT("/indexers/{id}", { params: { path: { id: indexer.id } }, body })
          : api.POST("/indexers", { body }),
      );
    },
    onSuccess: () => {
      invalidate();
      onClose();
    },
  });
  const err = save.error instanceof RequestError ? save.error : undefined;
  const testErr = test.error instanceof RequestError ? test.error : undefined;
  const fields = { ...testErr?.fields, ...err?.fields };
  const hasFieldErrors = Object.keys(fields).length > 0;
  const showAdvanced = advancedOpen || advancedFields.some((k) => fields[k]);

  function submit(e: FormEvent) {
    e.preventDefault();
    test.reset();
    save.mutate();
  }

  function toggleCategory(id: number, on: boolean) {
    set(
      "categories",
      on ? [...new Set([...f.categories, id])].sort() : f.categories.filter((c) => c !== id),
    );
  }

  const solverMissing = f.useSolver && solver.data !== undefined && !solver.data.url;
  const tracker = detail?.type
    ? `${typeLabel[detail.type] ?? detail.type} tracker${detail.language ? ` · ${detail.language}` : ""}`
    : "";
  const linkListId = "ix-links";

  return (
    <form onSubmit={submit} className="space-y-4">
      {isCardigann && detail && (
        <div className="space-y-1">
          <p className="text-sm font-medium">{detail.name}</p>
          {tracker && <p className="text-xs text-muted-foreground">{tracker}</p>}
          {detail.description && (
            <p className="text-sm text-muted-foreground">{detail.description}</p>
          )}
        </div>
      )}
      {kind === "knaben" && (
        <p className="text-sm text-muted-foreground">
          Knaben is a public search engine that covers many torrent sites. It has no settings.
        </p>
      )}
      {(save.error || test.error) && !hasFieldErrors && (
        <Alert variant="error">{(save.error ?? test.error)?.message}</Alert>
      )}

      <Field
        id="ix-name"
        label="Name"
        help="How this indexer is called in Reelkeeper."
        error={fields.name}
      >
        <Input id="ix-name" value={f.name} onChange={(e) => set("name", e.target.value)} />
      </Field>

      {isFeed && (
        <>
          <Field
            id="ix-feedkind"
            label="Feed type"
            help="Torznab is for torrents. Newznab is for Usenet, which Reelkeeper can't download yet."
            error={fields.kind}
          >
            <select
              id="ix-feedkind"
              className={selectClass}
              value={f.feedKind}
              onChange={(e) => set("feedKind", e.target.value as IndexerKind)}
            >
              <option value="torznab">Torznab (torrents)</option>
              <option value="newznab">Newznab (Usenet)</option>
            </select>
          </Field>
          <Field
            id="ix-feedurl"
            label="Feed address"
            help="The feed's API address, for example http://jackett:9117/api/v2.0/indexers/yourtracker/results/torznab/."
            error={fields.baseUrl}
          >
            <Input
              id="ix-feedurl"
              value={f.baseUrl}
              placeholder="http://jackett:9117/api/v2.0/indexers/…/results/torznab/"
              onChange={(e) => set("baseUrl", e.target.value)}
            />
          </Field>
          <Field
            id="ix-apikey"
            label="API key"
            help="Shown by Jackett (or whatever provides the feed)."
            error={fields["settings.apiKey"]}
          >
            <Input
              id="ix-apikey"
              type="password"
              autoComplete="off"
              value={f.settings.apiKey ?? ""}
              placeholder={secretsSet.includes("apiKey") ? "Saved; leave empty to keep" : ""}
              onChange={(e) => setSetting("apiKey", e.target.value)}
            />
          </Field>
        </>
      )}

      {detail?.settings.map((s) => (
        <DefinitionField
          key={s.name}
          setting={s}
          value={f.settings[s.name] ?? ""}
          saved={secretsSet.includes(s.name)}
          error={fields[`settings.${s.name}`]}
          onChange={(v) => setSetting(s.name, v)}
        />
      ))}

      {kind !== "knaben" && (
        <SwitchRow
          id="ix-solver"
          label="Use the Cloudflare solver"
          help="Some sites hide behind Cloudflare's 'checking your browser' page. With this on, every request to this site goes through your solver (such as TRAWL), and new releases are checked hourly instead of every 15 minutes to spare it. Leave it off unless the site needs it."
          error={fields.useSolver}
        >
          <Switch
            id="ix-solver"
            checked={f.useSolver}
            onCheckedChange={(on) => set("useSolver", on)}
          />
        </SwitchRow>
      )}
      {solverMissing && (
        <Alert variant="warning">
          No solver address is set yet, so this indexer will fail on Cloudflare-protected pages. Add
          one in the Cloudflare solver card on this page.
        </Alert>
      )}

      <SwitchRow
        id="ix-enabled"
        label="Enabled"
        help="Off keeps the settings but stops Reelkeeper from searching here."
      >
        <Switch id="ix-enabled" checked={f.enabled} onCheckedChange={(on) => set("enabled", on)} />
      </SwitchRow>

      {kind !== "knaben" && (
        <fieldset className="space-y-3 rounded-md border p-3">
          <legend className="px-1 text-sm font-medium">Seeding rules</legend>
          <p className="text-sm text-muted-foreground">
            Private trackers want you to keep sharing a download for a while, often a ratio of 1.0
            or a few days. Enter the tracker's minimums here. Reelkeeper's cleanup will never remove
            a torrent from this tracker before they are met, so you don't get a hit-and-run warning.
            Leave empty if the tracker has no minimum.
          </p>
          <div className="grid gap-3 sm:grid-cols-3">
            <Field
              id="ix-ratio"
              label="Minimum ratio"
              help="For example 1.0."
              error={fields.seedRatio}
            >
              <Input
                id="ix-ratio"
                type="number"
                min="0"
                step="0.1"
                value={f.seedRatio}
                onChange={(e) => set("seedRatio", e.target.value)}
              />
            </Field>
            <Field
              id="ix-seedtime"
              label="Minimum seed time (hours)"
              help="For example 72 for 3 days."
              error={fields.seedTimeMinutes}
            >
              <Input
                id="ix-seedtime"
                type="number"
                min="0"
                step="any"
                value={f.seedHours}
                onChange={(e) => set("seedHours", e.target.value)}
              />
            </Field>
            <Field
              id="ix-packtime"
              label="Season packs (hours)"
              help="Only if packs have their own minimum."
              error={fields.packSeedTimeMinutes}
            >
              <Input
                id="ix-packtime"
                type="number"
                min="0"
                step="any"
                value={f.packSeedHours}
                onChange={(e) => set("packSeedHours", e.target.value)}
              />
            </Field>
          </div>
        </fieldset>
      )}

      <div>
        <button
          type="button"
          aria-expanded={showAdvanced}
          onClick={() => setAdvancedOpen(!showAdvanced)}
          className="flex items-center gap-1 text-sm font-medium text-link hover:underline [&_svg]:size-4"
        >
          {showAdvanced ? <ChevronDown /> : <ChevronRight />}
          {showAdvanced ? "Hide advanced" : "Show advanced"}
        </button>
      </div>
      {showAdvanced && (
        <div className="space-y-4 rounded-md border p-3">
          {isCardigann && detail && (
            <Field
              id="ix-url"
              label="Address"
              help="The site's web address. Leave empty to use the first address the tracker lists. Choose another from the list, or type one if the site has moved."
              error={fields.baseUrl}
            >
              <Input
                id="ix-url"
                list={linkListId}
                value={f.baseUrl}
                placeholder={detail.links[0] ?? ""}
                onChange={(e) => set("baseUrl", e.target.value)}
              />
              <datalist id={linkListId}>
                {detail.links.map((l) => (
                  <option key={l} value={l} />
                ))}
              </datalist>
            </Field>
          )}
          <Field
            id="ix-priority"
            label="Priority"
            help="From 1 to 50. Lower numbers are preferred when the same release is found in several places. The default is 25."
            error={fields.priority}
          >
            <Input
              id="ix-priority"
              type="number"
              min="1"
              max="50"
              className="w-28"
              value={f.priority}
              onChange={(e) => set("priority", e.target.value)}
            />
          </Field>
          {kind !== "knaben" && (
            <>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field
                  id="ix-querylimit"
                  label="Searches per day"
                  help="Stops Reelkeeper asking more often than this. Empty means no limit."
                  error={fields.queryLimit}
                >
                  <Input
                    id="ix-querylimit"
                    type="number"
                    min="0"
                    value={f.queryLimit}
                    onChange={(e) => set("queryLimit", e.target.value)}
                  />
                </Field>
                <Field
                  id="ix-grablimit"
                  label="Downloads per day"
                  help="Some trackers cap daily downloads. Empty means no limit."
                  error={fields.grabLimit}
                >
                  <Input
                    id="ix-grablimit"
                    type="number"
                    min="0"
                    value={f.grabLimit}
                    onChange={(e) => set("grabLimit", e.target.value)}
                  />
                </Field>
              </div>
              <fieldset className="space-y-2">
                <legend className="text-sm font-medium">Categories</legend>
                <p className="text-sm text-muted-foreground">
                  What to look for here. The default is all movies and all TV. If you untick
                  everything, Reelkeeper uses the default.
                </p>
                {fields.categories && (
                  <p className="text-sm text-destructive">{fields.categories}</p>
                )}
                <div className="grid gap-x-4 gap-y-1 sm:grid-cols-2">
                  {categoryChoices.map((c) => (
                    <label
                      key={c.id}
                      className={`flex items-center gap-2 text-sm ${c.sub ? "pl-5" : "font-medium"}`}
                    >
                      <input
                        type="checkbox"
                        className="accent-primary"
                        checked={f.categories.includes(c.id)}
                        onChange={(e) => toggleCategory(c.id, e.target.checked)}
                      />
                      {c.label}
                    </label>
                  ))}
                </div>
              </fieldset>
            </>
          )}
        </div>
      )}

      {test.data && <Alert variant={test.data.ok ? "success" : "error"}>{test.data.message}</Alert>}

      <div className="flex flex-wrap justify-end gap-2">
        {onBack && (
          <Button type="button" variant="ghost" className="mr-auto" onClick={onBack}>
            Back
          </Button>
        )}
        <DialogClose asChild>
          <Button type="button" variant="ghost">
            Cancel
          </Button>
        </DialogClose>
        <Button
          type="button"
          variant="outline"
          disabled={test.isPending}
          onClick={() => test.mutate()}
        >
          {test.isPending ? "Testing…" : "Test"}
        </Button>
        <Button type="submit" disabled={save.isPending}>
          {save.isPending ? "Saving…" : indexer ? "Save" : "Add indexer"}
        </Button>
      </div>
    </form>
  );
}

function DefinitionField({
  setting: s,
  value,
  saved,
  error,
  onChange,
}: {
  setting: DefinitionSetting;
  value: string;
  saved: boolean;
  error?: string;
  onChange: (v: string) => void;
}) {
  const id = `ix-set-${s.name}`;
  if (s.type === "info") {
    // Standard help texts get plain-English wording of our own.
    const text =
      s.name === "info_cookie"
        ? cookieHelp
        : s.name === "info_flaresolverr"
          ? solverHelp
          : s.name === "info_useragent"
            ? userAgentHelp
            : plainInfoText(s.default);
    if (!text) return null;
    return (
      <div className="rounded-md bg-muted p-3 text-sm">
        {s.label && !s.name.startsWith("info_") && <p className="font-medium">{s.label}</p>}
        <p className="break-words whitespace-pre-line text-muted-foreground">{text}</p>
      </div>
    );
  }
  if (s.type === "checkbox") {
    return (
      <SwitchRow id={id} label={s.label || s.name} error={error}>
        <Switch id={id} checked={value === "true"} onCheckedChange={(on) => onChange(String(on))} />
      </SwitchRow>
    );
  }
  if (s.type === "select") {
    return (
      <Field id={id} label={s.label || s.name} error={error}>
        <select
          id={id}
          className={selectClass}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        >
          {s.options.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      </Field>
    );
  }
  const secret = s.secret || s.type === "password";
  return (
    <Field id={id} label={s.label || s.name} error={error}>
      <Input
        id={id}
        type={secret ? "password" : "text"}
        autoComplete="off"
        value={value}
        placeholder={saved ? "Saved; leave empty to keep" : ""}
        onChange={(e) => onChange(e.target.value)}
      />
    </Field>
  );
}
