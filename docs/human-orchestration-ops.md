# RFC: Public human orchestration operations (WEBUI.17 / #1085)

**Status:** Accepted; v1 Ops implemented in WEBUI.18 (#1088)  
**Tier:** A (specification); implementation is Tier C in engine/server/protocol  
**Parent:** [#1069](https://github.com/jonathanung/strike/issues/1069)  
**Blocks:** WEBUI.18 (#1088) protocol/engine implementation, WEBUI.19 (#1089) Team control UI  
**Depends:** WEBUI.1 contract (#1070), WEBUI.13 observation (#1081)  
**Coordinates:** #541 (serve transport/auth), #1032 (security audit), #1060 (cross-device continuity)

## 1. Problem

Task spawn, delegation, team-board transitions, peer messaging, and child
interrupt exist today as **model-facing tools** and engine methods
(`task`, `delegate`, `team_task`, `agent_message`, `agent_broadcast`,
`task_interrupt`, and the session-level `interrupt` Op). The browser has
observe-only team state (WEBUI.13) but no public mutation contract.

Ad-hoc REST mutations would bypass the shared protocol Op model, permission
evaluation, actor identity, replay/persistence, and audit correlation. This RFC
defines **public human orchestration Ops** as the only mutation path for Team
controls.

## 2. Goals and non-goals

### Goals

1. Map each candidate human action to an existing engine/tool primitive.
2. Make **public Ops** the canonical mutation contract; HTTP/WebSocket/RPC/ACP
   adapters must not invent different semantics.
3. Define actor, root/team targeting, ownership, attach-only, permission, audit,
   CAS/idempotency, capability negotiation, and versioned shapes.
4. Bound **v1** vs **deferred** so WEBUI.18/19 have a finite surface.

### Non-goals

- Implementing Ops, UI controls, human collaboration roles (#1056), or adaptive
  orchestration policy (#1058).
- A generic browser tool runner or arbitrary internal tool invocation.
- Cross-device controller arbitration and remote approval UX (#1060 owns those).
- Force-directed graph visualizer clone (#523 non-goal for web).

## 3. Design principles

| Principle | Rule |
|---|---|
| One mutation model | Every human team control is a versioned `pkg/protocol` Op. REST/WS are thin adapters that wrap the same Op. |
| Observe before control | Outcomes reuse existing events (`child.*`, `delegation.changed`, `agent.message`, `team.roster`, path overlap, verification). New events are additive and versioned only when no existing event represents the outcome. |
| No tool runner | The browser never calls `tool.Call` by name. Ops invoke the same engine entry points tools use, with a **human actor** stamp. |
| Fail closed | Attach-only, missing capability, cross-root, and permission denial reject **server-side** with stable errors. |
| Root isolation | Every Op targets exactly one root session (lead). Cross-root denial is mandatory. |
| Audit everything | Mutations emit `serve_op` / security-audit correlation consistent with #1032 and existing serve ops guard. |

## 4. Actor model

### 4.1 Identity

| Field | Source | Notes |
|---|---|---|
| `actorKind` | `"human"` | Distinguishes from model/tool actors in audit. |
| `actorID` | Serve auth principal when present; else `"local"` | Loopback unauthenticated serve uses `"local"`. Remote/auth modes (#541) must not invent anonymous writers. |
| `rootSessionID` | Op target / `?root=` / hub active root | Required. |
| `leadSessionID` | Engine team lead for that root | Usually equals root for single-lead teams. |

Human Ops are authorized only when the caller is controlling the **lead root**
for that team (or the solo root when no team exists). Child sessions are never
direct Op targets for spawn/board mutations.

### 4.2 Membership and ownership

| Check | Rule |
|---|---|
| Root exists | Target root must be a live (or explicitly resumable) session known to the hub. |
| Same project | Root's project key must match the serve process project. |
| Lead restriction | Spawn/delegate/board create require caller = lead root. |
| Child ownership | Interrupt/cancel of a child requires the child to be under the target root's team roster. |
| Cross-root denial | Any Op whose `rootSessionID` does not match the bound live root returns `403` / protocol error `cross_root_denied`. |
| Terminal team | After team dissolution / root close, mutations return `409` / `team_unavailable`. |

### 4.3 Attach-only and read-only

| Mode | Behavior |
|---|---|
| Attach-only (`!hasLive`) | **Every** orchestration mutation rejected server-side (`403` / `attach_only`). Observation APIs remain available. |
| `--read-only` serve | Same rejection path as other mutating ops (`read_only`). |
| Historical JSONL select | UI is read-only; no Op channel for that selection. |

Unsupported frontends (old clients without capability bits) must fail with
stable `501` / `capability_unavailable` rather than silent no-ops.

## 5. Capability negotiation

Bootstrap / session hello advertises:

```json
{
  "capabilities": {
    "team": true,
    "teamControl": true
  },
  "protocolOps": [
    "team.spawn",
    "team.message",
    "team.broadcast",
    "team.child_interrupt",
    "team.task_transition",
    "team.board_create",
    "team.board_claim",
    "team.board_complete"
  ]
}
```

| Flag | Meaning |
|---|---|
| `team` | Observation snapshot/events (already shipped WEBUI.13). |
| `teamControl` | Human orchestration Ops from this RFC are wired. |

When `teamControl` is false, listed Ops are absent from `protocolOps` and
HTTP adapters return `501 capability_unavailable: teamControl`.

RPC (`strike rpc`), ACP, and `pkg/sdk` must advertise the same Op names when
implemented (staged is OK; names are frozen here).

## 6. Candidate actions → engine primitives

| Human action | Existing primitive | v1? | Notes |
|---|---|---|---|
| Spawn / delegate child | `tool` `task` / `delegate` → engine child spawn | **v1** as `team.spawn` | Maps to lead-initiated spawn with objective, agent, optional budget/isolation. |
| Direct message | `agent_message` | **v1** as `team.message` | Human → child or human → lead mailbox. |
| Broadcast | `agent_broadcast` | **v1** as `team.broadcast` | Human as lead to all other members. |
| Child interrupt/cancel | `task_interrupt` / child cancel path | **v1** as `team.child_interrupt` | Distinct from session `interrupt` (current turn only). |
| Task/delegation transition | `delegation.changed` producer in engine | **v1** as `team.task_transition` | CAS on delegation version. |
| Team-task create | `team_task` create | **v1** as `team.board_create` | Board vocabulary only. |
| Team-task claim | `team_task` claim | **v1** as `team.board_claim` | CAS + owner. |
| Team-task complete | `team_task` complete | **v1** as `team.board_complete` | CAS + owner/lead. |
| Steer child mid-turn | `steer` Op / tool | **deferred** | Needs clearer human UX + safety. |
| Rewrite child prompt | internal only | **deferred** | Easy to desync transcripts. |
| Force path-ownership transfer | `patch_collab` internals | **deferred** | Conflict review UI (#1086) first. |
| Dissolve team | root close / engine teardown | **deferred** as dedicated Op | Use existing root close for now. |
| Arbitrary tool invoke | — | **rejected** | Never. |

## 7. Versioned Op shapes (v1)

All Ops use the existing envelope:

```json
{ "type": "<op>", "data": { ... } }
```

Common fields on every team-control `data` object:

| Field | Type | Required | Description |
|---|---|---|---|
| `rootSessionId` | string | yes* | Target root; may be implied by hub active root / `?root=` on HTTP. |
| `idempotencyKey` | string | yes for board + spawn | Client-generated UUID; server stores recent keys per root. |
| `clientMutationId` | string | optional | UI correlation; echoed on audit only. |

\*HTTP adapters may inject `rootSessionId` from the request root binding when
omitted; WS/RPC should send it explicitly when multi-root.

### 7.1 `team.spawn`

**Primitive:** engine child spawn (`task`/`delegate` semantics).

```json
{
  "type": "team.spawn",
  "data": {
    "rootSessionId": "…",
    "idempotencyKey": "…",
    "objective": "Implement X",
    "agent": "build",
    "name": "optional-alias",
    "isolation": "shared|worktree",
    "budget": { "maxTurns": 20 },
    "delegationId": "optional-link"
  }
}
```

**Success outcomes (events):** `child.started`, optional `delegation.changed`,
`team.roster`.  
**Reply (HTTP 200):** `{ "ok": true, "childSessionId", "name?", "delegationId?" }`.

**Errors:** `400` validation; `403` attach/cross-root/not-lead; `409` duplicate
idempotency with different body; `409` team_unavailable; `501` capability.

### 7.2 `team.message`

**Primitive:** `engine.agentMessage`.

```json
{
  "type": "team.message",
  "data": {
    "rootSessionId": "…",
    "idempotencyKey": "…",
    "to": "<childSessionId|lead>",
    "body": "…",
    "kind": "message|request",
    "urgency": "normal|high|blocker",
    "taskId": "optional"
  }
}
```

**Outcome event:** `agent.message`.  
Human messages set `from` to a stable human actor id (not a fake child).

### 7.3 `team.broadcast`

**Primitive:** `engine.agentBroadcast`. Lead-only. Same body/urgency fields as
message without `to`. Outcome: N-1 `agent.message` events.

### 7.4 `team.child_interrupt`

**Primitive:** child cancel / `task_interrupt` path (not session `interrupt`).

```json
{
  "type": "team.child_interrupt",
  "data": {
    "rootSessionId": "…",
    "idempotencyKey": "…",
    "childSessionId": "…",
    "reason": "optional human reason"
  }
}
```

**Outcomes:** `child.escalated` / `child.completed` with canceled/interrupted
status as engine already emits. Duplicate idempotent interrupt after terminal
child → `200` with `{ "ok": true, "alreadyTerminal": true }`.

### 7.5 `team.task_transition`

**Primitive:** delegation lifecycle CAS.

```json
{
  "type": "team.task_transition",
  "data": {
    "rootSessionId": "…",
    "idempotencyKey": "…",
    "delegationId": "…",
    "expectedVersion": 3,
    "toState": "working|blocked|completed|failed|canceled",
    "reason": "optional"
  }
}
```

**Outcome:** `delegation.changed` with `prev`, `state`, `version`.  
**CAS miss:** `409 conflict` with current version in error payload.

### 7.6 Board Ops

Map 1:1 onto `team_task` tool semantics (create / claim / complete).

| Op | Key fields | CAS |
|---|---|---|
| `team.board_create` | `title`, `body?`, `assignee?`, `dependsOn?` | n/a (create); idempotencyKey dedupes |
| `team.board_claim` | `taskId`, `expectedVersion` | yes |
| `team.board_complete` | `taskId`, `expectedVersion`, `summary?` | yes |

**Outcomes:** existing team-task / delegation events already projected by
WEBUI.13. Do not invent a parallel board event family in v1.

## 8. Permission posture

Human Ops run through the **same permission stack** as the lead session where a
tool analogue exists:

| Op | Permission name (proposed) | Default posture |
|---|---|---|
| `team.spawn` | `team.spawn` (or reuse `task` permission) | ask in supervised; allow in agent/yolo per mode |
| `team.message` / `broadcast` | `team.message` | allow for local human lead; ask when remote (#541/#1060) |
| `team.child_interrupt` | `team.interrupt` | allow local lead |
| board + transition | `team.board` | allow local lead |

Managed deny ceilings (#1032) must be able to block spawn/interrupt. YOLO does
**not** skip attach-only or cross-root checks.

Explain integration: `/permission` and `GET /v1/permissions/explain` gain sample
rows for these permission names when WEBUI.18 lands.

## 9. Audit and correlation

Every admitted or denied Op records:

| Field | Value |
|---|---|
| family | `serve_op` (HTTP/WS) or engine audit family for in-process RPC |
| `opType` | exact Op type string |
| `actorKind` / `actorID` | human identity |
| `rootSessionId` | target |
| `idempotencyKey` | when present |
| `outcome` | `ok` \| `rate_limited` \| `read_only` \| `attach_only` \| `denied` \| `conflict` \| `error` |
| `childSessionId` / `delegationId` / `taskId` | when applicable |
| bodies | **never** stored (same as current serve ops guard) |

Correlation id: reuse request id / WS frame id; surface on resulting events via
existing envelope `time` + engine correlation fields where present.

## 10. CAS, idempotency, reconnect

### 10.1 Idempotency keys

- Required on spawn, board_*, task_transition, child_interrupt, message, broadcast.
- Server retains a **bounded LRU per root** (recommended: 512 keys, 24h TTL).
- Replay with same key + **same canonical body hash** → return original success
  payload without re-mutating.
- Same key + different body → `409 idempotency_conflict`.

### 10.2 CAS

- Delegation and board tasks carry integer `version` already used by tools.
- Human Ops must send `expectedVersion` for transitions/claim/complete.
- Mismatch → `409` with `{ "error": "conflict", "currentVersion": N }`.

### 10.3 Duplicate delivery / reconnect

- HTTP clients should retry only with the same `idempotencyKey`.
- WS reconnect does not auto-replay Ops; UI must resubmit intentionally.
- Observation remains eventual via snapshot (`GET /v1/team`) + SSE backlog.

## 11. Transport adapters

| Channel | Mapping |
|---|---|
| `POST /v1/ops` | Existing op envelope; root from body or `?root=`. Rate limit + audit unchanged. |
| `GET /v1/ws` op frames | Same envelope; multi-root binding rules identical to today. |
| Optional REST sugar | `POST /v1/team/spawn` etc. **only** if they translate 1:1 to Ops and share validation — never divergent semantics. Prefer Ops-only in v1. |
| `strike rpc` / ACP / SDK | Register Op codecs in `pkg/protocol`; capability bits in hello/initialize. |

## 12. Replay and persistence boundaries

| Concern | Rule |
|---|---|
| Session JSONL | Engine-emitted outcome events persist as today. Ops themselves are not required to be stored as user messages. |
| Human message body | Appears in `agent.message` event content (redacted like other events). |
| Spawn objective | Persists on `child.started` / child session metadata as today. |
| Idempotency cache | Process-local is acceptable for v1; document loss on restart (client may safely retry with same key if outcome events already show success). |
| Attach-only replay | No mutations; snapshots/events only. |

## 13. Error catalog (stable strings)

| Code / error substring | HTTP | When |
|---|---|---|
| `capability_unavailable: teamControl` | 501 | Not wired |
| `attach_only` | 403 | No live engine |
| `read_only` | 403 | `--read-only` |
| `cross_root_denied` | 403 | Root mismatch |
| `not_lead` | 403 | Caller not lead |
| `team_unavailable` | 409 | Dissolved/missing team |
| `conflict` | 409 | CAS miss |
| `idempotency_conflict` | 409 | Key reuse different body |
| `already_terminal` | 200 (soft) or 409 | Policy choice: v1 uses 200 + flag on interrupt |
| `validation` | 400 | Bad fields |
| `permission_denied` | 403 | Permission stack deny |

## 14. Security, concurrency, and test matrix

### 14.1 Security

- [ ] Attach-only rejects all teamControl Ops.
- [ ] Cross-root spawn/message/interrupt denied.
- [ ] Non-lead cannot board_claim another lead's task across roots.
- [ ] Credential-shaped strings in message bodies redacted on event egress.
- [ ] Audit entries never contain bodies or secrets.
- [ ] Rate limit applies (shared serve ops bucket).
- [ ] Remote auth assumptions documented with #541; no anonymous remote write.

### 14.2 Concurrency

- [ ] Parallel claim on same taskId: one winner, one conflict.
- [ ] Parallel spawn with different idempotency keys: both succeed within budget.
- [ ] Interrupt during child tool call: terminal state coherent; no revive.
- [ ] Snapshot during mutation: readers see pre or post version, never torn structs (`go test -race`).

### 14.3 Replay / dissolution

- [ ] Idempotent retry after success returns same childSessionId.
- [ ] Ops after root close / team dissolve → `team_unavailable`.
- [ ] Historical session selection cannot submit Ops.

### 14.4 Compatibility

- [ ] Old clients without `teamControl` never see false success.
- [ ] SDK/RPC codec round-trip for each v1 Op.
- [ ] Observation-only clients unaffected.

## 15. v1 vs deferred (implementation bound)

### v1 (WEBUI.18 must implement)

1. `team.spawn`
2. `team.message`
3. `team.broadcast`
4. `team.child_interrupt`
5. `team.task_transition`
6. `team.board_create`
7. `team.board_claim`
8. `team.board_complete`
9. Bootstrap `teamControl` + `protocolOps` entries
10. Permission names + audit outcomes
11. Idempotency cache + CAS errors
12. Focused race tests from §14

### Deferred (explicitly out of WEBUI.18/19 v1)

- Steer / mid-turn prompt rewrite
- Path-ownership force transfer
- Dedicated team dissolve Op
- Human multi-role ACLs (#1056)
- Cross-device approval and controller arbitration (#1060)
- REST sugar resources beyond `/v1/ops`
- Browser-visible generic tool runner (**rejected permanently**)

### WEBUI.19 UI scope (after 18)

- Controls only for v1 Ops above
- Confirm dialogs for interrupt/spawn
- CAS conflict recovery (reload snapshot + retry)
- Attach-only / capability disabled explanations
- No graph editor; list/board actions only

## 16. Implementation sketch (non-normative)

1. Add Op structs + codec cases in `pkg/protocol`.
2. Engine methods: thin wrappers around existing spawn/message/board paths with
   `actorKind=human` and idempotency hooks.
3. `internal/server` ops guard: allowlist types when `teamControl` wired;
   attach-only/read-only short-circuit.
4. Permission registration in default layers.
5. SDK/RPC/ACP advertisement.
6. Web Team controls (#1089) call `sendOp` only.

## 17. Cross-check sources

Authors implementing WEBUI.18 must re-verify against:

- `pkg/protocol` — Op/Event envelopes, `AgentMessage`, child/delegation events
- `internal/engine` — `agentMessage`, `agentBroadcast`, child spawn/cancel, delegation
- `internal/tool` — `task`, `delegate`, `team_task`, `task_interrupt`, `agent_message`
- `internal/server` — ops guard, attach-only, rate limit, audit
- `internal/rpc`, `internal/acp`, `pkg/sdk` — capability hello / codec
- `docs/web-cockpit-contract.md` — attach matrix and Team IA
- `docs/audit.md`, `docs/web.md` — serve_op family and operator docs

## 18. Acceptance mapping (#1085)

| Criterion | Section |
|---|---|
| Every action justified against engine/tool primitive | §6 |
| Public Ops canonical; adapters do not diverge | §3, §11 |
| Actor, root/team, ownership, cross-root denial | §4 |
| Permission + audit vs #1032; remote vs #541/#1060 | §8, §9 |
| CAS/idempotency/duplicate delivery | §10 |
| Attach-only rejects; unsupported stable errors | §4.3, §5, §13 |
| Reuse observation events; additive only if needed | §3, §7 |
| RPC/ACP/SDK capability advertisement | §5, §11 |
| Security/concurrency/replay/dissolution tests | §14 |
| v1 vs deferred bound for .18/.19 | §15 |

## 19. Decision

**Accept** this RFC as the implementation contract for WEBUI.18/19. No human
Team mutation UI ships until WEBUI.18 implements the v1 Ops list with the
permission, audit, CAS, and attach-only gates above.
