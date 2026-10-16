import { useBlocker } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";

/**
 * A floating bar at the bottom of the window while there are unsaved changes,
 * with Discard and Save. After a save it says "Saved" for a moment. While
 * there are unsaved changes, leaving the page asks first: settings typed in
 * and tested, but never saved, were once silently lost this way.
 */
export function SaveBar({
  dirty,
  saving,
  saved,
  saveDisabled = false,
  onDiscard,
}: {
  dirty: boolean;
  saving: boolean;
  saved: boolean;
  /** Turns Save off although there are changes (the page says why). */
  saveDisabled?: boolean;
  onDiscard: () => void;
}) {
  const blocker = useBlocker({
    shouldBlockFn: () => dirty,
    enableBeforeUnload: () => dirty, // closing or reloading the tab
    withResolver: true,
  });

  // "Saved" is shown for a few seconds after the changes were saved.
  const [showSaved, setShowSaved] = useState(false);
  useEffect(() => {
    if (!(saved && !dirty)) {
      setShowSaved(false);
      return;
    }
    setShowSaved(true);
    const t = setTimeout(() => setShowSaved(false), 2500);
    return () => clearTimeout(t);
  }, [saved, dirty]);

  return (
    <>
      {(dirty || saving) && (
        <div
          role="status"
          className="animate-bar-up fixed inset-x-4 bottom-4 z-[55] mx-auto flex max-w-fit items-center gap-3 rounded-xl border border-primary/45 bg-card py-2 pr-2 pl-4 shadow-md sm:bottom-6"
        >
          <span className="ping-dot text-warning" aria-hidden="true" />
          <span className="text-sm font-medium">You have unsaved changes</span>
          <Button type="button" variant="ghost" disabled={!dirty || saving} onClick={onDiscard}>
            Discard
          </Button>
          <Button type="submit" disabled={!dirty || saving || saveDisabled}>
            {saving ? "Saving…" : "Save"}
          </Button>
        </div>
      )}
      {showSaved && !dirty && !saving && (
        <div
          role="status"
          className="animate-bar-up fixed inset-x-4 bottom-4 z-[55] mx-auto flex max-w-fit items-center gap-2 rounded-xl border bg-card px-4 py-3 text-sm font-medium text-success shadow-md sm:bottom-6"
        >
          Saved
        </div>
      )}
      <Dialog
        open={blocker.status === "blocked"}
        onOpenChange={(open) => !open && blocker.reset?.()}
      >
        <DialogContent>
          <DialogTitle>Leave without saving?</DialogTitle>
          <DialogDescription>
            Your changes on this page haven't been saved. Testing doesn't save them.
          </DialogDescription>
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => blocker.reset?.()}>
              Stay
            </Button>
            <Button variant="destructive" onClick={() => blocker.proceed?.()}>
              Leave without saving
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
