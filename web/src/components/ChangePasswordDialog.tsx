import { useMutation } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { api, RequestError, unwrap } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";

export function ChangePasswordDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [done, setDone] = useState(false);

  const change = useMutation({
    mutationFn: () =>
      unwrap(api.PUT("/auth/password", { body: { currentPassword: current, newPassword: next } })),
    onSuccess: () => {
      setDone(true);
      setCurrent("");
      setNext("");
      setConfirm("");
    },
  });

  const mismatch = confirm !== "" && confirm !== next;
  const err = change.error instanceof RequestError ? change.error : undefined;

  function submit(e: FormEvent) {
    e.preventDefault();
    if (mismatch) return;
    change.mutate();
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        onOpenChange(o);
        if (!o) {
          setDone(false);
          change.reset();
        }
      }}
    >
      <DialogContent>
        <DialogTitle>Change password</DialogTitle>
        <DialogDescription>
          You'll stay signed in here. Other browsers and devices are signed out.
        </DialogDescription>
        {done ? (
          <Alert variant="success">Password changed.</Alert>
        ) : (
          <form onSubmit={submit} className="space-y-4">
            {err && !err.fields.newPassword && <Alert variant="error">{err.message}</Alert>}
            <Field id="pw-current" label="Current password">
              <Input
                id="pw-current"
                type="password"
                autoComplete="current-password"
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
                required
              />
            </Field>
            <Field
              id="pw-new"
              label="New password"
              help="At least 8 characters."
              error={err?.fields.newPassword}
            >
              <Input
                id="pw-new"
                type="password"
                autoComplete="new-password"
                value={next}
                onChange={(e) => setNext(e.target.value)}
                required
              />
            </Field>
            <Field
              id="pw-confirm"
              label="Confirm new password"
              error={mismatch ? "The passwords don't match." : undefined}
            >
              <Input
                id="pw-confirm"
                type="password"
                autoComplete="new-password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                required
              />
            </Field>
            <div className="flex justify-end">
              <Button type="submit" disabled={change.isPending || mismatch}>
                {change.isPending ? "Saving…" : "Change password"}
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
