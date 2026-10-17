import { useQuery } from "@tanstack/react-query";
import type { FormEvent } from "react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { downloadClientQuery, isClientSet } from "@/hooks/downloads";
import { useDraft } from "@/hooks/draft";
import { type GrabbingSettings, grabbingQuery, useSaveGrabbing } from "@/hooks/grab";
import { cn } from "@/lib/utils";
import { PageHeader } from "@/pages/PageHeader";
import { DownloadClientCard } from "./DownloadClientCard";
import { SaveBar } from "./SaveBar";

const modes: { value: GrabbingSettings["mode"]; label: string; help: string }[] = [
  {
    value: "off",
    label: "Off",
    help: "Reelkeeper doesn't search on its own. Search now still works and shows what it would grab.",
  },
  {
    value: "dryrun",
    label: "Dry run",
    help: "Reelkeeper searches and records what it would grab in Activity → History, but downloads nothing. Recommended while you try Reelkeeper.",
  },
  {
    value: "on",
    label: "On",
    help: "Reelkeeper sends releases to the download client.",
  },
];

function sameGrabbing(a: GrabbingSettings, b: GrabbingSettings) {
  return (
    a.mode === b.mode &&
    a.tvMode === b.tvMode &&
    a.queueLimit === b.queueLimit &&
    a.minSeeders === b.minSeeders
  );
}

function toNumber(s: string) {
  const n = Number.parseInt(s, 10);
  return Number.isNaN(n) ? 0 : n;
}

/** One group of Off / Dry run / On choices (movies and TV each get one). */
function ModeGroup({
  legend,
  name,
  value,
  savedValue,
  clientSaved,
  error,
  onChange,
}: {
  legend: string;
  name: string;
  value: GrabbingSettings["mode"];
  savedValue: GrabbingSettings["mode"];
  clientSaved: boolean;
  error?: string;
  onChange: (mode: GrabbingSettings["mode"]) => void;
}) {
  return (
    <fieldset className="grid gap-2 sm:grid-cols-3">
      <legend className="mb-2 text-sm font-medium">{legend}</legend>
      {modes.map((m) => {
        const disabled = m.value === "on" && savedValue !== "on" && !clientSaved;
        const checked = value === m.value;
        return (
          <label
            key={m.value}
            className={cn(
              "relative flex items-start gap-3 rounded-lg border p-3.5 transition-[background-color,border-color,box-shadow] duration-[calc(200ms*var(--m))] has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring",
              checked && "border-primary bg-primary/10 shadow-xs",
              disabled ? "opacity-60" : "cursor-pointer hover:bg-muted/50",
            )}
          >
            <input
              type="radio"
              name={name}
              className="sr-only"
              value={m.value}
              checked={checked}
              disabled={disabled}
              onChange={() => onChange(m.value)}
            />
            <span className="space-y-0.5">
              <span className="flex items-center gap-2 text-sm font-medium">
                {m.label}
                {checked && <Badge variant="info">Selected</Badge>}
              </span>
              <span className="block text-sm text-muted-foreground">{m.help}</span>
              {disabled && (
                <span className="block text-sm text-warning">
                  Set up the download client above first.
                </span>
              )}
            </span>
          </label>
        );
      })}
      {error && <p className="col-span-full text-sm text-destructive">{error}</p>}
    </fieldset>
  );
}

function GrabbingCard() {
  const { data, error } = useQuery(grabbingQuery);
  const client = useQuery(downloadClientQuery);
  const clientSaved = Boolean(client.data && isClientSet(client.data));
  const save = useSaveGrabbing();
  const { draft: form, setDraft: setForm, dirty, reset, commit } = useDraft(data, sameGrabbing);

  if (error) return <Alert variant="error">Couldn't load settings: {error.message}</Alert>;
  if (!form || !data) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const err = save.error instanceof RequestError ? save.error : undefined;

  function submit(e: FormEvent) {
    e.preventDefault();
    if (form) save.mutate(form, { onSuccess: commit });
  }

  return (
    <form onSubmit={submit}>
      <Card>
        <CardHeader>
          <CardTitle>Automatic grabbing</CardTitle>
          <CardDescription>
            Whether Reelkeeper looks for releases of the movies and TV episodes you want, and what
            it does with them. Movies and TV are set separately.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-5">
          {err && !Object.keys(err.fields).length && <Alert variant="error">{err.message}</Alert>}
          <ModeGroup
            legend="Movies"
            name="grab-mode"
            value={form.mode}
            savedValue={data.mode}
            clientSaved={clientSaved}
            error={err?.fields.mode}
            onChange={(mode) => setForm({ ...form, mode })}
          />
          <ModeGroup
            legend="TV"
            name="grab-mode-tv"
            value={form.tvMode ?? "off"}
            savedValue={data.tvMode ?? "off"}
            clientSaved={clientSaved}
            error={err?.fields.tvMode}
            onChange={(tvMode) => setForm({ ...form, tvMode })}
          />
          <Field
            id="min-seeders"
            label="Minimum seeders"
            help="Releases with fewer seeders are skipped. 1 skips only dead torrents."
            error={err?.fields.minSeeders}
          >
            <Input
              id="min-seeders"
              type="number"
              min="0"
              className="w-28"
              value={form.minSeeders}
              onChange={(e) => setForm({ ...form, minSeeders: toNumber(e.target.value) })}
            />
          </Field>
          <Field
            id="queue-limit"
            label="Queue limit"
            help="Grabbing pauses while Activity → Queue holds this many downloads; it resumes as they finish. Seerr requests still go through. 0 means no limit."
            error={err?.fields.queueLimit}
          >
            <Input
              id="queue-limit"
              type="number"
              min="0"
              className="w-28"
              value={form.queueLimit}
              onChange={(e) => setForm({ ...form, queueLimit: toNumber(e.target.value) })}
            />
          </Field>
        </CardContent>
      </Card>
      <SaveBar dirty={dirty} saving={save.isPending} saved={save.isSuccess} onDiscard={reset} />
    </form>
  );
}

export function DownloadClientPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Download client"
        description="How Reelkeeper finds and downloads the movies and TV you want."
      />
      <DownloadClientCard />
      <GrabbingCard />
    </div>
  );
}
