import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ChevronRight, Film, Folder, FolderUp, Plus, RefreshCw, Trash2, Tv } from "lucide-react";
import { type CSSProperties, type FormEvent, useState } from "react";
import { api, RequestError, unwrap } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import {
  profilesQuery,
  type RootFolder,
  rootsQuery,
  useInvalidateLibrary,
  useScanRoot,
} from "@/hooks/library";
import { formatBytes } from "@/lib/format";
import { relativeTime } from "@/lib/time";
import { PageHeader } from "@/pages/PageHeader";
import { BusyLabel, IconTile } from "./BusyLabel";
import { ImportRadarrCard } from "./ImportRadarrCard";
import { ImportSonarrCard } from "./ImportSonarrCard";
import { NamingCard } from "./NamingCard";
import { SkippedFoldersCard } from "./SkippedFoldersCard";

export function MediaFoldersPage() {
  const roots = useQuery(rootsQuery);
  const [adding, setAdding] = useState(false);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Media folders"
        description="Where your movies and TV shows live. Reelkeeper scans these folders, identifies what's in them, and checks again every 6 hours."
      />
      {roots.error && <Alert variant="error">{roots.error.message}</Alert>}
      {roots.data?.length === 0 && (
        <Alert variant="info">
          No folders yet. Add the folder that holds your movies or TV shows to get started.
        </Alert>
      )}
      {roots.data?.map((r, i) => (
        <RootCard key={r.id} root={r} index={i} />
      ))}
      <Button onClick={() => setAdding(true)}>
        <Plus /> Add a media folder
      </Button>
      <AddRootDialog open={adding} onOpenChange={setAdding} />
      <SkippedFoldersCard />
      <ImportRadarrCard />
      <ImportSonarrCard />
      <NamingCard />
    </div>
  );
}

function RootCard({ root, index }: { root: RootFolder; index: number }) {
  const profiles = useQuery(profilesQuery);
  const invalidate = useInvalidateLibrary();
  const scan = useScanRoot();
  const [confirmManage, setConfirmManage] = useState(false);
  // The value the user is about to switch upgrades to; null while no question is open.
  const [confirmUpgrade, setConfirmUpgrade] = useState<boolean | null>(null);
  const [confirmRemove, setConfirmRemove] = useState(false);
  const update = useMutation({
    mutationFn: (body: {
      defaultProfileId: number;
      manageFiles: boolean;
      upgradeFound?: boolean;
    }) => unwrap(api.PUT("/library/roots/{id}", { params: { path: { id: root.id } }, body })),
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: () =>
      unwrap(api.DELETE("/library/roots/{id}", { params: { path: { id: root.id } } })),
    onSuccess: invalidate,
  });
  // A refused profile switch is shown under the dropdown, not as a banner.
  const profileErr =
    update.error instanceof RequestError ? update.error.fields.defaultProfileId : undefined;
  const err = (profileErr ? undefined : update.error) ?? remove.error ?? scan.error;

  const isTv = root.mediaType === "series";
  const noun = isTv ? "shows" : "movies";
  const movieCount = `${root.titles} ${root.titles === 1 ? "movie" : "movies"}`;
  const stats = [
    isTv ? `${root.titles} series` : `${root.titles} movies`,
    `${root.files} files`,
    root.size > 0 && formatBytes(root.size),
    root.freeSpace >= 0 && `${formatBytes(root.freeSpace)} free`,
  ].filter(Boolean);

  // How much of the disk Reelkeeper's files here and the free space make up, when the
  // server knows both. (The disk's total size isn't reported, so the bar shows files vs free.)
  const showBar = root.size > 0 && root.freeSpace >= 0;
  const usedPct = showBar ? Math.round((root.size / (root.size + root.freeSpace)) * 100) : 0;

  return (
    <Card className="slib-rise" style={{ "--i": index } as CSSProperties}>
      <CardHeader className="flex flex-row flex-wrap items-start gap-3">
        <IconTile>{isTv ? <Tv /> : <Film />}</IconTile>
        <div className="mr-auto min-w-0 flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <CardTitle className="font-mono break-all">{root.path}</CardTitle>
            <Badge variant="info">{isTv ? "TV shows" : "Movies"}</Badge>
          </div>
          <CardDescription>
            {stats.join(" · ")}
            {root.lastScanAt ? ` · scanned ${relativeTime(root.lastScanAt)}` : " · not scanned yet"}
          </CardDescription>
          <div className="flex flex-wrap gap-1 pt-1">
            {root.missing > 0 && <Badge variant="warning">{root.missing} files missing</Badge>}
            {root.review > 0 && (
              <Link to="/review">
                <Badge variant="warning">{root.review} to review</Badge>
              </Link>
            )}
          </div>
        </div>
        <Button
          variant="outline"
          size="sm"
          disabled={scan.isPending}
          onClick={() => scan.mutate(root.id)}
        >
          {scan.isPending || scan.isSuccess ? (
            <BusyLabel busy={scan.isPending} ok={scan.isSuccess}>
              {scan.isPending ? "Starting…" : "Scan now"}
            </BusyLabel>
          ) : (
            <>
              <RefreshCw /> Scan now
            </>
          )}
        </Button>
      </CardHeader>
      <CardContent>
        {err && <Alert variant="error">{err.message}</Alert>}
        {scan.isSuccess && (
          <Alert variant="info">
            Scan started. Follow it under{" "}
            <Link to="/system/tasks" className="underline">
              System → Tasks
            </Link>
            .
          </Alert>
        )}
        {showBar && (
          <div className="space-y-2">
            <div
              role="img"
              aria-label={`Reelkeeper's files here are ${usedPct}% of the files plus free space`}
              className="slib-bar flex h-2.5 overflow-hidden rounded-full bg-muted"
            >
              <i
                className="rounded-full bg-gradient-to-r from-brand-from to-brand-to"
                style={{ width: `${usedPct}%` }}
              />
            </div>
            <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
              <span className="flex items-center gap-1.5">
                <span className="size-2 rounded-full bg-brand-from" aria-hidden="true" />
                {formatBytes(root.size)} of {noun} here
              </span>
              <span className="flex items-center gap-1.5">
                <span className="size-2 rounded-full bg-muted-foreground/40" aria-hidden="true" />
                {formatBytes(root.freeSpace)} free on the disk
              </span>
            </div>
          </div>
        )}
        <Field
          id={`profile-${root.id}`}
          label={isTv ? "Shows here are the version" : "Movies here are the version"}
          help={`Each folder holds one version of its ${noun}. If the same ${isTv ? "show" : "movie"} is also in another folder with a different profile, Reelkeeper shows both versions under one title. Changing this moves the folder's ${noun} to the new profile; no files are touched.`}
          error={profileErr}
        >
          <select
            id={`profile-${root.id}`}
            className="w-full max-w-xs min-h-10 rounded-lg border border-input bg-background px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none"
            value={root.defaultProfileId}
            onChange={(e) =>
              update.mutate({
                defaultProfileId: Number(e.target.value),
                manageFiles: root.manageFiles,
              })
            }
          >
            {profiles.data
              ?.filter(
                (p) =>
                  p.appliesTo !== (isTv ? "movie" : "series") || p.id === root.defaultProfileId,
              )
              .map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
          </select>
        </Field>
        <div className="flex items-start justify-between gap-4">
          <div className="space-y-1">
            <label htmlFor={`manage-${root.id}`} className="text-sm font-medium">
              Reelkeeper manages files here
            </label>
            <p className="text-sm text-muted-foreground">
              {root.manageFiles
                ? "Reelkeeper may rename and organise files in this folder."
                : "Off: Reelkeeper only reads this folder and never changes anything in it. Keep it off while Radarr or Sonarr still manages the folder."}
            </p>
          </div>
          <Switch
            id={`manage-${root.id}`}
            checked={root.manageFiles}
            onCheckedChange={(on) =>
              on
                ? setConfirmManage(true)
                : update.mutate({ defaultProfileId: root.defaultProfileId, manageFiles: false })
            }
          />
        </div>
        {!isTv && (
          <div className="flex items-start justify-between gap-4">
            <div className="space-y-1">
              <label htmlFor={`upgrade-${root.id}`} className="text-sm font-medium">
                Upgrade movies found here
              </label>
              <p className="text-sm text-muted-foreground">
                {root.upgradeFound
                  ? "Reelkeeper looks for better copies of the movies in this folder, as their quality profile says."
                  : "Off: Reelkeeper keeps the files in this folder as they are and only downloads movies that are missing. You can turn upgrades on for one movie on its page."}
              </p>
            </div>
            <Switch
              id={`upgrade-${root.id}`}
              checked={root.upgradeFound}
              onCheckedChange={setConfirmUpgrade}
            />
          </div>
        )}
        <div className="flex justify-end">
          <Button variant="ghost" size="sm" onClick={() => setConfirmRemove(true)}>
            <Trash2 /> Remove folder
          </Button>
        </div>
      </CardContent>

      <Dialog open={confirmManage} onOpenChange={setConfirmManage}>
        <DialogContent>
          <DialogTitle>Let Reelkeeper manage files in this folder?</DialogTitle>
          <DialogDescription>
            Reelkeeper may then rename and organise files in {root.path}. Only turn this on once no
            other app (Radarr or Sonarr) manages the folder, or the two will fight over the same
            files.
          </DialogDescription>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button
              onClick={() => {
                update.mutate({ defaultProfileId: root.defaultProfileId, manageFiles: true });
                setConfirmManage(false);
              }}
            >
              Yes, Reelkeeper manages it
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      <Dialog open={confirmUpgrade !== null} onOpenChange={(o) => !o && setConfirmUpgrade(null)}>
        <DialogContent>
          <DialogTitle>
            {confirmUpgrade
              ? `Look for better copies of ${movieCount}?`
              : `Stop upgrading ${movieCount}?`}
          </DialogTitle>
          <DialogDescription>
            {confirmUpgrade
              ? `Reelkeeper will turn upgrades on for every movie in ${root.path}, including ones it downloaded, and look for better copies at its usual pace.`
              : `Reelkeeper keeps every file in ${root.path} as it is and stops looking for better copies of them. Missing movies are still downloaded. You can turn upgrades back on for one movie on its page.`}
          </DialogDescription>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button
              onClick={() => {
                update.mutate({
                  defaultProfileId: root.defaultProfileId,
                  manageFiles: root.manageFiles,
                  upgradeFound: confirmUpgrade === true,
                });
                setConfirmUpgrade(null);
              }}
            >
              {confirmUpgrade ? "Yes, look for better copies" : "Yes, keep the files"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      <Dialog open={confirmRemove} onOpenChange={setConfirmRemove}>
        <DialogContent>
          <DialogTitle>Remove {root.path}?</DialogTitle>
          <DialogDescription>
            Reelkeeper forgets the {root.titles} {isTv ? "series" : "movies"} it found here. Your
            files stay exactly where they are; add the folder again any time to find them again.
          </DialogDescription>
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost">Cancel</Button>
            </DialogClose>
            <Button
              variant="destructive"
              onClick={() => {
                remove.mutate();
                setConfirmRemove(false);
              }}
            >
              Remove folder
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

function AddRootDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
}) {
  const profiles = useQuery(profilesQuery);
  const qc = useQueryClient();
  const [path, setPath] = useState("");
  const [browse, setBrowse] = useState("/");
  const [mediaType, setMediaType] = useState<"movie" | "series">("movie");
  // Movies only. Off, so adding a big library doesn't queue an upgrade search for every movie.
  const [upgradeFound, setUpgradeFound] = useState(false);
  const isTv = mediaType === "series";
  // Only profiles that can hold this kind of media; default to the first of them.
  const fitting = (profiles.data ?? []).filter((p) => p.appliesTo !== (isTv ? "movie" : "series"));
  const [chosenProfile, setProfileId] = useState<number | undefined>();
  const profileId = fitting.find((p) => p.id === chosenProfile)?.id ?? fitting[0]?.id ?? 0;
  const listing = useQuery({
    queryKey: ["library", "browse", browse],
    queryFn: () => unwrap(api.GET("/library/browse", { params: { query: { path: browse } } })),
    enabled: open,
  });
  const add = useMutation({
    mutationFn: () =>
      unwrap(
        api.POST("/library/roots", {
          // A TV folder has no upgrade setting; the server ignores it there.
          body: {
            path,
            mediaType,
            defaultProfileId: profileId,
            ...(isTv ? {} : { upgradeFound }),
          },
        }),
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["library"] });
      qc.invalidateQueries({ queryKey: ["system", "tasks"] });
      onOpenChange(false);
      setPath("");
      setUpgradeFound(false);
      add.reset();
    },
  });
  const err = add.error instanceof RequestError ? add.error : undefined;

  function submit(e: FormEvent) {
    e.preventDefault();
    add.mutate();
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogTitle>Add a media folder</DialogTitle>
        <DialogDescription>
          Choose the folder that holds your {isTv ? "TV shows" : "movies"}, as Reelkeeper sees it
          inside its container (for example {isTv ? "/media/shows" : "/media/movies"}). Reelkeeper
          scans it straight away; this only reads the folder.
        </DialogDescription>
        <form onSubmit={submit} className="space-y-4">
          {err && !Object.keys(err.fields).length && <Alert variant="error">{err.message}</Alert>}
          <Field
            id="root-type"
            label="What's in it"
            help="A folder holds either movies or TV shows, not both."
          >
            <select
              id="root-type"
              className="w-full min-h-10 rounded-lg border border-input bg-background px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none"
              value={mediaType}
              onChange={(e) => setMediaType(e.target.value as "movie" | "series")}
            >
              <option value="movie">Movies</option>
              <option value="series">TV shows</option>
            </select>
          </Field>
          <div className="rounded-md border">
            <div className="flex items-center gap-2 border-b px-3 py-2 font-mono text-xs">
              {listing.data?.parent !== undefined && (
                <Button
                  type="button"
                  variant="tile"
                  size="icon"
                  aria-label="Up one folder"
                  className="size-7"
                  onClick={() => setBrowse(listing.data?.parent ?? "/")}
                >
                  <FolderUp />
                </Button>
              )}
              <span className="break-all">{listing.data?.path ?? browse}</span>
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="ml-auto"
                onClick={() => setPath(listing.data?.path ?? browse)}
              >
                Use this folder
              </Button>
            </div>
            <ul className="max-h-56 overflow-y-auto py-1">
              {listing.error && (
                <li className="px-3 py-2 text-sm text-destructive">{listing.error.message}</li>
              )}
              {listing.data?.folders.length === 0 && (
                <li className="px-3 py-2 text-sm text-muted-foreground">No sub-folders.</li>
              )}
              {listing.data?.folders.map((f) => (
                <li key={f.path}>
                  <button
                    type="button"
                    className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-muted"
                    onClick={() => setBrowse(f.path)}
                  >
                    <Folder className="size-4 text-muted-foreground" />
                    {f.name}
                    <ChevronRight className="ml-auto size-4 text-muted-foreground" />
                  </button>
                </li>
              ))}
            </ul>
          </div>
          <Field id="root-path" label="Folder" error={err?.fields.path}>
            <Input
              id="root-path"
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder={isTv ? "/media/shows" : "/media/movies"}
              className="font-mono"
            />
          </Field>
          <Field
            id="root-profile"
            label={isTv ? "Shows here are the version" : "Movies here are the version"}
            help="For example HD for your regular copies, Remux for a remux folder."
            error={err?.fields.defaultProfileId}
          >
            <select
              id="root-profile"
              className="w-full min-h-10 rounded-lg border border-input bg-background px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none"
              value={profileId}
              onChange={(e) => setProfileId(Number(e.target.value))}
            >
              {fitting.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </Field>
          {!isTv && (
            <div className="flex items-start justify-between gap-4">
              <div className="space-y-1">
                <label htmlFor="root-upgrade" className="text-sm font-medium">
                  Upgrade movies already in this folder
                </label>
                <p className="text-sm text-muted-foreground">
                  Off: Reelkeeper catalogs the movies it finds and keeps their files as they are.
                  Movies it downloads later are still upgraded as their quality profile says. You
                  can turn this on later.
                </p>
              </div>
              <Switch id="root-upgrade" checked={upgradeFound} onCheckedChange={setUpgradeFound} />
            </div>
          )}
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button type="button" variant="ghost">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" disabled={!path || add.isPending}>
              {add.isPending ? "Adding…" : "Add and scan"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
