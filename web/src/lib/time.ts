/** "just now", "3 minutes ago", "in 2 hours", "yesterday"... */
export function relativeTime(iso: string, now = Date.now()): string {
  const diff = new Date(iso).getTime() - now;
  const abs = Math.abs(diff);
  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ["day", 86_400_000],
    ["hour", 3_600_000],
    ["minute", 60_000],
  ];
  if (abs < 45_000) return diff < 0 ? "just now" : "in a moment";
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  for (const [unit, ms] of units) {
    if (abs >= ms || unit === "minute") return rtf.format(Math.round(diff / ms), unit);
  }
  return rtf.format(Math.round(diff / 60_000), "minute");
}

/** A duration in seconds as "42s", "3m 05s" or "2h 10m". */
export function shortDuration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, "0")}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

/** How often a task runs, from its interval in seconds. */
export function everyLabel(seconds: number): string {
  if (seconds <= 0) return "When needed";
  const h = seconds / 3600;
  if (h === 24) return "Every day";
  if (h === 168) return "Every week";
  if (h >= 24 && h % 24 === 0) return `Every ${h / 24} days`;
  if (h === 1) return "Every hour";
  if (h >= 1 && Number.isInteger(h)) return `Every ${h} hours`;
  return `Every ${Math.round(seconds / 60)} minutes`;
}
