#!/usr/bin/env bash
# After pair-1 resume finishes, run pair 2 and 3. Do not start until both
# pair-1 reports have 60/25 attempted with no materialize errors.
set -euo pipefail
export PYTHONUNBUFFERED=1
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
PY="$ROOT/evals/loop/run_parallel.py"
SWE_IDS="$ROOT/evals/loop/splits/swe-dev.txt"
TB_IDS="$ROOT/evals/loop/splits/tb-dev.txt"

pair1_ready() {
  python3 - <<'PY'
import json
from pathlib import Path
swe=Path("/home/jonathan/Projects/strike-cli/evals/loop/results/baseline-swe-dev-1/instances")
tb=Path("/home/jonathan/Projects/strike-cli/evals/loop/results/baseline-tb-dev-1/instances")
ns=len(list(swe.glob("*/report.json"))) if swe.exists() else 0
nt=len(list(tb.glob("*/report.json"))) if tb.exists() else 0
errs=0
for p in list(swe.glob("*/report.json"))+list(tb.glob("*/report.json")):
    r=json.loads(p.read_text())
    row=(r.get("results") or [{}])[0]
    if row.get("status")=="error" and "pull" in (row.get("error") or "").lower():
        errs+=1
print(f"swe={ns} tb={nt} pull_errs={errs}")
raise SystemExit(0 if ns>=60 and nt>=25 and errs==0 else 1)
PY
}

echo "waiting for pair-1 resume..."
while ! pair1_ready; do
  sleep 60
done
echo "pair 1 complete; starting pairs 2 and 3"

run_pair() {
  local n="$1"
  python3 "$PY" --bench swebench --ids "$SWE_IDS" --out "$ROOT/evals/loop/results/baseline-swe-dev-$n" --run-id "baseline-swe-dev-$n" --jobs 2 &
  local p1=$!
  python3 "$PY" --bench tbench --ids "$TB_IDS" --out "$ROOT/evals/loop/results/baseline-tb-dev-$n" --run-id "baseline-tb-dev-$n" --jobs 2 &
  local p2=$!
  wait "$p1"
  wait "$p2"
  echo "=== pair $n done ==="
}

run_pair 2
run_pair 3
python3 "$ROOT/evals/loop/summarize_baseline.py"
echo "baseline complete"
