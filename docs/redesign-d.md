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
  No blur. Nothing animates or blurs `<main>` or the poster grid (iPhone
  Safari memory; see index.css comments).
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

- The README's screenshots (docs/screenshots/*.png) still show the old look.
  Retake them from a real library (real posters) once this is merged.
- Not seen in a screenshot, only covered by tests: a series' page and the
  settings/system pages the mock data couldn't fill (Download client, Media
  servers, Notifications, Security, Status, Tasks, Backups). Worth a look in
  `just dev` with real data.

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
