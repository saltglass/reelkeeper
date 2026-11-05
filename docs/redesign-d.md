# UI redesign: direction D, "Contact sheet"

Dan chose direction D on 2026-11-04 (designs: the "Reelkeeper redesign: C vs D"
canvas on claude.ai, bottom row). The old look (near-black, violet-to-cyan
gradient, page glow, rounded soft-shadow cards, stat cards) goes away
completely; there is no "classic" option.

This file is both the plan and the hand-off. Each step is committed and pushed
on its own (branch `claude/gracious-cannon-u4heoj`); the checklist says where
work stopped.

## Decisions (made while building; tell Dan in the summary)

- Themes: `dark` = D (the default). `light` = D on cream paper, film strips
  stay black (a printed contact sheet). `contrast` = High contrast in D's
  style, 7:1. `midnight`, `ember`, `moss` and `paper` are removed; a saved
  dark one falls back to `dark`, `paper` to `light`. The page glow and its
  setting are removed. "Match system" and the Custom accent stay; Custom now
  only swaps the orange (buttons, links, edge print, focus ring, logo).
- CSS helpers in index.css: `.film` / `.film-v` (strips with sprocket holes),
  `.edge-print`, `.label-caps`, `.grease` / `.grease-row` (red loop on
  `:focus-visible` or `data-selected="true"`), `.unexposed` (wanted artwork).
  Tailwind: `bg-film text-film-foreground`, `text-edge`, `text-edge-film`,
  `font-condensed`, `font-mono`, `bg-sidebar`, `text-downloading`. All
  `rounded-*` radii are 0 and `shadow-xs/sm/md` are none via the theme.
- Logo: same shape (`Logo.tsx`), one colour (`--logo`, orange in dark). The
  gradient tokens `--brand-from/--brand-to` are removed from the UI.
- Fonts, self-hosted with @fontsource (SIL OFL): Barlow (interface), Barlow
  Condensed (headings, uppercase), DM Mono (numbers, metadata, edge print).
  Bebas Neue is dropped.
- Corners 0 everywhere. No shadows except menus/dialogs (`--shadow-pop`).
  No blur, and nothing blurs `<main>` or the poster grid (iPhone Safari
  memory; see index.css comments).
- Motion (Dan, 2026-11-04): frames, rows and cards *develop* in, fading up
  from the dark one after another (`.animate-rise`, opacity only, the first
  24 of a list; the poster grids fade in all together, since a per-poster
  stagger read as a choppy cascade), on the poster grids and tables, movie and series pages,
  Queue, History, Seeding & cleanup and everywhere else that already used
  it. Posters themselves develop too (`DevelopImg`, `.develop`): clear until
  the picture has loaded, then a 700 ms fade, because a black frame fading
  in on the dark sheet is invisible and the poster then popped in after it.
  Settings → Appearance → Motion scales or turns both off. No hover
  shimmer on posters: Dan agreed it would clash with the contact sheet.
- Status is shown as edge print (DM Mono caps, orange) in words, not
  coloured pills. Red `--selection` is only the grease-pencil mark for the
  selected or keyboard-focused poster/queue row.

## Tokens (dark / light / contrast)

| token | dark | light | contrast | use |
|---|---|---|---|---|
| background | #1b1a19 | #efebe3 | #000000 | the sheet |
| foreground | #e6e2d8 | #1b1a19 | #ffffff | text |
| card | #0b0b0a | #f7f4ee | #0a0a0a | raised panels (cards, menus) |
| card-foreground | #e6e2d8 | #1b1a19 | #ffffff | text on panels |
| film / film-foreground | #0b0b0a / #e6e2d8 | same | #0a0a0a / #ffffff | film strips (black in every theme) |
| sidebar | #121211 | #e4dfd5 | #000000 | sidebar, phone top bar |
| muted | #2a2927 | #e0dbd0 | #1a1a1a | wells, unexposed frames |
| muted-foreground | #9e998e | #5a554d | #d4d4d8 | secondary text |
| placeholder | #9e998e | #5a554d | #a1a1aa | input placeholders |
| border | #2b2a28 | #d3cdc1 | #a1a1aa | hairlines |
| input | #77736a | #77716a | #d4d4d8 | control borders (3:1) |
| ring | #f0a04b | #874d0b | #ffff66 | focus ring |
| primary | #f0a04b | #f0a04b | #ffc266 | main button fill |
| primary-foreground | #1a1208 | #1a1208 | #000000 | text on primary |
| accent / accent-foreground | #2a2927 / #f0a04b | #e0dbd0 / #874d0b | #1a1a1a / #ffc266 | current nav item |
| link | #f0a04b | #874d0b | #ffc266 | links on the sheet |
| edge | #e08a2e | #874d0b | #ffb060 | edge print on the sheet |
| edge-film | #e08a2e | #e08a2e | #ffb060 | edge print on film |
| selection | #e5483b | #c8352a | #ff6b5e | grease pencil (marks only) |
| destructive | #f0a04b | #9e3019 | #ff8a8a | failed / remove |
| success | #8fcb86 | #2e6b37 | #6ee7a0 | |
| warning | #e8c15a | #7a5800 | #fcd34d | |
| downloading | #7fb2e5 | #1f5f99 | #9ccaff | downloading only |
| logo | #f0a04b | #b8631a | #ffc266 | the mark |

## Steps

- [x] 1. Foundations: fonts, tokens, themes (index.css, hooks/theme.ts,
      web/index.html, AppearanceCard, theme tests), remove glow/gradients,
      Logo one colour.
- [x] 2. Shell: AppShell sidebar (▸ markers, mono counts, Settings/System
      fold open only when inside them), top bar (breadcrumb `LIBRARY ▸ MOVIES`
      in mono), VPN indicator, Jump to, phone menu.
- [x] 3. Primitives in components/ui: button, badge (edge print), card (square,
      no shadow), input, switch, segmented, progress (frames), alert, dialog,
      dropdown, field; Toaster; PageHeader (condensed uppercase title).
- [x] 4. Film strips: Movies and Series poster grids (`.film-cell`, edge
      print, frame numbers via `frameNumber` in PosterTitle.tsx, wanted
      artwork greyed with `.unexposed`, grease pencil on focus/selection) and
      table views (on `.film`, frame numbers, mono year and size); Queue (one
      `.film` strip, numbered rows, progress as frames lighting up); History
      (a `.film-v` strip with square markers); Wanted (titles on `.film`,
      posters `.unexposed`); Seeding & cleanup (lists on `.film`, goal shown as
      ten frames, `GoalFrames`). Stat cards became `SummaryLine`.
- [x] 5. Home (summary line, condensed headings), Login (film leader), Movie
      and Series pages (hero, versions and episode lists on film). Settings
      and System pages use the restyled parts; the last old-look details went
      (blurs, pulsing dot, Status heartbeat line and memory ring, bouncy
      pop-ins).
- [x] 6. CLAUDE.md Branding updated; Biome, tsc and Vitest pass (no Go code
      changed). Screenshots checked at device scale 1 in Dark, Light, High
      contrast and phone width for Home, Movies (posters and table), Queue,
      History, Wanted, Seeding & cleanup, a movie's page, Settings → General
      and Media folders, and Login.

## Follow-ups

- All checked now; see "Beta check" below.

## Beta check (2026-11-04, `just dev` on the dev stack)

Checked in Dark, Light and High contrast at 1440×900 and 390×844 (phone),
device scale 1, plus a text-contrast scan of every page (WCAG AA: 4.5:1, or
3:1 for large text). The sidebar's grey ▸ markers are decorative and stay
below that on purpose.

- [x] Orange text and icons on the sheet used the button orange
      (`text-primary`), 1.8:1 on cream in Light: the current sidebar item,
      settings-card and icon-button symbols, the monitored eye on a series,
      the VPN shield, the restore spinner and checked profile chips. They
      now use `text-link` (the current nav item `text-accent-foreground`, a
      pressed tile's border `border-ring`).
- [x] System → Tasks on a phone: "Run now" sat beside each task and left
      its description a narrow column; below 640px it goes underneath.
- [x] Four-way choices (the download client: qBittorrent, Transmission,
      Deluge, Built-in) didn't fit a phone: "Transmission" was cut off and
      Built-in lost its "Experimental" badge. Segmented controls with more
      than three choices now wrap into two columns below 640px; the chosen
      one is filled in place (no sliding indicator), and its badge takes the
      fill's text colour so it stays readable when chosen.
- [x] High contrast: placeholders (examples such as
      `http://192.168.1.30:8096`) were nearly as bright as real values, so an
      unset Jellyfin or Plex looked filled in. New token `--placeholder`
      (same as muted-foreground in Dark and Light; `#a1a1aa`, 7.8:1, in High
      contrast), used by inputs and Jump to.
- [x] Motion had almost nothing left to do: the redesign took the
      entrance animation off Movies, Series, Queue, History, Seeding &
      cleanup and the movie page. It's back as a fade-only "develop" (see
      Decisions), checked under Lively (0.52 s), Subtle (0.29 s) and Off.
- [x] A series' page (Severance; Law & Order: SVU with upcoming episodes),
      Dark, Light, High contrast and phone, from a real library: on film
      as designed. Fix: an episode's "Not monitored" was muted at 70%
      (3.9:1 on film); now full muted (above 4.5:1).
- [x] Settings → Download client, Media servers, Notifications, Security
      (with the fixes above; nothing else of the old look found)
- [x] System → Status, Tasks, Backups (with the Tasks fix above)
- [x] README screenshots retaken (same names and views, 1440×757, device
      scale 1) from a library with real TMDB posters: placeholder files
      found by a real scan, 20 movies added by hand, and grabs and imports
      through the dev stack's made-up Usenet (fakenews and SABnzbd), with
      Reelkeeper run as a container on the dev stack's network.

## Checking it visually

`dev/` has no mock server, so screenshots were taken with a throwaway
Playwright script that runs `npx vite` (port 7676) and answers
`/api/v1/*` with made-up data (poster images as flat SVG). Inside `.film`
and `.film-cell` every colour switches to its on-film version (`--film-*`
tokens), so text on black stays readable in the light theme; check new
film-strip parts in Light as well as Dark.

## Resuming locally

Prompt for a local Claude Code session:

> Continue the Reelkeeper UI redesign described in docs/redesign-d.md on
> branch `claude/gracious-cannon-u4heoj` (pull it first). Read that file and
> CLAUDE.md, check `git log` for the steps already committed, then carry on
> from the first unchecked step. Commit each step separately with
> conventional-commit messages and tick it off in docs/redesign-d.md. Keep
> `cd web && npx biome check . && npx tsc -b && npx vitest run` passing.
> Don't change the backend. When all steps are done, summarise for Dan in
> plain English what changed and how to see it.
