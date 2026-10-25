# Web UI screenshots

Headless Chrome captures of `strike serve --provider echo` on this branch.
These are the running UI, not mockups.

| File | What it shows |
|---|---|
| `before-daemon.png` | Daemon connect view before the session/activity layout |
| `chat-activity.png` | Tabbed session, transcript, and Activity with one running child and one blocked handoff |
| `activity-expanded.png` | Same session with the blocked child's verification output expanded |
| `activity-stopped.png` | The sleeper after `team.child_interrupt` (stopped, 49s) next to the blocked child |
| `empty-session.png` | A new root with an empty transcript and no delegated children |
| `daemon.png` | Daemon view: connect, saved daemons, one workspace |

The running child is echo `run sleep 90` (bash actually sleeping; sandbox degraded because `bwrap` is absent). The blocked child is a prose objective that fails the default `schema:handoff` gate. Done, failed, and stalled were not produced by this echo run, so they are not in the shots.
