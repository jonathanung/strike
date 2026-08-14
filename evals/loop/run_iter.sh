#!/usr/bin/env bash
# Launch one scored DEV (or HOLDOUT) suite with the current ./strike binary.
# Usage: run_iter.sh <run-id> swebench|tbench <ids-file> [jobs]
set -euo pipefail
export PYTHONUNBUFFERED=1
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
RUN_ID="${1:?run-id}"
BENCH="${2:?swebench|tbench}"
IDS="${3:?ids file}"
JOBS="${4:-2}"
OUT="$ROOT/evals/loop/results/$RUN_ID"
mkdir -p "$OUT"
exec python3 "$ROOT/evals/loop/run_parallel.py" \
  --bench "$BENCH" \
  --ids "$IDS" \
  --out "$OUT" \
  --run-id "$RUN_ID" \
  --jobs "$JOBS"
