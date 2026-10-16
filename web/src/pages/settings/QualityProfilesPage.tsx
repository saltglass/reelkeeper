import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Pencil, Plus, Settings2, SlidersHorizontal, Trash2 } from "lucide-react";
import { type CSSProperties, type FormEvent, useState } from "react";
import { api, RequestError, unwrap } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { type Profile, profilesQuery, useInvalidateLibrary } from "@/hooks/library";
import { profileOptionsQuery } from "@/hooks/profiles";
import { PageHeader } from "@/pages/PageHeader";
import { IconTile } from "./BusyLabel";

type AppliesTo = Profile["appliesTo"];

const appliesLabel: Record<AppliesTo, string> = {
  movie: "Movies",
  series: "TV",
  both: "Movies and TV",
};

function count(n: number, one: string, many: string) {
  return `${n} ${n === 1 ? one : many}`;
}

/** What uses a profile, as short parts: ["2 media folders", "14 movies"]. */
function usageParts(p: Profile) {
  const parts: string[] = [];
  if (p.mediaFolders > 0) parts.push(count(p.mediaFolders, "media folder", "media folders"));
  if (p.movies > 0) parts.push(count(p.movies, "movie", "movies"));
  if (p.series > 0) parts.push(count(p.series, "series", "series"));
  return parts;
}

function andList(parts: string[]) {
  return parts.length < 2
    ? parts.join("")
    : `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`;
}

export function QualityProfilesPage() {
  const profiles = useQuery(profilesQuery);
  const [editing, setEditing] = useState<Profile | "new" | null>(null);
  const [deleting, setDeleting] = useState<Profile | null>(null);
  const list = profiles.data ?? [];
  return (
    <div className="space-y-6">
      <PageHeader
        title="Quality profiles"
        description="A quality profile is a kind of copy, like HD or Remux. Each media folder files its movies under one profile, so one movie can have an HD copy and a Remux copy. Open a profile's settings to choose what it downloads: resolutions, sources, size limits, languages and more."
      />
      {profiles.error && <Alert variant="error">{profiles.error.message}</Alert>}
      {list.map((p, i) => (
        <ProfileCard
          key={p.id}
          index={i}
          profile={p}
          onlyOne={list.length === 1}
          onEdit={() => setEditing(p)}
          onDelete={() => setDeleting(p)}
        />
      ))}
      <Button onClick={() => setEditing("new")}>
        <Plus /> Add a profile
      </Button>
      {editing && (
        <ProfileDialog
          key={editing === "new" ? "new" : editing.id}
          profile={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
        />
      )}
      {deleting && <DeleteDialog profile={deleting} onClose={() => setDeleting(null)} />}
    </div>
  );
}

function ProfileCard({
  profile,
  index,
  onlyOne,
  onEdit,
  onDelete,
}: {
  profile: Profile;
  index: number;
  onlyOne: boolean;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const parts = usageParts(profile);
  // Say why Delete is off, so nobody has to guess.
  const blocked = onlyOne
    ? "Reelkeeper needs at least one profile, so the last one can't be deleted."
    : parts.length > 0
      ? `Used by ${andList(parts)}. To delete it, switch those media folders to another profile first.`
      : "";
  return (
    <Card className="slib-rise" style={{ "--i": index } as CSSProperties}>
      <CardContent>
        <div className="flex flex-wrap items-start gap-3">
          <IconTile>
            <SlidersHorizontal />
          </IconTile>
          <div className="mr-auto min-w-0 flex-1 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="font-medium break-words">
                <Link
                  to="/settings/profiles/$id"
                  params={{ id: String(profile.id) }}
                  className="hover:underline"
                >
                  {profile.name}
                </Link>
              </h2>
              <Badge variant="info">{appliesLabel[profile.appliesTo]}</Badge>
            </div>
            <p className="text-sm text-muted-foreground">
              {parts.length > 0 ? parts.join(" · ") : "Not used yet"}
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button asChild variant="outline" size="sm">
              <Link to="/settings/profiles/$id" params={{ id: String(profile.id) }}>
                <Settings2 /> Edit settings
              </Link>
            </Button>
            <Button variant="outline" size="sm" onClick={onEdit}>
              <Pencil /> Edit
            </Button>
            <Button
              variant="ghost"
              size="sm"
              disabled={blocked !== ""}
              aria-label={`Delete ${profile.name}`}
              onClick={onDelete}
            >
              <Trash2 /> Delete
            </Button>
          </div>
        </div>
        {blocked && <p className="mt-3 text-sm text-muted-foreground">{blocked}</p>}
      </CardContent>
    </Card>
  );
}

function ProfileDialog({ profile, onClose }: { profile?: Profile; onClose: () => void }) {
  const invalidate = useInvalidateLibrary();
  const [name, setName] = useState(profile?.name ?? "");
  const [appliesTo, setAppliesTo] = useState<AppliesTo>(profile?.appliesTo ?? "movie");
  const [preset, setPreset] = useState("hd");
  const options = useQuery({ ...profileOptionsQuery, enabled: !profile });
  const save = useMutation({
    mutationFn: () => {
      const body = { name, appliesTo };
      return unwrap(
        profile
          ? api.PUT("/profiles/{id}", { params: { path: { id: profile.id } }, body })
          : api.POST("/profiles", { body: { ...body, preset } }),
      );
    },
    onSuccess: () => {
      invalidate();
      onClose();
    },
  });
  const err = save.error instanceof RequestError ? save.error : undefined;

  function submit(e: FormEvent) {
    e.preventDefault();
    save.mutate();
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogTitle>{profile ? `Edit ${profile.name}` : "Add a quality profile"}</DialogTitle>
        <DialogDescription>
          {profile
            ? "Renaming is safe: everything that uses this profile keeps using it."
            : "Give the copy a name, like 4K or Kids."}
        </DialogDescription>
        <form onSubmit={submit} className="space-y-4">
          {save.error && !Object.keys(err?.fields ?? {}).length && (
            <Alert variant="error">{save.error.message}</Alert>
          )}
          <Field
            id="profile-name"
            label="Name"
            help="Up to 40 characters."
            error={err?.fields.name}
          >
            <Input
              id="profile-name"
              value={name}
              maxLength={40}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
          <Field
            id="profile-applies"
            label="Used for"
            help="Which kinds of media can be filed under this profile."
            error={err?.fields.appliesTo}
          >
            <select
              id="profile-applies"
              className="w-full min-h-10 rounded-lg border border-input bg-background px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none"
              value={appliesTo}
              onChange={(e) => setAppliesTo(e.target.value as AppliesTo)}
            >
              <option value="movie">Movies</option>
              <option value="series">TV</option>
              <option value="both">Movies and TV</option>
            </select>
          </Field>
          {!profile && (
            <fieldset className="space-y-2">
              <legend className="text-sm font-medium">Start from</legend>
              <p className="text-sm text-muted-foreground">
                A ready-made set of rules. You can change every one of them afterwards.
              </p>
              {err?.fields.preset && (
                <p className="text-sm text-destructive">{err.fields.preset}</p>
              )}
              <div className="grid gap-2">
                {(options.data?.presets ?? []).map((p) => (
                  <label
                    key={p.id}
                    className={`flex cursor-pointer items-start gap-3 rounded-md border p-3 text-sm ${
                      preset === p.id ? "border-primary bg-primary/5" : ""
                    }`}
                  >
                    <input
                      type="radio"
                      name="profile-preset"
                      className="mt-1"
                      checked={preset === p.id}
                      onChange={() => setPreset(p.id)}
                    />
                    <span>
                      <span className="block font-medium">{p.name}</span>
                      <span className="block text-muted-foreground">{p.description}</span>
                    </span>
                  </label>
                ))}
              </div>
            </fieldset>
          )}
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button type="button" variant="ghost">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" disabled={save.isPending}>
              {save.isPending ? "Saving…" : profile ? "Save" : "Add profile"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function DeleteDialog({ profile, onClose }: { profile: Profile; onClose: () => void }) {
  const invalidate = useInvalidateLibrary();
  const remove = useMutation({
    mutationFn: () =>
      unwrap(api.DELETE("/profiles/{id}", { params: { path: { id: profile.id } } })),
    onSuccess: () => {
      invalidate();
      onClose();
    },
    // The page may be out of date; show the server's reason and refresh.
    onError: invalidate,
  });
  const err = remove.error instanceof RequestError ? remove.error : undefined;
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogTitle>Delete {profile.name}?</DialogTitle>
        <DialogDescription>
          Nothing uses this profile, so no movies or folders are affected. You can add it again any
          time.
        </DialogDescription>
        {remove.error && (
          <Alert variant="error">{err?.fields.profile ?? remove.error.message}</Alert>
        )}
        <div className="flex justify-end gap-2">
          <DialogClose asChild>
            <Button variant="ghost">Cancel</Button>
          </DialogClose>
          <Button variant="destructive" disabled={remove.isPending} onClick={() => remove.mutate()}>
            Delete profile
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
