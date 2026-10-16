import { useSyncExternalStore } from "react";

/**
 * Pop-up notices: short messages that slide in at the bottom right and leave
 * by themselves. "ok" notices stay about 3 seconds; "problem" notices stay
 * about 6, so there's time to read them. At most 3 show at once.
 * Call toast() from anywhere; <Toaster/> (in AppShell) shows them.
 */
export type ToastKind = "ok" | "problem";
export type ToastItem = { id: number; text: string; kind: ToastKind; leaving: boolean };

const maxShown = 3;
const stay: Record<ToastKind, number> = { ok: 3000, problem: 6000 };
const leaveMs = 300;

let items: ToastItem[] = [];
let nextId = 1;
const listeners = new Set<() => void>();
const timers = new Set<ReturnType<typeof setTimeout>>();

function emit(next: ToastItem[]) {
  items = next;
  for (const l of listeners) l();
}

function later(fn: () => void, ms: number) {
  const t = setTimeout(() => {
    timers.delete(t);
    fn();
  }, ms);
  timers.add(t);
}

function remove(id: number) {
  emit(items.filter((i) => i.id !== id));
}

function leave(id: number) {
  if (!items.some((i) => i.id === id)) return;
  emit(items.map((i) => (i.id === id ? { ...i, leaving: true } : i)));
  later(() => remove(id), leaveMs);
}

/** Shows a notice. */
export function toast(text: string, kind: ToastKind = "ok") {
  const id = nextId++;
  const shown = items.filter((i) => !i.leaving);
  // Over the limit: the oldest goes at once.
  const dropped = shown.slice(0, Math.max(0, shown.length + 1 - maxShown)).map((i) => i.id);
  emit([...items.filter((i) => !dropped.includes(i.id)), { id, text, kind, leaving: false }]);
  later(() => leave(id), stay[kind]);
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

export function useToasts(): ToastItem[] {
  return useSyncExternalStore(
    subscribe,
    () => items,
    () => items,
  );
}

/** For tests: forget every notice and timer. */
export function resetToastsForTests() {
  for (const t of timers) clearTimeout(t);
  timers.clear();
  items = [];
  for (const l of listeners) l();
}
