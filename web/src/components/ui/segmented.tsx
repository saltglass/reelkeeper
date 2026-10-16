import { cn } from "@/lib/utils";

/**
 * A row of exactly-one-of choices with an indicator that slides to the chosen
 * one. Each choice is a button with aria-pressed, in a labelled group.
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
  options: { value: T; label: string }[];
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
        "relative inline-grid max-w-full auto-cols-fr grid-flow-col self-start rounded-[10px] border bg-muted p-1",
        className,
      )}
    >
      <span
        aria-hidden="true"
        className="absolute top-1 bottom-1 left-1 rounded-[7px] bg-card shadow-xs ring-1 ring-border transition-transform duration-[calc(420ms*var(--m))] [transition-timing-function:cubic-bezier(.34,1.35,.64,1)]"
        style={{
          width: `calc((100% - 8px) / ${options.length})`,
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
            "relative z-[1] min-h-9 cursor-pointer rounded-[7px] px-4 text-sm font-medium transition-colors duration-[calc(200ms*var(--m))]",
            o.value === value ? "text-foreground" : "text-muted-foreground hover:text-foreground",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
