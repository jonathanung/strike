# Web UI screenshots

Headless Chrome captures of `strike serve --provider echo` on this branch.
These are the running UI, not mockups.

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

## Earlier tabbed slice

| File | What it shows |
|---|---|
| `before-daemon.png` | Daemon connect view before the session/activity layout |
| `chat-activity.png` | Tabbed session, transcript, and Activity with one running child and one blocked handoff |
| `activity-expanded.png` | Same session with the blocked child's verification output expanded |
| `activity-stopped.png` | The sleeper after `team.child_interrupt` (stopped, 49s) next to the blocked child |
| `empty-session.png` | A new root with an empty transcript and no delegated children |
| `daemon.png` | Daemon view: connect, saved daemons, one workspace |
