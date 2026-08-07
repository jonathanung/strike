# Parameter sweeps (E3.5 / #563)

Compare config dials on a fixed eval subset (SWE-bench E3.3 or Terminal-Bench
E3.4). Each matrix point runs the subset once with a project-layer
`.strike/config` overlay (and optional `strike exec --effort`) and records
pass rate, tokens, cost, and wall-clock for side-by-side comparison.

**Do not publish pass rates in the product README.**

## Builtin matrices

| Name | Points |
|---|---|
| `compaction` | baseline / tight / loose / aggressive-prune (`compactionThreshold`, `pruneProtectTokens`, `pruneMinimumTokens`) |
| `leanCode` | `off` \| `lite` \| `full` |
| `deferTools` | `off` \| `on` |
| `effort` | `off` \| `low` \| `medium` \| `high` (via `--effort`) |
| `all` | concatenation of the above (default) |

```bash
strike eval sweep --matrix leanCode --list-points
```

## Quick start

```bash
# Wiring check (no Docker / no model calls) — SWE-bench subset, deferTools A/B
strike eval sweep \
  --benchmark swebench \
  --matrix deferTools \
  --dry-run \
  --limit 2 \
  --grader none \
  --out /tmp/sweep-dry

# Real sweep (Docker + credentials). Prefer --limit while iterating.
strike eval sweep \
  --benchmark swebench \
  --matrix compaction \
  --provider anthropic \
  --model claude-sonnet-4-20250514 \
  --limit 10 \
  --out evals/sweep/results/$(date -u +%Y%m%dT%H%M%SZ)

# Terminal-Bench workload (optional second instrument)
strike eval sweep \
  --benchmark tbench \
  --tasks-dir /path/to/tb2 \
  --matrix leanCode \
  --provider anthropic \
  --model claude-sonnet-4-20250514
```

Single-point overrides without the sweep driver:

```bash
strike eval swebench --config-json '{"leanCode":"full","deferTools":"on"}' ...
strike eval tbench --exec-arg '--sandbox=off' --config-json '{"compactionThreshold":0.5}' ...
```

## Outputs

Each sweep writes:

- `summary.json` — comparison table (pass rate, tokens, cost, wall-clock per point)
- `<point-id>/report.json` — underlying subset report for that point

Commit `summary.json` copies under `results/` when you want trend history.
Keep the sample fixture for schema reference.

## How overlays work

Before `strike exec`, the runner writes the point's JSON to
`<workspace>/.strike/config` (project config layer). SWE-bench patch extraction
excludes `.strike/` so the overlay never lands in `model_patch`.

## Progressive disclosure offline pack

For first-turn schema reduction and compatibility fixtures without Docker, see
[`evals/progressive/README.md`](../progressive/README.md) and
`go test ./internal/eval/progressive`.
