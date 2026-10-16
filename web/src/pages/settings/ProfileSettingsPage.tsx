import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { ChevronDown, ChevronLeft, ChevronRight } from "lucide-react";
import { type FormEvent, useMemo, useState } from "react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { useDraft } from "@/hooks/draft";
import { type Profile, profilesQuery } from "@/hooks/library";
import {
  type ProfileCompiled,
  type ProfileOptions,
  type ProfileSettings,
  profileOptionsQuery,
  profileSettingsQuery,
  useSaveAdvanced,
  useSaveSimple,
  useSwitchToAdvanced,
} from "@/hooks/profiles";
import { PageHeader } from "@/pages/PageHeader";
import { AdvancedEditor, CompiledSummary, normalizeCompiled } from "./ProfileAdvanced";
import { ProfileSimpleEditor } from "./ProfileSimpleEditor";
import { ConfirmDialog, sameValue } from "./ProfileWidgets";
import { SaveBar } from "./SaveBar";
import { TestReleaseCard } from "./TestReleaseCard";

const appliesLabel: Record<Profile["appliesTo"], string> = {
  movie: "Movies",
  series: "TV",
  both: "Movies and TV",
};

function AdvancedToggle({ open, onToggle }: { open: boolean; onToggle: () => void }) {
  return (
    <button
      type="button"
      aria-expanded={open}
      onClick={onToggle}
      className="flex items-center gap-1 text-sm font-medium text-link hover:underline [&_svg]:size-4"
    >
      {open ? <ChevronDown /> : <ChevronRight />}
      {open ? "Hide advanced" : "Show advanced"}
    </button>
  );
}

function SimpleForm({
  profile,
  options,
  settings,
}: {
  profile: Profile;
  options: ProfileOptions;
  settings: ProfileSettings;
}) {
  const save = useSaveSimple(profile.id);
  const toAdvanced = useSwitchToAdvanced(profile.id);
  const { draft, setDraft, dirty, reset, commit } = useDraft(settings.simple, sameValue);
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const err = save.error instanceof RequestError ? save.error : undefined;
  if (!draft) return null;

  function submit(e: FormEvent) {
    e.preventDefault();
    if (draft) save.mutate(draft, { onSuccess: (s) => commit(s.simple) });
  }

  return (
    <form onSubmit={submit} className="space-y-6">
      {err && !Object.keys(err.fields).length && <Alert variant="error">{err.message}</Alert>}
      {err && Object.keys(err.fields).length > 0 && (
        <Alert variant="error">Some settings need attention. They're marked below.</Alert>
      )}
      <ProfileSimpleEditor
        options={options}
        value={draft}
        onChange={setDraft}
        errors={err?.fields ?? {}}
        forMovies={profile.appliesTo !== "series"}
      />
      <Card>
        <CardContent className="space-y-4 pt-6">
          <AdvancedToggle open={showAdvanced} onToggle={() => setShowAdvanced(!showAdvanced)} />
          {showAdvanced && (
            <div className="space-y-4">
              <CompiledSummary value={settings.compiled} />
              <Button type="button" variant="outline" onClick={() => setConfirming(true)}>
                Edit advanced settings
              </Button>
            </div>
          )}
        </CardContent>
      </Card>
      <SaveBar dirty={dirty} saving={save.isPending} saved={save.isSuccess} onDiscard={reset} />
      {confirming && (
        <ConfirmDialog
          title="Edit advanced settings?"
          confirmLabel="Edit advanced settings"
          busy={toAdvanced.isPending}
          error={toAdvanced.error?.message}
          onClose={() => setConfirming(false)}
          onConfirm={() => toAdvanced.mutate(undefined, { onSuccess: () => setConfirming(false) })}
        >
          From now on the simple settings no longer apply: you edit qualities, custom formats and
          scores directly. Nothing about what the profile does changes now.
          {dirty && " Your unsaved changes to the simple settings are not included."} You can go
          back to simple settings later, but your advanced edits would be lost.
        </ConfirmDialog>
      )}
    </form>
  );
}

function AdvancedForm({ profile, settings }: { profile: Profile; settings: ProfileSettings }) {
  const save = useSaveAdvanced(profile.id);
  const back = useSaveSimple(profile.id);
  const normalized = useMemo(() => normalizeCompiled(settings.compiled), [settings.compiled]);
  const { draft, setDraft, dirty, reset, commit } = useDraft<ProfileCompiled>(
    normalized,
    sameValue,
  );
  const [showAdvanced, setShowAdvanced] = useState(true);
  const [confirming, setConfirming] = useState(false);
  const err = save.error instanceof RequestError ? save.error : undefined;
  if (!draft) return null;

  function submit(e: FormEvent) {
    e.preventDefault();
    if (draft) save.mutate(draft, { onSuccess: (s) => commit(normalizeCompiled(s.compiled)) });
  }

  return (
    <form onSubmit={submit} className="space-y-6">
      <Alert variant="info">
        This profile uses advanced settings. The simple settings no longer apply to it.
      </Alert>
      {err && !err.fields.advanced && <Alert variant="error">{err.message}</Alert>}
      <Card>
        <CardContent className="space-y-4 pt-6">
          <AdvancedToggle open={showAdvanced} onToggle={() => setShowAdvanced(!showAdvanced)} />
          {showAdvanced && (
            <AdvancedEditor value={draft} onChange={setDraft} error={err?.fields.advanced} />
          )}
        </CardContent>
      </Card>
      <div>
        <Button type="button" variant="outline" onClick={() => setConfirming(true)}>
          Go back to simple settings
        </Button>
      </div>
      <SaveBar dirty={dirty} saving={save.isPending} saved={save.isSuccess} onDiscard={reset} />
      {confirming && (
        <ConfirmDialog
          title="Go back to simple settings?"
          confirmLabel="Go back to simple settings"
          busy={back.isPending}
          error={back.error?.message}
          onClose={() => setConfirming(false)}
          onConfirm={() => back.mutate(settings.simple, { onSuccess: () => setConfirming(false) })}
        >
          Your advanced edits will be lost. The profile goes back to the simple settings it started
          from, and that may change what it downloads.
        </ConfirmDialog>
      )}
    </form>
  );
}

export function ProfileSettingsPage() {
  const { id: rawId } = useParams({ from: "/app/settings/profiles/$id" });
  const id = Number(rawId);
  const profiles = useQuery(profilesQuery);
  const options = useQuery(profileOptionsQuery);
  const settings = useQuery(profileSettingsQuery(id));
  const profile = profiles.data?.find((p) => p.id === id);

  const failed = settings.error ?? options.error ?? profiles.error;
  let body = <p className="text-sm text-muted-foreground">Loading…</p>;
  if (failed) {
    body = (
      <Alert variant="error">
        {settings.error && (settings.error as RequestError).status === 404
          ? "That profile doesn't exist (any more)."
          : `Couldn't load this profile: ${failed.message}`}
      </Alert>
    );
  } else if (profiles.data && !profile) {
    body = <Alert variant="error">That profile doesn't exist (any more).</Alert>;
  } else if (profile && options.data && settings.data) {
    const s = settings.data;
    body = (
      <div className="space-y-6">
        {s.mode === "simple" ? (
          // A fresh editor per profile, so drafts never leak between profiles.
          <SimpleForm key={`s${id}`} profile={profile} options={options.data} settings={s} />
        ) : (
          <AdvancedForm key={`a${id}`} profile={profile} settings={s} />
        )}
        <TestReleaseCard profileId={id} />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <Link
        to="/settings/profiles"
        className="inline-flex items-center gap-1 text-sm text-link hover:underline [&_svg]:size-4"
      >
        <ChevronLeft /> Quality profiles
      </Link>
      <PageHeader
        title={profile ? profile.name : "Quality profile"}
        description={
          profile ? (
            <span className="flex flex-wrap items-center gap-2">
              <span>
                {profile.slug} · used for {appliesLabel[profile.appliesTo]}
              </span>
              {settings.data && (
                <Badge variant={settings.data.mode === "simple" ? "info" : "warning"}>
                  {settings.data.mode === "simple" ? "Simple" : "Advanced"}
                </Badge>
              )}
            </span>
          ) : undefined
        }
      />
      {body}
    </div>
  );
}
