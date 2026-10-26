# Web UI status

One status vocabulary for the daemon and the Rust client. Other streams
import `webui/src/status.rs`. Do not add a second pill, gutter, or id list.

Machine-readable copy: [`schemas/session-status.json`](../schemas/session-status.json).
Go: `pkg/protocol` (`StatusIdle` … `ClassifyLive`, `SessionStatus`).
Wire version `1.19.0` (no new transcript event). See [protocol.md](protocol.md).

## Ids

| Id | Label | CSS variable | When |
|---|---|---|---|
| `idle` | Idle | `--muted` | Root can take input. Gateway chip uses `--success` instead. |
| `running` | Running | `--accent-alt` | Turn, tool, or thinking. Thinking is not its own id. |
| `waiting` | Waiting | `--warning` | Approval, question, or stall. Wins over running. |
| `blocked` | Blocked | `--warning` | Child handoff blocked. |
| `done` | Done | `--success` | Finished child or task. A root after `end_turn` is `idle`. |
| `failed` | Failed | `--signal` | Turn or child error, including `budget_exhausted`. |
| `stopped` | Stopped | `--muted` | Canceled turn or child. |
| `disconnected` | Disconnected | `--signal` | Client only. Heartbeat older than 6s. |

`stalled` normalizes to `waiting`. The activity row may still say "Stalled"
via the `label` prop. `data-status` stays `waiting`.

Unknown strings (`tool`, a free-form badge) are not a status.
`StatusKind::parse_known` returns `None`. `parse` maps those to `idle` only
when a caller wants a total function.

## Component

```rust
use crate::status::{StatusKind, StatusMark, StatusSlot, StatusTab, StatusTabs};

view! {
    <StatusMark kind=StatusKind::Running place=StatusSlot::Gutter unread=true />
    <StatusTabs tabs=tabs on_select=move |id: String| { /* open id */ } />
}
```

`StatusMark` sets `data-status` to the id and `data-unread` to `true` or
`false`. The `place` prop is a `StatusSlot`: `gutter`, `dot`, `badge`,
`chip`, or `row` (also written to `data-slot`). It is not named `slot`
because Leptos reserves that word and will not nest it inside HTML.

- `gutter` — session list. 3px bar plus an acid unread pip.
- `dot` — tabs and activity.
- `badge` — pill. `label` overrides the word.
- `chip` — header. Idle is success green so "daemon is up" matches the old chip.
- `row` — badge plus elapsed (`m:ss`) and last activity (`now`, `2m`, `1h`).

`StatusTabs` is the strip above the transcript, one tab per live root.
Empty input renders nothing. Session selection (#1261) can restyle the strip;
it should keep `StatusTabs` or `StatusMark` so the id and unread attributes
stay put.

The shell brand line still says `Connected` while `/health` is ok and
`Unreachable` when it is not. That keeps the cockpit's existing words.
The chip next to it is [`StatusMark`] with the gateway aggregate
(`idle` paints success). A missed heartbeat sets `data-status` to
`disconnected` and the chip label to `Unreachable`.

`apply_document_chrome(kind, unread_count)` sets `document.title`
(`Strike`, `Strike · Running`, `(2) Strike · Waiting`) and a favicon whose
fill is the computed CSS variable for that kind.

`HEARTBEAT_STALE_MS` is `6000`. `HEARTBEAT_INTERVAL_MS` is `2000`.

## Daemon

| Call | Body |
|---|---|
| `GET /health` | existing fields plus `now` (unix ms). No token. |
| `GET /v1/status` | live snapshot plus `turnState`, `turnSince`, `lastEventAt`, `stopReason`. |
| `GET /v1/status/sessions` | `{now, gateway, heartbeatIntervalMs, heartbeatStaleMs, sessions[]}`. |
| `GET /v1/status/events` | SSE. First event `snapshot`. `heartbeat` every 2s. `session.status` when a row changes. |
| `POST /v1/sessions/{id}/seen` | `{}` or `{"at": millis}`. Clears unread. |
| `DELETE /v1/sessions/{id}/seen` | Forces unread until the next ack. |
| `GET /v1/roots` | each root gains `status`, `statusSince`, `lastActivityAt`, `unread`. |
| `GET /v1/sessions` | each saved root gains `status`, `lastActivityAt`, `unread`. |

`sessions[]` items are `protocol.SessionStatus`: `sessionId`, `title`,
`status`, `since`, `lastActivityAt`, `unread`, `elapsedS`, `live`.
`elapsedS` is set only for `running` and `waiting`.

Unread is one file, `<session-dir>/status-seen.json`, mode `0600`.
A session with no ack is unread only when its last activity is after the
file's `epoch`, so old transcripts do not all flip on the first boot.
Reads and `/v1/status` polls do not count as activity. `LastEventAt` moves
on user, turn, tool, ask, and child events.

`gateway` on the board is the aggregate: waiting, else running, else blocked,
else failed, else idle. The server never sends `disconnected`.

Pins stay in browser storage. This contract does not move them.
