import { type FormEvent, useState } from "react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { useTestRelease } from "@/hooks/profiles";
import { BusyLabel } from "./BusyLabel";

function Read({ label, value }: { label: string; value: string }) {
  if (!value) return null;
  return (
    <div className="flex gap-2">
      <dt className="w-24 shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{value}</dd>
    </div>
  );
}

/** Tries a release name against the profile's saved settings. */
export function TestReleaseCard({ profileId }: { profileId: number }) {
  const test = useTestRelease(profileId);
  const [name, setName] = useState("");
  const [size, setSize] = useState("");
  const err = test.error instanceof RequestError ? test.error : undefined;
  const r = test.data;

  function submit(e: FormEvent) {
    e.preventDefault();
    const gb = Number(size);
    test.mutate({ name, ...(size.trim() !== "" && !Number.isNaN(gb) ? { sizeGB: gb } : {}) });
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Test a release name</CardTitle>
        <p className="text-sm text-muted-foreground">
          Paste the name of a release to see whether this profile would download it, and why.
        </p>
      </CardHeader>
      <CardContent className="space-y-4">
        <form onSubmit={submit} className="space-y-4">
          {err && !Object.keys(err.fields).length && <Alert variant="error">{err.message}</Alert>}
          <Field id="tr-name" label="Release name" error={err?.fields.name}>
            <Input
              id="tr-name"
              value={name}
              placeholder="Some.Movie.2021.1080p.BluRay.x264-GRP"
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
          <Field
            id="tr-size"
            label="Size in GB (optional)"
            help="Add it to check the size limits."
            error={err?.fields.sizeGB}
          >
            <Input
              id="tr-size"
              type="number"
              min={0}
              step="any"
              className="sm:w-48"
              value={size}
              onChange={(e) => setSize(e.target.value)}
            />
          </Field>
          <Button type="submit" disabled={test.isPending || name.trim() === ""}>
            <BusyLabel busy={test.isPending} ok={test.isSuccess}>
              {test.isPending ? "Testing…" : "Test"}
            </BusyLabel>
          </Button>
        </form>
        <p className="text-sm text-muted-foreground">
          This tests the saved settings, so save your changes first.
        </p>
        {r && (
          <div className="slib-rise space-y-4 border-t pt-4" aria-live="polite">
            <div
              className={`flex flex-wrap items-center gap-2 rounded-lg border p-3 ${
                r.accepted
                  ? "border-success/30 bg-success/5"
                  : "border-destructive/30 bg-destructive/5"
              }`}
            >
              <Badge variant={r.accepted ? "success" : "error"}>
                {r.accepted ? "Would grab" : "Would reject"}
              </Badge>
              <span className="text-sm">
                Quality: <strong>{r.quality}</strong> · Score: <strong>{r.score}</strong>
                {r.upgrade ? " · would replace an existing file" : ""}
              </span>
            </div>
            {r.rejections.length > 0 && (
              <ul className="list-disc space-y-1 rounded-lg border bg-background py-3 pr-3 pl-8 text-sm">
                {r.rejections.map((x) => (
                  <li key={x}>{x}</li>
                ))}
              </ul>
            )}
            {r.formats.length > 0 && (
              <div className="text-sm">
                <div className="font-medium">Matching custom formats</div>
                <ul className="mt-1">
                  {r.formats.map((f) => (
                    <li key={f.name} className="flex justify-between gap-2 sm:max-w-sm">
                      <span>{f.name}</span>
                      <span className="tabular-nums text-muted-foreground">{f.score}</span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
            <div className="text-sm">
              <div className="font-medium">What Reelkeeper read from the name</div>
              <dl className="mt-2 space-y-1 rounded-lg border bg-background p-3">
                <Read label="Title" value={r.parsed.title} />
                <Read label="Year" value={r.parsed.year ? String(r.parsed.year) : ""} />
                <Read
                  label="Resolution"
                  value={r.parsed.resolution ? `${r.parsed.resolution}p` : ""}
                />
                <Read label="Source" value={r.parsed.source} />
                <Read label="Group" value={r.parsed.group} />
                <Read label="Edition" value={r.parsed.edition} />
                <Read label="Languages" value={r.parsed.languages.join(", ")} />
                <Read label="HDR" value={r.parsed.hdr.join(", ")} />
                <Read label="Audio" value={r.parsed.audio.join(", ")} />
                <Read label="Video" value={r.parsed.videoCodec} />
              </dl>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
