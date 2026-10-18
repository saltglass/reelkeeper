import { ClipboardPaste, FileUp, ShieldCheck, Trash2, Upload } from "lucide-react";
import { type ChangeEvent, type DragEvent, useRef, useState } from "react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  type DownloadClient,
  useCheckVpnConfig,
  useSaveDownloadClient,
  type VpnSummary,
} from "@/hooks/downloads";
import { cn } from "@/lib/utils";

/** The facts of a VPN config, never its keys. */
function Summary({ vpn }: { vpn: VpnSummary }) {
  return (
    <div className="space-y-3">
      <dl className="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-[9rem_1fr]">
        <dt className="text-muted-foreground">Server</dt>
        <dd className="break-all">{vpn.endpoint}</dd>
        <dt className="text-muted-foreground">Tunnel address</dt>
        <dd className="break-all">{vpn.tunnelAddresses.join(", ")}</dd>
        <dt className="text-muted-foreground">DNS</dt>
        <dd className="break-all">
          {vpn.dns.join(", ")}
          {vpn.dnsDefault && (
            <span className="text-muted-foreground">
              {" "}
              (the file names none, so this one is asked, through the VPN)
            </span>
          )}
        </dd>
        <dt className="text-muted-foreground">Your public key</dt>
        <dd className="font-mono text-xs break-all">{vpn.publicKey}</dd>
        <dt className="text-muted-foreground">Keepalive</dt>
        <dd>every {vpn.keepalive === 1 ? "second" : `${vpn.keepalive} seconds`}</dd>
      </dl>
      {vpn.warnings.map((w) => (
        <Alert key={w} variant="warning">
          {w}
        </Alert>
      ))}
      {vpn.notes.length > 0 && (
        <ul className="list-disc space-y-0.5 pl-5 text-xs text-muted-foreground">
          {vpn.notes.map((n) => (
            <li key={n}>{n}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

/**
 * The built-in client's VPN (Settings → Download Client): drop, upload or
 * paste a WireGuard config from the VPN provider, see what it holds (never the
 * keys), replace it, or remove it. A new config is checked on the server as
 * soon as it is given, and only saved with the rest of the form. Removing asks
 * first, because peers will then see the real IP and downloads stop.
 */
export function BuiltinVpnSection({
  saved,
  pending,
  onPending,
  forwardedPort,
  onForwardedPort,
  fieldErrors,
}: {
  /** The saved client; its VPN, if any, is what the summary shows. */
  saved: DownloadClient;
  /** A config given but not saved yet ("" for none). */
  pending: string;
  onPending: (text: string) => void;
  forwardedPort: string;
  onForwardedPort: (value: string) => void;
  fieldErrors: Record<string, string>;
}) {
  const check = useCheckVpnConfig();
  const remove = useSaveDownloadClient();
  const fileInput = useRef<HTMLInputElement>(null);
  const [preview, setPreview] = useState<VpnSummary>();
  const [replacing, setReplacing] = useState(false);
  const [pasting, setPasting] = useState(false);
  const [pasted, setPasted] = useState("");
  const [dragging, setDragging] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [problem, setProblem] = useState<string>();

  const hasSaved = saved.type === "builtin" && saved.hasVpn;
  const hasConfig = hasSaved || pending !== "";
  const showDrop = !hasConfig || replacing;
  const shown = pending !== "" ? preview : (saved.vpn ?? undefined);
  const removeErr = remove.error instanceof RequestError ? remove.error : undefined;

  function take(text: string) {
    setProblem(undefined);
    check.mutate(text, {
      onSuccess: (summary) => {
        setPreview(summary);
        onPending(text);
        setReplacing(false);
        setPasting(false);
        setPasted("");
      },
      onError: (e) => {
        setProblem(e instanceof RequestError ? (e.fields.config ?? e.message) : e.message);
      },
    });
  }

  async function read(file: File | undefined) {
    if (!file) return;
    setProblem(undefined);
    try {
      take(await file.text());
    } catch {
      setProblem("That file couldn't be read.");
    }
  }

  function dropped(e: DragEvent) {
    e.preventDefault();
    setDragging(false);
    void read(e.dataTransfer.files[0]);
  }

  function chosen(e: ChangeEvent<HTMLInputElement>) {
    void read(e.target.files?.[0]);
    e.target.value = "";
  }

  function discard() {
    onPending("");
    setPreview(undefined);
    setReplacing(false);
    setProblem(undefined);
  }

  function removeNow() {
    // Everything but the VPN is left as it is saved.
    remove.mutate(
      {
        type: "builtin",
        url: "",
        username: "",
        folder: saved.folder,
        vpnConfig: "",
        confirmUnprotected: true,
      },
      {
        onSuccess: () => {
          setRemoving(false);
          discard();
        },
      },
    );
  }

  return (
    <section aria-labelledby="dc-vpn-title" className="space-y-4 rounded-lg border p-4">
      <div className="space-y-1">
        <h3 id="dc-vpn-title" className="flex items-center gap-2 text-sm font-semibold">
          <ShieldCheck aria-hidden="true" className="size-4 text-primary" />
          VPN (WireGuard)
        </h3>
        <p className="text-sm text-muted-foreground">
          Optional. With a VPN, everything the built-in client does goes through it and nothing
          else, and if it drops, every download stops until it is back. Get a WireGuard config file
          from your VPN provider.
        </p>
      </div>

      {saved.vpnProblem && pending === "" && <Alert variant="error">{saved.vpnProblem}</Alert>}

      {hasConfig && shown && (
        <div className="space-y-2">
          {pending !== "" && (
            <p className="text-sm font-medium text-warning">
              New config, not saved yet: it takes effect when you save.
            </p>
          )}
          <Summary vpn={shown} />
        </div>
      )}

      {hasConfig && (
        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            size="sm"
            variant="outline"
            aria-expanded={replacing}
            onClick={() => setReplacing(!replacing)}
          >
            Replace
          </Button>
          {pending !== "" && (
            <Button type="button" size="sm" variant="ghost" onClick={discard}>
              Discard new config
            </Button>
          )}
          {hasSaved && (
            <Button type="button" size="sm" variant="outline" onClick={() => setRemoving(true)}>
              <Trash2 aria-hidden="true" />
              Remove
            </Button>
          )}
        </div>
      )}

      {showDrop && (
        <div className="space-y-3">
          {/* biome-ignore lint/a11y/noStaticElementInteractions: a drop target; the buttons below do the same without dragging */}
          <div
            data-testid="vpn-drop"
            onDragOver={(e) => {
              e.preventDefault();
              setDragging(true);
            }}
            onDragLeave={() => setDragging(false)}
            onDrop={dropped}
            className={cn(
              "flex flex-col items-center gap-2 rounded-lg border-2 border-dashed px-4 py-6 text-center text-sm text-muted-foreground transition-colors",
              dragging && "border-primary bg-primary/5 text-foreground",
            )}
          >
            <FileUp aria-hidden="true" className="size-6" />
            <p>Drop a .conf file here</p>
            <div className="flex flex-wrap justify-center gap-2">
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={check.isPending}
                onClick={() => fileInput.current?.click()}
              >
                <Upload aria-hidden="true" />
                Upload file
              </Button>
              <Button
                type="button"
                size="sm"
                variant="outline"
                aria-expanded={pasting}
                onClick={() => setPasting(!pasting)}
              >
                <ClipboardPaste aria-hidden="true" />
                Paste config
              </Button>
            </div>
            <input
              ref={fileInput}
              type="file"
              accept=".conf,.txt,text/plain"
              aria-label="WireGuard config file"
              className="sr-only"
              tabIndex={-1}
              onChange={chosen}
            />
          </div>
          {pasting && (
            <div className="space-y-2">
              <label htmlFor="dc-vpn-paste" className="text-sm font-medium">
                WireGuard config
              </label>
              <textarea
                id="dc-vpn-paste"
                value={pasted}
                onChange={(e) => setPasted(e.target.value)}
                spellCheck={false}
                autoComplete="off"
                rows={8}
                placeholder={"[Interface]\nPrivateKey = …\nAddress = …\n\n[Peer]\n…"}
                className="w-full rounded-md border border-input bg-card p-2 font-mono text-xs"
              />
              <Button
                type="button"
                size="sm"
                disabled={check.isPending || pasted.trim() === ""}
                onClick={() => take(pasted)}
              >
                Use this config
              </Button>
            </div>
          )}
          {(problem ?? fieldErrors.vpnConfig) && (
            <Alert variant="error">{problem ?? fieldErrors.vpnConfig}</Alert>
          )}
          <p className="text-xs text-muted-foreground">
            The file holds your private key. Reelkeeper stores it encrypted and never shows it
            again.
          </p>
        </div>
      )}

      {hasConfig && (
        <Field
          id="dc-forwarded-port"
          label="Forwarded port"
          help="If your VPN provider gives you a forwarded port, enter it so peers can connect to you. Leave empty for outgoing connections only."
          error={fieldErrors.forwardedPort}
        >
          <Input
            id="dc-forwarded-port"
            type="number"
            min="0"
            max="65535"
            className="w-32"
            placeholder="None"
            value={forwardedPort}
            onChange={(e) => onForwardedPort(e.target.value)}
          />
        </Field>
      )}

      <Dialog
        open={removing}
        onOpenChange={(open) => !open && !remove.isPending && setRemoving(false)}
      >
        <DialogContent>
          <DialogTitle>Remove the VPN?</DialogTitle>
          <DialogDescription>
            Peers will see your real IP address. The built-in client's downloads stop until you
            press "Start without VPN" (or add a VPN again). Nothing is deleted.
          </DialogDescription>
          {removeErr && <Alert variant="error">{removeErr.message}</Alert>}
          <div className="flex justify-end gap-2">
            <DialogClose asChild>
              <Button variant="ghost" disabled={remove.isPending}>
                Cancel
              </Button>
            </DialogClose>
            <Button variant="destructive" disabled={remove.isPending} onClick={removeNow}>
              {remove.isPending && <span className="spin" aria-hidden="true" />}
              Remove VPN
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </section>
  );
}
