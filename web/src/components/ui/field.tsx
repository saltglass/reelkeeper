import type * as React from "react";
import { cn } from "@/lib/utils";
import { Label } from "./label";

/**
 * A labelled form field with help text and an error message. Every setting
 * gets help text explaining what it does, so nobody has to guess.
 */
export function Field({
  id,
  label,
  help,
  error,
  className,
  children,
}: {
  id: string;
  label: React.ReactNode;
  help?: React.ReactNode;
  error?: string;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <div className={cn("space-y-1.5", className)}>
      <Label htmlFor={id}>{label}</Label>
      {children}
      {error ? (
        <p id={`${id}-error`} className="text-sm text-destructive">
          {error}
        </p>
      ) : help ? (
        <p id={`${id}-help`} className="text-sm text-muted-foreground">
          {help}
        </p>
      ) : null}
    </div>
  );
}

/** A switch with a label and help text, laid out as a row. */
export function SwitchRow({
  id,
  label,
  help,
  error,
  children,
}: {
  id: string;
  label: React.ReactNode;
  help?: React.ReactNode;
  error?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="space-y-1">
        <Label htmlFor={id}>{label}</Label>
        {help && <p className="text-sm text-muted-foreground">{help}</p>}
        {error && <p className="text-sm text-destructive">{error}</p>}
      </div>
      {children}
    </div>
  );
}
