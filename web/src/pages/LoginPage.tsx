import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { type FormEvent, useEffect, useState } from "react";
import { api, RequestError, unwrap } from "@/api/client";
import { Logo } from "@/components/Logo";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { authStateQuery, useAuthState } from "@/hooks/auth";
import { useSetupRestore } from "@/hooks/backups";
import { urlBase } from "@/lib/utils";
import { RestartOverlay } from "./system/RestoreFlow";

/** Messages for the ?error= codes the single sign-on callback sends back. */
export const ssoErrors: Record<string, string> = {
  sso_disabled: "Single sign-on isn't set up.",
  sso_expired: "The sign-in took too long or was started in another browser. Please try again.",
  sso_denied:
    "Your account isn't allowed to use Reelkeeper. Ask the administrator to add you to the required group.",
  sso_failed:
    "Single sign-on didn't work. Check the identity provider settings; the details are in Reelkeeper's log.",
  sso_link_conflict:
    "That single sign-on account is already linked to a different Reelkeeper account, so it wasn't linked to this one.",
};

/** Only follow redirects that stay inside Reelkeeper. */
function safeRedirect(r: string | undefined): string {
  return r?.startsWith("/") && !r.startsWith("//") && !r.endsWith("/login") ? r : "/";
}

export function LoginPage() {
  const { redirect, error: ssoError, detail } = useSearch({ from: "/login" });
  const { data: state, isLoading, error: stateError } = useAuthState();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  // First run only: starting from a backup instead of a new account.
  const [restoring, setRestoring] = useState(false);
  const [backupFile, setBackupFile] = useState<File>();
  const [restarting, setRestarting] = useState(false);
  const [notice, setNotice] = useState<string>();

  const setupMode = state?.setupRequired ?? false;
  const target = safeRedirect(redirect);

  // Already signed in (or on a trusted network): go straight in, unless
  // there's an error to show (a failed "link single sign-on" lands here
  // while still signed in).
  const signedIn = Boolean(state?.authenticated && !state.setupRequired);
  useEffect(() => {
    if (signedIn && !ssoError) navigate({ href: target, replace: true });
  }, [signedIn, ssoError, navigate, target]);

  const submit = useMutation({
    mutationFn: async () => {
      const body = { username, password };
      await unwrap(
        setupMode ? api.POST("/auth/setup", { body }) : api.POST("/auth/login", { body }),
      );
    },
    onSuccess: async () => {
      await qc.fetchQuery({ ...authStateQuery, staleTime: 0 });
      navigate({ href: target, replace: true });
    },
  });

  const restore = useSetupRestore();
  function startRestore() {
    if (!backupFile) return;
    restore.mutate(backupFile, {
      onSuccess: () => setRestarting(true),
      onError: async (e) => {
        // Someone created an account in the meantime: this is a sign-in now.
        if (e instanceof RequestError && e.status === 409) {
          setNotice("An account already exists. Sign in instead.");
          setRestoring(false);
          await qc.fetchQuery({ ...authStateQuery, staleTime: 0 });
        }
      },
    });
  }
  const restoreErr =
    restore.error instanceof RequestError && restore.error.status !== 409
      ? restore.error.status === 413
        ? "That file is too big. Backups can be up to 1 GiB."
        : restore.error.message
      : restore.error && !(restore.error instanceof RequestError)
        ? "The backup couldn't be sent. Check your connection and try again."
        : undefined;

  const err = submit.error instanceof RequestError ? submit.error : undefined;
  const mismatch = setupMode && confirm !== "" && confirm !== password;

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (mismatch) return;
    submit.mutate();
  }

  const ssoHref = `${urlBase()}/api/v1/auth/oidc/login?returnTo=${encodeURIComponent(target)}`;

  return (
    <div className="relative flex min-h-full items-center justify-center overflow-hidden p-4">
      <div className="page-glow" aria-hidden="true">
        <span />
      </div>
      <div className="animate-rise relative z-[1] w-full max-w-sm space-y-6">
        <div className="flex flex-col items-center gap-3 text-center">
          <Logo className="size-14 drop-shadow-[0_6px_20px_color-mix(in_srgb,var(--brand-from)_55%,transparent)]" />
          <h1 className="text-2xl font-semibold">
            {setupMode ? (
              <>
                Welcome to{" "}
                <span className="font-wordmark text-brand-gradient text-[34px] tracking-wide">
                  Reelkeeper
                </span>
              </>
            ) : (
              <>
                Sign in to{" "}
                <span className="font-wordmark text-brand-gradient text-[34px] tracking-wide">
                  Reelkeeper
                </span>
              </>
            )}
          </h1>
          {setupMode && !restoring && (
            <p className="text-sm text-muted-foreground">
              Create the administrator account. You'll use it to sign in from now on.
            </p>
          )}
          {setupMode && restoring && (
            <p className="text-sm text-muted-foreground">Restore from a backup</p>
          )}
        </div>

        <Card className="shadow-lg">
          <CardContent>
            {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
            {stateError && (
              <Alert variant="error">
                Can't reach Reelkeeper. Check that it's running, then reload.
              </Alert>
            )}
            {ssoError && (
              <Alert variant="error">
                <p>{ssoErrors[ssoError] ?? ssoErrors.sso_failed}</p>
                {detail && <p className="text-xs opacity-80">{detail}</p>}
              </Alert>
            )}
            {notice && <Alert variant="warning">{notice}</Alert>}
            {signedIn && ssoError && (
              <Button className="w-full" onClick={() => navigate({ href: target, replace: true })}>
                Continue to Reelkeeper
              </Button>
            )}

            {state &&
              !signedIn &&
              (setupMode || state.localLoginEnabled) &&
              !(setupMode && restoring) && (
                <form onSubmit={onSubmit} className="space-y-4">
                  {err && !err.fields.password && !err.fields.username && (
                    <Alert variant="error">{err.message}</Alert>
                  )}
                  <Field id="username" label="Username" error={err?.fields.username}>
                    <Input
                      id="username"
                      autoComplete="username"
                      value={username}
                      onChange={(e) => setUsername(e.target.value)}
                      required
                      autoFocus
                    />
                  </Field>
                  <Field
                    id="password"
                    label="Password"
                    help={setupMode ? "At least 8 characters." : undefined}
                    error={err?.fields.password}
                  >
                    <Input
                      id="password"
                      type="password"
                      autoComplete={setupMode ? "new-password" : "current-password"}
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                      required
                    />
                  </Field>
                  {setupMode && (
                    <Field
                      id="confirm"
                      label="Confirm password"
                      error={mismatch ? "The passwords don't match." : undefined}
                    >
                      <Input
                        id="confirm"
                        type="password"
                        autoComplete="new-password"
                        value={confirm}
                        onChange={(e) => setConfirm(e.target.value)}
                        required
                      />
                    </Field>
                  )}
                  <Button type="submit" className="w-full" disabled={submit.isPending || mismatch}>
                    {submit.isPending ? "Please wait…" : setupMode ? "Create account" : "Sign in"}
                  </Button>
                </form>
              )}

            {state && !signedIn && setupMode && !restoring && (
              <p className="mt-4 text-center text-sm text-muted-foreground">
                Moving from another install?{" "}
                <button
                  type="button"
                  className="font-medium text-foreground underline underline-offset-4 hover:text-primary"
                  onClick={() => {
                    setNotice(undefined);
                    setRestoring(true);
                  }}
                >
                  Restore from a backup
                </button>{" "}
                instead.
              </p>
            )}

            {state && !signedIn && setupMode && restoring && (
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  startRestore();
                }}
                className="space-y-4"
              >
                <p className="text-sm text-muted-foreground">
                  Start this install from a backup made by Reelkeeper (System → Backups → Download).
                  Reelkeeper must run with the same REELKEEPER_SECRET_KEY as the install that made
                  it. Its accounts come with it: sign in with them afterwards. This is also how you
                  move between SQLite and PostgreSQL.
                </p>
                {restoreErr && <Alert variant="error">{restoreErr}</Alert>}
                <Field id="backup-file" label="Backup file">
                  <Input
                    id="backup-file"
                    type="file"
                    accept=".backup"
                    className="py-1.5 file:mr-3 file:rounded-md file:border-0 file:bg-muted file:px-3 file:py-1 file:text-sm file:font-medium file:text-foreground"
                    disabled={restore.isPending}
                    onChange={(e) => {
                      restore.reset();
                      setBackupFile(e.target.files?.[0]);
                    }}
                  />
                </Field>
                <Button
                  type="submit"
                  className="w-full"
                  disabled={!backupFile || restore.isPending}
                >
                  {restore.isPending && <span className="spin" aria-hidden="true" />}
                  {restore.isPending ? "Checking the backup…" : "Restore and restart"}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  className="w-full"
                  disabled={restore.isPending}
                  onClick={() => {
                    restore.reset();
                    setBackupFile(undefined);
                    setRestoring(false);
                  }}
                >
                  Create an account instead
                </Button>
              </form>
            )}

            {state && !signedIn && !setupMode && state.oidcEnabled && (
              <>
                {state.localLoginEnabled && (
                  <div className="flex items-center gap-3 text-xs text-muted-foreground">
                    <span className="h-px flex-1 bg-border" />
                    or
                    <span className="h-px flex-1 bg-border" />
                  </div>
                )}
                <Button variant="outline" className="w-full" asChild>
                  <a href={ssoHref}>{state.oidcButtonLabel}</a>
                </Button>
              </>
            )}

            {state && !setupMode && !state.localLoginEnabled && !state.oidcEnabled && (
              <Alert variant="warning">
                No sign-in method is turned on. Run <code>reelkeeper reset-auth</code> on the server
                to get back in.
              </Alert>
            )}
          </CardContent>
        </Card>
      </div>
      {restarting && <RestartOverlay destination="/" />}
    </div>
  );
}
