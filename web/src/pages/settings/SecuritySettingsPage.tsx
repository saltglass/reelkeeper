import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Eye, EyeOff, RefreshCw } from "lucide-react";
import { type FormEvent, useMemo, useState } from "react";
import {
  api,
  RequestError,
  type SecuritySettings,
  type SecuritySettingsUpdate,
  unwrap,
} from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, SwitchRow } from "@/components/ui/field";
import { Input, Textarea } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { useDraft } from "@/hooks/draft";
import { securitySettingsQuery, useSaveSecurity } from "@/hooks/settings";
import { PageHeader } from "@/pages/PageHeader";
import { BusyLabel } from "./BusyLabel";
import { SaveBar } from "./SaveBar";

/** The form's own shape: lists are edited as one entry per line. */
export type SecurityForm = {
  localLoginEnabled: boolean;
  bypass: string;
  proxies: string;
  sessionDays: string;
  oidc: {
    enabled: boolean;
    issuerUrl: string;
    clientId: string;
    /** A new secret to store; empty keeps the stored one. */
    clientSecret: string;
    clearSecret: boolean;
    scopes: string;
    groupsClaim: string;
    requiredGroup: string;
    buttonLabel: string;
  };
};

export function toForm(s: SecuritySettings): SecurityForm {
  return {
    localLoginEnabled: s.localLoginEnabled,
    bypass: s.bypassNetworks.join("\n"),
    proxies: s.trustedProxies.join("\n"),
    sessionDays: String(s.sessionDays),
    oidc: {
      enabled: s.oidc.enabled,
      issuerUrl: s.oidc.issuerUrl,
      clientId: s.oidc.clientId,
      clientSecret: "",
      clearSecret: false,
      scopes: s.oidc.scopes.join(" "),
      groupsClaim: s.oidc.groupsClaim,
      requiredGroup: s.oidc.requiredGroup,
      buttonLabel: s.oidc.buttonLabel,
    },
  };
}

export const lines = (s: string) =>
  s
    .split(/[\n,]/)
    .map((l) => l.trim())
    .filter(Boolean);

export function toUpdate(f: SecurityForm): SecuritySettingsUpdate {
  let clientSecret: string | undefined;
  if (f.oidc.clearSecret) clientSecret = "";
  else if (f.oidc.clientSecret !== "") clientSecret = f.oidc.clientSecret;
  return {
    localLoginEnabled: f.localLoginEnabled,
    bypassNetworks: lines(f.bypass),
    trustedProxies: lines(f.proxies),
    sessionDays: Number.parseInt(f.sessionDays, 10) || 0,
    oidc: {
      enabled: f.oidc.enabled,
      issuerUrl: f.oidc.issuerUrl.trim(),
      clientId: f.oidc.clientId.trim(),
      clientSecret,
      scopes: f.oidc.scopes.split(/\s+/).filter(Boolean),
      groupsClaim: f.oidc.groupsClaim.trim(),
      requiredGroup: f.oidc.requiredGroup.trim(),
      buttonLabel: f.oidc.buttonLabel.trim(),
    },
  };
}

const sameForm = (a: SecurityForm, b: SecurityForm) => JSON.stringify(a) === JSON.stringify(b);

/** Collects errors for one list field ("bypassNetworks.2" etc.) as text. */
export function listErrors(
  fields: Record<string, string>,
  name: string,
  entries: string[],
): string | undefined {
  const msgs: string[] = [];
  if (fields[name]) msgs.push(fields[name]);
  for (const [key, msg] of Object.entries(fields)) {
    const m = key.match(new RegExp(`^${name}\\.(\\d+)$`));
    if (m) msgs.push(`${entries[Number(m[1])] ?? "Entry"}: ${msg}`);
  }
  return msgs.length ? msgs.join(" ") : undefined;
}

export function SecuritySettingsPage() {
  const { data, error } = useQuery(securitySettingsQuery);
  const save = useSaveSecurity();
  // Memoized: a new object every render would look like new server data.
  const formData = useMemo(() => (data ? toForm(data) : undefined), [data]);
  const { draft: form, setDraft: setForm, dirty, reset, commit } = useDraft(formData, sameForm);
  const [advanced, setAdvanced] = useState(false);

  if (error) return <Alert variant="error">Couldn't load settings: {error.message}</Alert>;
  if (!form || !data) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const err = save.error instanceof RequestError ? save.error : undefined;
  const fe = err?.fields ?? {};
  const set = (patch: Partial<SecurityForm>) => setForm({ ...form, ...patch });
  const setOidc = (patch: Partial<SecurityForm["oidc"]>) =>
    setForm({ ...form, oidc: { ...form.oidc, ...patch } });

  const canDisableLocal =
    form.oidc.enabled &&
    Boolean(data.oidc.lastSuccessAt) &&
    form.oidc.issuerUrl.trim() === data.oidc.issuerUrl &&
    form.oidc.clientId.trim() === data.oidc.clientId;

  function submit(e: FormEvent) {
    e.preventDefault();
    if (!form) return;
    save.mutate(toUpdate(form), { onSuccess: (saved) => commit(toForm(saved)) });
  }

  const unreadable = data.unreadableCredentials ?? [];

  return (
    <form onSubmit={submit} className="space-y-6">
      <PageHeader title="Security" description="Who can use Reelkeeper, and how they sign in." />

      {unreadable.length > 0 && (
        <Alert variant="warning">
          <p className="font-medium">Some stored credentials can't be read</p>
          <p>
            Reelkeeper's secret key has changed since they were saved ({unreadable.join(", ")}).
            Restore the previous <code>REELKEEPER_SECRET_KEY</code>, or enter them again below.
          </p>
        </Alert>
      )}
      {err && Object.keys(fe).length > 0 && (
        <Alert variant="error">Some settings need attention. See the messages below.</Alert>
      )}
      {err && Object.keys(fe).length === 0 && <Alert variant="error">{err.message}</Alert>}

      <Card>
        <CardHeader>
          <CardTitle>Password sign-in</CardTitle>
        </CardHeader>
        <CardContent>
          <SwitchRow
            id="localLogin"
            label="Allow signing in with a username and password"
            help={
              form.localLoginEnabled && !canDisableLocal
                ? "To turn this off, first set up single sign-on below, save, and sign in with it once. That way you can't lock yourself out."
                : "Turn off to require single sign-on. If single sign-on ever breaks, run `reelkeeper reset-auth` on the server."
            }
            error={fe.localLoginEnabled}
          >
            <Switch
              id="localLogin"
              checked={form.localLoginEnabled}
              disabled={form.localLoginEnabled && !canDisableLocal}
              onCheckedChange={(v) => set({ localLoginEnabled: v })}
            />
          </SwitchRow>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Single sign-on (OIDC)</CardTitle>
          <CardDescription>
            Sign in through an identity provider such as Authentik. Create an OAuth2/OpenID provider
            and application there, then copy its details here.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <SwitchRow id="oidcEnabled" label="Use single sign-on">
            <Switch
              id="oidcEnabled"
              checked={form.oidc.enabled}
              onCheckedChange={(v) => setOidc({ enabled: v })}
            />
          </SwitchRow>

          {form.oidc.enabled && (
            <>
              <CopyField
                id="redirectUrl"
                label="Redirect URI"
                value={data.oidc.redirectUrl ?? ""}
                help="Register this exact address with your identity provider. It's based on the address you're using now."
              />
              <IssuerField
                value={form.oidc.issuerUrl}
                onChange={(v) => setOidc({ issuerUrl: v })}
                error={fe["oidc.issuerUrl"]}
              />
              <Field id="clientId" label="Client ID" error={fe["oidc.clientId"]}>
                <Input
                  id="clientId"
                  value={form.oidc.clientId}
                  onChange={(e) => setOidc({ clientId: e.target.value })}
                  autoComplete="off"
                />
              </Field>
              <Field
                id="clientSecret"
                label="Client secret"
                help={
                  data.oidc.clientSecretSet && !form.oidc.clearSecret
                    ? "A secret is stored. Leave this empty to keep it."
                    : "Required for confidential clients (Authentik's default)."
                }
              >
                <Input
                  id="clientSecret"
                  type="password"
                  value={form.oidc.clientSecret}
                  placeholder={
                    data.oidc.clientSecretSet && !form.oidc.clearSecret ? "••••••••" : ""
                  }
                  onChange={(e) => setOidc({ clientSecret: e.target.value, clearSecret: false })}
                  autoComplete="new-password"
                />
                {data.oidc.clientSecretSet && !form.oidc.clearSecret && (
                  <Button
                    type="button"
                    variant="link"
                    size="sm"
                    className="h-auto px-0"
                    onClick={() => setOidc({ clientSecret: "", clearSecret: true })}
                  >
                    Remove stored secret
                  </Button>
                )}
              </Field>
              <Field
                id="requiredGroup"
                label="Required group"
                help="Only members of this group can sign in. Leave empty to allow anyone the provider lets through."
                error={fe["oidc.requiredGroup"]}
              >
                <Input
                  id="requiredGroup"
                  value={form.oidc.requiredGroup}
                  onChange={(e) => setOidc({ requiredGroup: e.target.value })}
                />
              </Field>
              <p className="text-sm text-muted-foreground">
                {data.oidc.lastSuccessAt
                  ? `Last successful single sign-on: ${new Date(data.oidc.lastSuccessAt).toLocaleString()}.`
                  : "Single sign-on hasn't been used yet. Save, then sign out and try it."}
              </p>
            </>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Access without signing in</CardTitle>
        </CardHeader>
        <CardContent>
          <Field
            id="bypass"
            label="Trusted networks"
            help="Devices on these addresses use Reelkeeper without signing in. One IP address or range per line, for example 192.168.1.0/24. Leave empty to always require sign-in."
            error={listErrors(fe, "bypassNetworks", lines(form.bypass))}
          >
            <Textarea
              id="bypass"
              value={form.bypass}
              rows={3}
              spellCheck={false}
              onChange={(e) => set({ bypass: e.target.value })}
            />
          </Field>
          <Field
            id="proxies"
            label="Reverse proxies"
            help="If Reelkeeper is behind a reverse proxy (such as Traefik), list the proxy's address. Reelkeeper then reads the real visitor address from the proxy. Never list the proxy as a trusted network: everything coming through it would skip sign-in."
            error={listErrors(fe, "trustedProxies", lines(form.proxies))}
          >
            <Textarea
              id="proxies"
              value={form.proxies}
              rows={2}
              spellCheck={false}
              onChange={(e) => set({ proxies: e.target.value })}
            />
          </Field>
        </CardContent>
      </Card>

      <ApiKeyCard apiKey={data.apiKey ?? ""} />

      <div>
        <Button
          type="button"
          variant="link"
          className="px-0"
          onClick={() => setAdvanced(!advanced)}
        >
          {advanced ? "Hide advanced" : "Show advanced"}
        </Button>
      </div>
      {advanced && (
        <Card>
          <CardHeader>
            <CardTitle>Advanced</CardTitle>
          </CardHeader>
          <CardContent>
            <Field
              id="sessionDays"
              label="Stay signed in for (days)"
              help="A sign-in ends after this many days without using Reelkeeper."
              error={fe.sessionDays}
            >
              <Input
                id="sessionDays"
                type="number"
                min={1}
                max={365}
                value={form.sessionDays}
                onChange={(e) => set({ sessionDays: e.target.value })}
                className="w-32"
              />
            </Field>
            <Field
              id="scopes"
              label="Single sign-on scopes"
              help="Requested from the identity provider, separated by spaces. openid is always included."
            >
              <Input
                id="scopes"
                value={form.oidc.scopes}
                onChange={(e) => setOidc({ scopes: e.target.value })}
              />
            </Field>
            <Field
              id="groupsClaim"
              label="Groups claim"
              help="The token field that lists the user's groups. Authentik uses “groups”."
            >
              <Input
                id="groupsClaim"
                value={form.oidc.groupsClaim}
                onChange={(e) => setOidc({ groupsClaim: e.target.value })}
              />
            </Field>
            <Field id="buttonLabel" label="Sign-in button text">
              <Input
                id="buttonLabel"
                value={form.oidc.buttonLabel}
                onChange={(e) => setOidc({ buttonLabel: e.target.value })}
              />
            </Field>
          </CardContent>
        </Card>
      )}

      <SaveBar dirty={dirty} saving={save.isPending} saved={save.isSuccess} onDiscard={reset} />
    </form>
  );
}

function IssuerField({
  value,
  onChange,
  error,
}: {
  value: string;
  onChange: (v: string) => void;
  error?: string;
}) {
  const check = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/settings/security/oidc/check", { body: { issuerUrl: value.trim() } })),
  });
  return (
    <Field
      id="issuerUrl"
      label="Issuer URL"
      help="For Authentik: https://auth.example.com/application/o/<application slug>/ (with the trailing slash)."
      error={error}
    >
      <div className="flex gap-2">
        <Input
          id="issuerUrl"
          value={value}
          onChange={(e) => {
            onChange(e.target.value);
            check.reset();
          }}
          placeholder="https://"
          spellCheck={false}
        />
        <Button
          type="button"
          variant="outline"
          disabled={!value.trim() || check.isPending}
          onClick={() => check.mutate()}
        >
          <BusyLabel busy={check.isPending} ok={Boolean(check.data?.ok)}>
            {check.isPending ? "Checking…" : "Check"}
          </BusyLabel>
        </Button>
      </div>
      {check.data && (
        <Alert variant={check.data.ok ? "success" : "error"}>{check.data.message}</Alert>
      )}
    </Field>
  );
}

function CopyField({
  id,
  label,
  value,
  help,
}: {
  id: string;
  label: string;
  value: string;
  help?: string;
}) {
  return (
    <Field id={id} label={label} help={help}>
      <div className="flex gap-2">
        <Input
          id={id}
          value={value}
          readOnly
          className="font-mono"
          onFocus={(e) => e.target.select()}
        />
        <CopyButton value={value} />
      </div>
    </Field>
  );
}

function CopyButton({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  // The clipboard API only works over HTTPS or on localhost; elsewhere the
  // field can still be selected and copied by hand.
  if (!navigator.clipboard) return null;
  return (
    <Button
      type="button"
      variant="tile"
      size="icon"
      aria-label="Copy"
      onClick={async () => {
        await navigator.clipboard.writeText(value);
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      }}
    >
      {copied ? <Check className="popin text-success" /> : <Copy />}
    </Button>
  );
}

function ApiKeyCard({ apiKey }: { apiKey: string }) {
  const [shown, setShown] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const qc = useQueryClient();
  const regenerate = useMutation({
    mutationFn: () => unwrap(api.POST("/settings/security/api-key")),
    onSuccess: () => {
      setConfirmOpen(false);
      setShown(true);
      qc.invalidateQueries({ queryKey: securitySettingsQuery.queryKey });
    },
  });

  return (
    <Card>
      <CardHeader>
        <CardTitle>API key</CardTitle>
        <CardDescription>
          Seerr, the Dashboard and scripts use this key to talk to Reelkeeper. Treat it like a
          password.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {apiKey === "" ? (
          <Alert variant="warning">The API key can't be read. Generate a new one.</Alert>
        ) : (
          <div className="flex gap-2">
            <Input
              aria-label="API key"
              value={shown ? apiKey : "•".repeat(32)}
              readOnly
              className="font-mono"
              onFocus={(e) => shown && e.target.select()}
            />
            <Button
              type="button"
              variant="tile"
              size="icon"
              aria-label={shown ? "Hide API key" : "Show API key"}
              onClick={() => setShown(!shown)}
            >
              {shown ? <EyeOff /> : <Eye />}
            </Button>
            <CopyButton value={apiKey} />
          </div>
        )}
        <Button type="button" variant="outline" onClick={() => setConfirmOpen(true)}>
          <RefreshCw />
          Generate new key
        </Button>
        <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
          <DialogContent>
            <DialogTitle>Generate a new API key?</DialogTitle>
            <DialogDescription>
              The current key stops working immediately. Seerr, the Dashboard and anything else
              using it will need the new key.
            </DialogDescription>
            {regenerate.error && <Alert variant="error">{regenerate.error.message}</Alert>}
            <div className="flex justify-end gap-2">
              <DialogClose asChild>
                <Button type="button" variant="ghost">
                  Cancel
                </Button>
              </DialogClose>
              <Button
                type="button"
                variant="destructive"
                disabled={regenerate.isPending}
                onClick={() => regenerate.mutate()}
              >
                Generate new key
              </Button>
            </div>
          </DialogContent>
        </Dialog>
      </CardContent>
    </Card>
  );
}
