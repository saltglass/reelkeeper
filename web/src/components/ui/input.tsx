import type * as React from "react";
import { cn } from "@/lib/utils";

/**
 * The look shared by text boxes, text areas and drop-down lists: a 40px-tall
 * box whose border turns to the focus colour.
 */
export const controlClass =
  "min-h-11 w-full border border-input bg-background px-3 text-[15px] placeholder:text-muted-foreground focus:border-ring disabled:opacity-50 aria-invalid:border-destructive";

export function Input({ className, ...props }: React.ComponentProps<"input">) {
  return <input className={cn(controlClass, className)} {...props} />;
}

export function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return <textarea className={cn(controlClass, "min-h-20 py-2 font-mono", className)} {...props} />;
}
