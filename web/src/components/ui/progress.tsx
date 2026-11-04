import { cn } from "@/lib/utils";

/** A strip of frames that light up as it fills. value is 0-100. */
export function Progress({
  value,
  className,
  label,
}: {
  value: number;
  className?: string;
  label: string;
}) {
  return (
    <div
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={value}
      className={cn("h-2.5 w-full overflow-hidden bg-muted", className)}
    >
      <div
        className="h-full bg-downloading [mask:repeating-linear-gradient(90deg,#000_0_10px,transparent_10px_12px)]"
        style={{ width: `${Math.min(100, Math.max(0, value))}%` }}
      />
    </div>
  );
}
