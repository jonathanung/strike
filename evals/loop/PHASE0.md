# Phase 0 — Discovery

Date: 2026-08-14  
Harness: strike `eval` E3.3 / E3.4 (existing). No new runner was added.

## Exact commands

```bash
# Offline package tests
make swebench-eval

# List default embedded subsets
./strike eval swebench --subset-only    # 50 Verified ids
./strike eval tbench --subset-only      # 25 TB2 ids

# Wiring check
./strike eval swebench --dry-run --grader none --out /tmp/swe-dry
./strike eval tbench --dry-run --grader none --out /tmp/tb-dry

# Real run (this campaign uses --instance lists, not the 50/25 defaults)
./strike eval swebench \
  --dataset ~/.strike/eval/datasets/swebench_verified.jsonl \
  --provider xai --model grok-4.6 --effort high \
  --grader docker --agent-timeout 30m \
  --instance <id> --out evals/loop/results/<run>/<id>

./strike eval tbench \
  --tasks-dir ~/.strike/eval/tbench/terminal-bench-2 \
  --provider xai --model grok-4.6 --effort high \
  --grader docker \
  --instance <id> --out evals/loop/results/<run>/<id>
```

Pinned model (every result file / PIN.json):

| Field | Value |
|---|---|
| provider | `xai` |
| slug | `grok-4.6` |
| effort | `high` (`reasoning_effort=high` on the wire) |
| temperature | **unset** — strike's xAI chat-completions adapter does not send `temperature`; provider default applies |

Smoke: `strike exec --json --auto --provider xai --model grok-4.6 --effort high` returned `ok=true`, `model=grok-4.6`.

## Variants (not interchangeable)

| Suite | Variant in this repo | Official size | This campaign |
|---|---|---|---|
| SWE-bench | **SWE-bench Verified** (`SWE-bench/SWE-bench_Verified`, HF test split) | **500** | DEV 60 + HOLDOUT 60 (repo-level). FULL = all 500 |
| Terminal-Bench | **Terminal-Bench 2** (Harbor), pin `terminal-bench-2@20251031` | **89** tasks (88 runnable; `install-windows-3.11` excluded) | DEV 25 + HOLDOUT 25 (category-level). FULL = 88 runnable |

**Not** SWE-bench Lite / Full (2294) / Multimodal.  
**Not** Terminal-Bench v3.0. Public grok-4.6 ~26% on TB v3.0 and ~65.9% on DeepSWE v1.1 are **not comparable** to these suites.

Default embedded subsets (50 SWE / 25 TB) are a SHA-256 slice for internal regression. This loop does **not** use those 50/25 lists.

## Per suite

### SWE-bench Verified

- **Instances:** 500 official; DEV 60, HOLDOUT 60, FULL 500.
- **Repos (full):** django 231, sympy 75, sphinx 44, matplotlib 34, sklearn 32, astropy 22, xarray 22, pytest 19, pylint 10, requests 8, seaborn 2, flask 1.
- **Docker:** `docker.io/swebench/sweb.eval.x86_64.<id with __→_1776_>:latest`, `--platform linux/amd64`. One image per instance (multi-GB).
- **Agent timeout:** 30m (CLI default). **Grade timeout:** 15m (docker grader).
- **Grader:** `docker` (default). Applies model patch in a fresh image, then official `eval_script` when present, else reconstructed pytest / Django `runtests.py`. Resolved iff FAIL_TO_PASS pass and PASS_TO_PASS do not regress (`evalTestsPassed` / exit 0). Optional `harness` shells out to `python -m swebench.harness.run_evaluation`.
- **Driver:** `strike exec --json --auto --sandbox=off` in a host checkout bind-mounted at `/testbed` (`eval-test` helper).
- **Sequential wall-clock (30m cap):** DEV 60 × 30m = 30h; FULL 500 × 30m = 250h. At 4-way fan-out: ~8h DEV, ~63h FULL (plus image pull).
- **API cost (grok-4.6 $2/M in, $6/M out, $0.50/M cache; high effort):** easy smoke was $0.14–0.27 on grok-4.5. Planning rate **$2.00 / instance**. DEV ≈ $120; FULL 500 ≈ **$1,000**.

### Terminal-Bench 2

- **Instances:** 89 in pack; 88 runnable. DEV 25, HOLDOUT 25, FULL 88.
- **Docker:** `alexgshaw/<task>:20251031` (or `task.toml` `environment.docker_image`).
- **Agent timeout:** per-task `agent.timeout_sec` (observed 600–12000s, median 900s). CLI override unused so official per-task caps apply.
- **Verify timeout:** per-task `verifier.timeout_sec` (360–12000s), else 15m.
- **Grader:** copy workspace + `tests/` into a fresh task image, `bash /tests/test.sh`, read `/logs/verifier/reward.txt|json`. Resolved iff reward > 0.
- **Sequential wall-clock:** DEV 25 × ~15–20m ≈ 6–8h; FULL 88 × ~20m ≈ 29h. At 4-way: ~2h / ~8h.
- **API cost:** planning rate **$1.50 / task**. DEV ≈ $38; FULL 88 ≈ **$132**.

## One full run (Phase 0 step 3)

| | Wall-clock (4-way) | Est. API $ |
|---|---|---|
| SWE-bench Verified 500 | ~2.5 days | **$1,000** |
| Terminal-Bench 2 (88) | ~8 hours | **$132** |
| **Combined full pair** | **~3 days** | **$1,132** |

## Campaign budget (stop line)

A literal 20-iter protocol (3×DEV + 20×DEV + 4×HOLDOUT + 2×FULL + 3×final DEV + 1×cross-model) is ~3,800 instance-runs ≈ **$6,900** and more than a week of wall-clock.

**Operational stop: $3,500** (stated here so the loop has a number). That covers Phase 2 + a useful number of iterations + holdouts at 5/10, and may not cover 20 iters + two full 500-instance runs. If spend hits $3,500 we stop and write `evals/REPORT.md` with what we have.

## Splits (Phase 1)

Seed: `strike-eval-loop-v1`. Files: `evals/loop/splits/`.

**SWE repo-level holdout** (no shared repos):

- DEV (60): sympy 22, matplotlib 10, sklearn 9, xarray 6, pytest 5, pylint 3, requests 2, seaborn 2, flask 1. **No django, no astropy.**
- HOLDOUT (60): django 47, sphinx 9, astropy 4.

**TB category-level holdout** (no shared categories):

- DEV (25): scientific-computing 5, security 5, debugging 3, file-operations 3, model-training 2, mathematics 2, data-processing 2, games 1, personal-assistant 1, optimization 1.
- HOLDOUT (25): software-engineering 13, system-administration 4, data-science 4, machine-learning 2, data-querying 1, video-processing 1.

`install-windows-3.11` excluded (same as the shipped E3.4 subset).

## System prompt baseline (overfitting guard)

Static layers for default `build` + xAI + `leanCode=lite`: `shared.txt` + `xai.txt` + `lean_strict.txt` ≈ **1,286 tokens** (chars/4). Instance-specific eval preamble is extra. Track this every accepted prompt change; +30% ≈ 1,672 tokens.

## Capability ceiling

Expect a high CAPABILITY failure rate. grok-4.6 is publicly below frontier on both TB v3.0 and DeepSWE. Do not treat that as a harness bug.
