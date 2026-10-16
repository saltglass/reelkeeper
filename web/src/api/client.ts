import createClient from "openapi-fetch";
import { urlBase } from "@/lib/utils";
import type { components, paths } from "./schema";

/**
 * Typed API client. Types come from api/openapi.yaml via `just gen`, so a
 * mismatch between the UI and the server is a compile error.
 */
export const api = createClient<paths>({
  baseUrl: `${window.location.origin}${urlBase()}/api/v1`,
  credentials: "same-origin",
  // Looked up on each call (not captured once) so tests can replace fetch.
  fetch: (req) => globalThis.fetch(req),
});

export type ApiError = components["schemas"]["Error"];
export type AuthState = components["schemas"]["AuthState"];
export type GeneralSettings = components["schemas"]["GeneralSettings"];
export type LabelPart = components["schemas"]["LabelPart"];
export type LabelSettings = components["schemas"]["LabelSettings"];
export type SecuritySettings = components["schemas"]["SecuritySettings"];
export type SecuritySettingsUpdate = components["schemas"]["SecuritySettingsUpdate"];
export type SystemStatus = components["schemas"]["SystemStatus"];

/** Thrown by unwrap() so TanStack Query sees failures. */
export class RequestError extends Error {
  constructor(
    readonly status: number,
    readonly body: ApiError | undefined,
  ) {
    super(body?.message ?? `Request failed (${status})`);
  }
  /** Problems with individual fields, keyed by field path. */
  get fields(): Record<string, string> {
    return this.body?.fields ?? {};
  }
}

/** Returns the data of an openapi-fetch result, or throws RequestError. */
export async function unwrap<T>(
  p: Promise<{ data?: T; error?: unknown; response: Response }>,
): Promise<T> {
  const { data, error, response } = await p;
  if (!response.ok) {
    throw new RequestError(response.status, error as ApiError | undefined);
  }
  return data as T;
}
