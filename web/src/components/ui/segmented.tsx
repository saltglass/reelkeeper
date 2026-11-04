import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

/**
 * A row of exactly-one-of choices; the chosen one is filled, like a chip
 * pressed down. Four or more wrap into two columns on a phone. Each choice
 * is a button with aria-pressed, in a labelled group. A choice may carry a
 * small badge ("Experimental").
 */
export function Segmented<T extends string>({
  label,
  options,
  value,
  onChange,
  className,
}: {
  /** What the group chooses; read out by screen readers. */
  label: string;
  options: { value: T; label: string; badge?: string }[];
  value: T;
  onChange: (value: T) => void;
  className?: string;
}) {
  // More than three choices don't fit one row on a phone: they wrap into
  // two columns there, and sit in one row from the sm breakpoint up.
  const wrap = options.length > 3;
  return (
    // biome-ignore lint/a11y/useSemanticElements: a fieldset's legend and border don't suit a row of buttons
    <div
      role="group"
      aria-label={label}
      className={cn(
        "inline-grid max-w-full self-start border border-input",
        wrap
          ? "grid-cols-2 sm:auto-cols-fr sm:grid-flow-col sm:grid-cols-none"
          : "auto-cols-fr grid-flow-col",
        className,
      )}
    >
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          aria-pressed={o.value === value}
          onClick={() => onChange(o.value)}
          className={cn(
            "inline-flex min-h-11 cursor-pointer flex-wrap items-center justify-center gap-x-2 px-2 py-1 text-sm font-medium transition-colors duration-[calc(160ms*var(--m))] sm:px-4",
            o.value === value
              ? "bg-foreground text-background"
              : "text-foreground/80 hover:text-foreground",
          )}
        >
          {o.label}
          {o.badge && (
            <Badge
              variant="warning"
              className={cn("px-1.5 py-0 text-[11px]", o.value === value && "text-background")}
            >
              {o.badge}
            </Badge>
          )}
        </button>
      ))}
    </div>
  );
}
