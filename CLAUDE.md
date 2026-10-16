# Reelkeeper

One self-hosted web app that replaces **Radarr, Sonarr and Prowlarr**. It finds,
downloads, imports and organizes movies and TV, manages indexers itself, and
cleans up after itself in the download client. To Seerr and other tools it looks
exactly like Radarr and Sonarr.

- Owner: Dan. GPL-3.0 (`LICENSE`). Public at github.com/saltglass/reelkeeper.
- Dan is not a developer. Claude writes nearly all of the code, so the code,
  tests and docs must explain themselves.
- Named **Aerie** until 2026-10-10. The encryption label `"aerie secrets v1"`
  deliberately keeps the old name: changing it would make stored credentials
  and older backups unreadable (pinned by tests).
- Dan's own instance (deployment, hosts, releases, history of decisions) is
  described in private notes, loaded through the git-ignored
  `CLAUDE.local.md` when present. Nothing private goes in this file.

## Product principles

This is not a "do one thing" app, but it must never become a "lots of things,
all mediocre" app.

- **Depth over breadth.** A feature ships complete, with backend, UI, API,
  tests, error states, sensible defaults and help text, or it doesn't ship.
  Cut scope rather than merge something half-done.
- **Fewer, better settings.** Every setting needs a reason to exist and a good
  default. Rarely needed ones go behind "Show advanced".
- **Logical navigation.** Someone who has never used Radarr/Sonarr should
  reach any page in two clicks.
- **One app, not three glued together.** Use the fact that indexers, downloads
  and the library live together. For example, cleanup knows each tracker's
  seeding rules.
- **Never lose or corrupt media.** Destructive actions default to safe, are
  logged with a reason, and support dry-run.

## Scope

**v1**, built in the order given in the Roadmap section:
- Movies and TV (standard and daily series) in one app
- Multiple versions of a title (for example 1080p and remux), driven by quality
  profiles, so there's no need for separate instances
- Built-in indexer management (replaces Prowlarr)
- Cloudflare solver integration through a FlareSolverr-compatible API
  such as TRAWL.
- Release parsing; simple quality profiles, with custom formats under Advanced
  (TRaSH Guides compatible)
- qBittorrent as the download client (Transmission and Deluge followed in 1.x)
- Import pipeline, renaming, scanning of existing libraries
- Download cleanup engine (**must-have**)
- Radarr/Sonarr-compatible API, so Seerr and the Dashboard work unchanged
- Auth: local login, IP/subnet bypass, OIDC, API key
- Notifications: Emby library refresh, email, webhook
- Migration from existing Radarr, Sonarr and Prowlarr instances. Reelkeeper must
  first build a correct library on its own (scan and identify, no import);
  importing from Radarr/Sonarr comes later, next to each cutover. Prowlarr
  indexers are imported with the indexers milestone.

**Later**: design for these now, but don't build them yet:
- Usenet (SABnzbd/NZBGet)
- Anime (absolute numbering, XEM scene mapping)
- Other torrent clients beyond Transmission and Deluge (both shipped,
  as 1.2/1.4, see Roadmap). Only clients users ask for, e.g.
  rTorrent (seedboxes), Flood, Aria2, Download Station; each has its own
  API. qBittorrent Enhanced Edition speaks qBittorrent's API and should
  already work (test once before going public). Dan, 2026-10-15: no
  exhaustive list, only near-free drop-ins beyond the big ones.
- Import lists (Trakt/IMDb)
- Multiple users with roles
- Telegram notifications (Dan, 2026-10-09).
- Jellyfin and Plex library updates, like Emby's (planned for 2.0).
- Interactive (user-driven) search. Seerr already does this well (Dan,
  2026-10-08); Reelkeeper searches automatically. The search engine exists for
  that; only the screens are left out.
- A Calendar page (tabled by Dan, 2026-10-11: he never used Radarr's or
  Sonarr's, and Seerr already shows what's coming). The data exists: the
  compat calendar endpoints and the summary's upcoming lists.

**Non-goals**:
- Subtitles (Bazarr's job)
- Music and books
- Playback or acting as a media server
- Request management (Seerr's job)
- Solving Cloudflare challenges itself
- Auto-updating
- Kubernetes manifests

## Stack (and why)

- **Backend: Go** (latest stable, pinned in `go.mod`).
  - Builds a single static binary with low RAM use. The target Proxmox host is
    almost fully committed, and the .NET-based arr apps use hundreds of MB
    each.
  - Strong concurrency for schedulers and polling.
  - Easy cross-platform builds for future open-source users.
  - .NET (what the arr apps use) was the runner-up; it lost on footprint.
- **HTTP:** chi router. Logging: `log/slog`, structured.
- **Database:** SQLite by default (zero config; pure-Go `modernc.org/sqlite`, no
  CGO) or PostgreSQL (`pgx`). MariaDB is deliberately unsupported: the arr apps
  also stopped at these two, and every extra engine doubles DB testing.
  - Query layer: Bun.
  - Migrations: goose, one folder per engine.
- **Frontend:** React + TypeScript (Vite), TanStack Query and Router, Tailwind
  with shadcn/ui. The built assets are embedded in the Go binary.
- **API contract:** `api/openapi.yaml` is the source of truth for the native
  API.
  - Go types are generated with oapi-codegen and TS types with
    openapi-typescript.
  - Never hand-edit generated code.
- **Live UI updates:** Server-Sent Events.
- **Tooling:**
  - Lint: golangci-lint (Go) and Biome (TS).
  - Format: gofmt/goimports and Biome.
  - Tests: `go test` and Vitest.
  - Task runner: `just`.

## Commands

Created in M0. If one is missing or wrong, fix the Justfile and this list
together.

- `just dev`: Vite dev server at http://localhost:7676 plus the Go server
  with live reload (air) on :7677
- `just test`: all Go and frontend tests, using SQLite
- `just test-pg`: all Go tests on SQLite and Postgres (starts the dev stack)
- `just lint`: must be clean before every commit (golangci-lint on the Linux
  build, as CI runs it; Biome; tsc)
- `just fmt`: format all code
- `just gen`: regenerate Go and TS code from `api/openapi.yaml`
- `just build`: production binary with the UI embedded, in `bin/reelkeeper`
- `just image`: build the container image for amd64 and arm64
- `just image-local`: build the image for this machine and load it
- `just dev-stack` / `just dev-stack-down`: local docker compose (Colima)
  with Postgres, qBittorrent, Transmission, Deluge and Seerr for integration
  testing (peer discovery permanently off; never on the internet)
- `just test-clients`: the real-client tests against the dev stack (~40 min)
- Single Go test: `go test ./internal/parser -run TestName`
- Single frontend test: `cd web && npx vitest run src/pages/LoginPage.test.tsx`

## Layout

- `cmd/reelkeeper/`: entry point, with subcommands `serve`, `healthcheck`, `probe`,
  `migrate`, `reset-auth`
- `internal/api/`: native REST API (`/api/v1`). Handlers only, no business
  logic.
- `internal/compat/`: Radarr/Sonarr-compatible API. Translation only.
- `internal/library/`: titles, versions, root folders, files, scanning,
  renaming
- `internal/naming/`: folder and file name templates (Radarr syntax). Pure
  functions.
- `internal/metadata/`: TMDB client and cache
- `internal/indexer/`: Torznab/Newznab, the Cardigann definition engine, rate
  limits, health
- `internal/parser/`: release-name parsing. Pure functions, heavily tested.
- `internal/mediainfo/`: reads MKV/MP4 headers for resolution, codecs, HDR,
  audio and runtime
- `internal/decision/`: quality profiles and custom formats; accepts or rejects
  releases with plain-English reasons. Pure functions. Bundles TRaSH
  Guides custom formats (MIT) in `trash/`.
- `internal/grab/`: automatic search, RSS sync and the scheduled pass;
  History
- `internal/download/`: the qBittorrent, Transmission and Deluge clients,
  moving torrents between them, Reelkeeper's downloads table and queue, blocklist, and the "Check downloads" task that triggers imports
- Importing itself is `library.ImportMovie` (`internal/library/import.go`),
  next to the scanner whose file handling it shares; there's no separate
  importer package.
- `internal/cleanup/`: queue janitor and seeding policy
- `internal/auth/`: sessions, local users, OIDC, API keys, bypass rules
- `internal/jobs/`: persisted command queue and scheduler (survives restarts;
  visible under System → Tasks)
- `internal/notify/`: Emby, email, webhook
- `internal/store/`: DB access; `migrations/sqlite/` and `migrations/postgres/`
- `web/`: React app
- `testdata/`: release names, recorded indexer responses, and golden
  Radarr/Sonarr API responses

## Core design

### Titles and versions
A title (movie or series) has one or more **versions**. Each version is a
quality profile plus a root folder, a monitored flag, and its own files,
upgrades, queue and history. A regular copy and a remux copy of the same movie
are two versions of one title, not two instances. Metadata is shared.

- A profile can set a default root folder (for example, Remux → `/media/remux`).
- Versions can live in separate root folders or share a
  folder using Emby/Jellyfin multi-version naming
  (`Movie (2020) - Remux.mkv`).
- Each version is searched, upgraded and cleaned up independently.
- **Upgrades are opt-in for movies already on disk** (Dan, 2026-10-16).
  `Version.Upgrades` (movies only; series versions keep true): false means
  Reelkeeper keeps the file it has and never looks for a better copy, though a
  version with no file is still searched. The root folder's `upgrade_found`
  decides it for movies found from files on disk (scans, review accept, Radarr
  import); movies added by hand or by Seerr are always true. New folders
  default to off; folders from before migration 22 are on. Switching a movie
  folder rewrites every movie version in it. Every `Version` insert must set
  `Upgrades` (Bun writes false for a zero bool).

### Quality profiles (easier than Radarr/Sonarr)
Same power as Radarr/Sonarr, but configured in plain choices instead of score
math.

- **Presets** ship ready to use (for example HD, Remux, 4K and Compact).
  Most users only pick one.
- **Simple editor:**
  - Resolutions to accept
  - A source ladder (Remux > Blu-ray encode > WEB-DL > WEBRip > HDTV): the
    lowest acceptable tier and "upgrade until"
  - Size limits per movie or per episode-hour
  - Languages
  - Season-pack preference (TV)
  - Release groups to prefer or block
  - HDR/Dolby Vision, codec and audio features, each set to
    **require / prefer / avoid / block** rather than numbers
- **Under the hood**, the simple settings compile to qualities plus
  custom-format scores. "Advanced" shows and edits the raw custom formats and
  scores, and imports or exports TRaSH Guides JSON. Switching to Advanced must
  never silently change what the profile does.
- **Explain every decision.**
  - Every automatic grab or rejection is recorded with why, in plain words,
    and shown in History. (Interactive search, which would show this per
    release, is a "later" item.)
  - Each profile page has a "test a release name" box.
- Each profile applies to movies, TV or both.

### Radarr/Sonarr compatibility (drop-in for Seerr)
- Each quality profile can be exposed as its own compat endpoint:
  - `/compat/radarr/<profile-slug>/api/v3/...` (Radarr-shaped)
  - `/compat/sonarr/<profile-slug>/api/v3/...` (Sonarr-shaped)
- In Seerr, Reelkeeper is added once per profile wanted, as a normal
  Radarr/Sonarr server: host, port, API key, and that URL base. Nothing else
  is needed. For example:
  - Radarr default server → `/compat/radarr/hd`
  - Radarr 4K server → `/compat/radarr/remux`
  - Sonarr → `/compat/sonarr/hd`
- **Why one endpoint per profile:** Seerr tracks regular and 4K status per
  server entry. It also skips the add when the server reports the movie as
  already having a file. With a single shared endpoint, a remux request for a
  movie that has a 1080p copy would look finished and never download.
- **So each endpoint shows only its own version of a title.** hasFile,
  monitored, size, queue and history all describe that version. An add creates
  or re-monitors that version only.
- An endpoint's qualityprofile list contains just its own profile, so Seerr's
  choice is unambiguous.
- It must cover everything Seerr and the Dashboard call: system/status,
  qualityprofile, rootfolder, tag, movie/series list, lookup and add, command,
  queue, episode, and so on.
  - The authoritative list comes from **recorded traffic**: capture real
    requests and responses from the live Radarr/Sonarr into
    `testdata/compat/` and test against them.
- Match JSON field names, casing, types and status codes exactly. Extra fields
  are fine; missing ones are not.
- Report the versions of the instances being replaced (Radarr 6.x, Sonarr 4.x),
  because Seerr changes behavior based on version.
- Auth: `X-Api-Key` header or `apikey` query parameter.
- No business logic in `compat/`. It only calls the services.

### Indexers (replaces Prowlarr)
- Supports generic Torznab and Newznab, plus a runtime for **Cardigann YAML
  definitions** (the format Prowlarr and Jackett use). That way hundreds of
  trackers work without writing code for each one.
  - Definitions are fetched from the upstream community repo
    (Prowlarr/Indexers) while Reelkeeper runs and updated on a schedule, never
    vendored or committed. That repo has no license file and syncs its
    definitions from Jackett (GPL-2.0), so they stay out of Reelkeeper's code
    (decided 2026-10-08). Tests use made-up
    definitions.
- Each indexer has:
  - Categories and capabilities
  - Rate limit and query/grab limits
  - Backoff when failing, plus health status and stats
  - **Seed requirements** (minimum ratio and seed time); cleanup reads these
- Existing indexers can be imported from Prowlarr's API. Secrets the API
  hides (passwords, cookies) are never printed or logged.
- **Cloudflare-protected trackers** work through an external
  FlareSolverr-compatible solver such as TRAWL (decided 2026-10-07). Reelkeeper
  never solves challenges itself.
  - The solver is configured once (URL, timeout) and enabled per indexer,
    like Prowlarr's FlareSolverr proxy tag.
  - Send the whole request through the solver; don't just borrow its
    cookies. Clearance cookies are bound to the solver's IP and browser
    fingerprint, so reusing them from another client is fragile (and in
    other setups the solver may sit on another host or network).
- **Tests never hit real trackers.** Use recorded responses; trackers ban
  clients that hammer them.

### Download cleanup (must-have)
Cleanup only ever touches torrents in Reelkeeper's own categories (labels in
Transmission and Deluge).

- **Rules.** Each has thresholds and a strike count, so a torrent must fail N
  checks in a row before anything happens:
  - stalled (no progress for X)
  - slow (below X KB/s for Y)
  - stuck downloading metadata
  - tracker reports the torrent as unregistered or removed
  - import failed or was blocked
  - files missing
- **Actions:** remove from the client (with its data), blocklist the release,
  search for a replacement.
- **Seeding policy after import:** remove once the ratio or seed time target is
  reached, but never before the indexer's own minimums. This avoids
  hit-and-runs on private trackers.
- **Orphans:** report torrents in its categories that it doesn't track, and
  stray files in the download folder. Report only, never auto-delete.
- **Dry-run** is on by default when cleanup is first enabled. Every action is
  logged with its reason under Activity → Cleanup.
- Reelkeeper sets "no limit" share limits on its own torrents, so the
  client's global limits don't stop seeding early.
- **The download disk:** finished torrents waiting in the client's queue
  never gain seeding time, so the user's seeding goal counts hours since the download
  finished (a tracker's minimum still counts real seeding time); "Keep at
  least N GB free" skips grabs that wouldn't fit and makes
  cleanup remove imported torrents early (tracker minimum met, longest
  finished first, rule "space"). Reelkeeper never changes the client's
  own queue settings.

### Library and imports
- **Scanning and identification** (no help from Radarr/Sonarr):
  - IDs in folder names win: `{tmdb-…}`, `{imdb-…}`, `{tvdb-…}`.
  - Otherwise title and year from the folder name (or the file name), then
    a scored TMDB search. Clear matches are accepted; uncertain ones go to a
    review screen. Never accept an uncertain match silently.
  - Quality: the release name gives source, group and edition; the file's
    own headers (MKV/MP4, read in Go, no ffprobe) give resolution, codecs,
    HDR/Dolby Vision, audio tracks, languages and runtime. Headers win when
    they disagree.
  - A file's version follows its root folder's default profile (e.g.
    `/media/remux` → Remux).
  - Real names are messy: junk prefixes (`www.UIndex.org    -    `),
    episodes inside or outside `Season N` folders, samples and extras.
- **Metadata:** TMDB for both movies and TV (one API key; attribution
  required). Store TMDB, TVDB and IMDb IDs.
  - Seerr sends a tmdbId for movies but a **tvdbId for series**, so resolve it
    through TMDB's external-ID lookup.
- **Episode ordering** can differ between TMDB and TVDB, and Emby and Sonarr
  assume TVDB order. Support a per-series ordering (TMDB episode groups) and
  flag mismatches instead of guessing.
- **Parser:** table-driven tests with thousands of real release names plus a
  fuzz test. Every parser bug fix adds a test case. Library-style names
  (made up, in real naming styles) and hand-written cases, plus cases adapted from
  Radarr's and Sonarr's GPL-3.0 parser tests (allowed now Reelkeeper is GPL-3.0).
  Files with adapted cases say where they came from.
- **Import steps:**
  1. Copy the file, or hardlink it when it's on the same filesystem.
  2. Verify the size.
  3. Rename it into place atomically.
  4. Never delete the source while the torrent is still seeding; cleanup
     handles that later.
- **Naming:** templates with a live preview, in Radarr's syntax so TRaSH
  Guides schemes paste in unchanged. "Rename files" on a movie shows old and
  new names first, and only renames in media folders Reelkeeper manages: in
  place, never overwriting, subtitles with the same name included.

### Authentication
These mirror Radarr's options, but cleaner:
- **Local login:** forms, argon2id password hashes, server-side sessions.
- **Bypass list:** no login needed from listed IPs/CIDRs.
- **OIDC:** tested against Authentik; uses PKCE; can require a group claim.
  Includes a toggle to **disable local login**.
- **API key:** for `/api` and `/compat`.
- **Reverse proxy support:** configurable URL base and a trusted-proxy list.
  `X-Forwarded-For` is only believed when it comes from a trusted proxy;
  otherwise anyone could spoof a bypass IP.
- **Recovery:** `reelkeeper reset-auth`, or an env var, re-enables local login
  when OIDC is broken.
- Every mode needs tests, including spoofed headers. Never weaken auth to make
  something work.

### Config
- Env vars are for bootstrap only:
  - `REELKEEPER_DB`: SQLite path or Postgres URL
  - `REELKEEPER_PORT`
  - `REELKEEPER_URL_BASE`
  - `REELKEEPER_DATA_DIR`
  - `REELKEEPER_SECRET_KEY`: encrypts stored credentials
- Recovery only: `REELKEEPER_RESET_AUTH=true` turns password sign-in back on at
  startup (same as part of `reelkeeper reset-auth`, without changing passwords).
- Everything else lives in the DB and is edited in the UI.
- `GET /ping` is unauthenticated and makes no external calls, for
  healthchecks. Container images have no curl, so they use
  `reelkeeper healthcheck`.

### UI
- **Navigation:**
  - Movies
  - Series
  - Wanted
  - Activity: Queue, History, Seeding & cleanup
  - Settings: Media Folders, Quality Profiles, Indexers, Download Client, Cleanup,
    Notifications, Security, General
  - System: Status, Tasks, Logs, Backups, Blocklist
- **Branding:** the Gryphon palette (near-black `#07060b`, violet `#8b5cf6` →
  cyan `#22d3ee`) through theme tokens, so a public release can be rebranded.
  Support light and dark.
- Usable on a phone.
- Every list supports sorting, filtering and search. The library has poster
  and table views.

## Working rules for Claude

- **Explain in plain English.** Dan won't review code line by line.
  - Before each milestone or large feature, write a short plain-English plan
    and get his OK.
  - Afterwards, summarize what changed and how he can see it working.
- **Tests are the safety net.** The parser, decision engine, cleanup rules,
  auth and compat layer get tests before or alongside the code.
- `just lint` and `just test` must pass before calling anything done. A
  pre-commit hook (`.githooks/pre-commit`) blocks commits that fail lint or
  tests;
  enable it in a fresh clone with `git config core.hooksPath .githooks`.
- **Schema changes** need a migration for both SQLite and Postgres, tested on
  both.
- **Dependencies:** prefer the standard library. Add a dependency only with a
  stated reason in the commit message.
- **Commits:** small, with conventional-commit messages (`feat:`, `fix:`, ...).
- **Never touch real services from tests or development:** no real
  trackers, no one's production download client. Use the dev stack.
- **No bloat:** no settings, choosers or switches a feature doesn't need.
- **Plans state their size** (small / medium / large, and why), so the
  release a feature lands in is chosen knowing the cost.
- **Check visual changes at device scale 1** (headless Chrome
  `--force-device-scale-factor=1`), in dark and light.

## Branches and versions (from 1.0.0)

Planned with Dan on 2026-10-13; settled 2026-10-15 ("branch as you see
fit"; 1.x may include small features; Usenet and anime are probably 2.0).

- **Until 2.0 work starts, everything stays on `main`**: 1.1, 1.2… and
  patches are tagged there. A release branch only pays off once `main`
  holds unfinished or breaking work while a fix must ship.
- **When 2.0 work starts** (e.g. Usenet or anime), create `release/1.x`
  from the latest 1.x tag first. From then on `main` is 2.0, and 1.x
  fixes and small features (no schema changes, nothing breaking) go on
  `release/1.x`; production installs run its tags.
- **A fix lands on `release/1.x` first**, is tagged there, and then
  `release/1.x` is merged into `main`. That merge is how every 1.x fix
  reaches 2.0. Never merge `main` into `release/1.x`: it would leak
  half-built 2.0 work into a patch.
- **2.0 ships** as a v2.0.0 tag on `main`; `release/1.x` is then
  maintained for a while or retired.
- Images are built only for a tag (`.github/workflows/release.yml`, to
  ghcr.io/saltglass/reelkeeper, with release notes from git-cliff), so
  nothing on any branch reaches an install until it is tagged.
- **Schema changes:** once `release/1.x` exists, 1.x releases don't
  change the schema. If one truly
  must, it takes the next migration number on `release/1.x`, and 2.0's
  unreleased migrations are renumbered after it before the merge (safe
  only while 2.0 is unreleased). A 1.x backup restores into 2.0; a 2.0
  backup does not restore into 1.x.
- **Trying 2.0 next to a live install:** never share production's database,
  qBittorrent categories or media folders with a second instance (they
  would fight over the same downloads and files). Use the local dev
  stack, or a preview instance with its own database, its own categories,
  a test media folder and grabbing in dry run.

## Gotchas

- **Imports copy across filesystems.** When downloads and the library are on
  different filesystems (e.g. two NFS shares), hardlinks are impossible.
  Files replaced by upgrades go to `<media folder>/.reelkeeper-replaced/`
  (hidden from media servers), never deleted.
- **qBittorrent 5.2's login** answers 204/401 instead of 200 "Ok."/"Fails.";
  the client handles both.
- **NFS ownership can be fixed by the server** (all-squash), so chown and
  chmod may fail or be ignored. Treat that as non-fatal and don't retry.
- **Bypass list behind a reverse proxy.** Every request through the proxy
  comes from the proxy's IP. Never put the proxy's IP or a whole server
  subnet in the bypass list.
- **x86-64-v2.** Images must run on CPUs without AVX2 (common in VMs). Go's
  default build is fine.
- **RAM.** Aim for under 250 MB steady-state for a library of a few thousand
  titles.

## Open decisions

Ask Dan, then update this file once each is settled.

- **Usenet, anime, Jellyfin and Plex:** not in 1.x; 2.0, each from a plan
  Dan approves.

## Roadmap

Each milestone ends usable and tested. Don't start the next one while the
previous one is half-done.

- **Before going public** (private, 2026-10): milestones M0–M8 (foundation,
  movies and TV libraries, indexers, grab and import, cleanup, Radarr and
  Sonarr compatibility with imports from both, TV downloads, Wanted,
  backups and restore, the backfill queue, TV repacks, Seerr requests
  first, a queue limit), then Queue controls, Transmission, moving
  torrents between clients and Deluge. Version numbers started again at
  the public release; the private ones aren't used publicly.
- **1.0.0** (2026-10-16): the first public release. 1.x continues on
  `main`.
- **2.0:** cut `release/1.x` from the last 1.x tag first, then Usenet and
  anime, and Jellyfin and Plex next to Emby (Dan, 2026-10-16), each from a
  plan Dan approves. Other torrent clients only if users ask for them.
  - **Jellyfin and Plex:** the same job Emby has today: after an import,
    delete or rename, tell the media server which folder changed (with the
    same path translation), so it shows up without a full library scan,
    plus a Test button. Jellyfin forked from Emby and has the same
    "media updated" call, so it is small. Plex needs its own client: find
    the library section that holds the folder, then ask for a partial scan
    of that folder with the server's token. Naming and scanning already
    follow Jellyfin's and Plex's conventions (multi-version names, Plex
    edition tags, "Plex Versions" folders skipped).
