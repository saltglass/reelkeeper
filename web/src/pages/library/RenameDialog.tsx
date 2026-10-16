import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowDown, Check, CircleAlert } from "lucide-react";
import { api, unwrap } from "@/api/client";
import type { components } from "@/api/schema";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { useInvalidateLibrary } from "@/hooks/library";

type VersionRename = components["schemas"]["VersionRename"];

/**
 * Shows what "Rename files" would do, and does it on request. Works for a
 * movie or a series; both endpoints answer in the same shape. Files Reelkeeper
 * isn't sure about (series only) come back with a problem and are never
 * renamed.
 */
export function RenameDialog({
  kind = "movie",
  movieId,
  open,
  onOpenChange,
}: {
  kind?: "movie" | "series";
  /** The movie's or the series' id. */
  movieId: number;
  open: boolean;
  onOpenChange: (o: boolean) => void;
}) {
  const invalidate = useInvalidateLibrary();
  const noun = kind === "series" ? "series" : "movie";
  const params = { params: { path: { id: movieId } } };
  const plan = useQuery({
    queryKey: [kind === "series" ? "series" : "movies", movieId, "rename"],
    queryFn: () =>
      kind === "series"
        ? unwrap(api.GET("/series/{id}/rename", params))
        : unwrap(api.GET("/movies/{id}/rename", params)),
    enabled: open,
    staleTime: 0,
  });
  const rename = useMutation({
    mutationFn: () =>
      kind === "series"
        ? unwrap(api.POST("/series/{id}/rename", params))
        : unwrap(api.POST("/movies/{id}/rename", params)),
    onSuccess: invalidate,
  });

  function change(o: boolean) {
    onOpenChange(o);
    if (!o) rename.reset();
  }

  const shown: VersionRename[] = rename.data ?? plan.data ?? [];
  const ready = (plan.data ?? []).reduce(
    (n, v) => n + (v.managed ? v.files.filter((f) => !f.problem).length : 0),
    0,
  );
  const nothing = plan.data?.every((v) => v.files.length === 0);
  // Folders Reelkeeper only reads: renaming there is a preview. Said once, at
  // the top, with no greyed-out button that looks broken.
  const readOnly = (plan.data ?? []).filter((v) => !v.managed && v.files.length > 0);
  const previewOnly = !nothing && ready === 0 && readOnly.length > 0;
  const allDoubtful = !nothing && ready === 0 && readOnly.length === 0 && plan.isSuccess;

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogContent className="max-w-2xl">
        <DialogTitle>Rename files</DialogTitle>
        <DialogDescription>
          Gives this {noun}'s files the names from{" "}
          <Link to="/settings/media" className="underline">
            Settings → Media Folders → Naming
          </Link>
          . Files stay in their folders, subtitles with the same name are renamed too, and nothing
          is ever overwritten.
          {kind === "series" && " Files Reelkeeper isn't sure about are never renamed."}
        </DialogDescription>
        {plan.error && <Alert variant="error">{plan.error.message}</Alert>}
        {rename.error && <Alert variant="error">{rename.error.message}</Alert>}
        {plan.isLoading && <p className="text-sm text-muted-foreground">Working it out…</p>}
        {nothing && <Alert variant="success">Every file already has the right name.</Alert>}
        {previewOnly && !rename.isSuccess && (
          <Alert variant="info">
            This is a preview: Reelkeeper only reads {readOnly.map((v) => v.rootPath).join(" and ")}
            , so nothing will be renamed there. To let Reelkeeper rename files, turn on "Reelkeeper
            manages files here" in{" "}
            <Link to="/settings/media" className="underline">
              Settings → Media Folders
            </Link>
            .
          </Alert>
        )}
        {allDoubtful && !rename.isSuccess && (
          <Alert variant="warning">
            None of these files can be renamed yet; each one says why below.
          </Alert>
        )}

        <div className="max-h-[60vh] space-y-4 overflow-y-auto">
          {shown
            .filter((v) => v.files.length > 0)
            .map((v) => (
              <div key={v.versionId} className="space-y-2">
                <p className="text-sm font-medium">{v.profileName}</p>
                {!v.managed && !previewOnly && (
                  <Alert variant="info">
                    Reelkeeper only reads {v.rootPath}, so these won't be renamed. Turn on
                    "Reelkeeper manages files here" for it in Settings → Media Folders first.
                  </Alert>
                )}
                <ul className="space-y-2">
                  {v.files.map((f) => (
                    <li key={f.fileId} className="space-y-1 rounded-md border p-2 text-xs">
                      <p className="font-mono break-all text-muted-foreground">{f.from}</p>
                      <ArrowDown className="size-3 text-muted-foreground" />
                      <p className="font-mono break-all">{f.to}</p>
                      {f.extras.length > 0 && (
                        <p className="text-muted-foreground">
                          Also renamed: {f.extras.map((x) => x.from.split("/").pop()).join(", ")}
                        </p>
                      )}
                      {f.done && (
                        <p className="flex items-center gap-1 text-success">
                          <Check className="size-3" /> Renamed
                        </p>
                      )}
                      {f.problem && (
                        <p className="flex items-center gap-1 text-warning">
                          <CircleAlert className="size-3 shrink-0" /> {f.problem}
                        </p>
                      )}
                    </li>
                  ))}
                </ul>
              </div>
            ))}
        </div>

        <div className="flex justify-end gap-2">
          <DialogClose asChild>
            <Button variant={ready === 0 ? "default" : "ghost"}>
              {rename.isSuccess || ready === 0 ? "Close" : "Cancel"}
            </Button>
          </DialogClose>
          {!rename.isSuccess && ready > 0 && (
            <Button disabled={rename.isPending} onClick={() => rename.mutate()}>
              {rename.isPending ? "Renaming…" : `Rename ${ready} ${ready === 1 ? "file" : "files"}`}
            </Button>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
