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
`max_no_progress_iters=3`, `allowed_tools=[]` (empty = **evaluate-only**;
no tool side effects).

## Guards (ordered, first trip wins)

1. success — all criteria satisfied by critic  
2. human_abort — `/goal abort`  
3. budget — iterations / cost / wall clock  
4. no_progress — stable `state_hash` or identical action sequences  
5. irrecoverable — same tool error class 3×  

A loop without guards never runs against real tools: empty allowlist blocks
every action in `pre_act`.

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

## Out of scope (v1)

Multi-goal queue, sub-loops, parallel actions, HITL tiers beyond pause/abort,
cost-aware planning.
