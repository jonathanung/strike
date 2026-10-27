# Web UI screenshots

Headless Chrome captures of `strike serve --provider echo` on this branch.
These are the running UI, not mockups.

## Kiro Crew 0.10.0 beside Strike

Each comparison is 1024×594 CSS, rendered at devicePixelRatio 2. Kiro is the
left half, Lanczos-upscaled from the attached 1024×594 capture to 2048×1188.
Strike is the right half at the same pixel size. Neither half is shrunk.

| File | Left | Right |
|---|---|---|
| `comparison-sessions.png` | Kiro 16, sessions and an empty chat | Strike sessions and an empty chat, dark |
| `comparison-sessions-light.png` | Kiro 16, the same light frame | Strike sessions and an empty chat, light scheme |
| `comparison-chat.png` | Kiro 33, chat with the activity panel open | Strike chat, activity open, blocked children |
| `comparison-settings.png` | Kiro 25, settings | Strike daemon |
| `comparison-dark.png` | Kiro 46, dark chat with the files panel | Strike dark chat with the files panel |
| `comparison-command.png` | Kiro 32, command bar | Strike command palette |
| `comparison-approval.png` | Kiro 36, approval menu | Strike approval menu |

`strike-chat-1024.png` and `strike-daemon-1024.png` are the Strike halves
alone, scaled back to 1024×594. `strike-chat-1440.png` and
`strike-daemon-1440.png` are the earlier 1440×900 pass.

## Session list shell

| File | What it shows |
|---|---|
| `shell-empty.png` | Session list, command field, gateway chip, suggestion cards, approval and Steer/Queue chips |
| `shell-palette.png` | Ctrl+K command list: commands, the two views, Steer/Queue, and the five approval modes |
| `shell-approval.png` | Approval menu. Plan denies writes. Yolo is this session only |
| `shell-files.png` | Files tab for the daemon working directory |
| `shell-changes.png` | Changes tab. This checkout had no diff, so the empty state is shown |
| `shell-activity.png` | Two delegated children, both `blocked` on `schema:handoff`, nested under the session |
| `shell-expand.png` | The blocked child's verification text expanded |
| `shell-load-error.png` | A base URL that does not answer, with Retry. The page does not go blank |
| `shell-mobile.png` | The same shell at a phone width |

Echo produced `blocked` for both a prose objective and `run sleep 25` (the tool finished, then the handoff gate failed). It did not produce done, failed, or stalled.

## Model menu

Headless Chrome on `strike serve --provider echo`. The before shot is the
flat list from `main` before this stream. The hold and effort shots use a
temporary `~/.strike/providers.jsonc` overlay so echo reports a 24-token
window and variants `low`, `high`, and `custom-route`. That file is not
the default install.

| File | What it shows |
|---|---|
| `model-menu-before.png` | The previous chip: one unlabeled row |
| `model-menu-1440.png` | Filter, unreported capabilities, unreported window, hidden effort |
| `model-menu-1024.png` | Same menu at 1024×594 |
| `model-menu-mobile.png` | Same menu at 390×844 |
| `model-menu-filter.png` | A query that matches nothing, with Enter still available for a freeform id |
| `model-menu-error.png` | `nope/missing` rejected by the daemon (`unknown provider`) |
| `model-menu-pinned.png` | A browser-local pin (`strike.modelPins`) |
| `model-menu-default.png` | Ctrl/click save writes the global default and leaves this session on echo |
| `model-menu-effort.png` | Variant effort. `custom-route` stays disabled |
| `model-menu-meter.png` | 40/64 after a real turn. Under 80%, so Send is not held |
| `model-menu-hold.png` | Critical fit on a 24-token window. Send is held |

## Earlier tabbed slice

| File | What it shows |
|---|---|
| `before-daemon.png` | Daemon connect view before the session/activity layout |
| `chat-activity.png` | Tabbed session, transcript, and Activity with one running child and one blocked handoff |
| `activity-expanded.png` | Same session with the blocked child's verification output expanded |
| `activity-stopped.png` | The sleeper after `team.child_interrupt` (stopped, 49s) next to the blocked child |
| `empty-session.png` | A new root with an empty transcript and no delegated children |
| `daemon.png` | Daemon view: connect, saved daemons, one workspace |
