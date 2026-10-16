import { Check, TriangleAlert } from "lucide-react";
import { useToasts } from "@/hooks/toast";
import { cn } from "@/lib/utils";

/** Shows the pop-up notices (hooks/toast.ts). The live region is always there so screen readers hear new ones. */
export function Toaster() {
  const toasts = useToasts();
  return (
    <div
      role="status"
      aria-live="polite"
      className="pointer-events-none fixed right-4 bottom-24 left-4 z-[60] flex flex-col items-end gap-2 sm:right-6 sm:bottom-6 sm:left-auto"
    >
      {toasts.map((t) => (
        <div
          key={t.id}
          className={cn(
            "pointer-events-auto flex max-w-[380px] items-center gap-2.5 rounded-xl border bg-card py-3 pr-4 pl-3 text-sm shadow-md",
            t.leaving ? "animate-toast-out" : "animate-toast-in",
          )}
        >
          <span
            className={cn(
              "grid size-7 flex-none place-items-center rounded-full",
              t.kind === "problem" ? "bg-warning/20 text-warning" : "bg-success/20 text-success",
            )}
          >
            {t.kind === "problem" ? (
              <TriangleAlert className="animate-pop-in size-[15px]" aria-hidden="true" />
            ) : (
              <Check className="animate-pop-in size-[15px]" strokeWidth={3} aria-hidden="true" />
            )}
          </span>
          <span className="min-w-0 break-words">{t.text}</span>
        </div>
      ))}
    </div>
  );
}
