import { useQuery } from "@tanstack/react-query";
import { LoaderCircle, ShieldAlert, ShieldCheck } from "lucide-react";
import { RequestError } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  type BuiltinStatus,
  builtinStatusQuery,
  useCheckAgain,
  useStartWithoutVpn,
} from "@/hooks/downloads";
import { relativeTime } from "@/lib/time";
import { cn } from "@/lib/utils";

/** "23 s ago", "5 min ago", "2 h ago". */
export function ago(iso: string, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000));
  if (s < 60) return `${s} s ago`;
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  return `${Math.round(s / 3600)} h ago`;
}

type Tone = "red" | "amber" | "green";

const tones: Record<Tone, string> = {
  red: "border-destructive/40 bg-destructive/5",
  amber: "border-warning/40 bg-warning/5",
  green: "border-success/40 bg-success/5",
};

const toneText: Record<Tone, string> = {
  red: "text-destructive",
  amber: "text-warning",
  green: "text-success",
};

/** What the status card says about the built-in client's protection, and in what colour. */
export function statusText(st: BuiltinStatus): { tone: Tone; headline: string; detail?: string } {
  const real = st.publicIP?.ip;
  switch (st.state) {
    case "unprotected":
      return {
        tone: "red",
        headline: real
          ? `Unprotected: peers see your real IP (${real})`
          : "Unprotected: peers see your real IP",
        detail: real
          ? undefined
          : (st.publicIPError ?? "Add a VPN below to hide it, with a kill switch."),
      };
    case "connecting":
      return {
        tone: "amber",
        headline: "Connecting…",
        detail: "Downloads are stopped until the VPN is connected.",
      };
    case "connected": {
      const peers = st.vpnIP;
      return {
        tone: "green",
        headline: peers
          ? `VPN connected: peers see ${peers.ip}${peers.country ? ` (${peers.country})` : ""}`
          : "VPN connected",
        detail: peers
          ? undefined
          : st.vpnIPError
            ? `Couldn't check which IP peers see: ${st.vpnIPError}`
            : "Checking which IP peers see…",
      };
    }
    case "down":
      if (st.leak) {
        return {
          tone: "red",
          headline: "Peers would see your real IP: the VPN isn't hiding it",
          detail: "Downloads are stopped until that is fixed.",
        };
      }
      return {
        tone: "amber",
        headline: `VPN down: downloads stopped${st.since ? ` (since ${relativeTime(st.since)})` : ""}`,
        detail: "They start again by themselves when the VPN reconnects.",
      };
    default:
      return { tone: "amber", headline: "Downloads are stopped" };
  }
}

/**
 * The card at the top of the built-in client's settings: is the VPN up, and
 * which address do peers see. Polls every 10 seconds while the tab is visible.
 * `canStartWithoutVpn` is false while a VPN (even an unusable one) is set up.
 */
export function BuiltinStatusCard({ canStartWithoutVpn }: { canStartWithoutVpn: boolean }) {
  const { data, error } = useQuery(builtinStatusQuery);
  const again = useCheckAgain();
  const start = useStartWithoutVpn();
  const err =
    (again.error instanceof RequestError && again.error) ||
    (start.error instanceof RequestError && start.error) ||
    undefined;

  if (error) {
    return <Alert variant="warning">Couldn't check the VPN: {error.message}</Alert>;
  }
  if (!data) return <p className="text-sm text-muted-foreground">Checking the VPN…</p>;

  const { tone, headline, detail } = statusText(data);
  const Icon =
    data.state === "connecting" ? LoaderCircle : tone === "green" ? ShieldCheck : ShieldAlert;
  const withVpn = data.state !== "unprotected" && data.endpoint !== "";
  const held = data.state === "held";
  return (
    <section
      aria-label="VPN status"
      data-state={data.state}
      className={cn("space-y-2 rounded-lg border p-4", tones[tone])}
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-3">
          <Icon
            aria-hidden="true"
            className={cn(
              "mt-0.5 size-5 shrink-0",
              toneText[tone],
              data.state === "connecting" && "animate-spin",
            )}
          />
          <div className="min-w-0 space-y-1">
            <p className={cn("font-medium break-words", toneText[tone])}>{headline}</p>
            {held && data.message && (
              <p className="text-sm break-words text-muted-foreground">{data.message}</p>
            )}
            {!held && detail && <p className="text-sm text-muted-foreground">{detail}</p>}
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          {held && canStartWithoutVpn && (
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={start.isPending}
              onClick={() => start.mutate()}
            >
              Start without VPN
            </Button>
          )}
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={again.isPending}
            onClick={() => again.mutate()}
          >
            {again.isPending && <span className="spin" aria-hidden="true" />}
            Check again
          </Button>
        </div>
      </div>
      {withVpn && (
        <p className="text-xs break-words text-muted-foreground">
          {data.tunnelAddresses.length > 0 && <>Tunnel address {data.tunnelAddresses.join(", ")}</>}
          {data.lastHandshake && <> · Last handshake {ago(data.lastHandshake)}</>}
          {data.endpoint && <> · Endpoint {data.endpoint}</>}
          {data.forwardedPort > 0 && <> · Forwarded port {data.forwardedPort}</>}
        </p>
      )}
      {data.state !== "unprotected" && data.publicIP && (
        <p className="text-xs text-muted-foreground">Your real IP: {data.publicIP.ip}</p>
      )}
      {err && <p className="text-sm text-destructive">{err.message}</p>}
    </section>
  );
}
