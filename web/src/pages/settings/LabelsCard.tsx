import { useQuery } from "@tanstack/react-query";
import type { FormEvent } from "react";
import { type LabelPart, type LabelSettings, RequestError } from "@/api/client";
import { QualityLabel } from "@/components/QualityLabel";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { useDraft } from "@/hooks/draft";
import { labelSettingsQuery, useSaveLabels } from "@/hooks/settings";
import { type LabelFacts, labelParts, versionLabel } from "@/lib/format";

const partInfo: Record<LabelPart, { name: string; example: string }> = {
  profile: { name: "Quality profile", example: "HD, Remux" },
  quality: { name: "Quality", example: "Bluray-1080p, WEBDL-2160p" },
  hdr: { name: "HDR and Dolby Vision", example: "DV, HDR10" },
  videoCodec: { name: "Video codec", example: "H.264, H.265" },
  audio: { name: "Audio", example: "TrueHD 7.1, DD+ 5.1" },
  releaseGroup: { name: "Release group", example: "FraMeSToR" },
  edition: { name: "Edition", example: "Director's Cut" },
  size: { name: "Size", example: "58 GB" },
};

// Two made-up movies' versions for the preview, using every fact.
const samples: { title: string; facts: LabelFacts }[] = [
  {
    title: "A 4K remux",
    facts: {
      profileName: "Remux",
      quality: "Remux-2160p",
      resolution: 2160,
      hdr: ["DV", "HDR10"],
      videoCodec: "H.265",
      audioCodec: "TrueHD",
      audioChannels: 8,
      releaseGroup: "FraMeSToR",
      edition: "Director's Cut",
      size: 62 * 1024 ** 3,
    },
  },
  {
    title: "A 1080p copy",
    facts: {
      profileName: "HD",
      quality: "WEBDL-1080p",
      resolution: 1080,
      hdr: [],
      videoCodec: "H.264",
      audioCodec: "DD+",
      audioChannels: 6,
      releaseGroup: "FLUX",
      edition: "",
      size: 7.4 * 1024 ** 3,
    },
  },
];

const sameParts = (a: LabelSettings, b: LabelSettings) => a.parts.join() === b.parts.join();

/** What the labels under each movie show (Settings → General). */
export function LabelsCard() {
  const { data, error } = useQuery(labelSettingsQuery);
  const save = useSaveLabels();
  const { draft, setDraft, dirty, commit } = useDraft(data, sameParts);
  const err = save.error instanceof RequestError ? save.error : undefined;

  function toggle(part: LabelPart, on: boolean) {
    if (!draft) return;
    const chosen = on ? [...draft.parts, part] : draft.parts.filter((p) => p !== part);
    // Keep display order so the preview and the saved value agree.
    setDraft({ parts: labelParts.filter((p) => chosen.includes(p)) });
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    if (draft) save.mutate(draft, { onSuccess: commit });
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Labels</CardTitle>
        <CardDescription>
          Choose what the small labels under each movie or series show on the Movies and Series
          pages. They describe the best file of each version. Missing files are always flagged,
          whatever is chosen here.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {error && <Alert variant="error">Couldn't load label settings: {error.message}</Alert>}
        {!draft && !error && <p className="text-sm text-muted-foreground">Loading…</p>}
        {draft && (
          <form onSubmit={submit} className="space-y-5" aria-label="Labels">
            {err && <Alert variant="error">{err.message}</Alert>}
            <fieldset className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
              <legend className="sr-only">Show in labels</legend>
              {labelParts.map((part) => (
                <label key={part} className="flex items-start gap-2 text-sm">
                  <input
                    type="checkbox"
                    className="mt-0.5 size-4 accent-primary"
                    aria-label={partInfo[part].name}
                    aria-describedby={`label-ex-${part}`}
                    checked={draft.parts.includes(part)}
                    onChange={(e) => toggle(part, e.target.checked)}
                  />
                  <span>
                    {partInfo[part].name}
                    <span id={`label-ex-${part}`} className="block text-muted-foreground">
                      {partInfo[part].example}
                    </span>
                  </span>
                </label>
              ))}
            </fieldset>

            <div className="space-y-2">
              <p className="text-sm font-medium">Preview</p>
              <div className="flex flex-wrap gap-4">
                {samples.map((s) => {
                  const pieces = versionLabel(s.facts, draft.parts);
                  return (
                    <div key={s.title} className="w-38 space-y-1" data-testid="label-preview">
                      <p className="text-xs text-muted-foreground">{s.title}</p>
                      {pieces.length > 0 ? (
                        <QualityLabel variant="info">{pieces.join(" · ")}</QualityLabel>
                      ) : (
                        <p className="text-xs text-muted-foreground italic">No label</p>
                      )}
                    </div>
                  );
                })}
              </div>
            </div>

            <div className="flex justify-end">
              <Button type="submit" disabled={!dirty || save.isPending}>
                {save.isPending ? "Saving…" : save.isSuccess && !dirty ? "Saved" : "Save"}
              </Button>
            </div>
          </form>
        )}
      </CardContent>
    </Card>
  );
}
