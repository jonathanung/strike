# Failure injection and chaos testing

Harness failure modes (mid-tool kill, fsync failure, canceled streams, corrupt
session tails, permission flips) are exercised by a small **test-only** fault
catalog in [`internal/fault`](../internal/fault). This is **not** a production
chaos monkey — faults are never armed outside tests.

**Issue:** [#808](https://github.com/jonathanung/strike/issues/808)

## Catalog

| Point | Kind | Where | Safe outcome |
|---|---|---|---|
| `session.sync` | wired | `session.Store` fsync on header/append | `Append` latches + `Recover` rolls back to known-good size; prior complete JSONL lines remain `Replay`-able; runtime cancels on fatal |
| `session.write` | wired | `session.Store` before Write | `Append` errors without latch (retryable); manager may recover/retry |
| `process.after_start` | wired | `tool.RunProcess` after `Start` | Process tree killed; `ProcessStatusCanceled`; bash → `ErrorCodeCanceled`; no hang |
| `provider.stream_drop` | logical | tests close stream without terminal event | `NormalizeStream` → `ErrIncompleteStream`; engine retries then `stopReason=error` + `EngineError` |
| `permission.flip_mid_turn` | logical | tests `PermissionReply` reject / hard-deny rules | `ToolCallEnd` with `permission_denied`; defined turn stop |
| `session.log_truncate` | logical | tests truncate/corrupt JSONL on disk | Trailing partial line skipped; interior corrupt → `CorruptError` (never silent garbage) |

Wired points call `fault.Check` in production code. When unarmed, `Check` is a
cheap no-op. Logical points need no production hook — the chaos suite injects
them via test doubles and filesystem fixtures.

## Running the suite

Chaos tests are ordinary `go test` cases named `TestChaos*` under:

- `internal/fault` — Arm/Check registry
- `internal/session` — sync fail, truncate/corrupt
- `internal/tool` — process kill, bash cancel code, cancel×inject races
- `internal/engine` — stream drop, permission flip, cancel+tool+session

```sh
# Focused (fast)
go test ./internal/fault/ ./internal/session/ ./internal/tool/ ./internal/engine/ \
  -run 'Chaos|TestArm|TestCatalog' -count=1

# Full tier C (CI always races)
go test -race ./... -count=1
```

Or via Make:

```sh
make chaos   # focused Chaos/Fault package tests
```

## How to add a new fault

1. **Name it** in `internal/fault` (`const` + `Catalog()`).
2. **Prefer logical injection** (scripted provider, temp files, ops) when the
   failure is already expressible at a public boundary.
3. **Wire only when needed** — call `fault.Check(fault.YourPoint)` at the
   narrowest production site. Document the call in the table above.
4. **Arm in tests only:**
   ```go
   disarm := fault.Arm(fault.YourPoint, 1, nil)
   t.Cleanup(disarm)
   // … exercise path …
   ```
5. **Assert a defined safe outcome:** stable error code / status, no hang,
   session `Replay` succeeds **or** returns `*session.CorruptError` (not
   silent garbage), and error strings do not echo secrets.
6. **Cover cancel×inject** when the path interacts with ctx cancel or
   `protocol.Interrupt`.
7. **Keep it offline** — no network; use `echo` / scripted providers.

## Non-goals

- Production runtime chaos against user machines
- Owning sandbox resource limits (#799), eval regression packs (#807), or
  trace retention (#810) — those stay on their issues
- Replacing durability (#803), cancel (#794), or retry (#795) unit tests;
  chaos composes them under multi-fault scenarios
