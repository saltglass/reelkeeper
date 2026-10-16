import type { ReactNode } from "react";
import { tileClass } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/** A button label that shows a spinner while it works and a tick after it last succeeded. */
export function BusyLabel({
  busy,
  ok,
  children,
}: {
  busy: boolean;
  ok: boolean;
  children: ReactNode;
}) {
  return (
    <>
      {busy && <span className="slib-spin" aria-hidden="true" />}
      {!busy && ok && (
        <svg
          className="slib-pop"
          width="14"
          height="14"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="3"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden="true"
        >
          <path d="M20 6 9 17l-5-5" />
        </svg>
      )}
      {children}
    </>
  );
}

/** A rounded tile holding an icon, used at the start of a card header. */
export function IconTile({ children }: { children: ReactNode }) {
  return (
    <span
      aria-hidden="true"
      className={cn(tileClass, "grid size-10 shrink-0 place-items-center [&_svg]:size-5")}
    >
      {children}
    </span>
  );
}
