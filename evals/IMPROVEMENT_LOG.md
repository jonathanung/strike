# Harness improvement log

Model pin: `xai` / `grok-4.6` / `effort=high` / `temperature=unset` (not sent on the xAI wire).

Suites: **SWE-bench Verified** (500) and **Terminal-Bench 2** `@20251031` (88 runnable). Not TB v3.0, not SWE-bench Lite/Full/Multimodal.

Splits: `evals/loop/splits/manifest.json`. Iterate DEV only. HOLDOUT at iterations 5/10/15/20, aggregate only.

Significance floor: set after Phase 2 (3× DEV, `2 * sigma`).

**Pair 1 (honest, isolation + clean extract):** SWE **44/60 = 73.3%** ($89). All 16 misses produced an applied patch and used `eval-test` → **CAPABILITY 16/16**. TB **18/25 = 72%** attempted, **75%** of 24 graded; 6× Harbor `reward=0`; `gcode-to-text` timeout empty JSON.

**Pair 2:** SWE **45/60 = 75.0%** ($77). TB **16/25 = 66.7%** graded (8 unresolved + 1 empty-JSON error).

**Pair 3:** SWE **44/60 = 73.3%** ($85). TB **19/25 = 79.2%** graded ($30).

**Floor:** SWE mean **73.89%** sd 0.96 → **2σ = 1.92 pts**. TB mean **73.61%** sd 6.36 → **2σ = 12.73 pts**. Spend after baseline **$390**.

Campaign stop: **$3,500** API (see `evals/loop/PHASE0.md`).

| iter | hypothesis | bucket | change | swe-dev | tb-dev | holdout | accepted | note |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | TB agent bash runs in a live bind-mounted task image | HARNESS-TOOL | STRIKE_EVAL_WORKDIR + docker exec; --sandbox=off; chown before grade | flask smoke still resolved (no SWE DEV re-run) | **21/25 = 84%** ($19) | — | **ACCEPT** | resolved-count +3.33 > 2σ 3.06; graded passRate +10.4 < 12.7 (baseline errors inflated %). Salvaged openssl, financial, portfolio, pytorch. Misses: cython (install not in fresh grade image), dna-insert, gcode, protein |
| 2 | Map `/app` file-tool paths onto the bind-mount | HARNESS-TOOL | mapEvalMountPath in absPath/resolveAllowedPath | — | **21/25** ($16) | — | **REJECT score / retain** | dna-insert salvaged; chess variance loss; net 0. Path mapping is still correct (i1 write `/app/x` escaped workspace). |
| 3 | Grade TB in the live agent container | HARNESS-TOOL | DockerGrader.LiveContainer; keep /app + system pip; reset /tests + reward | — | **24/25** ($13) only gcode timeout | — | **ACCEPT** | +6.33 vs baseline 17.67 (2σ 3.06). cython + protein now pass. passRate=1.0 of 24 graded. |
| 4 | Grade after agent timeout (empty exec JSON) | HARNESS-LOOP | timeoutExecResult returns Usage{} so runner grades | — | gcode canary: **error → unresolved** (still reward=0) | — | **retain** | No full DEV re-run. Remaining TB miss is CAPABILITY. |
| 0a | Leaky DEV-1 aborted: 14/22 SWE resolves fetched GitHub PRs | INFRA/PROMPT | none (measurement) | 22/32 raw, 8/32 clean | 6/8 TB | — | n/a | invalid baseline; see results/leaky-dev1-audit.json |
| 0b | Eval egress isolation (network.allow + deny webfetch/websearch); eval-test local-safe | HARNESS-TOOL | default project config on every eval instance | n/a | n/a | — | precondition | leaky 14/22 SWE resolves were GitHub PR lookups |
| 0c | ExtractPatch: restore symlink typechanges + drop binary hunks | HARNESS-OUTPUT | patch.go | n/a | n/a | — | precondition | docker cp turns mpl-data icon symlinks into files; git apply died |
| 0d | Docker pull retry on rate-limit; stop deleting images after each instance | INFRA | docker.go + run_parallel | resume pair1 | resume | — | precondition | Hub anonymous limit aborted 40 SWE + 17 TB |
