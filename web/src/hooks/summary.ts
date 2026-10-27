import { queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import type { components } from "@/api/schema";

export type Summary = components["schemas"]["DashboardSummary"];

/**
 * The snapshot Home is built on: queue counts, failing indexers, grabs today
 * and what's coming up. Cheap: the server reads only its own records.
 */
export const summaryQuery = queryOptions({
  queryKey: ["summary"],
  queryFn: () => unwrap(api.GET("/summary")),
  refetchInterval: 30_000,
});
