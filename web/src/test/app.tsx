import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { render } from "@testing-library/react";
import { vi } from "vitest";
import { makeRouter } from "@/router";

type Handler = (req: Request) => Response | Promise<Response> | undefined;

export function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * Renders the whole app at `path` against a fake server. `routes` maps
 * "METHOD /path" (below /api/v1) to a response or a handler.
 */
export function renderApp(path: string, routes: Record<string, Response | Handler>) {
  const calls: Request[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    calls.push(req);
    const url = new URL(req.url);
    const key = `${req.method} ${url.pathname.replace(/^\/api\/v1/, "")}`;
    const route = routes[key];
    if (!route) return json({ code: "not_found", message: `no fake for ${key}` }, 404);
    const res = typeof route === "function" ? await route(req) : route.clone();
    return res ?? json({ code: "not_found", message: "unhandled" }, 404);
  });
  vi.stubGlobal(
    "matchMedia",
    (q: string) =>
      ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }) as unknown,
  );
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = makeRouter(queryClient, createMemoryHistory({ initialEntries: [path] }));
  const view = render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { ...view, router, calls };
}

export const signedOut = {
  setupRequired: false,
  localLoginEnabled: true,
  oidcEnabled: false,
  oidcButtonLabel: "Sign in with SSO",
  authenticated: false,
};

export const signedIn = {
  ...signedOut,
  authenticated: true,
  via: "session",
  user: { id: 1, username: "dan", hasPassword: true, ssoLinked: false },
};
