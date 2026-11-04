import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

/**
 * A row of exactly-one-of choices; the chosen one is filled, like a chip
 * pressed down. Each choice is a button with aria-pressed, in a labelled group. A choice
 * may carry a small badge ("Experimental").
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
  const index = Math.max(
    0,
    options.findIndex((o) => o.value === value),
  );
  return (
    // biome-ignore lint/a11y/useSemanticElements: a fieldset can't hold the sliding indicator layout
    <div
      role="group"
      aria-label={label}
      className={cn(
        "relative inline-grid max-w-full auto-cols-fr grid-flow-col self-start border border-input",
        className,
      )}
    >
      <span
        aria-hidden="true"
        className="absolute top-0 bottom-0 left-0 bg-foreground transition-transform duration-[calc(160ms*var(--m))]"
        style={{
          width: `calc(100% / ${options.length})`,
          transform: `translateX(${index * 100}%)`,
        }}
      />
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          aria-pressed={o.value === value}
          onClick={() => onChange(o.value)}
          className={cn(
            "relative z-[1] min-h-11 cursor-pointer px-2 text-sm font-medium transition-colors duration-[calc(160ms*var(--m))] sm:px-4",
            o.value === value ? "text-background" : "text-foreground/80 hover:text-foreground",
          )}
        >
          {o.label}
          {o.badge && (
            <Badge variant="warning" className="ml-2 hidden px-1.5 py-0 text-[11px] sm:inline-flex">
              {o.badge}
            </Badge>
          )}
        </button>
      ))}
    </div>
  );
}
