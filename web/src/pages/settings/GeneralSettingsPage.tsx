import { useQuery } from "@tanstack/react-query";
import type { FormEvent } from "react";
import { type GeneralSettings, RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useDraft } from "@/hooks/draft";
import { generalSettingsQuery, useSaveGeneral } from "@/hooks/settings";
import { PageHeader } from "@/pages/PageHeader";
import { AppearanceCard } from "./AppearanceCard";
import { LabelsCard } from "./LabelsCard";
import { MetadataCard } from "./MetadataCard";
import { SaveBar } from "./SaveBar";

const logLevels: { value: GeneralSettings["logLevel"]; label: string }[] = [
  { value: "error", label: "Errors only" },
  { value: "warn", label: "Warnings and errors" },
  { value: "info", label: "Normal (recommended)" },
  { value: "debug", label: "Debug (very detailed, for troubleshooting)" },
];

function GeneralForm() {
  const { data, error } = useQuery(generalSettingsQuery);
  const save = useSaveGeneral();
  const { draft: form, setDraft: setForm, dirty, reset, commit } = useDraft(data, sameGeneral);

  if (error) return <Alert variant="error">Couldn't load settings: {error.message}</Alert>;
  if (!form) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const err = save.error instanceof RequestError ? save.error : undefined;

  function submit(e: FormEvent) {
    e.preventDefault();
    if (form) save.mutate(form, { onSuccess: commit });
  }

  return (
    <form onSubmit={submit}>
      <Card>
        <CardHeader>
          <CardTitle>Instance</CardTitle>
        </CardHeader>
        <CardContent>
          {err && !Object.keys(err.fields).length && <Alert variant="error">{err.message}</Alert>}
          <Field
            id="instanceName"
            label="Name"
            help="Shown in the browser tab and the menu. Handy if you run more than one Reelkeeper."
            error={err?.fields.instanceName}
          >
            <Input
              id="instanceName"
              value={form.instanceName}
              maxLength={50}
              onChange={(e) => setForm({ ...form, instanceName: e.target.value })}
            />
          </Field>
          <Field
            id="logLevel"
            label="Log detail"
            help="How much Reelkeeper writes to its log. Use Debug only while investigating a problem."
            error={err?.fields.logLevel}
          >
            <select
              id="logLevel"
              className="w-full min-h-10 rounded-lg border border-input bg-background px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none"
              value={form.logLevel}
              onChange={(e) =>
                setForm({ ...form, logLevel: e.target.value as GeneralSettings["logLevel"] })
              }
            >
              {logLevels.map((l) => (
                <option key={l.value} value={l.value}>
                  {l.label}
                </option>
              ))}
            </select>
          </Field>
        </CardContent>
      </Card>
      <SaveBar dirty={dirty} saving={save.isPending} saved={save.isSuccess} onDiscard={reset} />
    </form>
  );
}

function sameGeneral(a: GeneralSettings, b: GeneralSettings) {
  return a.instanceName === b.instanceName && a.logLevel === b.logLevel;
}

export function GeneralSettingsPage() {
  return (
    <div className="space-y-6">
      <PageHeader title="General" description="Basic settings for this Reelkeeper." />
      <AppearanceCard />
      <GeneralForm />
      <MetadataCard />
      <LabelsCard />
    </div>
  );
}
