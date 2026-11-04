import { Link } from "@tanstack/react-router";
import { Fragment } from "react";

/**
 * A page's few numbers as one line of mono capitals ("412 MOVIES · 10 ON
 * DISK · 2 DOWNLOADING"), in place of a row of stat cards. An item with
 * `to` links to the page it counts. A number that isn't known yet shows as
 * a dash.
 */
export function SummaryLine({
  items,
}: {
  items: { label: string; n: number | undefined; to?: string }[];
}) {
  return (
    <dl className="mb-6 flex flex-wrap items-center gap-x-2.5 font-mono text-[12.5px] tracking-[0.04em] text-muted-foreground uppercase">
      {items.map((x, i) => {
        const pair = (
          <>
            {/* The label comes first for screen readers; the number shows first. */}
            <dt className="order-2">{x.label}</dt>
            <dd className="order-1 text-[15px] text-foreground tabular-nums">{x.n ?? "—"}</dd>
          </>
        );
        return (
          <Fragment key={x.label}>
            {i > 0 && (
              <span aria-hidden="true" className="select-none">
                ·
              </span>
            )}
            {x.to ? (
              <div>
                <Link
                  to={x.to}
                  className="flex min-h-11 items-baseline gap-1.5 pt-3 hover:text-foreground"
                >
                  {pair}
                </Link>
              </div>
            ) : (
              <div className="flex min-h-11 items-baseline gap-1.5 pt-3">{pair}</div>
            )}
          </Fragment>
        );
      })}
    </dl>
  );
}
