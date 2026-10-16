import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { api, unwrap } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { queueQuery, useClientName } from "@/hooks/downloads";
import { type MovieDetail, rootsQuery } from "@/hooks/library";

/**
 * Deleting a movie: the owner picks, every time, whether its files stay or
 * go. Keeping is the default; deleting is only offered in media folders
 * where "Manage files" is on (the server enforces that too).
 */
export function DeleteMovieDialog({
  movie,
  open,
  onOpenChange,
}: {
  movie: MovieDetail;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [choice, setChoice] = useState<"keep" | "delete">("keep");
  const roots = useQuery(rootsQuery);
  const queue = useQuery({ ...queueQuery, enabled: open });
  const client = useClientName();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const label = `${movie.title}${movie.year > 0 ? ` (${movie.year})` : ""}`;

  const readOnly = [
    ...new Set(
      movie.versions
        .map((v) => roots.data?.find((r) => r.id === v.rootFolderId))
        .filter((r) => r && !r.manageFiles)
        .map((r) => r?.path),
    ),
  ];
  // Until the folder list has loaded we can't tell, so deleting stays off.
  const canDelete = Boolean(roots.data) && readOnly.length === 0;
  const files = movie.versions.flatMap((v) => v.mediaFiles.map((f) => f.path));
  const hasDownloads = queue.data?.items.some((i) => i.movie?.id === movie.id) ?? false;
  const deleting = choice === "delete" && canDelete;

  const remove = useMutation({
    mutationFn: () =>
      unwrap(
        api.DELETE("/movies/{id}", {
          params: { path: { id: movie.id }, query: { deleteFiles: deleting } },
        }),
      ),
    onSuccess: async () => {
      await navigate({
        to: "/movies",
        search: { deleted: label, kept: deleting ? undefined : true },
      });
      qc.removeQueries({ queryKey: ["movies", movie.id] });
      for (const key of ["movies", "library", "queue", "history"]) {
        qc.invalidateQueries({ queryKey: [key] });
      }
    },
  });

  function close(o: boolean) {
    if (!o) {
      remove.reset();
      setChoice("keep");
    }
    onOpenChange(o);
  }

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent>
        <DialogTitle>Delete {label}?</DialogTitle>
        <DialogDescription>
          {hasDownloads
            ? `Its downloads in the Queue are removed from ${client} too.`
            : "Any downloads for it in the Queue are removed too."}
        </DialogDescription>
        <div className="space-y-3">
          <label className="flex items-start gap-2 text-sm">
            <input
              type="radio"
              name="delete-choice"
              className="mt-1"
              checked={choice === "keep"}
              onChange={() => setChoice("keep")}
            />
            <span>
              <span className="font-medium">Keep the files</span>
              <br />
              <span className="text-muted-foreground">
                Reelkeeper stops tracking this movie and its files stay where they are. Scans will
                skip its folder from now on (you can undo that under Settings → Media Folders).
              </span>
            </span>
          </label>
          <label className="flex items-start gap-2 text-sm">
            <input
              type="radio"
              name="delete-choice"
              className="mt-1"
              checked={choice === "delete"}
              disabled={!canDelete}
              onChange={() => setChoice("delete")}
            />
            <span className={canDelete ? undefined : "text-muted-foreground"}>
              <span className="font-medium">Also delete the files</span>
              <br />
              <span className="text-muted-foreground">
                Deletes this movie's video files, subtitles named after them, and its folder (if
                nothing else is in it). This can't be undone.
              </span>
              {readOnly.map((p) => (
                <span key={p} className="mt-1 block text-warning">
                  {p} is read-only to Reelkeeper. Turn on Manage files under Settings → Media
                  Folders to delete files there.
                </span>
              ))}
            </span>
          </label>
          {deleting && (
            <div className="rounded-md border p-3 text-xs">
              <p className="mb-1 font-medium">
                {files.length === 0
                  ? "There are no video files on disk to delete."
                  : "These files will be deleted:"}
              </p>
              <ul className="space-y-0.5 font-mono break-all text-muted-foreground">
                {files.map((f) => (
                  <li key={f}>{f}</li>
                ))}
              </ul>
            </div>
          )}
        </div>
        {remove.error && <Alert variant="error">{remove.error.message}</Alert>}
        <div className="flex justify-end gap-2">
          <DialogClose asChild>
            <Button variant="ghost">Cancel</Button>
          </DialogClose>
          <Button
            variant={deleting ? "destructive" : "default"}
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Deleting…" : deleting ? "Delete movie and files" : "Delete movie"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
