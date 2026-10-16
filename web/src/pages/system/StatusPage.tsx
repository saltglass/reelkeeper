import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { api, unwrap } from "@/api/client";
import { Alert } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { indexersQuery } from "@/hooks/indexers";
import { PageHeader } from "@/pages/PageHeader";

const heartbeat =
  "M0 24 H90 L102 24 L110 8 L120 40 L128 18 L136 24 H210 L222 24 L230 10 L240 38 L248 20 L256 24 H300";
const line = {
  fill: "none",
  strokeWidth: 2,
  strokeLinecap: "round",
  strokeLinejoin: "round",
} as const;

export function StatusPage() {
  const { data, error } = useQuery({
    queryKey: ["system", "status"],
    queryFn: () => unwrap(api.GET("/system/status")),
    refetchInterval: 10_000,
  });

  // A failing indexer is worth seeing here; if the list can't load, say nothing.
  const indexers = useQuery({ ...indexersQuery, refetchInterval: 30_000 });
  const failing = (indexers.data ?? []).filter((ix) => ix.health.status === "failing");

  if (error) return <Alert variant="error">Couldn't load status: {error.message}</Alert>;
  if (!data) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const memShare =
    data.memory.totalBytes > 0 ? Math.min(1, data.memory.heapBytes / data.memory.totalBytes) : 0;
  const rows: [string, string][] = [
    ["Version", data.version],
    ["Running for", formatDuration(data.uptimeSeconds)],
    ["Started", new Date(data.startedAt).toLocaleString()],
    [
      "Database",
      `${data.database.engine === "postgres" ? "PostgreSQL" : "SQLite"} (schema version ${data.database.schemaVersion})`,
    ],
    ["Memory in use", formatBytes(data.memory.heapBytes)],
    ["Memory from the system", formatBytes(data.memory.totalBytes)],
    ["Data folder", data.dataDir],
    ["URL base", data.urlBase || "(none)"],
    ["Platform", `${data.os}/${data.arch}, ${data.goVersion}`],
  ];

  return (
    <div>
      <PageHeader title="Status" description="What's running and how it's doing." />
      {failing.length > 0 && (
        <Card className="animate-rise mb-4 border-destructive/40">
          <CardHeader>
            <CardTitle>
              {failing.length === 1
                ? "1 indexer is failing"
                : `${failing.length} indexers are failing`}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <ul className="space-y-3 text-sm">
              {failing.map((ix) => (
                <li key={ix.id}>
                  <span className="font-medium">{ix.name}</span>
                  <span className="block text-muted-foreground">{ix.health.message}</span>
                </li>
              ))}
            </ul>
            <Link to="/settings/indexers" className="text-sm text-link underline">
              Open Settings → Indexers
            </Link>
          </CardContent>
        </Card>
      )}
      <div className="mb-4 grid gap-4 md:grid-cols-2">
        <Card className="animate-rise">
          <CardContent className="space-y-0">
            <div className="text-xs text-muted-foreground">Running for</div>
            <div className="mt-1 text-3xl font-bold tracking-tight tabular-nums">
              {formatDuration(data.uptimeSeconds)}
            </div>
            <svg
              viewBox="0 0 300 48"
              width="100%"
              height="48"
              aria-hidden="true"
              className="mt-3 block"
            >
              <path d={heartbeat} {...line} className="stroke-border" />
              <path d={heartbeat} {...line} className="hb stroke-primary" />
            </svg>
            <div className="text-xs text-muted-foreground">
              Started {new Date(data.startedAt).toLocaleString()}
            </div>
          </CardContent>
        </Card>
        <Card className="animate-rise" style={{ animationDelay: "calc(80ms * var(--m))" }}>
          <CardContent className="flex items-center gap-5">
            <svg
              width="104"
              height="104"
              viewBox="0 0 44 44"
              role="img"
              aria-label={`${formatBytes(data.memory.heapBytes)} of ${formatBytes(data.memory.totalBytes)} in use`}
              className="shrink-0"
            >
              <circle cx="22" cy="22" r="16" fill="none" strokeWidth="4" className="stroke-muted" />
              <circle
                className="ring-fill stroke-primary"
                cx="22"
                cy="22"
                r="16"
                fill="none"
                strokeWidth="4"
                strokeLinecap="round"
                strokeDasharray={`${memShare * 100.5} 101`}
                transform="rotate(-90 22 22)"
              />
            </svg>
            <div>
              <div className="text-xs text-muted-foreground">Memory in use</div>
              <div className="text-xl font-semibold">{formatBytes(data.memory.heapBytes)}</div>
              <div className="mt-1.5 text-xs text-muted-foreground">
                of {formatBytes(data.memory.totalBytes)} taken from the system.
              </div>
            </div>
          </CardContent>
        </Card>
      </div>
      <Card className="animate-rise" style={{ animationDelay: "calc(120ms * var(--m))" }}>
        <CardHeader>
          <CardTitle>About</CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="grid grid-cols-1 gap-x-4 gap-y-3 text-sm sm:grid-cols-[minmax(120px,200px)_1fr]">
            {rows.map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="text-muted-foreground">{k}</dt>
                <dd className="font-mono break-all tabular-nums">{v}</dd>
              </div>
            ))}
          </dl>
        </CardContent>
      </Card>
      <p className="mt-3 text-xs text-muted-foreground">
        This product uses the TMDB API but is not endorsed or certified by TMDB. Movie information
        and artwork come from The Movie Database (themoviedb.org).
      </p>
    </div>
  );
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

export function formatDuration(totalSeconds: number): string {
  const d = Math.floor(totalSeconds / 86400);
  const h = Math.floor((totalSeconds % 86400) / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m`;
  return `${totalSeconds}s`;
}
