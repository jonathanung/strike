# SWE-bench Verified subset (E3.3 / #561)

Internal regression runner for a fixed **50-instance** slice of
[SWE-bench Verified](https://huggingface.co/datasets/SWE-bench/SWE-bench_Verified).

**Do not publish pass rates in the product README** (SWE-ABS caveat).

## Quick start

```bash
# List the committed subset ids
strike eval swebench --subset-only

# Wiring check (no Docker / no model calls)
strike eval swebench --dry-run --grader none --out /tmp/swe-dry

# Real run (Docker + credentials + large images)
strike eval swebench \
  --provider anthropic \
  --model claude-sonnet-4-20250514 \
  --grader docker \
  --out evals/swebench/results/$(date -u +%Y%m%dT%H%M%SZ)
```

Optional local dataset export (JSONL of instance objects) avoids HuggingFace
fetch at start:

```bash
strike eval swebench --dataset /path/to/verified.jsonl ...
```

On Apple Silicon, official `sweb.eval.x86_64.*` images are pulled with
`--platform linux/amd64` (qemu). The runner bind-mounts the host checkout into
a live eval container so the agent can `docker exec` the conda testbed instead
of host Python. Eval `strike exec` defaults to `--sandbox=off` so that path
can reach the Docker socket; isolation remains the per-instance container.

Official harness grading (when `pip install swebench` is available):

```bash
strike eval swebench --grader harness ...
```

## Outputs

Each run writes:

- `report.json` — versioned metrics (pass rate, tokens, cost, wall-clock)
- `predictions.jsonl` — SWE-bench prediction rows for external re-grade

Commit new `report.json` copies under `results/` when you want trend history.
Keep the sample fixture for schema reference; replace with real runs as needed.

## Container backend

Today the runner shells out to the Docker CLI (`internal/eval/swebench.Runtime`).
[#592](https://github.com/jonathanung/strike/issues/592) wires the same runner
onto `internal/container` + the scheduler container pool.

## Config overlays (E3.5)

Pass dials without editing code:

```bash
strike eval swebench --config-json '{"leanCode":"full","deferTools":"on"}' ...
strike eval swebench --exec-arg '--sandbox=off' ...
```

Or run a full matrix via `strike eval sweep` (see `evals/sweep/README.md`).
