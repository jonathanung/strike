# Web cockpit polish log

Strike keeps its own name, copy, and the Bubble Tea palette from
`schemas/ui-tokens.json`. Panel radius is 10px on purpose; the TUI stays
square. See [theme.md](theme.md).

Iterations 1–4 were Strike-only layout passes. The files previously committed
as `comparison-*.png` were two Strike screenshots side by side (an earlier
Strike layout on the left, the palette pass on the right), squeezed into one
wide image. They were not Kiro Crew. Those files are removed.

The named Kiro full frames (`16-sessions-empty`,
`33-chat-empty-activity-panel-open`, `25-settings-agent-harness`,
`19-apps-discover`, `17-schedule-empty`, `46-dark-chat-empty-files-panel`) are
not in this workspace. A contact sheet in the agent attachment store OCRs as a
thumbnail index of those filenames; it is not a full-frame capture, so it was
not used as a left half. No new comparison image is committed until those
full frames are available.

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
`approval-1440.png`, `mobile.png`. The side-by-side PNGs committed with
this iteration were Strike versus Strike. They are deleted.

Defects:

- The activity column ended after its content, leaving a dead band in the side
  panel.

Fix: the side column is a flex column and the activity block grows. With two
blocked children the cards fill that column. Empty activity state sits in the
leftover space.

No further misalignment showed up at 1440 or 1024: panels share the top edge,
gutters are 10px, borders are 1px `--rule`, the command field is centered.

## Iteration 5

Shot: local `iter5/chat-1440.png` (1440×900), session `hello from the cockpit`.

Defects:

- The transcript was a monospace wall: the engine's `[child.completed …]`
  user note, with handoff and verification JSON inline.
- The echo provider's `Tip: start a message with \`run <command>\`` was
  rendered as part of that wall.
- Filter chips were still a 2×2 grid. Workspaces still sat open under the
  session list. Composer controls still wrapped. No icon set.

Fix: a `[child.completed]` note and a `child.completed` event collapse to one
card per child (name, status badge, one-line summary, Details closed). The
echo tip is stripped from transcript text. The run-command guidance lives in
the composer hint.

## Iteration 6

Shots: local `iter6/chat-1440.png`, `iter6/chat-1024.png`.

Measured at 1024×594: filter chips are one row (All x=23, Unread x=59,
In progress x=123, Pinned x=209, all y=88).

Defects still visible:

- The card summary was the handoff's echo line (`You said: say hello` plus
  the tip), not the verification failure.
- Activity stats said 2 blocked while the list rendered the empty state.
  Saved sessions are not the live team snapshot, so `/v1/team` was empty.
- A fallback clock used `started_ms == 0` and printed `498050:41:31`.
- Composer pills were not yet one row with Send. No icons. Daemon headings
  were still the muted 12px label.

Fixes: summaries prefer the verification or blocker line and drop the tip.
When the team snapshot has no members, activity rows are the session's saved
children plus the transcript card. Spacing scale is 4/8/12/16 and 12/13/14px.
Session rows are title, status dot, relative time, and one subtitle.
Workspaces are a collapsed section. Rail is 320px so the four chips fit.

## Iteration 7

Shots: `docs/webui-screenshots/strike-chat-1440.png`,
`strike-chat-1024.png`, `strike-daemon-1440.png`.

Measured:

- 1440×900 composer: Approval, Model, Steer, Queue, and Send all y=811,
  height 28. Send sits at the right edge (x=1053).
- 1024×594 composer: the same five controls all y=474, height 28.
- Filter chips still one row (y=91).
- Activity rows are 46px. The task line is `white-space: nowrap` (path
  height 17px). The bogus elapsed is gone.
- Seven inline SVGs in the shell (sessions, command, activity, files,
  changes, status). No external asset or icon font.
- Transcript cards read `scribe` / `blocked` plus the verification line, with
  Details collapsed. The echo tip is not a message. `You said: hello from
  the cockpit` remains once, because that is the echo provider's reply with
  the tip removed.
- Daemon sections use the same card, 16px padding, and 14px ink headings.

## Remaining gaps

- Named Kiro full-frame captures are not in this workspace, so there is no
  Kiro-left / Strike-right image. Schedule, Discover, and the agent-harness
  page are not Strike screens. The daemon page is the settings surface.
- Echo children still block on `schema:handoff`. Stop stays disabled while
  nothing is running. Retry spawns another child.
- The same child is listed in the sidebar, the transcript card, and the
  activity row. Those are three surfaces, not three copies of the JSON note.
  The stored session title is unchanged.
- Bundled themes that omit surfaces keep Strike's surface ladder. Nord's
  muted hex is the theme file.
- Back, Forward, and Focus stay in the command palette.
- `@path` is text. The composer does not expand slash commands.
- The activity task line is one visual line; the full verification sentence
  is still the summary, so it ellipsizes.
