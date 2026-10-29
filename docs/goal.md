# Goal loop harness (`/goal`)

Deterministic runtime for observe → plan → act → evaluate → reflect until
falsifiable success criteria pass or a termination guard fires.

## Design

| Layer | Role |
|---|---|
| **Harness** | State, budgets, hooks, termination, persistence — never the LLM |
| **Planner** | Stochastic stripe (LLM or evaluate-only stub) — cannot mark criteria done |
| **Critic** | Separate evaluation of `CheckSpec`s — only path that sets `satisfied` |

Zebra pattern: harness code and model calls alternate; they must not blur.

## Data

Stored under `~/.strike/goals/<project-hash>/`:

- `goals.json` — goal records
- `iter_<id>.jsonl` — committed iterations
- `events.jsonl` — stage observability
- `intents.json` — completed action keys for crash-safe resume

### CheckSpec (prefer left → right)

- `cmd: <shell>` — satisfied iff exit 0
- `predicate: <name>` — named harness predicate
- `judge: <prompt>` — optional LLM judge (fail-closed if unset)

### Constraints (defaults)

`max_iterations=25`, `max_cost_usd=5`, `max_wall_clock_s=1800`,
`max_no_progress_iters=3`, `max_tokens=200000`, `max_verify_failures=3`,
`allowed_tools=[]` (empty = **evaluate-only**; no tool side effects).

`max_tokens` counts a harness estimate (text length / 4, at least 1 per
committed iteration) until provider usage is plumbed. Zero on a record
written before this field means unlimited. `max_verify_failures` stops
the loop after that many consecutive unsatisfied critic passes even when
the state hash changes. Zero disables that guard for legacy records.

## Guards (ordered, first trip wins)

1. success — all criteria satisfied by critic  
2. human_abort — `/goal abort`  
3. budget — iterations / cost / wall clock / tokens (status `failed`; not resumable)  
4. verification — the same checks fail `max_verify_failures` times in a row (status `blocked`, escalation `human`)  
5. stall — stable `state_hash` or identical action sequences (`blocked`, escalation `human`)  
6. irrecoverable — same tool error class 3× (`blocked`, escalation `human`)  

The wall-clock budget counts time spent `active` only. Pause and `blocked`
stop the clock; the wait until Continue is not billed.

Resume from `blocked` sets a guard floor at the current iteration so the
same streak does not immediately trip again. Continue only after the check
or the tree changed.

A loop without guards never runs against real tools: empty allowlist blocks
every action in `pre_act`. Deny entries always beat the allowlist, including
when autopilot is on. Autopilot is not a skip-all switch: it still requires
an allowlist, and an empty allowlist runs no tools.

## Commands

```
/goal set "<desc>" --criterion "cmd: pytest -q" [--criterion …]
                   [--max-iter N] [--budget-usd X] [--tools a,b]
                   [--max-wall S]
/goal run [id]       # start/resume loop (evaluate-only planner by default)
/goal status [id]    # criteria matrix, spend, iteration
/goal pause|resume|abort [id]
/goal log [id] [--iter N]
/goal list
```

`set` validates and stores **pending**; it does not run until `/goal run`.

Default planner is evaluate-only (empty plan): useful to verify criteria and
to exercise guards. Tool actions require an explicit `--tools` allowlist;
hooks still block anything not listed. LLM planner injection is a later
integration (engine turn with restricted tools).

## Autopilot

Autopilot is a property of one goal, not a session permission mode. When it
is off (the default), the critic runs the verification command and tool
actions do not. When it is on, only the allowlist may run, and deny rules
still block. The web Goals view sends this on `POST /v1/goals`. The session
approval dial (default / plan / soft / edits / yolo) is unchanged.

`POST /v1/goals/{id}/run?background=1` starts the loop and returns `202`.
The loop keeps running after the page closes. Pause and abort stop it.
A second start while one is in progress does not start a second loop.

## Relationship to a schedule and a task runner

A goal is one loop with a verification command and the guards above. A
schedule (not built here) fires on a clock and may start a goal later; it
does not replace the guards. A task runner (not built here) walks a plan
section by section. Neither is this loop.

## Out of scope (v1)

Multi-goal queue, sub-loops, parallel actions, HITL tiers beyond pause/abort,
cost-aware planning.
