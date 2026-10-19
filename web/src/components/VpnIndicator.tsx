import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { queueQuery } from "@/hooks/downloads";
import { cn } from "@/lib/utils";

type State = "connecting" | "connected" | "down" | "held";

/** What each VPN state shows: a short label, the dot's colour, and the full story for the tooltip. */
const states: Record<State, { label: string; dot: string; detail: string }> = {
  connected: {
    label: "VPN connected",
    dot: "bg-success",
    detail: "The built-in client's traffic goes through the VPN.",
  },
  connecting: {
    label: "VPN connecting",
    dot: "bg-warning motion-safe:animate-pulse",
    detail: "The built-in client's downloads start when the VPN is connected.",
  },
  down: {
    label: "VPN down",
    dot: "bg-destructive",
    detail: "The built-in client's downloads are stopped until the VPN reconnects.",
  },
  held: {
    label: "Downloads held",
    dot: "bg-warning",
    detail:
      "The VPN was removed or can't be used, so the built-in client's downloads wait until you start them without it (Settings → Download Client).",
  },
};

/**
 * The built-in client's VPN at a glance, under the app's name. It only shows
 * while the built-in client is the download client and has a VPN set up (the
 * Queue reports no state for other clients, and "unprotected" without a VPN).
 * It reads the Queue the sidebar already polls, so it adds no request of its
 * own and never triggers the "what is my IP" check of the settings page.
 */
export function VpnIndicator({ className }: { className?: string }) {
  const { data: queue } = useQuery({ ...queueQuery, refetchInterval: 15_000 });
  const state = queue?.builtinState;
  if (!state || state === "unprotected") return null;
  const s = states[state];
  return (
    <Link
      to="/settings/download-client"
      title={s.detail}
      className={cn(
        "flex w-fit items-center gap-1.5 rounded-sm text-xs text-muted-foreground transition-colors hover:text-foreground",
        className,
      )}
    >
      <span className={cn("size-2 shrink-0 rounded-full", s.dot)} aria-hidden="true" />
      <span>{s.label}</span>
      <span className="sr-only">: {s.detail}</span>
    </Link>
  );
}
