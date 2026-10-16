import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import type { components } from "@/api/schema";

export type MovieSummary = components["schemas"]["MovieSummary"];
export type MovieDetail = components["schemas"]["MovieDetail"];
export type VersionSummary = components["schemas"]["VersionSummary"];
export type MediaFile = components["schemas"]["MediaFile"];
export type RootFolder = components["schemas"]["RootFolder"];
export type Profile = components["schemas"]["Profile"];
export type ReviewItem = components["schemas"]["ReviewItem"];
export type LookupResult = components["schemas"]["LookupResult"];
export type SeriesSummary = components["schemas"]["SeriesSummary"];
export type SeriesDetail = components["schemas"]["SeriesDetail"];
export type SeasonDetail = components["schemas"]["SeasonDetail"];
export type EpisodeDetail = components["schemas"]["EpisodeDetail"];
export type EpisodeOrder = components["schemas"]["EpisodeOrder"];
export type SeriesLookupResult = components["schemas"]["SeriesLookupResult"];

export const moviesQuery = queryOptions({
  queryKey: ["movies"],
  queryFn: () => unwrap(api.GET("/movies")),
});

export const movieQuery = (id: number) =>
  queryOptions({
    queryKey: ["movies", id],
    queryFn: () => unwrap(api.GET("/movies/{id}", { params: { path: { id } } })),
  });

export const seriesListQuery = queryOptions({
  queryKey: ["series"],
  queryFn: () => unwrap(api.GET("/series")),
});

export const seriesQuery = (id: number) =>
  queryOptions({
    queryKey: ["series", id],
    queryFn: () => unwrap(api.GET("/series/{id}", { params: { path: { id } } })),
  });

export const rootsQuery = queryOptions({
  queryKey: ["library", "roots"],
  queryFn: () => unwrap(api.GET("/library/roots")),
});

export const profilesQuery = queryOptions({
  queryKey: ["library", "profiles"],
  queryFn: () => unwrap(api.GET("/profiles")),
  staleTime: 5 * 60_000,
});

export const reviewQuery = queryOptions({
  queryKey: ["library", "review"],
  queryFn: () => unwrap(api.GET("/library/review")),
});

/** Refreshes everything library-related (after a change or a live event). */
export function useInvalidateLibrary() {
  const qc = useQueryClient();
  return () => {
    qc.invalidateQueries({ queryKey: ["movies"] });
    qc.invalidateQueries({ queryKey: ["series"] });
    qc.invalidateQueries({ queryKey: ["library"] });
  };
}

export function useScanRoot() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) =>
      unwrap(api.POST("/library/roots/{id}/scan", { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["system", "tasks"] }),
  });
}
