import { X } from "lucide-react";
import { type KeyboardEvent, type ReactNode, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

/** Compares two JSON-like values, ignoring the order of object keys. */
export function sameValue(a: unknown, b: unknown): boolean {
  return stable(a) === stable(b);
}

function stable(v: unknown): string {
  return JSON.stringify(v, (_k, x) =>
    x && typeof x === "object" && !Array.isArray(x)
      ? Object.fromEntries(Object.entries(x).sort(([p], [q]) => p.localeCompare(q)))
      : x,
  );
}

export const selectClass =
  "w-full min-h-10 rounded-lg border border-input bg-background px-3 text-sm transition-[border-color,box-shadow] duration-[calc(200ms*var(--m))] focus:border-ring focus:ring-4 focus:ring-ring/20 focus-visible:outline-none";

/**
 * A row of mutually exclusive choices with an indicator that slides to the
 * chosen one. Each choice is a real radio button (named "<label>: <choice>").
 */
export function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: T;
  options: { value: T; label: string }[];
  onChange: (v: T) => void;
}) {
  const index = Math.max(
    0,
    options.findIndex((o) => o.value === value),
  );
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className="relative inline-grid max-w-full auto-cols-fr grid-flow-col self-start rounded-[10px] border bg-muted p-1 md:shrink-0"
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
        <label key={o.value} className="relative z-[1] cursor-pointer">
          <input
            type="radio"
            className="peer sr-only"
            name={label}
            aria-label={`${label}: ${o.label}`}
            checked={value === o.value}
            onChange={() => onChange(o.value)}
          />
          <span
            className={cn(
              "flex min-h-8 items-center justify-center rounded-[7px] px-2.5 text-xs font-medium whitespace-nowrap transition-colors duration-[calc(200ms*var(--m))] peer-focus-visible:ring-2 peer-focus-visible:ring-ring sm:px-3",
              value === o.value ? "text-foreground" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {o.label}
          </span>
        </label>
      ))}
    </div>
  );
}

/** A list of short texts (release group names): type one and press Enter. */
export function TagInput({
  id,
  value,
  onChange,
  placeholder,
}: {
  id: string;
  value: string[];
  onChange: (v: string[]) => void;
  placeholder?: string;
}) {
  const [text, setText] = useState("");
  function add() {
    const t = text.trim().replace(/,$/, "").trim();
    if (t && !value.some((v) => v.toLowerCase() === t.toLowerCase())) onChange([...value, t]);
    setText("");
  }
  function key(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      add();
    } else if (e.key === "Backspace" && text === "" && value.length > 0) {
      onChange(value.slice(0, -1));
    }
  }
  return (
    <div className="space-y-2">
      {value.length > 0 && (
        <ul className="flex flex-wrap gap-1.5">
          {value.map((v) => (
            <li
              key={v}
              className="inline-flex items-center gap-1 rounded-full border bg-muted px-2 py-0.5 text-xs"
            >
              {v}
              <button
                type="button"
                aria-label={`Remove ${v}`}
                className="cursor-pointer text-muted-foreground hover:text-foreground"
                onClick={() => onChange(value.filter((x) => x !== v))}
              >
                <X className="size-3" />
              </button>
            </li>
          ))}
        </ul>
      )}
      <Input
        id={id}
        value={text}
        placeholder={placeholder}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={key}
        onBlur={add}
      />
    </div>
  );
}

/**
 * A number box that keeps what you type (like "1." on the way to "1.5") and
 * reports the number. Empty means 0, which the profile reads as "no limit".
 */
export function NumberBox({
  id,
  label,
  value,
  onChange,
  placeholder,
  step,
  allowNegative,
}: {
  id: string;
  label: string;
  value: number;
  onChange: (n: number) => void;
  placeholder?: string;
  step?: string;
  allowNegative?: boolean;
}) {
  const shown = value === 0 ? "" : String(value);
  const [text, setText] = useState(shown);
  // Follow outside changes (discard, a different profile) but not our own.
  useEffect(() => {
    setText((t) => (parse(t) === value ? t : shown));
  }, [value, shown]);
  return (
    <Input
      id={id}
      aria-label={label}
      inputMode="decimal"
      type="number"
      min={allowNegative ? undefined : 0}
      step={step ?? "any"}
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        setText(e.target.value);
        onChange(parse(e.target.value));
      }}
    />
  );
}

function parse(t: string) {
  const n = Number(t);
  return t.trim() === "" || Number.isNaN(n) ? 0 : n;
}

/** A yes/no question in a dialog. */
export function ConfirmDialog({
  title,
  confirmLabel,
  busy,
  error,
  onConfirm,
  onClose,
  children,
}: {
  title: string;
  confirmLabel: string;
  busy?: boolean;
  error?: string;
  onConfirm: () => void;
  onClose: () => void;
  children: ReactNode;
}) {
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogTitle>{title}</DialogTitle>
        <DialogDescription>{children}</DialogDescription>
        {error && <p className="text-sm text-destructive">{error}</p>}
        <div className="flex justify-end gap-2">
          <DialogClose asChild>
            <Button variant="ghost">Cancel</Button>
          </DialogClose>
          <Button disabled={busy} onClick={onConfirm}>
            {busy ? "Working…" : confirmLabel}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
