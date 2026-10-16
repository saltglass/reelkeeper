import { useQuery } from "@tanstack/react-query";
import type { FormEvent, ReactNode } from "react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, SwitchRow } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Segmented } from "@/components/ui/segmented";
import { Switch } from "@/components/ui/switch";
import {
  type CleanupSettings,
  cleanupSettingsQuery,
  useSaveCleanupSettings,
} from "@/hooks/cleanup";
import { atSentenceStart, useClientName } from "@/hooks/downloads";
import { useDraft } from "@/hooks/draft";
import { PageHeader } from "@/pages/PageHeader";
import { SaveBar } from "./SaveBar";

/** The cleanup modes; `client` is the download client's name. */
const modes = (
  client: string,
): { value: CleanupSettings["mode"]; label: string; help: string }[] => [
  { value: "off", label: "Off", help: `Reelkeeper never removes anything from ${client}.` },
  {
    value: "dryrun",
    label: "Dry run",
    help: "Reelkeeper records what it would remove under Activity → Cleanup, but removes nothing. Recommended while you try it.",
  },
  {
    value: "on",
    label: "On",
    help: `Reelkeeper removes torrents from ${client}, and what they downloaded, as the rules below say.`,
  },
];

function same(a: CleanupSettings, b: CleanupSettings) {
  return (Object.keys(a) as (keyof CleanupSettings)[]).every((k) => a[k] === b[k]);
}

function toInt(s: string) {
  const n = Number.parseInt(s, 10);
  return Number.isNaN(n) ? 0 : n;
}

function toFloat(s: string) {
  const n = Number.parseFloat(s);
  return Number.isNaN(n) ? 0 : n;
}

/** One stuck-download rule: a plain explanation and its number(s). */
function RuleRow({
  title,
  help,
  error,
  children,
}: {
  title: string;
  help: string;
  error?: string;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-3 border-b pb-4 last:border-b-0 last:pb-0">
      <div className="min-w-0 flex-1 basis-64 space-y-0.5">
        <p className="text-sm font-medium">{title}</p>
        <p className="text-sm text-muted-foreground">{help}</p>
        {error && <p className="text-sm text-destructive">{error}</p>}
      </div>
      <div className="flex items-center gap-2 text-sm">{children}</div>
    </div>
  );
}

function NumberInput({
  id,
  label,
  value,
  onChange,
  unit,
}: {
  id: string;
  label: string;
  value: number;
  onChange: (n: number) => void;
  unit: string;
}) {
  return (
    <>
      <Input
        id={id}
        aria-label={label}
        type="number"
        min="0"
        className="w-24"
        value={value}
        onChange={(e) => onChange(toInt(e.target.value))}
      />
      <span className="text-muted-foreground">{unit}</span>
    </>
  );
}

export function CleanupSettingsPage() {
  const { data, error } = useQuery(cleanupSettingsQuery);
  const client = useClientName();
  const modeList = modes(client);
  const save = useSaveCleanupSettings();
  const { draft: form, setDraft, dirty, reset, commit } = useDraft(data, same);

  if (error) return <Alert variant="error">Couldn't load settings: {error.message}</Alert>;
  if (!form) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const err = save.error instanceof RequestError ? save.error : undefined;
  const fe = err?.fields ?? {};
  const set = (patch: Partial<CleanupSettings>) => setDraft({ ...form, ...patch });

  function submit(e: FormEvent) {
    e.preventDefault();
    if (form) save.mutate(form, { onSuccess: commit });
  }

  return (
    <form onSubmit={submit} className="space-y-6">
      <PageHeader
        title="Cleanup"
        description={`How Reelkeeper tidies ${client} after itself: finished torrents, stuck downloads and leftovers.`}
      />
      {err && Object.keys(fe).length === 0 && <Alert variant="error">{err.message}</Alert>}

      <Card>
        <CardHeader>
          <CardTitle>Mode</CardTitle>
          <CardDescription>
            Cleanup only ever touches torrents in Reelkeeper's own category, reelkeeper-movies.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="space-y-3">
            <Segmented
              label="Mode"
              options={modeList.map((m) => ({ value: m.value, label: m.label }))}
              value={form.mode}
              onChange={(mode) => set({ mode })}
            />
            <p className="text-sm text-muted-foreground" aria-live="polite">
              {modeList.find((m) => m.value === form.mode)?.help}
            </p>
            {fe.mode && <p className="text-sm text-destructive">{fe.mode}</p>}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Seeding goal</CardTitle>
          <CardDescription>
            After a download is imported, its torrent keeps seeding until it reaches this ratio or
            this many hours have passed since it finished, whichever comes first — and never before
            the tracker's own minimum (set per indexer), which counts real seeding time. Leave both
            at 0 to remove as soon as the tracker's minimum is met.
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-5 sm:grid-cols-2">
          <Field
            id="seed-ratio"
            label="Ratio"
            help="Upload divided by download. 1 means you gave back as much as you took."
            error={fe.seedRatio}
          >
            <Input
              id="seed-ratio"
              type="number"
              min="0"
              step="0.1"
              className="w-28"
              value={form.seedRatio}
              onChange={(e) => set({ seedRatio: toFloat(e.target.value) })}
            />
          </Field>
          <Field
            id="seed-hours"
            label="Hours since it finished"
            help={`24 = 1 day, 168 = 1 week. Time spent waiting in ${client}'s queue counts.`}
            error={fe.seedHours}
          >
            <Input
              id="seed-hours"
              type="number"
              min="0"
              className="w-28"
              value={form.seedHours}
              onChange={(e) => set({ seedHours: toInt(e.target.value) })}
            />
          </Field>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Stuck downloads</CardTitle>
          <CardDescription>
            When a download is stuck, it is removed from {client} with what it downloaded,
            blocklisted, and Reelkeeper searches for another release. A rule set to 0 is off.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <RuleRow
            title="No progress"
            help="Nothing at all has downloaded for this long."
            error={fe.stalledHours}
          >
            <NumberInput
              id="stalled-hours"
              label="No progress, hours"
              value={form.stalledHours}
              onChange={(n) => set({ stalledHours: n })}
              unit="hours"
            />
          </RuleRow>
          <RuleRow
            title="Too slow"
            help="It has downloaded slower than this speed for this long. Set both, or neither."
            error={fe.slowKBps ?? fe.slowHours}
          >
            <NumberInput
              id="slow-kbps"
              label="Slower than, KB/s"
              value={form.slowKBps}
              onChange={(n) => set({ slowKBps: n })}
              unit="KB/s for"
            />
            <NumberInput
              id="slow-hours"
              label="Slow for, hours"
              value={form.slowHours}
              onChange={(n) => set({ slowHours: n })}
              unit="hours"
            />
          </RuleRow>
          <RuleRow
            title="No torrent details"
            help={`${atSentenceStart(client)} can't find out what's in the torrent.`}
            error={fe.metadataHours}
          >
            <NumberInput
              id="metadata-hours"
              label="No torrent details, hours"
              value={form.metadataHours}
              onChange={(n) => set({ metadataHours: n })}
              unit="hours"
            />
          </RuleRow>
          <RuleRow
            title="Import failed"
            help="The download finished but couldn't be imported, and nobody dealt with it."
            error={fe.importFailedDays}
          >
            <NumberInput
              id="import-failed-days"
              label="Import failed, days"
              value={form.importFailedDays}
              onChange={(n) => set({ importFailedDays: n })}
              unit="days"
            />
          </RuleRow>
          {/* Divided like the rule rows above it. */}
          <div className="border-b pb-4">
            <SwitchRow
              id="unregistered"
              label="Deleted on the tracker"
              help="The tracker says the torrent no longer exists."
              error={fe.unregistered}
            >
              <Switch
                id="unregistered"
                checked={form.unregistered}
                onCheckedChange={(v) => set({ unregistered: v })}
              />
            </SwitchRow>
          </div>
          <SwitchRow
            id="missing-files"
            label="Files missing"
            help={`${atSentenceStart(client)} lost the downloaded files.`}
            error={fe.missingFiles}
          >
            <Switch
              id="missing-files"
              checked={form.missingFiles}
              onCheckedChange={(v) => set({ missingFiles: v })}
            />
          </SwitchRow>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Download disk</CardTitle>
          <CardDescription>
            Keeps the disk your downloads go to from filling up. A grab that wouldn't leave this
            much free is skipped (History says why, and you get one email). When the disk has less
            free, finished torrents are removed early, longest finished first, but never before
            their tracker's own minimum.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Field
            id="keep-free-gb"
            label="Keep at least (GB) free"
            help="Leave room for your largest downloads. 0 turns this off."
            error={fe.keepFreeGB}
          >
            <Input
              id="keep-free-gb"
              type="number"
              min="0"
              className="w-28"
              value={form.keepFreeGB ?? 0}
              onChange={(e) => set({ keepFreeGB: toInt(e.target.value) })}
            />
          </Field>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Replaced files</CardTitle>
          <CardDescription>
            Files that upgrades replaced are kept in a hidden .reelkeeper-replaced folder in each
            media folder.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Field
            id="empty-replaced-days"
            label="Empty them after (days)"
            help="0 keeps them forever."
            error={fe.emptyReplacedDays}
          >
            <Input
              id="empty-replaced-days"
              type="number"
              min="0"
              className="w-28"
              value={form.emptyReplacedDays}
              onChange={(e) => set({ emptyReplacedDays: toInt(e.target.value) })}
            />
          </Field>
        </CardContent>
      </Card>

      <SaveBar dirty={dirty} saving={save.isPending} saved={save.isSuccess} onDiscard={reset} />
    </form>
  );
}
