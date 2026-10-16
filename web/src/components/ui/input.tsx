import type * as React from "react";
import { cn } from "@/lib/utils";

/**
 * The look shared by text boxes, text areas and drop-down lists: a 40px-tall
 * box that lights up with a soft halo in the focus colour.
 */
export const controlClass =
  "min-h-10 w-full rounded-lg border border-input bg-background px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] placeholder:text-muted-foreground focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none disabled:opacity-50 aria-invalid:border-destructive";

export function Input({ className, ...props }: React.ComponentProps<"input">) {
  return <input className={cn(controlClass, className)} {...props} />;
}

export function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return <textarea className={cn(controlClass, "min-h-20 py-2 font-mono", className)} {...props} />;
}
