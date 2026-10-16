import { queryOptions, useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";

/** Who is signed in, and which sign-in methods are available. */
export const authStateQuery = queryOptions({
  queryKey: ["auth", "state"],
  queryFn: () => unwrap(api.GET("/auth/state")),
  staleTime: 30_000,
});

export function useAuthState() {
  return useQuery(authStateQuery);
}
