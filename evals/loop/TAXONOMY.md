# Phase 3 — DEV failure taxonomy (pairs 1–3)

Model pin: `xai` / `grok-4.6` / `effort=high` / `temperature=unset`.

## Floor (Phase 2)

| suite | rates | mean | sd | 2σ floor |
|---|---|---|---|---|
| SWE DEV 60 | 73.33, 75.00, 73.33 | **73.89%** | 0.96 | **1.92 pts** (~+2/60) |
| TB DEV 25 | 75.00, 66.67, 79.17 (graded) | **73.61%** | 6.36 | **12.73 pts** (~+4/25 or 21–22 resolved) |

Spend after baseline: **$390**. Remaining to $3500 stop: **$3110**.

## Terminal-Bench (manual + sessions)

Classifier `network_denied` matches the traces. Dominant bucket is **HARNESS-TOOL**.

| instance | 1 | 2 | 3 | bucket | evidence |
|---|---|---|---|---|---|
| build-cython-ext | fail | fail | fail | HARNESS-TOOL | `git clone` + `python3` network_denied; needs image compile env |
| openssl-selfsigned-cert | fail | fail | fail | HARNESS-TOOL | `openssl` network_denied; no host `/app` |
| portfolio-optimization | fail | fail | fail | HARNESS-TOOL | `python3` / `python3-config` denied; C ext on host |
| financial-document-processor | fail | fail | fail | HARNESS-TOOL | `/app` missing; OCR/python blocked |
| pytorch-model-recovery | fail | fail | pass | HARNESS-TOOL | host `python3` denied (torch lives in image) |
| gcode-to-text | error | pass | error | HARNESS-LOOP | 900s timeout, empty exec JSON |
| chess-best-move | pass | error | pass | HARNESS-LOOP | same empty JSON |
| others (dna-insert, overfull-hbox, …) | mixed | mixed | mixed | mostly HARNESS-TOOL / CAPABILITY | |

**i1 target:** live task image + bash docker-exec + `--sandbox=off`. Does not change the Harbor grader (fresh container + workspace copy).

## SWE-bench (manual override)

`classify.py` labels many misses HARNESS-TOOL because a `webfetch`/`websearch` or host `python3` hit `network_denied`. Those are **incidental**: every pair-1/2 miss produced an applied patch and used `eval-test`.

Always-unresolved (3/3): 14 instances (seaborn 2, pytest 4, sklearn 3, xarray 2, pylint 1, mpl 1, sympy 1). Primary bucket **CAPABILITY**. Secondary: glob/grep result caps, not bash 16KB.

Do not spend iterations on SWE prompts until a concrete harness bug is isolated. SWE 2σ is only +2/60, so a real tool fix could still accept.

## Iteration order

1. TB live-image bash (in tree)
2. exec JSON on timeout (gcode/chess)
3. Only then consider bash head+tail / glob `**/` if SWE traces show it
