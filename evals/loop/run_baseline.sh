#!/usr/bin/env bash
# Phase 2: three unchanged DEV runs. Uses existing strike eval via run_parallel.py.
set -euo pipefail
export PYTHONUNBUFFERED=1
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
PY="$ROOT/evals/loop/run_parallel.py"
IDS_SWE="$ROOT/evals/loop/splits/swe-dev.txt"
IDS_TB="$ROOT/evals/loop/splits/tb-dev.txt"
JOBS_SWE="${JOBS_SWE:-4}"
JOBS_TB="${JOBS_TB:-2}"

run_pair() {
  local n="$1"
  local swe_out="$ROOT/evals/loop/results/baseline-swe-dev-$n"
  local tb_out="$ROOT/evals/loop/results/baseline-tb-dev-$n"
  echo "=== baseline pair $n SWE jobs=$JOBS_SWE TB jobs=$JOBS_TB ==="
  python3 "$PY" --bench swebench --ids "$IDS_SWE" --out "$swe_out" --run-id "baseline-swe-dev-$n" --jobs "$JOBS_SWE" &
  local p1=$!
  python3 "$PY" --bench tbench --ids "$IDS_TB" --out "$tb_out" --run-id "baseline-tb-dev-$n" --jobs "$JOBS_TB" &
  local p2=$!
  wait "$p1"
  local e1=$?
  wait "$p2"
  local e2=$?
  echo "=== pair $n done swe_exit=$e1 tb_exit=$e2 ==="
  python3 "$ROOT/evals/loop/spend.py" || true
  return 0
}

for n in 1 2 3; do
  run_pair "$n"
done
python3 "$ROOT/evals/loop/summarize_baseline.py"
echo "baseline complete"
