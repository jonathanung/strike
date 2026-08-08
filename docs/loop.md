# Recurring jobs (`/loop`)

> **Web cockpit:** The TUI `/loop` shortcut is superseded by durable **Goals** and **Workflows** controls under Project mode (WEBUI.12). The browser does not add a session-only loop engine.

Session-scoped scheduler that submits a prompt to the model on a fixed
interval. Distinct from [`/goal`](goal.md) (criteria harness with budgets and
guards): `/loop` is a simple cron-style LLM job, not a goal runtime.

## Commands

```
/loop <interval> <job>   # start (e.g. /loop 15m check pipeline)
/loop list               # active loops (bare /loop also lists or shows usage)
/loop stop [id]          # stop one id, or all when id omitted
```

### Interval

Go `time.ParseDuration` forms: `30s`, `15m`, `2h`, `1h30m`. A bare positive
integer is minutes (`15` → `15m`). Minimum `1s`, maximum `168h` (7 days).

### Lifecycle

- Lives only in the current TUI session — not written to JSONL or disk.
- Quit cancels every loop.
- Each fire is a normal `UserInput` turn (same permission mode, queue, history).
- If a turn is already running, the job is enqueued like a typed prompt.
- Display prompt is tagged `[loop <id>] …` in history/transcript labels.

## Examples

```
/loop 15m check pipeline
/loop 2h check for new issues and complete them as they come
/loop list
/loop stop l1
/loop stop
```

## vs `/goal`

| | `/loop` | `/goal` |
|---|---|---|
| Trigger | wall-clock interval | iterate until criteria / guards |
| Persistence | session only | `~/.strike/goals/…` |
| Success | none (runs until stop) | critic + criteria matrix |
| Side effects | full agent turn + tools | evaluate-only unless `--tools` |

Use `/loop` for periodic checks; use `/goal` for “keep going until tests pass”.
