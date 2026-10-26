# Web cockpit polish log

Compared with Kiro Crew 0.10.0 reference shots (sessions, chat with a side
panel, files, settings). Strike keeps its own name, copy, and the Bubble Tea
palette from `schemas/ui-tokens.json`. The references are a layout bar, not a
skin. Panel radius is 10px on purpose; the TUI stays square. See
[theme.md](theme.md).

Viewports: 1440×900, 1024×594, and 390×844.

## Iteration 1

Shots: `empty-1440.png`, `empty-1024.png`, `mobile.png`, `palette-1440.png`,
`daemon-1440.png`, `files-1440.png` (local capture, not all committed).

Defects:

- Page used an invented gray shell (`#0e1116` / `#171b22`) instead of the TUI
  ladder (`#14131c` / `#232230`).
- Running status was green. Blocked was orange. The TUI uses cyan for working
  and amber for a gate.
- Header was a packed row. The command field sat left of center. Back,
  Forward, and Focus competed with the field at 1024px.
- Section labels were shouted in small caps. Empty states were dashed boxes.
  The composer hint was a paragraph. Session rows were loose. The pin control
  looked like a primary button.

Fixes in the same pass: CSS variables from the token file, dark Strike as the
default, light Strike plus the six bundled themes, status classes remapped.

## Iteration 2

Shots: `iter2/empty-1440.png`, `empty-1024.png`, `mobile.png`.

Defects still visible after the token swap:

- Command field center was 303px on a 1024px viewport (should be 512).
- Workspaces and a long disclaimer sat above the session list.
- Activity had a second "ACTIVITY" heading under the Activity tab.
- "Pinned" was an orphan chip on its own row. Session rows were 60px because
  an empty child list still took a grid row.
- Approval, model, and Steer/Queue controls were 26px and 28px in the same row.
- On a phone the header was three rows and the side panel started below a
  forced 520px chat.

Fixes: absolute-centered command field, Back/Forward/Focus moved into the
command palette, sessions above workspaces, shorter hints, sentence-case
headings, quiet pin, flat empty states in the rail.

## Iteration 3

Shots: `iter3/empty-1440.png`, `empty-1024.png`, `mobile.png`.

Measured at 1024×594:

- Filter chips land on two equal rows (y=88 and y=120), not 3+1.
- Session rows are 43px.
- Composer controls are all 28px.
- Command field center is 512.5px. It does not overlap the gateway chip.
- Mobile activity sits inside the 844px viewport (bottom 819). No horizontal
  overflow.

Fixes: 2×2 filter grid, hide empty child lists, file rows get a 1px rule,
shorter files hint. A first attempt to hide empty child lists with `Show`
failed to compile (`FnOnce`); `:empty { display: none }` is what shipped.

## Iteration 4

Shots: `iter4/empty-1440.png`, `empty-1024.png`, `chat-1440.png`,
`chat-1024.png`, `files-1440.png`, `daemon-1440.png`, `palette-1440.png`,
`approval-1440.png`, `mobile.png`. Side-by-side commits:
`comparison-sessions.png`, `comparison-chat.png`, `comparison-files.png`,
`comparison-settings.png`.

Defects:

- The activity column ended after its content, leaving a dead band in the side
  panel.

Fix: the side column is a flex column and the activity block grows. With two
blocked children the cards fill that column. Empty activity state sits in the
leftover space.

No further misalignment showed up at 1440 or 1024: panels share the top edge,
gutters are 10px, borders are 1px `--rule`, the command field is centered.

## Remaining gaps

- Echo children block on `schema:handoff` immediately, so this pass did not
  catch a running row. Stop is present and disabled while nothing is running.
  Retry on a blocked row spawns another child (observed: scribe, then
  say-hello, both blocked).
- Suggestions and the rail have no icons. The web client has no icon set.
- Bundled themes that omit surfaces keep Strike's surface ladder, matching
  `theme.Resolve()`. Nord's own muted color is low contrast on Nord's
  background; that hex is the theme file.
- Schedule, Discover, and the agent-harness settings page are not Strike
  screens. The daemon page is the real settings surface.
- The transcript will show the engine's `[child.completed …]` note. That text
  is stored on the session; the client does not rewrite it.
- Back, Forward, and Focus are command-palette actions, not header buttons.
- `@path` is inserted as text. The composer does not expand slash commands.
