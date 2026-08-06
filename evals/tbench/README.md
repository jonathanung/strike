# Terminal-Bench subset (E3.4 / #562)

Internal regression runner for a fixed **25-task** slice of
[Terminal-Bench 2](https://github.com/harbor-framework/terminal-bench-2)
(Harbor task format). Same harness shape as SWE-bench E3.3 (`strike exec
--json`, Docker per instance, versioned `report.json`).

**Do not publish pass rates in the product README.**

## Dataset pin

| Field | Value |
|---|---|
| Pack | `harbor-framework/terminal-bench-2` |
| Image tag | `20251031` (`alexgshaw/<task>:20251031`) |
| Subset | SHA-256(`strike-e3.4-v1:`+id) order, first 25 (see `internal/eval/tbench/testdata/subset.json`) |

Clone the pack locally (tests/ + instruction.md are required for grading):

```bash
git clone --depth 1 https://github.com/harbor-framework/terminal-bench-2.git /path/to/tb2
```

## Quick start

```bash
# List the committed subset ids
strike eval tbench --subset-only

# Wiring check (no Docker / no model calls)
strike eval tbench --dry-run --grader none --out /tmp/tb-dry

# Real run (Docker + credentials + task images)
strike eval tbench \
  --tasks-dir /path/to/tb2 \
  --provider anthropic \
  --model claude-sonnet-4-20250514 \
  --grader docker \
  --out evals/tbench/results/$(date -u +%Y%m%dT%H%M%SZ)
```

Optional JSONL of instance objects (instruction + image metadata) can replace
the pack for agent-only runs; docker grading still needs `--tasks-dir` so
`tests/test.sh` is available.

```bash
strike eval tbench --dataset /path/to/instances.jsonl --tasks-dir /path/to/tb2 ...
```

## Outputs

Each run writes:

- `report.json` — versioned metrics (pass rate, tokens, cost, wall-clock, reward)

Commit new `report.json` copies under `results/` when you want trend history.
Keep the sample fixture for schema reference.

## How it works

1. Materialize `/app` from the task image onto the host
2. Drive `strike exec --json --auto` in that workspace
3. Grade: fresh container → copy workspace + `tests/` → `bash /tests/test.sh` →
   read `/logs/verifier/reward.txt` (or `reward.json`)

Container backend is the Docker CLI (`swebench.Runtime`). #592 may swap in
`internal/container` without changing this runner.

## Config overlays (E3.5)

```bash
strike eval tbench --config-json '{"compactionThreshold":0.5}' --tasks-dir /path/to/tb2 ...
strike eval tbench --exec-arg '--sandbox=off' ...
```

Matrix driver: `strike eval sweep --benchmark tbench` (see `evals/sweep/README.md`).
