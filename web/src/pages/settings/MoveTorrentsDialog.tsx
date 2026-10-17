import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { clientNames, type DownloadClientType, type DownloadMovePreview } from "@/hooks/downloads";

/** "1 torrent", "3 torrents". */
function torrents(n: number) {
  return n === 1 ? "1 torrent" : `${n} torrents`;
}

/**
 * Shown when saving a change of download client that has torrents to move
 * (Settings → Download Client). Everything in the Queue always moves; the
 * seeding ones may be left behind, so they can finish seeding in the old
 * client (a private tracker may count it against you if they stop early).
 * Not when the old client is the built-in one: it stops once the client is
 * switched, so nothing left in it would go on seeding.
 */
export function MoveTorrentsDialog({
  open,
  from,
  to,
  preview,
  leaveSeeding,
  saving,
  onLeaveSeedingChange,
  onConfirm,
  onCancel,
}: {
  open: boolean;
  from: DownloadClientType;
  to: DownloadClientType;
  preview: DownloadMovePreview;
  leaveSeeding: boolean;
  saving: boolean;
  onLeaveSeedingChange: (leave: boolean) => void;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const oldName = clientNames[from];
  const newName = clientNames[to];
  // What would be moved, less what can't be.
  const problems = preview.cannotMove.filter((p) => !(leaveSeeding && p.seeding));
  const downloading = preview.downloading - problems.filter((p) => !p.seeding).length;
  const moving = preview.downloading + (leaveSeeding ? 0 : preview.seeding) - problems.length;
  return (
    <Dialog open={open} onOpenChange={(o) => !o && !saving && onCancel()}>
      <DialogContent>
        {moving > 0 ? (
          <>
            <DialogTitle>
              Move {torrents(moving)} to {newName}?
            </DialogTitle>
            <DialogDescription>
              {downloading > 0 &&
                `${downloading === 1 ? "1 is" : `${downloading} are`} still downloading. `}
              Files already downloaded are checked, not downloaded again; nothing is deleted.
            </DialogDescription>
          </>
        ) : (
          <>
            <DialogTitle>Switch to {newName}?</DialogTitle>
            <DialogDescription>
              {problems.length > 0
                ? "None of Reelkeeper's torrents can move. "
                : "Nothing needs to move. "}
              {preview.seeding > 0 &&
                `Reelkeeper will stop tracking ${preview.seeding === 1 ? "the torrent" : `the ${preview.seeding} torrents`} seeding in ${oldName}. `}
              Nothing is deleted.
            </DialogDescription>
          </>
        )}
        {preview.seeding > 0 && from !== "builtin" && (
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              className="mt-1"
              checked={leaveSeeding}
              disabled={saving}
              onChange={(e) => onLeaveSeedingChange(e.target.checked)}
            />
            <span>
              <span className="font-medium">Leave the seeding ones in {oldName}</span>
              <br />
              <span className="text-muted-foreground">
                {preview.seeding === 1
                  ? "The 1 torrent still seeding stays"
                  : `The ${preview.seeding} torrents still seeding stay`}{" "}
                there to finish, so a private tracker's share rules aren't broken. Reelkeeper stops
                tracking them.
              </span>
            </span>
          </label>
        )}
        {problems.length > 0 && (
          <Alert variant="warning">
            <p className="font-medium">
              {problems.length === 1
                ? "This one can't move and stays where it is:"
                : "These can't move and stay where they are:"}
            </p>
            <ul className="mt-1 list-disc space-y-1 pl-5">
              {problems.map((p) => (
                <li key={p.release} className="break-words">
                  <span className="font-mono text-xs">{p.release}</span>: {p.reason}{" "}
                  {p.seeding
                    ? `It keeps seeding in ${oldName}, untracked.`
                    : `It is stopped in ${oldName} and Reelkeeper searches for it again.`}
                </li>
              ))}
            </ul>
          </Alert>
        )}
        {preview.clientError && (
          <Alert variant="warning">
            Reelkeeper couldn't ask {oldName} which torrents can move: {preview.clientError}
          </Alert>
        )}
        <p className="text-xs text-muted-foreground">
          The move runs as a task (System → Tasks). Grabbing, imports and cleanup wait until it's
          done.
        </p>
        <div className="flex justify-end gap-2">
          <DialogClose asChild>
            <Button type="button" variant="outline" disabled={saving}>
              Cancel
            </Button>
          </DialogClose>
          <Button type="button" autoFocus disabled={saving} onClick={onConfirm}>
            {saving ? "Saving…" : moving > 0 ? "Move them" : "Switch"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
