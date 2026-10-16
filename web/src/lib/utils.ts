import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

/** Joins class names, letting later Tailwind classes override earlier ones. */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

/** Where Reelkeeper is served, such as "" or "/reelkeeper". Set by the Go server. */
export function urlBase(): string {
  const base = document.querySelector("base")?.getAttribute("href") ?? "/";
  return base.replace(/\/$/, "");
}
