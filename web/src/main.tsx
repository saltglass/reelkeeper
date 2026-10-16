import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RequestError } from "@/api/client";
import { makeRouter } from "@/router";
// Bebas Neue (SIL Open Font License), for the Reelkeeper wordmark only.
import "@fontsource/bebas-neue/latin-400.css";
import "./index.css";
import "./styles/movies.css";
import "./styles/activity.css";
import "./styles/system.css";
import "./styles/settings-library.css";
import "./styles/settings-system.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Don't retry requests the server refused on purpose.
      retry: (count, err) => !(err instanceof RequestError && err.status < 500) && count < 2,
      refetchOnWindowFocus: true,
    },
  },
});

const router = makeRouter(queryClient);

// If the session ends (signed out elsewhere, expired), any request answers
// 401; send the user to the login page.
queryClient.getQueryCache().subscribe((e) => {
  const err = e.query.state.error;
  if (e.type === "updated" && err instanceof RequestError && err.status === 401) {
    if (!router.state.location.pathname.endsWith("/login")) {
      queryClient.invalidateQueries({ queryKey: ["auth"] });
      router.navigate({
        to: "/login",
        search: { redirect: router.state.location.href, error: undefined, detail: undefined },
      });
    }
  }
});

const root = document.getElementById("root");
if (root) {
  createRoot(root).render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </StrictMode>,
  );
}
