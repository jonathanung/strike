# Ranked hypotheses (do not apply until Phase 2 floor exists)

One change per iteration. Prefer HARNESS over PROMPT. No repo/task special cases.

| # | Bucket | Change | Why | Est. DEV salvage |
|---|---|---|---|---|
| 1 | HARNESS-TOOL | **i1 in tree:** TB live bind-mount + bash docker-exec + `--sandbox=off` | Host `network.allow` denies `python3`/`openssl`/`git`; `/app` does not exist on the host. Auto-route when `STRIKE_EVAL_CONTAINER` **and** `STRIKE_EVAL_WORKDIR` are set (SWE sets only the container id). | 3–6 / 25 TB (need ≥2σ ≈ 3 extra) |
| 2 | HARNESS-LOOP | `strike exec --json` flush on agent timeout | gcode/chess 900s → empty stdout → StatusError, no grade | 1–2 / 25 TB |
| 3 | HARNESS-TOOL | Bash output keep head+tail (pytest failures at end) | 16KB keep-first; seaborn/sklearn fails show trunc=4–7 | 1–3 SWE |
| 4 | HARNESS-TOOL | Glob: implicit `**/` when pattern has no slash | `*.py` is cwd-only | 0–2 SWE+TB |
| 5 | HARNESS-TOOL | `apply_patch` RecordBytes after write | Next edit hits CheckFresh false-positive | low — DEV fails use edit/write |
| 6 | HARNESS-TOOL | `apply_patch` accept unified `diff --git` | Models emit git diffs; tool wants envelope | low on this model |
| 7 | HARNESS-OUTPUT | Empty git-diff: apply last assistant `diff --git` once | ADHD first-line patch never hits the tree | 0 — DEV fails have patches |
| 8 | HARNESS-OUTPUT | Widen extract excludes (scratch `*.py` at repo root) | Extra files break official apply | 0–2 SWE |
| 9 | PROMPT | SWE preamble already points at `eval-test` | Pair 1–2 misses all used eval-test | skip |
| 10 | HARNESS-LOOP | 80% timeout nudge: leave a source diff now | Wander until 30m, empty tree | 0 — patches exist |

SWE remaining fails (pairs 1–2): 15–16 instances, all patched + `eval-test` → mostly **CAPABILITY**. Do not burn iterations on prompt tweaks until a concrete harness bug appears.

Iteration 1 is the TB live-image runtime (items 1+2 from the old list, one product feature).
