import { queryOptions, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import type { components } from "@/api/schema";

export type Indexer = components["schemas"]["Indexer"];
export type IndexerInput = components["schemas"]["IndexerInput"];
export type IndexerKind = components["schemas"]["IndexerKind"];
export type DefinitionSummary = components["schemas"]["DefinitionSummary"];
export type DefinitionDetail = components["schemas"]["DefinitionDetail"];
export type DefinitionSetting = components["schemas"]["DefinitionSetting"];
export type ProwlarrCandidate = components["schemas"]["ProwlarrCandidate"];
export type ProwlarrPreview = components["schemas"]["ProwlarrPreview"];
export type ProwlarrImportResult = components["schemas"]["ProwlarrImportResult"];
export type SolverSettings = components["schemas"]["SolverSettings"];

export const indexersQuery = queryOptions({
  queryKey: ["indexers"],
  queryFn: () => unwrap(api.GET("/indexers")),
});

// Not under ["indexers"], so saving an indexer doesn't refetch the catalogue.
export const definitionsQuery = queryOptions({
  queryKey: ["indexer-definitions"],
  queryFn: () => unwrap(api.GET("/indexers/definitions")),
  staleTime: 10 * 60_000,
});

export const definitionQuery = (id: string) =>
  queryOptions({
    queryKey: ["indexer-definitions", id],
    queryFn: () => unwrap(api.GET("/indexers/definitions/{id}", { params: { path: { id } } })),
    staleTime: 10 * 60_000,
  });

export const solverQuery = queryOptions({
  queryKey: ["settings", "solver"],
  queryFn: () => unwrap(api.GET("/settings/solver")),
});

export function useInvalidateIndexers() {
  const qc = useQueryClient();
  return () => qc.invalidateQueries({ queryKey: ["indexers"] });
}

/**
 * The request body that saves an indexer exactly as it is now. Secrets are
 * left out on purpose: the server keeps the stored ones.
 */
export function inputFromIndexer(ix: Indexer, overrides: Partial<IndexerInput> = {}): IndexerInput {
  return {
    name: ix.name,
    kind: ix.kind,
    definition: ix.definition || undefined,
    baseUrl: ix.baseUrl,
    enabled: ix.enabled,
    priority: ix.priority,
    useSolver: ix.useSolver,
    settings: ix.settings,
    categories: ix.categories,
    seedRatio: ix.seedRatio,
    seedTimeMinutes: ix.seedTimeMinutes,
    packSeedTimeMinutes: ix.packSeedTimeMinutes,
    queryLimit: ix.queryLimit,
    grabLimit: ix.grabLimit,
    ...overrides,
  };
}

export function kindLabel(ix: Pick<Indexer, "kind" | "definitionName" | "definition">): string {
  switch (ix.kind) {
    case "cardigann":
      return ix.definitionName || ix.definition || "Tracker";
    case "torznab":
      return "Torznab feed";
    case "newznab":
      return "Newznab feed";
    case "knaben":
      return "Knaben search engine";
  }
}

const entities: Record<string, string> = {
  "&amp;": "&",
  "&lt;": "<",
  "&gt;": ">",
  "&quot;": '"',
  "&#39;": "'",
  "&nbsp;": " ",
};

/**
 * Definitions carry help text with simple HTML. We never render it as HTML:
 * line breaks stay, links become "text (address)", every other tag goes.
 * The result is shown as plain text.
 */
export function plainInfoText(html: string): string {
  return html
    .replace(/<br\s*\/?>/gi, "\n")
    .replace(
      /<a\s[^>]*href=["']([^"']*)["'][^>]*>(.*?)<\/a>/gis,
      (_, href: string, text: string) =>
        text.trim() && text.trim() !== href ? `${text.trim()} (${href})` : href,
    )
    .replace(/<[^>]*>/g, "")
    .replace(/&(?:amp|lt|gt|quot|#39|nbsp);/g, (m) => entities[m] ?? m)
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}
