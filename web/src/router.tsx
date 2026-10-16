import type { QueryClient } from "@tanstack/react-query";
import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  Outlet,
  type RouterHistory,
  redirect,
} from "@tanstack/react-router";
import { AppShell } from "@/components/AppShell";
import { authStateQuery } from "@/hooks/auth";
import { urlBase } from "@/lib/utils";
import { CleanupPage } from "@/pages/activity/CleanupPage";
import { HistoryPage } from "@/pages/activity/HistoryPage";
import { QueuePage } from "@/pages/activity/QueuePage";
import { LoginPage } from "@/pages/LoginPage";
import { ReviewPage } from "@/pages/library/ReviewPage";
import { MovieDetailPage } from "@/pages/movies/MovieDetailPage";
import { MoviesPage } from "@/pages/movies/MoviesPage";
import { NotFoundPage } from "@/pages/NotFoundPage";
import { SeriesDetailPage } from "@/pages/series/SeriesDetailPage";
import { SeriesPage } from "@/pages/series/SeriesPage";
import { CleanupSettingsPage } from "@/pages/settings/CleanupSettingsPage";
import { DownloadClientPage } from "@/pages/settings/DownloadClientPage";
import { GeneralSettingsPage } from "@/pages/settings/GeneralSettingsPage";
import { ImportRadarrPage } from "@/pages/settings/ImportRadarrPage";
import { ImportSonarrPage } from "@/pages/settings/ImportSonarrPage";
import { IndexersPage } from "@/pages/settings/IndexersPage";
import { MediaFoldersPage } from "@/pages/settings/MediaFoldersPage";
import { NotificationsPage } from "@/pages/settings/NotificationsPage";
import { ProfileSettingsPage } from "@/pages/settings/ProfileSettingsPage";
import { QualityProfilesPage } from "@/pages/settings/QualityProfilesPage";
import { SecuritySettingsPage } from "@/pages/settings/SecuritySettingsPage";
import { BackupsPage } from "@/pages/system/BackupsPage";
import { BlocklistPage } from "@/pages/system/BlocklistPage";
import { StatusPage } from "@/pages/system/StatusPage";
import { TasksPage } from "@/pages/system/TasksPage";
import { WantedPage } from "@/pages/wanted/WantedPage";

type Context = { queryClient: QueryClient };

const rootRoute = createRootRouteWithContext<Context>()({
  component: Outlet,
  notFoundComponent: NotFoundPage,
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  validateSearch: (s: Record<string, unknown>) => ({
    redirect: typeof s.redirect === "string" ? s.redirect : undefined,
    error: typeof s.error === "string" ? s.error : undefined,
    detail: typeof s.detail === "string" ? s.detail : undefined,
  }),
  component: LoginPage,
});

// Everything inside the app shell requires being signed in (or bypassed).
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "app",
  beforeLoad: async ({ context, location }) => {
    const state = await context.queryClient.ensureQueryData(authStateQuery);
    if (!state.authenticated) {
      throw redirect({
        to: "/login",
        search: { redirect: location.href, error: undefined, detail: undefined },
      });
    }
  },
  component: AppShell,
});

// The app opens on Movies.
const indexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/movies" });
  },
});

const moviesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/movies",
  // After deleting a movie the detail page sends you here with a note to show.
  validateSearch: (s: {
    deleted?: string;
    kept?: boolean;
  }): { deleted?: string; kept?: boolean } => {
    const out: { deleted?: string; kept?: boolean } = {};
    if (typeof s.deleted === "string") out.deleted = s.deleted;
    if (s.kept === true || (s.kept as unknown) === "true") out.kept = true;
    return out;
  },
  component: MoviesPage,
});

// Folders Reelkeeper couldn't identify, for both movies and series.
const reviewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/review",
  component: ReviewPage,
});

// The review screen used to live under Movies; keep old links working.
const oldReviewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/movies/review",
  beforeLoad: () => {
    throw redirect({ to: "/review" });
  },
});

const seriesListRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/series",
  component: SeriesPage,
});

const wantedRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/wanted",
  component: WantedPage,
});

const seriesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/series/$id",
  component: SeriesDetailPage,
});

const movieRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/movies/$id",
  component: MovieDetailPage,
});

const mediaRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/settings/media",
  component: MediaFoldersPage,
});

const importRadarrRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/settings/import-radarr",
  component: ImportRadarrPage,
});

const importSonarrRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/settings/import-sonarr",
  component: ImportSonarrPage,
});

const profilesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/settings/profiles",
  component: QualityProfilesPage,
});

const profileSettingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/settings/profiles/$id",
  component: ProfileSettingsPage,
});

const indexersRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/settings/indexers",
  component: IndexersPage,
});

const downloadClientRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/settings/download-client",
  component: DownloadClientPage,
});

const cleanupSettingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/settings/cleanup",
  component: CleanupSettingsPage,
});

const notificationsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/settings/notifications",
  component: NotificationsPage,
});

const cleanupActivityRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/activity/cleanup",
  component: CleanupPage,
});

const historyRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/activity/history",
  component: HistoryPage,
});

const queueRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/activity/queue",
  component: QueuePage,
});

// The blocklist moved from Activity to System; keep old links working.
const oldBlocklistRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/activity/blocklist",
  beforeLoad: () => {
    throw redirect({ to: "/system/blocklist" });
  },
});

const generalRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/settings/general",
  component: GeneralSettingsPage,
});

const securityRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/settings/security",
  component: SecuritySettingsPage,
});

const statusRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/system/status",
  component: StatusPage,
});

const tasksRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/system/tasks",
  component: TasksPage,
});

const backupsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/system/backups",
  component: BackupsPage,
});

const blocklistRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/system/blocklist",
  component: BlocklistPage,
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  appRoute.addChildren([
    indexRoute,
    moviesRoute,
    reviewRoute,
    oldReviewRoute,
    seriesListRoute,
    seriesRoute,
    wantedRoute,
    movieRoute,
    mediaRoute,
    importRadarrRoute,
    importSonarrRoute,
    profilesRoute,
    profileSettingsRoute,
    indexersRoute,
    downloadClientRoute,
    queueRoute,
    historyRoute,
    oldBlocklistRoute,
    cleanupActivityRoute,
    cleanupSettingsRoute,
    notificationsRoute,
    generalRoute,
    securityRoute,
    statusRoute,
    tasksRoute,
    backupsRoute,
    blocklistRoute,
  ]),
]);

export function makeRouter(queryClient: QueryClient, history?: RouterHistory) {
  return createRouter({
    routeTree,
    history,
    context: { queryClient },
    basepath: urlBase() || "/",
    defaultPreload: "intent",
    scrollRestoration: true,
  });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof makeRouter>;
  }
}
