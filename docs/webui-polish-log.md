# Web cockpit polish log

Strike keeps its own name, copy, and the Bubble Tea palette from
`schemas/ui-tokens.json`. Panel radius is 10px on purpose; the TUI stays
square. See [theme.md](theme.md).

Iterations 1–4 were Strike-only layout passes. The files previously committed
as `comparison-*.png` were two Strike screenshots side by side (an earlier
Strike layout on the left, the palette pass on the right), squeezed into one
wide image. They were not Kiro Crew. Those files are removed.

Iterations 8–10 compare the attached Kiro Crew 0.10.0 frames (1024×594)
with Strike captured at the same CSS size and devicePixelRatio 2. Each
`comparison-*.png` is Kiro on the left and Strike on the right. Kiro is
Lanczos-upscaled from 1024×594 to 2048×1188 so neither half is shrunk.
Strike keeps the Bubble Tea palette. Kiro's name, mascot, copy, and colors
are not copied. Discover, Schedule, the plus menu, the model menu,
crewmates, and the task runner are reference files only. Strike does not
draw those screens.

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

## Iteration 8

Shots: local `iter8/` at 1024×594, devicePixelRatio 2, against Kiro 16, 33,
25, 46, 32, and 36. Kiro 16, 33, 25, 32, and 36 are light (corner near
white). Kiro 46 is dark (corner about rgb(25, 21, 28)). Strike's default
stays the dark Bubble Tea ground `#14131c`.

Defects against those frames:

- The active session row repeated each child with a status badge, so the
  sidebar and the activity list were the same task list.
- Activity's empty state had a title and a blank body.
- Back, Forward, and Focus were still command-palette rows.
- The rail was 320px and the side column 300px, so the transcript at 1024
  was a narrow strip between them. Kiro's center is the wide column.
- The header brand was two lines, and the gateway chip said "Gateway
  connected", so the centered command field had little room.
- Activity rows put the child name, the full verification sentence, the
  badge, and the agent on one grid line.

Fixes: sidebar rows are the session only (title, status dot, relative time,
Idle/Unread/Saved). Activity is "Live tasks" and counts tasks, not sessions.
Empty activity is an icon, "No live tasks", and one line of copy. Policy is
its own card on the daemon page. Back, Forward, and Focus left the palette.
Alt+Left, Alt+Right, and Alt+\ do that work, and the chords are in the rail
and on the palette footer. Columns are 268 / flexible / 280.

## Iteration 9

Shots: local `iter9/`. Dark captures were forced back to the dark scheme
after a light capture had stored `strike.scheme`.

Defects still visible:

- Activity was 222px wide. The verification sentence was the row title, so
  it ellipsized to nothing useful, and Stop/Retry sat in a second column.
- The shortcut line was a second paragraph under the pin hint, and the
  palette footer sat inside the scrolling command list.
- The composer hint wrapped to three lines under the pills.
- A light-scheme capture leaked into later shots through localStorage.

Fixes: an activity row is a short task headline, then the child name, then
Stop/Retry on their own line. `task_headline` keeps the clause before the
first colon ("verification failed") and the transcript card keeps the full
summary. The palette list scrolls and the shortcut footer stays put. The
composer hint is one shorter line. Captures set the scheme explicitly.

## Iteration 10

Shots: `docs/webui-screenshots/comparison-*.png`, from local `iter10/` at
1024×594, devicePixelRatio 2.

Measured on the dark empty shell: filter chips are one row (All x=21,
Unread x=57, In progress x=121, Pinned x=207, all y=91). Session rows are
44px. Composer controls are one row at y=507, height 28. The activity
column is 254×476. Empty activity reads "No live tasks". The chat capture's
sidebar row for `hello from the cockpit` is title, time, and Idle. The same
children appear only as transcript cards and as activity rows whose title
is "verification failed" and whose second line is `say-hello` or `scribe`.

Defects still visible, and the fixes in this pass:

- Three stat counters still shouted in the narrow activity column. The
  value is 14px and the label does not wrap.
- Approval rows did not show which mode was current. The open menu marks
  the current mode.
- Command rows were uneven. Palette and menu rows are at least 36px.
- The side column grew from 248px to 280px so a file name and a task row
  have a line to themselves. The center is still the wide column.

## Remaining gaps

- Echo children still block on `schema:handoff`. Stop stays disabled while
  nothing is running. Retry spawns another child. The activity headline is
  the short clause; the card still holds the long verification line.
- Untitled sessions render a short id. The daemon did not store a title.
- The sidebar, the transcript card, and the activity row can all mention
  one child. The sidebar does not list task state. The card is the
  completion record. The activity row is the live task.
- Bundled themes that omit surfaces keep Strike's surface ladder. Nord's
  muted hex is the theme file.
- `@path` is text. The composer does not expand slash commands.
- Discover, Schedule, crewmates, and the task runner are not Strike screens.
- Kiro's chrome is light and airy. Strike stays on the Bubble Tea dark
  ladder unless the scheme is Light. The light comparison is
  `comparison-sessions-light.png`.
