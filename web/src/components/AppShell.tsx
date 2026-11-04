import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import {
  Activity,
  Ban,
  Bell,
  DatabaseBackup,
  Download,
  Eraser,
  Film,
  FolderOpen,
  History,
  House,
  KeyRound,
  Layers,
  Link2,
  ListChecks,
  ListOrdered,
  ListTodo,
  LogOut,
  MonitorPlay,
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
import { VpnIndicator } from "@/components/VpnIndicator";
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
// A section with no title (Home) has no heading in the sidebar or breadcrumb.
type NavSection = { title: string; items: NavItem[] };

/** Every page, by section. The breadcrumb and Jump to use this list too. */
export const nav: NavSection[] = [
  { title: "", items: [{ to: "/", label: "Home", icon: <House /> }] },
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
      { to: "/settings/media-servers", label: "Media servers", icon: <MonitorPlay /> },
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
  let best: { section: string; page: string; len: number } | undefined;
  for (const s of nav) {
    for (const i of s.items) {
      const under = i.to !== "/" && path.startsWith(`${i.to}/`);
      if ((path === i.to || under) && (!best || i.to.length > best.len)) {
        best = { section: s.title, page: i.label, len: i.to.length };
      }
    }
  }
  return best;
}

// The library lists use the whole window, so the poster grid shows as many
// columns as fit; other pages keep a readable width.
const widePages = new Set(["/movies", "/series"]);
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
      <aside className="sticky top-0 hidden h-svh w-58 shrink-0 overflow-y-auto border-r bg-sidebar md:block">
        <Sidebar name={name} />
      </aside>

      <Dialog open={menuOpen} onOpenChange={setMenuOpen}>
        <DialogContent className="top-0 left-0 h-full max-w-72 translate-x-0 translate-y-0 overflow-y-auto rounded-none border-0 bg-sidebar p-0">
          <DialogTitle className="sr-only">Menu</DialogTitle>
          <Sidebar name={name} />
        </DialogContent>
      </Dialog>

      <div className="relative flex min-w-0 flex-1 flex-col">
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
  const pathname = useRouterState({ select: (st) => st.location.pathname });
  const { data: status } = useQuery({
    queryKey: ["system", "status"],
    queryFn: () => unwrap(api.GET("/system/status")),
    staleTime: 60_000,
  });
  // Settings and System are long; each shows as one row, and its pages
  // fold out only while you are on one of them.
  const folded = (section: NavSection) =>
    folding.has(section.title) && !section.items.some((i) => pathname.startsWith(i.to));
  return (
    <nav className="flex min-h-full flex-col pt-5 pb-4" aria-label="Main">
      <div className="mb-3 px-5">
        <Link to="/" className="flex min-h-11 items-center gap-2.5">
          <Logo className="size-7" />
          <span className="flex min-w-0 flex-col">
            <span className="font-condensed truncate text-[26px] leading-none font-semibold tracking-[0.04em] uppercase">
              {name}
            </span>
            {status?.version && (
              <span className="font-mono text-[11px] text-muted-foreground">{status.version}</span>
            )}
          </span>
        </Link>
      </div>
      {nav.map((section, si) => {
        // Settings and System sit together under one heading, as in the design.
        const heading =
          section.title === "Settings" ? "Setup" : folding.has(section.title) ? "" : section.title;
        return (
          <div key={section.title || si} className="flex flex-col">
            {heading && <p className="label-caps px-5 pt-4 pb-1">{heading}</p>}
            {folded(section) ? (
              <NavRow to={section.items[0]?.to ?? "/"} label={section.title} />
            ) : (
              <>
                {folding.has(section.title) && (
                  <p className="flex min-h-11 items-center gap-2 px-5 text-[15px] text-foreground">
                    <span className="w-3 font-mono text-xs" aria-hidden="true">
                      ▾
                    </span>
                    {section.title}
                  </p>
                )}
                {section.items.map((item) => {
                  const n = item.count ? counts[item.count] : undefined;
                  const full = item.count === "queue" && counts.queueFull;
                  return (
                    <NavRow
                      key={item.to}
                      to={item.to}
                      label={item.label}
                      indent={folding.has(section.title)}
                      count={full ? undefined : n}
                      full={full}
                    />
                  );
                })}
              </>
            )}
          </div>
        );
      })}
      <div className="mt-auto flex flex-col gap-1 px-5 pt-5">
        <VpnIndicator />
        <LiveCard />
      </div>
    </nav>
  );
}

/** The sections that fold up to one row unless you are inside them. */
const folding = new Set(["Settings", "System"]);

/** One menu row: a ▸ marker, the page name and, on the right, a number in
 * mono. The current page is orange on the sheet's own colour. */
function NavRow({
  to,
  label,
  count,
  full,
  indent,
}: {
  to: string;
  label: string;
  count?: number;
  full?: boolean;
  indent?: boolean;
}) {
  return (
    <Link
      to={to}
      // Home ("/") is active only on itself, not under every page.
      activeOptions={{ exact: to === "/" }}
      className={cn(
        "group flex min-h-11 items-center gap-2 pr-5 text-[15px] text-foreground/90 hover:bg-background/60 hover:text-foreground",
        indent ? "pl-10 text-sm" : "pl-5",
      )}
      activeProps={{ className: "bg-background !text-primary", "aria-current": "page" }}
    >
      <span
        className="w-3 font-mono text-xs text-muted-foreground/70 group-aria-[current=page]:text-primary"
        aria-hidden="true"
      >
        ▸
      </span>
      <span className="truncate">{label}</span>
      {((count !== undefined && count > 0) || full) && (
        <span
          className={cn(
            "ml-auto font-mono text-xs",
            full ? "text-warning uppercase" : "text-muted-foreground",
          )}
          title={full ? "The queue is full: no new grabs until downloads finish" : undefined}
        >
          {full ? "Full" : count}
        </span>
      )}
    </Link>
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

/** The sidebar's last line: what's downloading right now, as edge print. */
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
      className="flex min-h-11 flex-col justify-center font-mono text-xs text-muted-foreground hover:text-foreground"
    >
      <span className="text-[13px] text-foreground">{formatSpeed(speed)}</span>
      <span>{text}</span>
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
    <header className="sticky top-0 z-40 flex min-h-14 flex-wrap items-center gap-2.5 border-b bg-background px-4 py-2 md:px-7">
      <Button variant="outline" className="md:hidden" aria-label="Open menu" onClick={onMenu}>
        <span className="font-mono text-primary" aria-hidden="true">
          ▸
        </span>
        Menu
      </Button>
      <Logo className="size-6 md:hidden" />
      {here && (
        <nav
          aria-label="Breadcrumb"
          className="label-caps hidden min-w-0 flex-1 items-center gap-2 sm:flex"
        >
          {here.section && (
            <>
              <span>{here.section}</span>
              <span aria-hidden="true">▸</span>
            </>
          )}
          <span className="truncate text-foreground" aria-current="page">
            {here.page}
          </span>
        </nav>
      )}
      <button
        type="button"
        onClick={openJumpTo}
        className="ml-auto flex min-h-11 min-w-0 flex-1 items-center gap-2.5 border border-input bg-film px-3 text-sm text-muted-foreground hover:text-film-foreground sm:max-w-72 sm:min-w-60 sm:flex-none"
      >
        <Search className="size-4 shrink-0" aria-hidden="true" />
        <span className="truncate">Jump to a page or movie</span>
        <kbd className="ml-auto hidden font-mono text-xs sm:inline">
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
      {dark ? <Sun /> : <Moon />}
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
            className="flex min-h-11 items-center gap-2 border border-input py-1 pr-3 pl-1 text-sm font-medium hover:bg-muted"
          >
            <span className="grid size-8 place-items-center bg-accent font-mono font-medium text-accent-foreground uppercase">
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
