import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import {
  Activity,
  Ban,
  Bell,
  ChevronRight,
  DatabaseBackup,
  Download,
  Eraser,
  Film,
  FolderOpen,
  History,
  KeyRound,
  Layers,
  Link2,
  ListChecks,
  ListOrdered,
  ListTodo,
  LogOut,
  Menu,
  Moon,
  Palette,
  Radar,
  Search,
  Settings2,
  ShieldCheck,
  Sprout,
  Sun,
  Tv,
} from "lucide-react";
import { type ReactNode, useEffect, useState } from "react";
import { api, unwrap } from "@/api/client";
import { ChangePasswordDialog } from "@/components/ChangePasswordDialog";
import { JumpTo } from "@/components/JumpTo";
import { Logo } from "@/components/Logo";
import { Toaster } from "@/components/Toaster";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useAuthState } from "@/hooks/auth";
import { queueQuery } from "@/hooks/downloads";
import { useLiveEvents } from "@/hooks/events";
import { moviesQuery, seriesListQuery } from "@/hooks/library";
import { generalSettingsQuery } from "@/hooks/settings";
import { themes, useAppearance } from "@/hooks/theme";
import { missingTotal, wantedCountsQuery } from "@/hooks/wanted";
import { cn, urlBase } from "@/lib/utils";

type NavItem = {
  to: string;
  label: string;
  icon: ReactNode;
  count?: "movies" | "series" | "wanted" | "queue";
};
type NavSection = { title: string; items: NavItem[] };

/** Every page, by section. The breadcrumb and Jump to use this list too. */
export const nav: NavSection[] = [
  {
    title: "Library",
    items: [
      { to: "/movies", label: "Movies", icon: <Film />, count: "movies" },
      { to: "/series", label: "Series", icon: <Tv />, count: "series" },
      { to: "/wanted", label: "Wanted", icon: <ListTodo />, count: "wanted" },
    ],
  },
  {
    title: "Activity",
    items: [
      { to: "/activity/queue", label: "Queue", icon: <ListOrdered />, count: "queue" },
      { to: "/activity/history", label: "History", icon: <History /> },
      { to: "/activity/cleanup", label: "Seeding & cleanup", icon: <Sprout /> },
    ],
  },
  {
    title: "Settings",
    items: [
      { to: "/settings/media", label: "Media folders", icon: <FolderOpen /> },
      { to: "/settings/profiles", label: "Quality profiles", icon: <Layers /> },
      { to: "/settings/indexers", label: "Indexers", icon: <Radar /> },
      { to: "/settings/download-client", label: "Download client", icon: <Download /> },
      { to: "/settings/cleanup", label: "Cleanup", icon: <Eraser /> },
      { to: "/settings/notifications", label: "Notifications", icon: <Bell /> },
      { to: "/settings/general", label: "General", icon: <Settings2 /> },
      { to: "/settings/security", label: "Security", icon: <ShieldCheck /> },
    ],
  },
  {
    title: "System",
    items: [
      { to: "/system/status", label: "Status", icon: <Activity /> },
      { to: "/system/tasks", label: "Tasks", icon: <ListChecks /> },
      { to: "/system/backups", label: "Backups", icon: <DatabaseBackup /> },
      { to: "/system/blocklist", label: "Blocklist", icon: <Ban /> },
    ],
  },
];

/** The section and page a path belongs to, for the breadcrumb. */
export function whereAmI(pathname: string): { section: string; page: string } | undefined {
  const path = pathname.replace(/\/+$/, "") || "/";
  if (path === "/") return { section: "Library", page: "Movies" };
  let best: { section: string; page: string; len: number } | undefined;
  for (const s of nav) {
    for (const i of s.items) {
      if ((path === i.to || path.startsWith(`${i.to}/`)) && (!best || i.to.length > best.len)) {
        best = { section: s.title, page: i.label, len: i.to.length };
      }
    }
  }
  return best;
}

// The library lists use the whole window, so the poster grid shows as many
// columns as fit; other pages keep a readable width.
const widePages = new Set(["/", "/movies", "/series"]);
function wide(pathname: string) {
  return widePages.has(pathname.replace(/\/+$/, "") || "/");
}

/** Asks the Jump to box to open (it listens for this event). */
export function openJumpTo() {
  window.dispatchEvent(new Event("reelkeeper:jump-to"));
}

export function AppShell() {
  const { data: auth } = useAuthState();
  const { data: general } = useQuery(generalSettingsQuery);
  const [menuOpen, setMenuOpen] = useState(false);
  const pathname = useRouterState({ select: (s) => s.location.pathname });

  useLiveEvents(Boolean(auth?.authenticated));

  const name = general?.instanceName ?? "Reelkeeper";
  useEffect(() => {
    document.title = name;
  }, [name]);

  // Close the phone menu after navigating.
  // biome-ignore lint/correctness/useExhaustiveDependencies: runs on route change by design
  useEffect(() => setMenuOpen(false), [pathname]);

  return (
    <div className="flex min-h-full">
      {/* The menu stays put while the page scrolls. */}
      <aside className="sticky top-0 hidden h-svh w-64 shrink-0 overflow-y-auto border-r bg-card md:block">
        <Sidebar name={name} />
      </aside>

      <Dialog open={menuOpen} onOpenChange={setMenuOpen}>
        <DialogContent className="top-0 left-0 h-full max-w-72 translate-x-0 translate-y-0 overflow-y-auto rounded-none p-0">
          <DialogTitle className="sr-only">Menu</DialogTitle>
          <Sidebar name={name} />
        </DialogContent>
      </Dialog>

      <div className="relative flex min-w-0 flex-1 flex-col">
        <div className="page-glow" aria-hidden="true">
          <span />
        </div>
        <TopBar pathname={pathname} onMenu={() => setMenuOpen(true)} />
        <main
          // A new key per page replays the entrance animation.
          key={pathname}
          className={cn(
            "animate-page-in relative z-[1] mx-auto w-full flex-1",
            wide(pathname) ? "px-3 py-4 md:px-6 md:py-6" : "max-w-4xl p-4 md:p-8",
          )}
        >
          <Outlet />
        </main>
      </div>
      <JumpTo />
      <Toaster />
    </div>
  );
}

function Sidebar({ name }: { name: string }) {
  const counts = useNavCounts();
  const { data: status } = useQuery({
    queryKey: ["system", "status"],
    queryFn: () => unwrap(api.GET("/system/status")),
    staleTime: 60_000,
  });
  return (
    <nav className="flex min-h-full flex-col gap-0.5 px-3 pt-5 pb-5" aria-label="Main">
      <Link to="/" className="group mb-2 flex items-center gap-2.5 rounded-md px-2.5 pb-3">
        <Logo className="size-8 transition-transform duration-[calc(500ms*var(--m))] [transition-timing-function:cubic-bezier(.34,1.56,.64,1)] group-hover:scale-110 group-hover:-rotate-6" />
        <span className="flex min-w-0 flex-col">
          <span className="font-wordmark truncate text-[28px] leading-none tracking-wide">
            {name}
          </span>
          {status?.version && (
            <span className="text-xs text-muted-foreground tabular-nums">{status.version}</span>
          )}
        </span>
      </Link>
      {nav.map((section) => (
        <div key={section.title} className="flex flex-col gap-0.5">
          <p className="px-3 pt-4 pb-1.5 text-xs font-medium tracking-[0.06em] text-muted-foreground uppercase">
            {section.title}
          </p>
          {section.items.map((item) => {
            const n = item.count ? counts[item.count] : undefined;
            const full = item.count === "queue" && counts.queueFull;
            return (
              <Link
                key={item.to}
                to={item.to}
                className="group relative flex min-h-10 items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors duration-[calc(180ms*var(--m))] hover:bg-muted active:scale-[0.98] [&_svg]:size-[18px] [&_svg]:shrink-0 [&_svg]:text-muted-foreground [&_svg]:transition-transform [&_svg]:duration-[calc(320ms*var(--m))] hover:[&_svg]:scale-110 hover:[&_svg]:-rotate-6 hover:[&_svg]:text-foreground"
                // The active item gets the accent fill and a bar on its left
                // edge that grows in.
                activeProps={{
                  className:
                    "bg-accent text-accent-foreground [&_svg]:!text-accent-foreground before:absolute before:top-2 before:bottom-2 before:left-0 before:w-[3px] before:rounded-sm before:bg-primary before:animate-[bar-in_calc(380ms*var(--m))_cubic-bezier(.34,1.56,.64,1)_both]",
                }}
              >
                {item.icon}
                <span className="truncate">{item.label}</span>
                {((n !== undefined && n > 0) || full) && (
                  <span
                    className={cn(
                      "ml-auto rounded-full px-2 text-xs leading-5 font-medium",
                      full
                        ? "border border-warning/30 bg-warning/10 text-warning"
                        : item.count === "queue"
                          ? "bg-primary/25 text-accent-foreground"
                          : "bg-muted text-muted-foreground",
                    )}
                    title={
                      full ? "The queue is full: no new grabs until downloads finish" : undefined
                    }
                  >
                    {full ? "Full" : n}
                  </span>
                )}
              </Link>
            );
          })}
        </div>
      ))}
      <LiveCard />
    </nav>
  );
}

/** Numbers beside Movies, Series (shows, not episodes), Wanted (missing
 * movies and episodes) and Queue; missing while unknown. */
function useNavCounts() {
  const { data: movies } = useQuery({ ...moviesQuery, staleTime: 60_000 });
  const { data: series } = useQuery({ ...seriesListQuery, staleTime: 60_000 });
  const { data: wanted } = useQuery({ ...wantedCountsQuery, staleTime: 30_000 });
  const { data: queue } = useQuery({ ...queueQuery, refetchInterval: 15_000 });
  return {
    movies: movies?.length,
    series: series?.length,
    wanted: wanted && missingTotal(wanted.counts),
    queue: queue?.items.filter((i) => i.status !== "import-failed").length,
    // The queue is at its limit, so nothing new is grabbed (QueuePage says so).
    queueFull: queue?.full ?? false,
  };
}

/** The sidebar's live card: what's downloading right now. */
function LiveCard() {
  const { data: queue } = useQuery({ ...queueQuery, refetchInterval: 15_000 });
  if (!queue) return null;
  const items = queue.items;
  const speed = items.reduce((sum, i) => sum + (i.torrent?.speed ?? 0), 0);
  const downloading = items.filter((i) => i.torrent && i.torrent.progress < 1).length;
  const attention = items.filter((i) => i.status === "import-failed").length;
  let text = downloading === 0 ? "Nothing downloading" : `${downloading} downloading`;
  if (attention > 0) text += ` · ${attention} need${attention === 1 ? "s" : ""} attention`;
  return (
    <Link
      to="/activity/queue"
      className="mx-1 mt-auto flex flex-col gap-2 rounded-lg border bg-background p-3.5 transition-colors hover:border-input"
    >
      <span className="flex items-center gap-2">
        <span className={cn("eq", speed === 0 && "idle")} aria-hidden="true">
          <i />
          <i />
          <i />
          <i />
        </span>
        <b className="tabular-nums">{formatSpeed(speed)}</b>
      </span>
      <span className="text-xs text-muted-foreground">{text}</span>
    </Link>
  );
}

function formatSpeed(bytesPerSecond: number): string {
  if (bytesPerSecond < 1024) return "0 KB/s";
  if (bytesPerSecond < 1024 * 1024) return `${Math.round(bytesPerSecond / 1024)} KB/s`;
  return `${(bytesPerSecond / 1024 / 1024).toFixed(1)} MB/s`;
}

function TopBar({ pathname, onMenu }: { pathname: string; onMenu: () => void }) {
  const here = whereAmI(pathname);
  return (
    <header className="sticky top-0 z-40 flex min-h-14 flex-wrap items-center gap-2.5 border-b bg-card/80 px-4 py-2.5 backdrop-blur-md md:px-8">
      <Button
        variant="tile"
        size="icon"
        className="md:hidden"
        aria-label="Open menu"
        onClick={onMenu}
      >
        <Menu />
      </Button>
      <Logo className="size-6 md:hidden" />
      {here && (
        <div className="hidden min-w-0 flex-1 items-center gap-2 text-sm text-muted-foreground sm:flex">
          <span>{here.section}</span>
          <ChevronRight className="size-3.5" aria-hidden="true" />
          <b key={here.page} className="animate-crumb-in truncate font-semibold text-foreground">
            {here.page}
          </b>
        </div>
      )}
      <button
        type="button"
        onClick={openJumpTo}
        className="ml-auto flex min-h-10 min-w-0 flex-1 items-center gap-2.5 rounded-md border bg-background px-3 text-sm text-muted-foreground transition-[border-color,transform] duration-[calc(180ms*var(--m))] hover:-translate-y-px hover:border-input sm:max-w-72 sm:flex-none sm:min-w-60"
      >
        <Search className="size-4 shrink-0" aria-hidden="true" />
        <span className="truncate">Jump to a page or movie</span>
        <kbd className="ml-auto hidden rounded border px-1.5 text-xs sm:inline">
          {isMac() ? "⌘K" : "Ctrl K"}
        </kbd>
      </button>
      <ThemeFlip />
      <AccountMenu />
    </header>
  );
}

const isMac = () => typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);

/** Switches between the light and dark version of the look. */
function ThemeFlip() {
  const { resolved, update } = useAppearance();
  const dark = themes.find((t) => t.id === resolved)?.dark ?? true;
  return (
    <Button
      variant="tile"
      size="icon"
      aria-label={dark ? "Switch to the light theme" : "Switch to the dark theme"}
      onClick={() => update({ theme: dark ? "light" : "dark" })}
    >
      <span key={String(dark)} className="animate-pop-in">
        {dark ? <Sun /> : <Moon />}
      </span>
    </Button>
  );
}

function AccountMenu() {
  const { data: auth } = useAuthState();
  const [pwOpen, setPwOpen] = useState(false);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });

  const user = auth?.user;
  const who =
    user?.username ??
    (auth?.via === "bypass" ? "Trusted network" : auth?.via === "apiKey" ? "API key" : "");

  async function signOut() {
    await api.POST("/auth/logout");
    qc.clear();
    navigate({
      to: "/login",
      search: { redirect: undefined, error: undefined, detail: undefined },
    });
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label="Account menu"
            className="flex min-h-10 items-center gap-2 rounded-full border py-1 pr-3 pl-1 text-sm font-medium hover:bg-muted"
          >
            <span className="grid size-8 place-items-center rounded-full bg-accent font-semibold text-accent-foreground uppercase">
              {who.slice(0, 1) || "?"}
            </span>
            <span className="hidden sm:inline">{who}</span>
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {auth?.via === "bypass" && (
            <DropdownMenuLabel>Signed in automatically from a trusted network</DropdownMenuLabel>
          )}
          <DropdownMenuItem onSelect={() => navigate({ to: "/settings/general" })}>
            <Palette />
            Appearance
          </DropdownMenuItem>
          {user && (
            <>
              <DropdownMenuSeparator />
              {user.hasPassword && (
                <DropdownMenuItem onSelect={() => setPwOpen(true)}>
                  <KeyRound />
                  Change password
                </DropdownMenuItem>
              )}
              {auth?.oidcEnabled && user.hasPassword && !user.ssoLinked && (
                <DropdownMenuItem
                  onSelect={() => {
                    // A full page navigation: the server redirects to the
                    // identity provider and back.
                    window.location.href = `${urlBase()}/api/v1/auth/oidc/link?returnTo=${encodeURIComponent(pathname)}`;
                  }}
                >
                  <Link2 />
                  Link single sign-on
                </DropdownMenuItem>
              )}
              {user.ssoLinked && <DropdownMenuLabel>Single sign-on linked</DropdownMenuLabel>}
              <DropdownMenuItem onSelect={signOut}>
                <LogOut />
                Sign out
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      <ChangePasswordDialog open={pwOpen} onOpenChange={setPwOpen} />
    </>
  );
}
